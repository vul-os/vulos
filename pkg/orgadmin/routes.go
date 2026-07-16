package orgadmin

// routes.go — HTTP routes for the org-admin dashboard plane
// (OPS-ADMIN-DASHBOARD-01, Wave E).
//
// Routes (all session-gated, tenant-scoped to the SESSION's tenant — never a
// client-supplied tenant):
//
//	GET    /api/org/members
//	POST   /api/org/invite             { email, role, name? }
//	DELETE /api/org/members/{id}
//	PATCH  /api/org/members/{id}/role  { role }
//	PUT    /api/org/members/{id}/name  { name }
//	GET    /api/org/directory          [?q=]            (FILES-DIRECTORY-01)
//	GET    /api/org/directory/resolve  ?email=|?q=      (FILES-DIRECTORY-01)
//	GET    /api/org/instances
//	GET    /api/org/usage
//	GET    /api/org/quotas
//	GET    /api/org/seats             { billable_seats, active_members, … }
//	GET    /api/org/backup-mode
//	PUT    /api/org/backup-mode        { mode, per_location[] }
//	GET    /api/billing/summary
//
// Cross-tenant / unknown access surfaces as 404 (not 403) so we never disclose
// the existence of another tenant's resource — matching customdomain /
// meetalloc. Default-branch errors are scrubbed (generic client message +
// server-side log, no raw err) mirroring syncrz / customdomain
// (FIX-ADMIN-ERR-LEAK-01).
//
// The package stays standalone: instead of importing internal/auth the
// handlers take a SessionGate seam. main.go adapts auth.Store.RequireSession
// onto it; routes_test.go injects a fake.

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
)

// genericMsg is the generic client-facing string used in place of raw
// err.Error() on the default error branch. Mirrors the customdomain / syncrz
// pattern from audit-fix wave A (FIX-ADMIN-ERR-LEAK-01).
const genericMsg = "internal error"

// SessionGate is the narrow auth seam the routes use. It returns the
// authenticated caller's tenant id and true, or writes the 401 itself and
// returns ("", false). The session user's id doubles as the tenant id — the
// same 1:1 convention customdomain / meetalloc / breakouts use.
type SessionGate interface {
	// TenantFromSession resolves the session on r to a tenant id. On failure it
	// MUST write the appropriate auth error to w and return ("", false);
	// callers return immediately without writing anything further.
	TenantFromSession(w http.ResponseWriter, r *http.Request) (tenantID string, ok bool)
}

// SessionGateFunc adapts a plain func to SessionGate.
type SessionGateFunc func(w http.ResponseWriter, r *http.Request) (string, bool)

// TenantFromSession implements SessionGate.
func (f SessionGateFunc) TenantFromSession(w http.ResponseWriter, r *http.Request) (string, bool) {
	return f(w, r)
}

// RegisterRoutes attaches the org-admin routes to mux. svc and gate must be
// non-nil.
func RegisterRoutes(mux *http.ServeMux, svc *Service, gate SessionGate) {
	h := &handlers{svc: svc, gate: gate}

	// Members.
	mux.HandleFunc("GET /api/org/members", h.handleListMembers)
	mux.HandleFunc("POST /api/org/invite", h.handleInvite)
	// Go's stdlib mux routes these distinct patterns precisely: the literal
	// trailing /role segment never collides with the bare {id} delete.
	mux.HandleFunc("DELETE /api/org/members/{id}", h.handleRemoveMember)
	mux.HandleFunc("PATCH /api/org/members/{id}/role", h.handleChangeRole)
	mux.HandleFunc("PUT /api/org/members/{id}/name", h.handleSetMemberName)

	// Member directory + share-target resolution (FILES-DIRECTORY-01). These
	// back the Drive "Share with…" picker: list the org's shareable principals
	// and resolve an email/handle to the user-id the Files ACL stores. Read-only,
	// session-gated (any member — sharing is a member action, not an admin one),
	// tenant-scoped to the SESSION's tenant.
	mux.HandleFunc("GET /api/org/directory", h.handleDirectory)
	mux.HandleFunc("GET /api/org/directory/resolve", h.handleResolveShareTarget)

	// Read-only dashboards.
	mux.HandleFunc("GET /api/org/instances", h.handleInstances)
	mux.HandleFunc("GET /api/org/usage", h.handleUsage)
	mux.HandleFunc("GET /api/org/quotas", h.handleQuotas)
	// Seat billing (SEAT-BILLING-01): billable Workspace+OS seats = active org
	// members. Mail connections are NOT seat-billed and never appear here.
	mux.HandleFunc("GET /api/org/seats", h.handleSeats)

	// Backup mode.
	mux.HandleFunc("GET /api/org/backup-mode", h.handleGetBackupMode)
	mux.HandleFunc("PUT /api/org/backup-mode", h.handleSetBackupMode)

	// Billing summary. Invoice history (GET /api/billing/invoices) is NOT
	// registered here: it is owned by registerBillingCheckoutRoutes, which
	// serves the authoritative subscription_events ledger (plus the {id} and
	// {id}/receipt subroutes). Registering it twice panics the ServeMux.
	mux.HandleFunc("GET /api/billing/summary", h.handleBillingSummary)

	// Org audit trail (ORGADMIN-AUDIT-01). Admin-gated read (owner/admin/
	// billing-admin) of the caller-org's admin-action history. Own-org only +
	// paginated; a plain member is 403.
	mux.HandleFunc("GET /api/org/audit", h.handleAudit)
}

