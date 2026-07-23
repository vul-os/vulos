package auditlog_test

import (
	"context"
	"math"
	"strconv"
	"testing"

	"github.com/vul-os/vulos-management/pkg/auditlog"
)

// TestRecordOrg_RejectsEmptyTenant: an org record MUST carry a tenant.
func TestRecordOrg_RejectsEmptyTenant(t *testing.T) {
	ctx := context.Background()
	l, _ := openLogger(t)
	if err := l.RecordOrg(ctx, "", "actor", "org.member.invite", "tenant:", nil); err == nil {
		t.Fatal("RecordOrg with empty tenant: want error, got nil")
	}
}

// TestQueryOrg_OwnOrgIsolation: QueryOrg for org A returns ONLY org A's rows —
// never org B's, and never the blank-tenant platform rows.
func TestQueryOrg_OwnOrgIsolation(t *testing.T) {
	ctx := context.Background()
	l, _ := openLogger(t)

	// Platform (blank-tenant) rows — must never appear in any org view.
	if err := l.Record(ctx, "system", "pop.drain", "pop-1", nil); err != nil {
		t.Fatalf("Record platform: %v", err)
	}
	// Org A rows.
	for i := 0; i < 3; i++ {
		if err := l.RecordOrg(ctx, "org-A", "a@x.com", "org.member.invite", "tenant:org-A",
			map[string]string{"email": "invitee" + strconv.Itoa(i) + "@x.com"}); err != nil {
			t.Fatalf("RecordOrg A %d: %v", i, err)
		}
	}
	// Org B rows.
	for i := 0; i < 2; i++ {
		if err := l.RecordOrg(ctx, "org-B", "b@y.com", "org.member.remove", "tenant:org-B", nil); err != nil {
			t.Fatalf("RecordOrg B %d: %v", i, err)
		}
	}

	aRows, err := l.QueryOrg(ctx, "org-A", auditlog.OrgQueryOptions{})
	if err != nil {
		t.Fatalf("QueryOrg A: %v", err)
	}
	if len(aRows) != 3 {
		t.Fatalf("org-A rows: want 3, got %d", len(aRows))
	}
	for _, e := range aRows {
		if e.Action != "org.member.invite" {
			t.Errorf("org-A leaked a non-A row: %+v", e)
		}
		if e.Target != "tenant:org-A" {
			t.Errorf("org-A row has wrong target (possible cross-tenant leak): %+v", e)
		}
	}

	bRows, err := l.QueryOrg(ctx, "org-B", auditlog.OrgQueryOptions{})
	if err != nil {
		t.Fatalf("QueryOrg B: %v", err)
	}
	if len(bRows) != 2 {
		t.Fatalf("org-B rows: want 2, got %d", len(bRows))
	}

	// An org that wrote nothing sees nothing — and CANNOT see A's or B's or the
	// platform rows.
	none, err := l.QueryOrg(ctx, "org-C", auditlog.OrgQueryOptions{})
	if err != nil {
		t.Fatalf("QueryOrg C: %v", err)
	}
	if len(none) != 0 {
		t.Fatalf("org-C (no rows) leaked %d rows: %+v", len(none), none)
	}

	// Empty tenant must fail closed (never return the blank-tenant platform rows).
	blank, err := l.QueryOrg(ctx, "", auditlog.OrgQueryOptions{})
	if err != nil {
		t.Fatalf("QueryOrg blank: %v", err)
	}
	if len(blank) != 0 {
		t.Fatalf("QueryOrg with empty tenant leaked %d rows (platform rows!): %+v", len(blank), blank)
	}
}

