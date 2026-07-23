package files

// store.go — cpdb persistence for the Drive index. SQLite (self-host) or
// Postgres (cloud) is chosen by cpdb.Open; every statement is Rebind-ed and
// dialect-neutral, so both backends run the same SQL.

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"time"

	"github.com/vul-os/vulos-management/pkg/cpdb"
)

// rfc is the timestamp format persisted in every TEXT time column.
const rfc = time.RFC3339Nano

// Service is the Files control plane: it owns the Drive index and orchestrates
// self-only-gated object grants through a Broker. Safe for concurrent use.
type Service struct {
	db     *cpdb.DB
	broker Broker
	bucket BucketResolver
}

// Open applies the migrations to db and returns a ready Service.
//
// broker mints the object grants and moves/removes bytes; bucket resolves an
// owner to their unified bucket. Either may be nil in tests that never exercise
// a grant — a nil broker makes every byte-touching call fail rather than
// silently succeed.
func Open(db *cpdb.DB, broker Broker, bucket BucketResolver) (*Service, error) {
	subFS, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("files: embed sub: %w", err)
	}
	if err := db.Migrate(subFS); err != nil {
		return nil, fmt.Errorf("files: migrate: %w", err)
	}
	return &Service{db: db, broker: broker, bucket: bucket}, nil
}

// Close closes the underlying database.
func (s *Service) Close() error { return s.db.Close() }

// ─── ID generation ────────────────────────────────────────────────────────────

const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// newID returns a 26-char Crockford-base32 ULID: a 48-bit millisecond timestamp
// (10 chars) followed by 80 bits of crypto-random entropy (16 chars). Ids sort
// by creation time, and the random half makes them unguessable — a node id is a
// capability-shaped identifier and must never be an encoded path.
func newID() (string, error) {
	out := make([]byte, 26)
	ms := uint64(time.Now().UTC().UnixMilli())
	for i := 9; i >= 0; i-- {
		out[i] = crockford[ms&31]
		ms >>= 5
	}
	// One uniform byte per character: 256/32 is exact, so &31 stays uniform.
	r := make([]byte, 16)
	if _, err := rand.Read(r); err != nil {
		return "", fmt.Errorf("files: generate id: %w", err)
	}
	for i, b := range r {
		out[10+i] = crockford[b&31]
	}
	return string(out), nil
}

// ─── nodes ────────────────────────────────────────────────────────────────────

const nodeCols = `id, owner_id, parent_id, name, is_dir, bucket, object_key, path, size, content_type, current_version_id, deleted, created_at, updated_at`

func scanNode(sc interface{ Scan(...any) error }) (*Node, error) {
	var (
		n                    Node
		isDir, deleted       int
		createdAt, updatedAt string
	)
	if err := sc.Scan(&n.ID, &n.OwnerID, &n.ParentID, &n.Name, &isDir, &n.Bucket,
		&n.ObjectKey, &n.Path, &n.Size, &n.ContentType, &n.CurrentVersionID,
		&deleted, &createdAt, &updatedAt); err != nil {
		return nil, err
	}
	n.IsDir = isDir == 1
	n.CreatedAt, _ = time.Parse(rfc, createdAt)
	n.UpdatedAt, _ = time.Parse(rfc, updatedAt)
	return &n, nil
}