type handlers struct {
	svc  *Service
	gate SessionGate
}

// resolveCaller resolves (tenantID, callerAccountID) from the gate. When the
// wired gate implements CallerGate the caller account is distinct from the
// tenant (enabling a real RBAC role lookup); otherwise the caller defaults to
// the tenant id (legacy: the session user IS the tenant owner under the 1:1
// account==tenant convention). On auth failure the gate writes the error and
// resolveCaller returns ok=false.
func (h *handlers) resolveCaller(w http.ResponseWriter, r *http.Request) (tenantID, caller string, ok bool) {
	if cg, isCaller := h.gate.(CallerGate); isCaller {
		return cg.CallerFromSession(w, r)
	}
	t, gok := h.gate.TenantFromSession(w, r)
	if !gok {
		return "", "", false
	}
	return t, t, true
}

// authorize gates an action on the caller's role. On denial it writes 403 and
// returns false; the handler must then return immediately.
func (h *handlers) authorize(w http.ResponseWriter, r *http.Request, tenant, caller string, action Action) bool {
	if err := h.svc.Authorize(r.Context(), tenant, caller, action); err != nil {
		mapError(w, r, string(action), err)
		return false
	}
	return true
}

// ── Members ────────────────────────────────────────────────────────────────

func (h *handlers) handleListMembers(w http.ResponseWriter, r *http.Request) {
	tenant, ok := h.gate.TenantFromSession(w, r)
	if !ok {
		return
	}
	members, err := h.svc.ListMembers(r.Context(), tenant)
	if err != nil {
		mapError(w, r, "list_members", err)
		return
	}
	// PAGINATE-01: bounded page + opaque next-cursor. The roster is returned in a
	// stable order (the store's ORDER BY account_id); the page window is applied
	// here. When the client passes no paging params the FULL first page (up to
	// the default size) is returned and the legacy {members:[…]} shape is
	// preserved alongside the new pagination metadata.
	pg := parseMemberPage(r)
	total := len(members)
	lo := pg.offset
	if lo > total {
		lo = total
	}
	hi := lo + pg.limit
	if hi > total {
		hi = total
	}
	pageMembers := members[lo:hi]
	if pageMembers == nil {
		pageMembers = []Member{}
	}
	resp := MembersResponse{Members: pageMembers}
	resp.Limit = pg.limit
	resp.Offset = pg.offset
	resp.Count = len(pageMembers)
	resp.NextCursor = pg.next(len(pageMembers))
	writeJSON(w, http.StatusOK, resp)
}

