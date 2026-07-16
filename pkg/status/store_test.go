package status

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/vul-os/vulos-management/pkg/cpdb"
)

func openSQL(t *testing.T) *SQLStore {
	t.Helper()
	db, err := cpdb.OpenSQLiteDSN(":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	s, err := OpenSQLStore(db)
	if err != nil {
		t.Fatalf("open status store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// Run the whole contract against BOTH backends, so the SQLite (self-host) and the
// MemStore (fallback) can never silently diverge.
func eachStore(t *testing.T, fn func(t *testing.T, s Store)) {
	t.Run("sql", func(t *testing.T) { fn(t, openSQL(t)) })
	t.Run("mem", func(t *testing.T) { fn(t, NewMemStore()) })
}

// The honesty guarantee: with no samples in the window, uptime is NOT KNOWN — the
// caller must not render a fabricated 100%.
func TestUptime_UnknownWhenNoData(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		got, err := s.UptimeSince(ctx, []string{"cp"}, 7*24*time.Hour, time.Now())
		if err != nil {
			t.Fatalf("uptime: %v", err)
		}
		if got["cp"].Known {
			t.Fatalf("uptime must be unknown with no data, got %+v", got["cp"])
		}
	})
}

// Samples fold into a real up-fraction, and it persists across the raw-sample
// window (computed from the daily rollup, not the raw ring).
func TestRecordAndUptime(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		base := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
		// 8 up, 2 down over the last hour.
		var batch []Sample
		for i := 0; i < 10; i++ {
			st := "operational"
			if i < 2 {
				st = "down"
			}
			batch = append(batch, Sample{Component: "cp", CheckedAt: base.Add(time.Duration(i) * time.Minute), Status: st})
		}
		// an unknown sample must NOT move the fraction
		batch = append(batch, Sample{Component: "cp", CheckedAt: base.Add(11 * time.Minute), Status: "unknown"})
		if err := s.RecordSamples(ctx, batch); err != nil {
			t.Fatalf("record: %v", err)
		}
		got, err := s.UptimeSince(ctx, []string{"cp"}, 24*time.Hour, base.Add(20*time.Minute))
		if err != nil {
			t.Fatalf("uptime: %v", err)
		}
		u := got["cp"]
		if !u.Known || u.Samples != 10 {
			t.Fatalf("expected 10 counted samples, got %+v", u)
		}
		if u.Pct < 79.9 || u.Pct > 80.1 {
			t.Fatalf("expected 80%% uptime, got %.2f", u.Pct)
		}
	})
}

// A 90-day query spanning many days sums the rollup rather than the raw ring.
func TestUptime_AcrossDays(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
		// One down day 40 days ago, all-up otherwise across 60 days.
		var batch []Sample
		for d := 0; d < 60; d++ {
			day := now.AddDate(0, 0, -d)
			st := "operational"
			if d == 40 {
				st = "down"
			}
			batch = append(batch, Sample{Component: "cp", CheckedAt: day, Status: st})
		}
		if err := s.RecordSamples(ctx, batch); err != nil {
			t.Fatalf("record: %v", err)
		}
		// 90-day window sees all 60 samples, 1 down => 59/60.
		got, _ := s.UptimeSince(ctx, []string{"cp"}, 90*24*time.Hour, now)
		if got["cp"].Samples != 60 {
			t.Fatalf("90d expected 60 samples, got %d", got["cp"].Samples)
		}
		want := 59.0 / 60.0 * 100
		if diff := got["cp"].Pct - want; diff < -0.1 || diff > 0.1 {
			t.Fatalf("90d uptime got %.3f want %.3f", got["cp"].Pct, want)
		}
		// 30-day window excludes the 40-days-ago down sample => 100% of 30 (days 0..29 inclusive => 30).
		got30, _ := s.UptimeSince(ctx, []string{"cp"}, 30*24*time.Hour, now)
		if !got30["cp"].Known || got30["cp"].Pct != 100 {
			t.Fatalf("30d should be 100%% (down day excluded), got %+v", got30["cp"])
		}
	})
}

