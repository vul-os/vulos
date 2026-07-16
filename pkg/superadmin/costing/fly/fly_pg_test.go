package fly

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/vul-os/vulos-management/pkg/cpdb"
)

func openPGTestStore(t *testing.T) *Store {
	t.Helper()
	dsn := os.Getenv("VULOS_TEST_POSTGRES")
	if dsn == "" {
		t.Skip("set VULOS_TEST_POSTGRES to run Postgres integration tests")
	}
	t.Setenv("DATABASE_URL", dsn)
	db, err := cpdb.Open("costing_fly_pgtest")
	if err != nil {
		t.Fatalf("cpdb.Open (postgres): %v", err)
	}
	st, err := Open(db)
	if err != nil {
		db.Close()
		t.Fatalf("fly.Open (postgres): %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`DROP SCHEMA IF EXISTS costing_fly_pgtest CASCADE`)
		_ = st.Close()
	})
	return st
}

func TestPG_FlyInsertAndSnapshots(t *testing.T) {
	st := openPGTestStore(t)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Second)
	snaps := []Snapshot{
		{TakenAt: now, OrgID: "org1", AppName: "app-a", Region: "iad", AmountUSD: 1.23, Currency: "USD"},
		{TakenAt: now, OrgID: "org1", AppName: "app-b", Region: "lhr", AmountUSD: 0.45, Currency: "USD"},
	}
	if err := st.Insert(ctx, snaps); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	got, err := st.Snapshots(ctx, now.Add(-time.Minute), now.Add(time.Minute))
	if err != nil {
		t.Fatalf("Snapshots: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 snapshots, got %d", len(got))
	}
}

func TestPG_FlySweep(t *testing.T) {
	st := openPGTestStore(t)
	ctx := context.Background()

	now := time.Now().UTC()
	old := now.AddDate(0, 0, -91)
	recent := now.AddDate(0, 0, -5)

	if err := st.Insert(ctx, []Snapshot{
		{TakenAt: old, OrgID: "org1", AppName: "old-app", Region: "iad", AmountUSD: 9.99, Currency: "USD"},
		{TakenAt: recent, OrgID: "org1", AppName: "new-app", Region: "iad", AmountUSD: 1.00, Currency: "USD"},
	}); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	if err := st.Sweep(ctx); err != nil {
		t.Fatalf("Sweep: %v", err)
	}

	got, err := st.Snapshots(ctx, old.Add(-time.Hour), now.Add(time.Hour))
	if err != nil {
		t.Fatalf("Snapshots: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("expected 1 snapshot after sweep, got %d", len(got))
	}
}

func TestPG_FlyLatestMachineCount(t *testing.T) {
	st := openPGTestStore(t)
	ctx := context.Background()

	now := time.Now().UTC()
	if err := st.Insert(ctx, []Snapshot{
		{TakenAt: now, OrgID: "org1", AppName: "_org_total", Region: "", AmountUSD: 10.0, Currency: "USD"},
		{TakenAt: now, OrgID: "org1", AppName: "app-1", Region: "iad", AmountUSD: 5.0, Currency: "USD"},
		{TakenAt: now, OrgID: "org1", AppName: "app-2", Region: "lhr", AmountUSD: 5.0, Currency: "USD"},
	}); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	count, err := st.LatestMachineCount(ctx)
	if err != nil {
		t.Fatalf("LatestMachineCount: %v", err)
	}
	if count != 2 {
		t.Errorf("expected 2, got %d", count)
	}
}
