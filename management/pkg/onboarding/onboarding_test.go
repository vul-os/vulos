// Tests for SUPPORT-05 onboarding package.
// Covers both MemStore (in-memory) and SQLStore (SQLite) implementations.
package onboarding_test

import (
	"context"
	"testing"
	"time"

	"github.com/vul-os/vulos-management/pkg/cpdb"
	"github.com/vul-os/vulos-management/pkg/onboarding"
)

// ─── helpers ─────────────────────────────────────────────────────────────────

func newSQLStore(t *testing.T) *onboarding.SQLStore {
	t.Helper()
	db, err := cpdb.OpenSQLiteDSN(":memory:")
	if err != nil {
		t.Fatalf("cpdb open: %v", err)
	}
	st, err := onboarding.OpenSQLStore(db)
	if err != nil {
		db.Close()
		t.Fatalf("OpenSQLStore: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// storeTests runs the same test suite against any Store implementation.
func storeTests(t *testing.T, st onboarding.Store) {
	t.Helper()
	ctx := context.Background()
	acct := "acc-test-001"
	created := time.Now().UTC().Add(-24 * time.Hour) // 1 day old

	t.Run("GetState_not_found", func(t *testing.T) {
		_, err := st.GetState(ctx, acct)
		if err != onboarding.ErrNotFound {
			t.Fatalf("expected ErrNotFound, got %v", err)
		}
	})

	t.Run("MarkStep_init_no_step", func(t *testing.T) {
		s, err := st.MarkStep(ctx, acct, created, "")
		if err != nil {
			t.Fatalf("MarkStep: %v", err)
		}
		if s.AccountID != acct {
			t.Fatalf("account_id mismatch: %q", s.AccountID)
		}
		if s.CompletedSteps() != 0 {
			t.Fatalf("expected 0 steps, got %d", s.CompletedSteps())
		}
	})

	t.Run("MarkStep_domain_connected", func(t *testing.T) {
		s, err := st.MarkStep(ctx, acct, created, "domain_connected")
		if err != nil {
			t.Fatalf("MarkStep: %v", err)
		}
		if !s.DomainConnected {
			t.Fatal("domain_connected should be true")
		}
		if s.CompletedSteps() != 1 {
			t.Fatalf("expected 1 step, got %d", s.CompletedSteps())
		}
	})

	t.Run("MarkStep_all_steps", func(t *testing.T) {
		steps := []string{
			"dns_verified",
			"first_mail_sent",
			"first_backup_done",
			"first_device_paired",
			"first_user_invited",
		}
		for _, step := range steps {
			if _, err := st.MarkStep(ctx, acct, created, step); err != nil {
				t.Fatalf("MarkStep(%s): %v", step, err)
			}
		}
		s, err := st.GetState(ctx, acct)
		if err != nil {
			t.Fatalf("GetState: %v", err)
		}
		if s.CompletedSteps() != onboarding.TotalSteps {
			t.Fatalf("expected %d steps, got %d", onboarding.TotalSteps, s.CompletedSteps())
		}
	})

	t.Run("MarkStep_unknown_step", func(t *testing.T) {
		_, err := st.MarkStep(ctx, acct, created, "nonexistent_step")
		if err != onboarding.ErrUnknownStep {
			t.Fatalf("expected ErrUnknownStep, got %v", err)
		}
	})

	t.Run("migration_gate_closed_new_account", func(t *testing.T) {
		acct2 := "acc-new"
		newCreated := time.Now().UTC() // just created
		if _, err := st.MarkStep(ctx, acct2, newCreated, ""); err != nil {
			t.Fatalf("MarkStep: %v", err)
		}
		now := time.Now().UTC()
		err := st.CheckMigrationGate(ctx, acct2, now)
		if err != onboarding.ErrMigrationGated {
			t.Fatalf("expected ErrMigrationGated for new account, got %v", err)
		}
	})

	t.Run("migration_gate_open_old_account", func(t *testing.T) {
		acct3 := "acc-old"
		oldCreated := time.Now().UTC().Add(-8 * 24 * time.Hour) // 8 days old
		if _, err := st.MarkStep(ctx, acct3, oldCreated, ""); err != nil {
			t.Fatalf("MarkStep: %v", err)
		}
		now := time.Now().UTC()
		if err := st.CheckMigrationGate(ctx, acct3, now); err != nil {
			t.Fatalf("expected gate open for 8-day-old account, got %v", err)
		}
	})

	t.Run("migration_gate_exactly_7_days", func(t *testing.T) {
		acct4 := "acc-7d"
		sevenDaysAgo := time.Now().UTC().Add(-7 * 24 * time.Hour)
		if _, err := st.MarkStep(ctx, acct4, sevenDaysAgo, ""); err != nil {
			t.Fatalf("MarkStep: %v", err)
		}
		// Exactly at the boundary — gate should be open (>= 7 days).
		now := sevenDaysAgo.Add(7 * 24 * time.Hour)
		if err := st.CheckMigrationGate(ctx, acct4, now); err != nil {
			t.Fatalf("expected gate open at exactly 7 days, got %v", err)
		}
	})

	t.Run("migration_gate_no_row", func(t *testing.T) {
		err := st.CheckMigrationGate(ctx, "acc-no-row", time.Now().UTC())
		if err != onboarding.ErrMigrationGated {
			t.Fatalf("expected ErrMigrationGated for missing account, got %v", err)
		}
	})

	t.Run("DaysUntilMigration", func(t *testing.T) {
		s := onboarding.State{
			AccountCreatedAt: time.Now().UTC().Add(-3 * 24 * time.Hour),
		}
		days := s.DaysUntilMigration(time.Now().UTC())
		// 3 days elapsed → 4 days remaining (7 - 3)
		if days != 4 {
			t.Fatalf("expected 4 days remaining, got %d", days)
		}
	})

	t.Run("DaysUntilMigration_open", func(t *testing.T) {
		s := onboarding.State{
			AccountCreatedAt: time.Now().UTC().Add(-10 * 24 * time.Hour),
		}
		if s.DaysUntilMigration(time.Now().UTC()) != 0 {
			t.Fatal("expected 0 days remaining for old account")
		}
	})
}

// ─── MemStore ─────────────────────────────────────────────────────────────────

func TestMemStore(t *testing.T) {
	storeTests(t, onboarding.NewMemStore())
}

// ─── SQLStore ─────────────────────────────────────────────────────────────────

func TestSQLStore(t *testing.T) {
	storeTests(t, newSQLStore(t))
}

func TestSQLStore_Persistence(t *testing.T) {
	st := newSQLStore(t)
	ctx := context.Background()
	acct := "acc-persist"
	created := time.Now().UTC().Add(-2 * 24 * time.Hour)

	if _, err := st.MarkStep(ctx, acct, created, "domain_connected"); err != nil {
		t.Fatalf("MarkStep: %v", err)
	}

	s, err := st.GetState(ctx, acct)
	if err != nil {
		t.Fatalf("GetState: %v", err)
	}
	if !s.DomainConnected {
		t.Fatal("domain_connected should persist")
	}
	if s.CompletedSteps() != 1 {
		t.Fatalf("expected 1, got %d", s.CompletedSteps())
	}

	// Re-marking the same step should be idempotent.
	s2, err := st.MarkStep(ctx, acct, created, "domain_connected")
	if err != nil {
		t.Fatalf("MarkStep idempotent: %v", err)
	}
	if !s2.DomainConnected {
		t.Fatal("domain_connected should still be set")
	}

	// account_created_at should not change on re-insert attempt.
	if !s2.AccountCreatedAt.Equal(s.AccountCreatedAt) {
		t.Fatalf("account_created_at changed: %v → %v", s.AccountCreatedAt, s2.AccountCreatedAt)
	}
}
