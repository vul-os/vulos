package main

// routes_notify_readclear.go — NOTIF-USER-SCOPE-02.
//
// POST /api/notifications/read and POST /api/notifications/clear used to be
// registered inline in main(), which made them untestable: a scope test had to
// build its OWN mux with a COPY of the handler, so it verified a
// reimplementation and could not fail when main() regressed. That was proven,
// not assumed — reverting main() to the unscoped MarkAllRead/MarkRead/Clear
// left every one of those tests green.
//
// Extracting them here matches what the sibling notification surfaces already
// do (routes_notify_persist.go, routes_notify_push.go,
// routes_notify_unifiedpush.go) and lets the test drive the SAME function
// main() calls, so a regression in the shipped route fails the suite.
//
// The scoping itself is the security property: the unscoped Service.MarkRead /
// MarkAllRead / Clear operate across every account on the box. Wired to an
// authenticated-but-unprivileged route they let any profile flip the read state
// of another account's PRIVATE notification by supplying its id, silence every
// account's unread badges at once with an empty id, or wipe everyone's history.
// The *ForUser variants only touch what is visible to the caller — box-level
// notifications plus their own private ones.

import (
	"encoding/json"
	"net/http"

	"vulos/backend/services/notify"
)

// registerNotifyReadClearRoutes wires the caller-scoped read/clear surface.
func registerNotifyReadClearRoutes(mux *http.ServeMux, notifySvc *notify.Service) {
	mux.HandleFunc("POST /api/notifications/read", func(w http.ResponseWriter, r *http.Request) {
		userID := r.Header.Get("X-User-ID")
		if userID == "" {
			writeErr(w, 401, "unauthorized")
			return
		}
		var req struct {
			ID string `json:"id"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		if req.ID == "" {
			notifySvc.MarkAllReadForUser(userID)
		} else {
			notifySvc.MarkReadForUser(req.ID, userID)
		}
		writeJSON(w, map[string]string{"status": "ok"})
	})

	mux.HandleFunc("POST /api/notifications/clear", func(w http.ResponseWriter, r *http.Request) {
		userID := r.Header.Get("X-User-ID")
		if userID == "" {
			writeErr(w, 401, "unauthorized")
			return
		}
		notifySvc.ClearForUser(userID)
		writeJSON(w, map[string]string{"status": "cleared"})
	})
}
