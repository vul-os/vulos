package relayusage

import (
	"context"
	"testing"
	"time"

	"github.com/vul-os/vulos-management/pkg/cpdb"
)

// stores returns both implementations so each test exercises MemStore + SQLStore
// behind the same Store interface.
func stores(t *testing.T) map[string]Store {
	t.Helper()
	cdb, err := cpdb.OpenSQLiteDSN(":memory:")
	if err != nil {
		t.Fatalf("cpdb.OpenSQLiteDSN: %v", err)
	}
	sqlSt, err := OpenSQLStore(cdb)
	if err != nil {
		t.Fatalf("OpenSQLStore: %v", err)
	}
	t.Cleanup(func() { _ = sqlSt.Close() })
	return map[string]Store{
		"mem": NewMemStore(),
		"sql": sqlSt,
	}
}

func TestCurrentPeriod(t *testing.T) {
	// 2026-05-24 in any zone normalises to the UTC month key.
	tm := time.Date(2026, time.May, 24, 23, 30, 0, 0, time.FixedZone("WAT", 1*60*60))
	if got := CurrentPeriod(tm); got != "2026-05" {
		t.Fatalf("CurrentPeriod = %q, want 2026-05", got)
	}
}

func TestAdd_Accumulates(t *testing.T) {
	period := CurrentPeriod(time.Now())
	for name, st := range stores(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			// Two independent reports for the same tenant in the same period —
			// e.g. two PoPs, or two flush ticks. Must SUM.
			if err := st.Add(ctx, "acct-1", period, 1000, 2); err != nil {
				t.Fatalf("Add #1: %v", err)
			}
			if err := st.Add(ctx, "acct-1", period, 500, 3); err != nil {
				t.Fatalf("Add #2: %v", err)
			}
			bytes, sessions, err := st.UsageThisMonth(ctx, "acct-1")
			if err != nil {
				t.Fatalf("UsageThisMonth: %v", err)
			}
			if bytes != 1500 {
				t.Errorf("bytes = %d, want 1500", bytes)
			}
			if sessions != 5 {
				t.Errorf("sessions = %d, want 5", sessions)
			}
		})
	}
}

func TestUsageThisMonth_NoRow(t *testing.T) {
	for name, st := range stores(t) {
		t.Run(name, func(t *testing.T) {
			bytes, sessions, err := st.UsageThisMonth(context.Background(), "never-seen")
			if err != nil {
				t.Fatalf("UsageThisMonth: %v", err)
			}
			if bytes != 0 || sessions != 0 {
				t.Errorf("got (%d, %d), want (0, 0)", bytes, sessions)
			}
		})
	}
}

func TestUsageThisMonth_IsolatesAccountsAndPeriods(t *testing.T) {
	now := time.Now().UTC()
	thisMonth := CurrentPeriod(now)
	lastMonth := CurrentPeriod(now.AddDate(0, -1, 0))
	if thisMonth == lastMonth {
		// Extremely unlikely (only if AddDate underflowed oddly); guard anyway.
		lastMonth = "2000-01"
	}
	for name, st := range stores(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			// Other account in the same period — must not bleed in.
			if err := st.Add(ctx, "acct-other", thisMonth, 9999, 9); err != nil {
				t.Fatalf("Add other: %v", err)
			}
			// Same account in a PRIOR period — must not count toward this month.
			if err := st.Add(ctx, "acct-x", lastMonth, 7777, 7); err != nil {
				t.Fatalf("Add prior: %v", err)
			}
			// This month for acct-x.
			if err := st.Add(ctx, "acct-x", thisMonth, 100, 1); err != nil {
				t.Fatalf("Add current: %v", err)
			}
			bytes, sessions, err := st.UsageThisMonth(ctx, "acct-x")
			if err != nil {
				t.Fatalf("UsageThisMonth: %v", err)
			}
			if bytes != 100 || sessions != 1 {
				t.Errorf("got (%d, %d), want (100, 1) — leaked across account/period", bytes, sessions)
			}
		})
	}
}

func TestAdd_Validation(t *testing.T) {
	period := CurrentPeriod(time.Now())
	for name, st := range stores(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			if err := st.Add(ctx, "", period, 1, 1); err == nil {
				t.Error("Add with empty account_id should error")
			}
			if err := st.Add(ctx, "acct", "", 1, 1); err == nil {
				t.Error("Add with empty period should error")
			}
			// Negative deltas clamp to 0 (no error, no decrement).
			if err := st.Add(ctx, "acct", period, -5, -5); err != nil {
				t.Fatalf("Add negative: %v", err)
			}
			bytes, sessions, _ := st.UsageThisMonth(ctx, "acct")
			if bytes != 0 || sessions != 0 {
				t.Errorf("negative delta leaked: got (%d, %d)", bytes, sessions)
			}
		})
	}
}

func TestMarkReport_Idempotent(t *testing.T) {
	for name, st := range stores(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			// First sight → firstSeen=true.
			first, err := st.MarkReport(ctx, "pop-a", "rpt-1")
			if err != nil {
				t.Fatalf("MarkReport first: %v", err)
			}
			if !first {
				t.Fatal("first MarkReport should return firstSeen=true")
			}
			// Replay of the same (pop, report) → firstSeen=false.
			again, err := st.MarkReport(ctx, "pop-a", "rpt-1")
			if err != nil {
				t.Fatalf("MarkReport replay: %v", err)
			}
			if again {
				t.Fatal("replayed MarkReport should return firstSeen=false")
			}
			// Same report_id from a DIFFERENT PoP is distinct → firstSeen=true.
			otherPoP, err := st.MarkReport(ctx, "pop-b", "rpt-1")
			if err != nil {
				t.Fatalf("MarkReport other pop: %v", err)
			}
			if !otherPoP {
				t.Fatal("same report_id on a different pop should be firstSeen=true")
			}
			// Empty pop_id / report_id is rejected.
			if _, err := st.MarkReport(ctx, "", "rpt"); err == nil {
				t.Error("MarkReport with empty pop_id should error")
			}
			if _, err := st.MarkReport(ctx, "pop", ""); err == nil {
				t.Error("MarkReport with empty report_id should error")
			}
		})
	}
}
