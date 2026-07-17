// Package superadmin implements the super-admin portal backend for
// vulos.cloud/cp.
//
// Design constraints:
//   - Pure-Go SQLite via modernc.org/sqlite (never CGO).
//   - Separate admin session cookie (vulos_admin_session), 30-min idle.
//   - WebAuthn hardware-key login on top of main session + TOTP.
//   - IP allowlist enforced in prod; open in dev.
//   - Every admin action recorded via internal/auditlog.
package superadmin

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	_ "modernc.org/sqlite"

	"github.com/vul-os/vulos-management/pkg/cpdb"
	"github.com/vul-os/vulos-management/pkg/env"
)

// ─────────────────────────────────────────────────────────────────────────────
// Sentinel errors
// ─────────────────────────────────────────────────────────────────────────────

var (
	ErrNotSuperAdmin  = errors.New("superadmin: account is not a super-admin")
	ErrSessionExpired = errors.New("superadmin: admin session expired or not found")
	ErrIPDenied       = errors.New("superadmin: IP not in allowlist")
)

// ─────────────────────────────────────────────────────────────────────────────
// Store
// ─────────────────────────────────────────────────────────────────────────────

// Store holds the SQLite connection for the super-admin data.
// It shares the auth.db database (same *sql.DB passed in) so FK references to
// the users table work correctly.
type Store struct {
	db *cpdb.DB
	mu sync.Mutex
}

//go:embed migrations
var migrationsFS embed.FS

// New opens (or reuses) the database at db, applies the superadmin migration,
// and returns a ready Store. db must already have foreign_keys=ON and WAL mode.
func New(db *cpdb.DB) (*Store, error) {
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		return nil, fmt.Errorf("superadmin: migrate: %w", err)
	}
	return s, nil
}

func (s *Store) migrate() error {
	sub, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		return fmt.Errorf("superadmin: embed sub: %w", err)
	}
	return s.db.Migrate(sub)
}

// DB returns the underlying *sql.DB (for test helpers).
func (s *Store) DB() *sql.DB { return s.db.DB }

// ─────────────────────────────────────────────────────────────────────────────
// Bootstrap
// ─────────────────────────────────────────────────────────────────────────────

