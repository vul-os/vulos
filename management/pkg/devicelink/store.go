// Package devicelink implements a device-code style account LINK flow for
// headless / self-host installs (WAVE24-RELAY-BILLING), plus the account-bound
// install credential it mints on approval.
//
// The flow mirrors the OAuth 2.0 device authorization grant (RFC 8628) but is
// internal to the CP:
//
//  1. The install (e.g. a wede relay agent) POSTs /api/link/device/start and
//     receives {device_code, user_code, verification_url, interval, expires_in}.
//  2. A human opens verification_url while authenticated to the CP and POSTs
//     /api/link/device/approve {user_code}; the CP binds that pending link to
//     the caller's account.
//  3. The install polls /api/link/device/poll {device_code}. Once approved the
//     poll returns an account-bound INSTALL CREDENTIAL — an opaque bearer token
//     the install stores and thereafter presents to the CP (relay entitlement
//     read) and embeds in the tunnel-server token store so an agent resolves to
//     an account.
//
// Security posture (fail closed):
//   - device_code and the install credential are 256-bit random secrets, stored
//     ONLY as sha256 hashes (raw values never persisted). A DB read cannot
//     recover a usable credential.
//   - user_code is a short human-typed code; approval is rate-limited and codes
//     expire quickly, so brute force is bounded.
//   - a poll before approval returns pending; an expired/unknown device_code is
//     denied; a used device_code cannot be re-polled for a second credential.
//   - the install credential resolves to {account_id}; it is NOT a session and
//     grants no CP user privileges — only "this install belongs to account X".
package devicelink

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base32"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"sync"
	"time"

	"github.com/vul-os/vulos-management/pkg/cpdb"
)

// Sentinel errors.
var (
	ErrNotFound   = errors.New("devicelink: unknown or expired code")
	ErrPending    = errors.New("devicelink: authorization pending")
	ErrDenied     = errors.New("devicelink: authorization denied")
	ErrConsumed   = errors.New("devicelink: code already consumed")
	ErrBadInput   = errors.New("devicelink: invalid input")
	ErrCredential = errors.New("devicelink: unknown install credential")
)

// DefaultInterval is the recommended poll interval handed to the install.
const DefaultInterval = 5 * time.Second

// DefaultTTL is how long a device_code / user_code pair is valid before it
// must be restarted. Kept short so an unclaimed code is a small attack surface.
const DefaultTTL = 10 * time.Minute

// Start is the result of beginning a device-link.
type Start struct {
	DeviceCode      string        // opaque secret the install polls with (returned once)
	UserCode        string        // short human-typed approval code
	VerificationURL string        // where the human approves
	Interval        time.Duration // recommended poll interval
	ExpiresIn       time.Duration // code lifetime
}

// Credential is an approved, account-bound install credential.
type Credential struct {
	Token     string // opaque bearer (returned once, on the approving poll)
	AccountID string
}

// hash returns the hex sha256 of a secret (what we persist / look up by).
func hash(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// randToken returns a URL-safe 256-bit random secret.
func randToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// genUserCode returns a short, unambiguous, human-typeable code like "K7QP-2M9X".
func genUserCode() (string, error) {
	b := make([]byte, 5)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	// Crockford-ish base32 without padding; group as XXXX-XXXX for readability.
	enc := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b)
	enc = strings.ToUpper(enc)
	if len(enc) >= 8 {
		enc = enc[:8]
	}
	return enc[:4] + "-" + enc[4:], nil
}

// NormalizeUserCode upper-cases and strips spaces/dashes so a human can type it
// loosely. The stored form is the compact upper-case string.
func NormalizeUserCode(s string) string {
	s = strings.ToUpper(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, "-", "")
	s = strings.ReplaceAll(s, " ", "")
	return s
}

// ──────────────────────────────────────────────────────────────────────────
// Store — the devicelink persistence seam (cpdb dual SQLite/Postgres) plus a
// dep-free MemStore for tests/dev.
// ──────────────────────────────────────────────────────────────────────────

// Store is the devicelink persistence interface.
type Store interface {
	// StartLink creates a pending link and returns the raw device_code +
	// user_code (returned exactly once). verificationURL/interval/ttl shape the
	// Start result echoed to the install.
	StartLink(ctx context.Context, verificationURL string, interval, ttl time.Duration) (Start, error)

	// Approve binds the pending link identified by userCode to accountID. It is
	// the authenticated human's action. Returns ErrNotFound for an unknown/
	// expired code; ErrConsumed if the link was already approved+consumed.
	Approve(ctx context.Context, userCode, accountID string) error

	// Poll advances the device_code. Returns:
	//   - a minted Credential (raw token, once) when approved and not yet consumed,
	//   - ErrPending when not yet approved,
	//   - ErrDenied when denied,
	//   - ErrNotFound when unknown/expired,
	//   - ErrConsumed when the credential was already handed out.
	Poll(ctx context.Context, deviceCode string) (Credential, error)

	// ResolveCredential maps a raw install credential to its account, or
	// ErrCredential. Used by /api/relay/entitlement and the relay's CPTokenStore.
	ResolveCredential(ctx context.Context, token string) (accountID string, err error)

	Close() error
}

// ──────────────────────────────────────────────────────────────────────────
// SQLStore.
// ──────────────────────────────────────────────────────────────────────────

// SQLStore is the production cpdb-backed Store.
type SQLStore struct {
	db  *cpdb.DB
	mu  sync.Mutex
	now func() time.Time
}

// OpenSQLStore migrates and returns a SQLStore.
func OpenSQLStore(db *cpdb.DB) (*SQLStore, error) {
	subFS, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("devicelink: embed sub: %w", err)
	}
	if err := db.Migrate(subFS); err != nil {
		return nil, fmt.Errorf("devicelink: migrate: %w", err)
	}
	return &SQLStore{db: db, now: time.Now}, nil
}

