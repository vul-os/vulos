package orgadmin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// recordingAuditReader is an AuditReader test double. It records EVERY tenantID
// it is queried with (so a test can prove the route only ever asks for the
// caller's OWN org) and returns a fixed set of rows keyed by tenant.
type recordingAuditReader struct {
	byTenant map[string][]AuditEntry
	// seen collects the tenant ids passed to QueryOrgAudit, in call order.
	seen []string
	// lastAction / lastAfterSeq / lastLimit capture the last query params.
	lastAction   string
	lastAfterSeq int64
	lastLimit    int
}

func (r *recordingAuditReader) QueryOrgAudit(_ context.Context, tenantID, action string, afterSeq int64, limit int) ([]AuditEntry, error) {
	r.seen = append(r.seen, tenantID)
	r.lastAction = action
	r.lastAfterSeq = afterSeq
	r.lastLimit = limit
	return r.byTenant[tenantID], nil
}

// newAuditRouter builds a router whose service has the given role + reader and a
// CallerGate fixing (tenant, caller).
func newAuditRouter(role string, reader AuditReader, tenant, caller string) http.Handler {
	svc := &Service{Roles: fixedRoleResolver{role: role}, AuditR: reader}
	mux := http.NewServeMux()
	RegisterRoutes(mux, svc, callerGate{tenant: tenant, caller: caller})
	return mux
}

// TestAudit_AdminGate: owner/admin/billing-admin may read; a plain member is
// 403; an unauthenticated caller is 401.
func TestAudit_AdminGate(t *testing.T) {
	reader := &recordingAuditReader{byTenant: map[string][]AuditEntry{
		"org-1": {{Seq: 1, Action: "org.member.invite"}},
	}}

	for _, tc := range []struct {
		role string
		want int
	}{
		{"owner", http.StatusOK},
		{"admin", http.StatusOK},
		{"billing-admin", http.StatusOK},
		{"member", http.StatusForbidden},
	} {
		h := newAuditRouter(tc.role, reader, "org-1", "caller-1")
		if code := doReq(t, h, http.MethodGet, "/api/org/audit", nil); code != tc.want {
			t.Errorf("role=%s: want %d, got %d", tc.role, tc.want, code)
		}
	}

	// Unauthenticated → 401 (the gate writes it; the reader is never touched).
	h := newAuditRouter("owner", reader, "", "")
	if code := doReq(t, h, http.MethodGet, "/api/org/audit", nil); code != http.StatusUnauthorized {
		t.Errorf("unauth: want 401, got %d", code)
	}
}

// TestAudit_OwnOrgOnly: the route ONLY ever queries the caller's own tenant —
// never a client-supplied one — and only that org's rows come back. This is the
// IDOR / cross-tenant proof at the route boundary.
func TestAudit_OwnOrgOnly(t *testing.T) {
	reader := &recordingAuditReader{byTenant: map[string][]AuditEntry{
		"org-A": {{Seq: 10, Action: "org.member.invite", Actor: "a@x.com"}},
		"org-B": {{Seq: 20, Action: "org.member.remove", Actor: "b@y.com"}},
	}}

	// Caller is in org-A. Even if they try to smuggle org-B via a query param, the
	// route resolves the tenant from the session (org-A) and ignores the param.
	h := newAuditRouter("owner", reader, "org-A", "caller-A")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/org/audit?tenant=org-B&org_id=org-B&account_id=org-B", nil)
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	// The reader must have been asked ONLY for org-A.
	for _, seen := range reader.seen {
		if seen != "org-A" {
			t.Fatalf("route queried a foreign tenant %q — cross-tenant read!", seen)
		}
	}

	var page AuditPage
	if err := json.NewDecoder(rec.Body).Decode(&page); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(page.Entries) != 1 || page.Entries[0].Actor != "a@x.com" {
		t.Fatalf("expected only org-A's rows, got %+v", page.Entries)
	}
	for _, e := range page.Entries {
		if e.Action == "org.member.remove" {
			t.Fatalf("org-B row leaked into org-A's audit view: %+v", e)
		}
	}
}

// TestAudit_Shape: the response carries the documented fields (entries/count/
// limit) and a next_cursor only when the page is full.
func TestAudit_Shape(t *testing.T) {
	// Full page (limit rows returned) → a next cursor is offered.
	rows := make([]AuditEntry, orgAuditDefaultLimit)
	for i := range rows {
		rows[i] = AuditEntry{Seq: int64(orgAuditDefaultLimit - i), Action: "org.member.invite"}
	}
	reader := &recordingAuditReader{byTenant: map[string][]AuditEntry{"org-1": rows}}
	h := newAuditRouter("owner", reader, "org-1", "caller-1")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/org/audit", nil)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	var page AuditPage
	if err := json.NewDecoder(rec.Body).Decode(&page); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if page.Count != orgAuditDefaultLimit {
		t.Errorf("count: want %d, got %d", orgAuditDefaultLimit, page.Count)
	}
	if page.Limit != orgAuditDefaultLimit {
		t.Errorf("limit: want %d, got %d", orgAuditDefaultLimit, page.Limit)
	}
	// Oldest seq on the page == 1 → next cursor should be "1".
	if page.NextCursor != "1" {
		t.Errorf("next_cursor: want \"1\", got %q", page.NextCursor)
	}
}

// TestAudit_ParamsPassThrough: action filter, cursor, and limit reach the reader.
func TestAudit_ParamsPassThrough(t *testing.T) {
	reader := &recordingAuditReader{byTenant: map[string][]AuditEntry{"org-1": nil}}
	h := newAuditRouter("owner", reader, "org-1", "caller-1")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/org/audit?action=org.member.invite&cursor=42&limit=10", nil)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	if reader.lastAction != "org.member.invite" {
		t.Errorf("action not passed: %q", reader.lastAction)
	}
	if reader.lastAfterSeq != 42 {
		t.Errorf("cursor not passed: %d", reader.lastAfterSeq)
	}
	if reader.lastLimit != 10 {
		t.Errorf("limit not passed: %d", reader.lastLimit)
	}
}

// TestAudit_NilReaderDegrades: with no reader wired, the route still answers 200
// with an empty page (the console shows an empty state, never an error).
func TestAudit_NilReaderDegrades(t *testing.T) {
	svc := &Service{Roles: fixedRoleResolver{role: "owner"}} // AuditR nil
	mux := http.NewServeMux()
	RegisterRoutes(mux, svc, callerGate{tenant: "org-1", caller: "caller-1"})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/org/audit", nil)
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("nil reader: want 200, got %d", rec.Code)
	}
	var page AuditPage
	if err := json.NewDecoder(rec.Body).Decode(&page); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(page.Entries) != 0 {
		t.Fatalf("nil reader should yield empty page, got %+v", page.Entries)
	}
	if page.Entries == nil {
		t.Fatal("entries must be a non-nil empty slice for the console")
	}
}
