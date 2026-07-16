package multiloc_test

import (
	"context"
	"testing"
	"time"

	"github.com/vul-os/vulos-management/pkg/cpdb"
	"github.com/vul-os/vulos-management/pkg/multiloc"
)

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

func openTestStore(t *testing.T) multiloc.Store {
	t.Helper()
	db, err := cpdb.OpenSQLiteDSN(":memory:")
	if err != nil {
		t.Fatalf("cpdb open: %v", err)
	}
	st, err := multiloc.OpenStore(db)
	if err != nil {
		db.Close()
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// storeFactories returns both the SQL and Mem implementations so every test
// runs against both.
func storeFactories(t *testing.T) []struct {
	name  string
	store multiloc.Store
} {
	t.Helper()
	return []struct {
		name  string
		store multiloc.Store
	}{
		{"SQLStore", openTestStore(t)},
		{"MemStore", multiloc.NewMemStore()},
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Tests
// ─────────────────────────────────────────────────────────────────────────────

// TestEnroll_NewLocation verifies that a new location can be enrolled.
func TestEnroll_NewLocation(t *testing.T) {
	for _, tc := range storeFactories(t) {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			req := multiloc.EnrollRequest{
				OrgID:      "org-1",
				LocationID: "loc-a",
				Name:       "EU-West-1",
				Endpoint:   "https://eu1.example.com",
			}
			loc, err := tc.store.Enroll(ctx, req)
			if err != nil {
				t.Fatalf("Enroll: %v", err)
			}
			if loc.OrgID != req.OrgID {
				t.Errorf("OrgID = %q, want %q", loc.OrgID, req.OrgID)
			}
			if loc.LocationID != req.LocationID {
				t.Errorf("LocationID = %q, want %q", loc.LocationID, req.LocationID)
			}
			if loc.Healthy {
				t.Error("newly enrolled location should not be healthy")
			}
			if loc.EnrolledAt.IsZero() {
				t.Error("EnrolledAt should not be zero")
			}
		})
	}
}

// TestEnroll_AlreadyEnrolled verifies that ErrAlreadyEnrolled is returned on
// duplicate enrollment.
func TestEnroll_AlreadyEnrolled(t *testing.T) {
	for _, tc := range storeFactories(t) {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			req := multiloc.EnrollRequest{
				OrgID: "org-dup", LocationID: "loc-dup",
				Name: "Dup", Endpoint: "https://dup.example.com",
			}
			if _, err := tc.store.Enroll(ctx, req); err != nil {
				t.Fatalf("first Enroll: %v", err)
			}
			_, err := tc.store.Enroll(ctx, req)
			if err == nil {
				t.Fatal("expected ErrAlreadyEnrolled, got nil")
			}
			if err != multiloc.ErrAlreadyEnrolled {
				t.Errorf("got %v, want ErrAlreadyEnrolled", err)
			}
		})
	}
}

// TestList returns all enrolled locations for an org.
func TestList(t *testing.T) {
	for _, tc := range storeFactories(t) {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			reqs := []multiloc.EnrollRequest{
				{OrgID: "org-list", LocationID: "loc-1", Name: "L1", Endpoint: "https://l1.example.com"},
				{OrgID: "org-list", LocationID: "loc-2", Name: "L2", Endpoint: "https://l2.example.com"},
			}
			for _, req := range reqs {
				if _, err := tc.store.Enroll(ctx, req); err != nil {
					t.Fatalf("Enroll %s: %v", req.LocationID, err)
				}
				// Small sleep so enrolled_at ordering is stable in MemStore.
				time.Sleep(time.Millisecond)
			}

			locs, err := tc.store.List(ctx, "org-list")
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			if len(locs) != 2 {
				t.Errorf("len = %d, want 2", len(locs))
			}
		})
	}
}

// TestHealthyFilter verifies that HealthyLocations filters correctly.
func TestHealthyFilter(t *testing.T) {
	for _, tc := range storeFactories(t) {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			for _, id := range []string{"loc-h1", "loc-h2", "loc-u1"} {
				_, err := tc.store.Enroll(ctx, multiloc.EnrollRequest{
					OrgID: "org-hf", LocationID: id,
					Name: id, Endpoint: "https://" + id + ".example.com",
				})
				if err != nil {
					t.Fatalf("Enroll %s: %v", id, err)
				}
			}
			// Mark two as healthy.
			for _, id := range []string{"loc-h1", "loc-h2"} {
				if err := tc.store.MarkHealth(ctx, "org-hf", id, true); err != nil {
					t.Fatalf("MarkHealth %s: %v", id, err)
				}
			}
			healthy, err := tc.store.HealthyLocations(ctx, "org-hf")
			if err != nil {
				t.Fatalf("HealthyLocations: %v", err)
			}
			if len(healthy) != 2 {
				t.Errorf("len(healthy) = %d, want 2", len(healthy))
			}
			for _, loc := range healthy {
				if !loc.Healthy {
					t.Errorf("location %s marked healthy=false in HealthyLocations result", loc.LocationID)
				}
			}
		})
	}
}

// TestMarkHealth_NotFound verifies ErrLocationNotFound on unknown location.
func TestMarkHealth_NotFound(t *testing.T) {
	for _, tc := range storeFactories(t) {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			err := tc.store.MarkHealth(ctx, "org-x", "no-such-loc", true)
			if err == nil {
				t.Fatal("expected ErrLocationNotFound, got nil")
			}
			if err != multiloc.ErrLocationNotFound {
				t.Errorf("got %v, want ErrLocationNotFound", err)
			}
		})
	}
}

