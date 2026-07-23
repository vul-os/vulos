// trydemo_pg_test.go — Postgres integration tests for the trydemo store.
//
// Skipped unless VULOS_TEST_POSTGRES is set to a valid Postgres DSN, e.g.:
//
//	VULOS_TEST_POSTGRES=postgres://postgres:postgres@localhost:5432/cp_test?sslmode=disable \
//	  go test ./internal/trydemo/... -run TestPG_
package trydemo_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/vul-os/vulos-management/pkg/cpdb"
	"github.com/vul-os/vulos-management/pkg/trydemo"
)

func pgDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("VULOS_TEST_POSTGRES")
	if dsn == "" {
		t.Skip("set VULOS_TEST_POSTGRES to run Postgres integration tests")
	}
	return dsn
}

func openPGTestStore(t *testing.T) trydemo.Store {
	t.Helper()
	t.Setenv("DATABASE_URL", pgDSN(t))
	t.Setenv("VULOS_DATABASE_URL", "")

	db, err := cpdb.Open("trydemo_pgtest")
	if err != nil {
		t.Fatalf("cpdb.Open (postgres): %v", err)
	}
	st, err := trydemo.Open(db)
	if err != nil {
		db.Close()
		t.Fatalf("trydemo.Open (postgres): %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`DROP SCHEMA IF EXISTS trydemo_pgtest CASCADE`)
		_ = st.Close()
	})
	return st
}

func TestPG_DriverClaim_RoundTrip(t *testing.T) {
	st := openPGTestStore(t)
	ctx := context.Background()
	ip := "192.0.2.100"

	got, err := st.LastDriverClaim(ctx, ip)
	if err != nil {
		t.Fatalf("LastDriverClaim (empty): %v", err)
	}
	if !got.IsZero() {
		t.Errorf("expected zero time, got %v", got)
	}

	now := time.Now().UTC().Truncate(time.Second)
	if err := st.RecordDriverClaim(ctx, ip, now); err != nil {
		t.Fatalf("RecordDriverClaim: %v", err)
	}

	got, err = st.LastDriverClaim(ctx, ip)
	if err != nil {
		t.Fatalf("LastDriverClaim (after record): %v", err)
	}
	if !got.Equal(now) {
		t.Errorf("got %v, want %v", got, now)
	}

	later := now.Add(5 * time.Minute)
	if err := st.RecordDriverClaim(ctx, ip, later); err != nil {
		t.Fatalf("RecordDriverClaim (update): %v", err)
	}
	got, err = st.LastDriverClaim(ctx, ip)
	if err != nil {
		t.Fatalf("LastDriverClaim (after update): %v", err)
	}
	if !got.Equal(later) {
		t.Errorf("got %v, want %v", got, later)
	}
}

func TestPG_AccumulateUsage(t *testing.T) {
	st := openPGTestStore(t)
	ctx := context.Background()
	month := "202606"

	cost, err := st.MonthlyCostUSD(ctx, month)
	if err != nil {
		t.Fatalf("MonthlyCostUSD (empty): %v", err)
	}
	if cost != 0 {
		t.Errorf("expected 0, got %v", cost)
	}

	if err := st.AccumulateUsage(ctx, month, 10, 1024, 0.5); err != nil {
		t.Fatalf("AccumulateUsage 1: %v", err)
	}
	if err := st.AccumulateUsage(ctx, month, 5, 512, 0.25); err != nil {
		t.Fatalf("AccumulateUsage 2: %v", err)
	}

	cost, err = st.MonthlyCostUSD(ctx, month)
	if err != nil {
		t.Fatalf("MonthlyCostUSD (after accum): %v", err)
	}
	const want = 0.75
	if absF(cost-want) > 1e-9 {
		t.Errorf("cost = %v, want %v", cost, want)
	}
}

func TestPG_LogEvent(t *testing.T) {
	st := openPGTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	events := []struct{ typ, ip, sid, reason string }{
		{"driver_claim", "1.2.3.4", "tok-abc", "join_direct"},
		{"machine_start", "", "", ""},
	}
	for _, e := range events {
		if err := st.LogEvent(ctx, e.typ, e.ip, e.sid, e.reason, now); err != nil {
			t.Errorf("LogEvent(%q): %v", e.typ, err)
		}
	}
}

func absF(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}
