// store.go — SQLite-backed Store + MemStore reference for the kms package.
//
// Pure-Go modernc.org/sqlite; single writer via SetMaxOpenConns(1) + WAL.
// Embedded migrations via go:embed.
//
// Config.EncryptedKeyMaterial is stored as-is (caller must encrypt under
// STORAGE_KEK before calling PutConfig). The storage package's KEKFromHex +
// aesGCMEncrypt/aesGCMDecrypt pattern is reused for the config-layer KEK
// encryption; the raw functions are re-implemented here so the kms package
// has zero import dependency on the storage package.
package kms

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"sort"
	"sync"
	"time"

	"github.com/vul-os/vulos-management/pkg/cpdb"
)

// ---------------------------------------------------------------------------
// SQLStore
// ---------------------------------------------------------------------------

// SQLStore is the production cpdb-backed Store for the kms package
// (SQLite by default, Postgres when DATABASE_URL / VULOS_DATABASE_URL is set).
type SQLStore struct {
	db *cpdb.DB
	mu sync.Mutex
}

// Open applies migrations to db and returns a ready *SQLStore.
//
// db should be obtained from cpdb.Open("kms") for production, or from
// cpdb.OpenSQLiteDSN(":memory:") for tests. Migrations are applied
// automatically and are idempotent (safe to call on every startup).
func Open(db *cpdb.DB) (*SQLStore, error) {
	subFS, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("kms: embed sub: %w", err)
	}
	if err := db.Migrate(subFS); err != nil {
		return nil, fmt.Errorf("kms: migrate: %w", err)
	}
	return &SQLStore{db: db}, nil
}

// Close closes the underlying database connection.
func (s *SQLStore) Close() error { return s.db.Close() }

// compile-time assertion.
var _ Store = (*SQLStore)(nil)

// GetConfig returns the KMS config for an account.
func (s *SQLStore) GetConfig(ctx context.Context, accountID string) (Config, error) {
	var (
		c          Config
		endpoint   sql.NullString
		ekm        sql.NullString
		createdStr string
		updatedStr string
	)
	err := s.db.QueryRowContext(ctx,
		s.db.Rebind(`SELECT account_id, tier, kind, endpoint, encrypted_key_material,
		        kek_version, created_at, updated_at
		   FROM kms_configs WHERE account_id = ?`), accountID,
	).Scan(&c.AccountID, &c.Tier, &c.Kind,
		&endpoint, &ekm, &c.KEKVersion, &createdStr, &updatedStr)
	if errors.Is(err, sql.ErrNoRows) {
		return Config{}, ErrUnknownAccount
	}
	if err != nil {
		return Config{}, fmt.Errorf("kms: get config: %w", err)
	}
	c.Endpoint = endpoint.String
	c.EncryptedKeyMaterial = ekm.String
	c.CreatedAt, _ = time.Parse(time.RFC3339, createdStr)
	c.UpdatedAt, _ = time.Parse(time.RFC3339, updatedStr)
	return c, nil
}

// PutConfig upserts the KMS config for an account.
func (s *SQLStore) PutConfig(ctx context.Context, c Config) error {
	now := time.Now().UTC().Format(time.RFC3339)
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx,
		s.db.Rebind(`INSERT INTO kms_configs
		   (account_id, tier, kind, endpoint, encrypted_key_material,
		    kek_version, created_at, updated_at)
		   VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(account_id) DO UPDATE SET
		   tier                   = excluded.tier,
		   kind                   = excluded.kind,
		   endpoint               = excluded.endpoint,
		   encrypted_key_material = excluded.encrypted_key_material,
		   kek_version            = excluded.kek_version,
		   updated_at             = excluded.updated_at`),
		c.AccountID, string(c.Tier), string(c.Kind),
		nullableKMSStr(c.Endpoint),
		nullableKMSStr(c.EncryptedKeyMaterial),
		c.KEKVersion, now, now,
	)
	if err != nil {
		return fmt.Errorf("kms: put config: %w", err)
	}
	return nil
}

// DeleteConfig removes the KMS config and all DEKs for an account.
func (s *SQLStore) DeleteConfig(ctx context.Context, accountID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.db.ExecContext(ctx,
		s.db.Rebind(`DELETE FROM kms_deks WHERE account_id = ?`), accountID); err != nil {
		return fmt.Errorf("kms: delete deks: %w", err)
	}
	if _, err := s.db.ExecContext(ctx,
		s.db.Rebind(`DELETE FROM kms_configs WHERE account_id = ?`), accountID); err != nil {
		return fmt.Errorf("kms: delete config: %w", err)
	}
	return nil
}

// PutDEK inserts a new wrapped DEK record.
func (s *SQLStore) PutDEK(ctx context.Context, d DEKRecord) error {
	now := time.Now().UTC().Format(time.RFC3339)
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx,
		s.db.Rebind(`INSERT INTO kms_deks
		   (id, account_id, object_ref, wrapped_dek, kek_version, revoked, created_at)
		   VALUES (?, ?, ?, ?, ?, 0, ?)`),
		d.ID, d.AccountID, d.ObjectRef, d.WrappedDEK, d.KEKVersion, now,
	)
	if err != nil {
		return fmt.Errorf("kms: put dek: %w", err)
	}
	return nil
}

