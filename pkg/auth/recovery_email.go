// recovery_email.go — the optional out-of-network recovery destination.
//
// THE CIRCLE THIS BREAKS: a Vulos account is a Vulos mailbox. The
// forgot-password flow (vumail_reset.go) delivers the reset link INTO that
// mailbox and hard-rejects external addresses — so an account whose owner
// cannot reach the inbox cannot be recovered through it.
//
// recovery_email is a destination the account holder may CHOOSE to add, and may
// remove at any time. It is never an identity:
//
//   - No sign-in path reads it. Login/UserIDByEmail/UserIDForEmail all resolve
//     on users.email, and the column carries no unique constraint or index, so
//     an account cannot be reached FROM a recovery address at all.
//   - It cannot be an in-network address — that would just re-draw the circle.
//   - Changing or clearing it requires the account password (re-auth), so a
//     stolen session cannot silently redirect the account's recovery mail.
package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/vul-os/vulos-management/pkg/env"
)

var (
	// ErrRecoveryEmailInvalid is returned when the supplied address is not a
	// syntactically valid email address.
	ErrRecoveryEmailInvalid = errors.New("auth: invalid recovery address")

	// ErrRecoveryEmailInNetwork is returned when the supplied address is on the
	// deployment's own identity domain. A recovery destination inside the
	// network is worthless — it is the very mailbox the user is locked out of.
	ErrRecoveryEmailInNetwork = errors.New("auth: the recovery address must be outside the Vulos network")
)

// ExternalSender delivers a message to an address OUTSIDE the Vulos network.
// It is deliberately a different interface from InboxSender, whose contract is
// "<handle>@<domain>": a recovery copy is the only mail the CP sends off-network
// on behalf of an account, and it must be impossible to reach that code path by
// passing a handle to the in-network sender.
type ExternalSender interface {
	DeliverExternalMessage(ctx context.Context, toAddr, subject, body string) error
}

// SetExternalSender installs the sender used to copy recovery mail to an
// account's out-of-network recovery address. Call before serving traffic; nil
// (the default) means no copy is sent.
func (s *Store) SetExternalSender(sender ExternalSender) { s.externalSender = sender }

// SetRecoveryEmail sets the account's out-of-network recovery address.
//
// The caller MUST have re-authenticated the account holder (password) — the
// route layer does this. Returns ErrRecoveryEmailInNetwork for an address on
// the identity domain and ErrRecoveryEmailInvalid for a malformed one.
func (s *Store) SetRecoveryEmail(ctx context.Context, userID, addr string) error {
	addr = strings.ToLower(strings.TrimSpace(addr))
	if !validEmail(addr) {
		return ErrRecoveryEmailInvalid
	}
	if strings.HasSuffix(addr, "@"+strings.ToLower(env.Domain())) {
		return ErrRecoveryEmailInNetwork
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.db.ExecContext(ctx,
		s.db.Rebind(`UPDATE users SET recovery_email = ?, updated_at = ? WHERE id = ?`),
		addr, now().Format(time.RFC3339), userID,
	)
	if err != nil {
		return fmt.Errorf("auth: set recovery email: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ClearRecoveryEmail removes the account's recovery address. Declining the
// address is a first-class state: an account with no recovery address recovers
// through its recovery codes (ResetPasswordWithRecoveryCode) or the reviewed
// 14-day flow (internal/auth/recovery).
func (s *Store) ClearRecoveryEmail(ctx context.Context, userID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.db.ExecContext(ctx,
		s.db.Rebind(`UPDATE users SET recovery_email = NULL, updated_at = ? WHERE id = ?`),
		now().Format(time.RFC3339), userID,
	)
	if err != nil {
		return fmt.Errorf("auth: clear recovery email: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// NotifyRecoveryAddress copies a recovery-flow notice to the account's
// out-of-network recovery address, if it has one. Best-effort — a delivery
// failure is logged and swallowed, never surfaced to the caller (the flow that
// triggered the notice has already committed).
func (s *Store) NotifyRecoveryAddress(ctx context.Context, userID, subject, body string) {
	s.copyToRecoveryAddress(ctx, userID, subject, body)
}

// RecoveryEmail returns the account's recovery address, or "" when none is set.
// This is the ONLY read of users.recovery_email in the codebase, and it takes a
// userID — an account can never be resolved FROM a recovery address.
func (s *Store) RecoveryEmail(ctx context.Context, userID string) (string, error) {
	var addr sql.NullString
	err := s.db.QueryRowContext(ctx,
		s.db.Rebind(`SELECT recovery_email FROM users WHERE id = ?`), userID,
	).Scan(&addr)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("auth: recovery email: %w", err)
	}
	return addr.String, nil
}
