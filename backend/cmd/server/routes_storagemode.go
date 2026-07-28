package main

// routes_storagemode.go — STORE-LOCAL-01: HTTP surface for the bundle
// storage-mode selector. Since D-STORE-LOCAL-DEFAULT the default is local-fs
// (this box's own disk); local-minio-sync and the hosted central-tigris are
// both explicit opt-ins.
//
// The wizard step (firstboot) and the dashboard settings panel both POST
// here. The handler validates the request, persists via
// internal/storagemode, and returns the current Config (no credentials —
// only the creds_ref pointer).

import (
	"encoding/json"
	"log"
	"net/http"
	"path/filepath"

	"vulos/backend/internal/storagemode"
	"vulos/backend/services/auth"
)

// registerStorageModeRoutes wires the storage-mode HTTP endpoints into mux.
// home is the user's home directory; the SQLite store lives at
// $home/.vulos/db/storagemode.db.
//
// authStore is used to gate writes behind admin role — reading the current
// mode is allowed for any authenticated user because the dashboard panel
// renders it for everyone, but only admins may flip the selector.
func registerStorageModeRoutes(mux *http.ServeMux, home string, authStore *auth.Store) {
	dbPath := filepath.Join(home, "db", "storagemode.db")
	store, err := storagemode.Open(dbPath)
	if err != nil {
		// FIX-STORE-LOCAL-LOG-01: surface the open failure. Soft-degrade
		// behaviour (reads return Defaults(), writes 500) is intentional so
		// the server still boots on a misconfigured host — but the operator
		// needs to see WHY they're stuck on tigris-only.
		// The degraded default is resolved from the SAME evidence the store
		// uses (a legacy storage.yaml naming a hosted backend keeps reading
		// back as hosted), so a broken store never misreports a Tigris box as
		// local — which would be a lie about where that operator's bytes are.
		degraded, why := storagemode.EffectiveDefault("")
		log.Printf("[storagemode] store unavailable at %s: %v — soft-degrading to %q (%s); writes will 500",
			dbPath, err, degraded, why)
		mux.HandleFunc("GET /api/storagemode", func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, storagemode.Config{Mode: degraded})
		})
		mux.HandleFunc("PUT /api/storagemode", func(w http.ResponseWriter, r *http.Request) {
			// FIX-STORE-LOCAL-LOG-01: also log on the 500 path so each rejected
			// write produces a server-side breadcrumb (the client sees the
			// generic error message; the log line has the underlying detail).
			log.Printf("[storagemode] PUT rejected — store unavailable at %s: %v", dbPath, err)
			writeErr(w, 500, "storagemode store unavailable: "+err.Error())
		})
		return
	}

	mux.HandleFunc("GET /api/storagemode", func(w http.ResponseWriter, r *http.Request) {
		cfg, err := store.Get()
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, cfg)
	})

	mux.HandleFunc("PUT /api/storagemode", func(w http.ResponseWriter, r *http.Request) {
		// Admin-only — flipping the bundle storage mode at runtime is a
		// privileged operation. The setup wizard runs before any account
		// exists, so we also allow the request when no users are registered
		// yet (first-boot path).
		userID := r.Header.Get("X-User-ID")
		if authStore != nil && authStore.HasAnyUsers() {
			p, _ := authStore.GetProfile(userID)
			if p == nil || p.Role != auth.RoleAdmin {
				writeErr(w, 403, "admin only")
				return
			}
		}

		var req storagemode.Config
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
			writeErr(w, 400, "invalid request body")
			return
		}
		if err := store.Set(req); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		if req.Mode.Hosted() {
			// Selecting a hosted backend is a posture change (the box's bytes
			// start leaving it). Leave a breadcrumb naming who did it.
			log.Printf("[storagemode] mode set to %q — HOSTED third-party storage, selected explicitly by user %q",
				req.Mode, userID)
		}
		cfg, err := store.Get()
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, cfg)
	})
}
