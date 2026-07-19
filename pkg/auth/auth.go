// Package auth provides email+password login and session management for the
// vulos.cloud control-plane.
//
// Auth decision (LOCKED): email + password only. No Google OAuth, no SSO,
// no magic link. password_hash is always present (NOT NULL) -- there is no
// IdP path.
//
// Storage: pure-Go modernc.org/sqlite (never CGO). Single writer enforced via
// SetMaxOpenConns(1) + WAL mode. Migrations embedded via go:embed.
package auth

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"math/big"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/google/uuid"
	"github.com/vul-os/vulos-management/pkg/apptoken"
	"github.com/vul-os/vulos-management/pkg/cpdb"
)

// Sentinel errors mapped to HTTP status codes by the handler layer.
var (
	ErrEmailTaken       = errors.New("auth: email already registered")
	ErrNotFound         = errors.New("auth: user not found")
	ErrSessionNotFound  = errors.New("auth: session not found or expired")
	ErrResetExpired     = errors.New("auth: reset token expired or already used")
	ErrVerifyExpired    = errors.New("auth: verification token expired or already used")
	ErrAccountSuspended = errors.New("auth: account is suspended")
	// ErrPasswordSetupRequired is returned by LookupSession when the session
	// belongs to a social sign-up that still holds the LockedPasswordHash
	// sentinel (no usable password). MANDATORY-PASSWORD gate (LOCKED, 2026-07):
	// such an account is NOT usable on any session-gated surface until it runs
	// SetInitialPassword. RequireSession maps this to a 403 password_setup_required;
	// every other LookupSession caller treats it (like any error) as "no session"
	// and fails closed. The only paths that may proceed are the setup-tolerant ones
	// (GET /api/auth/me, POST /api/auth/password/set-initial) via
	// LookupSessionForSetup / RequireSessionAllowingSetup.
	ErrPasswordSetupRequired = errors.New("auth: password setup required")
)

// Preferred2FAModality is the per-account 2FA preference.
type Preferred2FAModality string

const (
	// ModalityPasswordTOTP is the classic password + TOTP flow (default).
	ModalityPasswordTOTP Preferred2FAModality = "password+totp"
	// ModalityPasswordPasskey is password-first, passkey as the 2FA step.
	ModalityPasswordPasskey Preferred2FAModality = "password+passkey"
	// ModalityPasskeyOnly is passkey-first signin (no password needed).
	ModalityPasskeyOnly Preferred2FAModality = "passkey"
)

// ValidModalities is the set of accepted values for preferred_2fa_modality.
var ValidModalities = map[Preferred2FAModality]bool{
	ModalityPasswordTOTP:    true,
	ModalityPasswordPasskey: true,
	ModalityPasskeyOnly:     true,
}

// LoginResult carries the outcome of a Login call.
type LoginResult struct {
	User                      *User
	Token                     string               // session token; empty when TOTPRequired=true, PasskeyRequired=true or EmailVerificationRequired=true
	TOTPRequired              bool                 // true when user has TOTP enabled; caller must complete /totp/verify
	PasskeyRequired           bool                 // true when account prefers passkey-first; caller must complete /webauthn/login/begin
	PasskeyAs2FA              bool                 // true when password succeeded but passkey is the 2FA step (password+passkey modality)
	EmailVerificationRequired bool                 // true when email is unverified and the gate is active; no full session issued
	Modality                  Preferred2FAModality // the effective modality used for this login
}

// User is the public representation of a user (no hash, no raw tokens).
type User struct {
	ID            string `json:"user_id"`
	Email         string `json:"email"`
	EmailVerified bool   `json:"email_verified"`
	FleetAdmin    bool   `json:"fleet_admin"`
	Suspended     bool   `json:"suspended"`
	CreatedAt     string `json:"created_at"`
	// HomeRegion is the tenant's home region (MULTI-REGION Phase 0). Default "eu".
	//
	// DEPRECATED (REGION-SSOT-01): users.home_region is NO LONGER the source of
	// truth — georoute.tenants_regions is. This denormalised copy is whitelisted
	// (kept for back-compat reads / the session region claim) but must not be
	// treated as authoritative; the region reconciler (internal/regionrecon)
	// reconciles it against the canonical georoute home region. Do not write new
	// placement logic against this field.
	HomeRegion string `json:"home_region"`
	// NeedsPasswordSetup is true when the account still holds the locked-password
	// sentinel — a social sign-up that has not yet run SetInitialPassword. It is
	// set by the setup-tolerant session lookup so the mandatory-password gate can
	// be reported (GET /api/auth/me) without granting a usable session. NOT
	// serialised: /me surfaces it via its own password_setup_required field.
	NeedsPasswordSetup bool `json:"-"`
}

// Store manages the auth database.
type Store struct {
	db     *cpdb.DB
	mu     sync.Mutex // serialises all writes (WAL + single connection)
	secret []byte     // HMAC/signing secret; derived from SESSION_SECRET env

	// appTokenIntrospector verifies CP-minted app-identity tokens (SECURITY-C1).
	// Injected once at startup; read-only thereafter.
	appTokenIntrospector AppTokenIntrospector

	// externalSender copies recovery mail to an account's out-of-network
	// recovery address (recovery_email.go). Injected once at startup; nil means
	// no copy is sent.
	externalSender ExternalSender
}

// replicaReadsEnabled reports whether login-critical READS should be served from
// the read-replica pool (IDENTITY-SERVICE §2.3/§2.4). Gated behind a flag so the
// seam is OFF by default and fully reversible: when unset (or when no replica is
// configured in cpdb) every read hits the primary exactly as before. Only the
// session-validation and login-lookup reads opt in; lockout/counter reads always
// stay on the primary.
//
// Even when enabled, negative session lookups CONFIRM against the primary before
// returning "invalid" (read-your-writes for a freshly-minted session), and a
// missing replica in cpdb makes the *Replica accessors fall back to primary — so
// enabling the flag without a replica URL is a safe no-op.
func replicaReadsEnabled() bool {
	v := os.Getenv("AUTH_REPLICA_READS")
	return v == "1" || v == "true"
}

// readRow runs a single-row login-critical read. When replica reads are enabled
// AND a replica pool is configured, it routes to the replica; otherwise it reads
// the primary. Use ONLY for reads that tolerate bounded replica lag per
// IDENTITY-SERVICE §2.3 (user lookup by email/id, suspension flag). It does NOT
// do confirm-on-miss — a missing row here is authoritative (an unknown email in
// Login, an absent user), and any real row already exists on the primary and
// replicates. For SESSION existence (read-your-writes), use readSessionRow.
//
// query is rebound for the active dialect internally; pass ?-placeholder SQL.
func (s *Store) readRow(ctx context.Context, query string, args ...any) *sql.Row {
	q := s.db.Rebind(query)
	if replicaReadsEnabled() && s.db.HasReplica() {
		return s.db.QueryRowReplica(ctx, q, args...)
	}
	return s.db.QueryRowContext(ctx, q, args...)
}

// readSessionRow runs a login-critical SESSION read with read-your-writes
// safety (IDENTITY-SERVICE §2.4). When replica reads are enabled and a replica
// exists, it reads the replica first; if that returns sql.ErrNoRows (which could
// be replica lag on a just-minted session rather than a truly-absent session) it
// CONFIRMS once against the PRIMARY before surfacing the miss. This guarantees a
// real session is never rejected because the replica has not caught up.
//
// SECURITY — revocation/suspension bypass (confirm-on-VALID): a stale replica is
// dangerous in BOTH directions. A replica MISS on a live session is only an
// availability problem (handled above). But a replica HIT that still shows the
// session as VALID (not revoked, not expired) is a SECURITY problem: a
// revocation / logout-all / admin-forced-revoke / account-suspension that has
// ALREADY committed to the primary may not have replicated yet, so trusting the
// replica's "valid" would honour a token the user/operator just killed. Therefore
// when the replica reports the session as valid, we RE-CONFIRM against the
// primary — the primary's verdict wins. A replica reporting revoked/expired is
// the fail-closed direction and is trusted as-is (no extra read). The confirm
// hits the primary at most once per lookup, only on the positive path.
//
// scan receives the *sql.Row to scan. valid, given the just-scanned row's
// error, reports whether the replica considers the session security-valid (the
// caller evaluates revoked/expired/partial after scan). It may be nil for reads
// where staleness is not security-sensitive (fall back to confirm-on-miss only).
func (s *Store) readSessionRow(ctx context.Context, query, token string, scan func(*sql.Row) error, valid func(scanErr error) bool) error {
	q := s.db.Rebind(query)
	if replicaReadsEnabled() && s.db.HasReplica() {
		err := scan(s.db.QueryRowReplica(ctx, q, token))
		if errors.Is(err, sql.ErrNoRows) {
			// Replica MISS: confirm against the primary (read-your-writes) so a
			// just-minted session is never rejected on replica lag.
			return scan(s.db.QueryRowContext(ctx, q, token))
		}
		if err != nil {
			return err // real error — surface it
		}
		// Replica HIT. If it looks security-valid, re-confirm against the primary
		// so a not-yet-replicated revocation/suspension/expiry is not masked.
		if valid != nil && valid(err) {
			return scan(s.db.QueryRowContext(ctx, q, token))
		}
		return err // replica says invalid (revoked/expired) — fail-closed, trust it
	}
	return scan(s.db.QueryRowContext(ctx, q, token))
}

