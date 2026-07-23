// store.go — SQLite-backed Recorder for the compliance package.
//
// Storage: SQLite (modernc.org/sqlite, pure-Go, no CGo), WAL + single-writer,
// embedded migrations applied on open — same shape as
// services/integrations/selfhost.Store.
package compliance

import (
	"context"
	"crypto/rand"
	"database/sql"
	"embed"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"time"

	dbmigrate "vulos/backend/internal/migrate"

	_ "modernc.org/sqlite" // pure-Go SQLite driver (no CGo)
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// newRequestID returns a random 128-bit hex identifier prefixed for readability.
func newRequestID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand failure is catastrophic; fall back to a timestamp so we
		// never return an empty ID.
		return fmt.Sprintf("dsr_%d", time.Now().UnixNano())
	}
	return "dsr_" + hex.EncodeToString(b)
}

// SQLStore is the production SQLite-backed Recorder.
type SQLStore struct {
	db *sql.DB
}

// OpenStore opens (or creates) the compliance SQLite store at
// <dbDir>/compliance.db and applies migrations. If dbDir is empty an in-memory
// store is used (tests). WAL + busy-timeout, idempotent schema — same DSN shape
// as the OS's other per-package stores.
func OpenStore(dbDir string) (*SQLStore, error) {
	dsn := "file::memory:?cache=shared&_pragma=busy_timeout(5000)"
	if dbDir != "" {
		path := filepath.Join(dbDir, "compliance.db")
		dsn = "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("compliance: open db: %w", err)
	}
	db.SetMaxOpenConns(1) // single-writer file; serialize writers
	s := &SQLStore{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *SQLStore) migrate() error {
	return dbmigrate.Apply(s.db, migrationsFS, "migrations")
}

// Close closes the underlying DB.
func (s *SQLStore) Close() error { return s.db.Close() }

// compile-time assertion.
var _ Recorder = (*SQLStore)(nil)

// Record persists a new request for accountID.
func (s *SQLStore) Record(ctx context.Context, accountID, kind, note string) (Request, error) {
	if !ValidKind(kind) {
		return Request{}, ErrInvalidKind
	}
	r := Request{
		ID:        newRequestID(),
		AccountID: accountID,
		Kind:      kind,
		Status:    StatusReceived,
		Note:      note,
		CreatedAt: time.Now().UTC(),
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO compliance_requests (id, account_id, kind, status, note, created_at)
VALUES (?, ?, ?, ?, ?, ?)`,
		r.ID, r.AccountID, r.Kind, r.Status, r.Note, r.CreatedAt.Unix())
	if err != nil {
		return Request{}, fmt.Errorf("compliance: record request: %w", err)
	}
	return r, nil
}

// ListByAccount returns the caller's own requests, newest first. Every query is
// filtered by account_id — the store is the isolation boundary, one user can
// never see another user's data-subject requests.
func (s *SQLStore) ListByAccount(ctx context.Context, accountID string) ([]Request, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, account_id, kind, status, note, created_at
FROM compliance_requests WHERE account_id = ?
ORDER BY created_at DESC, id DESC`, accountID)
	if err != nil {
		return nil, fmt.Errorf("compliance: list requests: %w", err)
	}
	defer rows.Close()

	out := []Request{}
	for rows.Next() {
		var r Request
		var createdAt int64
		if err := rows.Scan(&r.ID, &r.AccountID, &r.Kind, &r.Status, &r.Note, &createdAt); err != nil {
			return nil, fmt.Errorf("compliance: scan request: %w", err)
		}
		r.CreatedAt = time.Unix(createdAt, 0).UTC()
		out = append(out, r)
	}
	return out, rows.Err()
}
