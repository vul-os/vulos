// mailauth.go — service-to-service credential helpers for sibling products
// (vulos-office and other backends that authenticate an account by email +
// password over X-Relay-Auth).
//
// The cp `users` table is the single source of account identity: an account is
// the email (or OAuth identity) the user registered with — there is no
// separate mail-handle identity. These helpers let sibling cloud adapters
// (calling cp with X-Relay-Auth) verify an account's email + password and
// existence WITHOUT going through the interactive Login() flow (no TOTP /
// passkey / email-verification gates — a service client only has an email +
// password).
//
// Security notes:
//   - VerifyMailCredentials runs a constant-time dummy hash on a missing
//     account so timing does not reveal whether the account exists (the caller
//     must also return a generic 401 either way).
//   - A super-admin-suspended account (users.suspended=1) fails verification.
package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// dummyArgon2Hash is a syntactically-valid argon2id PHC string used to burn
// CPU on a missing account so VerifyMailCredentials is timing-constant whether
// or not the account exists. Same constant the interactive Login() path uses.
const dummyArgon2Hash = "$argon2id$v=19$m=65536,t=1,p=4$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

// MailIdentity is the resolved cp identity behind a mail address.
type MailIdentity struct {
	UserID    string // cp user ULID (account_id used by the billing store)
	Email     string // canonical lowercased email
	Suspended bool   // super-admin suspension (users.suspended)
}

// EmailExists reports whether a cp user with the given email exists. The email
// is lowercased/trimmed before lookup so callers need not normalise. Safe for
// concurrent use.
func (s *Store) EmailExists(ctx context.Context, email string) (bool, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return false, nil
	}
	var one int
	err := s.db.QueryRowContext(ctx,
		s.db.Rebind(`SELECT 1 FROM users WHERE email = ? LIMIT 1`), email,
	).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("auth: email exists: %w", err)
	}
	return true, nil
}

// LookupMailIdentity returns the cp identity for an email, or ErrNotFound if no
// such user exists. Used by entitlement resolution to map account_id=<email>
// onto the billing store's account_id=<user ULID>. Safe for concurrent use.
func (s *Store) LookupMailIdentity(ctx context.Context, email string) (MailIdentity, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return MailIdentity{}, ErrNotFound
	}
	var (
		id        string
		canonical string
		suspended int
	)
	err := s.db.QueryRowContext(ctx,
		s.db.Rebind(`SELECT id, email, COALESCE(suspended, 0) FROM users WHERE email = ?`), email,
	).Scan(&id, &canonical, &suspended)
	if errors.Is(err, sql.ErrNoRows) {
		return MailIdentity{}, ErrNotFound
	}
	if err != nil {
		return MailIdentity{}, fmt.Errorf("auth: lookup mail identity: %w", err)
	}
	return MailIdentity{UserID: id, Email: canonical, Suspended: suspended != 0}, nil
}

// VerifyMailCredentials verifies a mailbox login (username=email, password)
// against the cp users table using argon2id. On success it returns the resolved
// MailIdentity. It is timing-constant: a missing account still burns a hash so
// an attacker cannot distinguish "no such account" from "wrong password" by
// timing. Callers MUST return a generic 401 for any non-nil error.
//
// A super-admin-suspended account fails (returns ErrAccountSuspended) — but,
// like Login(), the password hash is still verified first so the failure mode
// is timing-indistinguishable from a normal bad-password rejection.
//
// Safe for concurrent use (read-only query + pure hash compute).
func (s *Store) VerifyMailCredentials(ctx context.Context, email, password string) (MailIdentity, error) {
	email = strings.ToLower(strings.TrimSpace(email))

	var (
		id        string
		canonical string
		hash      string
		suspended int
	)
	err := s.db.QueryRowContext(ctx,
		s.db.Rebind(`SELECT id, email, password_hash, COALESCE(suspended, 0) FROM users WHERE email = ?`), email,
	).Scan(&id, &canonical, &hash, &suspended)
	if errors.Is(err, sql.ErrNoRows) {
		// Constant-time: burn a dummy hash so the missing-account path costs the
		// same as a real verification.
		_ = VerifyPassword(dummyArgon2Hash, password)
		return MailIdentity{}, ErrNotFound
	}
	if err != nil {
		return MailIdentity{}, fmt.Errorf("auth: verify mail credentials: %w", err)
	}

	if vErr := VerifyPassword(hash, password); vErr != nil {
		return MailIdentity{}, vErr // ErrHashMismatch
	}

	if suspended != 0 {
		return MailIdentity{}, ErrAccountSuspended
	}

	return MailIdentity{UserID: id, Email: canonical, Suspended: false}, nil
}
