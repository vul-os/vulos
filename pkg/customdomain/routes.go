package customdomain

// routes.go — HTTP routes for the paid-tier custom-domain plane
// (OPS-AUTOSCALER-CUSTOMDOMAIN-01).
//
// Routes (all session-gated, tenant-scoped to the SESSION's tenant — never a
// client-supplied tenant):
//
//	POST   /api/org/domains                  — request a verification TXT token
//	GET    /api/org/domains                  — list the caller-tenant's domains
//	POST   /api/org/domains/{domain}/verify  — verify DNS, then attach on success
//	DELETE /api/org/domains/{domain}         — detach the domain
//
// Cross-tenant access surfaces as 404 (not 403) so we never disclose the
// existence of another tenant's domain — matching the breakouts / meetalloc
// convention. Default-branch errors are scrubbed (generic client message +
// server-side log) mirroring syncrz/routes.go and the Wave-A/Wave-D
// error-leak fixes (FIX-ADMIN-ERR-LEAK-01).
//
// The package stays standalone: instead of importing internal/auth (which would
// couple this plane to the session store and create awkward test deps) the
// handlers take a SessionGate seam. main.go adapts auth.Store.RequireSession
// onto it; routes_test.go injects a fake.

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
)

// genericMsg is the generic client-facing string used in place of raw
// err.Error() on the default error branch. Sentinel branches keep their safe
// deterministic text. Mirrors the syncrz / lancert / abuse pattern from
// audit-fix wave A (FIX-ADMIN-ERR-LEAK-01).
const genericMsg = "internal error"

// SessionGate is the narrow auth seam the routes use. It returns the
// authenticated caller's tenant id and true, or writes the 401 itself and
// returns ("", false). The session user's id doubles as the tenant id — the
// same 1:1 convention meetalloc / breakouts / streamsession use.
type SessionGate interface {
	// TenantFromSession resolves the session on r to a tenant id. On failure
	// it MUST write the appropriate auth error to w and return ("", false);
	// callers return immediately without writing anything further.
	TenantFromSession(w http.ResponseWriter, r *http.Request) (tenantID string, ok bool)
}

// SessionGateFunc adapts a plain func to SessionGate.
type SessionGateFunc func(w http.ResponseWriter, r *http.Request) (string, bool)

// TenantFromSession implements SessionGate.
func (f SessionGateFunc) TenantFromSession(w http.ResponseWriter, r *http.Request) (string, bool) {
	return f(w, r)
}

// RegisterRoutes attaches the custom-domain routes to mux. svc and gate must be
// non-nil.
func RegisterRoutes(mux *http.ServeMux, svc *Service, gate SessionGate) {
	h := &handlers{svc: svc, gate: gate}
	// Collection: POST creates a verification request, GET lists.
	mux.HandleFunc("POST /api/org/domains", h.handleRequestVerification)
	mux.HandleFunc("GET /api/org/domains", h.handleList)
	// Item: {domain}/verify (POST) and {domain} (DELETE). Go's stdlib mux
	// routes these distinct patterns precisely; the trailing /verify segment
	// is matched as a literal so it never collides with the bare {domain}.
	mux.HandleFunc("POST /api/org/domains/{domain}/verify", h.handleVerify)
	mux.HandleFunc("DELETE /api/org/domains/{domain}", h.handleDetach)
}

type handlers struct {
	svc  *Service
	gate SessionGate
}

