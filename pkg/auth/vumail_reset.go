// vumail_reset.go — Vulos-email-only password-reset flow.
//
// Design:
//   - Accept only @vulos.org addresses (or bare handle → auto-append).
//   - External email addresses are rejected with a hard 400 (never pretend-200
//     for them — the user typed the wrong field).
//   - For unknown handles the response is the same 200 as a known handle
//     (enumeration safety). A ~250 ms artificial latency is applied by the
//     HTTP handler so timing does not reveal existence.
//   - If the account exists, a signed HMAC-SHA256 token is generated and the
//     recovery link is delivered to the user's Vulos inbox (in-network JMAP
//     message from security@vulos.org).
//   - Reset tokens are 15-minute, single-use. Consuming one requires a valid
//     TOTP code (Branch A). Branch B (fully locked out) must use /account-recovery.
//
// InboxSender interface — implemented by the Vulos JMAP pipeline in production;
// stubbed in tests.
package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/vul-os/vulos-management/pkg/env"
)

// VulosDomain returns the canonical identity email domain, derived from
// env.Domain() so recovery works on a custom VULOS_DOMAIN deployment.
// VumailDomain is kept as an alias for backwards compatibility.
func VulosDomain() string  { return env.Domain() }
func VumailDomain() string { return env.Domain() }

// ErrExternalEmail is returned when the caller supplies an address that is not
// @vulos.org. The HTTP handler maps this to 400 (hard reject, NOT pretend-200).
var ErrExternalEmail = errors.New("auth: recovery supports @vulos.org addresses only")

// ErrResetTOTPRequired is returned by ConfirmPasswordResetWithTOTP when no
// TOTP code is supplied but the account has TOTP enabled.
var ErrResetTOTPRequired = errors.New("auth: TOTP code required to complete password reset")

// InboxSender delivers a system message to a user's Vulos inbox.
// The production implementation posts to the internal Vulos JMAP API.
// Tests inject a stub.
type InboxSender interface {
	// DeliverSystemMessage sends a plain-text message from security@vulos.org to
	// the recipient's @vulos.org inbox. subject and body are plain text.
	// Returns nil on success; a non-nil error should be treated as non-fatal by
	// the caller (the reset token is already stored — it can be retried).
	DeliverSystemMessage(ctx context.Context, recipientHandle, subject, body string) error
}

// NopInboxSender is a no-op InboxSender. Used when no sender is wired (dev
// mode) — the token is logged instead.
type NopInboxSender struct{}

func (NopInboxSender) DeliverSystemMessage(_ context.Context, handle, subject, body string) error {
	log.Printf("[auth] NopInboxSender: would deliver to %s@vulos.org — %s", handle, subject)
	return nil
}

// canonicaliseHandle normalises the input to a bare handle (no domain, lower-case).
//
//   - "alice"             → "alice", nil
//   - "alice@vulos.org"  → "alice", nil
//   - "alice@gmail.com"   → "", ErrExternalEmail
func canonicaliseHandle(input string) (string, error) {
	input = strings.ToLower(strings.TrimSpace(input))
	if input == "" {
		return "", fmt.Errorf("auth: handle must not be empty")
	}
	at := strings.Index(input, "@")
	if at < 0 {
		// Bare handle — no '@'. Validate characters (no spaces, etc.).
		if strings.ContainsAny(input, " \t\r\n") {
			return "", fmt.Errorf("auth: invalid handle")
		}
		return input, nil
	}
	domain := input[at+1:]
	if !strings.EqualFold(domain, VumailDomain()) {
		return "", ErrExternalEmail
	}
	handle := input[:at]
	if handle == "" {
		return "", fmt.Errorf("auth: handle must not be empty")
	}
	return handle, nil
}

// handleToEmail converts a bare handle to the canonical vulos.org email stored in
// the users table.
func handleToEmail(handle string) string {
	return handle + "@" + VumailDomain()
}

// signResetToken produces an HMAC-SHA256 signed token string:
//
//	<hex(32 random bytes)>.<hex(HMAC-SHA256 over random bytes + userID + expires)>
//
// The token stored in password_resets is the full signed string (used as PRIMARY
// KEY). Verification just re-derives the MAC.
func (s *Store) signResetToken(raw []byte, userID, expires string) string {
	mac := hmac.New(sha256.New, s.secret)
	mac.Write(raw)
	mac.Write([]byte(userID))
	mac.Write([]byte(expires))
	return hex.EncodeToString(raw) + "." + hex.EncodeToString(mac.Sum(nil))
}