// Bootstrap promotes the account whose email matches VULOS_BOOTSTRAP_SUPERADMIN
// if (and only if) no superadmin rows currently exist. Idempotent on subsequent
// runs: when any active superadmin exists the env var is silently ignored.
func (s *Store) Bootstrap(ctx context.Context) error {
	email := os.Getenv("VULOS_BOOTSTRAP_SUPERADMIN")
	if email == "" {
		return nil
	}
	email = strings.ToLower(strings.TrimSpace(email))

	// Check whether any active superadmin already exists.
	var count int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM superadmins WHERE revoked_at IS NULL`,
	).Scan(&count); err != nil {
		return fmt.Errorf("superadmin: bootstrap count: %w", err)
	}
	if count > 0 {
		// Already bootstrapped; ignore env var.
		return nil
	}

	// Look up account by email.
	var accountID string
	err := s.db.QueryRowContext(ctx,
		s.db.Rebind(`SELECT id FROM users WHERE email = ?`), email,
	).Scan(&accountID)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("superadmin: bootstrap: no account found for email %q", email)
	}
	if err != nil {
		return fmt.Errorf("superadmin: bootstrap lookup: %w", err)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	s.mu.Lock()
	_, err = s.db.ExecContext(ctx,
		s.db.Rebind(`INSERT INTO superadmins (account_id, promoted_at, promoted_by, revoked_at)
		 VALUES (?, ?, NULL, NULL) ON CONFLICT (account_id) DO NOTHING`),
		accountID, now,
	)
	s.mu.Unlock()
	if err != nil {
		return fmt.Errorf("superadmin: bootstrap insert: %w", err)
	}

	log.Printf("[superadmin] BOOTSTRAP: promoted %q (id=%s) as first super-admin", email, accountID)
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// IsSuperAdmin
// ─────────────────────────────────────────────────────────────────────────────

// IsSuperAdmin returns true when accountID has an active (not revoked) row in
// the superadmins table.
func (s *Store) IsSuperAdmin(ctx context.Context, accountID string) (bool, error) {
	var count int
	err := s.db.QueryRowContext(ctx,
		s.db.Rebind(`SELECT COUNT(*) FROM superadmins
		 WHERE account_id = ? AND revoked_at IS NULL`), accountID,
	).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("superadmin: is_super_admin: %w", err)
	}
	return count > 0, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Admin sessions
// ─────────────────────────────────────────────────────────────────────────────

const (
	AdminSessionCookieName = "vulos_admin_session"
	// AdminSessionIdleTimeout is the sliding idle window: a session expires
	// this long after the last request.
	AdminSessionIdleTimeout = 15 * time.Minute
	// AdminSessionAbsoluteTTL caps the total lifetime of an admin session
	// regardless of activity. After this long since creation the operator must
	// re-complete the full main + TOTP + WebAuthn login.
	AdminSessionAbsoluteTTL = 4 * time.Hour
)

// CreateAdminSession mints a new admin session token, stores its SHA-256 hash,
// and returns the raw token for delivery via cookie.
func (s *Store) CreateAdminSession(ctx context.Context, accountID, ip, ua string) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("superadmin: generate session token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	hash := tokenHash(token)

	now := time.Now().UTC()
	expires := now.Add(AdminSessionIdleTimeout)
	ts := now.Format(time.RFC3339)

	s.mu.Lock()
	_, err := s.db.ExecContext(ctx,
		s.db.Rebind(`INSERT INTO admin_sessions
		   (token_hash, account_id, ip, ua, created_at, last_seen_at, expires_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`),
		hash, accountID, ip, ua, ts, ts, expires.Format(time.RFC3339),
	)
	s.mu.Unlock()
	if err != nil {
		return "", fmt.Errorf("superadmin: create admin session: %w", err)
	}
	return token, nil
}

// LookupAdminSession validates a raw token and returns the accountID on success.
// It also touches last_seen_at and extends the expiry (sliding window).
func (s *Store) LookupAdminSession(ctx context.Context, token string) (string, error) {
	if token == "" {
		return "", ErrSessionExpired
	}
	hash := tokenHash(token)

	var accountID, expiresAt, createdAt string
	err := s.db.QueryRowContext(ctx,
		s.db.Rebind(`SELECT account_id, expires_at, created_at FROM admin_sessions WHERE token_hash = ?`), hash,
	).Scan(&accountID, &expiresAt, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrSessionExpired
	}
	if err != nil {
		return "", fmt.Errorf("superadmin: lookup admin session: %w", err)
	}

	now := time.Now().UTC()

	// Idle timeout (sliding window).
	exp, err := time.Parse(time.RFC3339, expiresAt)
	if err != nil || now.After(exp) {
		_ = s.deleteAdminSessionByHash(ctx, hash)
		return "", ErrSessionExpired
	}

	// Absolute TTL cap (regardless of activity).
	if created, cerr := time.Parse(time.RFC3339, createdAt); cerr == nil {
		if now.After(created.Add(AdminSessionAbsoluteTTL)) {
			_ = s.deleteAdminSessionByHash(ctx, hash)
			return "", ErrSessionExpired
		}
	}

	// Slide the idle window.
	newExpires := now.Add(AdminSessionIdleTimeout)
	s.mu.Lock()
	_, _ = s.db.ExecContext(ctx,
		s.db.Rebind(`UPDATE admin_sessions SET last_seen_at = ?, expires_at = ? WHERE token_hash = ?`),
		now.Format(time.RFC3339), newExpires.Format(time.RFC3339), hash,
	)
	s.mu.Unlock()

	return accountID, nil
}

// DeleteAdminSession removes the admin session identified by token.
func (s *Store) DeleteAdminSession(ctx context.Context, token string) error {
	return s.deleteAdminSessionByHash(ctx, tokenHash(token))
}

// deleteAdminSessionByHash removes the admin session row with the given token
// hash. Used both for explicit logout and to evict expired sessions on lookup.
func (s *Store) deleteAdminSessionByHash(ctx context.Context, hash string) error {
	s.mu.Lock()
	_, err := s.db.ExecContext(ctx, s.db.Rebind(`DELETE FROM admin_sessions WHERE token_hash = ?`), hash)
	s.mu.Unlock()
	return err
}

// AdminSessionFromRequest extracts the raw token from the request cookie.
func AdminSessionFromRequest(r *http.Request) string {
	c, err := r.Cookie(AdminSessionCookieName)
	if err != nil {
		return ""
	}
	return c.Value
}

// SetAdminSessionCookie writes the admin session cookie.
func SetAdminSessionCookie(w http.ResponseWriter, token string, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     AdminSessionCookieName,
		Value:    token,
		Path:     "/superadmin",
		MaxAge:   int(AdminSessionIdleTimeout.Seconds()),
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteStrictMode,
	})
}

// ClearAdminSessionCookie expires the admin session cookie.
func ClearAdminSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     AdminSessionCookieName,
		Value:    "",
		Path:     "/superadmin",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
}

// tokenHash returns the hex-encoded SHA-256 of token.
func tokenHash(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

// ─────────────────────────────────────────────────────────────────────────────
// IP Allowlist
// ─────────────────────────────────────────────────────────────────────────────

// ParseAllowlist parses the VULOS_ADMIN_IP_ALLOWLIST env var (comma-separated
// CIDRs) and returns the parsed nets. If prod and the list is empty, it fatals
// at startup. Prod is decided by the single authoritative, fail-safe resolver
// (env.IsProdResolved), so an unconfigured box requires the allowlist.
func ParseAllowlist() []*net.IPNet {
	raw := os.Getenv("VULOS_ADMIN_IP_ALLOWLIST")
	isProd := env.IsProdResolved()

	if raw == "" {
		if isProd {
			log.Fatalf("[superadmin] VULOS_ADMIN_IP_ALLOWLIST is empty in prod — refusing to start. Set the env var to comma-separated CIDRs.")
		}
		// Dev: allow all.
		return nil
	}

	var nets []*net.IPNet
	for _, cidr := range strings.Split(raw, ",") {
		cidr = strings.TrimSpace(cidr)
		if cidr == "" {
			continue
		}
		_, n, err := net.ParseCIDR(cidr)
		if err != nil {
			log.Fatalf("[superadmin] invalid CIDR in VULOS_ADMIN_IP_ALLOWLIST: %q: %v", cidr, err)
		}
		nets = append(nets, n)
	}
	return nets
}

// IPAllowed returns true when ip is in the allowlist, or the allowlist is nil
// (dev mode / allow-all).
func IPAllowed(allowlist []*net.IPNet, ip string) bool {
	if allowlist == nil {
		return true
	}
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return false
	}
	for _, n := range allowlist {
		if n.Contains(parsed) {
			return true
		}
	}
	return false
}

// remoteIP extracts the real client IP from r.
//
// Security: we MUST NOT trust the leftmost X-Forwarded-For value because that
// header is fully client-controlled and an attacker can forge it to bypass the
// IP allowlist (e.g. "X-Forwarded-For: 10.0.0.1" makes the forged IP appear
// to be the first entry).
//
// Instead we use the Fly-Client-IP header (set by the Fly.io edge proxy and
// stripped from untrusted clients) when available, and fall back to the
// TCP-level RemoteAddr which reflects the last hop and cannot be forged.
// X-Forwarded-For is intentionally ignored for IP-allowlist decisions.
func remoteIP(r *http.Request) string {
	// Fly.io edge sets this and strips it from client requests.
	if flyIP := r.Header.Get("Fly-Client-IP"); flyIP != "" {
		return strings.TrimSpace(flyIP)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// ─────────────────────────────────────────────────────────────────────────────
// Admin WebAuthn helpers
// ─────────────────────────────────────────────────────────────────────────────

// adminWAUser adapts an account for the go-webauthn library.
type adminWAUser struct {
	id    string
	email string
	creds []webauthn.Credential
}

func (u *adminWAUser) WebAuthnID() []byte                         { return []byte(u.id) }
func (u *adminWAUser) WebAuthnName() string                       { return u.email }
func (u *adminWAUser) WebAuthnDisplayName() string                { return u.email }
func (u *adminWAUser) WebAuthnCredentials() []webauthn.Credential { return u.creds }

// newAdminWebAuthn constructs a WebAuthn instance for the admin RP.
// Uses ADMIN_WEBAUTHN_RPID / ADMIN_WEBAUTHN_ORIGIN (falls back to the user-
// facing WEBAUTHN_RPID / WEBAUTHN_ORIGIN env vars).
func newAdminWebAuthn() (*webauthn.WebAuthn, error) {
	rpid := os.Getenv("ADMIN_WEBAUTHN_RPID")
	if rpid == "" {
		rpid = os.Getenv("WEBAUTHN_RPID")
	}
	origin := os.Getenv("ADMIN_WEBAUTHN_ORIGIN")
	if origin == "" {
		origin = os.Getenv("WEBAUTHN_ORIGIN")
	}
	if rpid == "" || origin == "" {
		return nil, fmt.Errorf("superadmin: WebAuthn not configured (set ADMIN_WEBAUTHN_RPID + ADMIN_WEBAUTHN_ORIGIN)")
	}
	return webauthn.New(&webauthn.Config{
		RPID:          rpid,
		RPDisplayName: "Vulos Admin",
		RPOrigins:     []string{origin},
	})
}

// loadAdminWAUser loads an adminWAUser for the given accountID.
func (s *Store) loadAdminWAUser(ctx context.Context, accountID string) (*adminWAUser, error) {
	var email string
	err := s.db.QueryRowContext(ctx, s.db.Rebind(`SELECT email FROM users WHERE id = ?`), accountID).Scan(&email)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("superadmin: account not found: %s", accountID)
	}
	if err != nil {
		return nil, fmt.Errorf("superadmin: load admin wa user: %w", err)
	}

	creds, err := s.loadAdminCredentials(ctx, accountID)
	if err != nil {
		return nil, err
	}
	return &adminWAUser{id: accountID, email: email, creds: creds}, nil
}

// CountAdminWebAuthnCredentials returns how many admin passkeys the account has
// registered. Used by the enrolment gate to distinguish first-key BOOTSTRAP
// (zero credentials) from a re-enrol attempt.
func (s *Store) CountAdminWebAuthnCredentials(ctx context.Context, accountID string) (int, error) {
	creds, err := s.loadAdminCredentials(ctx, accountID)
	if err != nil {
		return 0, err
	}
	return len(creds), nil
}

func (s *Store) loadAdminCredentials(ctx context.Context, accountID string) ([]webauthn.Credential, error) {
	rows, err := s.db.QueryContext(ctx,
		s.db.Rebind(`SELECT credential_id, public_key, sign_count, transports
		 FROM admin_webauthn_credentials WHERE account_id = ? ORDER BY created_at ASC`),
		accountID,
	)
	if err != nil {
		return nil, fmt.Errorf("superadmin: load admin credentials: %w", err)
	}
	defer rows.Close()

	var creds []webauthn.Credential
	for rows.Next() {
		var credIDBase64 string
		var pubKey []byte
		var signCount uint32
		var transportsJS string
		if err := rows.Scan(&credIDBase64, &pubKey, &signCount, &transportsJS); err != nil {
			return nil, fmt.Errorf("superadmin: scan admin credential: %w", err)
		}
		credID, err := base64.RawURLEncoding.DecodeString(credIDBase64)
		if err != nil {
			credID, err = base64.StdEncoding.DecodeString(credIDBase64)
			if err != nil {
				return nil, fmt.Errorf("superadmin: decode admin credential_id: %w", err)
			}
		}
		var rawT []string
		var transports []protocol.AuthenticatorTransport
		if json.Unmarshal([]byte(transportsJS), &rawT) == nil {
			for _, t := range rawT {
				transports = append(transports, protocol.AuthenticatorTransport(t))
			}
		}
		creds = append(creds, webauthn.Credential{
			ID:            credID,
			PublicKey:     pubKey,
			Authenticator: webauthn.Authenticator{SignCount: signCount},
			Transport:     transports,
		})
	}
	return creds, rows.Err()
}

// BeginAdminWebAuthnRegistration initiates a credential registration ceremony.
func (s *Store) BeginAdminWebAuthnRegistration(ctx context.Context, accountID string) (*protocol.CredentialCreation, *webauthn.SessionData, error) {
	wa, err := newAdminWebAuthn()
	if err != nil {
		return nil, nil, err
	}
	wu, err := s.loadAdminWAUser(ctx, accountID)
	if err != nil {
		return nil, nil, err
	}
	creation, session, err := wa.BeginRegistration(wu)
	if err != nil {
		return nil, nil, fmt.Errorf("superadmin: begin admin registration: %w", err)
	}
	if err := s.storePendingAdminChallenge(ctx, accountID, session); err != nil {
		return nil, nil, err
	}
	return creation, session, nil
}

// FinishAdminWebAuthnRegistration completes a credential registration.
func (s *Store) FinishAdminWebAuthnRegistration(ctx context.Context, accountID string, r *http.Request) error {
	wa, err := newAdminWebAuthn()
	if err != nil {
		return err
	}
	wu, err := s.loadAdminWAUser(ctx, accountID)
	if err != nil {
		return err
	}
	session, err := s.loadPendingAdminChallenge(ctx, accountID)
	if err != nil {
		return err
	}
	cred, err := wa.FinishRegistration(wu, *session, r)
	if err != nil {
		return fmt.Errorf("superadmin: finish admin registration: %w", err)
	}
	_ = s.deletePendingAdminChallenge(ctx, accountID)

	credIDBase64 := base64.RawURLEncoding.EncodeToString(cred.ID)
	rawT := make([]string, len(cred.Transport))
	for i, t := range cred.Transport {
		rawT[i] = string(t)
	}
	transportsJSON, _ := json.Marshal(rawT)

	s.mu.Lock()
	_, err = s.db.ExecContext(ctx,
		s.db.Rebind(`INSERT INTO admin_webauthn_credentials
		   (credential_id, account_id, public_key, sign_count, transports, attestation_type, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`),
		credIDBase64, accountID, cred.PublicKey, cred.Authenticator.SignCount,
		string(transportsJSON), cred.AttestationType, time.Now().UTC().Format(time.RFC3339),
	)
	s.mu.Unlock()
	if err != nil {
		if cpdb.IsUniqueViolation(err) {
			return fmt.Errorf("superadmin: admin credential already registered")
		}
		return fmt.Errorf("superadmin: insert admin credential: %w", err)
	}
	return nil
}

// BeginAdminWebAuthnLogin initiates a login assertion ceremony.
func (s *Store) BeginAdminWebAuthnLogin(ctx context.Context, accountID string) (*protocol.CredentialAssertion, *webauthn.SessionData, error) {
	wa, err := newAdminWebAuthn()
	if err != nil {
		return nil, nil, err
	}
	wu, err := s.loadAdminWAUser(ctx, accountID)
	if err != nil {
		return nil, nil, err
	}
	if len(wu.creds) == 0 {
		return nil, nil, fmt.Errorf("superadmin: no admin WebAuthn credentials registered for account %s", accountID)
	}
	assertion, session, err := wa.BeginLogin(wu)
	if err != nil {
		return nil, nil, fmt.Errorf("superadmin: begin admin login: %w", err)
	}
	if err := s.storePendingAdminChallenge(ctx, accountID, session); err != nil {
		return nil, nil, err
	}
	return assertion, session, nil
}

// FinishAdminWebAuthnLogin validates the assertion, mints an admin session, returns the token.
func (s *Store) FinishAdminWebAuthnLogin(ctx context.Context, accountID string, r *http.Request, ip, ua string) (string, error) {
	wa, err := newAdminWebAuthn()
	if err != nil {
		return "", err
	}
	wu, err := s.loadAdminWAUser(ctx, accountID)
	if err != nil {
		return "", err
	}
	session, err := s.loadPendingAdminChallenge(ctx, accountID)
	if err != nil {
		return "", err
	}
	cred, err := wa.FinishLogin(wu, *session, r)
	if err != nil {
		return "", fmt.Errorf("superadmin: finish admin login: %w", err)
	}
	_ = s.deletePendingAdminChallenge(ctx, accountID)

	// Update sign count.
	credIDBase64 := base64.RawURLEncoding.EncodeToString(cred.ID)
	s.mu.Lock()
	_, _ = s.db.ExecContext(ctx,
		s.db.Rebind(`UPDATE admin_webauthn_credentials SET sign_count = ? WHERE credential_id = ? AND account_id = ?`),
		cred.Authenticator.SignCount, credIDBase64, accountID,
	)
	s.mu.Unlock()

	return s.CreateAdminSession(ctx, accountID, ip, ua)
}

// Pending challenge helpers.
func (s *Store) storePendingAdminChallenge(ctx context.Context, accountID string, session *webauthn.SessionData) error {
	data, err := json.Marshal(session)
	if err != nil {
		return fmt.Errorf("superadmin: marshal challenge: %w", err)
	}
	s.mu.Lock()
	_, err = s.db.ExecContext(ctx,
		s.db.Rebind(`INSERT INTO pending_admin_webauthn (account_id, session_json, created_at)
		 VALUES (?, ?, ?)
		 ON CONFLICT (account_id) DO UPDATE SET session_json = excluded.session_json, created_at = excluded.created_at`),
		accountID, string(data), time.Now().UTC().Format(time.RFC3339),
	)
	s.mu.Unlock()
	return err
}

func (s *Store) loadPendingAdminChallenge(ctx context.Context, accountID string) (*webauthn.SessionData, error) {
	var raw string
	err := s.db.QueryRowContext(ctx,
		s.db.Rebind(`SELECT session_json FROM pending_admin_webauthn WHERE account_id = ?`), accountID,
	).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("superadmin: no pending admin challenge for account %s", accountID)
	}
	if err != nil {
		return nil, fmt.Errorf("superadmin: load pending challenge: %w", err)
	}
	var session webauthn.SessionData
	if err := json.Unmarshal([]byte(raw), &session); err != nil {
		return nil, fmt.Errorf("superadmin: unmarshal challenge: %w", err)
	}
	return &session, nil
}

func (s *Store) deletePendingAdminChallenge(ctx context.Context, accountID string) error {
	s.mu.Lock()
	_, err := s.db.ExecContext(ctx,
		s.db.Rebind(`DELETE FROM pending_admin_webauthn WHERE account_id = ?`), accountID,
	)
	s.mu.Unlock()
	return err
}