// GetDEK returns the DEKRecord by id.
func (s *SQLStore) GetDEK(ctx context.Context, id string) (DEKRecord, error) {
	var (
		d          DEKRecord
		revokedInt int
		createdStr string
	)
	err := s.db.QueryRowContext(ctx,
		s.db.Rebind(`SELECT id, account_id, object_ref, wrapped_dek, kek_version, revoked, created_at
		   FROM kms_deks WHERE id = ?`), id,
	).Scan(&d.ID, &d.AccountID, &d.ObjectRef, &d.WrappedDEK,
		&d.KEKVersion, &revokedInt, &createdStr)
	if errors.Is(err, sql.ErrNoRows) {
		return DEKRecord{}, ErrUnknownKey
	}
	if err != nil {
		return DEKRecord{}, fmt.Errorf("kms: get dek: %w", err)
	}
	d.Revoked = revokedInt != 0
	d.CreatedAt, _ = time.Parse(time.RFC3339, createdStr)
	if d.Revoked {
		return d, ErrKeyRevoked
	}
	return d, nil
}

// GetDEKByRef returns the most-recent non-revoked DEKRecord for account+objectRef.
func (s *SQLStore) GetDEKByRef(ctx context.Context, accountID, objectRef string) (DEKRecord, error) {
	var (
		d          DEKRecord
		revokedInt int
		createdStr string
	)
	err := s.db.QueryRowContext(ctx,
		s.db.Rebind(`SELECT id, account_id, object_ref, wrapped_dek, kek_version, revoked, created_at
		   FROM kms_deks
		  WHERE account_id = ? AND object_ref = ? AND revoked = 0
		  ORDER BY created_at DESC
		  LIMIT 1`), accountID, objectRef,
	).Scan(&d.ID, &d.AccountID, &d.ObjectRef, &d.WrappedDEK,
		&d.KEKVersion, &revokedInt, &createdStr)
	if errors.Is(err, sql.ErrNoRows) {
		return DEKRecord{}, ErrUnknownKey
	}
	if err != nil {
		return DEKRecord{}, fmt.Errorf("kms: get dek by ref: %w", err)
	}
	d.Revoked = revokedInt != 0
	d.CreatedAt, _ = time.Parse(time.RFC3339, createdStr)
	return d, nil
}

// ListDEKs returns all DEKRecord rows for an account, newest first.
func (s *SQLStore) ListDEKs(ctx context.Context, accountID string, includeRevoked bool) ([]DEKRecord, error) {
	q := `SELECT id, account_id, object_ref, wrapped_dek, kek_version, revoked, created_at
		    FROM kms_deks WHERE account_id = ?`
	if !includeRevoked {
		q += ` AND revoked = 0`
	}
	q += ` ORDER BY created_at DESC`
	rows, err := s.db.QueryContext(ctx, s.db.Rebind(q), accountID)
	if err != nil {
		return nil, fmt.Errorf("kms: list deks: %w", err)
	}
	defer rows.Close()
	var out []DEKRecord
	for rows.Next() {
		var (
			d          DEKRecord
			revokedInt int
			createdStr string
		)
		if err := rows.Scan(&d.ID, &d.AccountID, &d.ObjectRef, &d.WrappedDEK,
			&d.KEKVersion, &revokedInt, &createdStr); err != nil {
			return nil, fmt.Errorf("kms: list deks scan: %w", err)
		}
		d.Revoked = revokedInt != 0
		d.CreatedAt, _ = time.Parse(time.RFC3339, createdStr)
		out = append(out, d)
	}
	return out, rows.Err()
}

// UpdateWrappedDEK replaces the wrappedDEK blob and kek_version for an existing row.
func (s *SQLStore) UpdateWrappedDEK(ctx context.Context, id string, wrappedDEK []byte, newVersion int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.db.ExecContext(ctx,
		s.db.Rebind(`UPDATE kms_deks SET wrapped_dek = ?, kek_version = ? WHERE id = ? AND revoked = 0`),
		wrappedDEK, newVersion, id)
	if err != nil {
		return fmt.Errorf("kms: update wrapped dek: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrUnknownKey
	}
	return nil
}

// RevokeDEK marks a DEK as revoked.
func (s *SQLStore) RevokeDEK(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.db.ExecContext(ctx,
		s.db.Rebind(`UPDATE kms_deks SET revoked = 1 WHERE id = ?`), id)
	if err != nil {
		return fmt.Errorf("kms: revoke dek: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrUnknownKey
	}
	return nil
}

// ---------------------------------------------------------------------------
// MemStore — in-memory reference Store
// ---------------------------------------------------------------------------

// MemStore is the in-memory reference Store. Concurrency-safe. It DEFINES
// the semantics every other implementation must match.
type MemStore struct {
	mu      sync.Mutex
	configs map[string]Config
	deks    map[string]DEKRecord // id → record
}

// NewMemStore returns an empty MemStore.
func NewMemStore() *MemStore {
	return &MemStore{
		configs: map[string]Config{},
		deks:    map[string]DEKRecord{},
	}
}

// compile-time assertion.
var _ Store = (*MemStore)(nil)

func (m *MemStore) GetConfig(_ context.Context, accountID string) (Config, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.configs[accountID]
	if !ok {
		return Config{}, ErrUnknownAccount
	}
	return c, nil
}

func (m *MemStore) PutConfig(_ context.Context, c Config) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	c.UpdatedAt = time.Now().UTC()
	if c.CreatedAt.IsZero() {
		c.CreatedAt = c.UpdatedAt
	}
	m.configs[c.AccountID] = c
	return nil
}

func (m *MemStore) DeleteConfig(_ context.Context, accountID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.configs, accountID)
	for id, d := range m.deks {
		if d.AccountID == accountID {
			delete(m.deks, id)
		}
	}
	return nil
}

