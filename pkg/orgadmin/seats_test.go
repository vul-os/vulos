package orgadmin

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

func rfc(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func TestCountActiveSeats(t *testing.T) {
	now := time.Date(2026, 6, 26, 12, 0, 0, 0, time.UTC)
	members := []Member{
		{ID: "a", Role: "owner", LastActive: rfc(now.AddDate(0, 0, -1))},   // active
		{ID: "b", Role: "member", LastActive: rfc(now.AddDate(0, 0, -29))}, // active (within 30d)
		{ID: "c", Role: "member", LastActive: rfc(now.AddDate(0, 0, -45))}, // dormant
		{ID: "d", Role: "member", LastActive: ""},                          // never active
		{ID: "e", Role: "admin", LastActive: "not-a-timestamp"},            // unparseable → inactive
	}
	billable, active, total := countActiveSeats(members, now, 30)
	if total != 5 {
		t.Fatalf("total = %d, want 5", total)
	}
	if active != 2 || billable != 2 {
		t.Fatalf("active/billable = %d/%d, want 2/2 (dormant + empty + bad-stamp excluded)", active, billable)
	}
}

func TestCountActiveSeats_Empty(t *testing.T) {
	billable, active, total := countActiveSeats(nil, time.Now(), 0)
	if billable != 0 || active != 0 || total != 0 {
		t.Fatalf("got %d/%d/%d, want 0/0/0 for empty roster", billable, active, total)
	}
}

func TestServiceSeats_NoBilling_CountsOnly(t *testing.T) {
	now := time.Now().UTC()
	svc := &Service{
		Members: &tenantScopedMembers{owner: "t1", members: []Member{
			{ID: "a", Role: "owner", LastActive: rfc(now.AddDate(0, 0, -1))},
			{ID: "b", Role: "member", LastActive: rfc(now.AddDate(0, 0, -2))},
			{ID: "c", Role: "member", LastActive: rfc(now.AddDate(0, 0, -90))}, // dormant
		}},
	}
	got, err := svc.Seats(context.Background(), "t1")
	if err != nil {
		t.Fatal(err)
	}
	if got.BillableSeats != 2 {
		t.Fatalf("billable = %d, want 2 (dormant excluded)", got.BillableSeats)
	}
	if got.TotalMembers != 3 {
		t.Fatalf("total = %d, want 3", got.TotalMembers)
	}
	if got.Tier != "free" {
		t.Fatalf("tier = %q, want free (no billing summarizer wired)", got.Tier)
	}
	// Mail is never seat-billed.
	if got.MailSeatBilled {
		t.Fatal("MailSeatBilled must be false — mail is per-user external, not a seat cost")
	}
}

// TestSeatsRoute_Unauthenticated confirms GET /api/org/seats is session-gated.
func TestSeatsRoute_Unauthenticated(t *testing.T) {
	h := newRouter(fullService(), "") // empty tenant ⇒ gate writes 401
	if rec := do(t, h, http.MethodGet, "/api/org/seats", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

// TestSeatsRoute_TenantScoped confirms the count comes from the SESSION tenant's
// roster (never a client-supplied tenant) and serialises the documented shape.
func TestSeatsRoute_TenantScoped(t *testing.T) {
	now := time.Now().UTC()
	svc := &Service{
		Members: &tenantScopedMembers{owner: "tenant-a", members: []Member{
			{ID: "m1", Role: "owner", LastActive: rfc(now)},
			{ID: "m2", Role: "member", LastActive: rfc(now.AddDate(0, 0, -5))},
		}},
	}
	h := newRouter(svc, "tenant-a")
	rec := do(t, h, http.MethodGet, "/api/org/seats", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var out SeatsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.BillableSeats != 2 || out.TotalMembers != 2 {
		t.Fatalf("seats = %d/%d, want 2/2", out.BillableSeats, out.TotalMembers)
	}

	// A different session tenant has zero members in this fake → zero seats,
	// proving the count is scoped to the session tenant.
	h2 := newRouter(svc, "tenant-b")
	rec2 := do(t, h2, http.MethodGet, "/api/org/seats", nil)
	var out2 SeatsResponse
	_ = json.Unmarshal(rec2.Body.Bytes(), &out2)
	if out2.BillableSeats != 0 {
		t.Fatalf("cross-tenant billable = %d, want 0", out2.BillableSeats)
	}
}
