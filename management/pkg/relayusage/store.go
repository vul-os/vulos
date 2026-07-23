// Package relayusage records per-tenant relay usage (bytes relayed + TURN /
// circuit session counts) reported by the stateless relay PoPs, and exposes a
// read-only current-month aggregate for the org-admin quota tab.
//
// Wire shape: the relay PoP drains its in-memory meter channel, aggregates
// per-tenant, and flushes deltas to POST /api/relay/usage. The receiver
// (cmd/server/routes_relayusage.go) calls Add per item. The org-admin quota
// adapter reads UsageThisMonth to fill the relay-bytes-used + turn-sessions-used
// bars that previously read TODO(metric).
//
// Persistence uses the cpdb dual-backend seam (SQLite / Postgres).
// MemStore is the dep-free reference for tests / dev mode.
package relayusage

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

// Store is the per-tenant relay-usage aggregate.
type Store interface {
	Add(ctx context.Context, accountID, period string, bytes int64, sessions int) error
	UsageThisMonth(ctx context.Context, accountID string) (bytes int64, sessions int, err error)
	MarkReport(ctx context.Context, popID, reportID string) (firstSeen bool, err error)
	Close() error
}

// CurrentPeriod returns the billing-month key "YYYY-MM" for t in UTC.
func CurrentPeriod(t time.Time) string { return t.UTC().Format("2006-01") }

// ──────────────────────────────────────────────────────────────────────────
// SQLStore — cpdb-backed, dual SQLite/Postgres.
// ──────────────────────────────────────────────────────────────────────────

// SQLStore is the production store.
type SQLStore struct {
	db *cpdb.DB
	mu sync.Mutex
}

// OpenSQLStore runs the embedded migrations against db and returns a SQLStore.
func OpenSQLStore(db *cpdb.DB) (*SQLStore, error) {
	subFS, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("relayusage: embed sub: %w", err)
	}
	if err := db.Migrate(subFS); err != nil {
		return nil, fmt.Errorf("relayusage: migrate: %w", err)
	}
	return &SQLStore{db: db}, nil
}

// MarkReport implements Store.
func (s *SQLStore) MarkReport(ctx context.Context, popID, reportID string) (bool, error) {
	if popID == "" || reportID == "" {
		return false, errors.New("relayusage: pop_id and report_id required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.db.ExecContext(ctx, s.db.Rebind(`
		INSERT INTO relay_usage_reports (pop_id, report_id, seen_at)
		VALUES (?, ?, ?)
		ON CONFLICT(pop_id, report_id) DO NOTHING`),
		popID, reportID, time.Now().UTC().Format(time.RFC3339),
	)
	if err != nil {
		return false, fmt.Errorf("relayusage: mark report: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("relayusage: mark report rows: %w", err)
	}
	return n > 0, nil
}

// Close closes the underlying database.
func (s *SQLStore) Close() error { return s.db.Close() }

// Add implements Store.
func (s *SQLStore) Add(ctx context.Context, accountID, period string, bytes int64, sessions int) error {
	if accountID == "" {
		return errors.New("relayusage: account_id required")
	}
	if period == "" {
		return errors.New("relayusage: period required")
	}
	if bytes < 0 {
		bytes = 0
	}
	if sessions < 0 {
		sessions = 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx, s.db.Rebind(`
        INSERT INTO relay_usage (account_id, period, bytes, sessions, updated_at)
        VALUES (?, ?, ?, ?, ?)
        ON CONFLICT(account_id, period) DO UPDATE SET
            bytes      = relay_usage.bytes + excluded.bytes,
            sessions   = relay_usage.sessions + excluded.sessions,
            updated_at = excluded.updated_at`),
		accountID, period, bytes, sessions, time.Now().UTC().Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("relayusage: add: %w", err)
	}
	return nil
}

// UsageThisMonth implements Store.
func (s *SQLStore) UsageThisMonth(ctx context.Context, accountID string) (int64, int, error) {
	period := CurrentPeriod(time.Now())
	var bytes int64
	var sessions int
	err := s.db.QueryRowContext(ctx, s.db.Rebind(
		`SELECT bytes, sessions FROM relay_usage WHERE account_id = ? AND period = ?`),
		accountID, period,
	).Scan(&bytes, &sessions)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, 0, nil
	}
	if err != nil {
		return 0, 0, fmt.Errorf("relayusage: usage this month: %w", err)
	}
	return bytes, sessions, nil
}

// ──────────────────────────────────────────────────────────────────────────
// MemStore — in-memory reference for tests / dev mode.
// ──────────────────────────────────────────────────────────────────────────

type memKey struct {
	account string
	period  string
}

type memRow struct {
	bytes    int64
	sessions int
}

// MemStore is the dep-free in-memory Store.
type MemStore struct {
	mu      sync.Mutex
	rows    map[memKey]memRow
	reports map[string]struct{} // "pop_id\x00report_id" → seen
}

// NewMemStore constructs an empty MemStore.
func NewMemStore() *MemStore {
	return &MemStore{rows: make(map[memKey]memRow), reports: make(map[string]struct{})}
}

// Add implements Store.
func (m *MemStore) Add(_ context.Context, accountID, period string, bytes int64, sessions int) error {
	if accountID == "" {
		return errors.New("relayusage: account_id required")
	}
	if period == "" {
		return errors.New("relayusage: period required")
	}
	if bytes < 0 {
		bytes = 0
	}
	if sessions < 0 {
		sessions = 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	k := memKey{account: accountID, period: period}
	r := m.rows[k]
	r.bytes += bytes
	r.sessions += sessions
	m.rows[k] = r
	return nil
}

// UsageThisMonth implements Store.
func (m *MemStore) UsageThisMonth(_ context.Context, accountID string) (int64, int, error) {
	period := CurrentPeriod(time.Now())
	m.mu.Lock()
	defer m.mu.Unlock()
	r := m.rows[memKey{account: accountID, period: period}]
	return r.bytes, r.sessions, nil
}

// MarkReport implements Store.
func (m *MemStore) MarkReport(_ context.Context, popID, reportID string) (bool, error) {
	if popID == "" || reportID == "" {
		return false, errors.New("relayusage: pop_id and report_id required")
	}
	k := popID + "\x00" + reportID
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.reports[k]; ok {
		return false, nil
	}
	m.reports[k] = struct{}{}
	return true, nil
}

// Close implements Store.
func (m *MemStore) Close() error { return nil }

// compile-time assertions.
var (
	_ Store = (*SQLStore)(nil)
	_ Store = (*MemStore)(nil)
)
