package main

// routes_instances_manage.go — fleet mutation surface for the dashboard's
// Instances panel (MINST-07).
//
// Routes registered:
//
//	PATCH  /api/instances/{ulid}/rename — set an instance's display name
//	DELETE /api/instances/{ulid}        — remove an instance from the fleet
//
// The panel has always called both; neither was registered, so the SPA
// catch-all answered them and the UI's `if (!r.ok)` never fired: "Remove"
// dropped the instance from the rendered list while the registry kept it.
//
// Both are ADMIN-ONLY. The registry is account-wide fleet state (it drives
// routing and CRDT sync peers, not just a view), so mutating it is gated the
// same way as the other fleet operations issued from the console
// (/api/cluster/join-code, backups, app versions). Authentication itself is
// stamped by auth.Handler.Middleware, which strips any client-supplied
// X-User-ID before the request reaches here.

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"vulos/backend/internal/multiinstance"
	"vulos/backend/services/auth"
)

// registerInstanceManageRoutes wires the rename/remove routes onto mux against
// the shared registry. authStore may be nil in degraded boot (no auth store);
// the routes then fail closed (403) rather than mutate the fleet unauthorized.
func registerInstanceManageRoutes(mux *http.ServeMux, reg *multiinstance.Registry, authStore *auth.Store) {
	mux.HandleFunc("PATCH /api/instances/{ulid}/rename", func(w http.ResponseWriter, r *http.Request) {
		if !instRequireAdmin(w, r, authStore) {
			return
		}
		var body struct {
			DisplayName string `json:"display_name"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid JSON body")
			return
		}

		inst, err := reg.Rename(r.PathValue("ulid"), body.DisplayName)
		switch {
		case errors.Is(err, multiinstance.ErrNotFound):
			writeErr(w, http.StatusNotFound, "instance not found")
			return
		case err != nil:
			// A validation failure is the caller's fault (empty / too long /
			// control chars); the registry reports it as a plain error.
			writeErr(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		writeJSON(w, inst)
	})

	mux.HandleFunc("DELETE /api/instances/{ulid}", func(w http.ResponseWriter, r *http.Request) {
		if !instRequireAdmin(w, r, authStore) {
			return
		}

		ulid := r.PathValue("ulid")
		err := reg.Delete(ulid)
		switch {
		case errors.Is(err, multiinstance.ErrNotFound):
			writeErr(w, http.StatusNotFound, "instance not found")
			return
		case errors.Is(err, multiinstance.ErrIsOwner):
			writeErr(w, http.StatusConflict, "cannot remove this instance — it is the box you are signed in to")
			return
		case err != nil:
			log.Printf("[multiinstance/manage] delete %s: %v", ulid, err)
			writeErr(w, http.StatusInternalServerError, "could not remove instance")
			return
		}
		writeJSON(w, map[string]string{"status": "removed", "ulid": ulid})
	})
}

// instRequireAdmin authorises a fleet mutation. A nil auth store (degraded
// boot) is treated as "no admin exists" and refuses — never as "everyone is
// admin".
func instRequireAdmin(w http.ResponseWriter, r *http.Request, authStore *auth.Store) bool {
	userID := r.Header.Get("X-User-ID")
	if userID == "" {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return false
	}
	if authStore == nil {
		writeErr(w, http.StatusForbidden, "admin only")
		return false
	}
	if p, ok := authStore.GetProfile(userID); !ok || p == nil || p.Role != auth.RoleAdmin {
		writeErr(w, http.StatusForbidden, "admin only")
		return false
	}
	return true
}
