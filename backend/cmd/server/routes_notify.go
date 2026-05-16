// routes_notify.go — NOTIF-05+06 HTTP endpoints.
//
// Registered by registerNotifyExtRoutes (called once from main.go).
// Endpoints:
//
//	GET  /api/notifications/dnd           — current DND status
//	POST /api/notifications/dnd           — set/clear DND
//	POST /api/notifications/{id}/action   — dispatch an inline action
package main

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"time"

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
	dndPath := filepath.Join(home, ".vulos", "db", "dnd.json")
	return &notifyExt{
		svc:     svc,
		dnd:     notify.NewDNDManager(dndPath),
		actions: notify.NewActionRegistry(),
	}
}

// registerNotifyExtRoutes wires the DND and action endpoints into mux.
// Call this exactly once from main(), passing the shared notifySvc and home dir.
func registerNotifyExtRoutes(mux *http.ServeMux, svc *notify.Service, home string) *notifyExt {
	ext := newNotifyExt(svc, home)

	// GET /api/notifications/dnd
	mux.HandleFunc("GET /api/notifications/dnd", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, ext.dnd.Status())
	})

	// POST /api/notifications/dnd
	// Body (all fields optional):
	//   { "mode": "off"|"priority"|"total", "until": "<RFC3339>", "schedule": [...] }
	mux.HandleFunc("POST /api/notifications/dnd", func(w http.ResponseWriter, r *http.Request) {
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
	mux.HandleFunc("POST /api/notifications/{id}/action", func(w http.ResponseWriter, r *http.Request) {
		notifID := r.PathValue("id")
		if notifID == "" {
			writeErr(w, 400, "notification id required")
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

	return ext
}
