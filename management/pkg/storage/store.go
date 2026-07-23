// store.go — cpdb-backed Store implementation for the storage subsystem.
//
// Backend is SQLite (default, zero-dep, self-host) or Postgres (cloud/Neon),
// selected automatically by cpdb.Open / cpdb.OpenSQLiteDSN.
// AES-GCM encryption of BYO secret keys via STORAGE_KEK env (32-byte hex).
// When KEK is empty, BYO secret_key is stored plaintext (dev-mode warning only).
package storage

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
	"sync"
	"time"

	"github.com/vul-os/vulos-management/pkg/cpdb"
)

// ---------------------------------------------------------------------------
// SQLStore
// ---------------------------------------------------------------------------

// SQLStore is the production Store implementation backed by cpdb (SQLite or Postgres).
// Concurrency-safe via mu for writes.
type SQLStore struct {
	db  *cpdb.DB
	mu  sync.Mutex // serialises all writes
	kek []byte     // 32-byte AES-GCM key-encryption key; nil = dev mode (plaintext)
}

// Open applies migrations to db and returns a ready *SQLStore.
// kek is the key-encryption key for BYO secrets (may be nil in dev mode).
//
// db should be obtained from cpdb.Open("storage") for production, or from
// cpdb.OpenSQLiteDSN(":memory:") for tests.
func Open(db *cpdb.DB, kek []byte) (*SQLStore, error) {
	subFS, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("storage: embed sub: %w", err)
	}
	if err := db.Migrate(subFS); err != nil {
		return nil, fmt.Errorf("storage: migrate: %w", err)
	}
	return &SQLStore{db: db, kek: kek}, nil
}

// Close closes the underlying database connection.
func (s *SQLStore) Close() error { return s.db.Close() }

// compile-time assertion: SQLStore satisfies Store.
var _ Store = (*SQLStore)(nil)

// ---------------------------------------------------------------------------
// Config persistence
// ---------------------------------------------------------------------------

// GetConfig returns the Config for the given account.
// Returns ErrUnknownAccount if no record exists.
func (s *SQLStore) GetConfig(ctx context.Context, accountID string) (Config, error) {
	var (
		c            Config
		endpoint     sql.NullString
		accessKey    sql.NullString
		secretKeyEnc sql.NullString
		byoInt       int
		createdAt    string
		updatedAt    string
	)
	err := s.db.QueryRowContext(ctx,
		s.db.Rebind(`SELECT account_id, byo, endpoint, region, bucket, access_key, secret_key_enc,
		        created_at, updated_at
		   FROM storage_configs WHERE account_id = ?`), accountID,
	).Scan(&c.AccountID, &byoInt, &endpoint, &c.Region, &c.Bucket,
		&accessKey, &secretKeyEnc, &createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Config{}, ErrUnknownAccount
	}
	if err != nil {
		return Config{}, fmt.Errorf("storage: get config: %w", err)
	}

	c.BYO = byoInt != 0
	c.Endpoint = endpoint.String
	c.AccessKey = accessKey.String

	if secretKeyEnc.Valid && secretKeyEnc.String != "" && len(s.kek) == 32 {
		dec, err := aesGCMDecrypt(s.kek, secretKeyEnc.String)
		if err != nil {
			return Config{}, fmt.Errorf("storage: decrypt secret_key: %w", err)
		}
		c.SecretKey = dec
	}
	_ = createdAt
	_ = updatedAt
	return c, nil
}

// PutConfig upserts the Config for an account.
// If c.BYO and c.SecretKey is non-empty, encrypts it under KEK.
func (s *SQLStore) PutConfig(ctx context.Context, c Config) error {
	now := time.Now().UTC().Format(time.RFC3339)

	var secretKeyEnc sql.NullString
	if c.BYO && c.SecretKey != "" {
		if len(s.kek) == 32 {
			enc, err := aesGCMEncrypt(s.kek, c.SecretKey)
			if err != nil {
				return fmt.Errorf("storage: encrypt secret_key: %w", err)
			}
			secretKeyEnc = sql.NullString{String: enc, Valid: true}
		} else {
			// Dev mode: store plaintext (no KEK).
			secretKeyEnc = sql.NullString{String: c.SecretKey, Valid: true}
		}
	}

	byoInt := 0
	if c.BYO {
		byoInt = 1
	}
	region := c.Region
	if region == "" {
		region = "auto"
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx,
		s.db.Rebind(`INSERT INTO storage_configs
		   (account_id, byo, endpoint, region, bucket, access_key, secret_key_enc, created_at, updated_at)
		   VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(account_id) DO UPDATE SET
		   byo           = excluded.byo,
		   endpoint      = excluded.endpoint,
		   region        = excluded.region,
		   bucket        = excluded.bucket,
		   access_key    = excluded.access_key,
		   secret_key_enc = excluded.secret_key_enc,
		   updated_at    = excluded.updated_at`),
		c.AccountID, byoInt,
		nullableStr(c.Endpoint),
		region,
		c.Bucket,
		nullableStr(c.AccessKey),
		secretKeyEnc,
		now, now,
	)
	if err != nil {
		return fmt.Errorf("storage: put config: %w", err)
	}
	return nil
}

