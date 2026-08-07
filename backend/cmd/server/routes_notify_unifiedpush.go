package main

// routes_notify_unifiedpush.go — UP-CELL-01: cell-side UnifiedPush endpoint
// registration surface + wiring of the send-path into the notify Service.
// This is the ALONGSIDE sibling of routes_notify_push.go (Web Push); see its
// header for the shared subscribe-surface rationale.
//
// Endpoints (all under the authed API prefix, so X-User-ID is stamped by the
// auth Middleware; NONE of these are in publicPaths):
//
//	GET    /api/notifications/unifiedpush/status    → { enabled }
//	POST   /api/notifications/unifiedpush/subscribe → store an endpoint URL (per-owner)
//	DELETE /api/notifications/unifiedpush/subscribe → remove one endpoint by URL
//
// The endpoint is ALWAYS keyed on the VERIFIED caller (X-User-ID); the
// request body carries no owner id, so a client can never register, overwrite,
// or delete another user's endpoint. When UnifiedPush is not configured
// (VULOS_PUSH_UNIFIEDPUSH_ENABLE unset), status reports enabled:false and the
// subscribe route 503s — fail-safe-off, no behavior change from before this
// feature existed.
//
// SSRF: a UnifiedPush endpoint is a user-supplied URL the box will later POST
// to outbound. unifiedpush.Validate screens it (loopback/private LAN/
// link-local/metadata denied, reusing backend/internal/safedial) BEFORE it is
// ever stored, and unifiedpush.LiveSender re-screens the resolved IP again at
// send time.

import (
	"encoding/json"
	"log"
	"net/http"
	"path/filepath"

	"vulos/backend/internal/unifiedpush"
	"vulos/backend/services/notify"
)

// registerNotifyUnifiedPushRoutes loads the UnifiedPush config, opens the
// per-owner endpoint store, attaches the send-path to notifySvc (respecting
// the SAME DND policy Web Push uses, via dnd), and serves the subscribe
// surface.
//
// It is best-effort and FLAG-GATED: if VULOS_PUSH_UNIFIEDPUSH_ENABLE is not
// set the store is still opened (so a later config flip works after a
// restart) but sends stay inert; a store-open failure is logged and the
// server continues without UnifiedPush — Web Push and the in-app path are
// entirely unaffected either way.
func registerNotifyUnifiedPushRoutes(mux *http.ServeMux, notifySvc *notify.Service, home string, dnd *notify.DNDManager) {
	cfg := unifiedpush.LoadConfig()

	storePath := filepath.Join(home, "db", "unifiedpush_endpoints.sqlite")
	store, err := unifiedpush.NewSQLiteStore(storePath)
	if err != nil {
		log.Printf("[notify] unifiedpush: endpoint store unavailable (disabled): %v", err)
		return
	}

	// Attach the send-path. Suppress via the SAME DND policy that governs the
	// in-app surface and Web Push, so a user in Do-Not-Disturb is not
	// UnifiedPush'd either — a registered endpoint must never become a way to
	// bypass the checks Web Push already honours.
	notifySvc.SetUnifiedPush(store, cfg, func(_ string, level notify.Level, priority notify.Priority) bool {
		if dnd == nil {
			return false
		}
		return dnd.Suppressed(level, priority)
	})

	if cfg.Enabled {
		log.Printf("[notify] unifiedpush: cell-side UnifiedPush ENABLED (store %s)", storePath)
	} else {
		log.Printf("[notify] unifiedpush: endpoint store ready; UnifiedPush DISABLED (set VULOS_PUSH_UNIFIEDPUSH_ENABLE=1 to opt in)")
	}

	// GET status — reports whether the box has UnifiedPush turned on at all.
	// No key material to leak here (unlike vapid-public); just the flag.
	mux.HandleFunc("GET /api/notifications/unifiedpush/status", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-User-ID") == "" {
			writeErr(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		writeJSON(w, map[string]any{"enabled": cfg.Enabled})
	})

	// POST subscribe — body: { "endpoint": "https://..." }, the URL the
	// user's chosen UnifiedPush distributor handed them. The owner is stamped
	// from X-User-ID; the body cannot override it.
	mux.HandleFunc("POST /api/notifications/unifiedpush/subscribe", func(w http.ResponseWriter, r *http.Request) {
		owner := r.Header.Get("X-User-ID")
		if owner == "" {
			writeErr(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		if !cfg.Enabled {
			writeErr(w, http.StatusServiceUnavailable, "unifiedpush not configured")
			return
		}
		var body struct {
			Endpoint string `json:"endpoint"`
		}
		// Bound the body so an oversized blob cannot be streamed in.
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid endpoint body")
			return
		}
		ep := unifiedpush.Endpoint{
			OwnerID: owner, // authoritative; body cannot override
			URL:     body.Endpoint,
		}
		if err := unifiedpush.Validate(ep); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := store.Save(owner, ep); err != nil {
			// Do not surface internal error detail.
			log.Printf("[notify] unifiedpush: save endpoint failed: %v", err)
			writeErr(w, http.StatusInternalServerError, "could not store endpoint")
			return
		}
		writeJSON(w, map[string]string{"status": "subscribed"})
	})

	// DELETE subscribe — body: { "endpoint": "..." }. Idempotent; scoped to
	// the caller so one user can never delete another's endpoint.
	mux.HandleFunc("DELETE /api/notifications/unifiedpush/subscribe", func(w http.ResponseWriter, r *http.Request) {
		owner := r.Header.Get("X-User-ID")
		if owner == "" {
			writeErr(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		var body struct {
			Endpoint string `json:"endpoint"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil || body.Endpoint == "" {
			writeErr(w, http.StatusBadRequest, "endpoint required")
			return
		}
		if err := store.Delete(owner, body.Endpoint); err != nil {
			log.Printf("[notify] unifiedpush: delete endpoint failed: %v", err)
			writeErr(w, http.StatusInternalServerError, "could not remove endpoint")
			return
		}
		writeJSON(w, map[string]string{"status": "unsubscribed"})
	})
}
