package orgadmin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// dirMembers is a tenant-scoped MemberStore used by the directory tests: it
// knows ONE tenant's roster and returns an empty roster for any other tenant
// (mirroring the real fleet adapter, which surfaces a cross-tenant lookup as an
// empty org rather than an error).
type dirMembers struct {
	owner   string
	members []Member
}

func (m dirMembers) ListMembers(_ context.Context, tenant string) ([]Member, error) {
	if tenant != m.owner {
		return []Member{}, nil
	}
	return m.members, nil
}
func (dirMembers) Invite(_ context.Context, _, email, role, _ string) (InviteResponse, error) {
	return InviteResponse{ID: "inv", Email: email, Role: role, State: "pending"}, nil
}
func (dirMembers) RemoveMember(_ context.Context, _, _ string) error     { return nil }
func (dirMembers) SetRole(_ context.Context, _, _, _ string) error       { return nil }
func (dirMembers) SetMemberName(_ context.Context, _, _, _ string) error { return nil }

func sampleRoster() []Member {
	return []Member{
		{ID: "u-alice", Name: "Alice", Email: "alice@org.example", Role: "owner"},
		{ID: "u-bob", Name: "", Email: "bob@org.example", Role: "member"},
		{ID: "u-carol", Name: "Carol", Email: "carol@other.example", Role: "member"},
	}
}

func newDirService() *Service {
	return &Service{Members: dirMembers{owner: "tenant-1", members: sampleRoster()}}
}

// ── service-level: Directory ────────────────────────────────────────────────

func TestDirectory_ListsEntriesExcludingCaller(t *testing.T) {
	svc := newDirService()
	got, err := svc.Directory(context.Background(), "tenant-1", "u-alice", "")
	if err != nil {
		t.Fatalf("Directory: %v", err)
	}
	if got.Count != 2 || len(got.Members) != 2 {
		t.Fatalf("want 2 entries (caller excluded), got %d: %+v", got.Count, got.Members)
	}
	for _, e := range got.Members {
		if e.ID == "u-alice" {
			t.Fatalf("caller must be excluded from the directory: %+v", got.Members)
		}
	}
	// Bob has no display name → falls back to email.
	for _, e := range got.Members {
		if e.ID == "u-bob" && e.Name != "bob@org.example" {
			t.Fatalf("missing-name member must fall back to email, got %q", e.Name)
		}
	}
	// Sorted by display label: Bob (bob@…) before Carol.
	if got.Members[0].ID != "u-bob" || got.Members[1].ID != "u-carol" {
		t.Fatalf("entries not sorted by label: %+v", got.Members)
	}
}

func TestDirectory_FilterQ(t *testing.T) {
	svc := newDirService()
	got, err := svc.Directory(context.Background(), "tenant-1", "", "carol")
	if err != nil {
		t.Fatalf("Directory: %v", err)
	}
	if got.Count != 1 || got.Members[0].ID != "u-carol" {
		t.Fatalf("q=carol should match exactly Carol, got %+v", got.Members)
	}
}

func TestDirectory_CrossTenantIsEmpty(t *testing.T) {
	svc := newDirService()
	got, err := svc.Directory(context.Background(), "tenant-OTHER", "", "")
	if err != nil {
		t.Fatalf("Directory: %v", err)
	}
	if got.Members == nil || got.Count != 0 {
		t.Fatalf("cross-tenant directory must be empty non-nil, got %+v", got)
	}
}

func TestDirectory_NilMemberStore(t *testing.T) {
	svc := &Service{}
	got, err := svc.Directory(context.Background(), "tenant-1", "", "")
	if err != nil {
		t.Fatalf("Directory: %v", err)
	}
	if got.Members == nil || len(got.Members) != 0 {
		t.Fatalf("nil MemberStore must degrade to empty directory, got %+v", got)
	}
}

// ── service-level: ResolveShareTarget ───────────────────────────────────────

