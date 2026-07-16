// multiloc.go — multi-location HTTP routes for the control plane.
//
// Routes registered:
//
//	POST /api/multiloc/enroll          — enroll a new location (session, org-admin)
//	GET  /api/multiloc/locations       — list locations for an org (session, org-admin)
//	POST /api/multiloc/health-report   — location reports its health (session, org-admin)
//
// All routes are session-gated and require the caller to be an org admin.
// Every mutating action is audit-logged via auditRecord.
//
// Task coverage: CP-MULTLOC-01, CP-MULTLOC-02
package cproutes

import (
	"errors"
	"net/http"
	"time"

	"github.com/vul-os/vulos-management/pkg/auth"
	"github.com/vul-os/vulos-management/pkg/fleet"
	"github.com/vul-os/vulos-management/pkg/httpx"
	"github.com/vul-os/vulos-management/pkg/multiloc"
)

// ─────────────────────────────────────────────────────────────────────────────
// RegisterMultiLoc
// ─────────────────────────────────────────────────────────────────────────────

// RegisterMultiLoc wires the multi-location management endpoints into mux.
//
//   - st:        the multiloc.Store (SQLStore in prod, MemStore in tests).
//   - authStore: used for session validation.
//   - fleetSt:   used for org-admin authorization (MemberRole).
func RegisterMultiLoc(
	mux *http.ServeMux,
	st multiloc.Store,
	authStore *auth.Store,
	fleetSt fleet.Store,
) {
	h := &multiLocHandlers{store: st, auth: authStore, fleet: fleetSt}

	mux.HandleFunc("POST /api/multiloc/enroll", h.enroll)
	mux.HandleFunc("GET /api/multiloc/locations", h.list)
	mux.HandleFunc("POST /api/multiloc/health-report", h.healthReport)
}

// ─────────────────────────────────────────────────────────────────────────────
// multiLocHandlers
// ─────────────────────────────────────────────────────────────────────────────

type multiLocHandlers struct {
	store multiloc.Store
	auth  *auth.Store
	fleet fleet.Store
}

// requireOrgAdmin validates the session and checks that the caller is an admin
// or owner of orgID.  Returns the user on success, nil on failure (response
// already written).
func (h *multiLocHandlers) requireOrgAdmin(w http.ResponseWriter, r *http.Request, orgID string) *auth.User {
	u := h.auth.RequireSession(r.Context(), w, r)
	if u == nil {
		return nil
	}
	role, err := h.fleet.MemberRole(r.Context(), orgID, u.ID)
	if err != nil {
		if errors.Is(err, fleet.ErrNotMember) {
			httpx.Err(w, http.StatusForbidden, "not a member of this org")
			return nil
		}
		httpx.Err(w, http.StatusInternalServerError, err.Error())
		return nil
	}
	if role != "owner" && role != "admin" {
		httpx.Err(w, http.StatusForbidden, fleet.ErrNotAdmin.Error())
		return nil
	}
	return u
}

// ─────────────────────────────────────────────────────────────────────────────
// POST /api/multiloc/enroll
// ─────────────────────────────────────────────────────────────────────────────

func (h *multiLocHandlers) enroll(w http.ResponseWriter, r *http.Request) {
	var req struct {
		OrgID      string `json:"org_id"`
		LocationID string `json:"location_id"`
		Name       string `json:"name"`
		Endpoint   string `json:"endpoint"`
	}
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	if req.OrgID == "" || req.LocationID == "" || req.Endpoint == "" {
		httpx.Err(w, http.StatusBadRequest, "org_id, location_id, and endpoint are required")
		return
	}

	u := h.requireOrgAdmin(w, r, req.OrgID)
	if u == nil {
		return
	}

	loc, err := h.store.Enroll(r.Context(), multiloc.EnrollRequest{
		OrgID:      req.OrgID,
		LocationID: req.LocationID,
		Name:       req.Name,
		Endpoint:   req.Endpoint,
	})
	if err != nil {
		if errors.Is(err, multiloc.ErrAlreadyEnrolled) {
			httpx.Err(w, http.StatusConflict, err.Error())
			return
		}
		httpx.Err(w, http.StatusInternalServerError, err.Error())
		return
	}

	// COORDINATOR: needs auditRecord (package main helper, cloud cmd/server wire_auditlog.go)
	auditRecord(r.Context(), u.Email, "multiloc.enroll", req.OrgID, map[string]string{
		"location_id": req.LocationID,
		"endpoint":    req.Endpoint,
	})

	w.WriteHeader(http.StatusCreated)
	httpx.JSON(w, locationJSON(loc))
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /api/multiloc/locations?org_id=<id>
// ─────────────────────────────────────────────────────────────────────────────

func (h *multiLocHandlers) list(w http.ResponseWriter, r *http.Request) {
	orgID := r.URL.Query().Get("org_id")
	if orgID == "" {
		httpx.Err(w, http.StatusBadRequest, "org_id is required")
		return
	}

	u := h.requireOrgAdmin(w, r, orgID)
	if u == nil {
		return
	}

	locs, err := h.store.List(r.Context(), orgID)
	if err != nil {
		httpx.Err(w, http.StatusInternalServerError, err.Error())
		return
	}

	out := make([]map[string]any, 0, len(locs))
	for _, loc := range locs {
		out = append(out, locationJSON(loc))
	}
	httpx.JSON(w, out)
}

// ─────────────────────────────────────────────────────────────────────────────
// POST /api/multiloc/health-report
// ─────────────────────────────────────────────────────────────────────────────

func (h *multiLocHandlers) healthReport(w http.ResponseWriter, r *http.Request) {
	var req struct {
		OrgID      string `json:"org_id"`
		LocationID string `json:"location_id"`
		Healthy    bool   `json:"healthy"`
	}
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	if req.OrgID == "" || req.LocationID == "" {
		httpx.Err(w, http.StatusBadRequest, "org_id and location_id are required")
		return
	}

	u := h.requireOrgAdmin(w, r, req.OrgID)
	if u == nil {
		return
	}

	if err := h.store.MarkHealth(r.Context(), req.OrgID, req.LocationID, req.Healthy); err != nil {
		if errors.Is(err, multiloc.ErrLocationNotFound) {
			httpx.Err(w, http.StatusNotFound, err.Error())
			return
		}
		httpx.Err(w, http.StatusInternalServerError, err.Error())
		return
	}

	// COORDINATOR: needs auditRecord (package main helper, cloud cmd/server wire_auditlog.go)
	auditRecord(r.Context(), u.Email, "multiloc.health_report", req.OrgID, map[string]string{
		"location_id": req.LocationID,
		"healthy":     boolStr(req.Healthy),
	})

	httpx.JSON(w, map[string]any{"ok": true, "org_id": req.OrgID, "location_id": req.LocationID, "healthy": req.Healthy})
}

// ─────────────────────────────────────────────────────────────────────────────
// JSON helpers
// ─────────────────────────────────────────────────────────────────────────────

func locationJSON(loc multiloc.Location) map[string]any {
	m := map[string]any{
		"org_id":      loc.OrgID,
		"location_id": loc.LocationID,
		"name":        loc.Name,
		"endpoint":    loc.Endpoint,
		"healthy":     loc.Healthy,
		"enrolled_at": loc.EnrolledAt.Format(time.RFC3339),
	}
	if !loc.LastSeenAt.IsZero() {
		m["last_seen_at"] = loc.LastSeenAt.Format(time.RFC3339)
	} else {
		m["last_seen_at"] = nil
	}
	return m
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