// OpenAuthStore applies migrations to db and returns a ready Store.
// db should be obtained from cpdb.Open("auth") for production, or from
// cpdb.OpenSQLiteDSN(":memory:") for tests. The caller must call Close.
func OpenAuthStore(db *cpdb.DB, secret []byte) (*Store, error) {
	subFS, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("auth: embed sub: %w", err)
	}
	if err := db.Migrate(subFS); err != nil {
		return nil, fmt.Errorf("auth: migrate: %w", err)
	}
	return &Store{db: db, secret: secret}, nil
}

// Close closes the underlying database.
func (s *Store) Close() error { return s.db.Close() }

// Ping verifies the auth database is reachable. It is the readiness signal for
// the isolated idp service (IDENTITY-SERVICE §2.5): a machine that cannot reach
// the auth schema must fail-closed and remove itself from the login rotation
// rather than accept logins it cannot serve. Uses the PRIMARY handle — readiness
// means "can I do a login write", not just "can I read a replica".
func (s *Store) Ping(ctx context.Context) error {
	return s.db.DB.PingContext(ctx)
}

// Secret returns the HMAC secret (used by the route layer for session signing).
func (s *Store) Secret() []byte { return s.secret }

// DB returns the underlying *sql.DB.  Intended for superadmin / test helpers
// that need direct SQL access; production code should not use this.
func (s *Store) DB() *sql.DB { return s.db.DB }

// CPDB returns the underlying *cpdb.DB (dialect-aware handle) so callers that
// share the auth database (e.g. the superadmin store, whose tables FK into
// users) can use Rebind + dialect-split migrations on both backends.
func (s *Store) CPDB() *cpdb.DB { return s.db }

// --- ULID generation (URL-safe 26-char Crockford base32) ---

const crockfordAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// newULID returns a random 26-char Crockford base32 string suitable as a user ID.
// We use pure crypto/rand rather than a time-prefixed ULID; the IDs are opaque
// identifiers for the auth layer (no sortability requirement here).
func newULID() (string, error) {
	b := make([]byte, 26)
	for i := range b {
		n, err := rand.Int(rand.Reader, big.NewInt(32))
		if err != nil {
			return "", fmt.Errorf("auth: generate id: %w", err)
		}
		b[i] = crockfordAlphabet[n.Int64()]
	}
	return string(b), nil
}

// --- Email validation (simple, pragmatic) ---

func validEmail(e string) bool {
	at := strings.LastIndex(e, "@")
	if at < 1 {
		return false
	}
	domain := e[at+1:]
	if len(domain) < 3 || !strings.Contains(domain, ".") {
		return false
	}
	for _, ch := range e {
		if unicode.IsSpace(ch) {
			return false
		}
	}
	return true
}

// --- Password policy ---

// validatePasswordPolicy enforces the NIST SP 800-63B length-first policy.
// fleetAdmin=true requires 14+ characters; standard minimum is 12.
func validatePasswordPolicy(password string, fleetAdmin bool) error {
	minLen := 12
	if fleetAdmin {
		minLen = 14
	}
	// Count Unicode code points, not bytes, so passphrases with multi-byte chars work.
	runes := []rune(password)
	if len(runes) < minLen {
		return fmt.Errorf("auth: password must be at least %d characters", minLen)
	}
	if len(runes) > 1024 {
		return fmt.Errorf("auth: password must be no more than 1024 characters")
	}
	return nil
}

// --- Signup ---

// Signup creates a new user with an argon2id-hashed password, issues a
// session, and returns the User and session token.
// Returns ErrEmailTaken if the email is already registered.
func (s *Store) Signup(ctx context.Context, email, password string, ip, ua string) (*User, string, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if !validEmail(email) {
		return nil, "", fmt.Errorf("auth: invalid email address")
	}
	// NIST SP 800-63B length-first policy: 12 chars minimum.
	if err := validatePasswordPolicy(password, false); err != nil {
		return nil, "", err
	}

	hash, err := HashPassword(password)
	if err != nil {
		return nil, "", err
	}

	id, err := newULID()
	if err != nil {
		return nil, "", err
	}

	n := now()
	ts := n.Format(time.RFC3339)

	s.mu.Lock()
	_, err = s.db.ExecContext(ctx,
		s.db.Rebind(`INSERT INTO users (id, email, password_hash, email_verified, created_at, updated_at)
		 VALUES (?, ?, ?, 0, ?, ?)`),
		id, email, hash, ts, ts,
	)
	s.mu.Unlock()
	if err != nil {
		if cpdb.IsUniqueViolation(err) {
			return nil, "", ErrEmailTaken
		}
		return nil, "", fmt.Errorf("auth: insert user: %w", err)
	}

	u := &User{
		ID:        id,
		Email:     email,
		CreatedAt: ts,
	}

	// Generate and log email verification token (v1: log only, email send is out-of-scope).
	if verr := s.issueEmailVerificationToken(ctx, id, email, n); verr != nil {
		// Non-fatal: signup still completes; token will be generated on resend.
		log.Printf("[auth] email verification token generation failed for %s: %v", email, verr)
	}

	token, err := s.createSession(ctx, id, ip, ua)
	if err != nil {
		return nil, "", err
	}
	return u, token, nil
}

// --- Login ---

