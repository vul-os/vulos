// store.go — SQLite-backed Store for the kms package (single-owner box
// scope). Pure-Go modernc.org/sqlite; single writer via SetMaxOpenConns(1) +
// WAL, matching every other on-box store (see services/integrations/selfhost,
// services/files). Migrations are embedded and applied via the shared
// internal/migrate runner.
package kms

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	dbmigrate "vulos/backend/internal/migrate"

	_ "modernc.org/sqlite" // pure-Go SQLite driver (no CGo)
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// singletonConfigID is the fixed primary key of the one-and-only kms_config
// row (CHECK(id = 1) enforces this at the schema level too).
const singletonConfigID = 1

// SQLStore is the production SQLite-backed Store for the kms package.
type SQLStore struct {
	db *sql.DB
	mu sync.Mutex
}

// OpenStore opens (or creates) the KMS SQLite store at <dbDir>/kms.db. If
// dbDir is empty an in-memory store is used (tests). WAL + busy-timeout,
// idempotent schema — same posture as the OS's other on-box stores.
func OpenStore(dbDir string) (*SQLStore, error) {
	dsn := "file::memory:?cache=shared&_pragma=busy_timeout(5000)"
	if dbDir != "" {
		path := filepath.Join(dbDir, "kms.db")
		dsn = "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("kms: open db: %w", err)
	}
	db.SetMaxOpenConns(1) // single-writer file; serialize writers
	s := &SQLStore{db: db}
	if err := dbmigrate.Apply(db, migrationsFS, "migrations"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("kms: migrate: %w", err)
	}
	return s, nil
}

// Close closes the underlying database connection.
func (s *SQLStore) Close() error { return s.db.Close() }

// compile-time assertion.
var _ Store = (*SQLStore)(nil)

// GetConfig returns the singleton KMS config.
func (s *SQLStore) GetConfig(ctx context.Context) (Config, error) {
	var (
		c          Config
		endpoint   sql.NullString
		ekm        sql.NullString
		createdStr string
		updatedStr string
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT kind, endpoint, encrypted_key_material, kek_version, created_at, updated_at
		   FROM kms_config WHERE id = ?`, singletonConfigID,
	).Scan(&c.Kind, &endpoint, &ekm, &c.KEKVersion, &createdStr, &updatedStr)
	if errors.Is(err, sql.ErrNoRows) {
		return Config{}, ErrNotConfigured
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

// PutConfig upserts the singleton KMS config.
func (s *SQLStore) PutConfig(ctx context.Context, c Config) error {
	now := time.Now().UTC().Format(time.RFC3339)
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO kms_config (id, kind, endpoint, encrypted_key_material, kek_version, created_at, updated_at)
		   VALUES (?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET
		   kind                   = excluded.kind,
		   endpoint               = excluded.endpoint,
		   encrypted_key_material = excluded.encrypted_key_material,
		   kek_version            = excluded.kek_version,
		   updated_at             = excluded.updated_at`,
		singletonConfigID, string(c.Kind), nullableKMSStr(c.Endpoint),
		c.EncryptedKeyMaterial, c.KEKVersion, now, now,
	)
	if err != nil {
		return fmt.Errorf("kms: put config: %w", err)
	}
	return nil
}

// DeleteConfig removes the KMS config and all DEKs.
func (s *SQLStore) DeleteConfig(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.db.ExecContext(ctx, `DELETE FROM kms_deks`); err != nil {
		return fmt.Errorf("kms: delete deks: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM kms_config WHERE id = ?`, singletonConfigID); err != nil {
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
		`INSERT INTO kms_deks (id, object_ref, wrapped_dek, kek_version, revoked, created_at)
		   VALUES (?, ?, ?, ?, 0, ?)`,
		d.ID, d.ObjectRef, d.WrappedDEK, d.KEKVersion, now,
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
		`SELECT id, object_ref, wrapped_dek, kek_version, revoked, created_at
		   FROM kms_deks WHERE id = ?`, id,
	).Scan(&d.ID, &d.ObjectRef, &d.WrappedDEK, &d.KEKVersion, &revokedInt, &createdStr)
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

// GetDEKByRef returns the most-recent non-revoked DEKRecord for objectRef.
func (s *SQLStore) GetDEKByRef(ctx context.Context, objectRef string) (DEKRecord, error) {
	var (
		d          DEKRecord
		revokedInt int
		createdStr string
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT id, object_ref, wrapped_dek, kek_version, revoked, created_at
		   FROM kms_deks
		  WHERE object_ref = ? AND revoked = 0
		  ORDER BY created_at DESC
		  LIMIT 1`, objectRef,
	).Scan(&d.ID, &d.ObjectRef, &d.WrappedDEK, &d.KEKVersion, &revokedInt, &createdStr)
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

// ListDEKs returns all DEKRecord rows, newest first.
func (s *SQLStore) ListDEKs(ctx context.Context, includeRevoked bool) ([]DEKRecord, error) {
	q := `SELECT id, object_ref, wrapped_dek, kek_version, revoked, created_at FROM kms_deks`
	if !includeRevoked {
		q += ` WHERE revoked = 0`
	}
	q += ` ORDER BY created_at DESC`
	rows, err := s.db.QueryContext(ctx, q)
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
		if err := rows.Scan(&d.ID, &d.ObjectRef, &d.WrappedDEK, &d.KEKVersion, &revokedInt, &createdStr); err != nil {
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
		`UPDATE kms_deks SET wrapped_dek = ?, kek_version = ? WHERE id = ? AND revoked = 0`,
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
	res, err := s.db.ExecContext(ctx, `UPDATE kms_deks SET revoked = 1 WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("kms: revoke dek: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrUnknownKey
	}
	return nil
}

// nullableKMSStr returns a sql.NullString for the given string.
func nullableKMSStr(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}
