// Package apikeys implements the vulos.cloud unified API-key store (APIKEYS-01).
//
// Keys carry the prefix "vk_" followed by 43 base64url characters (32 random
// bytes). Only the SHA-256 hex digest is persisted — the raw secret is returned
// exactly once at creation and is never logged or re-exposed.
//
// The introspection contract matches the office consumer exactly:
//
//	POST /api/keys/introspect {"key":"vk_…"}
//	→ {"valid":true, "account":"alice@vulos.org", "scopes":[…], "products":[…]}
//	→ {"valid":false}
//
// Storage: cpdb-backed — SQLite (modernc.org/sqlite, pure-Go, no CGO) by default;
// Postgres via pgx/v5/stdlib when DATABASE_URL / VULOS_DATABASE_URL is set.
package apikeys

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math/big"
	"sync"
	"time"

	"github.com/vul-os/vulos-management/pkg/cpdb"
)

// KeyPrefix is the required prefix for every API key issued by this package.
// Matches the constant in the office consumer (vulos-office/backend/apikey).
const KeyPrefix = "vk_"

// ErrKeyNotFound is returned when a key hash is not in the store.
var ErrKeyNotFound = errors.New("apikeys: key not found")

// ErrKeyRevoked is returned when the key is marked revoked.
var ErrKeyRevoked = errors.New("apikeys: key revoked")

// ErrKeyExpired is returned when the key has passed its expires_at timestamp.
var ErrKeyExpired = errors.New("apikeys: key expired")

// ─── ID generation ────────────────────────────────────────────────────────────

const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// newKeyID returns a 26-char Crockford-base32 random ID (same alphabet as the
// publicapi and other internal stores).
func newKeyID() (string, error) {
	b := make([]byte, 26)
	for i := range b {
		n, err := rand.Int(rand.Reader, big.NewInt(32))
		if err != nil {
			return "", fmt.Errorf("apikeys: generate id: %w", err)
		}
		b[i] = crockford[n.Int64()]
	}
	return string(b), nil
}

// ─── Key generation + hashing ─────────────────────────────────────────────────

// generateRawKey returns a new "vk_" + 43-char base64url key (32 random bytes).
// The raw key is NEVER stored; only its SHA-256 hash is persisted.
func generateRawKey() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("apikeys: generate key: %w", err)
	}
	return KeyPrefix + base64.RawURLEncoding.EncodeToString(b), nil
}

// HashKey returns the SHA-256 hex of raw (constant-time-safe storage key).
// This is the only operation that touches the raw key value after generation;
// callers MUST NOT log raw.
func HashKey(raw string) string {
	h := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(h[:])
}

// ─── Models ───────────────────────────────────────────────────────────────────