// Login verifies credentials and returns a LoginResult.
//
// Modality routing (AUTH-12):
//
//   - passkey modality: if the account's preferred_2fa_modality is "passkey" and
//     a passkey is registered, Login returns PasskeyRequired=true and no token.
//     The password field is still verified for accounts that have one (backward-compat
//     for account-takeover resistance). The caller must redirect to
//     POST /api/auth/webauthn/login/begin (no partial session needed for passkey-first).
//
//   - password+passkey modality: password is verified, then a partial session is
//     issued and PasskeyAs2FA=true is returned. Caller redirects to the WebAuthn 2FA
//     step (/api/auth/webauthn/login/begin-2fa).
//
//   - password+totp (default): existing TOTP flow unchanged.
//
// When the account's email is unverified and the gate is active,
// LoginResult.EmailVerificationRequired is true and no session token is issued.
// The gate can be bypassed by setting AUTH_ALLOW_UNVERIFIED_LOGIN=1.
// Returns ErrHashMismatch (from argon2.go) on bad password, ErrNotFound if no account.
func (s *Store) Login(ctx context.Context, email, password string, ip, ua string) (result *LoginResult, retErr error) {
	email = strings.ToLower(strings.TrimSpace(email))

	// Shared, DB-backed login-attempt cap (IDENTITY-SERVICE §2.4/§6). Enforced
	// BEFORE any credential material is examined so a throttled request reveals
	// nothing about the account. No-op unless AUTH_SHARED_LOGIN_LIMIT is set.
	if err := s.checkLoginThrottle(ctx, email, ip); err != nil {
		return nil, err
	}
	// Record the outcome for the sliding window. A login that stops at a 2FA/
	// passkey/email-verify STEP is NOT counted as a failure (the password proof
	// succeeded); only a hard credential failure counts against the cap. Success
	// (full or partial session issued, or a legitimate step) is recorded as ok=1.
	defer func() {
		switch {
		case retErr == nil:
			s.recordLoginAttempt(ctx, email, ip, true)
		case errors.Is(retErr, ErrHashMismatch), errors.Is(retErr, ErrNotFound):
			s.recordLoginAttempt(ctx, email, ip, false)
		default:
			// Suspension / internal errors are not attacker-controlled brute
			// force; do not count them toward the cap.
		}
	}()

	var (
		u                       User
		hash                    string
		emailVerified           int
		fleetAdmin              int
		suspended               int
		totpEnabled             int
		emailVerifiedGraceUntil sql.NullInt64
		modalityRaw             string
	)
	err := s.readRow(ctx,
		`SELECT id, email, password_hash, email_verified, fleet_admin,
		        COALESCE(suspended,0), totp_enabled, created_at,
		        email_verified_grace_until, preferred_2fa_modality
		 FROM users WHERE email = ?`, email,
	).Scan(&u.ID, &u.Email, &hash, &emailVerified, &fleetAdmin, &suspended, &totpEnabled, &u.CreatedAt,
		&emailVerifiedGraceUntil, &modalityRaw)
	if errors.Is(err, sql.ErrNoRows) {
		// Constant-time: run a dummy hash to avoid timing oracle.
		_ = VerifyPassword("$argon2id$v=19$m=65536,t=1,p=4$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", password)
		return nil, ErrHashMismatch
	}
	if err != nil {
		return nil, fmt.Errorf("auth: login query: %w", err)
	}

	u.EmailVerified = emailVerified != 0
	u.FleetAdmin = fleetAdmin != 0
	u.Suspended = suspended != 0

	// Suspended accounts must not be issued any new session (partial or full).
	// Password verification still runs first so timing is identical to a normal
	// failed login (no enumeration of suspension status).
	if u.Suspended {
		_ = VerifyPassword(hash, password) // constant-time burn
		return nil, ErrAccountSuspended
	}

	modality := Preferred2FAModality(modalityRaw)
	if !ValidModalities[modality] {
		modality = ModalityPasswordTOTP
	}

	// ── Passkey-first modality (AUTH-12) ──────────────────────────────────────
	// If the account prefers passkey-only login and has at least one registered
	// passkey, skip the password check entirely and signal the caller to drive
	// a WebAuthn ceremony instead.
	if modality == ModalityPasskeyOnly {
		hasPasskeys, pkErr := s.userHasPasskeys(ctx, u.ID)
		if pkErr == nil && hasPasskeys {
			// No password verification — passkey ceremony IS the authentication.
			return &LoginResult{
				User:            &u,
				Modality:        modality,
				PasskeyRequired: true,
			}, nil
		}
		// Fall back to password+totp when no passkeys registered yet.
		modality = ModalityPasswordTOTP
	}

	// A password-less account — a social sign-up that has not yet run
	// SetInitialPassword — holds the LockedPasswordHash sentinel, which is not a
	// valid argon2id string. No password can ever match it; treat a password login
	// as an ordinary credential failure (401) rather than leaking an "invalid hash
	// format" 500. The account is usable only after it sets a Vulos password.
	if hash == LockedPasswordHash {
		_ = VerifyPassword("$argon2id$v=19$m=65536,t=1,p=4$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", password)
		return nil, ErrHashMismatch
	}

	// For all password-based modalities, verify the password now.
	if err := VerifyPassword(hash, password); err != nil {
		return nil, err
	}

	// ── Email-verification gate (AUTH-10) ─────────────────────────────────────
	// Gate runs AFTER password OK but BEFORE any 2FA step.
	// Skip when: email is already verified, env override is set, or the account
	// is still within its legacy grace window.
	if !u.EmailVerified {
		allowUnverified := os.Getenv("AUTH_ALLOW_UNVERIFIED_LOGIN") == "1"
		inGrace := emailVerifiedGraceUntil.Valid && now().Unix() < emailVerifiedGraceUntil.Int64

		if !allowUnverified && !inGrace {
			// Return without issuing any session token.
			return &LoginResult{User: &u, EmailVerificationRequired: true}, nil
		}
	}

	// ── Password + Passkey modality (AUTH-12) ─────────────────────────────────
	// Password succeeded. Now check if the account uses passkey as the 2FA step.
	if modality == ModalityPasswordPasskey {
		hasPasskeys, pkErr := s.userHasPasskeys(ctx, u.ID)
		if pkErr == nil && hasPasskeys {
			partial, err := s.CreatePartialSession(ctx, u.ID, ip, ua)
			if err != nil {
				return nil, err
			}
			return &LoginResult{
				User:         &u,
				Token:        partial,
				PasskeyAs2FA: true,
				Modality:     modality,
			}, nil
		}
		// No passkeys registered: fall through to TOTP or full session.
		modality = ModalityPasswordTOTP
	}

	// ── TOTP 2FA (default / password+totp modality) ───────────────────────────
	if totpEnabled != 0 {
		// Issue a short-lived partial session; caller must complete TOTP step.
		partial, err := s.CreatePartialSession(ctx, u.ID, ip, ua)
		if err != nil {
			return nil, err
		}
		return &LoginResult{User: &u, Token: partial, TOTPRequired: true, Modality: modality}, nil
	}

	token, err := s.createSession(ctx, u.ID, ip, ua)
	if err != nil {
		return nil, err
	}
	return &LoginResult{User: &u, Token: token, TOTPRequired: false, Modality: modality}, nil
}

// UserIDByEmail resolves an email to its account id, or ErrNotFound. Used by the
// OPAQUE login/start route to map the submitted email to the record's stable
// credential identifier (the user id). Does not read any secret material.
func (s *Store) UserIDByEmail(ctx context.Context, email string) (string, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	var id string
	err := s.db.QueryRowContext(ctx, s.db.Rebind(`SELECT id FROM users WHERE email = ?`), email).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("auth: user id by email: %w", err)
	}
	return id, nil
}

// IssuePostAuthSession runs the post-credential-verification tail of login for an
// account whose PASSWORD proof already succeeded by an alternate means (today:
// the OPAQUE handshake, IDENTITY-SERVICE §3.5 step 4). It reuses the SAME
// email-verification gate + 2FA modality branches + session issuance as Login, so
// TOTP / passkey-as-2FA / partial sessions / suspension all still apply — OPAQUE
// changes only HOW the password is proven, never what a successful proof grants.
//
// It does NOT verify any password itself; the caller must have already proven the
// credential (e.g. via OpaqueService.OpaqueLoginFinish). userID must be the
// account the proof authenticated.
func (s *Store) IssuePostAuthSession(ctx context.Context, userID, ip, ua string) (*LoginResult, error) {
	var (
		u                       User
		emailVerified           int
		fleetAdmin              int
		suspended               int
		totpEnabled             int
		emailVerifiedGraceUntil sql.NullInt64
		modalityRaw             string
	)
	err := s.db.QueryRowContext(ctx,
		s.db.Rebind(`SELECT id, email, email_verified, fleet_admin, COALESCE(suspended,0),
		        totp_enabled, created_at, email_verified_grace_until, preferred_2fa_modality
		 FROM users WHERE id = ?`), userID,
	).Scan(&u.ID, &u.Email, &emailVerified, &fleetAdmin, &suspended, &totpEnabled, &u.CreatedAt,
		&emailVerifiedGraceUntil, &modalityRaw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("auth: post-auth session query: %w", err)
	}
	u.EmailVerified = emailVerified != 0
	u.FleetAdmin = fleetAdmin != 0
	u.Suspended = suspended != 0

	if u.Suspended {
		return nil, ErrAccountSuspended
	}

	modality := Preferred2FAModality(modalityRaw)
	if !ValidModalities[modality] {
		modality = ModalityPasswordTOTP
	}

	// Email-verification gate (AUTH-10) — identical to Login.
	if !u.EmailVerified {
		allowUnverified := os.Getenv("AUTH_ALLOW_UNVERIFIED_LOGIN") == "1"
		inGrace := emailVerifiedGraceUntil.Valid && now().Unix() < emailVerifiedGraceUntil.Int64
		if !allowUnverified && !inGrace {
			return &LoginResult{User: &u, EmailVerificationRequired: true}, nil
		}
	}

	// Passkey-as-2FA (AUTH-12).
	if modality == ModalityPasswordPasskey {
		hasPasskeys, pkErr := s.userHasPasskeys(ctx, u.ID)
		if pkErr == nil && hasPasskeys {
			partial, err := s.CreatePartialSession(ctx, u.ID, ip, ua)
			if err != nil {
				return nil, err
			}
			return &LoginResult{User: &u, Token: partial, PasskeyAs2FA: true, Modality: modality}, nil
		}
		modality = ModalityPasswordTOTP
	}

	// TOTP 2FA.
	if totpEnabled != 0 {
		partial, err := s.CreatePartialSession(ctx, u.ID, ip, ua)
		if err != nil {
			return nil, err
		}
		return &LoginResult{User: &u, Token: partial, TOTPRequired: true, Modality: modality}, nil
	}

	token, err := s.createSession(ctx, u.ID, ip, ua)
	if err != nil {
		return nil, err
	}
	return &LoginResult{User: &u, Token: token, TOTPRequired: false, Modality: modality}, nil
}

