// store.go — keydir SQLite-backed Store (modernc.org/sqlite, pure-Go, no CGO).
package keydir

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"sync"
	"time"

	"github.com/vul-os/vulos-management/pkg/cpdb"
)

// ---------------------------------------------------------------------------
// SQLStore
// ---------------------------------------------------------------------------

// SQLStore is the cpdb-backed keydir Store (SQLite or Postgres).
type SQLStore struct {
	db *cpdb.DB
	mu sync.Mutex // serialise writes
}

// OpenSQLStore applies schema migrations to db and returns a ready *SQLStore.
func OpenSQLStore(db *cpdb.DB) (*SQLStore, error) {
	subFS, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("keydir: embed sub: %w", err)
	}
	if err := db.Migrate(subFS); err != nil {
		return nil, fmt.Errorf("keydir: migrate: %w", err)
	}
	return &SQLStore{db: db}, nil
}

// Close closes the underlying database connection.
func (s *SQLStore) Close() error { return s.db.Close() }

// Upsert creates or updates the key-directory entry for an account.
func (s *SQLStore) Upsert(ctx context.Context, e Entry) (Entry, error) {
	if e.AccountID == "" || e.VumailAddress == "" || e.PublicKeyB64 == "" {
		return Entry{}, ErrInvalidInput
	}
	e.VumailAddress = strings.ToLower(strings.TrimSpace(e.VumailAddress))
	if err := ValidateVumailAddress(e.VumailAddress); err != nil {
		return Entry{}, err
	}
	if e.State == "" {
		e.State = "active"
	}
	discInt := 0
	if e.Discoverable {
		discInt = 1
	}
	now := time.Now().UTC().Format(time.RFC3339)

	s.mu.Lock()
	res, err := s.db.ExecContext(ctx, s.db.Rebind(`
		INSERT INTO keydir_entries
		  (account_id, vumail_address, public_key_b64, peer_endpoint,
		   discoverable, state, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(account_id) DO UPDATE SET
		  vumail_address = excluded.vumail_address,
		  public_key_b64 = excluded.public_key_b64,
		  peer_endpoint  = excluded.peer_endpoint,
		  discoverable   = excluded.discoverable,
		  state          = excluded.state,
		  updated_at     = excluded.updated_at`),
		e.AccountID, e.VumailAddress, e.PublicKeyB64, e.PeerEndpoint,
		discInt, e.State, now, now,
	)
	s.mu.Unlock()
	if err != nil {
		// Handle UNIQUE constraint on vumail_address for both SQLite and Postgres.
		errStr := err.Error()
		if strings.Contains(errStr, "UNIQUE constraint failed: keydir_entries.vumail_address") ||
			strings.Contains(errStr, "unique constraint") || strings.Contains(errStr, "duplicate key") {
			return Entry{}, ErrDuplicate
		}
		return Entry{}, fmt.Errorf("keydir: upsert: %w", err)
	}
	id, _ := res.LastInsertId()
	return s.fetchByRowID(ctx, id, e.AccountID)
}

// GetByAddress returns the active entry for the given Vulos identity address.
func (s *SQLStore) GetByAddress(ctx context.Context, address string) (Entry, error) {
	address = strings.ToLower(strings.TrimSpace(address))
	row := s.db.QueryRowContext(ctx, s.db.Rebind(`
		SELECT id, account_id, vumail_address, public_key_b64, peer_endpoint,
		       discoverable, state, created_at, updated_at
		FROM keydir_entries
		WHERE vumail_address = ? AND state = 'active'`), address)
	e, err := scanEntry(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Entry{}, ErrNotFound
	}
	return e, err
}

// GetByAccount returns the entry for the given account_id.
func (s *SQLStore) GetByAccount(ctx context.Context, accountID string) (Entry, error) {
	row := s.db.QueryRowContext(ctx, s.db.Rebind(`
		SELECT id, account_id, vumail_address, public_key_b64, peer_endpoint,
		       discoverable, state, created_at, updated_at
		FROM keydir_entries
		WHERE account_id = ?`), accountID)
	e, err := scanEntry(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Entry{}, ErrNotFound
	}
	return e, err
}

// Revoke marks the entry for accountID as revoked.
func (s *SQLStore) Revoke(ctx context.Context, accountID string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	s.mu.Lock()
	_, err := s.db.ExecContext(ctx,
		s.db.Rebind(`UPDATE keydir_entries SET state = 'revoked', updated_at = ? WHERE account_id = ?`),
		now, accountID)
	s.mu.Unlock()
	return err
}

// SetDiscoverable toggles the discoverable flag for an account's entry.
func (s *SQLStore) SetDiscoverable(ctx context.Context, accountID string, discoverable bool) error {
	discInt := 0
	if discoverable {
		discInt = 1
	}
	now := time.Now().UTC().Format(time.RFC3339)
	s.mu.Lock()
	res, err := s.db.ExecContext(ctx,
		s.db.Rebind(`UPDATE keydir_entries SET discoverable = ?, updated_at = ? WHERE account_id = ?`),
		discInt, now, accountID)
	s.mu.Unlock()
	if err != nil {
		return fmt.Errorf("keydir: set discoverable: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// compile-time assertion: SQLStore satisfies Store.
var _ Store = (*SQLStore)(nil)

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

func (s *SQLStore) fetchByRowID(ctx context.Context, rowID int64, accountID string) (Entry, error) {
	row := s.db.QueryRowContext(ctx, s.db.Rebind(`
		SELECT id, account_id, vumail_address, public_key_b64, peer_endpoint,
		       discoverable, state, created_at, updated_at
		FROM keydir_entries WHERE id = ?`), rowID)
	e, err := scanEntry(row)
	if errors.Is(err, sql.ErrNoRows) {
		// ON CONFLICT path: rowID is the existing row's ID; fetch by account.
		row = s.db.QueryRowContext(ctx, s.db.Rebind(`
			SELECT id, account_id, vumail_address, public_key_b64, peer_endpoint,
			       discoverable, state, created_at, updated_at
			FROM keydir_entries WHERE account_id = ?`), accountID)
		return scanEntry(row)
	}
	return e, err
}

func scanEntry(row *sql.Row) (Entry, error) {
	var (
		e            Entry
		discInt      int
		createdAtStr string
		updatedAtStr string
	)
	err := row.Scan(
		&e.ID, &e.AccountID, &e.VumailAddress, &e.PublicKeyB64, &e.PeerEndpoint,
		&discInt, &e.State, &createdAtStr, &updatedAtStr,
	)
	if err != nil {
		return Entry{}, err
	}
	e.Discoverable = discInt == 1
	if t, terr := time.Parse(time.RFC3339, createdAtStr); terr == nil {
		e.CreatedAt = t
	}
	if t, terr := time.Parse(time.RFC3339, updatedAtStr); terr == nil {
		e.UpdatedAt = t
	}
	return e, nil
}