// APIKey is the public (safe-to-return) representation of a key row.
// The raw key secret is NEVER included in this struct.
type APIKey struct {
	ID        string     `json:"id"`
	AccountID string     `json:"account_id"`
	Name      string     `json:"name"`
	Scopes    []string   `json:"scopes"`
	Products  []string   `json:"products"`
	CreatedAt time.Time  `json:"created_at"`
	RevokedAt *time.Time `json:"revoked_at,omitempty"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

// Active reports whether the key is currently usable (not revoked, not expired).
func (k APIKey) Active() bool {
	if k.RevokedAt != nil {
		return false
	}
	if k.ExpiresAt != nil && time.Now().UTC().After(*k.ExpiresAt) {
		return false
	}
	return true
}

// IntrospectResult is the introspection response shape — matches the office
// consumer contract exactly (vulos-office/backend/apikey.Result).
type IntrospectResult struct {
	Valid    bool     `json:"valid"`
	Account  string   `json:"account,omitempty"`
	Scopes   []string `json:"scopes,omitempty"`
	Products []string `json:"products,omitempty"`
}

// ─── Store ────────────────────────────────────────────────────────────────────

// EntitlementFilter narrows the products/scopes an introspected key may claim,
// to reflect state this package deliberately knows nothing about (the
// account's current billing tier/suspension standing) — ENTITLEMENT-SEAM-01.
// A vk_ key's products/scopes are set once at issuance time (IssueKey) and
// echoed back verbatim by Introspect; without this seam a downgraded or
// admin-suspended account's key keeps claiming entitlement to products it can
// no longer access until the key is manually revoked. apikeys does not import
// billing directly (avoids a dependency cycle and keeps this package
// billing-agnostic) — the filter is injected once at wiring time, mirroring
// billing.Store's own AdminSuspendChecker seam.
type EntitlementFilter interface {
	// FilterProducts returns the subset of products the account is CURRENTLY
	// entitled to, given its billing standing. Implementations should fail
	// OPEN (return products unchanged, nil) on a transient lookup error — an
	// entitlement-check outage must never turn into a wholesale outage for
	// every app.
	FilterProducts(ctx context.Context, accountID string, products []string) ([]string, error)
}

// Store is the API-key backend, backed by either SQLite or Postgres via cpdb.
// All public methods are identical regardless of backend.
type Store struct {
	db *cpdb.DB
	// mu serialises writes for SQLite (which requires MaxOpenConns(1)).
	// On Postgres the pool handles concurrency and the mutex is a no-op overhead,
	// but the overhead is negligible and keeps the implementation consistent.
	mu sync.Mutex

	// entitlement is the optional ENTITLEMENT-SEAM-01 filter consulted by
	// Introspect. nil (the default) preserves the exact pre-existing
	// behaviour: products are returned verbatim from the key row.
	entitlementMu sync.RWMutex
	entitlement   EntitlementFilter
}

// SetEntitlementFilter installs the EntitlementFilter consulted by Introspect.
// Call once during wiring; safe to pass nil to disable (reverts to returning
// the key's stored products verbatim).
func (s *Store) SetEntitlementFilter(f EntitlementFilter) {
	s.entitlementMu.Lock()
	defer s.entitlementMu.Unlock()
	s.entitlement = f
}

func (s *Store) entitlementFilter() EntitlementFilter {
	s.entitlementMu.RLock()
	defer s.entitlementMu.RUnlock()
	return s.entitlement
}

// Open applies migrations to db and returns a ready Store.
//
// db should be obtained from cpdb.Open("apikeys") for production, or from
// cpdb.OpenSQLiteDSN(":memory:") for tests.  Migrations are applied
// automatically and are idempotent (safe to call on every startup).
func Open(db *cpdb.DB) (*Store, error) {
	subFS, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("apikeys: embed sub: %w", err)
	}
	if err := db.Migrate(subFS); err != nil {
		return nil, fmt.Errorf("apikeys: migrate: %w", err)
	}
	return &Store{db: db}, nil
}

// Close closes the underlying database.
func (s *Store) Close() error { return s.db.Close() }

// ─── IssueKey ─────────────────────────────────────────────────────────────────

// IssueKey mints a new API key for accountID with the given name, scopes, and
// products. Returns the raw key (returned EXACTLY ONCE; caller must show it
// to the user immediately) and the persisted APIKey metadata.
func (s *Store) IssueKey(ctx context.Context, accountID, name string, scopes, products []string) (rawKey string, key APIKey, err error) {
	if scopes == nil {
		scopes = []string{}
	}
	if products == nil {
		products = []string{}
	}

	raw, err := generateRawKey()
	if err != nil {
		return "", APIKey{}, err
	}
	hash := HashKey(raw)

	id, err := newKeyID()
	if err != nil {
		return "", APIKey{}, err
	}

	scopesJSON, err := json.Marshal(scopes)
	if err != nil {
		return "", APIKey{}, fmt.Errorf("apikeys: marshal scopes: %w", err)
	}
	productsJSON, err := json.Marshal(products)
	if err != nil {
		return "", APIKey{}, fmt.Errorf("apikeys: marshal products: %w", err)
	}

	now := time.Now().UTC()
	nowStr := now.Format(time.RFC3339)

	s.mu.Lock()
	defer s.mu.Unlock()
	_, err = s.db.ExecContext(ctx,
		s.db.Rebind(`
			INSERT INTO api_keys (id, account_id, name, key_hash, scopes, products, created_at)
			VALUES (?,?,?,?,?,?,?)`),
		id, accountID, name, hash, string(scopesJSON), string(productsJSON), nowStr,
	)
	if err != nil {
		return "", APIKey{}, fmt.Errorf("apikeys: insert key: %w", err)
	}

	return raw, APIKey{
		ID:        id,
		AccountID: accountID,
		Name:      name,
		Scopes:    scopes,
		Products:  products,
		CreatedAt: now,
	}, nil
}

// ─── ListKeys ─────────────────────────────────────────────────────────────────

// ListKeys returns all non-revoked keys for accountID ordered by creation date
// descending. The raw key secret is NEVER included in the results.
func (s *Store) ListKeys(ctx context.Context, accountID string) ([]APIKey, error) {
	rows, err := s.db.QueryContext(ctx,
		s.db.Rebind(`
			SELECT id, account_id, name, scopes, products, created_at, revoked_at, expires_at
			FROM api_keys
			WHERE account_id = ?
			ORDER BY created_at DESC`),
		accountID,
	)
	if err != nil {
		return nil, fmt.Errorf("apikeys: list keys: %w", err)
	}
	defer rows.Close()

	var out []APIKey
	for rows.Next() {
		var k APIKey
		var (
			scopesJSON, productsJSON string
			createdAt                string
			revokedAt, expiresAt     sql.NullString
		)
		if err := rows.Scan(
			&k.ID, &k.AccountID, &k.Name,
			&scopesJSON, &productsJSON,
			&createdAt, &revokedAt, &expiresAt,
		); err != nil {
			return nil, err
		}
		k.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
		if err := json.Unmarshal([]byte(scopesJSON), &k.Scopes); err != nil {
			k.Scopes = []string{}
		}
		if err := json.Unmarshal([]byte(productsJSON), &k.Products); err != nil {
			k.Products = []string{}
		}
		if revokedAt.Valid {
			t, _ := time.Parse(time.RFC3339, revokedAt.String)
			k.RevokedAt = &t
		}
		if expiresAt.Valid {
			t, _ := time.Parse(time.RFC3339, expiresAt.String)
			k.ExpiresAt = &t
		}
		out = append(out, k)
	}
	if out == nil {
		out = []APIKey{}
	}
	return out, rows.Err()
}

// ─── RevokeKey ────────────────────────────────────────────────────────────────

// RevokeKey marks the key with the given id as revoked. Only keys belonging to
// accountID are touched (prevents cross-account revocation).
func (s *Store) RevokeKey(ctx context.Context, accountID, keyID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := s.db.ExecContext(ctx,
		s.db.Rebind(`UPDATE api_keys SET revoked_at=? WHERE id=? AND account_id=? AND revoked_at IS NULL`),
		now, keyID, accountID,
	)
	if err != nil {
		return fmt.Errorf("apikeys: revoke key: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrKeyNotFound
	}
	return nil
}

// ─── Introspect ───────────────────────────────────────────────────────────────

// Introspect resolves a raw key (must start with "vk_") and returns the
// associated account, scopes, and products. Returns {Valid:false} for unknown,
// revoked, or expired keys — never returns an error for those cases; errors
// are reserved for unexpected DB failures.
func (s *Store) Introspect(ctx context.Context, rawKey string) (IntrospectResult, error) {
	if rawKey == "" {
		return IntrospectResult{Valid: false}, nil
	}

	hash := HashKey(rawKey)

	var (
		accountID                string
		scopesJSON, productsJSON string
		revokedAt, expiresAt     sql.NullString
	)
	err := s.db.QueryRowContext(ctx,
		s.db.Rebind(`
			SELECT account_id, scopes, products, revoked_at, expires_at
			FROM api_keys
			WHERE key_hash = ?`),
		hash,
	).Scan(&accountID, &scopesJSON, &productsJSON, &revokedAt, &expiresAt)

	if errors.Is(err, sql.ErrNoRows) {
		return IntrospectResult{Valid: false}, nil
	}
	if err != nil {
		return IntrospectResult{}, fmt.Errorf("apikeys: introspect: %w", err)
	}

	// Revoked?
	if revokedAt.Valid {
		return IntrospectResult{Valid: false}, nil
	}
	// Expired?
	if expiresAt.Valid {
		exp, _ := time.Parse(time.RFC3339, expiresAt.String)
		if time.Now().UTC().After(exp) {
			return IntrospectResult{Valid: false}, nil
		}
	}

	var scopes, products []string
	_ = json.Unmarshal([]byte(scopesJSON), &scopes)
	_ = json.Unmarshal([]byte(productsJSON), &products)
	if scopes == nil {
		scopes = []string{}
	}
	if products == nil {
		products = []string{}
	}

	// ENTITLEMENT-SEAM-01: narrow products to what the account's CURRENT
	// billing standing entitles, when a filter is wired. Fails open (keeps the
	// stored products) on a filter error — see EntitlementFilter's doc comment.
	if f := s.entitlementFilter(); f != nil {
		if filtered, ferr := f.FilterProducts(ctx, accountID, products); ferr == nil {
			products = filtered
		}
	}

	return IntrospectResult{
		Valid:    true,
		Account:  accountID,
		Scopes:   scopes,
		Products: products,
	}, nil
}

// ─── Issue rate limiter ───────────────────────────────────────────────────────

// IssueLimiter is a simple in-process rate limiter for key issuance.
// Allows at most maxPerWindow key creations per account per window duration.
// Uses lazy GC to keep memory bounded.
type IssueLimiter struct {
	mu        sync.Mutex
	counts    map[string][]time.Time
	window    time.Duration
	maxPerWin int
	lastGC    time.Time
}

// NewIssueLimiter creates a limiter allowing maxPerWindow creations per window.
func NewIssueLimiter(window time.Duration, maxPerWindow int) *IssueLimiter {
	return &IssueLimiter{
		counts:    make(map[string][]time.Time),
		window:    window,
		maxPerWin: maxPerWindow,
	}
}

// Allow returns true if accountID is within its issue rate limit and records
// the attempt. Returns false when the account has exceeded the limit.
func (l *IssueLimiter) Allow(accountID string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()

	// Lazy GC: evict stale entries every 5 minutes.
	if now.Sub(l.lastGC) > 5*time.Minute {
		cutoff := now.Add(-l.window)
		for k, times := range l.counts {
			filtered := times[:0]
			for _, t := range times {
				if t.After(cutoff) {
					filtered = append(filtered, t)
				}
			}
			if len(filtered) == 0 {
				delete(l.counts, k)
			} else {
				l.counts[k] = filtered
			}
		}
		l.lastGC = now
	}

	// Filter current window for this account.
	cutoff := now.Add(-l.window)
	times := l.counts[accountID]
	filtered := times[:0]
	for _, t := range times {
		if t.After(cutoff) {
			filtered = append(filtered, t)
		}
	}
	l.counts[accountID] = filtered

	if len(l.counts[accountID]) >= l.maxPerWin {
		return false
	}
	l.counts[accountID] = append(l.counts[accountID], now)
	return true
}