// DeleteConfig removes the Config for an account (idempotent).
func (s *SQLStore) DeleteConfig(ctx context.Context, accountID string) error {
	s.mu.Lock()
	_, err := s.db.ExecContext(ctx,
		s.db.Rebind(`DELETE FROM storage_configs WHERE account_id = ?`), accountID)
	s.mu.Unlock()
	return err
}

// ---------------------------------------------------------------------------
// Usage persistence
// ---------------------------------------------------------------------------

// InsertUsage appends a new usage sample for an account bucket.
func (s *SQLStore) InsertUsage(ctx context.Context, u UsageSample) error {
	s.mu.Lock()
	_, err := s.db.ExecContext(ctx,
		s.db.Rebind(`INSERT INTO storage_usage
		   (account_id, bucket, size_bytes, object_count, sampled_at)
		   VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(account_id, bucket, sampled_at) DO UPDATE SET
		   size_bytes   = excluded.size_bytes,
		   object_count = excluded.object_count`),
		u.AccountID, u.Bucket, u.SizeBytes, u.ObjectCount,
		u.SampledAt.UTC().Format(time.RFC3339),
	)
	s.mu.Unlock()
	if err != nil {
		return fmt.Errorf("storage: insert usage: %w", err)
	}
	return nil
}

// LatestUsage returns the most recent usage sample for an account.
// Returns ErrUnknownAccount if no sample exists.
func (s *SQLStore) LatestUsage(ctx context.Context, accountID string) (UsageSample, error) {
	var (
		u          UsageSample
		sampledStr string
	)
	err := s.db.QueryRowContext(ctx,
		s.db.Rebind(`SELECT account_id, bucket, size_bytes, object_count, sampled_at
		   FROM storage_usage
		  WHERE account_id = ?
		  ORDER BY sampled_at DESC
		  LIMIT 1`), accountID,
	).Scan(&u.AccountID, &u.Bucket, &u.SizeBytes, &u.ObjectCount, &sampledStr)
	if errors.Is(err, sql.ErrNoRows) {
		return UsageSample{}, ErrUnknownAccount
	}
	if err != nil {
		return UsageSample{}, fmt.Errorf("storage: latest usage: %w", err)
	}
	u.SampledAt, _ = time.Parse(time.RFC3339, sampledStr)
	return u, nil
}

// ---------------------------------------------------------------------------
// AES-GCM helpers for BYO secret encryption
// ---------------------------------------------------------------------------

// aesGCMEncrypt encrypts plaintext using AES-GCM with the given 32-byte key.
// Returns hex(nonce||ciphertext).
func aesGCMEncrypt(key []byte, plaintext string) (string, error) {
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
	ciphertext := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return hex.EncodeToString(ciphertext), nil
}

// aesGCMDecrypt decrypts a hex(nonce||ciphertext) string produced by aesGCMEncrypt.
func aesGCMDecrypt(key []byte, encoded string) (string, error) {
	data, err := hex.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("storage: decode ciphertext: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonceSize := gcm.NonceSize()
	if len(data) < nonceSize {
		return "", errors.New("storage: ciphertext too short")
	}
	plaintext, err := gcm.Open(nil, data[:nonceSize], data[nonceSize:], nil)
	if err != nil {
		return "", fmt.Errorf("storage: decrypt: %w", err)
	}
	return string(plaintext), nil
}

// ---------------------------------------------------------------------------
// SQL nullable helpers
// ---------------------------------------------------------------------------

func nullableStr(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}

// KEKFromHex decodes a 32-byte hex string into a key-encryption key.
// Returns nil if the input is empty (dev mode). Returns an error for
// malformed or wrong-length input.
func KEKFromHex(h string) ([]byte, error) {
	if h == "" {
		return nil, nil
	}
	b, err := hex.DecodeString(h)
	if err != nil {
		return nil, fmt.Errorf("storage: STORAGE_KEK is not valid hex: %w", err)
	}
	if len(b) != 32 {
		return nil, fmt.Errorf("storage: STORAGE_KEK must be 32 bytes (64 hex chars), got %d bytes", len(b))
	}
	return b, nil
}
