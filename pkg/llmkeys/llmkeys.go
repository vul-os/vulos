// Package llmkeys stores per-user BYO LLM provider API keys, encrypted at rest
// (LLM-BYOK-01).
//
// Keys are sealed with AES-256-GCM under a dedicated KEK (the same custody model
// as internal/integrations crypto.go): a 32-byte base64 key from LLM_KEYS_KEK,
// falling back to INTEGRATIONS_KEK, with an all-zeros dev fallback ONLY under
// VULOS_DEV. The plaintext key is NEVER returned by any read path and NEVER
// logged — GET lists providers + timestamps only.
//
// Storage: cpdb dual-backend seam (SQLite for self-host, Postgres for cloud).
// MemStore is the dep-free reference for tests / dev mode.
package llmkeys

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/vul-os/vulos-management/pkg/cpdb"
	"github.com/vul-os/vulos-management/pkg/env"
)

// ErrNotFound is returned when no key exists for (account, provider).
var ErrNotFound = errors.New("llmkeys: not found")

// ErrInvalidProvider is returned for an empty/invalid provider id.
var ErrInvalidProvider = errors.New("llmkeys: invalid provider")

// ErrEmptyKey is returned when an empty key is submitted.
var ErrEmptyKey = errors.New("llmkeys: empty key")

