package main

// routes_notify_push.go — PUSH-CELL-01: cell-side Web Push subscribe surface +
// wiring of the direct send-path into the notify Service.
//
// Endpoints (all under the authed API prefix, so X-User-ID is stamped by the
// auth Middleware):
//
//	GET    /api/notifications/push/vapid-public → { enabled, publicKey? }
//	POST   /api/notifications/push/subscribe    → store a PushSubscription (per-owner)
//	DELETE /api/notifications/push/subscribe     → remove one subscription by endpoint
//
// The subscription is ALWAYS keyed on the VERIFIED caller (X-User-ID); the
// request body carries no owner id, so a client can never register, overwrite,
// or delete another user's subscription. When Web Push is not configured (no
// VAPID keys), vapid-public reports enabled:false and the subscribe/unsubscribe
// routes 503 — fail-safe-off, no key material leaked.
//
// The VAPID PRIVATE key is never returned by any route and never logged.

import (
	"encoding/json"
	"log"
	"net/http"
	"path/filepath"

	"vulos/backend/services/notify"
)

// registerNotifyPushRoutes resolves/loads the VAPID config, opens the
// per-owner push subscription store, attaches the direct send-path to notifySvc
// (respecting the DND policy carried by dnd), and serves the subscribe surface.
//
// It is best-effort and FLAG-GATED: if VAPID is not configured the store is
// still opened (so a later key config would work after a restart) but sends are
// inert; a store-open failure is logged and the server continues without push.
func registerNotifyPushRoutes(mux *http.ServeMux, notifySvc *notify.Service, home string, dnd *notify.DNDManager) {
	cfg := notify.LoadPushConfig()
	// Default the key file to a stable box-local path when the operator did not
	// pin explicit keys, so a self-host box auto-provisions a persistent VAPID
	// pair on first boot (0600). This makes the sovereign direct-send path work
	// out of the box while staying overridable by env.
	if cfg.VAPIDPublic == "" && cfg.VAPIDPrivate == "" && cfg.VAPIDKeyFile == "" {
		cfg.VAPIDKeyFile = filepath.Join(home, ".vulos", "db", "vapid.json")
	}
	if err := notify.ResolvePushVAPID(&cfg); err != nil {
		// A key-file we cannot read/write → run WITHOUT push rather than fail boot.
		log.Printf("[notify] push: VAPID unavailable (push disabled): %v", err)
		cfg = notify.LoadPushConfig() // discard the half-resolved config
	}

	storePath := filepath.Join(home, ".vulos", "db", "push_subs.sqlite")
	store, err := notify.NewSQLitePushStore(storePath)
	if err != nil {
		log.Printf("[notify] push: subscription store unavailable (push disabled): %v", err)
		return
	}

	// Attach the send-path. Suppress via the SAME DND policy that governs the
	// in-app surface, so a user in Do-Not-Disturb is not web-pushed either.
	notifySvc.SetPush(store, cfg, func(_ string, level notify.Level, priority notify.Priority) bool {
		if dnd == nil {
			return false
		}
		return dnd.Suppressed(level, priority)
	})

	if cfg.Enabled() {
		log.Printf("[notify] push: cell-side Web Push ENABLED (store %s)", storePath)
	} else {
		log.Printf("[notify] push: subscription store ready; Web Push DISABLED (no VAPID keys)")
	}

	// GET vapid-public — the PUBLIC key the browser passes to
	// PushManager.subscribe as applicationServerKey. The public key is not a
	// secret; the private key is NEVER returned.
	mux.HandleFunc("GET /api/notifications/push/vapid-public", func(w http.ResponseWriter, r *http.Request) {
		if !cfg.Enabled() {
			writeJSON(w, map[string]any{"enabled": false})
			return
		}
		writeJSON(w, map[string]any{"enabled": true, "publicKey": cfg.VAPIDPublic})
	})

	// POST subscribe — body is the browser's PushSubscription.toJSON():
	//   { "endpoint": "...", "keys": { "p256dh": "...", "auth": "..." } }
	// The owner is stamped from X-User-ID; the body cannot override it.
	mux.HandleFunc("POST /api/notifications/push/subscribe", func(w http.ResponseWriter, r *http.Request) {
		owner := r.Header.Get("X-User-ID")
		if owner == "" {
			writeErr(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		if !cfg.Enabled() {
			writeErr(w, http.StatusServiceUnavailable, "push not configured")
			return
		}
		var body struct {
			Endpoint string `json:"endpoint"`
			Keys     struct {
				P256DH string `json:"p256dh"`
				Auth   string `json:"auth"`
			} `json:"keys"`
		}
		// Bound the body so an oversized blob cannot be streamed in.
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&body); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid subscription body")
			return
		}
		sub := notify.PushSubscription{
			OwnerID:  owner, // authoritative; body cannot override
			Endpoint: body.Endpoint,
			P256DH:   body.Keys.P256DH,
			Auth:     body.Keys.Auth,
		}
		if err := notify.ValidateSubscription(sub); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := store.Save(owner, sub); err != nil {
			// Do not surface internal error detail.
			log.Printf("[notify] push: save subscription failed: %v", err)
			writeErr(w, http.StatusInternalServerError, "could not store subscription")
			return
		}
		writeJSON(w, map[string]string{"status": "subscribed"})
	})

	// DELETE subscribe — body: { "endpoint": "..." }. Idempotent; scoped to the
	// caller so one user can never delete another's subscription.
	mux.HandleFunc("DELETE /api/notifications/push/subscribe", func(w http.ResponseWriter, r *http.Request) {
		owner := r.Header.Get("X-User-ID")
		if owner == "" {
			writeErr(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		var body struct {
			Endpoint string `json:"endpoint"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&body); err != nil || body.Endpoint == "" {
			writeErr(w, http.StatusBadRequest, "endpoint required")
			return
		}
		if err := store.Delete(owner, body.Endpoint); err != nil {
			log.Printf("[notify] push: delete subscription failed: %v", err)
			writeErr(w, http.StatusInternalServerError, "could not remove subscription")
			return
		}
		writeJSON(w, map[string]string{"status": "unsubscribed"})
	})
}
