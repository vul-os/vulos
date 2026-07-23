package auth

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
)

// mockExternalSender records off-network deliveries (the recovery-address copy).
type mockExternalSender struct {
	calls  atomic.Int32
	lastTo atomic.Value // string
}

func (m *mockExternalSender) DeliverExternalMessage(_ context.Context, toAddr, _, _ string) error {
	m.calls.Add(1)
	m.lastTo.Store(toAddr)
	return nil
}

// ── ResetTOTP: the seam recovery.Complete() needs ─────────────────────────────

func TestResetTOTP_DisablesTOTPAndMintsFreshCodes(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	userID := signupVulos(t, st, "resetme", "SecurePassw0rd123!")
	enableTOTPForUser(t, st, userID)

	oldCodes, oldHashes, err := GenerateRecoveryCodes()
	if err != nil {
		t.Fatalf("GenerateRecoveryCodes: %v", err)
	}
	if err := st.InsertRecoveryCodes(ctx, userID, oldHashes); err != nil {
		t.Fatalf("InsertRecoveryCodes: %v", err)
	}

	newCodes, err := st.ResetTOTP(ctx, userID)
	if err != nil {
		t.Fatalf("ResetTOTP: %v", err)
	}
	if len(newCodes) == 0 {
		t.Fatal("ResetTOTP minted no recovery codes")
	}
	if st.IsTOTPEnabled(ctx, userID) {
		t.Fatal("TOTP is still enabled after ResetTOTP")
	}

	// The codes from before the reset are dead …
	burned, err := st.BurnRecoveryCode(ctx, userID, oldCodes[0])
	if err != nil {
		t.Fatalf("BurnRecoveryCode(old): %v", err)
	}
	if burned {
		t.Fatal("a pre-reset recovery code still verifies")
	}
	// … and the new ones are live.
	burned, err = st.BurnRecoveryCode(ctx, userID, newCodes[0])
	if err != nil {
		t.Fatalf("BurnRecoveryCode(new): %v", err)
	}
	if !burned {
		t.Fatal("a code minted by ResetTOTP does not verify")
	}
}

// ── Recovery code → password reset ────────────────────────────────────────────

func TestResetPasswordWithRecoveryCode(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	userID := signupVulos(t, st, "codereset", "SecurePassw0rd123!")

	codes, err := st.MintRecoveryCodes(ctx, userID)
	if err != nil {
		t.Fatalf("MintRecoveryCodes: %v", err)
	}
	// A live session — an attacker's, or a stale one on a lost device.
	if _, err := st.createSession(ctx, userID, "127.0.0.1", "test"); err != nil {
		t.Fatalf("createSession: %v", err)
	}

	const newPassword = "AnotherStrongPass99!"
	if err := st.ResetPasswordWithRecoveryCode(ctx, "codereset", codes[0], newPassword); err != nil {
		t.Fatalf("ResetPasswordWithRecoveryCode: %v", err)
	}

	if _, err := st.Login(ctx, "codereset@vulos.org", newPassword, "127.0.0.1", "test"); err != nil {
		t.Fatalf("login with the new password: %v", err)
	}
	if _, err := st.Login(ctx, "codereset@vulos.org", "SecurePassw0rd123!", "127.0.0.1", "test"); err == nil {
		t.Fatal("the old password still works")
	}

	// Every session the account had is gone.
	var live int
	if err := st.db.QueryRowContext(ctx,
		st.db.Rebind(`SELECT COUNT(*) FROM sessions WHERE user_id = ?`), userID,
	).Scan(&live); err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	// The login above minted one; anything more means the reset did not revoke.
	if live > 1 {
		t.Fatalf("sessions survived a recovery-code reset: %d", live)
	}

	// The code is single-use.
	if err := st.ResetPasswordWithRecoveryCode(ctx, "codereset", codes[0], "YetAnotherPass123!"); !errors.Is(err, ErrRecoveryCodeInvalid) {
		t.Fatalf("replaying a burned code: want ErrRecoveryCodeInvalid, got %v", err)
	}
}

func TestResetPasswordWithRecoveryCode_UnknownHandleIsIndistinguishable(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	userID := signupVulos(t, st, "realuser", "SecurePassw0rd123!")
	codes, err := st.MintRecoveryCodes(ctx, userID)
	if err != nil {
		t.Fatalf("MintRecoveryCodes: %v", err)
	}

	unknown := st.ResetPasswordWithRecoveryCode(ctx, "ghost", codes[0], "AnotherStrongPass99!")
	wrongCode := st.ResetPasswordWithRecoveryCode(ctx, "realuser", "NOT-A-CODE", "AnotherStrongPass99!")
	if !errors.Is(unknown, ErrRecoveryCodeInvalid) || !errors.Is(wrongCode, ErrRecoveryCodeInvalid) {
		t.Fatalf("unknown handle and wrong code must be the same error: unknown=%v wrong=%v", unknown, wrongCode)
	}

	// An external address is a hard reject (the user typed the wrong field) —
	// never a pretend-success that quietly does nothing.
	if err := st.ResetPasswordWithRecoveryCode(ctx, "someone@gmail.com", codes[0], "AnotherStrongPass99!"); !errors.Is(err, ErrExternalEmail) {
		t.Fatalf("external address: want ErrExternalEmail, got %v", err)
	}
}

