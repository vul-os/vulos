// store.go — cloud-home identity persistence (cpdb: SQLite or Postgres) plus an
// in-memory reference store for tests.
package cloudhome

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

// ─────────────────────────────────────────────────────────────────────────────
// SQLStore
// ─────────────────────────────────────────────────────────────────────────────

// SQLStore is the cpdb-backed Store (SQLite for self-host, Postgres for cloud).
type SQLStore struct {
	db *cpdb.DB
	mu sync.Mutex // serialise writes (SQLite single-writer)
}

// OpenSQLStore applies migrations to db and returns a ready *SQLStore.
func OpenSQLStore(db *cpdb.DB) (*SQLStore, error) {
	subFS, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("cloudhome: embed sub: %w", err)
	}
	if err := db.Migrate(subFS); err != nil {
		return nil, fmt.Errorf("cloudhome: migrate: %w", err)
	}
	return &SQLStore{db: db}, nil
}

// Close closes the underlying database connection.
func (s *SQLStore) Close() error { return s.db.Close() }

// Insert persists a new record. Returns ErrExists on a unique-constraint
// violation (account_id PK or vula_id UNIQUE).
func (s *SQLStore) Insert(ctx context.Context, r record) error {
	if r.AccountID == "" || r.VulaID == "" || r.EncPrivKey == "" {
		return ErrInvalidInput
	}
	if r.KEKVersion < 1 {
		r.KEKVersion = 1
	}
	s.mu.Lock()
	_, err := s.db.ExecContext(ctx, s.db.Rebind(`
		INSERT INTO cloudhome_identities
		  (account_id, vula_id, public_key_b64, enc_priv_key, kek_version, server, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`),
		r.AccountID, r.VulaID, r.PublicKeyB64, r.EncPrivKey, r.KEKVersion, r.Server,
		r.CreatedAt.UTC().Format(time.RFC3339),
	)
	s.mu.Unlock()
	if err != nil {
		if cpdb.IsUniqueViolation(err) {
			return ErrExists
		}
		return fmt.Errorf("cloudhome: insert: %w", err)
	}
	return nil
}

// GetByAccount returns the record for accountID, or ErrNotFound.
func (s *SQLStore) GetByAccount(ctx context.Context, accountID string) (record, error) {
	row := s.db.QueryRowContext(ctx, s.db.Rebind(`
		SELECT account_id, vula_id, public_key_b64, enc_priv_key, kek_version, server, created_at
		FROM cloudhome_identities WHERE account_id = ?`), accountID)
	return scanRecord(row)
}

// GetByVulaID returns the record for vulaID, or ErrNotFound.
func (s *SQLStore) GetByVulaID(ctx context.Context, vulaID string) (record, error) {
	row := s.db.QueryRowContext(ctx, s.db.Rebind(`
		SELECT account_id, vula_id, public_key_b64, enc_priv_key, kek_version, server, created_at
		FROM cloudhome_identities WHERE vula_id = ?`), vulaID)
	return scanRecord(row)
}