// userHasPasskeys returns true if the user has at least one registered WebAuthn credential.
func (s *Store) userHasPasskeys(ctx context.Context, userID string) (bool, error) {
	var count int
	err := s.db.QueryRowContext(ctx,
		s.db.Rebind(`SELECT COUNT(*) FROM webauthn_credentials WHERE user_id = ?`), userID,
	).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("auth: check passkeys: %w", err)
	}
	return count > 0, nil
}

// GetPreferred2FAModality returns the preferred 2FA modality for a user.
// Returns ModalityPasswordTOTP for unknown users or unrecognised values.
func (s *Store) GetPreferred2FAModality(ctx context.Context, userID string) (Preferred2FAModality, error) {
	var raw string
	err := s.db.QueryRowContext(ctx,
		s.db.Rebind(`SELECT preferred_2fa_modality FROM users WHERE id = ?`), userID,
	).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return ModalityPasswordTOTP, nil
	}
	if err != nil {
		return ModalityPasswordTOTP, fmt.Errorf("auth: get modality: %w", err)
	}
	m := Preferred2FAModality(raw)
	if !ValidModalities[m] {
		return ModalityPasswordTOTP, nil
	}
	return m, nil
}

// SetPreferred2FAModality updates the preferred 2FA modality for a user.
// Returns an error for unrecognised modality values.
func (s *Store) SetPreferred2FAModality(ctx context.Context, userID string, modality Preferred2FAModality) error {
	if !ValidModalities[modality] {
		return fmt.Errorf("auth: unknown 2FA modality: %q", modality)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx,
		s.db.Rebind(`UPDATE users SET preferred_2fa_modality = ?, updated_at = ? WHERE id = ?`),
		string(modality), now().Format(time.RFC3339), userID,
	)
	if err != nil {
		return fmt.Errorf("auth: set modality: %w", err)
	}
	return nil
}

// SetEmailVerifiedGraceUntil sets (or clears) the email_verified_grace_until
// timestamp for a user. Pass 0 to clear it (NULL in DB). Intended for use
// during data migrations that need to give legacy accounts a grace window.
func (s *Store) SetEmailVerifiedGraceUntil(ctx context.Context, userID string, graceUntilUnix int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var err error
	if graceUntilUnix == 0 {
		_, err = s.db.ExecContext(ctx,
			s.db.Rebind(`UPDATE users SET email_verified_grace_until = NULL WHERE id = ?`), userID,
		)
	} else {
		_, err = s.db.ExecContext(ctx,
			s.db.Rebind(`UPDATE users SET email_verified_grace_until = ? WHERE id = ?`), graceUntilUnix, userID,
		)
	}
	if err != nil {
		return fmt.Errorf("auth: set email_verified_grace_until: %w", err)
	}
	return nil
}

// --- Session management ---

func (s *Store) createSession(ctx context.Context, userID, ip, ua string) (string, error) {
	token, err := newSessionToken()
	if err != nil {
		return "", err
	}
	n := now()
	expires := n.Add(SessionDuration)
	ts := n.Format(time.RFC3339)

	s.mu.Lock()
	_, err = s.db.ExecContext(ctx,
		s.db.Rebind(`INSERT INTO sessions (id, user_id, created_at, last_seen_at, expires_at, ip, user_agent, revoked)
		 VALUES (?, ?, ?, ?, ?, ?, ?, 0)`),
		token, userID, ts, ts, expires.Format(time.RFC3339), ip, ua,
	)
	s.mu.Unlock()
	if err != nil {
		return "", fmt.Errorf("auth: create session: %w", err)
	}
	return token, nil
}

// LookupSession validates a session token and returns the associated User.
// Returns ErrSessionNotFound if absent, expired, revoked, or invalid, and
// ErrPasswordSetupRequired for a live session whose account still holds the
// LockedPasswordHash sentinel (a social sign-up that has not set a password) —
// the MANDATORY-PASSWORD gate. Setup-tolerant callers use LookupSessionForSetup.
func (s *Store) LookupSession(ctx context.Context, token string) (*User, error) {
	return s.lookupSession(ctx, token, false)
}

// LookupSessionForSetup is LookupSession that DOES resolve a password-less social
// account (it sets User.NeedsPasswordSetup instead of failing). It is ONLY for the
// two surfaces that must reach such an account to make it usable: GET /api/auth/me
// (to report password_setup_required) and POST /api/auth/password/set-initial (to
// set the first password). Every other caller MUST use LookupSession so a
// password-less account stays unusable everywhere else (fail closed).
func (s *Store) LookupSessionForSetup(ctx context.Context, token string) (*User, error) {
	return s.lookupSession(ctx, token, true)
}

// lookupSession is the shared session-resolution impl. When allowSetup is false a
// live session for a password-less account fails with ErrPasswordSetupRequired.
func (s *Store) lookupSession(ctx context.Context, token string, allowSetup bool) (*User, error) {
	if token == "" {
		return nil, ErrSessionNotFound
	}
	// SECURITY-C1: an app-identity token is NOT a session. It would miss the
	// sessions table anyway, but say so explicitly — the credential-class
	// boundary is load-bearing and must not rest on a lookup accident.
	if apptoken.Looks(token) {
		return nil, ErrSessionNotFound
	}

	var (
		userID    string
		expiresAt string
		revoked   int
		partial   int
	)
	scanSession := func(row *sql.Row) error {
		return row.Scan(&userID, &expiresAt, &revoked, &partial)
	}
	// sessionValid reports whether the just-scanned session row is security-valid
	// (present, not revoked, not partial, not expired). readSessionRow uses this to
	// re-confirm a replica "valid" verdict against the primary so a not-yet-
	// replicated revocation/expiry can never be masked by a stale replica.
	sessionValid := func(scanErr error) bool {
		if scanErr != nil || revoked != 0 || partial != 0 {
			return false
		}
		exp, perr := time.Parse(time.RFC3339, expiresAt)
		return perr == nil && !now().After(exp)
	}
	const sessionSelect = `SELECT user_id, expires_at, revoked, partial FROM sessions WHERE id = ?`
	err := s.readSessionRow(ctx, sessionSelect, token, scanSession, sessionValid)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrSessionNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("auth: lookup session: %w", err)
	}

	// Reject partial sessions — callers must use LookupPartialSession instead.
	if revoked != 0 || partial != 0 {
		return nil, ErrSessionNotFound
	}

	exp, err := time.Parse(time.RFC3339, expiresAt)
	if err != nil || now().After(exp) {
		return nil, ErrSessionNotFound
	}

	var u User
	var emailVerified int
	var fleetAdmin int
	var suspended int
	var passwordHash string
	// PRIMARY-only: this row carries the suspension gate, enforced on EVERY
	// request. A stale replica could mask a just-committed account suspension and
	// keep honouring the session, so the suspension read must not tolerate lag.
	// password_hash rides along (no extra query) to enforce the mandatory-password
	// gate below.
	err = s.db.QueryRowContext(ctx,
		s.db.Rebind(`SELECT id, email, email_verified, fleet_admin, COALESCE(suspended,0), created_at,
		        COALESCE(home_region,'eu'), password_hash
		 FROM users WHERE id = ?`), userID,
	).Scan(&u.ID, &u.Email, &emailVerified, &fleetAdmin, &suspended, &u.CreatedAt, &u.HomeRegion, &passwordHash)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrSessionNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("auth: lookup user for session: %w", err)
	}

	u.EmailVerified = emailVerified != 0
	u.FleetAdmin = fleetAdmin != 0
	u.Suspended = suspended != 0
	u.NeedsPasswordSetup = passwordHash == LockedPasswordHash

	// Reject sessions belonging to suspended accounts. Suspension takes effect
	// immediately on the next request — existing sessions are not force-deleted so
	// the audit trail is preserved, but access is denied here.
	if u.Suspended {
		return nil, ErrAccountSuspended
	}

	// MANDATORY-PASSWORD gate (LOCKED, 2026-07). A social sign-up holds the
	// LockedPasswordHash sentinel until POST /api/auth/password/set-initial runs.
	// Such a session is NOT usable on any session-gated surface: fail closed here
	// so RequireSession, the app-token minter (wire_apptoken), the OAuth-provider
	// consent screen and every other LookupSession caller all deny it. Only the
	// setup-tolerant path (allowSetup, via LookupSessionForSetup) may proceed, and
	// then only to report the gate / set the first password.
	if u.NeedsPasswordSetup && !allowSetup {
		return nil, ErrPasswordSetupRequired
	}

	// Touch last_seen_at (fire-and-forget; not critical).
	s.mu.Lock()
	_, _ = s.db.ExecContext(ctx,
		s.db.Rebind(`UPDATE sessions SET last_seen_at = ? WHERE id = ?`),
		now().Format(time.RFC3339), token,
	)
	s.mu.Unlock()

	return &u, nil
}

