package orgadmin

// pagination_test.go — PAGINATE-01: bounded member-roster pages + next-cursor.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// decode unmarshals a recorder's JSON body into dst.
func decode(t *testing.T, rr *httptest.ResponseRecorder, dst any) {
	t.Helper()
	if err := json.Unmarshal(rr.Body.Bytes(), dst); err != nil {
		t.Fatalf("decode body: %v (body=%s)", err, rr.Body.String())
	}
}

// manyMembers is a MemberStore returning N members in a stable order.
type manyMembers struct{ n int }

func (m manyMembers) ListMembers(_ context.Context, _ string) ([]Member, error) {
	out := make([]Member, m.n)
	for i := 0; i < m.n; i++ {
		out[i] = Member{ID: "m" + itoaP(i), Role: "member"}
	}
	return out, nil
}
func (manyMembers) Invite(_ context.Context, _, e, r, _ string) (InviteResponse, error) {
	return InviteResponse{Email: e, Role: r}, nil
}
func (manyMembers) RemoveMember(_ context.Context, _, _ string) error     { return nil }
func (manyMembers) SetRole(_ context.Context, _, _, _ string) error       { return nil }
func (manyMembers) SetMemberName(_ context.Context, _, _, _ string) error { return nil }

func itoaP(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

func newMembersRouter(n int) http.Handler {
	svc := NewService(manyMembers{n: n}, nil, nil, nil, nil, nil, nil)
	mux := http.NewServeMux()
	RegisterRoutes(mux, svc, fixedGate{tenant: "org-1"})
	return mux
}

// TestMembersPagination_BoundedAndCursor verifies a bounded page is returned,
// the page size is clamped to maxMemberPageSize, and the next-cursor advances
// across the full set until exhausted.
func TestMembersPagination_BoundedAndCursor(t *testing.T) {
	h := newMembersRouter(120)

	// First page of 50 (default).
	rr := do(t, h, http.MethodGet, "/api/org/members", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("page 1: status %d", rr.Code)
	}
	var p1 MembersResponse
	decode(t, rr, &p1)
	if len(p1.Members) != 50 {
		t.Fatalf("page 1: want 50 members, got %d", len(p1.Members))
	}
	if p1.NextCursor == "" {
		t.Fatal("page 1: expected a next_cursor (more pages remain)")
	}
	if p1.Members[0].ID != "m0" {
		t.Errorf("page 1: stable order broken, first=%s", p1.Members[0].ID)
	}

	// Second page via the cursor.
	rr2 := do(t, h, http.MethodGet, "/api/org/members?cursor="+p1.NextCursor, nil)
	var p2 MembersResponse
	decode(t, rr2, &p2)
	if len(p2.Members) != 50 {
		t.Fatalf("page 2: want 50, got %d", len(p2.Members))
	}
	if p2.Members[0].ID != "m50" {
		t.Errorf("page 2: want first=m50, got %s", p2.Members[0].ID)
	}

	// Third (last) page: 20 remaining → no further cursor.
	rr3 := do(t, h, http.MethodGet, "/api/org/members?cursor="+p2.NextCursor, nil)
	var p3 MembersResponse
	decode(t, rr3, &p3)
	if len(p3.Members) != 20 {
		t.Fatalf("page 3: want 20, got %d", len(p3.Members))
	}
	if p3.NextCursor != "" {
		t.Errorf("page 3: expected no next_cursor on the last page, got %q", p3.NextCursor)
	}
}

// TestMembersPagination_LimitClamped verifies an over-large client limit is
// clamped to maxMemberPageSize.
func TestMembersPagination_LimitClamped(t *testing.T) {
	h := newMembersRouter(500)
	rr := do(t, h, http.MethodGet, "/api/org/members?limit=99999", nil)
	var p MembersResponse
	decode(t, rr, &p)
	if len(p.Members) != maxMemberPageSize {
		t.Errorf("limit clamp: want %d members, got %d", maxMemberPageSize, len(p.Members))
	}
	if p.Limit != maxMemberPageSize {
		t.Errorf("limit clamp: reported limit %d, want %d", p.Limit, maxMemberPageSize)
	}
}

// TestMembersPagination_OffsetParam verifies the ?offset= path works alongside
// the cursor path.
func TestMembersPagination_OffsetParam(t *testing.T) {
	h := newMembersRouter(30)
	rr := do(t, h, http.MethodGet, "/api/org/members?offset=25&limit=10", nil)
	var p MembersResponse
	decode(t, rr, &p)
	if len(p.Members) != 5 {
		t.Fatalf("offset 25 of 30 with limit 10: want 5, got %d", len(p.Members))
	}
	if p.Members[0].ID != "m25" {
		t.Errorf("offset: want first=m25, got %s", p.Members[0].ID)
	}
	if p.NextCursor != "" {
		t.Errorf("offset: last partial page must have no next_cursor, got %q", p.NextCursor)
	}
}
