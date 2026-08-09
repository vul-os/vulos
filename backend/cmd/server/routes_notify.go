// routes_notify.go — NOTIF-05+06 HTTP endpoints.
//
// Registered by registerNotifyExtRoutes (called once from main.go).
// Endpoints:
//
//	GET  /api/notifications/dnd           — current DND status
//	POST /api/notifications/dnd           — set/clear DND (M7: authenticated user required)
//	POST /api/notifications/{id}/action   — dispatch an inline action (M7: authenticated user required)
package main

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"time"

	"vulos/backend/services/auth"
	"vulos/backend/services/notify"
)

// notifyExt bundles the shared singletons used by the ext routes.
type notifyExt struct {
	svc     *notify.Service
	dnd     *notify.DNDManager
	actions *notify.ActionRegistry
}

// newNotifyExt constructs the DND manager and action registry.
// home is the user's home directory (e.g. os.UserHomeDir()).
func newNotifyExt(svc *notify.Service, home string) *notifyExt {
	dndPath := filepath.Join(home, "db", "dnd.json")
	return &notifyExt{
		svc:     svc,
		dnd:     notify.NewDNDManager(dndPath),
		actions: notify.NewActionRegistry(),
	}
}

// registerNotifyExtRoutes wires the DND and action endpoints into mux.
// Call this exactly once from main(), passing the shared notifySvc, home dir,
// and authStore.
//
// M7 fix: all mutation endpoints (POST) require an authenticated user
// (X-User-ID set by auth Middleware). The auth Middleware already enforces
// this for non-public paths; the explicit check here is belt-and-suspenders.
func registerNotifyExtRoutes(mux *http.ServeMux, svc *notify.Service, home string, authStore *auth.Store) *notifyExt {
	ext := newNotifyExt(svc, home)

	// GET /api/notifications/dnd — read-only, any authenticated user.
	mux.HandleFunc("GET /api/notifications/dnd", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, ext.dnd.Status())
	})

	// POST /api/notifications/dnd
	// Body (all fields optional):
	//   { "mode": "off"|"priority"|"total", "until": "<RFC3339>", "schedule": [...] }
	//
	// M7: requires an authenticated user.
	mux.HandleFunc("POST /api/notifications/dnd", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-User-ID") == "" {
			writeErr(w, 401, "unauthorized")
			return
		}
		var req struct {
			Mode     string                  `json:"mode"`
			Until    string                  `json:"until"` // RFC3339, empty = permanent
			Schedule []notify.ScheduleWindow `json:"schedule"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, 400, "invalid JSON")
			return
		}

		// Update schedule if provided
		if req.Schedule != nil {
			ext.dnd.SetSchedule(req.Schedule)
		}

		// Apply mode change
		if req.Mode != "" {
			mode := notify.DNDMode(req.Mode)
			var until time.Time
			if req.Until != "" {
				var err error
				until, err = time.Parse(time.RFC3339, req.Until)
				if err != nil {
					writeErr(w, 400, "until must be RFC3339")
					return
				}
			}
			if mode == notify.DNDModeOff {
				ext.dnd.Clear()
			} else {
				ext.dnd.Set(mode, until)
			}
		}

		writeJSON(w, ext.dnd.Status())
	})

	// POST /api/notifications/{id}/action
	// Body: { "action_id": "<id>" }
	//
	// Requires an authenticated user AND that the notification is one the caller
	// may act on.
	//
	// NOTIF-USER-SCOPE-03: authentication is NOT authorization. This handler
	// previously checked only that X-User-ID was non-empty and then dispatched on
	// the caller-supplied {id}, so any profile on a shared box could fire the
	// inline action attached to ANOTHER account's private notification (e.g. a
	// reminder) simply by supplying its id — ActionRegistry.Dispatch takes no user
	// id and cannot make that judgement itself. The DeliverableToUser gate below
	// is the missing ownership check; it fails closed on an unknown id and
	// returns the same 404 for "no such notification" and "not yours" so the
	// endpoint is not an existence oracle.
	mux.HandleFunc("POST /api/notifications/{id}/action", func(w http.ResponseWriter, r *http.Request) {
		userID := r.Header.Get("X-User-ID")
		if userID == "" {
			writeErr(w, 401, "unauthorized")
			return
		}
		notifID := r.PathValue("id")
		if notifID == "" {
			writeErr(w, 400, "notification id required")
			return
		}
		if !ext.svc.DeliverableToUser(notifID, userID) {
			writeErr(w, 404, "notification not found")
			return
		}

		var req struct {
			ActionID string `json:"action_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ActionID == "" {
			writeErr(w, 400, "action_id required")
			return
		}

		if err := ext.actions.Dispatch(notifID, req.ActionID); err != nil {
			writeErr(w, 422, err.Error())
			return
		}

		writeJSON(w, map[string]string{"status": "dispatched"})
	})

	// Suppress unused-variable warning: authStore is threaded in for future
	// per-user notification scoping (e.g. when notifySvc gains multi-user support).
	_ = authStore

	return ext
}