// ── The out-of-network recovery address ───────────────────────────────────────

func TestRecoveryEmail_IsADestinationNotAnIdentity(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	userID := signupVulos(t, st, "backup", "SecurePassw0rd123!")
	const external = "backup-owner@example.net"

	if err := st.SetRecoveryEmail(ctx, userID, external); err != nil {
		t.Fatalf("SetRecoveryEmail: %v", err)
	}

	// NOTHING resolves an account from the recovery address.
	if _, err := st.UserIDByEmail(ctx, external); !errors.Is(err, ErrNotFound) {
		t.Fatalf("UserIDByEmail resolved the recovery address: %v", err)
	}
	id, err := st.UserIDForEmail(ctx, external)
	if err != nil || id != "" {
		t.Fatalf("UserIDForEmail resolved the recovery address: id=%q err=%v", id, err)
	}
	// Login on an unknown address burns a dummy hash and reports ErrHashMismatch
	// (it never says "no such account") — the recovery address lands there, i.e.
	// it resolves to nothing, even with the account's real password.
	if _, err := st.Login(ctx, external, "SecurePassw0rd123!", "127.0.0.1", "test"); !errors.Is(err, ErrHashMismatch) {
		t.Fatalf("the recovery address is a login path: %v", err)
	}
	// Nor can it be used to start a password reset.
	sender := &mockInboxSender{}
	if err := st.RequestPasswordResetByHandle(ctx, external, sender); !errors.Is(err, ErrExternalEmail) {
		t.Fatalf("forgot-password on the recovery address: want ErrExternalEmail, got %v", err)
	}
	if sender.calls.Load() != 0 {
		t.Fatal("a reset was delivered for the recovery address")
	}

	// Read-back is by userID only, and clearing works.
	got, err := st.RecoveryEmail(ctx, userID)
	if err != nil || got != external {
		t.Fatalf("RecoveryEmail = (%q, %v), want %q", got, err, external)
	}
	if err := st.ClearRecoveryEmail(ctx, userID); err != nil {
		t.Fatalf("ClearRecoveryEmail: %v", err)
	}
	if got, err := st.RecoveryEmail(ctx, userID); err != nil || got != "" {
		t.Fatalf("RecoveryEmail after clear = (%q, %v), want empty", got, err)
	}
}

func TestSetRecoveryEmail_RejectsInNetworkAndMalformed(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	userID := signupVulos(t, st, "picky", "SecurePassw0rd123!")

	if err := st.SetRecoveryEmail(ctx, userID, "someone@"+VulosDomain()); !errors.Is(err, ErrRecoveryEmailInNetwork) {
		t.Fatalf("in-network address: want ErrRecoveryEmailInNetwork, got %v", err)
	}
	if err := st.SetRecoveryEmail(ctx, userID, "not-an-email"); !errors.Is(err, ErrRecoveryEmailInvalid) {
		t.Fatalf("malformed address: want ErrRecoveryEmailInvalid, got %v", err)
	}
	if got, err := st.RecoveryEmail(ctx, userID); err != nil || got != "" {
		t.Fatalf("a rejected address was stored: (%q, %v)", got, err)
	}
}

func TestForgotPassword_CopiesLinkToRecoveryAddress(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	userID := signupVulos(t, st, "copied", "SecurePassw0rd123!")
	ext := &mockExternalSender{}
	st.SetExternalSender(ext)

	// No recovery address yet → nothing leaves the network.
	if err := st.RequestPasswordResetByHandle(ctx, "copied", &mockInboxSender{}); err != nil {
		t.Fatalf("RequestPasswordResetByHandle: %v", err)
	}
	if n := ext.calls.Load(); n != 0 {
		t.Fatalf("an off-network copy was sent with no recovery address configured (%d calls)", n)
	}

	if err := st.SetRecoveryEmail(ctx, userID, "owner@example.net"); err != nil {
		t.Fatalf("SetRecoveryEmail: %v", err)
	}
	inbox := &mockInboxSender{}
	if err := st.RequestPasswordResetByHandle(ctx, "copied", inbox); err != nil {
		t.Fatalf("RequestPasswordResetByHandle: %v", err)
	}
	if n := ext.calls.Load(); n != 1 {
		t.Fatalf("want 1 off-network copy, got %d", n)
	}
	if to, _ := ext.lastTo.Load().(string); to != "owner@example.net" {
		t.Fatalf("copy delivered to %q, want owner@example.net", to)
	}
	// The in-network delivery still happens — the copy is additive.
	if n := inbox.calls.Load(); n != 1 {
		t.Fatalf("want 1 in-network delivery, got %d", n)
	}
	if body, _ := inbox.lastBody.Load().(string); !strings.Contains(body, "/reset?token=") {
		t.Fatal("the in-network message no longer carries the reset link")
	}
}
