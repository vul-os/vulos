// residency_test.go — unit tests for the residency package.
//
// Tests cover MemStore and SQLStore (in-memory DSN) against the same table of
// behaviours, plus IsValidRegion, DataFlowMap, and ErrInvalidRegion.
package residency_test

import (
	"context"
	"errors"
	"testing"

	"github.com/vul-os/vulos-management/pkg/cpdb"
	"github.com/vul-os/vulos-management/pkg/residency"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func openMemSQL(t *testing.T) residency.Store {
	t.Helper()
	db, err := cpdb.OpenSQLiteDSN(":memory:")
	if err != nil {
		t.Fatalf("cpdb open: %v", err)
	}
	st, err := residency.OpenSQLStore(db)
	if err != nil {
		db.Close()
		t.Fatalf("OpenSQLStore: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// storeImpls returns both implementations to run the same suite on each.
func storeImpls(t *testing.T) []struct {
	name string
	st   residency.Store
} {
	t.Helper()
	return []struct {
		name string
		st   residency.Store
	}{
		{"MemStore", residency.NewMemStore()},
		{"SQLStore", openMemSQL(t)},
	}
}

// ---------------------------------------------------------------------------
// IsValidRegion
// ---------------------------------------------------------------------------

func TestIsValidRegion(t *testing.T) {
	cases := []struct {
		region string
		want   bool
	}{
		{"af-south-1", true},
		{"eu-west-1", true},
		{"us-east-1", true},
		{"ap-southeast-1", true},
		{"auto", true},
		{"", false},
		{"us-west-99", false},
		{"af-south", false}, // partial match must not pass
	}
	for _, c := range cases {
		if got := residency.IsValidRegion(c.region); got != c.want {
			t.Errorf("IsValidRegion(%q) = %v, want %v", c.region, got, c.want)
		}
	}
}

// ---------------------------------------------------------------------------
// Store: GetRegion on unknown org
// ---------------------------------------------------------------------------

func TestStore_GetRegion_UnknownOrg(t *testing.T) {
	for _, tc := range storeImpls(t) {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.st.GetRegion(context.Background(), "org-does-not-exist")
			if !errors.Is(err, residency.ErrUnknownOrg) {
				t.Errorf("expected ErrUnknownOrg, got %v", err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Store: SetRegion + GetRegion round-trip
// ---------------------------------------------------------------------------

func TestStore_SetAndGetRegion(t *testing.T) {
	for _, tc := range storeImpls(t) {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			orgID := "org-set-get"

			if err := tc.st.SetRegion(ctx, orgID, "eu-west-1"); err != nil {
				t.Fatalf("SetRegion: %v", err)
			}

			got, err := tc.st.GetRegion(ctx, orgID)
			if err != nil {
				t.Fatalf("GetRegion: %v", err)
			}
			if got.Region != "eu-west-1" {
				t.Errorf("expected region 'eu-west-1', got %q", got.Region)
			}
			if got.OrgID != orgID {
				t.Errorf("expected org_id %q, got %q", orgID, got.OrgID)
			}
			if got.UpdatedAt.IsZero() {
				t.Error("UpdatedAt should be non-zero")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Store: SetRegion is immutable after first set (item 11)
//   - same-value re-set is an idempotent no-op
//   - a CHANGE is rejected with ErrRegionLocked (split-brain guard)
// ---------------------------------------------------------------------------

func TestStore_SetRegion_Update(t *testing.T) {
	for _, tc := range storeImpls(t) {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			orgID := "org-update"

			if err := tc.st.SetRegion(ctx, orgID, "us-east-1"); err != nil {
				t.Fatalf("first SetRegion: %v", err)
			}
			// Idempotent same-value re-set is allowed.
			if err := tc.st.SetRegion(ctx, orgID, "us-east-1"); err != nil {
				t.Fatalf("same-value SetRegion should be a no-op: %v", err)
			}
			// Changing the region after first set is rejected.
			if err := tc.st.SetRegion(ctx, orgID, "af-south-1"); !errors.Is(err, residency.ErrRegionLocked) {
				t.Fatalf("change SetRegion: want ErrRegionLocked, got %v", err)
			}

			got, err := tc.st.GetRegion(ctx, orgID)
			if err != nil {
				t.Fatalf("GetRegion: %v", err)
			}
			if got.Region != "us-east-1" {
				t.Errorf("region must remain 'us-east-1', got %q", got.Region)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Store: SetRegion with invalid region → ErrInvalidRegion
// ---------------------------------------------------------------------------

func TestStore_SetRegion_InvalidRegion(t *testing.T) {
	for _, tc := range storeImpls(t) {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.st.SetRegion(context.Background(), "org-invalid", "mars-north-99")
			if !errors.Is(err, residency.ErrInvalidRegion) {
				t.Errorf("expected ErrInvalidRegion, got %v", err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// DataFlowMap — non-empty, all IDs unique
// ---------------------------------------------------------------------------

func TestDataFlowMap_NonEmpty(t *testing.T) {
	flows := residency.DataFlowMap()
	if len(flows) == 0 {
		t.Fatal("DataFlowMap returned empty slice")
	}

	seen := map[string]bool{}
	for _, f := range flows {
		if f.ID == "" {
			t.Error("DataFlow with empty ID")
		}
		if f.Description == "" {
			t.Errorf("DataFlow %q has empty Description", f.ID)
		}
		if f.RegionBinding == "" {
			t.Errorf("DataFlow %q has empty RegionBinding", f.ID)
		}
		if seen[f.ID] {
			t.Errorf("duplicate DataFlow ID %q", f.ID)
		}
		seen[f.ID] = true
	}
}

// ---------------------------------------------------------------------------
// DataFlowMap — every org-setting flow is documented
// ---------------------------------------------------------------------------

func TestDataFlowMap_OrgSettingFlowsExist(t *testing.T) {
	flows := residency.DataFlowMap()
	orgSettingCount := 0
	for _, f := range flows {
		if f.RegionBinding == "org-setting" {
			orgSettingCount++
		}
	}
	if orgSettingCount == 0 {
		t.Error("expected at least one DataFlow with RegionBinding='org-setting'")
	}
}

// ---------------------------------------------------------------------------
// SupportedRegions — contains expected canonical entries
// ---------------------------------------------------------------------------

func TestSupportedRegions_ContainsCanonical(t *testing.T) {
	required := []string{"af-south-1", "eu-west-1", "us-east-1", "auto"}
	for _, r := range required {
		if !residency.IsValidRegion(r) {
			t.Errorf("SupportedRegions missing expected region %q", r)
		}
	}
}