// TestAllDown verifies ErrAllLocationsUnhealthy when no location is healthy.
func TestAllDown(t *testing.T) {
	for _, tc := range storeFactories(t) {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			// Enroll one location, but do not mark it healthy.
			if _, err := tc.store.Enroll(ctx, multiloc.EnrollRequest{
				OrgID: "org-down", LocationID: "loc-d",
				Name: "Down", Endpoint: "https://down.example.com",
			}); err != nil {
				t.Fatalf("Enroll: %v", err)
			}
			_, err := tc.store.PickLocation(ctx, "org-down")
			if err == nil {
				t.Fatal("expected ErrAllLocationsUnhealthy, got nil")
			}
			if err != multiloc.ErrAllLocationsUnhealthy {
				t.Errorf("got %v, want ErrAllLocationsUnhealthy", err)
			}
		})
	}
}

// TestCrossOrgIsolation verifies that locations for org-A are not visible to org-B.
func TestCrossOrgIsolation(t *testing.T) {
	for _, tc := range storeFactories(t) {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			// Enroll loc for org-a.
			if _, err := tc.store.Enroll(ctx, multiloc.EnrollRequest{
				OrgID: "org-a", LocationID: "loc-a",
				Name: "A", Endpoint: "https://a.example.com",
			}); err != nil {
				t.Fatalf("Enroll org-a: %v", err)
			}
			// org-b should see no locations.
			locs, err := tc.store.List(ctx, "org-b")
			if err != nil {
				t.Fatalf("List org-b: %v", err)
			}
			if len(locs) != 0 {
				t.Errorf("org-b sees %d locations, want 0", len(locs))
			}
			// MarkHealth on org-b's unknown loc should be ErrLocationNotFound.
			if err := tc.store.MarkHealth(ctx, "org-b", "loc-a", true); err != multiloc.ErrLocationNotFound {
				t.Errorf("MarkHealth cross-org: got %v, want ErrLocationNotFound", err)
			}
		})
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Routing tests
// ─────────────────────────────────────────────────────────────────────────────

// TestPickLocation_PicksHealthy verifies PickLocation returns a healthy location.
func TestPickLocation_PicksHealthy(t *testing.T) {
	ctx := context.Background()
	st := multiloc.NewMemStore()

	for _, id := range []string{"loc-p1", "loc-p2"} {
		if _, err := st.Enroll(ctx, multiloc.EnrollRequest{
			OrgID: "org-pick", LocationID: id,
			Name: id, Endpoint: "https://" + id + ".example.com",
		}); err != nil {
			t.Fatalf("Enroll %s: %v", id, err)
		}
	}
	// Only mark loc-p2 healthy.
	if err := st.MarkHealth(ctx, "org-pick", "loc-p2", true); err != nil {
		t.Fatalf("MarkHealth: %v", err)
	}

	loc, err := st.PickLocation(ctx, "org-pick")
	if err != nil {
		t.Fatalf("PickLocation: %v", err)
	}
	if loc.LocationID != "loc-p2" {
		t.Errorf("got %q, want loc-p2", loc.LocationID)
	}
}

// TestPickLocation_FailoverWhenPrimaryDown verifies that a secondary is picked
// when the primary is unhealthy.
func TestPickLocation_FailoverWhenPrimaryDown(t *testing.T) {
	ctx := context.Background()
	st := multiloc.NewMemStore()

	reqs := []multiloc.EnrollRequest{
		{OrgID: "org-fo", LocationID: "primary", Name: "Primary", Endpoint: "https://primary.example.com"},
		{OrgID: "org-fo", LocationID: "secondary", Name: "Secondary", Endpoint: "https://secondary.example.com"},
	}
	for _, req := range reqs {
		if _, err := st.Enroll(ctx, req); err != nil {
			t.Fatalf("Enroll %s: %v", req.LocationID, err)
		}
	}
	// Mark primary healthy, then mark it unhealthy (simulate failure).
	if err := st.MarkHealth(ctx, "org-fo", "primary", true); err != nil {
		t.Fatalf("MarkHealth primary up: %v", err)
	}
	if err := st.MarkHealth(ctx, "org-fo", "primary", false); err != nil {
		t.Fatalf("MarkHealth primary down: %v", err)
	}
	// Mark secondary healthy.
	if err := st.MarkHealth(ctx, "org-fo", "secondary", true); err != nil {
		t.Fatalf("MarkHealth secondary: %v", err)
	}

	loc, err := st.PickLocation(ctx, "org-fo")
	if err != nil {
		t.Fatalf("PickLocation: %v", err)
	}
	if loc.LocationID != "secondary" {
		t.Errorf("got %q, want secondary", loc.LocationID)
	}
}

// TestPickLocation_AllDown_Hold verifies ErrAllLocationsUnhealthy (hold in queue).
func TestPickLocation_AllDown_Hold(t *testing.T) {
	ctx := context.Background()
	st := multiloc.NewMemStore()

	// Enroll two locations, neither healthy.
	for _, id := range []string{"loc-1", "loc-2"} {
		if _, err := st.Enroll(ctx, multiloc.EnrollRequest{
			OrgID: "org-alldown", LocationID: id,
			Name: id, Endpoint: "https://" + id + ".example.com",
		}); err != nil {
			t.Fatalf("Enroll %s: %v", id, err)
		}
	}

	_, err := st.PickLocation(ctx, "org-alldown")
	if err != multiloc.ErrAllLocationsUnhealthy {
		t.Errorf("got %v, want ErrAllLocationsUnhealthy", err)
	}
}