// ListAll returns every cloud-home record (KEK rotation iterates this).
func (s *SQLStore) ListAll(ctx context.Context) ([]record, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT account_id, vula_id, public_key_b64, enc_priv_key, kek_version, server, created_at
		FROM cloudhome_identities`)
	if err != nil {
		return nil, fmt.Errorf("cloudhome: list all: %w", err)
	}
	defer rows.Close()
	var out []record
	for rows.Next() {
		r, serr := scanRecordRows(rows)
		if serr != nil {
			return nil, serr
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// UpdateEncKey re-seals one record's encrypted private key under a new KEK
// version. Only enc_priv_key + kek_version change.
func (s *SQLStore) UpdateEncKey(ctx context.Context, accountID, encPrivKey string, kekVersion int) error {
	if accountID == "" || encPrivKey == "" || kekVersion < 1 {
		return ErrInvalidInput
	}
	s.mu.Lock()
	res, err := s.db.ExecContext(ctx, s.db.Rebind(`
		UPDATE cloudhome_identities SET enc_priv_key = ?, kek_version = ? WHERE account_id = ?`),
		encPrivKey, kekVersion, accountID)
	s.mu.Unlock()
	if err != nil {
		return fmt.Errorf("cloudhome: update enc key: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func scanRecord(row *sql.Row) (record, error) {
	var (
		r            record
		createdAtStr string
	)
	err := row.Scan(&r.AccountID, &r.VulaID, &r.PublicKeyB64, &r.EncPrivKey, &r.KEKVersion, &r.Server, &createdAtStr)
	if errors.Is(err, sql.ErrNoRows) {
		return record{}, ErrNotFound
	}
	if err != nil {
		return record{}, fmt.Errorf("cloudhome: scan: %w", err)
	}
	if t, terr := time.Parse(time.RFC3339, createdAtStr); terr == nil {
		r.CreatedAt = t
	}
	return r, nil
}

func scanRecordRows(rows *sql.Rows) (record, error) {
	var (
		r            record
		createdAtStr string
	)
	if err := rows.Scan(&r.AccountID, &r.VulaID, &r.PublicKeyB64, &r.EncPrivKey, &r.KEKVersion, &r.Server, &createdAtStr); err != nil {
		return record{}, fmt.Errorf("cloudhome: scan: %w", err)
	}
	if t, terr := time.Parse(time.RFC3339, createdAtStr); terr == nil {
		r.CreatedAt = t
	}
	return r, nil
}

func (s *SQLStore) PutRevocation(ctx context.Context, vulaID, accountID, certJSON string, revokedAt time.Time) error {
	if vulaID == "" || certJSON == "" {
		return ErrInvalidInput
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// Upsert: dialect-portable two-step (DELETE then INSERT) under the write lock.
	if _, err := s.db.ExecContext(ctx, s.db.Rebind(`
		DELETE FROM cloudhome_revocations WHERE vula_id = ?`), vulaID); err != nil {
		return fmt.Errorf("cloudhome: revocation clear: %w", err)
	}
	_, err := s.db.ExecContext(ctx, s.db.Rebind(`
		INSERT INTO cloudhome_revocations (vula_id, account_id, cert_json, revoked_at)
		VALUES (?, ?, ?, ?)`),
		vulaID, accountID, certJSON, revokedAt.UTC().Format(time.RFC3339))
	if err != nil {
		return fmt.Errorf("cloudhome: insert revocation: %w", err)
	}
	return nil
}

func (s *SQLStore) GetRevocation(ctx context.Context, vulaID string) (string, bool, error) {
	row := s.db.QueryRowContext(ctx, s.db.Rebind(`
		SELECT cert_json FROM cloudhome_revocations WHERE vula_id = ?`), vulaID)
	var certJSON string
	err := row.Scan(&certJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("cloudhome: get revocation: %w", err)
	}
	return certJSON, true, nil
}

// compile-time assertion.
var _ Store = (*SQLStore)(nil)

// ─────────────────────────────────────────────────────────────────────────────
// MemStore — in-memory reference store (tests / degraded mode)
// ─────────────────────────────────────────────────────────────────────────────

// MemStore is a concurrency-safe in-memory Store.
type MemStore struct {
	mu      sync.Mutex
	byAcct  map[string]record
	byVula  map[string]record
	revoked map[string]string // vulaID → verbatim revocation cert JSON
}

// NewMemStore returns an empty in-memory store.
func NewMemStore() *MemStore {
	return &MemStore{
		byAcct:  map[string]record{},
		byVula:  map[string]record{},
		revoked: map[string]string{},
	}
}

func (m *MemStore) Insert(_ context.Context, r record) error {
	if r.AccountID == "" || r.VulaID == "" || r.EncPrivKey == "" {
		return ErrInvalidInput
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.byAcct[r.AccountID]; ok {
		return ErrExists
	}
	if _, ok := m.byVula[r.VulaID]; ok {
		return ErrExists
	}
	m.byAcct[r.AccountID] = r
	m.byVula[r.VulaID] = r
	return nil
}

func (m *MemStore) GetByAccount(_ context.Context, accountID string) (record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.byAcct[accountID]
	if !ok {
		return record{}, ErrNotFound
	}
	return r, nil
}

func (m *MemStore) GetByVulaID(_ context.Context, vulaID string) (record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.byVula[vulaID]
	if !ok {
		return record{}, ErrNotFound
	}
	return r, nil
}

// ListAll returns every record (KEK rotation).
func (m *MemStore) ListAll(_ context.Context) ([]record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]record, 0, len(m.byAcct))
	for _, r := range m.byAcct {
		out = append(out, r)
	}
	return out, nil
}

// UpdateEncKey re-seals one record under a new KEK version.
func (m *MemStore) UpdateEncKey(_ context.Context, accountID, encPrivKey string, kekVersion int) error {
	if accountID == "" || encPrivKey == "" || kekVersion < 1 {
		return ErrInvalidInput
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.byAcct[accountID]
	if !ok {
		return ErrNotFound
	}
	r.EncPrivKey = encPrivKey
	r.KEKVersion = kekVersion
	m.byAcct[accountID] = r
	m.byVula[r.VulaID] = r
	return nil
}

// ── MemStore lifecycle (revocation) ─────────────────────────────────────────────

func (m *MemStore) PutRevocation(_ context.Context, vulaID, _ string, certJSON string, _ time.Time) error {
	if vulaID == "" || certJSON == "" {
		return ErrInvalidInput
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.revoked[vulaID] = certJSON
	return nil
}

func (m *MemStore) GetRevocation(_ context.Context, vulaID string) (string, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.revoked[vulaID]
	return c, ok, nil
}

func (m *MemStore) Close() error { return nil }

var _ Store = (*MemStore)(nil)
