package syncrz

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

// SQLStore is the cpdb-backed Store. Supports both SQLite and Postgres via
// the cpdb dual-backend seam (POSTGRES-SEAM).
type SQLStore struct {
	db *cpdb.DB
	mu sync.Mutex // serialises writes (single-writer pattern for SQLite)
}

// Open applies migrations to db and returns a ready SQLStore.
//
// db should be obtained from cpdb.Open("syncrz") for production, or from
// cpdb.OpenSQLiteDSN(":memory:") for tests. Migrations are applied
// automatically and are idempotent.
func Open(db *cpdb.DB) (*SQLStore, error) {
	subFS, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("syncrz: embed sub: %w", err)
	}
	if err := db.Migrate(subFS); err != nil {
		return nil, fmt.Errorf("syncrz: migrate: %w", err)
	}
	return &SQLStore{db: db}, nil
}

// Close releases the underlying database.
func (s *SQLStore) Close() error { return s.db.Close() }

// PingDB satisfies the storeHealth ping contract.
func (s *SQLStore) PingDB(ctx context.Context) error {
	return s.db.PingContext(ctx)
}

// AppendDelta implements Store. Idempotent on (account, sync_key, payload_sha256):
// re-push returns the existing row id with inserted=false.
func (s *SQLStore) AppendDelta(ctx context.Context, accountID, syncKey, boxID string, codecVersion int, payloadBytes int64, payloadSHA256, tigrisKey string, refs []string, now time.Time) (int64, bool, error) {
	if accountID == "" || syncKey == "" || boxID == "" || payloadSHA256 == "" || tigrisKey == "" {
		return 0, false, ErrInvalidInput
	}

	// Pre-compute rebound queries once (not inside the lock).
	dedupeQ := s.db.Rebind(`
		SELECT id FROM syncrz_deltas
		WHERE account_id = ? AND sync_key = ? AND payload_sha256 = ?
	`)
	insertDeltaQ := s.db.Rebind(`
		INSERT INTO syncrz_deltas
		  (account_id, sync_key, box_id, codec_version, payload_bytes, payload_sha256, tigris_key, pushed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?) RETURNING id
	`)
	insertRefQ := s.db.Rebind(`
		INSERT INTO syncrz_manifest_refs (delta_id, content_hash)
		VALUES (?, ?)
		ON CONFLICT DO NOTHING
	`)

	s.mu.Lock()
	defer s.mu.Unlock()

	// Idempotent re-push: look up existing.
	var existingID int64
	err := s.db.QueryRowContext(ctx, dedupeQ, accountID, syncKey, payloadSHA256).Scan(&existingID)
	if err == nil {
		return existingID, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, false, fmt.Errorf("syncrz: dedup lookup: %w", err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, false, fmt.Errorf("syncrz: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var id int64
	if err := tx.QueryRowContext(ctx, insertDeltaQ,
		accountID, syncKey, boxID, codecVersion, payloadBytes, payloadSHA256, tigrisKey, now.UTC().Unix(),
	).Scan(&id); err != nil {
		return 0, false, fmt.Errorf("syncrz: insert delta: %w", err)
	}

	for _, h := range refs {
		if h == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, insertRefQ, id, h); err != nil {
			return 0, false, fmt.Errorf("syncrz: insert ref: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, false, fmt.Errorf("syncrz: commit: %w", err)
	}
	return id, true, nil
}

// PullDeltas implements Store. Returns deltas with id > since, excluding the
// caller's own pushes (no echo), oldest first, capped at limit.
func (s *SQLStore) PullDeltas(ctx context.Context, accountID, syncKey, excludeBoxID string, since int64, limit int) ([]DeltaRef, error) {
	if accountID == "" || syncKey == "" {
		return nil, ErrInvalidInput
	}
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx, s.db.Rebind(`
		SELECT id, codec_version, box_id, payload_sha256, pushed_at
		FROM syncrz_deltas
		WHERE account_id = ? AND sync_key = ? AND id > ? AND box_id != ?
		ORDER BY id ASC
		LIMIT ?
	`), accountID, syncKey, since, excludeBoxID, limit)
	if err != nil {
		return nil, fmt.Errorf("syncrz: pull deltas: %w", err)
	}
	defer rows.Close()
	var out []DeltaRef
	for rows.Next() {
		var d DeltaRef
		if err := rows.Scan(&d.ID, &d.CodecVersion, &d.BoxID, &d.PayloadSHA256, &d.PushedAt); err != nil {
			return nil, fmt.Errorf("syncrz: pull scan: %w", err)
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("syncrz: pull err: %w", err)
	}

	// Hydrate refs for each delta.
	refQ := s.db.Rebind(`SELECT content_hash FROM syncrz_manifest_refs WHERE delta_id = ? ORDER BY content_hash`)
	for i := range out {
		refRows, err := s.db.QueryContext(ctx, refQ, out[i].ID)
		if err != nil {
			return nil, fmt.Errorf("syncrz: pull refs: %w", err)
		}
		for refRows.Next() {
			var h string
			if err := refRows.Scan(&h); err != nil {
				refRows.Close()
				return nil, fmt.Errorf("syncrz: pull refs scan: %w", err)
			}
			out[i].Refs = append(out[i].Refs, h)
		}
		refRows.Close()
	}
	return out, nil
}

// UpsertCursor implements Store.
func (s *SQLStore) UpsertCursor(ctx context.Context, accountID, syncKey, boxID string, lastSeenID int64, now time.Time) error {
	if accountID == "" || syncKey == "" || boxID == "" {
		return ErrInvalidInput
	}
	// SQLite uses max(a,b) scalar; Postgres uses GREATEST(a,b).
	maxFn := "max"
	if s.db.Backend() == cpdb.BackendPostgres {
		maxFn = "GREATEST"
	}
	upsertQ := s.db.Rebind(fmt.Sprintf(`
		INSERT INTO syncrz_cursors (account_id, sync_key, box_id, last_seen_id, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(account_id, sync_key, box_id) DO UPDATE SET
		  last_seen_id = %s(syncrz_cursors.last_seen_id, excluded.last_seen_id),
		  updated_at   = excluded.updated_at
	`, maxFn))
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx, upsertQ, accountID, syncKey, boxID, lastSeenID, now.UTC().Unix())
	if err != nil {
		return fmt.Errorf("syncrz: upsert cursor: %w", err)
	}
	return nil
}

// LastSeen implements Store.
func (s *SQLStore) LastSeen(ctx context.Context, accountID, syncKey, boxID string) (int64, error) {
	if accountID == "" || syncKey == "" || boxID == "" {
		return 0, ErrInvalidInput
	}
	var n int64
	err := s.db.QueryRowContext(ctx, s.db.Rebind(`
		SELECT last_seen_id FROM syncrz_cursors
		WHERE account_id = ? AND sync_key = ? AND box_id = ?
	`), accountID, syncKey, boxID).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("syncrz: last seen: %w", err)
	}
	return n, nil
}

// AppendBlob implements Store. Idempotent on (account, content_hash).
func (s *SQLStore) AppendBlob(ctx context.Context, accountID, contentHash string, sizeBytes int64, tigrisKey string, now time.Time) (bool, error) {
	if accountID == "" || contentHash == "" || tigrisKey == "" {
		return false, ErrInvalidInput
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.db.ExecContext(ctx, s.db.Rebind(`
		INSERT INTO syncrz_blobs (account_id, content_hash, size_bytes, tigris_key, first_seen_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT DO NOTHING
	`), accountID, contentHash, sizeBytes, tigrisKey, now.UTC().Unix())
	if err != nil {
		return false, fmt.Errorf("syncrz: insert blob: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("syncrz: rows affected: %w", err)
	}
	return n > 0, nil
}

// GetBlobKey implements Store.
func (s *SQLStore) GetBlobKey(ctx context.Context, accountID, contentHash string) (string, error) {
	if accountID == "" || contentHash == "" {
		return "", ErrInvalidInput
	}
	var k string
	err := s.db.QueryRowContext(ctx, s.db.Rebind(`
		SELECT tigris_key FROM syncrz_blobs WHERE account_id = ? AND content_hash = ?
	`), accountID, contentHash).Scan(&k)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrUnknown
	}
	if err != nil {
		return "", fmt.Errorf("syncrz: get blob key: %w", err)
	}
	return k, nil
}

// compile-time assertion
var _ Store = (*SQLStore)(nil)
