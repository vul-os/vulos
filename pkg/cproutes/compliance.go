// compliance.go — POPIA / GDPR data-subject request intake.
//
// Routes:
//
//	POST /api/compliance/request   — record a data-rights request (export|erasure)
//	GET  /api/compliance/requests  — list the caller's recorded requests
//
// Auth: every route REQUIRES a valid session; the account_id is ALWAYS derived
// from that session, never from the body — a user can only file/list requests
// for their own account.
//
// SCOPE (honest by design): this records the request so the operator fulfils it
// within the statutory window. It does NOT generate an export bundle or delete
// the account synchronously — the dashboard shows an honest acknowledgement with
// a reference ID rather than a fabricated download URL or grace-window timer.
package cproutes

import (
	"encoding/json"
	"net/http"

	"github.com/vul-os/vulos-management/pkg/auth"
	"github.com/vul-os/vulos-management/pkg/compliance"
	"github.com/vul-os/vulos-management/pkg/httpx"
)

// RegisterCompliance wires the data-rights intake endpoints into mux.
func RegisterCompliance(mux *http.ServeMux, st compliance.Store, authStore *auth.Store) {
	h := &complianceHandlers{st: st, auth: authStore}
	mux.HandleFunc("POST /api/compliance/request", h.record)
	mux.HandleFunc("GET /api/compliance/requests", h.list)
}

type complianceHandlers struct {
	st   compliance.Store
	auth *auth.Store
}

type complianceRequestBody struct {
	Kind string `json:"kind"` // "export" | "erasure"
	Note string `json:"note,omitempty"`
}

func (h *complianceHandlers) record(w http.ResponseWriter, r *http.Request) {
	u := h.auth.RequireSession(r.Context(), w, r)
	if u == nil {
		return
	}
	var body complianceRequestBody
	if !httpx.DecodeJSON(w, r, &body) {
		return
	}
	if !compliance.ValidKind(body.Kind) {
		httpx.Err(w, http.StatusBadRequest, "kind must be \"export\" or \"erasure\"")
		return
	}
	// Cap the free-text note so a request can't be used to stuff arbitrary data.
	if len(body.Note) > 2000 {
		body.Note = body.Note[:2000]
	}

	req, err := h.st.Record(r.Context(), u.ID, body.Kind, body.Note)
	if err != nil {
		httpx.Err(w, http.StatusInternalServerError, "could not record request")
		return
	}
	// Content-Type must be set before WriteHeader; encode the body afterwards.
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(req)
}

func (h *complianceHandlers) list(w http.ResponseWriter, r *http.Request) {
	u := h.auth.RequireSession(r.Context(), w, r)
	if u == nil {
		return
	}
	reqs, err := h.st.ListByAccount(r.Context(), u.ID)
	if err != nil {
		httpx.Err(w, http.StatusInternalServerError, "could not list requests")
		return
	}
	httpx.JSON(w, map[string]any{"requests": reqs})
}
