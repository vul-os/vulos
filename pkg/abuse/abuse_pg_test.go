package abuse

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
	db, err := cpdb.Open("abuse_pgtest")
	if err != nil {
		t.Fatalf("cpdb.Open: %v", err)
	}
	st, err := OpenStore(db)
	if err != nil {
		db.Close()
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`DROP SCHEMA IF EXISTS abuse_pgtest CASCADE`)
		_ = st.Close()
	})
	return st
}

func TestPG_SuspendAndReinstate(t *testing.T) {
	st := openPGTestStore(t)
	ctx := context.Background()

	changed, err := st.Suspend(ctx, "acct-pg-1", "spam", 0.9, true)
	if err != nil {
		t.Fatalf("Suspend: %v", err)
	}
	if !changed {
		t.Errorf("expected changed=true on first suspend")
	}

	susp, err := st.IsSuspended(ctx, "acct-pg-1")
	if err != nil || !susp {
		t.Fatalf("IsSuspended: got susp=%v err=%v", susp, err)
	}

	if err := st.Reinstate(ctx, "acct-pg-1", "ts-lead"); err != nil {
		t.Fatalf("Reinstate: %v", err)
	}
	susp, _ = st.IsSuspended(ctx, "acct-pg-1")
	if susp {
		t.Errorf("expected not suspended after reinstate")
	}
}

func TestPG_FileAndListReports(t *testing.T) {
	st := openPGTestStore(t)
	ctx := context.Background()

	r, err := st.FileReport(ctx, "alice@example.com", "acct-evil", CategoryPhishing, "fake login", map[string]string{"url": "https://bad.example"})
	if err != nil {
		t.Fatalf("FileReport: %v", err)
	}
	if r.ID == "" || r.Status != StatusOpen {
		t.Errorf("bad report: %+v", r)
	}

	list, err := st.ListReports(ctx, StatusOpen, 10)
	if err != nil {
		t.Fatalf("ListReports: %v", err)
	}
	if len(list) != 1 {
		t.Errorf("expected 1 report, got %d", len(list))
	}

	if err := st.SetReportStatus(ctx, r.ID, StatusActioned, "ts-lead"); err != nil {
		t.Fatalf("SetReportStatus: %v", err)
	}
}

func TestPG_LogSignalAndCount(t *testing.T) {
	st := openPGTestStore(t)
	ctx := context.Background()
	before := time.Now().UTC()

	if err := st.LogSignal(ctx, Signal{Kind: KindSignup, AccountID: "acct-x", IP: "1.2.3.4", Score: 0.5}); err != nil {
		t.Fatalf("LogSignal: %v", err)
	}

	n, err := st.CountSignalsSince(ctx, before)
	if err != nil {
		t.Fatalf("CountSignalsSince: %v", err)
	}
	if n != 1 {
		t.Errorf("expected 1 signal, got %d", n)
	}
}
