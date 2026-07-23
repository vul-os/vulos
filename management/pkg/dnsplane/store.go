// store.go — dnsplane Store implementations (SQL + Mem).
//
// SQLStore is the production cpdb-backed Store (SQLite or Postgres via cpdb).
// MemStore is the in-memory reference implementation; it DEFINES semantics.
package dnsplane

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

// -----------------------------------------------------------------------
// SQLStore
// -----------------------------------------------------------------------

// SQLStore is the production cpdb-backed dnsplane Store.
type SQLStore struct {
	db *cpdb.DB
	mu sync.Mutex // single writer
}

// Open applies migrations to db and returns a ready *SQLStore.
//
// db should be obtained from cpdb.Open("dnsplane") for production, or from
// cpdb.OpenSQLiteDSN(":memory:") for tests.
func Open(db *cpdb.DB) (*SQLStore, error) {
	subFS, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("dnsplane: embed sub: %w", err)
	}
	if err := db.Migrate(subFS); err != nil {
		return nil, fmt.Errorf("dnsplane: migrate: %w", err)
	}
	return &SQLStore{db: db}, nil
}

// Close closes the underlying database connection.
func (s *SQLStore) Close() error { return s.db.Close() }

// Upsert inserts or replaces a record by (fqdn, type, value).
func (s *SQLStore) Upsert(ctx context.Context, r Record) (Record, error) {
	if !validFQDN(r.FQDN) {
		return Record{}, ErrInvalidFQDN
	}
	if !validType(r.Type) {
		return Record{}, ErrInvalidType
	}
	if r.TTL <= 0 {
		r.TTL = 60
	}
	now := time.Now().UTC().Format(time.RFC3339)
	s.mu.Lock()
	res, err := s.db.ExecContext(ctx, s.db.Rebind(`
		INSERT INTO dnsplane_records
		  (fqdn, type, value, ttl, provider_id, ulid, state, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, 'desired', ?)
		ON CONFLICT(fqdn, type, value) DO UPDATE SET
		  ttl        = excluded.ttl,
		  provider_id = COALESCE(dnsplane_records.provider_id, excluded.provider_id),
		  ulid       = excluded.ulid,
		  state      = 'desired',
		  last_error = NULL,
		  updated_at = excluded.updated_at`),
		r.FQDN, strings.ToUpper(r.Type), r.Value, r.TTL,
		nullableStr(r.ProviderID), nullableStr(r.ULID), now,
	)
	s.mu.Unlock()
	if err != nil {
		return Record{}, fmt.Errorf("dnsplane: upsert: %w", err)
	}
	id, _ := res.LastInsertId()
	// Re-fetch to get actual row (ON CONFLICT may not update LastInsertId on all SQLite versions).
	return s.fetchOne(ctx, id, r.FQDN, r.Type, r.Value)
}

func (s *SQLStore) fetchOne(ctx context.Context, id int64, fqdn, typ, val string) (Record, error) {
	// Try by ID first (insert path); fall back to (fqdn,type,value) (conflict path).
	row := s.db.QueryRowContext(ctx, s.db.Rebind(`
		SELECT id, fqdn, type, value, ttl,
		       COALESCE(provider_id,''), COALESCE(ulid,''),
		       applied_at, state, COALESCE(last_error,''), updated_at
		FROM dnsplane_records WHERE id = ?`), id)
	r, err := scanRecord(row)
	if err == nil && r.ID != 0 {
		return r, nil
	}
	row = s.db.QueryRowContext(ctx, s.db.Rebind(`
		SELECT id, fqdn, type, value, ttl,
		       COALESCE(provider_id,''), COALESCE(ulid,''),
		       applied_at, state, COALESCE(last_error,''), updated_at
		FROM dnsplane_records WHERE fqdn=? AND type=? AND value=?`),
		fqdn, strings.ToUpper(typ), val)
	return scanRecord(row)
}

// Delete removes a record by ID.
func (s *SQLStore) Delete(ctx context.Context, id int64) error {
	s.mu.Lock()
	_, err := s.db.ExecContext(ctx, s.db.Rebind(`DELETE FROM dnsplane_records WHERE id = ?`), id)
	s.mu.Unlock()
	return err
}

