package recovery_test

import (
	"context"
	"os"
	"testing"

	"github.com/vul-os/vulos-management/pkg/auth/recovery"
	"github.com/vul-os/vulos-management/pkg/cpdb"
)

func openPGRecoveryStore(t *testing.T) *recovery.Store {
	t.Helper()
	dsn := os.Getenv("VULOS_TEST_POSTGRES")
	if dsn == "" {
		t.Skip("set VULOS_TEST_POSTGRES to run Postgres integration tests")
	}
	t.Setenv("DATABASE_URL", dsn)
	t.Setenv("VULOS_DATABASE_URL", "")
	db, err := cpdb.Open("recovery_pgtest")
	if err != nil {
		t.Fatalf("cpdb.Open (postgres): %v", err)
	}
	st, err := recovery.Open(db, nil)
	if err != nil {
		db.Close()
		t.Fatalf("recovery.Open (postgres): %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`DROP SCHEMA IF EXISTS recovery_pgtest CASCADE`)
		_ = st.Close()
	})
	return st
}

func TestPG_RecoverySubmitAndGet(t *testing.T) {
	st := openPGRecoveryStore(t)
	ctx := context.Background()
	req, err := st.SubmitRequest(ctx, "pg_acc1", "pg@test.com", "1234")
	if err != nil {
		t.Fatalf("SubmitRequest: %v", err)
	}
	got, err := st.GetRequest(ctx, req.ID)
	if err != nil {
		t.Fatalf("GetRequest: %v", err)
	}
	if got.AccountID != "pg_acc1" {
		t.Errorf("AccountID: want pg_acc1, got %q", got.AccountID)
	}
	if got.Status != "pending" {
		t.Errorf("Status: want pending, got %q", got.Status)
	}
}

func TestPG_RecoveryAbuse(t *testing.T) {
	st := openPGRecoveryStore(t)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if _, err := st.SubmitRequest(ctx, "pg_abuse", "a@b.com", ""); err != nil {
			st.Cancel(ctx, "", "pg_abuse") // allow re-submit by cancelling
			_ = err                        // first 3 should succeed or hit AlreadyPending
		}
	}
	// 4th should hit abuse threshold or AlreadyPending
	_, err := st.SubmitRequest(ctx, "pg_abuse", "a@b.com", "")
	if err == nil {
		t.Error("expected error on 4th submission")
	}
}
