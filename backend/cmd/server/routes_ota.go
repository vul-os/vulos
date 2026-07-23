package main

// routes_ota.go — box-side OTA (over-the-air OS update) client HTTP surface.
//
// This is VERIFY-ONLY and OPT-IN, matching services/ota's package doc:
//
//   - The background poll loop (started below) only ever calls
//     ota.Client.Check — it fetches and cryptographically verifies the
//     channel's manifest against the baked release-signing trust chain
//     (services/signing). It NEVER downloads the OS image and NEVER touches
//     the A/B slots.
//   - POST /api/os/update/stage is the only path that writes anything, and it
//     only ever runs in direct response to the box owner clicking "Download &
//     stage update" in Settings → OS Update — nothing in this file calls
//     Stage on its own.
//   - A NEW security update (manifest.is_security, transitioning to a version
//     this box has never notified about) fires exactly ONE priority
//     notification to the owner via services/notify — see onSecurityCheck's
//     anti-spam guard below. Non-security updates are visible in the status
//     endpoint but do not push a notification.
//
// Routes registered:
//
//	GET  /api/os/update/status — the last verified ota.UpdateStatus (owner/
//	                              admin only). Serves the cached result of the
//	                              background poll — no network call per request.
//	POST /api/os/update/stage  — opt-in: verify fresh, then stage the update
//	                              to the INACTIVE A/B slot (owner + step-up).
//	                              Hardware-gated: on a non-image install this
//	                              cleanly refuses (409) rather than writing
//	                              anything.
//
// Ownership/step-up follow the same pattern as every other privileged mutation
// route in this package (see routes_stepup.go, routes_kms.go): the verified
// X-User-ID is read from the auth-stamped header, gated to the admin role, and
// POST additionally requires a fresh stepup.Require(r) elevation token — a
// stage-and-reboot is exactly the kind of destructive host action step-up
// exists for.

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"sync"

	"vulos/backend/internal/datadir"
	"vulos/backend/services/auth"
	"vulos/backend/services/notify"
	"vulos/backend/services/osdist"
	"vulos/backend/services/ota"
	"vulos/backend/services/signing"
	"vulos/backend/services/stepup"
)

// registerOTARoutes wires the verify-only, opt-in OTA client onto mux and
// starts its background verify-only poll loop (never auto-stages).
//
// Returns the *ota.Client so main.go's shutdown path can call StopPolling();
// returns nil when the client could not be constructed (e.g. no writable
// epoch-floor store), in which case both routes still serve a clean 503
// rather than being left unmounted.
func registerOTARoutes(mux *http.ServeMux, notifySvc *notify.Service, authStore *auth.Store) *ota.Client {
	// The persistent rollback floor (services/signing.EpochStore) is required —
	// verify-only fail-closed means we refuse to run at all rather than check
	// manifests with no rollback defence.
	epochStore, err := signing.NewEpochStore(signing.DefaultEpochPath)
	if err != nil {
		log.Printf("[ota] epoch store unavailable (%v) — OTA routes disabled", err)
		registerOTAUnavailableRoutes(mux)
		return nil
	}

	// Reuse the SAME cache directory the existing osdist SlotManager uses
	// (main.go's "OS update status + apply" block) — boot-state.json is real
	// physical A/B-slot bookkeeping for this one machine; a second
	// osdist.SlotManager value pointed at the same path is safe (every
	// mutation is an atomic write-then-rename), so no coordination with that
	// block is required. Staging is simply disabled (not the whole client)
	// if this fails — status/notification keep working.
	slotMgr, slotErr := osdist.NewSlotManager(datadir.Join("os-cache"))
	if slotErr != nil {
		log.Printf("[ota] slot manager unavailable (%v) — staging disabled, status/poll still active", slotErr)
		slotMgr = nil
	}

	client, err := ota.NewClient(ota.ClientConfig{
		EpochStore:     epochStore,
		SlotManager:    slotMgr,
		RunningVersion: os.Getenv("VULOS_OS_VERSION"),
	})
	if err != nil {
		log.Printf("[ota] client init failed (%v) — OTA routes disabled", err)
		registerOTAUnavailableRoutes(mux)
		return nil
	}

	// The box's single owner (first admin, by convention) is who OS-update
	// notifications and mutations are scoped to — same resolution telephony
	// uses for its owner-targeted SMS/call alerts.
	ownerID := func() string {
		if authStore == nil {
			return ""
		}
		return authStore.AdminUserID()
	}
	isOwnerOrAdmin := func(r *http.Request) bool {
		if authStore == nil {
			return false
		}
		p, _ := authStore.GetProfile(r.Header.Get("X-User-ID"))
		return p != nil && p.Role == auth.RoleAdmin
	}

	onCheck := onSecurityCheck(notifySvc, ownerID)

	mux.HandleFunc("GET /api/os/update/status", func(w http.ResponseWriter, r *http.Request) {
		if !isOwnerOrAdmin(r) {
			writeErr(w, http.StatusForbidden, "admin only")
			return
		}
		writeJSON(w, client.Status())
	})

	mux.HandleFunc("POST /api/os/update/stage", func(w http.ResponseWriter, r *http.Request) {
		if !isOwnerOrAdmin(r) {
			writeErr(w, http.StatusForbidden, "admin only")
			return
		}
		// SEC: staging writes a verified image into the inactive A/B slot and
		// marks it pending — a privileged, destructive-adjacent host mutation
		// (the next reboot boots it). Same step-up bar as routes_kms.go.
		if err := stepup.Require(r); err != nil {
			writeErr(w, http.StatusForbidden, "step-up required: "+err.Error())
			return
		}

		result, err := client.Stage(r.Context())
		if err != nil {
			writeErr(w, otaStageStatus(err), err.Error())
			return
		}
		writeJSON(w, result)
	})

	// Background poll loop: verify-only, never stages. StartPolling runs the
	// first check immediately so /status has real data shortly after boot.
	client.StartPolling(context.Background(), ota.DefaultPollInterval, onCheck)
	log.Printf("[ota] update client started (channel=%s, running=%s)", client.ChannelURL(), os.Getenv("VULOS_OS_VERSION"))

	return client
}

