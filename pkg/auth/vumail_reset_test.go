package auth

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"
)

// ── mock InboxSender ──────────────────────────────────────────────────────────

type mockInboxSender struct {
	calls    atomic.Int32
	lastTo   atomic.Value // stores string
	lastSubj atomic.Value
	lastBody atomic.Value
	failWith error
}

func (m *mockInboxSender) DeliverSystemMessage(_ context.Context, recipientHandle, subject, body string) error {
	m.calls.Add(1)
	m.lastTo.Store(recipientHandle)
	m.lastSubj.Store(subject)
	m.lastBody.Store(body)
	return m.failWith
}

// ── helpers ───────────────────────────────────────────────────────────────────

// signupVumail creates a user with a @vulos.org email in the test store.
func signupVulos(t *testing.T, st *Store, handle, password string) string {
	t.Helper()
	ctx := context.Background()
	email := handle + "@vulos.org"
	u, _, err := st.Signup(ctx, email, password, "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("signupVumail %s: %v", handle, err)
	}
	return u.ID
}

// enableTOTPForUser sets up TOTP for a user and returns the plaintext base32
// secret so the test can generate valid codes.
func enableTOTPForUser(t *testing.T, st *Store, userID string) string {
	t.Helper()
	ctx := context.Background()

	os.Setenv("VULOS_DEV", "true")

	_, secretB32, err := GenerateTOTPSecret(userID + "@vulos.org")
	if err != nil {
		t.Fatalf("GenerateTOTPSecret: %v", err)
	}
	kek, err := LoadTOTPKEK()
	if err != nil {
		t.Fatalf("LoadTOTPKEK: %v", err)
	}
	enc, err := EncryptTOTPSecret([]byte(secretB32), kek)
	if err != nil {
		t.Fatalf("EncryptTOTPSecret: %v", err)
	}
	if err := st.SaveTOTPSecret(ctx, userID, enc); err != nil {
		t.Fatalf("SaveTOTPSecret: %v", err)
	}
	if err := st.EnableTOTP(ctx, userID); err != nil {
		t.Fatalf("EnableTOTP: %v", err)
	}
	return secretB32
}

// ── Test 1: external email rejected with ErrExternalEmail ────────────────────

func TestVulosReset_ExternalEmailRejected(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	sender := &mockInboxSender{}

	err := st.RequestPasswordResetByHandle(ctx, "alice@gmail.com", sender)
	if !errors.Is(err, ErrExternalEmail) {
		t.Errorf("expected ErrExternalEmail for external domain, got %v", err)
	}
	if sender.calls.Load() != 0 {
		t.Error("sender should not be called for external email")
	}
}

// ── Test 2: non-existent handle returns pretend-200 (no enumeration leak) ────

func TestVulosReset_NonExistentHandlePretends200(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	sender := &mockInboxSender{}

	err := st.RequestPasswordResetByHandle(ctx, "ghostuser", sender)
	if err != nil {
		t.Errorf("unknown handle should return nil (pretend-200), got %v", err)
	}
	if sender.calls.Load() != 0 {
		t.Error("sender should not be called for unknown handle")
	}
}

// ── Test 3: existing handle → token signed correctly + message delivered ─────

func TestVulosReset_ExistingHandle_TokenSignedAndDelivered(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	sender := &mockInboxSender{}

	userID := signupVulos(t, st, "bob", "SecurePassw0rd123!")

	if err := st.RequestPasswordResetByHandle(ctx, "bob", sender); err != nil {
		t.Fatalf("RequestPasswordResetByHandle: %v", err)
	}

	// Verify sender was called exactly once with the correct recipient.
	if n := sender.calls.Load(); n != 1 {
		t.Errorf("expected 1 sender call, got %d", n)
	}
	gotHandle, _ := sender.lastTo.Load().(string)
	if gotHandle != "bob" {
		t.Errorf("delivered to wrong handle: got %q, want %q", gotHandle, "bob")
	}

	// The body must contain the reset link.
	body, _ := sender.lastBody.Load().(string)
	if !strings.Contains(body, "https://vulos.org/reset?token=") {
		t.Errorf("body does not contain reset link: %s", body)
	}

	// Extract the token from the body.
	const prefix = "https://vulos.org/reset?token="
	linkStart := strings.Index(body, prefix)
	if linkStart < 0 {
		t.Fatal("reset link not found in body")
	}
	rest := body[linkStart+len(prefix):]
	// Token ends at the next whitespace or end of string.
	end := strings.IndexAny(rest, " \n\r\t")
	if end < 0 {
		end = len(rest)
	}
	token := rest[:end]

	// Token must have the <raw>.<sig> structure.
	if !strings.Contains(token, ".") {
		t.Errorf("token missing HMAC signature: %q", token)
	}

	// Token must be stored in the DB with the correct user_id.
	var dbUserID string
	err := st.db.QueryRowContext(ctx, `SELECT user_id FROM password_resets WHERE token = ?`, token).Scan(&dbUserID)
	if err != nil {
		t.Fatalf("token not found in DB: %v", err)
	}
	if dbUserID != userID {
		t.Errorf("DB user_id mismatch: got %q, want %q", dbUserID, userID)
	}

	// HMAC signature must verify.
	var expiresAt string
	st.db.QueryRowContext(ctx, `SELECT expires_at FROM password_resets WHERE token = ?`, token).Scan(&expiresAt)
	_, ok := st.verifyResetTokenSignature(token, userID, expiresAt)
	if !ok {
		t.Error("HMAC signature verification failed for freshly-generated token")
	}
}

