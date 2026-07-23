// lookup.go — convenience query helpers for cross-package seams.
package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// UserIDForEmail returns the account ID (users.id) for the given email
// address, or ("", nil) when no account exists (enumeration-safe: callers
// must not distinguish not-found from other errors for security-sensitive
// paths).
//
// This method is used by the security step-up middleware (via
// wire_security.go) to resolve the canonical user ID from an email address
// for per-account risk bucketing. It deliberately does not expose whether
// the account exists — the security package logs only the resolved ID, never
// the lookup result.
func (s *Store) UserIDForEmail(ctx context.Context, email string) (string, error) {
	var id string
	err := s.db.QueryRowContext(ctx,
		s.db.Rebind(`SELECT id FROM users WHERE email = ?`), email,
	).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil // enumeration-safe: unknown email → empty string
	}
	if err != nil {
		return "", fmt.Errorf("auth: user id for email: %w", err)
	}
	return id, nil
}

// EmailForUserID returns the canonical email for a user ULID, or ("", nil) when
// no such account exists. Used by the llmux key-resolve endpoint to map a
// resolved billing account_id (ULID) back to the account email it reports.
func (s *Store) EmailForUserID(ctx context.Context, userID string) (string, error) {
	var email string
	err := s.db.QueryRowContext(ctx,
		s.db.Rebind(`SELECT email FROM users WHERE id = ?`), userID,
	).Scan(&email)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("auth: email for user id: %w", err)
	}
	return email, nil
}

// ProfileByID returns the email + verification flag for a user ULID. It returns
// ErrNotFound when no such account exists so callers can fail closed rather than
// acting on a deleted user.
func (s *Store) ProfileByID(ctx context.Context, userID string) (email string, emailVerified bool, err error) {
	var verified int
	err = s.db.QueryRowContext(ctx,
		s.db.Rebind(`SELECT email, email_verified FROM users WHERE id = ?`), userID,
	).Scan(&email, &verified)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, ErrNotFound
	}
	if err != nil {
		return "", false, fmt.Errorf("auth: profile by id: %w", err)
	}
	return email, verified != 0, nil
}

// CountUsers returns the total number of user accounts.
// Used by the superadmin stats worker (AccountsReader).
func (s *Store) CountUsers(ctx context.Context) (int64, error) {
	var n int64
	err := s.db.QueryRowContext(ctx, s.db.Rebind(`SELECT COUNT(*) FROM users`)).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("auth: count users: %w", err)
	}
	return n, nil
}

// CountActiveUsersSince returns the number of users who had a session created
// on or after 'since'. Used as a proxy for "active users" in the stats worker.
func (s *Store) CountActiveUsersSince(ctx context.Context, since time.Time) (int64, error) {
	var n int64
	err := s.db.QueryRowContext(ctx,
		s.db.Rebind(`SELECT COUNT(DISTINCT user_id) FROM sessions WHERE created_at >= ?`),
		since.UTC().Format(time.RFC3339),
	).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("auth: count active users since: %w", err)
	}
	return n, nil
}

// CountSignupsSince returns the number of users who registered on or after 'since'.
// Used by the superadmin stats worker (AccountsReader).
func (s *Store) CountSignupsSince(ctx context.Context, since time.Time) (int64, error) {
	var n int64
	err := s.db.QueryRowContext(ctx,
		s.db.Rebind(`SELECT COUNT(*) FROM users WHERE created_at >= ?`),
		since.UTC().Format(time.RFC3339),
	).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("auth: count signups since: %w", err)
	}
	return n, nil
}