func (m *MemStore) PutDEK(_ context.Context, d DEKRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if d.CreatedAt.IsZero() {
		d.CreatedAt = time.Now().UTC()
	}
	m.deks[d.ID] = d
	return nil
}

func (m *MemStore) GetDEK(_ context.Context, id string) (DEKRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.deks[id]
	if !ok {
		return DEKRecord{}, ErrUnknownKey
	}
	if d.Revoked {
		return d, ErrKeyRevoked
	}
	return d, nil
}

func (m *MemStore) GetDEKByRef(_ context.Context, accountID, objectRef string) (DEKRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var best DEKRecord
	found := false
	for _, d := range m.deks {
		if d.AccountID == accountID && d.ObjectRef == objectRef && !d.Revoked {
			if !found || d.CreatedAt.After(best.CreatedAt) {
				best = d
				found = true
			}
		}
	}
	if !found {
		return DEKRecord{}, ErrUnknownKey
	}
	return best, nil
}

func (m *MemStore) ListDEKs(_ context.Context, accountID string, includeRevoked bool) ([]DEKRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []DEKRecord
	for _, d := range m.deks {
		if d.AccountID != accountID {
			continue
		}
		if !includeRevoked && d.Revoked {
			continue
		}
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out, nil
}

func (m *MemStore) UpdateWrappedDEK(_ context.Context, id string, wrappedDEK []byte, newVersion int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.deks[id]
	if !ok || d.Revoked {
		return ErrUnknownKey
	}
	d.WrappedDEK = wrappedDEK
	d.KEKVersion = newVersion
	m.deks[id] = d
	return nil
}

func (m *MemStore) RevokeDEK(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.deks[id]
	if !ok {
		return ErrUnknownKey
	}
	d.Revoked = true
	m.deks[id] = d
	return nil
}

func (m *MemStore) Close() error { return nil }

// ---------------------------------------------------------------------------
// Config-layer KEK encryption helpers
// (mirrors storage package pattern; no import dependency on storage)
// ---------------------------------------------------------------------------

// EncryptKeyMaterial encrypts raw key/token material under the Vulos STORAGE_KEK.
// Returns hex(nonce||ciphertext). kek must be 32 bytes.
func EncryptKeyMaterial(kek []byte, material string) (string, error) {
	if len(kek) != 32 {
		return "", fmt.Errorf("kms: KEK must be 32 bytes, got %d", len(kek))
	}
	return kmsAESGCMEncrypt(kek, material)
}

// DecryptKeyMaterial decrypts a value produced by EncryptKeyMaterial.
func DecryptKeyMaterial(kek []byte, encoded string) (string, error) {
	if len(kek) != 32 {
		return "", fmt.Errorf("kms: KEK must be 32 bytes, got %d", len(kek))
	}
	return kmsAESGCMDecrypt(kek, encoded)
}

// KEKFromHex decodes a 32-byte hex string into a KEK.
// Returns nil for empty input (dev mode). Errors on malformed/wrong-length input.
func KEKFromHex(h string) ([]byte, error) {
	if h == "" {
		return nil, nil
	}
	b, err := hex.DecodeString(h)
	if err != nil {
		return nil, fmt.Errorf("kms: KEK not valid hex: %w", err)
	}
	if len(b) != 32 {
		return nil, fmt.Errorf("kms: KEK must be 32 bytes (64 hex chars), got %d bytes", len(b))
	}
	return b, nil
}

// kmsAESGCMEncrypt encrypts plaintext with the given 32-byte key.
func kmsAESGCMEncrypt(key []byte, plaintext string) (string, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	ct := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return hex.EncodeToString(ct), nil
}

// kmsAESGCMDecrypt decrypts a hex(nonce||ciphertext) value.
func kmsAESGCMDecrypt(key []byte, encoded string) (string, error) {
	data, err := hex.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("kms: decode: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	ns := gcm.NonceSize()
	if len(data) < ns {
		return "", errors.New("kms: ciphertext too short")
	}
	plain, err := gcm.Open(nil, data[:ns], data[ns:], nil)
	if err != nil {
		return "", fmt.Errorf("kms: decrypt: %w", err)
	}
	return string(plain), nil
}

// nullableKMSStr returns a sql.NullString for the given string.
func nullableKMSStr(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}
