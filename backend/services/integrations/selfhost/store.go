package selfhost

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver (no CGo — matches services/store)
)

// Connection is one owner-account ↔ provider link. The refresh token and cached
// access token are stored ENCRYPTED (base64 AES-GCM ciphertext) — the plaintext
// never touches disk and is NEVER returned in an API response.
type Connection struct {
	UserID          string
	Provider        string
	RefreshTokenEnc string // base64(nonce||ct||tag) — encrypted at rest
	AccessTokenEnc  string // cached short-lived access token, encrypted at rest
	AccessExpiry    time.Time
	Scopes          string
	AccountEmail    string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// Store is the per-user connection store. Every method is USER-SCOPED: the SQL is
// filtered by user_id so one user can never read or mutate another user's
// connection (the store is the isolation boundary — proven by the per-user test).
type Store struct {
	db *sql.DB
}

// OpenStore opens (or creates) the self-host integrations SQLite store at
// <dbDir>/integrations_selfhost.db. If dbDir is empty an in-memory store is used
// (tests). WAL + busy-timeout, idempotent schema — same as the OS's other stores.
func OpenStore(dbDir string) (*Store, error) {
	dsn := "file::memory:?cache=shared&_pragma=busy_timeout(5000)"
	if dbDir != "" {
		path := filepath.Join(dbDir, "integrations_selfhost.db")
		dsn = "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("selfhost: open db: %w", err)
	}
	db.SetMaxOpenConns(1) // single-writer file; serialize writers
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) migrate() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS integrations_conn (
  user_id            TEXT NOT NULL,
  provider           TEXT NOT NULL,
  refresh_token_enc  TEXT NOT NULL,
  access_token_enc   TEXT NOT NULL DEFAULT '',
  access_expiry      INTEGER NOT NULL DEFAULT 0,
  scopes             TEXT NOT NULL DEFAULT '',
  account_email      TEXT NOT NULL DEFAULT '',
  created_at         INTEGER NOT NULL,
  updated_at         INTEGER NOT NULL,
  PRIMARY KEY (user_id, provider)
);`)
	if err != nil {
		return fmt.Errorf("selfhost: migrate: %w", err)
	}
	return nil
}

// Close closes the underlying DB.
func (s *Store) Close() error { return s.db.Close() }

// ErrNotFound is returned when no connection exists for (userID, provider).
var ErrNotFound = errors.New("selfhost: connection not found")

// Upsert inserts or replaces the connection for (userID, provider). It preserves
// created_at on replace.
func (s *Store) Upsert(ctx context.Context, c Connection) error {
	now := time.Now().UTC().Unix()
	_, err := s.db.ExecContext(ctx, `
INSERT INTO integrations_conn
  (user_id, provider, refresh_token_enc, access_token_enc, access_expiry, scopes, account_email, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(user_id, provider) DO UPDATE SET
  refresh_token_enc = excluded.refresh_token_enc,
  access_token_enc  = excluded.access_token_enc,
  access_expiry     = excluded.access_expiry,
  scopes            = excluded.scopes,
  account_email     = excluded.account_email,
  updated_at        = excluded.updated_at`,
		c.UserID, c.Provider, c.RefreshTokenEnc, c.AccessTokenEnc,
		c.AccessExpiry.UTC().Unix(), c.Scopes, c.AccountEmail, now, now)
	if err != nil {
		return fmt.Errorf("selfhost: upsert: %w", err)
	}
	return nil
}

// Get returns the connection for (userID, provider) or ErrNotFound.
func (s *Store) Get(ctx context.Context, userID, provider string) (Connection, error) {
	var c Connection
	var accessExpiry, createdAt, updatedAt int64
	err := s.db.QueryRowContext(ctx, `
SELECT user_id, provider, refresh_token_enc, access_token_enc, access_expiry, scopes, account_email, created_at, updated_at
FROM integrations_conn WHERE user_id = ? AND provider = ?`, userID, provider).
		Scan(&c.UserID, &c.Provider, &c.RefreshTokenEnc, &c.AccessTokenEnc,
			&accessExpiry, &c.Scopes, &c.AccountEmail, &createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Connection{}, ErrNotFound
	}
	if err != nil {
		return Connection{}, fmt.Errorf("selfhost: get: %w", err)
	}
	c.AccessExpiry = time.Unix(accessExpiry, 0).UTC()
	c.CreatedAt = time.Unix(createdAt, 0).UTC()
	c.UpdatedAt = time.Unix(updatedAt, 0).UTC()
	return c, nil
}

// SetAccessToken updates only the cached access token + expiry for a connection.
func (s *Store) SetAccessToken(ctx context.Context, userID, provider, atEnc string, expiry time.Time) error {
	_, err := s.db.ExecContext(ctx, `
UPDATE integrations_conn SET access_token_enc = ?, access_expiry = ?, updated_at = ?
WHERE user_id = ? AND provider = ?`,
		atEnc, expiry.UTC().Unix(), time.Now().UTC().Unix(), userID, provider)
	if err != nil {
		return fmt.Errorf("selfhost: set access token: %w", err)
	}
	return nil
}

// SetRefreshToken updates only the refresh token (provider rotated it).
func (s *Store) SetRefreshToken(ctx context.Context, userID, provider, rtEnc string) error {
	_, err := s.db.ExecContext(ctx, `
UPDATE integrations_conn SET refresh_token_enc = ?, updated_at = ?
WHERE user_id = ? AND provider = ?`,
		rtEnc, time.Now().UTC().Unix(), userID, provider)
	if err != nil {
		return fmt.Errorf("selfhost: set refresh token: %w", err)
	}
	return nil
}

// List returns all of a user's connections (connected-accounts UI).
func (s *Store) List(ctx context.Context, userID string) ([]Connection, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT user_id, provider, refresh_token_enc, access_token_enc, access_expiry, scopes, account_email, created_at, updated_at
FROM integrations_conn WHERE user_id = ? ORDER BY provider`, userID)
	if err != nil {
		return nil, fmt.Errorf("selfhost: list: %w", err)
	}
	defer rows.Close()
	var out []Connection
	for rows.Next() {
		var c Connection
		var accessExpiry, createdAt, updatedAt int64
		if err := rows.Scan(&c.UserID, &c.Provider, &c.RefreshTokenEnc, &c.AccessTokenEnc,
			&accessExpiry, &c.Scopes, &c.AccountEmail, &createdAt, &updatedAt); err != nil {
			return nil, fmt.Errorf("selfhost: list scan: %w", err)
		}
		c.AccessExpiry = time.Unix(accessExpiry, 0).UTC()
		c.CreatedAt = time.Unix(createdAt, 0).UTC()
		c.UpdatedAt = time.Unix(updatedAt, 0).UTC()
		out = append(out, c)
	}
	return out, rows.Err()
}

// Delete removes a user's connection for a provider (disconnect).
func (s *Store) Delete(ctx context.Context, userID, provider string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM integrations_conn WHERE user_id = ? AND provider = ?`, userID, provider)
	if err != nil {
		return fmt.Errorf("selfhost: delete: %w", err)
	}
	return nil
}
