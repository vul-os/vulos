// Package integrations is the control-plane OAuth broker for the Vulos fleet.
//
// INTEG-01. The CP holds the single registered Google client_id/secret and a
// stable redirect_uri, performs the authorization-code exchange, and custodies
// the long-lived refresh_token ENCRYPTED at rest (crypto.go). Vulos OS boxes
// never see the refresh_token or the client secret: they mint short-lived
// access tokens over the existing authenticated CP↔box channel (Wave 2,
// routes_integrations.go GET /api/integrations/{provider}/token).
//
// This is a connected-account data integration (Gmail/Drive/Calendar + browser
// session seeding), not an identity provider — see the migration header and
// AUTH-02.
package integrations

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"sync"
	"time"

	"github.com/vul-os/vulos-management/pkg/cpdb"
)

// ErrNotConnected is returned when no connection exists for (account, provider).
var ErrNotConnected = errors.New("integrations: not connected")

// Connection is a stored third-party OAuth connection for one account+provider.
// The token fields hold ENCRYPTED ciphertext (AES-256-GCM, base64); callers use
// the Service to encrypt/decrypt, never touching these fields directly.
type Connection struct {
	AccountID       string
	Provider        string
	RefreshTokenEnc string
	AccessTokenEnc  string
	AccessExpiry    time.Time // zero = no cached access token
	Scopes          string    // space-separated granted scopes
	AccountEmail    string    // display only
	AccountSub      string    // display only, NOT a login identity
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// Store persists integration connections. SQLStore is the production impl;
// MemStore is the test double. Both are semantically identical.
type Store interface {
	// Upsert stores (or replaces) a connection's refresh token, scopes, and
	// connected-account metadata. CreatedAt is preserved across re-connects.
	Upsert(ctx context.Context, c Connection) error
	// Get returns the connection for (accountID, provider) or ErrNotConnected.
	Get(ctx context.Context, accountID, provider string) (Connection, error)
	// SetAccessToken caches a freshly-minted access token (encrypted) + expiry.
	SetAccessToken(ctx context.Context, accountID, provider, accessTokenEnc string, expiry time.Time) error
	// SetRefreshToken replaces the refresh token (Google occasionally rotates it).
	SetRefreshToken(ctx context.Context, accountID, provider, refreshTokenEnc string) error
	// Delete removes a connection (disconnect). No error if absent.
	Delete(ctx context.Context, accountID, provider string) error
	// List returns all connections for an account (connected-accounts UI).
	List(ctx context.Context, accountID string) ([]Connection, error)
	Close() error
}

// ---------------------------------------------------------------------------
// SQLStore
// ---------------------------------------------------------------------------

// SQLStore is the cpdb-backed Store. Concurrency-safe: WAL (SQLite) or pool
// (Postgres) + mutex for write serialisation.
type SQLStore struct {
	db *cpdb.DB
	mu sync.Mutex
}

// Open applies migrations to db and returns a ready SQLStore.
//
// db should be obtained from cpdb.Open("integrations") for production, or from
// cpdb.OpenSQLiteDSN(":memory:") for tests. Migrations are applied
// automatically and are idempotent (safe to call on every startup).
func Open(db *cpdb.DB) (*SQLStore, error) {
	subFS, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("integrations: embed sub: %w", err)
	}
	if err := db.Migrate(subFS); err != nil {
		return nil, fmt.Errorf("integrations: migrate: %w", err)
	}
	return &SQLStore{db: db}, nil
}

func (s *SQLStore) Close() error { return s.db.Close() }

// DeviceKeys returns a DeviceKeyStore backed by the same database (table
// device_pubkeys). INTEG-SEC-01.
func (s *SQLStore) DeviceKeys() *SQLDeviceKeyStore { return &SQLDeviceKeyStore{db: s.db} }