// otaStageStatus maps a Stage error to the HTTP status the frontend should
// treat as a clean, expected refusal (409) versus an unexpected upstream
// failure (502) or missing configuration (503).
func otaStageStatus(err error) int {
	switch {
	case errors.Is(err, ota.ErrAlreadyUpToDate), errors.Is(err, ota.ErrNotImageInstall):
		return http.StatusConflict
	case errors.Is(err, ota.ErrStagingNotConfigured):
		return http.StatusServiceUnavailable
	default:
		return http.StatusBadGateway
	}
}

// onSecurityCheck builds the ota.Client's onCheck callback: it fires exactly
// ONE priority notification to the owner per NEW security version. The guard
// is a simple last-notified-version transition, kept in-memory here (not in
// services/ota, which stays notification-agnostic) — a poll that repeats the
// same already-notified version, or one that finds no update / a non-security
// update, never re-fires.
func onSecurityCheck(notifySvc *notify.Service, ownerID func() string) func(ota.UpdateStatus) {
	var mu sync.Mutex
	lastNotified := ""

	return func(status ota.UpdateStatus) {
		if !status.Available || !status.IsSecurity || status.LatestVersion == "" {
			return
		}

		mu.Lock()
		already := status.LatestVersion == lastNotified
		if !already {
			lastNotified = status.LatestVersion
		}
		mu.Unlock()
		if already {
			return
		}

		if notifySvc == nil {
			return
		}
		owner := ""
		if ownerID != nil {
			owner = ownerID()
		}
		if owner == "" {
			return
		}

		body := fmt.Sprintf("Security update %s is available for your Vulos box. Review and stage it from Settings → OS Update.", status.LatestVersion)
		if status.Notes != "" {
			body = fmt.Sprintf("Security update %s: %s", status.LatestVersion, status.Notes)
		}

		notifySvc.SendNotification(notify.Notification{
			Title:  "Security update available",
			Body:   body,
			Level:  notify.LevelUrgent, // highest achievable priority for a non-call alert (PriorityHigh)
			Source: "os-update",
			Type:   notify.TypeAlert,
			UserID: owner, // targeted → web-pushed to the owner even with the app closed
		})
	}
}

// registerOTAUnavailableRoutes mounts clean 503 handlers so the frontend gets
// a predictable JSON error instead of a 404 when the OTA client could not be
// constructed (e.g. no writable epoch-floor store in a degraded environment).
func registerOTAUnavailableRoutes(mux *http.ServeMux) {
	unavailable := func(w http.ResponseWriter, r *http.Request) {
		writeErr(w, http.StatusServiceUnavailable, "OS update service unavailable")
	}
	mux.HandleFunc("GET /api/os/update/status", unavailable)
	mux.HandleFunc("POST /api/os/update/stage", unavailable)
}
