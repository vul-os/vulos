// telemetry_pg_test.go — Postgres integration tests for the telemetry store.
//
// Skipped unless VULOS_TEST_POSTGRES is set to a valid Postgres DSN, e.g.:
//
//	VULOS_TEST_POSTGRES=postgres://postgres:postgres@localhost:5432/cp_test?sslmode=disable \
//	  go test ./internal/telemetry/... -run TestPG_
package telemetry_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/vul-os/vulos-management/pkg/cpdb"
	"github.com/vul-os/vulos-management/pkg/telemetry"
)

func pgDSNTelem(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("VULOS_TEST_POSTGRES")
	if dsn == "" {
		t.Skip("set VULOS_TEST_POSTGRES to run Postgres integration tests")
	}
	return dsn
}

func openPGTelemStore(t *testing.T) *telemetry.SQLStore {
	t.Helper()
	t.Setenv("DATABASE_URL", pgDSNTelem(t))
	t.Setenv("VULOS_DATABASE_URL", "")

	db, err := cpdb.Open("telemetry_pgtest")
	if err != nil {
		t.Fatalf("cpdb.Open (postgres): %v", err)
	}
	st, err := telemetry.Open(db)
	if err != nil {
		db.Close()
		t.Fatalf("telemetry.Open (postgres): %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`DROP SCHEMA IF EXISTS telemetry_pgtest CASCADE`)
		_ = st.Close()
	})
	return st
}

func TestPG_InsertSample_CleanNoAlerts(t *testing.T) {
	st := openPGTelemStore(t)
	ctx := context.Background()

	s := cleanSample("PG001", "PGORG1")
	got, alerts, err := st.InsertSample(ctx, s)
	if err != nil {
		t.Fatalf("InsertSample: %v", err)
	}
	if got.ID == 0 {
		t.Error("expected non-zero ID")
	}
	if len(alerts) != 0 {
		t.Fatalf("expected 0 alerts, got %d", len(alerts))
	}
}

func TestPG_InsertSample_BlocklistFiring(t *testing.T) {
	st := openPGTelemStore(t)
	ctx := context.Background()

	s := cleanSample("PG002", "PGORG1")
	s.BlocklistSpamhaus = true
	_, alerts, err := st.InsertSample(ctx, s)
	if err != nil {
		t.Fatalf("InsertSample: %v", err)
	}
	if len(alerts) == 0 {
		t.Fatal("expected alert for blocklist hit")
	}
	found := false
	for _, a := range alerts {
		if a.Signal == "blocklist_spamhaus" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected blocklist_spamhaus alert, got: %v", alerts)
	}
}

func TestPG_LatestSample(t *testing.T) {
	st := openPGTelemStore(t)
	ctx := context.Background()

	s := cleanSample("PG003", "PGORG1")
	s.DiskUsedPct = 75
	if _, _, err := st.InsertSample(ctx, s); err != nil {
		t.Fatalf("InsertSample: %v", err)
	}

	got, err := st.LatestSample(ctx, "PG003")
	if err != nil {
		t.Fatalf("LatestSample: %v", err)
	}
	if got.DiskUsedPct != 75 {
		t.Errorf("want 75, got %d", got.DiskUsedPct)
	}
}

func TestPG_ResolveAlert(t *testing.T) {
	st := openPGTelemStore(t)
	ctx := context.Background()

	s := cleanSample("PG004", "PGORG2")
	s.BlocklistSORBS = true
	_, fired, err := st.InsertSample(ctx, s)
	if err != nil || len(fired) == 0 {
		t.Fatalf("insert/fire: err=%v fires=%d", err, len(fired))
	}
	aid := fired[0].ID
	if err := st.ResolveAlert(ctx, aid, time.Now().UTC()); err != nil {
		t.Fatalf("ResolveAlert: %v", err)
	}

	open, _ := st.OpenAlerts(ctx, "PG004")
	for _, a := range open {
		if a.ID == aid {
			t.Fatal("resolved alert still appears in OpenAlerts")
		}
	}
}

func TestPG_AllOpenAlerts(t *testing.T) {
	st := openPGTelemStore(t)
	ctx := context.Background()

	s := cleanSample("PG005", "PGORG3")
	s.SyncQueueDepth = 5000
	if _, _, err := st.InsertSample(ctx, s); err != nil {
		t.Fatalf("InsertSample: %v", err)
	}
	all, err := st.AllOpenAlerts(ctx)
	if err != nil {
		t.Fatalf("AllOpenAlerts: %v", err)
	}
	if len(all) == 0 {
		t.Fatal("expected at least one open alert")
	}
}

func TestPG_LatestSample_NotFound(t *testing.T) {
	st := openPGTelemStore(t)
	ctx := context.Background()

	_, err := st.LatestSample(ctx, "NONEXISTENT")
	if err != telemetry.ErrSampleNotFound {
		t.Fatalf("want ErrSampleNotFound, got %v", err)
	}
}