// verifyResetTokenSignature checks the embedded MAC. Returns the raw hex prefix
// (first field) so the caller can look up the DB row.
func (s *Store) verifyResetTokenSignature(token, userID, expires string) (string, bool) {
	parts := strings.SplitN(token, ".", 2)
	if len(parts) != 2 {
		return "", false
	}
	rawHex, sigHex := parts[0], parts[1]
	raw, err := hex.DecodeString(rawHex)
	if err != nil || len(raw) != resetTokenLen {
		return "", false
	}
	mac := hmac.New(sha256.New, s.secret)
	mac.Write(raw)
	mac.Write([]byte(userID))
	mac.Write([]byte(expires))
	wantSig := hex.EncodeToString(mac.Sum(nil))
	return rawHex, hmac.Equal([]byte(sigHex), []byte(wantSig))
}

// RequestPasswordResetByHandle is the new entry point for the forgot-password
// flow. It accepts a handle (bare or @vulos.org). External domains return
// ErrExternalEmail immediately (400). Unknown handles silently return nil (200)
// to prevent enumeration.
//
// When the account exists:
//  1. A signed reset token is generated.
//  2. A recovery link is delivered to the user's Vulos inbox via sender.
//  3. The token + expiry are stored in password_resets (same table as before,
//     keyed on the full signed token string).
func (s *Store) RequestPasswordResetByHandle(ctx context.Context, handle string, sender InboxSender) error {
	h, err := canonicaliseHandle(handle)
	if err != nil {
		if errors.Is(err, ErrExternalEmail) {
			return ErrExternalEmail
		}
		// Other validation errors treated as unknown handle (pretend-200).
		return nil
	}

	email := handleToEmail(h)

	var userID string
	dbErr := s.db.QueryRowContext(ctx,
		s.db.Rebind(`SELECT id FROM users WHERE email = ?`), email,
	).Scan(&userID)
	if errors.Is(dbErr, sql.ErrNoRows) {
		// Enumeration-safe: unknown handle → nil.
		return nil
	}
	if dbErr != nil {
		return fmt.Errorf("auth: identity reset query: %w", dbErr)
	}

	// Generate raw random token.
	raw := make([]byte, resetTokenLen)
	if _, err := rand.Read(raw); err != nil {
		return fmt.Errorf("auth: generate vumail reset token: %w", err)
	}

	n := now()
	expires := n.Add(resetDuration)
	expiresStr := expires.Format(time.RFC3339)

	// Build signed token.
	token := s.signResetToken(raw, userID, expiresStr)

	s.mu.Lock()
	_, err = s.db.ExecContext(ctx,
		s.db.Rebind(`INSERT INTO password_resets (token, user_id, created_at, expires_at, used)
		 VALUES (?, ?, ?, ?, 0)`),
		token, userID,
		n.Format(time.RFC3339),
		expiresStr,
	)
	s.mu.Unlock()
	if err != nil {
		return fmt.Errorf("auth: insert vumail reset token: %w", err)
	}

	// Do NOT log the token value.
	log.Printf("[auth] identity password reset requested for user_id=%s handle=%s (expires %s)", userID, h, expiresStr)

	// Deliver recovery link to the user's Vulos inbox.
	resetLink := env.SiteURL() + "/reset?token=" + token
	subject := "Reset your Vulos password"
	body := "Someone (hopefully you) requested a password reset for your Vulos account.\n\n" +
		"Click the link below to set a new password. You will need to enter your TOTP code.\n\n" +
		"  " + resetLink + "\n\n" +
		"This link expires in 15 minutes and can only be used once.\n\n" +
		"If you did not request this, you can ignore this message — your password has not been changed.\n\n" +
		"— security@vulos.org"

	if sErr := sender.DeliverSystemMessage(ctx, h, subject, body); sErr != nil {
		// Non-fatal: token is stored; the user can still complete the flow if
		// they find the link via another logged-in session. Log the delivery failure.
		log.Printf("[auth] identity reset: inbox delivery failed for handle=%s: %v", h, sErr)
	}

	// The inbox we just delivered to IS the account. If the holder cannot reach
	// it, that copy is useless — so copy the link to the out-of-network recovery
	// address when the account has opted in to one (recovery_email.go).
	s.copyToRecoveryAddress(ctx, userID, subject, body)

	return nil
}

// copyToRecoveryAddress delivers body to the account's out-of-network recovery
// address, if it has one and an ExternalSender is wired. Best-effort: a failure
// here never fails the flow that triggered it — the reset token is already
// stored and the in-network copy has already been sent.
func (s *Store) copyToRecoveryAddress(ctx context.Context, userID, subject, body string) {
	if s.externalSender == nil {
		return
	}
	addr, err := s.RecoveryEmail(ctx, userID)
	if err != nil {
		log.Printf("[auth] recovery address lookup failed for user_id=%s: %v", userID, err)
		return
	}
	if addr == "" {
		return
	}
	if err := s.externalSender.DeliverExternalMessage(ctx, addr, subject, body); err != nil {
		log.Printf("[auth] recovery address delivery failed for user_id=%s: %v", userID, err)
	}
}

