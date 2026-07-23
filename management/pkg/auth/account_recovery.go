// account_recovery.go — the two account-store operations the recovery flow needs.
//
//  1. ResetTOTP satisfies recovery.TOTPResetter, the seam
//     internal/auth/recovery.Complete() calls once the 14-day window has elapsed
//     and an admin has verified the review token. Without it the engine cannot
//     finish a recovery in production at all.
//
//  2. ResetPasswordWithRecoveryCode makes a recovery code a PASSWORD-RESET
//     credential, not just a 2FA fallback. This is what closes the recovery
//     circle: /api/auth/forgot-password delivers its link to the user's own
//     Vulos inbox, so an account locked out of that inbox has no reachable reset
//     path. A recovery code — minted at signup, held by the user, out of band —
//     is one, and it needs no mailbox at all.
//
// Both reuse the existing totp_recovery_codes table (totp.go): one pool of
// single-use argon2id-hashed codes per account, replaced wholesale whenever it
// is re-minted (signup, TOTP enrollment, recovery completion).
package auth

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ErrRecoveryCodeInvalid is returned when a recovery code does not match any
// unused code for the account — and, deliberately, when the account does not
// exist at all. The caller maps both to the same 401 so the endpoint is not an
// account-existence oracle.
var ErrRecoveryCodeInvalid = errors.New("auth: invalid or already-used recovery code")

// MintRecoveryCodes generates a fresh pool of single-use recovery codes for
// userID, replaces any existing pool, and returns the PLAINTEXT codes. They are
// shown to the user exactly once — only argon2id hashes are stored.
func (s *Store) MintRecoveryCodes(ctx context.Context, userID string) ([]string, error) {
	codes, hashes, err := GenerateRecoveryCodes()
	if err != nil {
		return nil, err
	}
	if err := s.InsertRecoveryCodes(ctx, userID, hashes); err != nil {
		return nil, err
	}
	return codes, nil
}

// ResetTOTP disables TOTP for accountID and mints a fresh pool of recovery
// codes, returning the plaintext codes for one-time display. It implements
// recovery.TOTPResetter.
//
// It does NOT verify eligibility: the 14-day window, the review-token check and
// the request status are all enforced by recovery.Complete before it calls here.
func (s *Store) ResetTOTP(ctx context.Context, accountID string) ([]string, error) {
	if accountID == "" {
		return nil, ErrNotFound
	}
	// DisableTOTP drops the old code pool with the secret; MintRecoveryCodes
	// then writes the new one. Neither may be called while holding s.mu.
	if err := s.DisableTOTP(ctx, accountID); err != nil {
		return nil, err
	}
	codes, err := s.MintRecoveryCodes(ctx, accountID)
	if err != nil {
		return nil, err
	}
	return codes, nil
}

// ResetPasswordWithRecoveryCode burns one of the account's recovery codes and
// sets a new password, revoking every live session.
//
// handle may be a bare handle or an in-network address; an external address is
// rejected with ErrExternalEmail (the caller maps it to 400 — the user typed the
// wrong field). An unknown handle and a wrong code both return
// ErrRecoveryCodeInvalid, so the endpoint reveals nothing about which accounts
// exist.
//
// The password policy is checked twice: the baseline policy BEFORE the account
// is resolved (so a weak password is rejected identically whether or not the
// account exists), and the account's own policy after the code is burned (a
// fleet-admin's stricter rule cannot be probed without a valid code).
func (s *Store) ResetPasswordWithRecoveryCode(ctx context.Context, handle, code, newPassword string) error {
	if err := validatePasswordPolicy(newPassword, false); err != nil {
		return err
	}

	h, err := canonicaliseHandle(handle)
	if err != nil {
		if errors.Is(err, ErrExternalEmail) {
			return ErrExternalEmail
		}
		return ErrRecoveryCodeInvalid
	}

	userID, err := s.UserIDForEmail(ctx, handleToEmail(h))
	if err != nil {
		return err
	}
	if userID == "" || code == "" {
		return ErrRecoveryCodeInvalid
	}

	burned, err := s.BurnRecoveryCode(ctx, userID, code)
	if err != nil {
		return err
	}
	if !burned {
		return ErrRecoveryCodeInvalid
	}

	if err := validatePasswordPolicy(newPassword, s.IsFleetAdmin(ctx, userID)); err != nil {
		return err
	}
	hash, err := HashPassword(newPassword)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	ts := now().Format(time.RFC3339)
	if _, err := s.db.ExecContext(ctx,
		s.db.Rebind(`UPDATE users SET password_hash = ?, updated_at = ? WHERE id = ?`),
		hash, ts, userID,
	); err != nil {
		return fmt.Errorf("auth: recovery-code reset update password: %w", err)
	}
	// A password reset from a recovery code is exactly the situation where an
	// attacker may already hold a session: kill them all.
	if _, err := s.db.ExecContext(ctx,
		s.db.Rebind(`DELETE FROM sessions WHERE user_id = ?`), userID,
	); err != nil {
		return fmt.Errorf("auth: recovery-code reset invalidate sessions: %w", err)
	}
	return nil
}

// UnusedRecoveryCodeCount returns how many single-use recovery codes the account
// still holds. Surfaced so the account UI can nag a user who is down to their
// last codes — a locked-out user with zero codes and no recovery address has
// nothing left but the reviewed 14-day flow.
func (s *Store) UnusedRecoveryCodeCount(ctx context.Context, userID string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		s.db.Rebind(`SELECT COUNT(*) FROM totp_recovery_codes WHERE user_id = ? AND used = 0`), userID,
	).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("auth: count recovery codes: %w", err)
	}
	return n, nil
}
