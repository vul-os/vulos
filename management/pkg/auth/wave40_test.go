package auth

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"
)

// insertResetToken signs + inserts a live (15-min) password-reset row and
// returns the raw token. Mirrors the setup in vumail_reset_test.go so the
// wave-40 negative cases share the same known-good path.
func insertResetToken(t *testing.T, st *Store, userID string, fill byte) string {
	t.Helper()
	ctx := context.Background()
	n := time.Now().UTC()
	expiresAt := n.Add(15 * time.Minute).Format(time.RFC3339)
	raw := make([]byte, resetTokenLen)
	for i := range raw {
		raw[i] = fill
	}
	token := st.signResetToken(raw, userID, expiresAt)
	if _, err := st.db.ExecContext(ctx,
		`INSERT INTO password_resets (token, user_id, created_at, expires_at, used) VALUES (?,?,?,?,0)`,
		token, userID, n.Format(time.RFC3339), expiresAt,
	); err != nil {
		t.Fatalf("insert reset token: %v", err)
	}
	return token
}

// TestVulosReset_WrongTOTPCode_Rejected covers the ErrTOTPInvalid branch of
// ConfirmPasswordResetWithTOTP: a syntactically valid but incorrect TOTP code
// must NOT reset the password. Previously only the happy path and the
// empty-code (ErrResetTOTPRequired) path were exercised.
func TestVulosReset_WrongTOTPCode_Rejected(t *testing.T) {
	os.Setenv("VULOS_DEV", "true")
	st := openTestStore(t)
	ctx := context.Background()

	userID := signupVulos(t, st, "wrongtotp", "SecurePassw0rd123!")
	_ = enableTOTPForUser(t, st, userID)
	token := insertResetToken(t, st, userID, 'w')

	// "000000" is a well-formed 6-digit code that will not match the secret.
	err := st.ConfirmPasswordResetWithTOTP(ctx, token, "000000", "NewPassw0rd456!")
	if !errors.Is(err, ErrTOTPInvalid) {
		t.Fatalf("wrong TOTP code must be ErrTOTPInvalid, got %v", err)
	}

	// The token must still be unused (a failed TOTP must not burn the reset).
	var used int
	if err := st.db.QueryRowContext(ctx,
		st.db.Rebind(`SELECT used FROM password_resets WHERE token = ?`), token).Scan(&used); err != nil {
		t.Fatalf("query used: %v", err)
	}
	if used != 0 {
		t.Fatal("a failed TOTP verify must not mark the reset token used")
	}
}

// TestVulosReset_TamperedTokenSignature_Rejected covers the HMAC-signature
// verification branch: a token whose bytes were altered after signing (so the
// row exists but the signature no longer verifies) must be rejected as
// ErrResetExpired, never allowed to reset the password.
func TestVulosReset_TamperedTokenSignature_Rejected(t *testing.T) {
	os.Setenv("VULOS_DEV", "true")
	st := openTestStore(t)
	ctx := context.Background()

	userID := signupVulos(t, st, "tampered", "SecurePassw0rd123!")
	secretB32 := enableTOTPForUser(t, st, userID)
	token := insertResetToken(t, st, userID, 't')

	// Flip one character of the stored token so the HMAC no longer verifies but
	// the row still matches on lookup.
	tampered := "A" + token[1:]
	if tampered == token {
		tampered = "B" + token[1:]
	}
	if _, err := st.db.ExecContext(ctx,
		`UPDATE password_resets SET token = ? WHERE token = ?`, tampered, token); err != nil {
		t.Fatalf("tamper update: %v", err)
	}

	totpCode, err := totp.GenerateCode(secretB32, time.Now())
	if err != nil {
		t.Fatalf("totp.GenerateCode: %v", err)
	}
	if err := st.ConfirmPasswordResetWithTOTP(ctx, tampered, totpCode, "NewPassw0rd456!"); !errors.Is(err, ErrResetExpired) {
		t.Fatalf("tampered-signature token must be ErrResetExpired, got %v", err)
	}
}

// TestVulosReset_UnknownToken_Rejected covers the sql.ErrNoRows branch: a token
// that was never issued is rejected as ErrResetExpired.
func TestVulosReset_UnknownToken_Rejected(t *testing.T) {
	os.Setenv("VULOS_DEV", "true")
	st := openTestStore(t)
	ctx := context.Background()
	if err := st.ConfirmPasswordResetWithTOTP(ctx, "never-issued-token", "000000", "NewPassw0rd456!"); !errors.Is(err, ErrResetExpired) {
		t.Fatalf("unknown reset token must be ErrResetExpired, got %v", err)
	}
}

// TestVerifyTOTP_ValidAndInvalid exercises VerifyTOTP directly (the non-replay
// verify used outside login), covering both the accept and reject branches that
// were previously untested (0% coverage).
func TestVerifyTOTP_ValidAndInvalid(t *testing.T) {
	setupTOTPTestEnv(t)
	_, secretB32, err := GenerateTOTPSecret("verify@example.com")
	if err != nil {
		t.Fatalf("GenerateTOTPSecret: %v", err)
	}
	code, err := totp.GenerateCode(secretB32, time.Now())
	if err != nil {
		t.Fatalf("GenerateCode: %v", err)
	}
	if !VerifyTOTP([]byte(secretB32), code) {
		t.Fatal("valid TOTP code must verify")
	}
	// VerifyTOTP has no replay protection: the same code verifies twice.
	if !VerifyTOTP([]byte(secretB32), code) {
		t.Fatal("VerifyTOTP (no replay cache) must accept the same code again")
	}
	if VerifyTOTP([]byte(secretB32), "000000") {
		t.Fatal("wrong code must be rejected")
	}
}

// TestVerifyTOTPAndRecord_InvalidCodeNotRecorded covers the early-return branch
// of VerifyTOTPAndRecord where the code fails cryptographic validation: it must
// return false WITHOUT consuming a replay-cache slot, so a subsequent VALID code
// still succeeds.
func TestVerifyTOTPAndRecord_InvalidCodeNotRecorded(t *testing.T) {
	setupTOTPTestEnv(t)
	_, secretB32, err := GenerateTOTPSecret("invalid@example.com")
	if err != nil {
		t.Fatalf("GenerateTOTPSecret: %v", err)
	}
	userID := "wave40-invalid-code-user"

	if VerifyTOTPAndRecord([]byte(secretB32), "000000", userID) {
		t.Fatal("invalid code must return false")
	}
	// A valid code afterwards must still work (invalid attempt recorded nothing).
	code, err := totp.GenerateCode(secretB32, time.Now())
	if err != nil {
		t.Fatalf("GenerateCode: %v", err)
	}
	if !VerifyTOTPAndRecord([]byte(secretB32), code, userID) {
		t.Fatal("valid code after an invalid attempt must succeed")
	}
}
