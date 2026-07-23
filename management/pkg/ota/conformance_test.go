package ota

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vul-os/vulos-management/pkg/cpdb"
)

// ─────────────────────────────────────────────────────────────────────────────
// Store factories
// ─────────────────────────────────────────────────────────────────────────────

type storeFactory struct {
	name    string
	newFunc func() Store
}

var sqlConfSeq atomic.Int64

var storeFactories = []storeFactory{
	{"MemStore", func() Store { return NewMemStore() }},
	{"SQLStore", func() Store {
		dsn := fmt.Sprintf("file:otaconf%d?mode=memory&cache=shared", sqlConfSeq.Add(1))
		db, err := cpdb.OpenSQLiteDSN(dsn)
		if err != nil {
			panic("ota: cpdb.OpenSQLiteDSN: " + err.Error())
		}
		s, err := Open(db)
		if err != nil {
			panic("ota: Open: " + err.Error())
		}
		return s
	}},
}

// ─────────────────────────────────────────────────────────────────────────────
// TestStoreConformance runs every sub-case against every registered Store.
// ─────────────────────────────────────────────────────────────────────────────

func TestStoreConformance(t *testing.T) {
	t.Parallel()
	for _, sf := range storeFactories {
		sf := sf
		t.Run(sf.name, func(t *testing.T) {
			t.Parallel()
			storeConformance(t, sf.newFunc)
		})
	}
}

