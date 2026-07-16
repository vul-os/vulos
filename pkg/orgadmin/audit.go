package orgadmin

// audit.go — ORGADMIN-AUDIT-01: the org-admin audit-trail read path.
//
// Org management was members/roles/invite only; there was no way for an org
// admin to review WHO did WHAT (member add/remove/role-change, product-status
// changes, billing events, invites). The mutation sites already record entries
// into the CP admin audit log (wire_orgadmin.go auditRecord calls); this seam
// adds the OWN-ORG, ADMIN-GATED read side.
//
// The service stays decoupled from the concrete auditlog package via the narrow
// AuditReader seam (main.go adapts internal/auditlog.Logger.QueryOrg onto it;
// tests inject a fake). Every read is:
//
//   - session-authed (the route resolves the caller from the session, never the
//     request body/query),
//   - own-org only (the tenantID passed to the reader is the SESSION's org; the
//     reader itself hard-filters on tenant_id = ? so a cross-org read is
//     impossible even if a caller forged a body),
//   - admin-gated (Authorize(ActionAuditView) — owner / admin / billing-admin;
//     a plain member is 403),
//   - bounded + keyset-paginated (limit clamped, opaque next-seq cursor).

import (
	"context"
	"strconv"
)

// AuditEntry is one org audit record as returned to the console. It mirrors
// auditlog.OrgEntry but is defined here so the orgadmin package does not leak
// the auditlog type into its wire contract (and so a nil reader degrades to an
// empty list without importing auditlog).
type AuditEntry struct {
	Seq      int64             `json:"seq"`
	ID       string            `json:"id"`
	TS       string            `json:"ts"`
	Actor    string            `json:"actor,omitempty"`
	Action   string            `json:"action"`
	Target   string            `json:"target,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

// AuditPage is the GET /api/org/audit response: a bounded page of entries plus
// an opaque next cursor (empty when there are no older rows).
type AuditPage struct {
	Entries    []AuditEntry `json:"entries"`
	Count      int          `json:"count"`
	Limit      int          `json:"limit"`
	NextCursor string       `json:"next_cursor,omitempty"`
}

// AuditReader is the read seam over the org-scoped admin audit log. tenantID is
// ALWAYS the caller's own org (resolved server-side). The implementation MUST
// hard-filter on tenantID and MUST NOT return another org's or platform rows.
type AuditReader interface {
	// QueryOrgAudit returns entries for tenantID ONLY, newest-first, bounded.
	// action (optional) filters to one action label. afterSeq (>0) is the keyset
	// cursor: return rows with seq < afterSeq. limit is a hint (the reader clamps).
	QueryOrgAudit(ctx context.Context, tenantID, action string, afterSeq int64, limit int) ([]AuditEntry, error)
}

// orgAuditDefaultLimit / orgAuditMaxLimit bound one page at the service layer
// (defence in depth; the reader also clamps).
const (
	orgAuditDefaultLimit = 50
	orgAuditMaxLimit     = 200
)

// AuditQuery bounds a service Audit call. Cursor is the opaque next-cursor from
// a prior page ("" = newest page); it is parsed as a seq keyset internally.
type AuditQuery struct {
	Action string
	Cursor string
	Limit  int
}

// Audit authorizes the caller (ActionAuditView — owner/admin/billing-admin) and
// returns the caller-org's audit trail, own-org only, bounded + paginated.
//
//   - ErrForbidden when the caller's role does not permit audit review (→ 403).
//   - A nil reader degrades to an empty page (the console shows an empty state
//     rather than erroring) — reads never fail closed to an error the way writes
//     do, because an absent audit backing is an ops condition, not a client one.
func (s *Service) Audit(ctx context.Context, tenantID, callerAccountID string, q AuditQuery) (AuditPage, error) {
	if err := s.Authorize(ctx, tenantID, callerAccountID, ActionAuditView); err != nil {
		return AuditPage{}, err
	}

	limit := q.Limit
	if limit <= 0 || limit > orgAuditMaxLimit {
		limit = orgAuditDefaultLimit
	}

	if s.AuditR == nil {
		return AuditPage{Entries: []AuditEntry{}, Count: 0, Limit: limit}, nil
	}

	afterSeq := parseAuditCursor(q.Cursor)

	entries, err := s.AuditR.QueryOrgAudit(ctx, tenantID, q.Action, afterSeq, limit)
	if err != nil {
		return AuditPage{}, err
	}
	if entries == nil {
		entries = []AuditEntry{}
	}

	page := AuditPage{Entries: entries, Count: len(entries), Limit: limit}
	// A next cursor is only offered when the page is full (there MAY be older
	// rows). The cursor is the oldest seq on this page; the next request returns
	// rows strictly older than it.
	if len(entries) == limit && limit > 0 {
		page.NextCursor = strconv.FormatInt(entries[len(entries)-1].Seq, 10)
	}
	return page, nil
}

// parseAuditCursor turns the opaque next-cursor string back into a seq keyset.
// A malformed / empty / non-positive cursor yields 0 (newest page) — fail safe:
// a bad cursor can never widen the query, only reset it to the first page.
func parseAuditCursor(cursor string) int64 {
	if cursor == "" {
		return 0
	}
	n, err := strconv.ParseInt(cursor, 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return n
}
