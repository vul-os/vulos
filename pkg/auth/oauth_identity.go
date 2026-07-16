package auth

// oauth_identity.go — linked social-identity store methods (Vulos as OAuth CLIENT).
//
// Account model (LOCKED, 2026-07): email+password is the CENTRE of every account
// and is MANDATORY. Social login (Google / Microsoft / …) is a convenience login
// layered on top; it is recorded as a LINK from an external IdP subject to a Vulos
// account in the oauth_identities table (migration 0002). None of this weakens the
// password rule:
//
//   - A social SIGN-UP creates a normal users row whose password_hash is the
//     LockedPasswordHash sentinel (see oauth_sunset.go) — an account with NO usable
//     password. Such an account cannot log in with a password (VerifyPassword always
//     rejects the sentinel) and must run SetInitialPassword before it is usable. The
//     route layer will not finalise provisioning until a real password exists.
//
//   - A social LOGIN to an EXISTING native account is only permitted once the link
//     has been proven (safe linking): the collision path in the route layer forces
//     the user to authenticate with the existing password before LinkOAuthIdentity
//     is ever called. This prevents an attacker who controls a matching IdP email
//     from silently taking over a pre-existing password account.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/vul-os/vulos-management/pkg/cpdb"
)

// Sentinel errors for the social-identity layer.
var (
	// ErrOAuthIdentityLinked is returned when (provider, subject) is already linked
	// to a DIFFERENT Vulos account than the one requested.
	ErrOAuthIdentityLinked = errors.New("auth: oauth identity already linked to another account")
	// ErrPasswordAlreadySet is returned by SetInitialPassword when the account
	// already holds a real (non-sentinel) password — the initial-password gate is
	// for password-less social sign-ups only, never a password change.
	ErrPasswordAlreadySet = errors.New("auth: account already has a password")
)

// FindOAuthIdentity resolves a linked (provider, subject) to its Vulos account id.
// Returns ErrNotFound when no link exists. provider is lower-cased by the caller.
func (s *Store) FindOAuthIdentity(ctx context.Context, provider, subject string) (userID string, err error) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	err = s.db.QueryRowContext(ctx,
		s.db.Rebind(`SELECT user_id FROM oauth_identities WHERE provider = ? AND subject = ?`),
		provider, subject,
	).Scan(&userID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("auth: find oauth identity: %w", err)
	}
	return userID, nil
}

// LinkOAuthIdentity binds (provider, subject) to userID. It is idempotent when the
// same (provider, subject) is re-linked to the SAME user; it returns
// ErrOAuthIdentityLinked if the pair is already bound to a different account (never
// silently re-point a subject at a new owner).
//
// SECURITY: callers MUST only call this after establishing that the caller is
// entitled to bind this identity — either a brand-new account they just created
// for this subject, or an existing account whose password the caller has proven.
func (s *Store) LinkOAuthIdentity(ctx context.Context, provider, subject, userID, email string, emailVerified bool) error {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "" || subject == "" || userID == "" {
		return fmt.Errorf("auth: link oauth identity: provider, subject and user_id are required")
	}

	// Fail closed on an existing binding to a different account.
	existing, err := s.FindOAuthIdentity(ctx, provider, subject)
	if err == nil {
		if existing != userID {
			return ErrOAuthIdentityLinked
		}
		return nil // already linked to the same account — idempotent no-op
	}
	if !errors.Is(err, ErrNotFound) {
		return err
	}

	ev := 0
	if emailVerified {
		ev = 1
	}
	ts := now().Format(time.RFC3339)
	s.mu.Lock()
	_, err = s.db.ExecContext(ctx,
		s.db.Rebind(`INSERT INTO oauth_identities (provider, subject, user_id, email, email_verified, created_at)
		             VALUES (?, ?, ?, ?, ?, ?)`),
		provider, subject, userID, strings.ToLower(strings.TrimSpace(email)), ev, ts,
	)
	s.mu.Unlock()
	if err != nil {
		if cpdb.IsUniqueViolation(err) {
			// Raced with a concurrent link on the same (provider, subject).
			return ErrOAuthIdentityLinked
		}
		return fmt.Errorf("auth: link oauth identity: %w", err)
	}
	return nil
}