func TestResolveShareTarget_ByEmail(t *testing.T) {
	svc := newDirService()
	e, err := svc.ResolveShareTarget(context.Background(), "tenant-1", "ALICE@org.example")
	if err != nil {
		t.Fatalf("resolve by email: %v", err)
	}
	if e.ID != "u-alice" {
		t.Fatalf("want u-alice, got %+v", e)
	}
}

func TestResolveShareTarget_ByHandle(t *testing.T) {
	svc := newDirService()
	e, err := svc.ResolveShareTarget(context.Background(), "tenant-1", "bob")
	if err != nil {
		t.Fatalf("resolve by handle: %v", err)
	}
	if e.ID != "u-bob" {
		t.Fatalf("want u-bob, got %+v", e)
	}
}

func TestResolveShareTarget_AmbiguousHandleNotFound(t *testing.T) {
	// Two members share the local-part "dup" across different domains → the bare
	// handle is ambiguous and must NOT resolve.
	svc := &Service{Members: dirMembers{owner: "tenant-1", members: []Member{
		{ID: "u-1", Email: "dup@a.example"},
		{ID: "u-2", Email: "dup@b.example"},
	}}}
	if _, err := svc.ResolveShareTarget(context.Background(), "tenant-1", "dup"); err != ErrNotFound {
		t.Fatalf("ambiguous handle must be ErrNotFound, got %v", err)
	}
	// But the full email still resolves unambiguously.
	e, err := svc.ResolveShareTarget(context.Background(), "tenant-1", "dup@b.example")
	if err != nil || e.ID != "u-2" {
		t.Fatalf("exact email must resolve, got %+v err=%v", e, err)
	}
}

func TestResolveShareTarget_CrossTenantNotFound(t *testing.T) {
	svc := newDirService()
	// alice exists in tenant-1, but a session for tenant-OTHER must not resolve her.
	if _, err := svc.ResolveShareTarget(context.Background(), "tenant-OTHER", "alice@org.example"); err != ErrNotFound {
		t.Fatalf("cross-tenant resolve must be ErrNotFound, got %v", err)
	}
}

func TestResolveShareTarget_UnknownAndEmpty(t *testing.T) {
	svc := newDirService()
	if _, err := svc.ResolveShareTarget(context.Background(), "tenant-1", "nobody@org.example"); err != ErrNotFound {
		t.Fatalf("unknown email must be ErrNotFound, got %v", err)
	}
	if _, err := svc.ResolveShareTarget(context.Background(), "tenant-1", "   "); err != ErrNotFound {
		t.Fatalf("empty query must be ErrNotFound, got %v", err)
	}
}

// ── route-level ─────────────────────────────────────────────────────────────

func TestRoute_Directory(t *testing.T) {
	srv := newRouter(newDirService(), "tenant-1")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/org/directory", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var resp DirectoryResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// fixedGate has no CallerGate, so caller==tenant ("tenant-1"), which is not in
	// the roster — all three members are returned.
	if resp.Count != 3 {
		t.Fatalf("want 3 entries, got %d: %+v", resp.Count, resp.Members)
	}
}

func TestRoute_DirectoryUnauthenticated(t *testing.T) {
	srv := newRouter(newDirService(), "") // empty tenant ⇒ 401
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/org/directory", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestRoute_ResolveByEmail(t *testing.T) {
	srv := newRouter(newDirService(), "tenant-1")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/org/directory/resolve?email=alice@org.example", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var e DirectoryEntry
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if e.ID != "u-alice" {
		t.Fatalf("want u-alice, got %+v", e)
	}
}

func TestRoute_ResolveUnknownIs404(t *testing.T) {
	srv := newRouter(newDirService(), "tenant-1")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/org/directory/resolve?email=ghost@org.example", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestRoute_ResolveMissingParamIs400(t *testing.T) {
	srv := newRouter(newDirService(), "tenant-1")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/org/directory/resolve", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}
