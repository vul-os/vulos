package tigris

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
	db, err := cpdb.Open("costing_tigris_pgtest")
	if err != nil {
		t.Fatalf("cpdb.Open (postgres): %v", err)
	}
	st, err := Open(db)
	if err != nil {
		db.Close()
		t.Fatalf("tigris.Open (postgres): %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`DROP SCHEMA IF EXISTS costing_tigris_pgtest CASCADE`)
		_ = st.Close()
	})
	return st
}

func TestPG_TigrisInsertAndSnapshots(t *testing.T) {
	st := openPGTestStore(t)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Second)
	snaps := []Snapshot{
		{TakenAt: now, Bucket: "tenant-a", StorageGBHrs: 120.5, EgressGB: 3.2, RequestCount: 15000, AmountUSD: 0.85},
		{TakenAt: now, Bucket: "tenant-b", StorageGBHrs: 50.0, EgressGB: 1.1, RequestCount: 4200, AmountUSD: 0.30},
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

func TestPG_TigrisSweep(t *testing.T) {
	st := openPGTestStore(t)
	ctx := context.Background()

	now := time.Now().UTC()
	old := now.AddDate(0, 0, -91)
	recent := now.AddDate(0, 0, -5)

	if err := st.Insert(ctx, []Snapshot{
		{TakenAt: old, Bucket: "old-bucket", StorageGBHrs: 100.0, EgressGB: 1.0, RequestCount: 500, AmountUSD: 0.50},
		{TakenAt: recent, Bucket: "new-bucket", StorageGBHrs: 200.0, EgressGB: 2.0, RequestCount: 1000, AmountUSD: 1.00},
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

func TestPG_TigrisLatestEgressGB(t *testing.T) {
	st := openPGTestStore(t)
	ctx := context.Background()

	now := time.Now().UTC()
	if err := st.Insert(ctx, []Snapshot{
		{TakenAt: now, Bucket: "bkt-a", StorageGBHrs: 10.0, EgressGB: 2.5, RequestCount: 100, AmountUSD: 0.10},
		{TakenAt: now, Bucket: "bkt-b", StorageGBHrs: 5.0, EgressGB: 1.5, RequestCount: 50, AmountUSD: 0.05},
	}); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	total, err := st.LatestEgressGB(ctx)
	if err != nil {
		t.Fatalf("LatestEgressGB: %v", err)
	}
	if total != 4.0 {
		t.Errorf("expected 4.0 GB egress, got %v", total)
	}
}
