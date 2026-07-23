package onboarding_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/vul-os/vulos-management/pkg/cpdb"
	"github.com/vul-os/vulos-management/pkg/onboarding"
)

func openPGOnboardingStore(t *testing.T) *onboarding.SQLStore {
	t.Helper()
	dsn := os.Getenv("VULOS_TEST_POSTGRES")
	if dsn == "" {
		t.Skip("set VULOS_TEST_POSTGRES to run Postgres integration tests")
	}
	t.Setenv("DATABASE_URL", dsn)
	t.Setenv("VULOS_DATABASE_URL", "")
	db, err := cpdb.Open("onboarding_pgtest")
	if err != nil {
		t.Fatalf("cpdb.Open (postgres): %v", err)
	}
	st, err := onboarding.OpenSQLStore(db)
	if err != nil {
		db.Close()
		t.Fatalf("onboarding.OpenSQLStore (postgres): %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`DROP SCHEMA IF EXISTS onboarding_pgtest CASCADE`)
		_ = st.Close()
	})
	return st
}

func TestPG_OnboardingMarkAndGet(t *testing.T) {
	st := openPGOnboardingStore(t)
	ctx := context.Background()
	created := time.Now().UTC().Add(-8 * 24 * time.Hour)
	state, err := st.MarkStep(ctx, "pg_acc1", created, "domain_connected")
	if err != nil {
		t.Fatalf("MarkStep: %v", err)
	}
	if !state.DomainConnected {
		t.Error("expected DomainConnected=true")
	}
	got, err := st.GetState(ctx, "pg_acc1")
	if err != nil {
		t.Fatalf("GetState: %v", err)
	}
	if got.AccountID != "pg_acc1" {
		t.Errorf("AccountID: want pg_acc1, got %q", got.AccountID)
	}
}

func TestPG_OnboardingMigrationGate(t *testing.T) {
	st := openPGOnboardingStore(t)
	ctx := context.Background()
	oldAccount := time.Now().UTC().Add(-10 * 24 * time.Hour)
	_, _ = st.MarkStep(ctx, "pg_old", oldAccount, "")
	if err := st.CheckMigrationGate(ctx, "pg_old", time.Now().UTC()); err != nil {
		t.Errorf("old account should pass gate: %v", err)
	}
	newAccount := time.Now().UTC()
	_, _ = st.MarkStep(ctx, "pg_new", newAccount, "")
	if err := st.CheckMigrationGate(ctx, "pg_new", time.Now().UTC()); err == nil {
		t.Error("new account should be gated")
	}
}