func (s *SQLStore) Upsert(ctx context.Context, c Connection) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().Unix()
	// ON CONFLICT preserves created_at; everything else is replaced.
	_, err := s.db.ExecContext(ctx, s.db.Rebind(`
		INSERT INTO integration_connections
		    (account_id, provider, refresh_token_enc, access_token_enc, access_expiry,
		     scopes, account_email, account_sub, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(account_id, provider) DO UPDATE SET
		    refresh_token_enc = excluded.refresh_token_enc,
		    access_token_enc  = excluded.access_token_enc,
		    access_expiry     = excluded.access_expiry,
		    scopes            = excluded.scopes,
		    account_email     = excluded.account_email,
		    account_sub       = excluded.account_sub,
		    updated_at        = excluded.updated_at`),
		c.AccountID, c.Provider, c.RefreshTokenEnc, c.AccessTokenEnc, unixOrZero(c.AccessExpiry),
		c.Scopes, c.AccountEmail, c.AccountSub, now, now)
	if err != nil {
		return fmt.Errorf("integrations: upsert: %w", err)
	}
	return nil
}

func (s *SQLStore) Get(ctx context.Context, accountID, provider string) (Connection, error) {
	row := s.db.QueryRowContext(ctx, s.db.Rebind(`
		SELECT account_id, provider, refresh_token_enc, access_token_enc, access_expiry,
		       scopes, account_email, account_sub, created_at, updated_at
		FROM integration_connections WHERE account_id = ? AND provider = ?`),
		accountID, provider)
	return scanConnection(row)
}

func (s *SQLStore) SetAccessToken(ctx context.Context, accountID, provider, accessTokenEnc string, expiry time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.db.ExecContext(ctx, s.db.Rebind(`
		UPDATE integration_connections
		SET access_token_enc = ?, access_expiry = ?, updated_at = ?
		WHERE account_id = ? AND provider = ?`),
		accessTokenEnc, unixOrZero(expiry), time.Now().Unix(), accountID, provider)
	if err != nil {
		return fmt.Errorf("integrations: set access token: %w", err)
	}
	return affectedOrNotConnected(res)
}

func (s *SQLStore) SetRefreshToken(ctx context.Context, accountID, provider, refreshTokenEnc string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.db.ExecContext(ctx, s.db.Rebind(`
		UPDATE integration_connections
		SET refresh_token_enc = ?, updated_at = ?
		WHERE account_id = ? AND provider = ?`),
		refreshTokenEnc, time.Now().Unix(), accountID, provider)
	if err != nil {
		return fmt.Errorf("integrations: set refresh token: %w", err)
	}
	return affectedOrNotConnected(res)
}

func (s *SQLStore) Delete(ctx context.Context, accountID, provider string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx, s.db.Rebind(
		`DELETE FROM integration_connections WHERE account_id = ? AND provider = ?`),
		accountID, provider)
	if err != nil {
		return fmt.Errorf("integrations: delete: %w", err)
	}
	return nil
}

func (s *SQLStore) List(ctx context.Context, accountID string) ([]Connection, error) {
	rows, err := s.db.QueryContext(ctx, s.db.Rebind(`
		SELECT account_id, provider, refresh_token_enc, access_token_enc, access_expiry,
		       scopes, account_email, account_sub, created_at, updated_at
		FROM integration_connections WHERE account_id = ? ORDER BY provider`), accountID)
	if err != nil {
		return nil, fmt.Errorf("integrations: list: %w", err)
	}
	defer rows.Close()
	var out []Connection
	for rows.Next() {
		c, err := scanConnection(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// scanner is satisfied by *sql.Row and *sql.Rows.
type scanner interface{ Scan(dest ...any) error }

func scanConnection(sc scanner) (Connection, error) {
	var c Connection
	var accessExpiry, createdAt, updatedAt int64
	err := sc.Scan(&c.AccountID, &c.Provider, &c.RefreshTokenEnc, &c.AccessTokenEnc, &accessExpiry,
		&c.Scopes, &c.AccountEmail, &c.AccountSub, &createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Connection{}, ErrNotConnected
	}
	if err != nil {
		return Connection{}, fmt.Errorf("integrations: scan: %w", err)
	}
	c.AccessExpiry = timeOrZero(accessExpiry)
	c.CreatedAt = time.Unix(createdAt, 0).UTC()
	c.UpdatedAt = time.Unix(updatedAt, 0).UTC()
	return c, nil
}

func affectedOrNotConnected(res sql.Result) error {
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotConnected
	}
	return nil
}

func unixOrZero(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

func timeOrZero(u int64) time.Time {
	if u == 0 {
		return time.Time{}
	}
	return time.Unix(u, 0).UTC()
}
