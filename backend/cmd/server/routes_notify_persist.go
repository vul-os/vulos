package main

// routes_notify_persist.go — NOTIF-02: persistent notification store wiring.
//
// Opens the on-disk notification store, attaches it to the notify Service so
// SendNotification additively persists, and exposes read/prune endpoints.
// DND and inline-action endpoints are NOT here — those belong to NOTIF-05/06
// (routes_notify.go).

import (
	"encoding/json"
	"log"
	"net/http"
	"path/filepath"
	"strconv"
	"time"

	"vulos/backend/services/notify"
)

// registerNotifyPersistRoutes opens the persistent store at
// ~/.vulos/db/notifications.json, attaches it to notifySvc, and serves:
//
//	GET  /api/notifications/persist?limit=N  → persisted history (newest first)
//	POST /api/notifications/prune            → {max_n?, max_age_hours?}
//
// home is os.UserHomeDir(). A store-open failure is logged and the server
// continues memory-only (persistence is best-effort, never fatal).
func registerNotifyPersistRoutes(mux *http.ServeMux, notifySvc *notify.Service, home string) {
	storePath := filepath.Join(home, "db", "notifications.json")

	store, err := notify.OpenStore(storePath)
	if err != nil {
		log.Printf("[notify] persistent store unavailable (memory-only): %v", err)
		return
	}
	notifySvc.SetStore(store)
	log.Printf("[notify] persistent store: %s (%d entries)", storePath, store.Len())

	// GET /api/notifications/persist?limit=N — persisted history, newest first.
	mux.HandleFunc("GET /api/notifications/persist", func(w http.ResponseWriter, r *http.Request) {
		limit := 0
		if q := r.URL.Query().Get("limit"); q != "" {
			if v, convErr := strconv.Atoi(q); convErr == nil {
				limit = v
			}
		}
		// User-scoped read: box-level notifications + this user's own private ones
		// only, so the persisted history cannot leak another account's private
		// notification (e.g. a reminder's text) (NOTIF-USER-SCOPE).
		writeJSON(w, store.ListForUser(r.Header.Get("X-User-ID"), limit))
	})

	// POST /api/notifications/prune — manual retention trigger.
	// Body: {"max_n": 500, "max_age_hours": 720}. Omitted/zero fields fall
	// back to the store defaults (cap 500, age 30d).
	//
	// M7: requires an authenticated user.
	mux.HandleFunc("POST /api/notifications/prune", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-User-ID") == "" {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		var req struct {
			MaxN        int `json:"max_n"`
			MaxAgeHours int `json:"max_age_hours"`
		}
		// An empty / malformed body is tolerated → fall back to defaults.
		_ = json.NewDecoder(r.Body).Decode(&req)

		maxN := req.MaxN
		if maxN <= 0 {
			maxN = 500
		}
		maxAge := time.Duration(req.MaxAgeHours) * time.Hour
		if maxAge <= 0 {
			maxAge = 30 * 24 * time.Hour
		}

		store.Prune(maxN, maxAge)
		writeJSON(w, map[string]int{"remaining": store.Len()})
	})
}
