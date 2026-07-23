// store.go — local, single-tenant issuance/storage for box-scoped API keys
// (APIKEY-LOCAL-01).
//
// apikey.go (the rest of this package) is a CLIENT ONLY: it introspects
// "vk_…" keys against a REMOTE control plane (VULOS_CP_BASE_URL) for
// cross-product entitlement in cloud/cell deployments. A self-hosted box has
// no such CP to call, yet still needs to let its owner mint bearer
// credentials for the box's OWN public REST API
// (backend/services/publicapi) — a locally-authoritative key that is good
// for THIS box only.
//
// LocalStore fills that gap: a tiny, single-tenant, SQLite-backed key store
// (create / list / revoke / introspect) that implements the SAME
// Introspector interface as the CP client. It deliberately does NOT
// replicate vulos-management's pkg/apikeys: no Postgres backend, no
// cross-product scope/entitlement filter, no SCIM — just "does this box
// know this key, and which local user does it belong to".
//
// Keys minted here use LocalKeyPrefix ("vkl_"), NOT KeyPrefix ("vk_"). This
// is deliberate, not cosmetic: auth.Handler.Middleware (services/auth) scans
// every request for "Bearer vk_…" and, when a CP is configured
// (VKIntrospector != nil), sends it to the CP for introspection — an
// unrecognised local key would come back {valid:false} and get the request
// 401'd by the GLOBAL middleware before it ever reached a locally-authoritative
// consumer. A distinct prefix means the global vk_ path never even looks at
// these keys; only backend/services/publicapi (which calls LocalStore.
// Introspect directly, in-process) ever validates them.
//
// LocalStore.Introspect's Result.Account is a local OS user ID
// (auth.User.ID / auth.Profile.UserID), NOT an email — unlike the CP
// contract, where Account is an email resolved via auth.Store.
// GetUserByEmail. Do NOT wire a *LocalStore into auth.Handler.
// VKIntrospector; it is for in-process use by services/publicapi only.
package apikey

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver for the local key store
)

// LocalKeyPrefix is the required prefix for every key minted by LocalStore.
// See the package doc above for why this differs from KeyPrefix ("vk_").
const LocalKeyPrefix = "vkl_"

// ErrLocalKeyNotFound is returned when a key id is not found for the owner.
var ErrLocalKeyNotFound = errors.New("apikey: local key not found")

// LocalKey is the public (safe-to-return) representation of a locally
// issued key row. The raw secret is NEVER included.
type LocalKey struct {
	ID        string     `json:"id"`
	OwnerID   string     `json:"owner_id"`
	Name      string     `json:"name"`
	CreatedAt time.Time  `json:"created_at"`
	RevokedAt *time.Time `json:"revoked_at,omitempty"`
}

// Active reports whether the key is currently usable (not revoked).
func (k LocalKey) Active() bool { return k.RevokedAt == nil }

// LocalStore is a single-tenant, SQLite-backed key store for a self-hosted
// box's own public REST API. It implements Introspector.
type LocalStore struct {
	db *sql.DB
	mu sync.Mutex // serialises writes (SQLite is single-writer)
}