// AppTokenIntrospector verifies a CP-minted app-identity token and resolves it
// to the user it authenticates. It is injected by the composition root
// (cmd/server/wire_apptoken.go) so this package needs no knowledge of where the
// signing key lives or how it rotates. Nil in builds with no app proxy — in
// which case app tokens simply never resolve (fail-closed).
//
// expectedAud (SECURITY-C1 / F4 defence-in-depth) is the audience of the app
// REQUESTING the introspection. When non-empty the verifier MUST reject a token
// whose `aud` is a different app — so app B can never introspect (and act on) a
// token minted for app A. An empty expectedAud preserves the historical
// audience-agnostic behaviour (the internal session-gate path, which cannot know
// a requester).
type AppTokenIntrospector func(ctx context.Context, token, expectedAud string) (userID string, expiresAt time.Time, ok bool)

// SetAppTokenIntrospector wires the app-token verifier. Call once at startup,
// before serving.
func (s *Store) SetAppTokenIntrospector(fn AppTokenIntrospector) { s.appTokenIntrospector = fn }

// introspectAppToken resolves a verified app-identity token to its owner,
// applying the SAME account-suspension gate a session introspection applies.
// expectedAud binds the token to the REQUESTING app (F4): a non-empty value
// rejects a token minted for any other app before it can resolve to a user.
func (s *Store) introspectAppToken(ctx context.Context, token, expectedAud string) Introspection {
	if s.appTokenIntrospector == nil {
		return Introspection{}
	}
	userID, expiresAt, ok := s.appTokenIntrospector(ctx, token, expectedAud)
	if !ok || userID == "" {
		return Introspection{}
	}
	// A token outlives a suspension otherwise: re-check the owner, PRIMARY-only,
	// exactly as the session path does.
	var suspended int
	if err := s.db.QueryRowContext(ctx,
		s.db.Rebind(`SELECT COALESCE(suspended,0) FROM users WHERE id = ?`), userID,
	).Scan(&suspended); err != nil || suspended != 0 {
		return Introspection{}
	}
	return Introspection{
		Valid:     true,
		UserID:    userID,
		TenantID:  userID, // tenant == account in this control plane
		ExpiresAt: expiresAt.UTC().Format(time.RFC3339),
	}
}

// Introspection is the result of a fail-closed session introspection: the
// caller learns whether a session token is currently valid and, if so, the
// account it authenticates and when it expires. It carries NO secret material.
type Introspection struct {
	Valid     bool   // true only for a live, non-partial, non-revoked, non-expired, non-suspended session
	UserID    string // the account id the session authenticates (empty when !Valid)
	TenantID  string // the tenant id; in this control plane tenant == account, so TenantID == UserID
	ExpiresAt string // RFC3339 UTC session expiry (empty when !Valid)
}

// IntrospectSession validates a raw session token and reports whether it is
// currently usable, returning the owning account and expiry. This is the CP
// half of the SSO design: the CP is the identity provider in cloud, and a
// service (e.g. Office) that holds the CP shared secret can introspect a
// user's session without holding any signing power.
//
// FAIL-CLOSED, NO ORACLE: any invalid/expired/revoked/partial/suspended/unknown
// session resolves to {Valid:false} with empty fields and a nil error — the
// caller (and thus the untrusted product) learns nothing beyond "not valid".
// A session is only ever resolved to its own owner; there is no cross-account
// path. It never touches or returns secret material.
func (s *Store) IntrospectSession(ctx context.Context, token string) Introspection {
	return s.IntrospectSessionForAudience(ctx, token, "")
}

// IntrospectSessionForAudience is IntrospectSession with an F4 defence-in-depth
// binding: expectedAud is the audience of the app REQUESTING the introspection
// (identified by its own credential/host at the route layer). When non-empty and
// the presented credential is an app-identity token, the token's `aud` MUST equal
// expectedAud or the result is a fail-closed {Valid:false} — so a token minted for
// app A can never be resolved (and acted on) by app B.
//
// The `vc_session` path is deliberately UNAFFECTED: a real session is not an app
// token (apptoken.Looks == false), so expectedAud is never consulted for it. An
// empty expectedAud reproduces the historical audience-agnostic behaviour used by
// the internal session gate, which cannot know a requester.
func (s *Store) IntrospectSessionForAudience(ctx context.Context, token, expectedAud string) Introspection {
	if token == "" {
		return Introspection{}
	}

	// SECURITY-C1: app-identity tokens introspect too. The reverse proxy hands
	// Office one of these in place of the user's session, and introspection
	// is exactly how they learn who the request is for — so resolving it here is
	// what lets those apps keep working while holding a credential that is
	// useless against the session-gated CP surface (see RequireSession).
	//
	// Same fail-closed contract as a session: any bad/expired/forged token, a
	// suspended owner, an audience mismatch (F4), or an unwired verifier resolves
	// to {Valid:false}.
	if apptoken.Looks(token) {
		return s.introspectAppToken(ctx, token, expectedAud)
	}

	var (
		userID    string
		expiresAt string
		revoked   int
		partial   int
	)
	scanSession := func(row *sql.Row) error {
		return row.Scan(&userID, &expiresAt, &revoked, &partial)
	}
	// Re-confirm a replica "valid" verdict against the primary (see LookupSession)
	// so a just-committed revocation/expiry can't be masked by a stale replica.
	sessionValid := func(scanErr error) bool {
		if scanErr != nil || revoked != 0 || partial != 0 {
			return false
		}
		exp, perr := time.Parse(time.RFC3339, expiresAt)
		return perr == nil && !now().After(exp)
	}
	err := s.readSessionRow(ctx,
		`SELECT user_id, expires_at, revoked, partial FROM sessions WHERE id = ?`, token, scanSession, sessionValid)
	if err != nil {
		// sql.ErrNoRows OR any error → no oracle, just "not valid".
		return Introspection{}
	}
	// Partial (pre-2FA) and revoked sessions are not valid full sessions.
	if revoked != 0 || partial != 0 {
		return Introspection{}
	}
	exp, perr := time.Parse(time.RFC3339, expiresAt)
	if perr != nil || now().After(exp) {
		return Introspection{}
	}

	// Reject sessions whose owning account is suspended (access denied
	// immediately on the next request, same as LookupSession). PRIMARY-only: the
	// suspension gate must not tolerate replica lag (see LookupSession).
	//
	// MANDATORY-PASSWORD gate: also reject a session whose account still holds the
	// LockedPasswordHash sentinel (a social sign-up that has not set a password).
	// This is the apps' session→user path (Office POST /api/session/
	// introspect); a password-less account must be unusable there too, matching
	// LookupSession's fail-closed verdict. password_hash rides the same query.
	var suspended int
	var passwordHash string
	if err := s.db.QueryRowContext(ctx,
		s.db.Rebind(`SELECT COALESCE(suspended,0), password_hash FROM users WHERE id = ?`), userID,
	).Scan(&suspended, &passwordHash); err != nil || suspended != 0 || passwordHash == LockedPasswordHash {
		return Introspection{}
	}

	return Introspection{
		Valid:     true,
		UserID:    userID,
		TenantID:  userID, // tenant == account in this control plane
		ExpiresAt: exp.UTC().Format(time.RFC3339),
	}
}

// SessionCreatedAt returns the created_at time of a live, full (non-partial),
// non-revoked, non-expired session identified by token. It is used by the
// profile login-broker (UNIFIED-SIGNIN) to enforce a session-freshness window
// when a no-2FA account mints an OS login token: only a session created by a
// RECENT password login qualifies.
//
// PRIMARY-only read: freshness is a security gate, so it must not be satisfied
// by a stale replica row. Returns ErrSessionNotFound for absent / revoked /
// partial / expired sessions — callers fail closed.
func (s *Store) SessionCreatedAt(ctx context.Context, token string) (time.Time, error) {
	if token == "" {
		return time.Time{}, ErrSessionNotFound
	}
	var (
		createdAt string
		expiresAt string
		revoked   int
		partial   int
	)
	err := s.db.QueryRowContext(ctx,
		s.db.Rebind(`SELECT created_at, expires_at, revoked, partial FROM sessions WHERE id = ?`), token,
	).Scan(&createdAt, &expiresAt, &revoked, &partial)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, ErrSessionNotFound
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("auth: session created_at: %w", err)
	}
	if revoked != 0 || partial != 0 {
		return time.Time{}, ErrSessionNotFound
	}
	exp, perr := time.Parse(time.RFC3339, expiresAt)
	if perr != nil || now().After(exp) {
		return time.Time{}, ErrSessionNotFound
	}
	created, cerr := time.Parse(time.RFC3339, createdAt)
	if cerr != nil {
		return time.Time{}, fmt.Errorf("auth: parse session created_at: %w", cerr)
	}
	return created, nil
}