// CreateOAuthUser creates a NEW account for a social sign-up. The account has NO
// usable password: password_hash is the LockedPasswordHash sentinel, so a password
// login is impossible until SetInitialPassword runs. email is the verified provider
// email and becomes the account's login identifier; emailVerified seeds
// users.email_verified. Returns ErrEmailTaken if the email already exists (the
// caller must then route to the safe-linking / proof flow instead).
func (s *Store) CreateOAuthUser(ctx context.Context, email string, emailVerified bool) (userID string, err error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if !validEmail(email) {
		return "", fmt.Errorf("auth: invalid email address")
	}
	id, err := newULID()
	if err != nil {
		return "", err
	}
	ev := 0
	if emailVerified {
		ev = 1
	}
	ts := now().Format(time.RFC3339)
	s.mu.Lock()
	_, err = s.db.ExecContext(ctx,
		s.db.Rebind(`INSERT INTO users (id, email, password_hash, email_verified, created_at, updated_at)
		             VALUES (?, ?, ?, ?, ?, ?)`),
		id, email, LockedPasswordHash, ev, ts, ts,
	)
	s.mu.Unlock()
	if err != nil {
		if cpdb.IsUniqueViolation(err) {
			return "", ErrEmailTaken
		}
		return "", fmt.Errorf("auth: create oauth user: %w", err)
	}
	return id, nil
}

// HasPassword reports whether the account holds a usable password credential — i.e.
// its password_hash is NOT the LockedPasswordHash sentinel. A false result means the
// account is a social sign-up that has not yet run SetInitialPassword and is NOT yet
// usable (the mandatory-password rule). Returns ErrNotFound for an unknown account.
func (s *Store) HasPassword(ctx context.Context, userID string) (bool, error) {
	var hash string
	err := s.db.QueryRowContext(ctx,
		s.db.Rebind(`SELECT password_hash FROM users WHERE id = ?`), userID,
	).Scan(&hash)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrNotFound
	}
	if err != nil {
		return false, fmt.Errorf("auth: has password: %w", err)
	}
	return hash != LockedPasswordHash, nil
}

// SetInitialPassword sets the FIRST real password on a password-less social account
// (one holding the LockedPasswordHash sentinel). It enforces the standard password
// policy and is the gate that makes a social account usable. It fails closed:
//
//   - ErrNotFound          — no such account
//   - ErrPasswordAlreadySet — the account already has a real password (use the
//     normal change/reset flow instead; this endpoint must never overwrite one)
//
// It intentionally does NOT verify an old password: the account never had one. The
// argon2 hash is computed here; the plaintext is never stored.
func (s *Store) SetInitialPassword(ctx context.Context, userID, newPassword string) error {
	var hash string
	var fleetAdmin int
	err := s.db.QueryRowContext(ctx,
		s.db.Rebind(`SELECT password_hash, fleet_admin FROM users WHERE id = ?`), userID,
	).Scan(&hash, &fleetAdmin)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("auth: set initial password: %w", err)
	}
	if hash != LockedPasswordHash {
		return ErrPasswordAlreadySet
	}
	if err := validatePasswordPolicy(newPassword, fleetAdmin != 0); err != nil {
		return err
	}
	newHash, err := HashPassword(newPassword)
	if err != nil {
		return err
	}
	ts := now().Format(time.RFC3339)
	s.mu.Lock()
	_, err = s.db.ExecContext(ctx,
		s.db.Rebind(`UPDATE users SET password_hash = ?, updated_at = ? WHERE id = ? AND password_hash = ?`),
		newHash, ts, userID, LockedPasswordHash,
	)
	s.mu.Unlock()
	if err != nil {
		return fmt.Errorf("auth: set initial password: %w", err)
	}
	return nil
}

// IssueOAuthSignupSession mints a FULL session for a brand-new social sign-up so the
// caller can drive the mandatory set-password step. It deliberately bypasses the
// email-verification and 2FA gates because (a) the IdP just authenticated the user
// and (b) the account is fresh: it has no TOTP/passkey enrolled, no data, and no
// provisioning yet — the ONLY thing this session can meaningfully do is
// SetInitialPassword (the route layer reports password_setup_required and defers all
// provisioning until then). For a social login to a PRE-EXISTING account, use
// IssuePostAuthSession instead so that account's 2FA still applies.
func (s *Store) IssueOAuthSignupSession(ctx context.Context, userID, ip, ua string) (string, error) {
	return s.createSession(ctx, userID, ip, ua)
}
