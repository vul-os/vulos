package orgadmin

// orgs_routes.go — multi-org HTTP routes consumed by the Workspace surfaces
// (ORG-MULTI-01). Distinct from the org-admin dashboard routes in routes.go;
// these manage the SET of orgs a user belongs to and the active org:
//
//	GET  /api/org          → [{id,slug,name,role,current}]
//	POST /api/org/create   { name }   → {id,slug,role}   (creator=owner)
//	POST /api/org/switch   { id }      → {id,current:true}
//
// All three are session-gated. The caller account id is resolved from the gate
// (CallerGate preferred; SessionGate's tenant id is the fallback under the 1:1
// account==tenant convention). Cross-tenant / non-member access surfaces as 404.

import (
	"net/http"
	"strings"
)

// orgHandlers backs the multi-org routes.
type orgHandlers struct {
	svc  *OrgService
	gate SessionGate
}

// RegisterOrgRoutes attaches the multi-org routes to mux. svc and gate must be
// non-nil. The gate SHOULD implement CallerGate so the caller account id is
// resolved distinctly from the tenant; otherwise the tenant id is used.
//
// Routes registered:
//
//	GET  /api/org          → [{id,slug,name,role,current}]
//	POST /api/org/create   → {id,slug,role}
//	POST /api/org/switch   → {id,current:true}
//	POST /api/org/leave    → {} (204) — member leaves active/named org;
//	                         last-owner is blocked (409).
func RegisterOrgRoutes(mux *http.ServeMux, svc *OrgService, gate SessionGate) {
	h := &orgHandlers{svc: svc, gate: gate}
	mux.HandleFunc("GET /api/org", h.handleList)
	mux.HandleFunc("POST /api/org/create", h.handleCreate)
	mux.HandleFunc("POST /api/org/switch", h.handleSwitch)
	mux.HandleFunc("POST /api/org/leave", h.handleLeave)
}

// caller resolves the authenticated caller account id from the gate. On auth
// failure the gate has already written the response and caller returns ok=false.
func (h *orgHandlers) caller(w http.ResponseWriter, r *http.Request) (string, bool) {
	if cg, ok := h.gate.(CallerGate); ok {
		_, acct, gok := cg.CallerFromSession(w, r)
		return acct, gok
	}
	t, gok := h.gate.TenantFromSession(w, r)
	return t, gok
}

func (h *orgHandlers) handleList(w http.ResponseWriter, r *http.Request) {
	acct, ok := h.caller(w, r)
	if !ok {
		return
	}
	orgs, err := h.svc.ListForUser(r.Context(), acct)
	if err != nil {
		mapError(w, r, "org_list", err)
		return
	}
	if orgs == nil {
		orgs = []OrgListItem{}
	}
	writeJSON(w, http.StatusOK, orgs)
}

func (h *orgHandlers) handleCreate(w http.ResponseWriter, r *http.Request) {
	acct, ok := h.caller(w, r)
	if !ok {
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if strings.TrimSpace(body.Name) == "" {
		writeErr(w, http.StatusBadRequest, "name is required")
		return
	}
	res, err := h.svc.CreateOrg(r.Context(), acct, body.Name)
	if err != nil {
		mapError(w, r, "org_create", err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (h *orgHandlers) handleSwitch(w http.ResponseWriter, r *http.Request) {
	acct, ok := h.caller(w, r)
	if !ok {
		return
	}
	var body struct {
		ID string `json:"id"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if strings.TrimSpace(body.ID) == "" {
		writeErr(w, http.StatusBadRequest, "id is required")
		return
	}
	if err := h.svc.SwitchActive(r.Context(), acct, body.ID); err != nil {
		mapError(w, r, "org_switch", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": strings.TrimSpace(body.ID), "current": true})
}

// handleLeave backs POST /api/org/leave. The caller leaves the org specified in
// the request body (or their current active org when "id" is omitted). On
// success the membership is removed; the caller's active org is automatically
// reverted to their next remaining org. The caller's account and root mailbox
// are NEVER deleted. The last owner is blocked with 409.
func (h *orgHandlers) handleLeave(w http.ResponseWriter, r *http.Request) {
	acct, ok := h.caller(w, r)
	if !ok {
		return
	}
	var body struct {
		ID string `json:"id,omitempty"` // optional: defaults to active org
	}
	// Body is optional — a POST with no JSON body means "leave current active org".
	_ = decodeJSON(w, r, &body)
	orgID := strings.TrimSpace(body.ID)
	// Resolve to active org when id is not supplied.
	if orgID == "" {
		active, err := h.svc.Store.GetActiveOrg(r.Context(), acct)
		if err != nil || active == "" {
			writeErr(w, http.StatusBadRequest, "id is required (no active org found)")
			return
		}
		orgID = active
	}
	if err := h.svc.LeaveOrg(r.Context(), acct, orgID); err != nil {
		mapError(w, r, "org_leave", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