// DeleteSession removes a session by token (logout).
func (s *Store) DeleteSession(ctx context.Context, token string) error {
	s.mu.Lock()
	_, err := s.db.ExecContext(ctx, s.db.Rebind(`DELETE FROM sessions WHERE id = ?`), token)
	s.mu.Unlock()
	return err
}

// RevokeSession marks a session as revoked (soft delete, kept for audit).
func (s *Store) RevokeSession(ctx context.Context, token string) error {
	s.mu.Lock()
	_, err := s.db.ExecContext(ctx,
		s.db.Rebind(`UPDATE sessions SET revoked = 1 WHERE id = ?`), token,
	)
	s.mu.Unlock()
	return err
}

// DeleteAllUserSessions removes all sessions for a user (e.g. after password reset).
func (s *Store) DeleteAllUserSessions(ctx context.Context, userID string) error {
	s.mu.Lock()
	_, err := s.db.ExecContext(ctx, s.db.Rebind(`DELETE FROM sessions WHERE user_id = ?`), userID)
	s.mu.Unlock()
	return err
}

// LastActiveAt returns the most recent session activity for the user — the
// freshest sessions.last_seen_at across all of the user's session rows.
//
// last_seen_at is touched on every successful LookupSession (see that method),
// so it is the truest "last active" signal the auth layer holds. We consider
// ALL of the user's session rows regardless of revoked/expired state: a
// revoked or expired row's last_seen_at still records the last time the user
// was active on that session, which is exactly what callers want to display.
//
// Note on pruning: logout DELETEs the session row (DeleteSession /
// DeleteAllUserSessions), so if a user has logged out of every session there is
// no row left and this returns found=false. Soft-revoked rows (RevokeSession /
// RevokeByID / RevokeAll) are retained and still counted. When found is false
// the caller (org-admin Members adapter) falls back to the membership join
// date.
//
// This is a read-only reader: it issues no writes and does not touch the
// existing login/session lifecycle.
func (s *Store) LastActiveAt(ctx context.Context, userID string) (time.Time, bool, error) {
	if userID == "" {
		return time.Time{}, false, nil
	}
	var maxSeen sql.NullString
	err := s.db.QueryRowContext(ctx,
		s.db.Rebind(`SELECT MAX(last_seen_at) FROM sessions WHERE user_id = ?`), userID,
	).Scan(&maxSeen)
	if errors.Is(err, sql.ErrNoRows) {
		// MAX() over zero rows yields one NULL row, so this branch is rarely
		// hit; handled for completeness.
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, fmt.Errorf("auth: last active: %w", err)
	}
	if !maxSeen.Valid || maxSeen.String == "" {
		// No session rows for this user (e.g. all logged out / pruned).
		return time.Time{}, false, nil
	}
	t, err := time.Parse(time.RFC3339, maxSeen.String)
	if err != nil {
		// Tolerate RFC3339Nano-formatted timestamps too (defensive).
		t, err = time.Parse(time.RFC3339Nano, maxSeen.String)
		if err != nil {
			return time.Time{}, false, fmt.Errorf("auth: last active parse: %w", err)
		}
	}
	return t.UTC(), true, nil
}

// MemberInfo is the per-account display info the org-admin Members tab needs:
// the user's email and their most-recent session activity (LastActiveOK is
// false when the user has no surviving session row, mirroring LastActiveAt).
type MemberInfo struct {
	Email        string
	LastActive   time.Time
	LastActiveOK bool
}

// EmailsAndLastActive resolves email + most-recent session activity for a BATCH
// of account ids in TWO queries total — one `WHERE id IN (...)` over users and
// one grouped `MAX(last_seen_at) ... GROUP BY user_id` over sessions — instead
// of the previous N+1 (one email query + one MAX query PER member). On a
// single-writer SQLite store the round-trip count dominates ListMembers latency,
// so collapsing 2N queries to 2 is the fix.
//
// The returned map is keyed by account id and only contains ids that resolved
// to a users row; an id with no users row is simply absent (the caller leaves
// Email empty, exactly as the per-member lookup returned "" for an unknown id).
// last_seen_at is read across ALL of a user's session rows (revoked/expired
// included) to match LastActiveAt's semantics.
func (s *Store) EmailsAndLastActive(ctx context.Context, ids []string) (map[string]MemberInfo, error) {
	out := make(map[string]MemberInfo, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	// De-dup ids and drop empties so the IN-list is minimal and placeholder
	// counts match the args exactly.
	seen := make(map[string]struct{}, len(ids))
	uniq := make([]string, 0, len(ids))
	for _, id := range ids {
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		uniq = append(uniq, id)
	}
	if len(uniq) == 0 {
		return out, nil
	}

	placeholders, args := inPlaceholders(uniq)

	// Query 1: emails.
	emailRows, err := s.db.QueryContext(ctx,
		s.db.Rebind(`SELECT id, email FROM users WHERE id IN (`+placeholders+`)`), args...)
	if err != nil {
		return nil, fmt.Errorf("auth: batch emails: %w", err)
	}
	func() {
		defer emailRows.Close()
		for emailRows.Next() {
			var id, email string
			if scanErr := emailRows.Scan(&id, &email); scanErr != nil {
				err = fmt.Errorf("auth: batch emails scan: %w", scanErr)
				return
			}
			out[id] = MemberInfo{Email: email}
		}
		if rerr := emailRows.Err(); rerr != nil && err == nil {
			err = fmt.Errorf("auth: batch emails rows: %w", rerr)
		}
	}()
	if err != nil {
		return nil, err
	}

	// Query 2: most-recent session activity, grouped.
	actRows, err := s.db.QueryContext(ctx,
		s.db.Rebind(`SELECT user_id, MAX(last_seen_at) FROM sessions WHERE user_id IN (`+placeholders+`) GROUP BY user_id`), args...)
	if err != nil {
		return nil, fmt.Errorf("auth: batch last-active: %w", err)
	}
	defer actRows.Close()
	for actRows.Next() {
		var id string
		var maxSeen sql.NullString
		if scanErr := actRows.Scan(&id, &maxSeen); scanErr != nil {
			return nil, fmt.Errorf("auth: batch last-active scan: %w", scanErr)
		}
		if !maxSeen.Valid || maxSeen.String == "" {
			continue
		}
		t, perr := time.Parse(time.RFC3339, maxSeen.String)
		if perr != nil {
			if t, perr = time.Parse(time.RFC3339Nano, maxSeen.String); perr != nil {
				continue // tolerate a single malformed timestamp; don't fail the batch
			}
		}
		mi := out[id] // zero MemberInfo if the user had no users row (session-only)
		mi.LastActive = t.UTC()
		mi.LastActiveOK = true
		out[id] = mi
	}
	if rerr := actRows.Err(); rerr != nil {
		return nil, fmt.Errorf("auth: batch last-active rows: %w", rerr)
	}
	return out, nil
}

// inPlaceholders builds an "?, ?, ?" placeholder list and the matching []any
// args slice for an IN-clause.
func inPlaceholders(ids []string) (string, []any) {
	var b strings.Builder
	args := make([]any, len(ids))
	for i, id := range ids {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteByte('?')
		args[i] = id
	}
	return b.String(), args
}

// --- Password reset ---

const resetTokenLen = 32
const resetDuration = 15 * time.Minute

// RequestPasswordReset generates a reset token for the given email (if found)
// and logs it. Always returns nil to prevent email enumeration.
func (s *Store) RequestPasswordReset(ctx context.Context, email string) error {
	email = strings.ToLower(strings.TrimSpace(email))

	var userID string
	err := s.db.QueryRowContext(ctx,
		s.db.Rebind(`SELECT id FROM users WHERE email = ?`), email,
	).Scan(&userID)
	if errors.Is(err, sql.ErrNoRows) {
		// Email enumeration safe: return nil even for unknown emails.
		return nil
	}
	if err != nil {
		return fmt.Errorf("auth: reset request query: %w", err)
	}

	tokenBytes := make([]byte, resetTokenLen)
	if _, err := rand.Read(tokenBytes); err != nil {
		return fmt.Errorf("auth: generate reset token: %w", err)
	}
	// Use hex so it's URL-safe.
	token := fmt.Sprintf("%x", tokenBytes)

	n := now()
	expires := n.Add(resetDuration)

	s.mu.Lock()
	_, err = s.db.ExecContext(ctx,
		s.db.Rebind(`INSERT INTO password_resets (token, user_id, created_at, expires_at, used)
		 VALUES (?, ?, ?, ?, 0)`),
		token, userID,
		n.Format(time.RFC3339),
		expires.Format(time.RFC3339),
	)
	s.mu.Unlock()
	if err != nil {
		return fmt.Errorf("auth: insert reset token: %w", err)
	}

	// Do NOT log the token value — it is a one-time bearer credential equivalent
	// to a password. Only log that a reset was requested so operators can audit
	// reset rates and detect abuse. The token is delivered via the mail-out
	// pipeline that consumes the password_resets row.
	log.Printf("[auth] password reset requested for user_id=%s email=%s (expires %s)", userID, email, expires.Format(time.RFC3339))
	return nil
}

// ConfirmPasswordReset validates the reset token, updates the password hash,
// and invalidates all existing sessions for the user.
// Returns ErrResetExpired if the token is expired, used, or missing.
func (s *Store) ConfirmPasswordReset(ctx context.Context, token, newPassword string) error {
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
		return fmt.Errorf("auth: reset confirm query: %w", err)
	}
	if used != 0 {
		return ErrResetExpired
	}
	exp, err := time.Parse(time.RFC3339, expiresAt)
	if err != nil || now().After(exp) {
		return ErrResetExpired
	}

	// Enforce password policy: fleet_admin accounts require >=14 chars.
	if err := validatePasswordPolicy(newPassword, s.IsFleetAdmin(ctx, userID)); err != nil {
		return err
	}

	hash, err := HashPassword(newPassword)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	_, err = s.db.ExecContext(ctx,
		s.db.Rebind(`UPDATE users SET password_hash = ?, updated_at = ? WHERE id = ?`),
		hash, now().Format(time.RFC3339), userID,
	)
	if err != nil {
		return fmt.Errorf("auth: update password: %w", err)
	}

	_, err = s.db.ExecContext(ctx,
		s.db.Rebind(`UPDATE password_resets SET used = 1 WHERE token = ?`), token,
	)
	if err != nil {
		return fmt.Errorf("auth: mark reset used: %w", err)
	}

	_, err = s.db.ExecContext(ctx,
		s.db.Rebind(`DELETE FROM sessions WHERE user_id = ?`), userID,
	)
	if err != nil {
		return fmt.Errorf("auth: invalidate sessions: %w", err)
	}

	return nil
}