// ValidateVumailResetToken checks that the token exists, is not expired, is not
// used, and has a valid HMAC signature. Returns whether the account has TOTP
// enabled (so the frontend can decide which form to show). Does NOT consume the
// token.
func (s *Store) ValidateVumailResetToken(ctx context.Context, token string) (totpEnabled bool, err error) {
	var (
		userID    string
		expiresAt string
		used      int
	)
	dbErr := s.db.QueryRowContext(ctx,
		s.db.Rebind(`SELECT user_id, expires_at, used FROM password_resets WHERE token = ?`), token,
	).Scan(&userID, &expiresAt, &used)
	if errors.Is(dbErr, sql.ErrNoRows) {
		return false, ErrResetExpired
	}
	if dbErr != nil {
		return false, fmt.Errorf("auth: validate reset token: %w", dbErr)
	}
	if used != 0 {
		return false, ErrResetExpired
	}
	exp, parseErr := time.Parse(time.RFC3339, expiresAt)
	if parseErr != nil || now().After(exp) {
		return false, ErrResetExpired
	}
	_, ok := s.verifyResetTokenSignature(token, userID, expiresAt)
	if !ok {
		return false, ErrResetExpired
	}
	return s.IsTOTPEnabled(ctx, userID), nil
}

// ValidateVulosResetToken is the canonical name for ValidateVumailResetToken.
// ValidateVumailResetToken is retained as an alias for callers not yet updated.
func (s *Store) ValidateVulosResetToken(ctx context.Context, token string) (totpEnabled bool, err error) {
	return s.ValidateVumailResetToken(ctx, token)
}

// ConfirmPasswordResetWithTOTP validates the signed token, verifies the TOTP
// code (required — 401 if absent/invalid), updates the password, and
// invalidates all sessions.
//
// Error mapping for callers:
//   - ErrResetExpired     → 410
//   - ErrResetTOTPRequired → 401
//   - ErrTOTPInvalid       → 401
//   - validation errors   → 400
func (s *Store) ConfirmPasswordResetWithTOTP(ctx context.Context, token, totpCode, newPassword string) error {
	// Look up the raw DB row.
	var (
		userID    string
		expiresAt string
		used      int
	)
	err := s.db.QueryRowContext(ctx,
		s.db.Rebind(`SELECT user_id, expires_at, used FROM password_resets WHERE token = ?`), token,
	).Scan(&userID, &expiresAt, &used)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrResetExpired
	}
	if err != nil {
		return fmt.Errorf("auth: identity reset confirm query: %w", err)
	}
	if used != 0 {
		return ErrResetExpired
	}
	exp, err := time.Parse(time.RFC3339, expiresAt)
	if err != nil || now().After(exp) {
		return ErrResetExpired
	}

	// Verify HMAC signature.
	_, ok := s.verifyResetTokenSignature(token, userID, expiresAt)
	if !ok {
		// Invalid signature — treat as expired/unknown.
		return ErrResetExpired
	}

	// TOTP is required.
	if totpCode == "" {
		return ErrResetTOTPRequired
	}

	// Load and verify TOTP.
	encSecret, enabled, totpErr := s.LoadTOTPSecret(ctx, userID)
	if totpErr != nil || !enabled || encSecret == nil {
		// Account has no TOTP configured — we still require TOTP.
		// (Accounts without TOTP should not be using this flow; return the
		// same error so the client can surface a clear message.)
		return ErrResetTOTPRequired
	}
	kek, kekErr := LoadTOTPKEK()
	if kekErr != nil {
		return fmt.Errorf("auth: identity reset totp kek: %w", kekErr)
	}
	plain, decErr := DecryptTOTPSecret(encSecret, kek)
	if decErr != nil {
		return fmt.Errorf("auth: identity reset decrypt totp: %w", decErr)
	}
	if !VerifyTOTPAndRecord(plain, totpCode, userID) {
		return ErrTOTPInvalid
	}

	// Validate new password policy.
	if err := validatePasswordPolicy(newPassword, s.IsFleetAdmin(ctx, userID)); err != nil {
		return err
	}

	hash, err := HashPassword(newPassword)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, err = s.db.ExecContext(ctx,
		s.db.Rebind(`UPDATE users SET password_hash = ?, updated_at = ? WHERE id = ?`),
		hash, now().Format(time.RFC3339), userID,
	); err != nil {
		return fmt.Errorf("auth: identity reset update password: %w", err)
	}
	if _, err = s.db.ExecContext(ctx,
		s.db.Rebind(`UPDATE password_resets SET used = 1 WHERE token = ?`), token,
	); err != nil {
		return fmt.Errorf("auth: identity reset mark used: %w", err)
	}
	if _, err = s.db.ExecContext(ctx,
		s.db.Rebind(`DELETE FROM sessions WHERE user_id = ?`), userID,
	); err != nil {
		return fmt.Errorf("auth: identity reset invalidate sessions: %w", err)
	}
	return nil
}