// handleRequestVerification: POST /api/org/domains  body {"domain":"..."}
// Returns the VerificationInstruction (the TXT record the user must publish).
func (h *handlers) handleRequestVerification(w http.ResponseWriter, r *http.Request) {
	tenant, ok := h.gate.TenantFromSession(w, r)
	if !ok {
		return
	}
	var body struct {
		Domain string `json:"domain"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if strings.TrimSpace(body.Domain) == "" {
		writeErr(w, http.StatusBadRequest, "domain required")
		return
	}
	instr, err := h.svc.RequestVerification(r.Context(), tenant, body.Domain)
	if err != nil {
		mapError(w, r, "request_verification", err)
		return
	}
	writeJSON(w, http.StatusOK, instr)
}

// handleList: GET /api/org/domains — list the caller-tenant's domains.
func (h *handlers) handleList(w http.ResponseWriter, r *http.Request) {
	tenant, ok := h.gate.TenantFromSession(w, r)
	if !ok {
		return
	}
	list, err := h.svc.List(r.Context(), tenant)
	if err != nil {
		mapError(w, r, "list", err)
		return
	}
	if list == nil {
		list = []CustomDomain{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"domains": list})
}

// handleVerify: POST /api/org/domains/{domain}/verify — verify DNS then, on
// success, attach. Returns the resulting CustomDomain state.
func (h *handlers) handleVerify(w http.ResponseWriter, r *http.Request) {
	tenant, ok := h.gate.TenantFromSession(w, r)
	if !ok {
		return
	}
	domain := r.PathValue("domain")
	if strings.TrimSpace(domain) == "" {
		writeErr(w, http.StatusBadRequest, "domain required")
		return
	}
	// Ownership pre-check: confirm the session's tenant owns this domain BEFORE
	// touching DNS, and answer cross-tenant / unknown alike with 404 so we never
	// disclose another tenant's domain. RouteFor isn't suitable here (it only
	// resolves attached rows) — we read the row through the service's store via
	// VerifyDNS, which surfaces ErrTenantMismatch downstream; we guard with an
	// explicit owns() check first for the not-yet-attached states.
	if !h.owns(r, tenant, domain) {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	row, err := h.svc.VerifyDNS(r.Context(), domain)
	if err != nil {
		mapError(w, r, "verify", err)
		return
	}
	// DNS verified — attempt the attach. Attach is a no-op-friendly transition
	// (ErrAlreadyAttached when the row was already attached); treat that as
	// success and return the current state.
	if row.VerifyState == StateVerified {
		attached, aerr := h.svc.Attach(r.Context(), tenant, domain)
		if aerr != nil && !errors.Is(aerr, ErrAlreadyAttached) {
			mapError(w, r, "attach", aerr)
			return
		}
		if aerr == nil {
			row = attached
		}
	}
	writeJSON(w, http.StatusOK, row)
}

// handleDetach: DELETE /api/org/domains/{domain}.
func (h *handlers) handleDetach(w http.ResponseWriter, r *http.Request) {
	tenant, ok := h.gate.TenantFromSession(w, r)
	if !ok {
		return
	}
	domain := r.PathValue("domain")
	if strings.TrimSpace(domain) == "" {
		writeErr(w, http.StatusBadRequest, "domain required")
		return
	}
	if !h.owns(r, tenant, domain) {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	if err := h.svc.Detach(r.Context(), tenant, domain); err != nil {
		mapError(w, r, "detach", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"domain": normaliseDomain(domain), "detached": true})
}

// owns reports whether tenant owns the domain row. Unknown domain → false (the
// caller answers 404). A store error other than ErrNotFound also yields false
// here; the subsequent service call re-surfaces the real error for logging.
func (h *handlers) owns(r *http.Request, tenant, domain string) bool {
	row, err := h.svc.Store.Get(r.Context(), normaliseDomain(domain))
	if err != nil {
		return false
	}
	return row.TenantID == tenant
}

// mapError maps service-layer errors to HTTP statuses. Sentinel errors get
// stable client-safe codes; unknown errors collapse to 500 with a generic
// message and a server-side log line (no raw err leaked to the client).
func mapError(w http.ResponseWriter, r *http.Request, kind string, err error) {
	switch {
	case errors.Is(err, ErrInvalidDomain):
		writeErr(w, http.StatusBadRequest, "invalid domain")
	case errors.Is(err, ErrNotFound),
		errors.Is(err, ErrTenantMismatch):
		// Cross-tenant + unknown collapse to 404 — no existence disclosure.
		writeErr(w, http.StatusNotFound, "not found")
	case errors.Is(err, ErrNotVerified):
		writeErr(w, http.StatusConflict, "domain not verified yet")
	case errors.Is(err, ErrAlreadyAttached):
		writeErr(w, http.StatusConflict, "domain already attached")
	case errors.Is(err, ErrVerificationFailed):
		writeErr(w, http.StatusUnprocessableEntity, "verification token not found in DNS")
	default:
		logServerErr(r, kind, err)
		writeErr(w, http.StatusInternalServerError, genericMsg)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// logServerErr emits a structured server-side log line carrying the raw error
// and request route. Kept dep-free so the package stays standalone.
func logServerErr(r *http.Request, kind string, err error) {
	if err == nil {
		return
	}
	log.Printf("[customdomain] route=%s kind=%s err=%v", r.URL.Path, kind, err)
}
