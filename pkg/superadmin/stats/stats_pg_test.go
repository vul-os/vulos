// stats_pg_test.go — Postgres integration tests for the stats store.
//
// Skipped unless VULOS_TEST_POSTGRES is set to a valid Postgres DSN, e.g.:
//
//	VULOS_TEST_POSTGRES=postgres://postgres:postgres@localhost:5432/cp_test?sslmode=disable \
//	  go test ./internal/superadmin/stats/... -run TestPG_
package stats_test

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/vul-os/vulos-management/pkg/cpdb"
	"github.com/vul-os/vulos-management/pkg/superadmin/stats"
)

// ─── mock readers (shared with pg tests) ─────────────────────────────────────

type pgMockAccounts struct {
	total   int64
	signups int64
}

func (m *pgMockAccounts) CountTotal(_ context.Context) (int64, error) { return m.total, nil }
func (m *pgMockAccounts) CountActiveAfter(_ context.Context, _ time.Time) (int64, error) {
	return 0, nil
}
func (m *pgMockAccounts) CountSignupsSince(_ context.Context, _ time.Time) (int64, error) {
	return m.signups, nil
}
func (m *pgMockAccounts) SignupsByTierSince(_ context.Context, _ time.Time) (stats.SignupsByTier, error) {
	return stats.SignupsByTier{Free: m.signups}, nil
}

type pgMockBilling struct{ mrr int64 }

func (m *pgMockBilling) MRRZARCents(_ context.Context) (int64, error) { return m.mrr, nil }

// ─── PG store helper ──────────────────────────────────────────────────────────

func openPGStatsStore(t *testing.T) *stats.Store {
	t.Helper()
	dsn := os.Getenv("VULOS_TEST_POSTGRES")
	if dsn == "" {
		t.Skip("set VULOS_TEST_POSTGRES to run Postgres integration tests")
	}
	t.Setenv("DATABASE_URL", dsn)
	t.Setenv("VULOS_DATABASE_URL", "")

	db, err := cpdb.Open("superadmin_stats_pgtest")
	if err != nil {
		t.Fatalf("cpdb.Open (postgres): %v", err)
	}
	st, err := stats.Open(db)
	if err != nil {
		db.Close()
		t.Fatalf("stats.Open (postgres): %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`DROP SCHEMA IF EXISTS superadmin_stats_pgtest CASCADE`)
		_ = st.Close()
	})
	return st
}

// ─── Tests ────────────────────────────────────────────────────────────────────

func TestPG_Stats_RunSnapshotAndSeries(t *testing.T) {
	st := openPGStatsStore(t)
	ctx := context.Background()
	today := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

	accounts := &pgMockAccounts{total: 5000, signups: 42}
	billing := &pgMockBilling{mrr: 1_234_00}

	if err := st.RunSnapshot(ctx, today, accounts, billing); err != nil {
		t.Fatalf("RunSnapshot: %v", err)
	}

	snaps, err := st.Series(ctx, today.AddDate(0, 0, -1), today.AddDate(0, 0, 1))
	if err != nil {
		t.Fatalf("Series: %v", err)
	}
	if len(snaps) != 1 {
		t.Fatalf("expected 1 snapshot, got %d", len(snaps))
	}
	sn := snaps[0]
	if sn.TotalAccounts != 5000 {
		t.Errorf("TotalAccounts: want 5000, got %d", sn.TotalAccounts)
	}
	if sn.MRRZARCents != 123400 {
		t.Errorf("MRRZARCents: want 123400, got %d", sn.MRRZARCents)
	}
	if sn.SignupsToday != 42 {
		t.Errorf("SignupsToday: want 42, got %d", sn.SignupsToday)
	}
}

func TestPG_Stats_IdempotentRerun(t *testing.T) {
	st := openPGStatsStore(t)
	ctx := context.Background()
	today := time.Date(2026, 6, 2, 12, 0, 0, 0, time.UTC)

	if err := st.RunSnapshot(ctx, today, &pgMockAccounts{total: 100}, &pgMockBilling{mrr: 1000}); err != nil {
		t.Fatalf("first RunSnapshot: %v", err)
	}
	if err := st.RunSnapshot(ctx, today, &pgMockAccounts{total: 200}, &pgMockBilling{mrr: 2000}); err != nil {
		t.Fatalf("second RunSnapshot: %v", err)
	}

	snaps, err := st.Series(ctx, today, today)
	if err != nil || len(snaps) != 1 {
		t.Fatalf("Series: %v / %d", err, len(snaps))
	}
	if snaps[0].TotalAccounts != 200 {
		t.Errorf("idempotent: want 200, got %d", snaps[0].TotalAccounts)
	}
}

func TestPG_Stats_BackfillMissingDays(t *testing.T) {
	st := openPGStatsStore(t)
	ctx := context.Background()

	from := time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 6, 14, 0, 0, 0, 0, time.UTC)

	// Insert only day 11.
	if err := st.RunSnapshot(ctx, from.AddDate(0, 0, 1),
		&pgMockAccounts{total: 50}, &pgMockBilling{mrr: 500}); err != nil {
		t.Fatalf("RunSnapshot: %v", err)
	}

	if err := st.BackfillMissingDays(ctx, from, to); err != nil {
		t.Fatalf("BackfillMissingDays: %v", err)
	}

	snaps, err := st.Series(ctx, from, to)
	if err != nil {
		t.Fatalf("Series: %v", err)
	}
	if len(snaps) != 5 {
		t.Errorf("expected 5 rows after backfill, got %d", len(snaps))
	}
}

func TestPG_Stats_TierJSON(t *testing.T) {
	st := openPGStatsStore(t)
	ctx := context.Background()
	today := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)

	accounts := &pgMockAccounts{total: 1000, signups: 20}
	billing := &pgMockBilling{mrr: 0}
	if err := st.RunSnapshot(ctx, today, accounts, billing); err != nil {
		t.Fatalf("RunSnapshot: %v", err)
	}

	snaps, _ := st.Series(ctx, today, today)
	if len(snaps) == 0 {
		t.Fatal("no snapshots")
	}
	var tier stats.SignupsByTier
	if err := json.Unmarshal([]byte(snaps[0].SignupsByTierJSON), &tier); err != nil {
		t.Fatalf("unmarshal tier: %v", err)
	}
	if tier.Free != 20 {
		t.Errorf("tier.Free: want 20, got %d", tier.Free)
	}
}

func TestPG_Stats_Rebind(t *testing.T) {
	// Exercises all query paths with $N placeholders on a real Postgres connection.
	st := openPGStatsStore(t)
	ctx := context.Background()
	today := time.Date(2026, 6, 4, 12, 0, 0, 0, time.UTC)

	if err := st.RunSnapshot(ctx, today, &pgMockAccounts{total: 1}, &pgMockBilling{mrr: 1}); err != nil {
		t.Fatalf("RunSnapshot: %v", err)
	}
	snaps, err := st.Series(ctx, today, today)
	if err != nil || len(snaps) == 0 {
		t.Fatalf("Series: %v / %d", err, len(snaps))
	}
	if err := st.BackfillMissingDays(ctx, today, today); err != nil {
		t.Fatalf("BackfillMissingDays: %v", err)
	}
}