func (s *Service) getNode(ctx context.Context, id string) (*Node, error) {
	row := s.db.QueryRowContext(ctx, s.db.Rebind(
		`SELECT `+nodeCols+` FROM files_nodes WHERE id=? AND deleted=0`), id)
	n, err := scanNode(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return n, err
}

func (s *Service) insertNode(ctx context.Context, n *Node) error {
	isDir := 0
	if n.IsDir {
		isDir = 1
	}
	_, err := s.db.ExecContext(ctx, s.db.Rebind(`INSERT INTO files_nodes(`+nodeCols+`)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,0,?,?)`),
		n.ID, n.OwnerID, n.ParentID, n.Name, isDir, n.Bucket, n.ObjectKey, n.Path,
		n.Size, n.ContentType, n.CurrentVersionID,
		n.CreatedAt.Format(rfc), n.UpdatedAt.Format(rfc))
	if cpdb.IsUniqueViolation(err) {
		// The partial unique index caught a lost find-or-create race; report it
		// as the same collision the pre-check reports.
		return fmt.Errorf("%w: name already exists", ErrInvalid)
	}
	return err
}

// listChildren returns the non-deleted children of parentID, folders first then
// files, each alphabetical — the order the client renders verbatim.
func (s *Service) listChildren(ctx context.Context, parentID string) ([]*Node, error) {
	rows, err := s.db.QueryContext(ctx, s.db.Rebind(`SELECT `+nodeCols+` FROM files_nodes
		WHERE parent_id=? AND deleted=0 ORDER BY is_dir DESC, name ASC`), parentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Node{}
	for rows.Next() {
		n, err := scanNode(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// childrenAll returns every child of parentID INCLUDING tombstoned ones — the
// purge sweep's walk. Delete only marks the node it was given, so a deleted
// folder's children keep deleted=0; a purge that filtered them out (as every
// user-facing read does) would strand their bytes in the bucket forever.
func (s *Service) childrenAll(ctx context.Context, parentID string) ([]*Node, error) {
	rows, err := s.db.QueryContext(ctx, s.db.Rebind(
		`SELECT `+nodeCols+` FROM files_nodes WHERE parent_id=?`), parentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Node{}
	for rows.Next() {
		n, err := scanNode(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// childrenOwnedBy lists the root-level nodes owned by ownerID (their Drive
// root). The owner filter is applied in SQL, so another account's root-level
// nodes are never even read.
func (s *Service) childrenOwnedBy(ctx context.Context, ownerID string) ([]*Node, error) {
	rows, err := s.db.QueryContext(ctx, s.db.Rebind(`SELECT `+nodeCols+` FROM files_nodes
		WHERE owner_id=? AND parent_id='' AND deleted=0 ORDER BY is_dir DESC, name ASC`), ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Node{}
	for rows.Next() {
		n, err := scanNode(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// childByName finds a non-deleted child of parentID by name (case-sensitive).
func (s *Service) childByName(ctx context.Context, ownerID, parentID, name string) (*Node, error) {
	row := s.db.QueryRowContext(ctx, s.db.Rebind(`SELECT `+nodeCols+` FROM files_nodes
		WHERE owner_id=? AND parent_id=? AND name=? AND deleted=0`), ownerID, parentID, name)
	n, err := scanNode(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return n, err
}

// touchNode persists the mutable columns of n (used by Commit).
func (s *Service) touchNode(ctx context.Context, n *Node) error {
	now := time.Now().UTC()
	_, err := s.db.ExecContext(ctx, s.db.Rebind(`UPDATE files_nodes SET size=?, content_type=?,
		current_version_id=?, updated_at=? WHERE id=?`),
		n.Size, n.ContentType, n.CurrentVersionID, now.Format(rfc), n.ID)
	if err != nil {
		return err
	}
	n.UpdatedAt = now
	return nil
}

func (s *Service) softDelete(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, s.db.Rebind(
		`UPDATE files_nodes SET deleted=1, updated_at=? WHERE id=?`),
		time.Now().UTC().Format(rfc), id)
	return err
}

// staleTombstones returns nodes soft-deleted before cutoff (updated_at is set at
// delete time) — the purge sweep's candidates. Unlike every other read here it
// deliberately selects deleted=1 rows.
func (s *Service) staleTombstones(ctx context.Context, cutoff time.Time) ([]*Node, error) {
	rows, err := s.db.QueryContext(ctx, s.db.Rebind(`SELECT `+nodeCols+` FROM files_nodes
		WHERE deleted=1 AND updated_at<?`), cutoff.UTC().Format(rfc))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Node{}
	for rows.Next() {
		n, err := scanNode(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// hardDeleteNode permanently removes id's row and its versions in one
// transaction. Called only AFTER the purge sweep has removed (or confirmed
// absent) the node's bytes — this is the point of no return for the row.
func (s *Service) hardDeleteNode(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op once Commit succeeded
	if _, err := tx.ExecContext(ctx, s.db.Rebind(`DELETE FROM files_versions WHERE node_id=?`), id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, s.db.Rebind(`DELETE FROM files_nodes WHERE id=?`), id); err != nil {
		return err
	}
	return tx.Commit()
}

// ─── versions ─────────────────────────────────────────────────────────────────

func (s *Service) insertVersion(ctx context.Context, v Version) error {
	_, err := s.db.ExecContext(ctx, s.db.Rebind(`INSERT INTO files_versions(
		id, node_id, version_key, size, content_type, etag, created_by, created_at)
		VALUES(?,?,?,?,?,?,?,?)`),
		v.ID, v.NodeID, v.VersionKey, v.Size, v.ContentType, v.ETag, v.CreatedBy,
		v.CreatedAt.Format(rfc))
	return err
}

// listVersions returns a node's history, newest first. Ids are time-sortable, so
// they break a same-millisecond tie deterministically.
func (s *Service) listVersions(ctx context.Context, nodeID string) ([]Version, error) {
	rows, err := s.db.QueryContext(ctx, s.db.Rebind(`SELECT
		id, node_id, version_key, size, content_type, etag, created_by, created_at
		FROM files_versions WHERE node_id=? ORDER BY created_at DESC, id DESC`), nodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Version{}
	for rows.Next() {
		var (
			v         Version
			createdAt string
		)
		if err := rows.Scan(&v.ID, &v.NodeID, &v.VersionKey, &v.Size, &v.ContentType,
			&v.ETag, &v.CreatedBy, &createdAt); err != nil {
			return nil, err
		}
		v.CreatedAt, _ = time.Parse(rfc, createdAt)
		out = append(out, v)
	}
	return out, rows.Err()
}
