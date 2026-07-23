// lockout_test.go -- tests for AUTH-07: failed 2FA burst lockout.
//
// Acceptance criteria:
//   - 5 failed TOTP attempts → account locked; Check2FALockout returns true
//   - Locked account: retry_after is positive
//   - Successful verify resets failed_2fa_count and unlocks
//   - Unlock after simulated time advance (LockoutDuration injectable)
package auth

import (
	"context"
	"testing"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

// setupLockoutUser creates a user and enables TOTP so partial-session lockout
// can be exercised.
func setupLockoutUser(t *testing.T, st *Store, email string) (userID string) {
	t.Helper()
	ctx := context.Background()
	u, _, err := st.Signup(ctx, email, "securePass1234!", "127.0.0.1", "test-ua")
	if err != nil {
		t.Fatalf("Signup: %v", err)
	}
	return u.ID
}

// ─────────────────────────────────────────────────────────────────────────────
// Lockout: 5 failures → locked
// ─────────────────────────────────────────────────────────────────────────────

func TestLockout_FiveFailures_Locks(t *testing.T) {
	setupTOTPTestEnv(t)
	st := openTestStore(t)
	ctx := context.Background()
	userID := setupLockoutUser(t, st, "lock5@example.com")

	// Should not be locked initially.
	_, locked := st.Check2FALockout(ctx, userID)
	if locked {
		t.Fatal("account should not be locked initially")
	}

	// 4 failures: not yet locked.
	for i := 0; i < 4; i++ {
		if err := st.RecordFailed2FA(ctx, userID); err != nil {
			t.Fatalf("RecordFailed2FA iteration %d: %v", i, err)
		}
		_, locked := st.Check2FALockout(ctx, userID)
		if locked {
			t.Errorf("account should not be locked after %d failures", i+1)
		}
	}

	// 5th failure: should lock.
	if err := st.RecordFailed2FA(ctx, userID); err != nil {
		t.Fatalf("RecordFailed2FA 5th: %v", err)
	}
	lockedUntil, locked := st.Check2FALockout(ctx, userID)
	if !locked {
		t.Fatal("account should be locked after 5 failures")
	}
	retryAfter := lockedUntil - time.Now().Unix()
	if retryAfter <= 0 {
		t.Errorf("retry_after should be positive, got %d", retryAfter)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Lockout: reset on success
// ─────────────────────────────────────────────────────────────────────────────

func TestLockout_ResetOnSuccess(t *testing.T) {
	setupTOTPTestEnv(t)
	st := openTestStore(t)
	ctx := context.Background()
	userID := setupLockoutUser(t, st, "lockrst@example.com")

	// Record 5 failures to lock.
	for i := 0; i < 5; i++ {
		if err := st.RecordFailed2FA(ctx, userID); err != nil {
			t.Fatalf("RecordFailed2FA: %v", err)
		}
	}
	_, locked := st.Check2FALockout(ctx, userID)
	if !locked {
		t.Fatal("expected account to be locked")
	}

	// Successful verify resets.
	if err := st.ResetFailed2FA(ctx, userID); err != nil {
		t.Fatalf("ResetFailed2FA: %v", err)
	}

	_, locked = st.Check2FALockout(ctx, userID)
	if locked {
		t.Error("account should no longer be locked after successful reset")
	}

	// Verify counter is actually reset (recording 4 more failures should not lock again).
	for i := 0; i < 4; i++ {
		if err := st.RecordFailed2FA(ctx, userID); err != nil {
			t.Fatalf("RecordFailed2FA post-reset iteration %d: %v", i, err)
		}
	}
	_, locked = st.Check2FALockout(ctx, userID)
	if locked {
		t.Error("account should not be locked after only 4 failures since last reset")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Lockout: unlock after time advance
// ─────────────────────────────────────────────────────────────────────────────

func TestLockout_UnlockAfterTimeAdvance(t *testing.T) {
	setupTOTPTestEnv(t)

	// Inject a short lockout duration (1 second) so we can advance time.
	origDuration := LockoutDuration
	LockoutDuration = 1 * time.Second
	defer func() { LockoutDuration = origDuration }()

	st := openTestStore(t)
	ctx := context.Background()
	userID := setupLockoutUser(t, st, "locktime@example.com")

	// Record 5 failures.
	for i := 0; i < 5; i++ {
		if err := st.RecordFailed2FA(ctx, userID); err != nil {
			t.Fatalf("RecordFailed2FA: %v", err)
		}
	}
	_, locked := st.Check2FALockout(ctx, userID)
	if !locked {
		t.Fatal("expected account to be locked")
	}

	// Advance the clock beyond the lockout window.
	origNow := now
	now = func() time.Time { return time.Now().UTC().Add(2 * time.Second) }
	defer func() { now = origNow }()

	_, locked = st.Check2FALockout(ctx, userID)
	if locked {
		t.Error("account should be unlocked after the lockout duration has elapsed")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Lockout: retry_after value is consistent
// ─────────────────────────────────────────────────────────────────────────────

func TestLockout_RetryAfterIsPositive(t *testing.T) {
	setupTOTPTestEnv(t)

	origDuration := LockoutDuration
	LockoutDuration = 15 * time.Minute
	defer func() { LockoutDuration = origDuration }()

	st := openTestStore(t)
	ctx := context.Background()
	userID := setupLockoutUser(t, st, "lockra@example.com")

	for i := 0; i < 5; i++ {
		_ = st.RecordFailed2FA(ctx, userID)
	}

	lockedUntil, locked := st.Check2FALockout(ctx, userID)
	if !locked {
		t.Fatal("expected lock")
	}

	retryAfter := lockedUntil - time.Now().UTC().Unix()
	// Should be close to 15*60 = 900 seconds (within 5 second jitter for test execution).
	if retryAfter < 890 || retryAfter > 910 {
		t.Errorf("retry_after out of expected range [890,910]: %d", retryAfter)
	}
}
