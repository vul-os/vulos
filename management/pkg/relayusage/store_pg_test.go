// store_pg_test.go — Postgres integration tests for relayusage.
// Skipped when DATABASE_URL / VULOS_DATABASE_URL is not set.
package relayusage

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/vul-os/vulos-management/pkg/cpdb"
)

func openSQLStorePG(t *testing.T) *SQLStore {
	t.Helper()
	if os.Getenv("DATABASE_URL") == "" && os.Getenv("VULOS_DATABASE_URL") == "" {
		t.Skip("DATABASE_URL not set; skipping Postgres test")
	}
	db, err := cpdb.Open("relayusage_pgtest")
	if err != nil {
		t.Fatalf("cpdb.Open: %v", err)
	}
	st, err := OpenSQLStore(db)
	if err != nil {
		_ = db.Close()
		t.Fatalf("OpenSQLStore: %v", err)
	}
	// These tests share a fixed schema and assert ABSOLUTE row counts, so a
	// repeated run (go test -count=2) would accumulate into the same tables and
	// double the totals. Start every open from clean tables so the suite is
	// deterministic under -count=N.
	if _, err := db.ExecContext(context.Background(), `TRUNCATE relay_usage, relay_usage_reports`); err != nil {
		_ = db.Close()
		t.Fatalf("truncate: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestPG_Add_Accumulates(t *testing.T) {
	st := openSQLStorePG(t)
	ctx := context.Background()
	period := CurrentPeriod(time.Now())
	accountID := "pg-acct-add"

	if err := st.Add(ctx, accountID, period, 1000, 2); err != nil {
		t.Fatalf("Add #1: %v", err)
	}
	if err := st.Add(ctx, accountID, period, 500, 3); err != nil {
		t.Fatalf("Add #2: %v", err)
	}
	bytes, sessions, err := st.UsageThisMonth(ctx, accountID)
	if err != nil {
		t.Fatalf("UsageThisMonth: %v", err)
	}
	if bytes != 1500 {
		t.Errorf("bytes = %d, want 1500", bytes)
	}
	if sessions != 5 {
		t.Errorf("sessions = %d, want 5", sessions)
	}
}

func TestPG_UsageThisMonth_NoRow(t *testing.T) {
	st := openSQLStorePG(t)
	bytes, sessions, err := st.UsageThisMonth(context.Background(), "pg-never-seen")
	if err != nil {
		t.Fatalf("UsageThisMonth: %v", err)
	}
	if bytes != 0 || sessions != 0 {
		t.Errorf("got (%d, %d), want (0, 0)", bytes, sessions)
	}
}

func TestPG_MarkReport_Idempotent(t *testing.T) {
	st := openSQLStorePG(t)
	ctx := context.Background()

	first, err := st.MarkReport(ctx, "pg-pop-a", "pg-rpt-1")
	if err != nil {
		t.Fatalf("MarkReport first: %v", err)
	}
	if !first {
		t.Fatal("first MarkReport should return firstSeen=true")
	}

	again, err := st.MarkReport(ctx, "pg-pop-a", "pg-rpt-1")
	if err != nil {
		t.Fatalf("MarkReport replay: %v", err)
	}
	if again {
		t.Fatal("replayed MarkReport should return firstSeen=false")
	}
}

func TestPG_Add_NegativeDeltaClamps(t *testing.T) {
	st := openSQLStorePG(t)
	ctx := context.Background()
	period := CurrentPeriod(time.Now())
	accountID := "pg-acct-neg"

	if err := st.Add(ctx, accountID, period, -5, -5); err != nil {
		t.Fatalf("Add negative: %v", err)
	}
	bytes, sessions, _ := st.UsageThisMonth(ctx, accountID)
	if bytes != 0 || sessions != 0 {
		t.Errorf("negative delta leaked: got (%d, %d)", bytes, sessions)
	}
}