// --- Password hash query (for TOTP disable) ---

// QueryPasswordHash fetches the argon2id password hash for the given userID.
// Used by handlers that need to re-verify the user's password (e.g. /totp/disable).
func (s *Store) QueryPasswordHash(ctx context.Context, userID string, hash *string) error {
	err := s.db.QueryRowContext(ctx,
		s.db.Rebind(`SELECT password_hash FROM users WHERE id = ?`), userID,
	).Scan(hash)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// --- Session registry ---

// SessionInfo is the public representation of a single session (no token
// value returned — the ID is the opaque row ID, not the secret token).
type SessionInfo struct {
	ID         string `json:"id"` // opaque row identifier (= the token)
	IP         string `json:"ip"`
	UserAgent  string `json:"user_agent"`
	CreatedAt  string `json:"created_at"`
	LastSeenAt string `json:"last_seen_at"`
	IsCurrent  bool   `json:"is_current"`
}

// ListSessions returns all non-expired, non-revoked, non-partial sessions for
// userID. The caller provides currentToken so IsCurrent can be set correctly.
func (s *Store) ListSessions(ctx context.Context, userID, currentToken string) ([]SessionInfo, error) {
	rows, err := s.db.QueryContext(ctx,
		s.db.Rebind(`SELECT id, ip, user_agent, created_at, last_seen_at
		 FROM sessions
		 WHERE user_id = ?
		   AND revoked = 0
		   AND partial = 0
		   AND expires_at > ?
		 ORDER BY last_seen_at DESC`),
		userID, now().Format(time.RFC3339),
	)
	if err != nil {
		return nil, fmt.Errorf("auth: list sessions: %w", err)
	}
	defer rows.Close()

	var sessions []SessionInfo
	for rows.Next() {
		var si SessionInfo
		var ip, ua sql.NullString
		if err := rows.Scan(&si.ID, &ip, &ua, &si.CreatedAt, &si.LastSeenAt); err != nil {
			return nil, fmt.Errorf("auth: scan session: %w", err)
		}
		si.IP = ip.String
		si.UserAgent = ua.String
		si.IsCurrent = si.ID == currentToken
		sessions = append(sessions, si)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("auth: list sessions rows: %w", err)
	}
	if sessions == nil {
		sessions = []SessionInfo{}
	}
	return sessions, nil
}

// RevokeByID marks the session with the given id as revoked, scoped to userID
// (so one user cannot revoke another user's session).
func (s *Store) RevokeByID(ctx context.Context, userID, sessionID string) error {
	s.mu.Lock()
	res, err := s.db.ExecContext(ctx,
		s.db.Rebind(`UPDATE sessions SET revoked = 1 WHERE id = ? AND user_id = ?`),
		sessionID, userID,
	)
	s.mu.Unlock()
	if err != nil {
		return fmt.Errorf("auth: revoke session by id: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("auth: revoke session by id rows affected: %w", err)
	}
	if n == 0 {
		return ErrSessionNotFound
	}
	return nil
}

// RevokeAll revokes all non-partial sessions for userID except the one
// identified by exceptToken (the caller's current session). Returns the count
// of sessions that were revoked.
func (s *Store) RevokeAll(ctx context.Context, userID, exceptToken string) (int, error) {
	s.mu.Lock()
	res, err := s.db.ExecContext(ctx,
		s.db.Rebind(`UPDATE sessions
		 SET revoked = 1
		 WHERE user_id = ?
		   AND id != ?
		   AND partial = 0
		   AND revoked = 0`),
		userID, exceptToken,
	)
	s.mu.Unlock()
	if err != nil {
		return 0, fmt.Errorf("auth: revoke all sessions: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("auth: revoke all sessions rows affected: %w", err)
	}
	return int(n), nil
}

// --- Email verification ---

const emailVerifyDuration = 24 * time.Hour

// issueEmailVerificationToken creates a new UUID token for the given user,
// stores it, and logs it to stderr. n is the "now" timestamp.
func (s *Store) issueEmailVerificationToken(ctx context.Context, userID, email string, n time.Time) error {
	token := uuid.New().String()
	expires := n.Add(emailVerifyDuration)

	s.mu.Lock()
	_, err := s.db.ExecContext(ctx,
		s.db.Rebind(`INSERT INTO email_verification_tokens (token, user_id, created_at, expires_at, used)
		 VALUES (?, ?, ?, ?, 0)`),
		token, userID,
		n.Format(time.RFC3339),
		expires.Format(time.RFC3339),
	)
	s.mu.Unlock()
	if err != nil {
		return fmt.Errorf("auth: insert email verification token: %w", err)
	}

	// SECURITY (audit M5): never log the verification token in plaintext — a log
	// reader could verify another user's email. Log only that one was issued.
	log.Printf("[auth] email verification token issued for %s (expires %s)", email, expires.Format(time.RFC3339))
	return nil
}

// ConsumeEmailVerificationToken validates the token, marks it used, and sets
// email_verified=1 on the users row. Returns ErrVerifyExpired for unknown,
// expired, or already-used tokens.
func (s *Store) ConsumeEmailVerificationToken(ctx context.Context, token string) error {
	var (
		userID    string
		expiresAt string
		used      int
	)
	err := s.db.QueryRowContext(ctx,
		s.db.Rebind(`SELECT user_id, expires_at, used FROM email_verification_tokens WHERE token = ?`), token,
	).Scan(&userID, &expiresAt, &used)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrVerifyExpired
	}
	if err != nil {
		return fmt.Errorf("auth: verify token query: %w", err)
	}
	if used != 0 {
		return ErrVerifyExpired
	}
	exp, err := time.Parse(time.RFC3339, expiresAt)
	if err != nil || now().After(exp) {
		return ErrVerifyExpired
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	_, err = s.db.ExecContext(ctx,
		s.db.Rebind(`UPDATE email_verification_tokens SET used = 1 WHERE token = ?`), token,
	)
	if err != nil {
		return fmt.Errorf("auth: mark verify token used: %w", err)
	}

	_, err = s.db.ExecContext(ctx,
		s.db.Rebind(`UPDATE users SET email_verified = 1, updated_at = ? WHERE id = ?`),
		now().Format(time.RFC3339), userID,
	)
	if err != nil {
		return fmt.Errorf("auth: set email_verified: %w", err)
	}

	return nil
}

// ResendEmailVerification issues a fresh verification token for the user
// identified by email. If the email is unknown or already verified it returns
// nil (no enumeration). Rate-limiting is handled at the HTTP layer.
func (s *Store) ResendEmailVerification(ctx context.Context, email string) error {
	email = strings.ToLower(strings.TrimSpace(email))

	var (
		userID   string
		verified int
	)
	err := s.db.QueryRowContext(ctx,
		s.db.Rebind(`SELECT id, email_verified FROM users WHERE email = ?`), email,
	).Scan(&userID, &verified)
	if errors.Is(err, sql.ErrNoRows) {
		// Enumeration-safe: return nil for unknown emails.
		return nil
	}
	if err != nil {
		return fmt.Errorf("auth: resend verify query: %w", err)
	}
	if verified != 0 {
		// Already verified: no-op (no enumeration).
		return nil
	}

	n := now()
	if verr := s.issueEmailVerificationToken(ctx, userID, email, n); verr != nil {
		return verr
	}
	return nil
}

// --- RequireSession helper ---

// RequireSession is a helper for handlers that need a valid full session.
// It reads the session cookie, looks up the session, and returns the user.
// Partial sessions (post-password, pre-TOTP) are rejected with 401.
// Suspended accounts are rejected with 403.
// On failure it writes a 401/403 JSON error and returns nil.
func (s *Store) RequireSession(ctx context.Context, w http.ResponseWriter, r *http.Request) *User {
	return s.requireSession(ctx, w, r, false)
}

// RequireSessionAllowingSetup is RequireSession that PERMITS a password-less
// social account (User.NeedsPasswordSetup == true) through the gate. It exists
// ONLY for the two surfaces that must reach such an account to make it usable:
// GET /api/auth/me (report password_setup_required) and POST
// /api/auth/password/set-initial (set the first password). Do NOT use it
// anywhere else — every other route uses RequireSession so a password-less
// account stays unusable (MANDATORY-PASSWORD gate, fail closed).
func (s *Store) RequireSessionAllowingSetup(ctx context.Context, w http.ResponseWriter, r *http.Request) *User {
	return s.requireSession(ctx, w, r, true)
}

// requireSession is the shared session gate. allowSetup governs whether a
// password-less social account is admitted (see RequireSessionAllowingSetup).
func (s *Store) requireSession(ctx context.Context, w http.ResponseWriter, r *http.Request, allowSetup bool) *User {
	token := SessionFromRequest(r)
	if token == "" {
		http.Error(w, `{"error":"not authenticated"}`, http.StatusUnauthorized)
		return nil
	}
	// SECURITY-C1: THE credential-class boundary. An app-identity token
	// (minted by the reverse proxy for a lower-trust app backend, see
	// cmd/server/wire_apptoken.go) is not a session and can never act as one —
	// no matter that it arrives in the vc_session cookie. This is what stops a
	// compromised Office/Board backend from replaying what it was given
	// against the session-gated CP surface.
	if apptoken.Looks(token) {
		http.Error(w, `{"error":"app_token_is_not_a_session"}`, http.StatusForbidden)
		return nil
	}
	// Reject partial sessions explicitly before looking up the full user.
	if s.IsPartialSession(ctx, token) {
		http.Error(w, `{"error":"totp_required"}`, http.StatusUnauthorized)
		return nil
	}
	var u *User
	var err error
	if allowSetup {
		u, err = s.LookupSessionForSetup(ctx, token)
	} else {
		u, err = s.LookupSession(ctx, token)
	}
	if errors.Is(err, ErrAccountSuspended) {
		http.Error(w, `{"error":"account_suspended"}`, http.StatusForbidden)
		return nil
	}
	// MANDATORY-PASSWORD gate: a social sign-up that has not set a Vulos password
	// is refused with a distinct, non-enumerating 403 so the SPA can route to
	// /onboarding/set-password. Only reachable when allowSetup is false.
	if errors.Is(err, ErrPasswordSetupRequired) {
		http.Error(w, `{"error":"password_setup_required"}`, http.StatusForbidden)
		return nil
	}
	if err != nil {
		http.Error(w, `{"error":"not authenticated"}`, http.StatusUnauthorized)
		return nil
	}
	return u
}

// --- Fleet-admin TOTP enforcement ---

// IsTOTPEnabled returns true if the user identified by userID has totp_enabled=1.
func (s *Store) IsTOTPEnabled(ctx context.Context, userID string) bool {
	var te int
	err := s.db.QueryRowContext(ctx, s.db.Rebind(`SELECT totp_enabled FROM users WHERE id = ?`), userID).Scan(&te)
	if err != nil {
		return false
	}
	return te != 0
}

// IsSuspendedByID reports whether the account (users.id) is super-admin
// hard-suspended (users.suspended=1). This is the ADMIN suspension flag,
// distinct from billing/dunning (non-payment) suspension. An admin
// hard-suspend does NOT touch the subscription tier, so quota surfaces that
// resolve entitlements through billing.EffectiveTierFor must consult this to
// be authoritative — see billing.Store.SetAdminSuspendChecker.
//
// A missing user row (or any query error) reports false: fail OPEN so a
// transient auth-DB problem never denies a legitimate, non-suspended account.
// The caller may log the error separately if it cares.
func (s *Store) IsSuspendedByID(ctx context.Context, accountID string) (bool, error) {
	if accountID == "" {
		return false, nil
	}
	var suspended int
	err := s.db.QueryRowContext(ctx,
		s.db.Rebind(`SELECT COALESCE(suspended, 0) FROM users WHERE id = ?`), accountID,
	).Scan(&suspended)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return suspended != 0, nil
}

// LookupByEmail resolves a registered email to its account id. It is used by the
// cloud-home directory (account-only document sharing, Contract 2) to map a
// share recipient's email to an account before resolving/provisioning that
// account's cloud-home VulaID. Returns ("", "", nil) when no account owns the
// email (the caller treats this as "not found" / 404). The second return value
// is a best-effort display name; the users table has no separate display field,
// so the caller falls back to the email.
func (s *Store) LookupByEmail(ctx context.Context, email string) (accountID, displayName string, err error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return "", "", nil
	}
	var (
		id        string
		suspended int
	)
	err = s.db.QueryRowContext(ctx,
		s.db.Rebind(`SELECT id, COALESCE(suspended, 0) FROM users WHERE email = ?`), email,
	).Scan(&id, &suspended)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", nil
	}
	if err != nil {
		return "", "", fmt.Errorf("auth: lookup by email: %w", err)
	}
	// Suspended accounts are not discoverable share recipients.
	if suspended != 0 {
		return "", "", nil
	}
	// No dedicated display name column; the directory falls back to email.
	return id, "", nil
}

// RequireFleetAuth returns an http.Handler middleware that enforces mandatory
// 2FA for fleet_admin accounts. A fleet_admin must have either:
//   - TOTP enabled (classic mode), or
//   - at least one registered passkey AND preferred_2fa_modality set to
//     "passkey" or "password+passkey" (AUTH-12 passkey-as-2FA mode).
//
// If neither condition is met the middleware responds 403
// {"error":"totp_required",...} and does not call the next handler.
// For non-fleet-admin users the middleware passes through unconditionally.
//
// Usage: wrap fleet and managed route handlers with this middleware.
func (s *Store) RequireFleetAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u := s.RequireSession(r.Context(), w, r)
		if u == nil {
			// RequireSession already wrote the error response.
			return
		}
		if u.FleetAdmin {
			if !s.fleetAdminHas2FA(r.Context(), u.ID) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusForbidden)
				fmt.Fprint(w, `{"error":"totp_required","message":"Fleet admins must enable 2FA"}`)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// FleetAdminHas2FA returns true when the user satisfies the fleet-admin 2FA
// requirement via any accepted modality:
//   - TOTP enabled, or
//   - passkey modality ("passkey" or "password+passkey") with a registered passkey.
//
// Exported so that other packages (e.g. routes_profile.go) can gate
// high-privilege actions behind the same 2FA check used for fleet-admin.
func (s *Store) FleetAdminHas2FA(ctx context.Context, userID string) bool {
	return s.fleetAdminHas2FA(ctx, userID)
}

// fleetAdminHas2FA is the internal implementation; see FleetAdminHas2FA.
func (s *Store) fleetAdminHas2FA(ctx context.Context, userID string) bool {
	if s.IsTOTPEnabled(ctx, userID) {
		return true
	}
	modality, err := s.GetPreferred2FAModality(ctx, userID)
	if err != nil {
		return false
	}
	if modality == ModalityPasskeyOnly || modality == ModalityPasswordPasskey {
		has, err := s.userHasPasskeys(ctx, userID)
		if err == nil && has {
			return true
		}
	}
	return false
}