// RecordSamples is idempotent per (component, checked_at): re-recording a poll must
// not double-count the rollup.
func TestRecordSamples_IdempotentPerInstant(t *testing.T) {
	// SQL only — the PK/ON CONFLICT is a SQL guarantee (MemStore appends).
	s := openSQL(t)
	ctx := context.Background()
	at := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	sample := []Sample{{Component: "cp", CheckedAt: at, Status: "operational"}}
	for i := 0; i < 3; i++ {
		if err := s.RecordSamples(ctx, sample); err != nil {
			t.Fatalf("record %d: %v", i, err)
		}
	}
	got, _ := s.UptimeSince(ctx, []string{"cp"}, time.Hour, at.Add(time.Minute))
	if got["cp"].Samples != 1 {
		t.Fatalf("re-recording the same instant must not double-count: got %d", got["cp"].Samples)
	}
}

func TestPurgeSamples_KeepsRollup(t *testing.T) {
	s := openSQL(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	old := Sample{Component: "cp", CheckedAt: now.AddDate(0, 0, -40), Status: "down"}
	recent := Sample{Component: "cp", CheckedAt: now, Status: "operational"}
	if err := s.RecordSamples(ctx, []Sample{old, recent}); err != nil {
		t.Fatalf("record: %v", err)
	}
	n, err := s.PurgeSamplesBefore(ctx, now.AddDate(0, 0, -7))
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 raw sample purged, got %d", n)
	}
	// The rollup still remembers the down day → 90-day uptime unchanged.
	got, _ := s.UptimeSince(ctx, []string{"cp"}, 90*24*time.Hour, now)
	if got["cp"].Samples != 2 {
		t.Fatalf("rollup must survive raw purge: got %d samples", got["cp"].Samples)
	}
	// But the raw recent window lost the purged sample.
	raw, _ := s.RecentSamples(ctx, "cp", 100)
	if len(raw) != 1 {
		t.Fatalf("expected 1 surviving raw sample, got %d", len(raw))
	}
}

func TestIncidentLifecycle(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		inc, err := s.CreateIncident(ctx, Incident{
			ID: "inc-1", Title: "Mail delayed", Severity: "major", Components: []string{"mail"},
		})
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		if err := s.AddIncidentUpdate(ctx, IncidentUpdate{ID: "u-1", IncidentID: inc.ID, Status: "investigating", Body: "looking"}); err != nil {
			t.Fatalf("update: %v", err)
		}
		if err := s.ResolveIncident(ctx, inc.ID, time.Now()); err != nil {
			t.Fatalf("resolve: %v", err)
		}
		list, err := s.ListIncidents(ctx, 10)
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if len(list) != 1 {
			t.Fatalf("expected 1 incident, got %d", len(list))
		}
		got := list[0]
		if got.ResolvedAt == nil {
			t.Fatal("incident should be resolved")
		}
		if len(got.Components) != 1 || got.Components[0] != "mail" {
			t.Fatalf("components round-trip failed: %+v", got.Components)
		}
		if len(got.Updates) != 1 || got.Updates[0].Status != "investigating" {
			t.Fatalf("update timeline round-trip failed: %+v", got.Updates)
		}
	})
}

func TestMaintenanceWindow(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()
		now := time.Now().UTC()
		if _, err := s.ScheduleMaintenance(ctx, Maintenance{
			ID: "m-1", Title: "DB upgrade", Components: []string{"cp"},
			StartsAt: now.Add(-time.Hour), EndsAt: now.Add(time.Hour),
		}); err != nil {
			t.Fatalf("schedule: %v", err)
		}
		if _, err := s.ScheduleMaintenance(ctx, Maintenance{
			ID: "m-2", Title: "future", StartsAt: now.Add(time.Hour), EndsAt: now.Add(2 * time.Hour),
		}); err != nil {
			t.Fatalf("schedule future: %v", err)
		}
		active, err := s.ActiveMaintenance(ctx, now)
		if err != nil {
			t.Fatalf("active: %v", err)
		}
		if len(active) != 1 || active[0].ID != "m-1" {
			t.Fatalf("expected only the in-window maintenance, got %+v", active)
		}
	})
}

// Deterministic label so a failure names the backend.
func TestStoreInterfaceParity(t *testing.T) {
	for _, tc := range []struct {
		name string
		s    Store
	}{
		{"sql", openSQL(t)},
		{"mem", NewMemStore()},
	} {
		if tc.s == nil {
			t.Fatal(fmt.Sprintf("%s store is nil", tc.name))
		}
	}
}