func storeConformance(t *testing.T, newStore func() Store) {
	t.Helper()
	ctx := context.Background()

	// ─── InsertRelease ────────────────────────────────────────────────────────

	t.Run("InsertRelease/happy-path", func(t *testing.T) {
		s := newStore()
		defer s.Close()
		r := baseRelease()
		id, err := s.InsertRelease(ctx, r)
		if err != nil {
			t.Fatalf("InsertRelease: %v", err)
		}
		if id <= 0 {
			t.Errorf("InsertRelease returned id %d; want > 0", id)
		}
	})

	t.Run("InsertRelease/duplicate-returns-error", func(t *testing.T) {
		s := newStore()
		defer s.Close()
		r := baseRelease()
		if _, err := s.InsertRelease(ctx, r); err != nil {
			t.Fatalf("first insert: %v", err)
		}
		_, err := s.InsertRelease(ctx, r)
		if !errors.Is(err, ErrDuplicateRelease) {
			t.Fatalf("second insert = %v; want ErrDuplicateRelease", err)
		}
	})

	t.Run("InsertRelease/different-channels-allowed", func(t *testing.T) {
		s := newStore()
		defer s.Close()
		r1 := baseRelease()
		r1.Channel = "stable"
		r2 := baseRelease()
		r2.Channel = "beta"
		if _, err := s.InsertRelease(ctx, r1); err != nil {
			t.Fatalf("stable insert: %v", err)
		}
		if _, err := s.InsertRelease(ctx, r2); err != nil {
			t.Fatalf("beta insert (same version, different channel): %v", err)
		}
	})

	// ─── HaltRelease ─────────────────────────────────────────────────────────

	t.Run("HaltRelease/happy-path", func(t *testing.T) {
		s := newStore()
		defer s.Close()
		id, _ := s.InsertRelease(ctx, baseRelease())
		if err := s.HaltRelease(ctx, id); err != nil {
			t.Fatalf("HaltRelease: %v", err)
		}
		// Halted release must not appear in feed.
		_, err := s.FeedFor(ctx, testULID1, "stable", "1.0.0")
		if !errors.Is(err, ErrNoUpgrade) {
			t.Fatalf("FeedFor(halted) = %v; want ErrNoUpgrade", err)
		}
	})

	t.Run("HaltRelease/unknown-returns-error", func(t *testing.T) {
		s := newStore()
		defer s.Close()
		if err := s.HaltRelease(ctx, 99999); !errors.Is(err, ErrReleaseNotFound) {
			t.Fatalf("HaltRelease(unknown) = %v; want ErrReleaseNotFound", err)
		}
	})

	// ─── GetPolicy / SetPolicy ────────────────────────────────────────────────

	t.Run("GetPolicy/unknown-ulid", func(t *testing.T) {
		s := newStore()
		defer s.Close()
		_, err := s.GetPolicy(ctx, testULID1)
		if !errors.Is(err, ErrUnknownULID) {
			t.Fatalf("GetPolicy(unknown) = %v; want ErrUnknownULID", err)
		}
	})

	t.Run("SetPolicy/and-get", func(t *testing.T) {
		s := newStore()
		defer s.Close()
		p := DevicePolicy{
			ULID:           testULID1,
			Channel:        "beta",
			PinVersion:     "1.0.1",
			OptOutFeatures: true,
		}
		if err := s.SetPolicy(ctx, p); err != nil {
			t.Fatalf("SetPolicy: %v", err)
		}
		got, err := s.GetPolicy(ctx, testULID1)
		if err != nil {
			t.Fatalf("GetPolicy: %v", err)
		}
		if got.Channel != "beta" {
			t.Errorf("Channel = %q; want beta", got.Channel)
		}
		if got.PinVersion != "1.0.1" {
			t.Errorf("PinVersion = %q; want 1.0.1", got.PinVersion)
		}
		if !got.OptOutFeatures {
			t.Error("OptOutFeatures should be true")
		}
		if got.UpdatedAt.IsZero() {
			t.Error("UpdatedAt should not be zero")
		}
	})

	t.Run("SetPolicy/defer-until-roundtrip", func(t *testing.T) {
		s := newStore()
		defer s.Close()
		future := time.Now().Add(48 * time.Hour).Truncate(time.Second)
		p := DevicePolicy{
			ULID:       testULID1,
			Channel:    "stable",
			DeferUntil: &future,
		}
		if err := s.SetPolicy(ctx, p); err != nil {
			t.Fatalf("SetPolicy: %v", err)
		}
		got, err := s.GetPolicy(ctx, testULID1)
		if err != nil {
			t.Fatalf("GetPolicy: %v", err)
		}
		if got.DeferUntil == nil {
			t.Fatal("DeferUntil should not be nil")
		}
		diff := got.DeferUntil.Sub(future)
		if diff < -time.Second || diff > time.Second {
			t.Errorf("DeferUntil round-trip diff %v; want within 1s", diff)
		}
	})

	t.Run("SetPolicy/idempotent-overwrite", func(t *testing.T) {
		s := newStore()
		defer s.Close()
		p1 := DevicePolicy{ULID: testULID1, Channel: "stable"}
		if err := s.SetPolicy(ctx, p1); err != nil {
			t.Fatalf("first SetPolicy: %v", err)
		}
		p2 := DevicePolicy{ULID: testULID1, Channel: "beta", PinVersion: "2.0.0"}
		if err := s.SetPolicy(ctx, p2); err != nil {
			t.Fatalf("second SetPolicy: %v", err)
		}
		got, err := s.GetPolicy(ctx, testULID1)
		if err != nil {
			t.Fatalf("GetPolicy: %v", err)
		}
		if got.Channel != "beta" {
			t.Errorf("Channel after overwrite = %q; want beta", got.Channel)
		}
		if got.PinVersion != "2.0.0" {
			t.Errorf("PinVersion after overwrite = %q; want 2.0.0", got.PinVersion)
		}
	})

	// ─── FeedFor ─────────────────────────────────────────────────────────────

	t.Run("FeedFor/upgrade-available", func(t *testing.T) {
		s := newStore()
		defer s.Close()
		if _, err := s.InsertRelease(ctx, baseRelease()); err != nil {
			t.Fatalf("insert: %v", err)
		}
		rel, err := s.FeedFor(ctx, testULID1, "stable", "1.0.0")
		if err != nil {
			t.Fatalf("FeedFor: %v", err)
		}
		if rel.Version != "1.2.0" {
			t.Errorf("version = %q; want 1.2.0", rel.Version)
		}
		if rel.Sha256 == "" {
			t.Error("Sha256 should not be empty")
		}
	})

	t.Run("FeedFor/no-upgrade-when-current", func(t *testing.T) {
		s := newStore()
		defer s.Close()
		if _, err := s.InsertRelease(ctx, baseRelease()); err != nil {
			t.Fatalf("insert: %v", err)
		}
		_, err := s.FeedFor(ctx, testULID1, "stable", "1.2.0")
		if !errors.Is(err, ErrNoUpgrade) {
			t.Fatalf("FeedFor(current) = %v; want ErrNoUpgrade", err)
		}
	})

	t.Run("FeedFor/selects-highest-version", func(t *testing.T) {
		s := newStore()
		defer s.Close()
		r1 := baseRelease()
		r1.Version = "1.2.0"
		r2 := baseRelease()
		r2.Version = "1.3.0"
		if _, err := s.InsertRelease(ctx, r1); err != nil {
			t.Fatalf("insert r1: %v", err)
		}
		if _, err := s.InsertRelease(ctx, r2); err != nil {
			t.Fatalf("insert r2: %v", err)
		}
		rel, err := s.FeedFor(ctx, testULID1, "stable", "1.0.0")
		if err != nil {
			t.Fatalf("FeedFor: %v", err)
		}
		if rel.Version != "1.3.0" {
			t.Errorf("FeedFor should pick highest version; got %q", rel.Version)
		}
	})

	t.Run("FeedFor/channel-isolation", func(t *testing.T) {
		s := newStore()
		defer s.Close()
		rb := baseRelease()
		rb.Channel = "beta"
		rb.Version = "2.0.0"
		if _, err := s.InsertRelease(ctx, rb); err != nil {
			t.Fatalf("insert beta: %v", err)
		}
		// Asking for stable channel should get no upgrade (only beta has releases).
		_, err := s.FeedFor(ctx, testULID1, "stable", "1.0.0")
		if !errors.Is(err, ErrNoUpgrade) {
			t.Fatalf("FeedFor(stable, only beta release) = %v; want ErrNoUpgrade", err)
		}
	})

	t.Run("FeedFor/security-overrides-pin", func(t *testing.T) {
		s := newStore()
		defer s.Close()
		r := baseRelease()
		r.Version = "1.3.0"
		r.Security = true
		if _, err := s.InsertRelease(ctx, r); err != nil {
			t.Fatalf("insert: %v", err)
		}
		if err := s.SetPolicy(ctx, DevicePolicy{
			ULID:       testULID1,
			Channel:    "stable",
			PinVersion: "1.2.0",
		}); err != nil {
			t.Fatalf("SetPolicy: %v", err)
		}
		rel, err := s.FeedFor(ctx, testULID1, "stable", "1.0.0")
		if err != nil {
			t.Fatalf("FeedFor(security overrides pin): %v", err)
		}
		if rel.Version != "1.3.0" {
			t.Errorf("version = %q; want 1.3.0", rel.Version)
		}
	})

	t.Run("FeedFor/security-overrides-defer", func(t *testing.T) {
		s := newStore()
		defer s.Close()
		r := baseRelease()
		r.Version = "1.3.0"
		r.Security = true
		if _, err := s.InsertRelease(ctx, r); err != nil {
			t.Fatalf("insert: %v", err)
		}
		future := time.Now().Add(24 * time.Hour)
		if err := s.SetPolicy(ctx, DevicePolicy{
			ULID:       testULID1,
			Channel:    "stable",
			DeferUntil: &future,
		}); err != nil {
			t.Fatalf("SetPolicy: %v", err)
		}
		rel, err := s.FeedFor(ctx, testULID1, "stable", "1.0.0")
		if err != nil {
			t.Fatalf("FeedFor(security overrides defer): %v", err)
		}
		if rel.Version != "1.3.0" {
			t.Errorf("version = %q; want 1.3.0", rel.Version)
		}
	})

	t.Run("FeedFor/halted-not-served", func(t *testing.T) {
		s := newStore()
		defer s.Close()
		id, _ := s.InsertRelease(ctx, baseRelease())
		if err := s.HaltRelease(ctx, id); err != nil {
			t.Fatalf("HaltRelease: %v", err)
		}
		_, err := s.FeedFor(ctx, testULID1, "stable", "1.0.0")
		if !errors.Is(err, ErrNoUpgrade) {
			t.Fatalf("FeedFor(halted) = %v; want ErrNoUpgrade", err)
		}
	})

	// ─── InsertReport ─────────────────────────────────────────────────────────

	t.Run("InsertReport/happy-path", func(t *testing.T) {
		s := newStore()
		defer s.Close()
		if err := s.InsertReport(ctx, testULID1, "1.2.0", "applied"); err != nil {
			t.Fatalf("InsertReport: %v", err)
		}
		if err := s.InsertReport(ctx, testULID1, "1.2.0", "failed"); err != nil {
			t.Fatalf("InsertReport(failed): %v", err)
		}
		if err := s.InsertReport(ctx, testULID1, "1.2.0", "rolled-back"); err != nil {
			t.Fatalf("InsertReport(rolled-back): %v", err)
		}
	})

	// ─── ListReleases ─────────────────────────────────────────────────────────

	t.Run("ListReleases/empty", func(t *testing.T) {
		s := newStore()
		defer s.Close()
		list, err := s.ListReleases(ctx, 0, 0)
		if err != nil {
			t.Fatalf("ListReleases(empty): %v", err)
		}
		if len(list) != 0 {
			t.Errorf("expected empty list, got %d", len(list))
		}
	})

	t.Run("ListReleases/returns-all-ordered-by-id-desc", func(t *testing.T) {
		s := newStore()
		defer s.Close()
		// Insert 3 releases.
		for _, ver := range []string{"1.0.0", "1.1.0", "1.2.0"} {
			r := baseRelease()
			r.Version = ver
			if ver == "1.1.0" {
				r.Channel = "beta" // avoid duplicate (version, channel)
			} else if ver == "1.2.0" {
				r.Channel = "pinned"
			}
			if _, err := s.InsertRelease(ctx, r); err != nil {
				t.Fatalf("InsertRelease(%s): %v", ver, err)
			}
		}
		list, err := s.ListReleases(ctx, 0, 0)
		if err != nil {
			t.Fatalf("ListReleases: %v", err)
		}
		if len(list) != 3 {
			t.Fatalf("want 3 releases, got %d", len(list))
		}
		// IDs must be descending.
		for i := 1; i < len(list); i++ {
			if list[i-1].ID <= list[i].ID {
				t.Errorf("releases not ordered by ID desc: list[%d].ID=%d list[%d].ID=%d",
					i-1, list[i-1].ID, i, list[i].ID)
			}
		}
	})

	t.Run("ListReleases/pagination", func(t *testing.T) {
		s := newStore()
		defer s.Close()
		// Insert 4 releases on different channels to avoid duplicate error.
		versions := []string{"1.0.0", "1.1.0", "1.2.0", "1.3.0"}
		channels := []string{"stable", "beta", "pinned", "stable"}
		vchans := [][2]string{{"1.0.0", "stable"}, {"1.1.0", "beta"}, {"1.2.0", "pinned"}, {"1.3.0", "beta"}}
		_ = versions
		_ = channels
		for _, vc := range vchans {
			r := baseRelease()
			r.Version = vc[0]
			r.Channel = vc[1]
			if _, err := s.InsertRelease(ctx, r); err != nil {
				t.Fatalf("InsertRelease(%s/%s): %v", vc[0], vc[1], err)
			}
		}
		// Page 1: limit=2 offset=0 → 2 items.
		page1, err := s.ListReleases(ctx, 2, 0)
		if err != nil {
			t.Fatalf("ListReleases page1: %v", err)
		}
		if len(page1) != 2 {
			t.Errorf("page1 want 2, got %d", len(page1))
		}
		// Page 2: limit=2 offset=2 → 2 items.
		page2, err := s.ListReleases(ctx, 2, 2)
		if err != nil {
			t.Fatalf("ListReleases page2: %v", err)
		}
		if len(page2) != 2 {
			t.Errorf("page2 want 2, got %d", len(page2))
		}
		// IDs must not overlap between pages.
		seen := map[int64]bool{}
		for _, r := range page1 {
			seen[r.ID] = true
		}
		for _, r := range page2 {
			if seen[r.ID] {
				t.Errorf("release ID %d appears in both pages", r.ID)
			}
		}
	})

	// ─── AdoptionCounts ───────────────────────────────────────────────────────

	t.Run("AdoptionCounts/unknown-release", func(t *testing.T) {
		s := newStore()
		defer s.Close()
		_, err := s.AdoptionCounts(ctx, 99999)
		if !errors.Is(err, ErrReleaseNotFound) {
			t.Fatalf("AdoptionCounts(unknown) = %v; want ErrReleaseNotFound", err)
		}
	})

	t.Run("AdoptionCounts/no-reports", func(t *testing.T) {
		s := newStore()
		defer s.Close()
		id, _ := s.InsertRelease(ctx, baseRelease())
		a, err := s.AdoptionCounts(ctx, id)
		if err != nil {
			t.Fatalf("AdoptionCounts: %v", err)
		}
		if a.TotalDevices != 0 || a.Applied != 0 || a.Failed != 0 || a.RolledBack != 0 {
			t.Errorf("expected all-zero adoption, got %+v", a)
		}
	})

	t.Run("AdoptionCounts/counts-by-result", func(t *testing.T) {
		s := newStore()
		defer s.Close()
		id, _ := s.InsertRelease(ctx, baseRelease())

		// 2 applied, 1 failed, 1 rolled-back — all for the release version.
		_ = s.InsertReport(ctx, testULID1, "1.2.0", "applied")
		_ = s.InsertReport(ctx, testULID2, "1.2.0", "applied")
		_ = s.InsertReport(ctx, "01C3XK7BMTQRPD2FGVTCH8QN5A", "1.2.0", "failed")
		_ = s.InsertReport(ctx, "01D4YL8CNUTSQE3GHWUDI9RO6B", "1.2.0", "rolled-back")
		// Report for a different version — should NOT count.
		_ = s.InsertReport(ctx, "01E5ZM9DOVUTRE4HIXVEJ0SP7C", "1.0.0", "applied")

		a, err := s.AdoptionCounts(ctx, id)
		if err != nil {
			t.Fatalf("AdoptionCounts: %v", err)
		}
		if a.TotalDevices != 4 {
			t.Errorf("total_devices = %d; want 4", a.TotalDevices)
		}
		if a.Applied != 2 {
			t.Errorf("applied = %d; want 2", a.Applied)
		}
		if a.Failed != 1 {
			t.Errorf("failed = %d; want 1", a.Failed)
		}
		if a.RolledBack != 1 {
			t.Errorf("rolled_back = %d; want 1", a.RolledBack)
		}
	})

	t.Run("AdoptionCounts/in-cohort-at-100pct", func(t *testing.T) {
		s := newStore()
		defer s.Close()
		r := baseRelease()
		r.RolloutPct = 100 // all ULIDs in cohort
		id, _ := s.InsertRelease(ctx, r)
		_ = s.InsertReport(ctx, testULID1, "1.2.0", "applied")
		_ = s.InsertReport(ctx, testULID2, "1.2.0", "applied")

		a, err := s.AdoptionCounts(ctx, id)
		if err != nil {
			t.Fatalf("AdoptionCounts: %v", err)
		}
		if a.TotalDevices != 2 {
			t.Errorf("total_devices = %d; want 2", a.TotalDevices)
		}
		if a.InCohort != 2 {
			t.Errorf("in_cohort = %d; want 2 (rollout_pct=100)", a.InCohort)
		}
	})

	// ─── Close ────────────────────────────────────────────────────────────────

	t.Run("Close/no-error", func(t *testing.T) {
		s := newStore()
		if err := s.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	})
}