// TestQueryOrg_NewestFirstAndPagination: rows come back newest-first and the
// AfterSeq keyset cursor pages backwards without overlap or gaps.
func TestQueryOrg_NewestFirstAndPagination(t *testing.T) {
	ctx := context.Background()
	l, _ := openLogger(t)

	const n = 10
	for i := 0; i < n; i++ {
		if err := l.RecordOrg(ctx, "org-A", "a@x.com", "org.member.role", "m"+strconv.Itoa(i), nil); err != nil {
			t.Fatalf("RecordOrg %d: %v", i, err)
		}
	}

	page1, err := l.QueryOrg(ctx, "org-A", auditlog.OrgQueryOptions{Limit: 4})
	if err != nil {
		t.Fatalf("page1: %v", err)
	}
	if len(page1) != 4 {
		t.Fatalf("page1 len: want 4, got %d", len(page1))
	}
	// Newest-first: seq strictly descending.
	for i := 1; i < len(page1); i++ {
		if page1[i].Seq >= page1[i-1].Seq {
			t.Fatalf("page1 not newest-first: %d then %d", page1[i-1].Seq, page1[i].Seq)
		}
	}

	lastSeq := page1[len(page1)-1].Seq
	page2, err := l.QueryOrg(ctx, "org-A", auditlog.OrgQueryOptions{Limit: 4, AfterSeq: lastSeq})
	if err != nil {
		t.Fatalf("page2: %v", err)
	}
	if len(page2) != 4 {
		t.Fatalf("page2 len: want 4, got %d", len(page2))
	}
	// No overlap: every page2 seq is strictly below the page1 cursor.
	for _, e := range page2 {
		if e.Seq >= lastSeq {
			t.Fatalf("page2 overlaps page1: seq %d >= cursor %d", e.Seq, lastSeq)
		}
	}
}

// TestQueryOrg_ActionFilter: the optional action filter is exact-match and
// stays tenant-scoped.
func TestQueryOrg_ActionFilter(t *testing.T) {
	ctx := context.Background()
	l, _ := openLogger(t)

	_ = l.RecordOrg(ctx, "org-A", "a@x.com", "org.member.invite", "t", nil)
	_ = l.RecordOrg(ctx, "org-A", "a@x.com", "org.member.remove", "t", nil)
	_ = l.RecordOrg(ctx, "org-A", "a@x.com", "org.member.invite", "t", nil)

	got, err := l.QueryOrg(ctx, "org-A", auditlog.OrgQueryOptions{Action: "org.member.invite"})
	if err != nil {
		t.Fatalf("QueryOrg filter: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("action filter: want 2 invites, got %d", len(got))
	}
	for _, e := range got {
		if e.Action != "org.member.invite" {
			t.Errorf("action filter leaked %q", e.Action)
		}
	}
}

// TestQueryOrg_LimitBounded: an over-large or non-positive limit is clamped to
// the default, so a caller cannot pull an unbounded slice.
func TestQueryOrg_LimitBounded(t *testing.T) {
	ctx := context.Background()
	l, _ := openLogger(t)
	for i := 0; i < 60; i++ {
		_ = l.RecordOrg(ctx, "org-A", "a@x.com", "org.member.invite", "t", nil)
	}
	// Ask for a wildly oversized page; expect the default cap (50).
	got, err := l.QueryOrg(ctx, "org-A", auditlog.OrgQueryOptions{Limit: 100000})
	if err != nil {
		t.Fatalf("QueryOrg: %v", err)
	}
	if len(got) != 50 {
		t.Fatalf("oversized limit not clamped: got %d, want 50", len(got))
	}
}

// TestRecordOrg_ChainStaysTamperEvident: org rows participate in the hash chain
// and Verify catches tampering with a tenant_id (proving the tenant is bound
// into the tamper-evident hash — an org row can't be re-pointed at another org).
func TestRecordOrg_ChainStaysTamperEvident(t *testing.T) {
	ctx := context.Background()
	l, db := openLogger(t)

	_ = l.Record(ctx, "system", "pop.drain", "pop-1", nil) // legacy platform row
	for i := 0; i < 3; i++ {
		if err := l.RecordOrg(ctx, "org-A", "a@x.com", "org.member.invite", "t", nil); err != nil {
			t.Fatalf("RecordOrg %d: %v", i, err)
		}
	}
	if err := l.Verify(ctx, 0, math.MaxInt64); err != nil {
		t.Fatalf("clean chain (mixed platform+org rows): %v", err)
	}

	// Re-attribute an org row to another tenant via raw SQL. Because tenant_id is
	// hashed, Verify must now detect the tamper.
	if _, err := db.Exec(`UPDATE auditlog_entries SET tenant_id = 'org-EVIL' WHERE seq = 3`); err != nil {
		t.Fatalf("tamper: %v", err)
	}
	if err := l.Verify(ctx, 0, math.MaxInt64); err == nil {
		t.Fatal("Verify did not detect a tenant_id re-attribution — tenant is not bound into the hash")
	}
}