// ── Test 4: expired token rejected with ErrResetExpired ──────────────────────

func TestVulosReset_ExpiredToken_Rejected(t *testing.T) {
	os.Setenv("VULOS_DEV", "true")
	st := openTestStore(t)
	ctx := context.Background()

	userID := signupVulos(t, st, "carol", "SecurePassw0rd123!")
	enableTOTPForUser(t, st, userID)

	// Build an already-expired token.
	past := time.Now().UTC().Add(-20 * time.Minute)
	expiresAt := past.Format(time.RFC3339)
	raw := make([]byte, resetTokenLen)
	for i := range raw {
		raw[i] = byte(i % 256) // deterministic fill
	}
	token := st.signResetToken(raw, userID, expiresAt)
	st.db.ExecContext(ctx,
		`INSERT INTO password_resets (token, user_id, created_at, expires_at, used) VALUES (?,?,?,?,0)`,
		token, userID, past.Format(time.RFC3339), expiresAt,
	)

	err := st.ConfirmPasswordResetWithTOTP(ctx, token, "000000", "NewPassw0rd456!")
	if !errors.Is(err, ErrResetExpired) {
		t.Errorf("expected ErrResetExpired for expired token, got %v", err)
	}
}

// ── Test 5: token replay (second use) rejected with ErrResetExpired ──────────

func TestVulosReset_TokenReplay_Rejected(t *testing.T) {
	os.Setenv("VULOS_DEV", "true")
	st := openTestStore(t)
	ctx := context.Background()

	userID := signupVulos(t, st, "dave", "SecurePassw0rd123!")
	secretB32 := enableTOTPForUser(t, st, userID)

	// Insert a valid (non-expired) token.
	n := time.Now().UTC()
	expiresAt := n.Add(15 * time.Minute).Format(time.RFC3339)
	raw := make([]byte, resetTokenLen)
	for i := range raw {
		raw[i] = byte('d')
	}
	token := st.signResetToken(raw, userID, expiresAt)
	st.db.ExecContext(ctx,
		`INSERT INTO password_resets (token, user_id, created_at, expires_at, used) VALUES (?,?,?,?,0)`,
		token, userID, n.Format(time.RFC3339), expiresAt,
	)

	// Generate a valid TOTP code.
	totpCode, err := totp.GenerateCode(secretB32, time.Now())
	if err != nil {
		t.Fatalf("totp.GenerateCode: %v", err)
	}

	// First use must succeed.
	if err := st.ConfirmPasswordResetWithTOTP(ctx, token, totpCode, "NewPassw0rd456!"); err != nil {
		t.Fatalf("first reset use: %v", err)
	}

	// Second use must fail (token is now marked used).
	err = st.ConfirmPasswordResetWithTOTP(ctx, token, totpCode, "AnotherPass789!")
	if !errors.Is(err, ErrResetExpired) {
		t.Errorf("expected ErrResetExpired on token replay, got %v", err)
	}
}

// ── Test 6: reset without TOTP code returns ErrResetTOTPRequired ─────────────

func TestVulosReset_NoTOTPCode_ReturnsRequired(t *testing.T) {
	os.Setenv("VULOS_DEV", "true")
	st := openTestStore(t)
	ctx := context.Background()

	userID := signupVulos(t, st, "eve", "SecurePassw0rd123!")
	enableTOTPForUser(t, st, userID)

	// Insert a valid (non-expired) token.
	n := time.Now().UTC()
	expiresAt := n.Add(15 * time.Minute).Format(time.RFC3339)
	raw := make([]byte, resetTokenLen)
	for i := range raw {
		raw[i] = byte('e')
	}
	token := st.signResetToken(raw, userID, expiresAt)
	st.db.ExecContext(ctx,
		`INSERT INTO password_resets (token, user_id, created_at, expires_at, used) VALUES (?,?,?,?,0)`,
		token, userID, n.Format(time.RFC3339), expiresAt,
	)

	// Empty TOTP code → ErrResetTOTPRequired.
	err := st.ConfirmPasswordResetWithTOTP(ctx, token, "", "NewPassw0rd456!")
	if !errors.Is(err, ErrResetTOTPRequired) {
		t.Errorf("expected ErrResetTOTPRequired when TOTP code is empty, got %v", err)
	}
}