func (s *SQLStore) clock() time.Time {
	if s.now != nil {
		return s.now().UTC()
	}
	return time.Now().UTC()
}

// StartLink implements Store.
func (s *SQLStore) StartLink(ctx context.Context, verificationURL string, interval, ttl time.Duration) (Start, error) {
	if interval <= 0 {
		interval = DefaultInterval
	}
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	deviceCode, err := randToken()
	if err != nil {
		return Start{}, err
	}
	// Retry a few times on the (astronomically unlikely) user_code collision.
	var userCode string
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.clock()
	expires := now.Add(ttl)
	for attempt := 0; attempt < 5; attempt++ {
		uc, uerr := genUserCode()
		if uerr != nil {
			return Start{}, uerr
		}
		_, xerr := s.db.ExecContext(ctx, s.db.Rebind(`
			INSERT INTO device_links
			    (device_code_hash, user_code, account_id, state, created_at, expires_at)
			VALUES (?, ?, '', 'pending', ?, ?)`),
			hash(deviceCode), NormalizeUserCode(uc),
			now.Format(time.RFC3339), expires.Format(time.RFC3339),
		)
		if xerr == nil {
			userCode = uc
			break
		}
		// A duplicate user_code (UNIQUE) → retry with a fresh one.
		if attempt == 4 {
			return Start{}, fmt.Errorf("devicelink: start: %w", xerr)
		}
	}
	return Start{
		DeviceCode:      deviceCode,
		UserCode:        userCode,
		VerificationURL: verificationURL,
		Interval:        interval,
		ExpiresIn:       ttl,
	}, nil
}

// Approve implements Store.
func (s *SQLStore) Approve(ctx context.Context, userCode, accountID string) error {
	if accountID == "" {
		return ErrBadInput
	}
	uc := NormalizeUserCode(userCode)
	if uc == "" {
		return ErrBadInput
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.clock()
	// Only approve a still-pending, unexpired link.
	res, err := s.db.ExecContext(ctx, s.db.Rebind(`
		UPDATE device_links
		   SET state = 'approved', account_id = ?, approved_at = ?
		 WHERE user_code = ? AND state = 'pending' AND expires_at > ?`),
		accountID, now.Format(time.RFC3339), uc, now.Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("devicelink: approve: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		// Either unknown/expired, or already approved/consumed. Disambiguate for a
		// clearer error without leaking existence to an unauthenticated caller
		// (this path is behind a session).
		var state string
		qerr := s.db.QueryRowContext(ctx, s.db.Rebind(
			`SELECT state FROM device_links WHERE user_code = ?`), uc).Scan(&state)
		if qerr == nil && (state == "approved" || state == "consumed") {
			return ErrConsumed
		}
		return ErrNotFound
	}
	return nil
}

// Poll implements Store.
func (s *SQLStore) Poll(ctx context.Context, deviceCode string) (Credential, error) {
	if deviceCode == "" {
		return Credential{}, ErrNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.clock()
	dh := hash(deviceCode)
	var state, accountID, expiresAt string
	err := s.db.QueryRowContext(ctx, s.db.Rebind(
		`SELECT state, account_id, expires_at FROM device_links WHERE device_code_hash = ?`),
		dh,
	).Scan(&state, &accountID, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Credential{}, ErrNotFound
	}
	if err != nil {
		return Credential{}, fmt.Errorf("devicelink: poll: %w", err)
	}
	if exp, perr := time.Parse(time.RFC3339, expiresAt); perr == nil && now.After(exp) {
		return Credential{}, ErrNotFound
	}
	switch state {
	case "pending":
		return Credential{}, ErrPending
	case "denied":
		return Credential{}, ErrDenied
	case "consumed":
		return Credential{}, ErrConsumed
	case "approved":
		// Mint the install credential and flip to consumed atomically.
		token, terr := randToken()
		if terr != nil {
			return Credential{}, terr
		}
		res, uerr := s.db.ExecContext(ctx, s.db.Rebind(`
			UPDATE device_links SET state = 'consumed', consumed_at = ?
			 WHERE device_code_hash = ? AND state = 'approved'`),
			now.Format(time.RFC3339), dh,
		)
		if uerr != nil {
			return Credential{}, fmt.Errorf("devicelink: consume: %w", uerr)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			// Lost a race — someone else consumed it.
			return Credential{}, ErrConsumed
		}
		if _, ierr := s.db.ExecContext(ctx, s.db.Rebind(`
			INSERT INTO install_credentials (token_hash, account_id, created_at)
			VALUES (?, ?, ?)`),
			hash(token), accountID, now.Format(time.RFC3339),
		); ierr != nil {
			return Credential{}, fmt.Errorf("devicelink: mint credential: %w", ierr)
		}
		return Credential{Token: token, AccountID: accountID}, nil
	default:
		return Credential{}, ErrNotFound
	}
}

// ResolveCredential implements Store.
func (s *SQLStore) ResolveCredential(ctx context.Context, token string) (string, error) {
	if token == "" {
		return "", ErrCredential
	}
	var accountID string
	err := s.db.QueryRowContext(ctx, s.db.Rebind(
		`SELECT account_id FROM install_credentials WHERE token_hash = ? AND revoked = 0`),
		hash(token),
	).Scan(&accountID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrCredential
	}
	if err != nil {
		return "", fmt.Errorf("devicelink: resolve credential: %w", err)
	}
	return accountID, nil
}

// Close implements Store.
func (s *SQLStore) Close() error { return s.db.Close() }

var _ Store = (*SQLStore)(nil)