// ProviderInfo is the non-secret view returned by List: never the raw key.
type ProviderInfo struct {
	Provider  string `json:"provider"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// devFallbackKEK is used only when VULOS_DEV=true and no KEK env var is set.
var devFallbackKEK = make([]byte, 32)

// LoadKEK returns the 32-byte AES key for LLM-key encryption. It reads
// LLM_KEYS_KEK (base64), then INTEGRATIONS_KEK (base64) as a fallback so a
// deployment can reuse one KEK, and finally an all-zeros dev key under
// VULOS_DEV. Production MUST set one of the env vars.
func LoadKEK() ([]byte, error) {
	for _, env := range []string{"LLM_KEYS_KEK", "INTEGRATIONS_KEK"} {
		raw := strings.TrimSpace(os.Getenv(env))
		if raw == "" {
			continue
		}
		key, err := base64.StdEncoding.DecodeString(raw)
		if err != nil {
			return nil, fmt.Errorf("llmkeys: %s is not valid base64: %w", env, err)
		}
		if len(key) != 32 {
			return nil, fmt.Errorf("llmkeys: %s must decode to exactly 32 bytes, got %d", env, len(key))
		}
		return key, nil
	}
	// WAVE34: gate the all-zeros dev fallback on env.DevKEKFallbackAllowed so a
	// stray VULOS_DEV on a prod box can never activate a zero KEK.
	if env.DevKEKFallbackAllowed() {
		return devFallbackKEK, nil
	}
	return nil, fmt.Errorf("llmkeys: LLM_KEYS_KEK (or INTEGRATIONS_KEK) must be set (base64-encoded 32-byte key)")
}

// NormalizeProvider lowercases + trims a provider id.
func NormalizeProvider(p string) string { return strings.ToLower(strings.TrimSpace(p)) }

func encrypt(plain string, kek []byte) (string, error) {
	block, err := aes.NewCipher(kek)
	if err != nil {
		return "", fmt.Errorf("llmkeys: aes new cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("llmkeys: gcm: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("llmkeys: rand nonce: %w", err)
	}
	ct := gcm.Seal(nonce, nonce, []byte(plain), nil)
	return base64.StdEncoding.EncodeToString(ct), nil
}

func decrypt(enc string, kek []byte) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		return "", fmt.Errorf("llmkeys: base64 decode: %w", err)
	}
	block, err := aes.NewCipher(kek)
	if err != nil {
		return "", fmt.Errorf("llmkeys: aes new cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("llmkeys: gcm: %w", err)
	}
	ns := gcm.NonceSize()
	if len(raw) < ns {
		return "", fmt.Errorf("llmkeys: ciphertext too short")
	}
	plain, err := gcm.Open(nil, raw[:ns], raw[ns:], nil)
	if err != nil {
		return "", fmt.Errorf("llmkeys: gcm open: %w", err)
	}
	return string(plain), nil
}

// Store is the per-account LLM-key store.
type Store interface {
	// Put upserts the encrypted key for (accountID, provider).
	Put(ctx context.Context, accountID, provider, rawKey string) error
	// List returns the providers configured for accountID — WITHOUT raw keys.
	List(ctx context.Context, accountID string) ([]ProviderInfo, error)
	// Get returns the DECRYPTED key (server-internal use only — never an API
	// response). ErrNotFound when absent.
	Get(ctx context.Context, accountID, provider string) (string, error)
	// Delete removes the key for (accountID, provider). Deleting a missing key is
	// a no-op (no error).
	Delete(ctx context.Context, accountID, provider string) error
	Close() error
}

// ──────────────────────────────────────────────────────────────────────────
// SQLStore
// ──────────────────────────────────────────────────────────────────────────

// SQLStore is the cpdb-backed store.
type SQLStore struct {
	db  *cpdb.DB
	kek []byte
	mu  sync.Mutex
}

// OpenSQLStore opens the llmkeys store using the provided cpdb.DB, applies
// the migration, and binds the KEK.
func OpenSQLStore(db *cpdb.DB, kek []byte) (*SQLStore, error) {
	if len(kek) != 32 {
		return nil, fmt.Errorf("llmkeys: kek must be 32 bytes, got %d", len(kek))
	}
	subFS, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("llmkeys: embed sub: %w", err)
	}
	if err := db.Migrate(subFS); err != nil {
		return nil, fmt.Errorf("llmkeys: migrate: %w", err)
	}
	return &SQLStore{db: db, kek: kek}, nil
}

// Close closes the underlying database.
func (s *SQLStore) Close() error { return s.db.Close() }

// Ping pings the database (for /readyz).
func (s *SQLStore) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

// Put implements Store.
func (s *SQLStore) Put(ctx context.Context, accountID, provider, rawKey string) error {
	provider = NormalizeProvider(provider)
	if provider == "" {
		return ErrInvalidProvider
	}
	if strings.TrimSpace(rawKey) == "" {
		return ErrEmptyKey
	}
	enc, err := encrypt(rawKey, s.kek)
	if err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err = s.db.ExecContext(ctx, s.db.Rebind(`
		INSERT INTO llm_keys (account_id, provider, key_enc, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(account_id, provider) DO UPDATE SET
		  key_enc    = excluded.key_enc,
		  updated_at = excluded.updated_at`),
		accountID, provider, enc, now, now,
	)
	if err != nil {
		return fmt.Errorf("llmkeys: put: %w", err)
	}
	return nil
}

// List implements Store.
func (s *SQLStore) List(ctx context.Context, accountID string) ([]ProviderInfo, error) {
	rows, err := s.db.QueryContext(ctx,
		s.db.Rebind(`SELECT provider, created_at, updated_at FROM llm_keys WHERE account_id = ? ORDER BY provider`),
		accountID,
	)
	if err != nil {
		return nil, fmt.Errorf("llmkeys: list: %w", err)
	}
	defer rows.Close()
	out := []ProviderInfo{}
	for rows.Next() {
		var pi ProviderInfo
		if err := rows.Scan(&pi.Provider, &pi.CreatedAt, &pi.UpdatedAt); err != nil {
			return nil, fmt.Errorf("llmkeys: scan: %w", err)
		}
		out = append(out, pi)
	}
	return out, rows.Err()
}

// Get implements Store.
func (s *SQLStore) Get(ctx context.Context, accountID, provider string) (string, error) {
	provider = NormalizeProvider(provider)
	var enc string
	err := s.db.QueryRowContext(ctx,
		s.db.Rebind(`SELECT key_enc FROM llm_keys WHERE account_id = ? AND provider = ?`), accountID, provider,
	).Scan(&enc)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("llmkeys: get: %w", err)
	}
	return decrypt(enc, s.kek)
}

// Delete implements Store.
func (s *SQLStore) Delete(ctx context.Context, accountID, provider string) error {
	provider = NormalizeProvider(provider)
	if provider == "" {
		return ErrInvalidProvider
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx,
		s.db.Rebind(`DELETE FROM llm_keys WHERE account_id = ? AND provider = ?`), accountID, provider)
	if err != nil {
		return fmt.Errorf("llmkeys: delete: %w", err)
	}
	return nil
}

// ──────────────────────────────────────────────────────────────────────────
// MemStore — in-memory reference for tests / dev.
// ──────────────────────────────────────────────────────────────────────────

type memRow struct {
	enc                  string
	createdAt, updatedAt string
}

// MemStore is the dep-free in-memory Store. It still encrypts at rest so the
// raw key is never held in plaintext (matching the SQL store's guarantee).
type MemStore struct {
	mu   sync.Mutex
	kek  []byte
	rows map[string]map[string]memRow // account → provider → row
}

// NewMemStore constructs an empty MemStore bound to kek.
func NewMemStore(kek []byte) *MemStore {
	return &MemStore{kek: kek, rows: map[string]map[string]memRow{}}
}

// Put implements Store.
func (m *MemStore) Put(_ context.Context, accountID, provider, rawKey string) error {
	provider = NormalizeProvider(provider)
	if provider == "" {
		return ErrInvalidProvider
	}
	if strings.TrimSpace(rawKey) == "" {
		return ErrEmptyKey
	}
	enc, err := encrypt(rawKey, m.kek)
	if err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.rows[accountID] == nil {
		m.rows[accountID] = map[string]memRow{}
	}
	prev, ok := m.rows[accountID][provider]
	created := now
	if ok {
		created = prev.createdAt
	}
	m.rows[accountID][provider] = memRow{enc: enc, createdAt: created, updatedAt: now}
	return nil
}

// List implements Store.
func (m *MemStore) List(_ context.Context, accountID string) ([]ProviderInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []ProviderInfo{}
	for provider, row := range m.rows[accountID] {
		out = append(out, ProviderInfo{Provider: provider, CreatedAt: row.createdAt, UpdatedAt: row.updatedAt})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Provider < out[j].Provider })
	return out, nil
}

// Get implements Store.
func (m *MemStore) Get(_ context.Context, accountID, provider string) (string, error) {
	provider = NormalizeProvider(provider)
	m.mu.Lock()
	defer m.mu.Unlock()
	row, ok := m.rows[accountID][provider]
	if !ok {
		return "", ErrNotFound
	}
	return decrypt(row.enc, m.kek)
}

// Delete implements Store.
func (m *MemStore) Delete(_ context.Context, accountID, provider string) error {
	provider = NormalizeProvider(provider)
	if provider == "" {
		return ErrInvalidProvider
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.rows[accountID] != nil {
		delete(m.rows[accountID], provider)
	}
	return nil
}

// Close implements Store.
func (m *MemStore) Close() error { return nil }

// compile-time assertions.
var (
	_ Store = (*SQLStore)(nil)
	_ Store = (*MemStore)(nil)
)
