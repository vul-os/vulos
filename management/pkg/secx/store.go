package secx

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"sync"
	"time"

	"github.com/vul-os/vulos-management/pkg/cpdb"
)

// SQLStore is the production cpdb-backed implementation of VisibilityStore.
// It works against either SQLite (modernc.org/sqlite, pure-Go) or Postgres
// (pgx/v5/stdlib) depending on the backend of the cpdb.DB it is opened with.
// Concurrency-safe. Behaviour is semantically identical to MemStore.
type SQLStore struct {
	db *cpdb.DB
	mu sync.Mutex // serialises all writes
}

// Open applies the embedded migrations to db idempotently and returns a ready
// *SQLStore.
//
// db should be obtained from cpdb.Open("secx") for production, or from
// cpdb.OpenSQLiteDSN(":memory:") for tests.
func Open(db *cpdb.DB) (*SQLStore, error) {
	subFS, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("secx: embed sub: %w", err)
	}
	if err := db.Migrate(subFS); err != nil {
		return nil, fmt.Errorf("secx: migrate: %w", err)
	}
	return &SQLStore{db: db}, nil
}

// Close closes the underlying database connection.
func (s *SQLStore) Close() error { return s.db.Close() }

// compile-time assertion: SQLStore satisfies VisibilityStore.
var _ VisibilityStore = (*SQLStore)(nil)

// ─────────────────────────────────────────────────────────────────────────────
// Get
// ─────────────────────────────────────────────────────────────────────────────

func (s *SQLStore) Get(ctx context.Context, appID, ulid string) (AppVisibility, error) {
	var av AppVisibility
	var updatedAt string
	err := s.db.QueryRowContext(ctx,
		s.db.Rebind(`SELECT app_id, ulid, account_id, visibility, updated_at
		   FROM app_visibility WHERE app_id = ? AND ulid = ?`),
		appID, ulid,
	).Scan(&av.AppID, &av.ULID, &av.AccountID, &av.Visibility, &updatedAt)
	if err == sql.ErrNoRows {
		return AppVisibility{}, ErrUnknownApp
	}
	if err != nil {
		return AppVisibility{}, fmt.Errorf("secx: get visibility: %w", err)
	}
	av.UpdatedAt = parseTime(updatedAt)
	return av, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Set
// ─────────────────────────────────────────────────────────────────────────────

func (s *SQLStore) Set(ctx context.Context, av AppVisibility, audit AuditEntry) error {
	ts := nowStr()
	av.UpdatedAt = parseTime(ts)
	audit.ChangedAt = av.UpdatedAt

	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("secx: set begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	_, err = tx.ExecContext(ctx,
		s.db.Rebind(`INSERT INTO app_visibility (app_id, ulid, account_id, visibility, updated_at)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(app_id, ulid) DO UPDATE SET
		   account_id  = excluded.account_id,
		   visibility  = excluded.visibility,
		   updated_at  = excluded.updated_at`),
		av.AppID, av.ULID, av.AccountID, string(av.Visibility), ts,
	)
	if err != nil {
		return fmt.Errorf("secx: upsert visibility: %w", err)
	}

	_, err = tx.ExecContext(ctx,
		s.db.Rebind(`INSERT INTO secx_audit (app_id, ulid, account_id, old_vis, new_vis, ip, changed_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`),
		audit.AppID, audit.ULID, audit.AccountID,
		audit.OldVis, audit.NewVis, audit.IP, ts,
	)
	if err != nil {
		return fmt.Errorf("secx: insert audit: %w", err)
	}

	return tx.Commit()
}

// ─────────────────────────────────────────────────────────────────────────────
// ListByULID
// ─────────────────────────────────────────────────────────────────────────────

func (s *SQLStore) ListByULID(ctx context.Context, ulid string) ([]AppVisibility, error) {
	rows, err := s.db.QueryContext(ctx,
		s.db.Rebind(`SELECT app_id, ulid, account_id, visibility, updated_at
		   FROM app_visibility WHERE ulid = ?
		   ORDER BY app_id`),
		ulid,
	)
	if err != nil {
		return nil, fmt.Errorf("secx: list by ulid: %w", err)
	}
	defer rows.Close()

	var out []AppVisibility
	for rows.Next() {
		var av AppVisibility
		var updatedAt string
		if err := rows.Scan(&av.AppID, &av.ULID, &av.AccountID, &av.Visibility, &updatedAt); err != nil {
			return nil, fmt.Errorf("secx: scan visibility: %w", err)
		}
		av.UpdatedAt = parseTime(updatedAt)
		out = append(out, av)
	}
	return out, rows.Err()
}

// ─────────────────────────────────────────────────────────────────────────────
// AuditLog
// ─────────────────────────────────────────────────────────────────────────────

func (s *SQLStore) AuditLog(ctx context.Context, appID, ulid string, limit int) ([]AuditEntry, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx,
		s.db.Rebind(`SELECT app_id, ulid, account_id, old_vis, new_vis, ip, changed_at
		   FROM secx_audit WHERE app_id = ? AND ulid = ?
		   ORDER BY changed_at DESC
		   LIMIT ?`),
		appID, ulid, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("secx: audit log: %w", err)
	}
	defer rows.Close()

	var out []AuditEntry
	for rows.Next() {
		var e AuditEntry
		var changedAt string
		if err := rows.Scan(&e.AppID, &e.ULID, &e.AccountID, &e.OldVis, &e.NewVis, &e.IP, &changedAt); err != nil {
			return nil, fmt.Errorf("secx: scan audit: %w", err)
		}
		e.ChangedAt = parseTime(changedAt)
		out = append(out, e)
	}
	return out, rows.Err()
}

// ─────────────────────────────────────────────────────────────────────────────
// Time helpers (mirrors fleet/store.go style)
// ─────────────────────────────────────────────────────────────────────────────

func nowStr() string { return time.Now().UTC().Format(time.RFC3339Nano) }

func parseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		t, _ = time.Parse(time.RFC3339, s)
	}
	return t.UTC()
}