// OpenLocalStore opens (or creates) the local key store at dsn (a SQLite
// file path) and ensures its schema. It keeps its own dedicated handle so it
// never contends with any other package's writer (mirrors internal/webpush's
// NewSQLiteStore).
func OpenLocalStore(dsn string) (*LocalStore, error) {
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("apikey: open local store: %w", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS local_api_keys (
			id         TEXT PRIMARY KEY,
			owner_id   TEXT NOT NULL,
			name       TEXT NOT NULL,
			key_hash   TEXT NOT NULL UNIQUE,
			created_at TEXT NOT NULL,
			revoked_at TEXT
		);
		CREATE INDEX IF NOT EXISTS idx_local_api_keys_owner ON local_api_keys(owner_id);
	`); err != nil {
		db.Close()
		return nil, fmt.Errorf("apikey: init local schema: %w", err)
	}
	return &LocalStore{db: db}, nil
}

// Close closes the underlying database handle.
func (s *LocalStore) Close() error { return s.db.Close() }

// ─── ID / key generation ───────────────────────────────────────────────────

func newLocalKeyID() (string, error) {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("apikey: generate id: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// generateRawLocalKey returns a new LocalKeyPrefix + 43-char base64url key
// (32 random bytes). The raw key is NEVER stored — only its SHA-256 hash is.
func generateRawLocalKey() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("apikey: generate key: %w", err)
	}
	return LocalKeyPrefix + base64.RawURLEncoding.EncodeToString(b), nil
}

func hashLocalKey(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// ─── IssueKey ───────────────────────────────────────────────────────────────

// IssueKey mints a new key for ownerID. Returns the raw key — returned
// EXACTLY ONCE; the caller must surface it to the user immediately and must
// never log it.
func (s *LocalStore) IssueKey(ctx context.Context, ownerID, name string) (rawKey string, key LocalKey, err error) {
	if ownerID == "" {
		return "", LocalKey{}, errors.New("apikey: owner id required")
	}
	raw, err := generateRawLocalKey()
	if err != nil {
		return "", LocalKey{}, err
	}
	hash := hashLocalKey(raw)
	id, err := newLocalKeyID()
	if err != nil {
		return "", LocalKey{}, err
	}
	now := time.Now().UTC()

	s.mu.Lock()
	defer s.mu.Unlock()
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO local_api_keys (id, owner_id, name, key_hash, created_at) VALUES (?,?,?,?,?)`,
		id, ownerID, name, hash, now.Format(time.RFC3339),
	)
	if err != nil {
		return "", LocalKey{}, fmt.Errorf("apikey: insert local key: %w", err)
	}
	return raw, LocalKey{ID: id, OwnerID: ownerID, Name: name, CreatedAt: now}, nil
}

// ─── ListKeys ───────────────────────────────────────────────────────────────

// ListKeys returns all keys (including revoked ones, so the UI can show
// history) owned by ownerID, newest first. The raw secret is never included.
func (s *LocalStore) ListKeys(ctx context.Context, ownerID string) ([]LocalKey, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, owner_id, name, created_at, revoked_at FROM local_api_keys
		 WHERE owner_id = ? ORDER BY created_at DESC`, ownerID)
	if err != nil {
		return nil, fmt.Errorf("apikey: list local keys: %w", err)
	}
	defer rows.Close()

	out := []LocalKey{}
	for rows.Next() {
		var k LocalKey
		var createdAt string
		var revokedAt sql.NullString
		if err := rows.Scan(&k.ID, &k.OwnerID, &k.Name, &createdAt, &revokedAt); err != nil {
			return nil, err
		}
		k.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
		if revokedAt.Valid {
			t, _ := time.Parse(time.RFC3339, revokedAt.String)
			k.RevokedAt = &t
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// ─── RevokeKey ──────────────────────────────────────────────────────────────

// RevokeKey marks the key revoked. Only a key owned by ownerID is touched
// (prevents cross-account revocation).
func (s *LocalStore) RevokeKey(ctx context.Context, ownerID, keyID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := s.db.ExecContext(ctx,
		`UPDATE local_api_keys SET revoked_at=? WHERE id=? AND owner_id=? AND revoked_at IS NULL`,
		now, keyID, ownerID,
	)
	if err != nil {
		return fmt.Errorf("apikey: revoke local key: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrLocalKeyNotFound
	}
	return nil
}

// ─── Introspect (Introspector) ─────────────────────────────────────────────

// Introspect implements Introspector against the local store: unknown or
// revoked keys return {Valid:false} (never an error — errors are reserved
// for unexpected DB failures, matching the CP contract's fail-closed-on-
// error / valid-false-on-miss split). A hit always carries exactly
// [ProductOS] — a locally issued key is only ever good for this box's own
// APIs, never a claim about other products. See the package doc for why
// Result.Account is a local user ID here, not an email.
func (s *LocalStore) Introspect(ctx context.Context, rawKey string) (Result, error) {
	if rawKey == "" {
		return Result{Valid: false}, nil
	}
	hash := hashLocalKey(rawKey)

	var ownerID string
	var revokedAt sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT owner_id, revoked_at FROM local_api_keys WHERE key_hash = ?`, hash,
	).Scan(&ownerID, &revokedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Result{Valid: false}, nil
	}
	if err != nil {
		return Result{}, fmt.Errorf("apikey: introspect local key: %w", err)
	}
	if revokedAt.Valid {
		return Result{Valid: false}, nil
	}
	return Result{Valid: true, Account: ownerID, Scopes: []string{}, Products: []string{ProductOS}}, nil
}