// ByULID returns all records for a ULID.
func (s *SQLStore) ByULID(ctx context.Context, ulid string) ([]Record, error) {
	rows, err := s.db.QueryContext(ctx, s.db.Rebind(`
		SELECT id, fqdn, type, value, ttl,
		       COALESCE(provider_id,''), COALESCE(ulid,''),
		       applied_at, state, COALESCE(last_error,''), updated_at
		FROM dnsplane_records WHERE ulid = ?
		ORDER BY id`), ulid)
	if err != nil {
		return nil, fmt.Errorf("dnsplane: by_ulid: %w", err)
	}
	defer rows.Close()
	return scanRecords(rows)
}

// DesiredUnapplied returns up to limit records in state "desired".
func (s *SQLStore) DesiredUnapplied(ctx context.Context, limit int) ([]Record, error) {
	rows, err := s.db.QueryContext(ctx, s.db.Rebind(`
		SELECT id, fqdn, type, value, ttl,
		       COALESCE(provider_id,''), COALESCE(ulid,''),
		       applied_at, state, COALESCE(last_error,''), updated_at
		FROM dnsplane_records WHERE state = 'desired'
		ORDER BY id LIMIT ?`), limit)
	if err != nil {
		return nil, fmt.Errorf("dnsplane: desired_unapplied: %w", err)
	}
	defer rows.Close()
	return scanRecords(rows)
}

// MarkApplied marks a record applied with its provider-assigned ID.
func (s *SQLStore) MarkApplied(ctx context.Context, id int64, providerID string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	s.mu.Lock()
	_, err := s.db.ExecContext(ctx, s.db.Rebind(`
		UPDATE dnsplane_records
		SET state='applied', provider_id=?, applied_at=?, last_error=NULL, updated_at=?
		WHERE id=?`), providerID, now, now, id)
	s.mu.Unlock()
	return err
}

// MarkFailed marks a record as failed with an error message.
func (s *SQLStore) MarkFailed(ctx context.Context, id int64, errMsg string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	s.mu.Lock()
	_, err := s.db.ExecContext(ctx, s.db.Rebind(`
		UPDATE dnsplane_records
		SET state='failed', last_error=?, updated_at=?
		WHERE id=?`), errMsg, now, id)
	s.mu.Unlock()
	return err
}

// CountApplied returns the count of records in state "applied".
func (s *SQLStore) CountApplied(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM dnsplane_records WHERE state = 'applied'`).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("dnsplane: count_applied: %w", err)
	}
	return n, nil
}

// CountFailed returns the count of records in state "failed".
func (s *SQLStore) CountFailed(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM dnsplane_records WHERE state = 'failed'`).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("dnsplane: count_failed: %w", err)
	}
	return n, nil
}

// compile-time assertion
var _ Store = (*SQLStore)(nil)

// -----------------------------------------------------------------------
// MemStore (in-memory reference Store)
// -----------------------------------------------------------------------

// memRecord is the internal representation in MemStore.
type memRecord struct {
	Record
}

// MemDNSStore is the in-memory reference Store. Concurrency-safe.
// It DEFINES the semantics every other implementation must match.
type MemDNSStore struct {
	mu      sync.Mutex
	records map[int64]memRecord
	nextID  int64
}

// NewMemDNSStore returns an empty MemDNSStore.
func NewMemDNSStore() *MemDNSStore {
	return &MemDNSStore{records: map[int64]memRecord{}}
}

// Upsert inserts or replaces a record by (fqdn, type, value).
func (m *MemDNSStore) Upsert(_ context.Context, r Record) (Record, error) {
	if !validFQDN(r.FQDN) {
		return Record{}, ErrInvalidFQDN
	}
	if !validType(r.Type) {
		return Record{}, ErrInvalidType
	}
	if r.TTL <= 0 {
		r.TTL = 60
	}
	r.Type = strings.ToUpper(r.Type)
	now := time.Now().UTC()

	m.mu.Lock()
	defer m.mu.Unlock()

	// Check for existing match on (fqdn, type, value).
	for id, mr := range m.records {
		if strings.EqualFold(mr.FQDN, r.FQDN) && mr.Type == r.Type && mr.Value == r.Value {
			mr.TTL = r.TTL
			mr.ULID = r.ULID
			mr.State = "desired"
			mr.LastError = ""
			// preserve ProviderID if already set
			if mr.ProviderID == "" {
				mr.ProviderID = r.ProviderID
			}
			m.records[id] = mr
			return mr.Record, nil
		}
	}
	m.nextID++
	out := Record{
		ID:    m.nextID,
		FQDN:  r.FQDN,
		Type:  r.Type,
		Value: r.Value,
		TTL:   r.TTL,
		ULID:  r.ULID,
		State: "desired",
	}
	_ = now
	m.records[m.nextID] = memRecord{out}
	return out, nil
}

