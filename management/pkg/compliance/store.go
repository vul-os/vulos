// store.go — cpdb-backed Store for the compliance package.
//
// Storage: cpdb-backed — SQLite (modernc.org/sqlite, pure-Go, no CGO) by default;
// Postgres via pgx/v5/stdlib when DATABASE_URL / VULOS_DATABASE_URL is set.
// Embedded migrations via go:embed (see store_embed.go). Mirrors internal/apikeys.
package compliance

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io/fs"
	"sync"
	"time"

	"github.com/vul-os/vulos-management/pkg/cpdb"
)

// newRequestID returns a random 128-bit hex identifier prefixed for readability.
func newRequestID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand failure is catastrophic; fall back to a timestamp so we
		// never return an empty ID.
		return "dsr_" + fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return "dsr_" + hex.EncodeToString(b)
}

// SQLStore is the production cpdb-backed Store (SQLite or Postgres).
type SQLStore struct {
	db *cpdb.DB
	mu sync.Mutex // serialises writes
}

// Open applies migrations to db and returns a ready *SQLStore.
//
// db should be obtained from cpdb.Open("compliance") for production, or from
// cpdb.OpenSQLiteDSN(":memory:") for tests. Migrations are applied automatically
// and are idempotent (safe to call on every startup).
func Open(db *cpdb.DB) (*SQLStore, error) {
	subFS, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("compliance: embed sub: %w", err)
	}
	if err := db.Migrate(subFS); err != nil {
		return nil, fmt.Errorf("compliance: migrate: %w", err)
	}
	return &SQLStore{db: db}, nil
}

// Close closes the underlying database connection.
func (s *SQLStore) Close() error { return s.db.Close() }

// compile-time assertion.
var _ Store = (*SQLStore)(nil)

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
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx,
		s.db.Rebind(`INSERT INTO compliance_requests (id, account_id, kind, status, note, created_at)
			   VALUES (?, ?, ?, ?, ?, ?)`),
		r.ID, r.AccountID, r.Kind, r.Status, r.Note, r.CreatedAt.Format(time.RFC3339),
	)
	if err != nil {
		return Request{}, fmt.Errorf("compliance: record request: %w", err)
	}
	return r, nil
}

// ListByAccount returns the caller's requests, newest first.
func (s *SQLStore) ListByAccount(ctx context.Context, accountID string) ([]Request, error) {
	rows, err := s.db.QueryContext(ctx,
		s.db.Rebind(`SELECT id, account_id, kind, status, note, created_at
			   FROM compliance_requests WHERE account_id = ?
			   ORDER BY created_at DESC, id DESC`), accountID,
	)
	if err != nil {
		return nil, fmt.Errorf("compliance: list requests: %w", err)
	}
	defer rows.Close()

	out := []Request{}
	for rows.Next() {
		var r Request
		var createdStr string
		if err := rows.Scan(&r.ID, &r.AccountID, &r.Kind, &r.Status, &r.Note, &createdStr); err != nil {
			return nil, fmt.Errorf("compliance: scan request: %w", err)
		}
		r.CreatedAt, _ = time.Parse(time.RFC3339, createdStr)
		out = append(out, r)
	}
	return out, rows.Err()
}