func (h *handlers) handleInvite(w http.ResponseWriter, r *http.Request) {
	tenant, caller, ok := h.resolveCaller(w, r)
	if !ok {
		return
	}
	if !h.authorize(w, r, tenant, caller, ActionMemberInvite) {
		return
	}
	var body InviteRequest
	if err := decodeJSON(w, r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	inv, err := h.svc.CreateInvite(r.Context(), tenant, body.Email, body.Role, body.Name)
	if err != nil {
		mapError(w, r, "invite", err)
		return
	}
	writeJSON(w, http.StatusOK, inv)
}

func (h *handlers) handleSetMemberName(w http.ResponseWriter, r *http.Request) {
	tenant, caller, ok := h.resolveCaller(w, r)
	if !ok {
		return
	}
	if !h.authorize(w, r, tenant, caller, ActionMemberName) {
		return
	}
	id := r.PathValue("id")
	if strings.TrimSpace(id) == "" {
		writeErr(w, http.StatusBadRequest, "member id required")
		return
	}
	var body MemberNameRequest
	if err := decodeJSON(w, r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if err := h.svc.SetMemberName(r.Context(), tenant, id, body.Name); err != nil {
		mapError(w, r, "set_member_name", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "name": strings.TrimSpace(body.Name)})
}

func (h *handlers) handleRemoveMember(w http.ResponseWriter, r *http.Request) {
	tenant, caller, ok := h.resolveCaller(w, r)
	if !ok {
		return
	}
	// Destructive: owner-only.
	if !h.authorize(w, r, tenant, caller, ActionMemberRemove) {
		return
	}
	id := r.PathValue("id")
	if strings.TrimSpace(id) == "" {
		writeErr(w, http.StatusBadRequest, "member id required")
		return
	}
	if err := h.svc.RemoveMember(r.Context(), tenant, id); err != nil {
		mapError(w, r, "remove_member", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "removed": true})
}

func (h *handlers) handleChangeRole(w http.ResponseWriter, r *http.Request) {
	tenant, caller, ok := h.resolveCaller(w, r)
	if !ok {
		return
	}
	// Destructive (privilege change): owner-only.
	if !h.authorize(w, r, tenant, caller, ActionMemberRole) {
		return
	}
	id := r.PathValue("id")
	if strings.TrimSpace(id) == "" {
		writeErr(w, http.StatusBadRequest, "member id required")
		return
	}
	var body RoleChangeRequest
	if err := decodeJSON(w, r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if err := h.svc.ChangeRole(r.Context(), tenant, id, body.Role); err != nil {
		mapError(w, r, "change_role", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "role": normaliseRole(body.Role)})
}

// ── Member directory + share-target resolution (FILES-DIRECTORY-01) ─────────

// handleDirectory serves GET /api/org/directory — the shareable principals in
// the caller's org for the Drive picker (id + display name + email). Optional
// ?q= applies a case-insensitive substring filter over name + email. The caller
// is excluded from the result (you do not share with yourself). Session-gated
// (any member) + tenant-scoped to the session's tenant.
func (h *handlers) handleDirectory(w http.ResponseWriter, r *http.Request) {
	tenant, caller, ok := h.resolveCaller(w, r)
	if !ok {
		return
	}
	out, err := h.svc.Directory(r.Context(), tenant, caller, r.URL.Query().Get("q"))
	if err != nil {
		mapError(w, r, "directory", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleResolveShareTarget serves GET /api/org/directory/resolve — turn an
// email (?email=) or bare handle (?q=) into the DirectoryEntry whose id is the
// user-id the Files ACL stores. Resolution is scoped to the session's tenant, so
// an unknown / cross-org target surfaces as 404 (no existence disclosure).
func (h *handlers) handleResolveShareTarget(w http.ResponseWriter, r *http.Request) {
	tenant, ok := h.gate.TenantFromSession(w, r)
	if !ok {
		return
	}
	q := r.URL.Query().Get("email")
	if strings.TrimSpace(q) == "" {
		q = r.URL.Query().Get("q")
	}
	if strings.TrimSpace(q) == "" {
		writeErr(w, http.StatusBadRequest, "email or q required")
		return
	}
	entry, err := h.svc.ResolveShareTarget(r.Context(), tenant, q)
	if err != nil {
		mapError(w, r, "resolve_share_target", err)
		return
	}
	writeJSON(w, http.StatusOK, entry)
}

// ── Read-only dashboards ─────────────────────────────────────────────────────

func (h *handlers) handleInstances(w http.ResponseWriter, r *http.Request) {
	tenant, ok := h.gate.TenantFromSession(w, r)
	if !ok {
		return
	}
	out, err := h.svc.ListInstances(r.Context(), tenant)
	if err != nil {
		mapError(w, r, "instances", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *handlers) handleUsage(w http.ResponseWriter, r *http.Request) {
	tenant, ok := h.gate.TenantFromSession(w, r)
	if !ok {
		return
	}
	out, err := h.svc.Usage(r.Context(), tenant)
	if err != nil {
		mapError(w, r, "usage", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *handlers) handleQuotas(w http.ResponseWriter, r *http.Request) {
	tenant, ok := h.gate.TenantFromSession(w, r)
	if !ok {
		return
	}
	out, err := h.svc.Quotas(r.Context(), tenant)
	if err != nil {
		mapError(w, r, "quotas", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleSeats serves GET /api/org/seats — the billable Workspace+OS seat
// snapshot. Read-only + tenant-scoped (tenant comes from the session, never the
// request). Mail connections are not seat-billed and never appear here.
func (h *handlers) handleSeats(w http.ResponseWriter, r *http.Request) {
	tenant, ok := h.gate.TenantFromSession(w, r)
	if !ok {
		return
	}
	out, err := h.svc.Seats(r.Context(), tenant)
	if err != nil {
		mapError(w, r, "seats", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// ── Backup mode ──────────────────────────────────────────────────────────────

func (h *handlers) handleGetBackupMode(w http.ResponseWriter, r *http.Request) {
	tenant, ok := h.gate.TenantFromSession(w, r)
	if !ok {
		return
	}
	out, err := h.svc.GetBackupMode(r.Context(), tenant)
	if err != nil {
		mapError(w, r, "get_backup_mode", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *handlers) handleSetBackupMode(w http.ResponseWriter, r *http.Request) {
	tenant, caller, ok := h.resolveCaller(w, r)
	if !ok {
		return
	}
	if !h.authorize(w, r, tenant, caller, ActionBackupModeSet) {
		return
	}
	var body BackupModeRequest
	if err := decodeJSON(w, r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	out, err := h.svc.SetBackupMode(r.Context(), tenant, body)
	if err != nil {
		mapError(w, r, "set_backup_mode", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// ── Billing summary ──────────────────────────────────────────────────────────

func (h *handlers) handleBillingSummary(w http.ResponseWriter, r *http.Request) {
	tenant, ok := h.gate.TenantFromSession(w, r)
	if !ok {
		return
	}
	out, err := h.svc.BillingSummary(r.Context(), tenant)
	if err != nil {
		mapError(w, r, "billing_summary", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// ── Org audit trail (ORGADMIN-AUDIT-01) ──────────────────────────────────────

// handleAudit serves GET /api/org/audit — the caller-org's admin-action audit
// trail. Admin-gated (ActionAuditView → owner/admin/billing-admin; a plain
// member is 403). Own-org only: the tenant comes from the SESSION, never the
// request, and the service reader hard-filters on it, so there is no cross-org
// read path. Bounded + keyset-paginated:
//
//	GET /api/org/audit?action=<label>&cursor=<seq>&limit=<n>
//	 → 200 { entries:[{seq,id,ts,actor,action,target,metadata}], count, limit, next_cursor? }
//
// Query params are all optional. `action` filters to one action label; `cursor`
// is the opaque next_cursor from a prior page; `limit` is clamped by the service.
func (h *handlers) handleAudit(w http.ResponseWriter, r *http.Request) {
	tenant, caller, ok := h.resolveCaller(w, r)
	if !ok {
		return
	}
	q := AuditQuery{
		Action: strings.TrimSpace(r.URL.Query().Get("action")),
		Cursor: strings.TrimSpace(r.URL.Query().Get("cursor")),
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		if n, err := parseAuditLimit(raw); err == nil {
			q.Limit = n
		}
	}
	page, err := h.svc.Audit(r.Context(), tenant, caller, q)
	if err != nil {
		mapError(w, r, "audit", err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

// parseAuditLimit parses a positive limit; a non-numeric or non-positive value
// returns an error so the caller leaves Limit at 0 (→ service default).
func parseAuditLimit(raw string) (int, error) {
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return 0, errBadLimit
	}
	return n, nil
}

var errBadLimit = errors.New("orgadmin: bad limit")

// ──────────────────────────────────────────────────────────────────────────
// Error mapping + JSON helpers
// ──────────────────────────────────────────────────────────────────────────

// mapError maps service-layer errors to HTTP statuses. Sentinel errors get
// stable client-safe codes; unknown errors collapse to 500 with a generic
// message and a server-side log line (no raw err leaked to the client).
func mapError(w http.ResponseWriter, r *http.Request, kind string, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		// Cross-tenant + unknown collapse to 404 — no existence disclosure.
		writeErr(w, http.StatusNotFound, "not found")
	case errors.Is(err, ErrForbidden):
		// RBAC-ORG-01: the caller's role does not permit this action.
		writeErr(w, http.StatusForbidden, "forbidden")
	case errors.Is(err, ErrInvalidRole):
		writeErr(w, http.StatusBadRequest, "invalid role")
	case errors.Is(err, ErrInvalidInput):
		writeErr(w, http.StatusBadRequest, "invalid input")
	case errors.Is(err, ErrOrgLimitReached):
		// Anti-farming: per-account org cap / creation rate-limit hit.
		writeErr(w, http.StatusTooManyRequests, "org creation limit reached — upgrade to a paid plan to add more orgs")
	case errors.Is(err, ErrLastOwner):
		// Cannot leave as the sole owner — transfer ownership first.
		writeErr(w, http.StatusConflict, "cannot leave: you are the last owner — transfer ownership before leaving")
	case errors.Is(err, ErrInviteStoreUnavailable):
		// No invite backing wired — not a client error; scrub + log.
		logServerErr(r, kind, err)
		writeErr(w, http.StatusServiceUnavailable, "invites not available")
	default:
		logServerErr(r, kind, err)
		writeErr(w, http.StatusInternalServerError, genericMsg)
	}
}

// decodeJSON decodes a JSON body with a 64 KiB cap. An empty body decodes to
// the zero value (no error) so PUT/PATCH with empty bodies fail validation
// downstream rather than at decode.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	if err := dec.Decode(dst); err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
		return err
	}
	return nil
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
	log.Printf("[orgadmin] route=%s kind=%s err=%v", r.URL.Path, kind, err)
}