// Delete removes a record by ID.
func (m *MemDNSStore) Delete(_ context.Context, id int64) error {
	m.mu.Lock()
	delete(m.records, id)
	m.mu.Unlock()
	return nil
}

// ByULID returns all records for a ULID.
func (m *MemDNSStore) ByULID(_ context.Context, ulid string) ([]Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Record
	for _, mr := range m.records {
		if mr.ULID == ulid {
			out = append(out, mr.Record)
		}
	}
	sortByID(out)
	return out, nil
}

// DesiredUnapplied returns up to limit records in state "desired".
func (m *MemDNSStore) DesiredUnapplied(_ context.Context, limit int) ([]Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Record
	for _, mr := range m.records {
		if mr.State == "desired" {
			out = append(out, mr.Record)
		}
	}
	sortByID(out)
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// MarkApplied marks a record as applied.
func (m *MemDNSStore) MarkApplied(_ context.Context, id int64, providerID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	mr, ok := m.records[id]
	if !ok {
		return nil
	}
	now := time.Now().UTC()
	mr.State = "applied"
	mr.ProviderID = providerID
	mr.AppliedAt = &now
	mr.LastError = ""
	m.records[id] = mr
	return nil
}

// MarkFailed marks a record as failed.
func (m *MemDNSStore) MarkFailed(_ context.Context, id int64, errMsg string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	mr, ok := m.records[id]
	if !ok {
		return nil
	}
	mr.State = "failed"
	mr.LastError = errMsg
	m.records[id] = mr
	return nil
}

// CountApplied returns the count of records in state "applied".
func (m *MemDNSStore) CountApplied(_ context.Context) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var n int
	for _, mr := range m.records {
		if mr.State == "applied" {
			n++
		}
	}
	return n, nil
}

// CountFailed returns the count of records in state "failed".
func (m *MemDNSStore) CountFailed(_ context.Context) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var n int
	for _, mr := range m.records {
		if mr.State == "failed" {
			n++
		}
	}
	return n, nil
}

// Close is a no-op for MemDNSStore.
func (m *MemDNSStore) Close() error { return nil }

// compile-time assertion
var _ Store = (*MemDNSStore)(nil)

// -----------------------------------------------------------------------
// Shared helpers
// -----------------------------------------------------------------------

func nullableStr(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

func scanRecord(row *sql.Row) (Record, error) {
	var (
		r         Record
		appliedAt sql.NullString
	)
	err := row.Scan(
		&r.ID, &r.FQDN, &r.Type, &r.Value, &r.TTL,
		&r.ProviderID, &r.ULID, &appliedAt, &r.State, &r.LastError,
		new(string), // updated_at, not mapped to struct
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Record{}, nil
	}
	if err != nil {
		return Record{}, fmt.Errorf("dnsplane: scan: %w", err)
	}
	if appliedAt.Valid && appliedAt.String != "" {
		t, _ := time.Parse(time.RFC3339, appliedAt.String)
		r.AppliedAt = &t
	}
	return r, nil
}

func scanRecords(rows *sql.Rows) ([]Record, error) {
	var out []Record
	for rows.Next() {
		var (
			r         Record
			appliedAt sql.NullString
			updatedAt string
		)
		if err := rows.Scan(
			&r.ID, &r.FQDN, &r.Type, &r.Value, &r.TTL,
			&r.ProviderID, &r.ULID, &appliedAt, &r.State, &r.LastError,
			&updatedAt,
		); err != nil {
			return nil, fmt.Errorf("dnsplane: scan rows: %w", err)
		}
		if appliedAt.Valid && appliedAt.String != "" {
			t, _ := time.Parse(time.RFC3339, appliedAt.String)
			r.AppliedAt = &t
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func sortByID(recs []Record) {
	for i := 1; i < len(recs); i++ {
		for j := i; j > 0 && recs[j].ID < recs[j-1].ID; j-- {
			recs[j], recs[j-1] = recs[j-1], recs[j]
		}
	}
}
