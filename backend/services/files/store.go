package files

// SQLite backing for the Files control plane. Pure-Go modernc.org/sqlite (never
// CGO), matching the auth.Store convention. Unlike auth (which mirrors in-memory
// maps), the Files index is queried directly from SQLite — it is the metadata
// source of truth and benefits from indexed lookups/joins.

import (
	"database/sql"
	"embed"
	"errors"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed migrations/0001_files.sql
var migrationsFS embed.FS

// ErrNotFound is returned when a node / link / version does not exist.
var ErrNotFound = errors.New("files: not found")

const rfc = time.RFC3339Nano

func openDB(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	// modernc/sqlite is not safe for unbounded concurrent writers on one file.
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	sqlBytes, err := migrationsFS.ReadFile("migrations/0001_files.sql")
	if err != nil {
		db.Close()
		return nil, err
	}
	if _, err := db.Exec(string(sqlBytes)); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// --- nodes ---

func scanNode(s interface{ Scan(...any) error }) (*Node, error) {
	var (
		n                    Node
		isDir, deleted       int
		createdAt, updatedAt string
	)
	if err := s.Scan(&n.ID, &n.OwnerID, &n.ParentID, &n.Name, &isDir, &n.Bucket,
		&n.ObjectKey, &n.Path, &n.Size, &n.ContentType, &n.CurrentVersionID,
		&deleted, &createdAt, &updatedAt); err != nil {
		return nil, err
	}
	n.IsDir = isDir == 1
	n.CreatedAt, _ = time.Parse(rfc, createdAt)
	n.UpdatedAt, _ = time.Parse(rfc, updatedAt)
	return &n, nil
}

const nodeCols = `id, owner_id, parent_id, name, is_dir, bucket, object_key, path, size, content_type, current_version_id, deleted, created_at, updated_at`

func (s *Service) getNode(id string) (*Node, error) {
	row := s.db.QueryRow(`SELECT `+nodeCols+` FROM files_nodes WHERE id=? AND deleted=0`, id)
	n, err := scanNode(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return n, err
}

func (s *Service) insertNode(n *Node) error {
	isDir := 0
	if n.IsDir {
		isDir = 1
	}
	_, err := s.db.Exec(`INSERT INTO files_nodes(`+nodeCols+`)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,0,?,?)`,
		n.ID, n.OwnerID, n.ParentID, n.Name, isDir, n.Bucket, n.ObjectKey, n.Path,
		n.Size, n.ContentType, n.CurrentVersionID,
		n.CreatedAt.Format(rfc), n.UpdatedAt.Format(rfc))
	return err
}

// listChildren returns the non-deleted children of parentID owned-or-visible in
// the index (visibility filtering is the caller's ACL job).
func (s *Service) listChildren(parentID string) ([]*Node, error) {
	rows, err := s.db.Query(`SELECT `+nodeCols+` FROM files_nodes
		WHERE parent_id=? AND deleted=0 ORDER BY is_dir DESC, name ASC`, parentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Node
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
func (s *Service) childByName(ownerID, parentID, name string) (*Node, error) {
	row := s.db.QueryRow(`SELECT `+nodeCols+` FROM files_nodes
		WHERE owner_id=? AND parent_id=? AND name=? AND deleted=0`, ownerID, parentID, name)
	n, err := scanNode(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return n, err
}

func (s *Service) touchNode(n *Node) error {
	_, err := s.db.Exec(`UPDATE files_nodes SET name=?, parent_id=?, path=?, object_key=?, size=?,
		content_type=?, current_version_id=?, updated_at=? WHERE id=?`,
		n.Name, n.ParentID, n.Path, n.ObjectKey, n.Size, n.ContentType, n.CurrentVersionID,
		time.Now().Format(rfc), n.ID)
	return err
}

func (s *Service) softDelete(id string) error {
	_, err := s.db.Exec(`UPDATE files_nodes SET deleted=1, updated_at=? WHERE id=?`,
		time.Now().Format(rfc), id)
	return err
}

// --- acls ---

func (s *Service) aclFor(nodeID, principalID string) (Role, bool) {
	var role string
	err := s.db.QueryRow(`SELECT role FROM files_acls WHERE node_id=? AND principal_id=?`,
		nodeID, principalID).Scan(&role)
	if err != nil {
		return "", false
	}
	return Role(role), true
}

func (s *Service) listACLs(nodeID string) ([]ACLEntry, error) {
	rows, err := s.db.Query(`SELECT id, node_id, principal_id, role, created_by, created_at
		FROM files_acls WHERE node_id=? ORDER BY created_at ASC`, nodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ACLEntry
	for rows.Next() {
		var e ACLEntry
		var created string
		if err := rows.Scan(&e.ID, &e.NodeID, &e.PrincipalID, &e.Role, &e.CreatedBy, &created); err != nil {
			return nil, err
		}
		e.CreatedAt, _ = time.Parse(rfc, created)
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Service) upsertACL(e ACLEntry) error {
	_, err := s.db.Exec(`INSERT INTO files_acls(id, node_id, principal_id, role, created_by, created_at)
		VALUES(?,?,?,?,?,?)
		ON CONFLICT(node_id, principal_id) DO UPDATE SET role=excluded.role`,
		e.ID, e.NodeID, e.PrincipalID, e.Role, e.CreatedBy, e.CreatedAt.Format(rfc))
	return err
}

func (s *Service) deleteACL(nodeID, principalID string) error {
	_, err := s.db.Exec(`DELETE FROM files_acls WHERE node_id=? AND principal_id=?`, nodeID, principalID)
	return err
}

// nodesSharedWith returns the node IDs that have a direct ACL entry for principal.
func (s *Service) nodesSharedWith(principalID string) ([]string, error) {
	rows, err := s.db.Query(`SELECT node_id FROM files_acls WHERE principal_id=?`, principalID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// --- share links ---

func (s *Service) insertLink(l ShareLink) error {
	_, err := s.db.Exec(`INSERT INTO files_share_links(token, node_id, role, created_by, created_at, expires_at, revoked)
		VALUES(?,?,?,?,?,?,0)`,
		l.Token, l.NodeID, l.Role, l.CreatedBy, l.CreatedAt.Format(rfc), l.ExpiresAt.Format(rfc))
	return err
}

func (s *Service) getLink(token string) (*ShareLink, error) {
	var l ShareLink
	var created, expires string
	var revoked int
	err := s.db.QueryRow(`SELECT token, node_id, role, created_by, created_at, expires_at, revoked
		FROM files_share_links WHERE token=?`, token).
		Scan(&l.Token, &l.NodeID, &l.Role, &l.CreatedBy, &created, &expires, &revoked)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	l.CreatedAt, _ = time.Parse(rfc, created)
	l.ExpiresAt, _ = time.Parse(rfc, expires)
	l.Revoked = revoked == 1
	return &l, nil
}

func (s *Service) revokeLink(token string) error {
	res, err := s.db.Exec(`UPDATE files_share_links SET revoked=1 WHERE token=?`, token)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Service) listLinks(nodeID string) ([]ShareLink, error) {
	rows, err := s.db.Query(`SELECT token, node_id, role, created_by, created_at, expires_at, revoked
		FROM files_share_links WHERE node_id=? ORDER BY created_at DESC`, nodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ShareLink
	for rows.Next() {
		var l ShareLink
		var created, expires string
		var revoked int
		if err := rows.Scan(&l.Token, &l.NodeID, &l.Role, &l.CreatedBy, &created, &expires, &revoked); err != nil {
			return nil, err
		}
		l.CreatedAt, _ = time.Parse(rfc, created)
		l.ExpiresAt, _ = time.Parse(rfc, expires)
		l.Revoked = revoked == 1
		out = append(out, l)
	}
	return out, rows.Err()
}

// --- versions ---

func (s *Service) insertVersion(v Version) error {
	_, err := s.db.Exec(`INSERT INTO files_versions(id, node_id, version_key, size, content_type, etag, created_by, created_at)
		VALUES(?,?,?,?,?,?,?,?)`,
		v.ID, v.NodeID, v.VersionKey, v.Size, v.ContentType, v.ETag, v.CreatedBy, v.CreatedAt.Format(rfc))
	return err
}

func (s *Service) listVersions(nodeID string) ([]Version, error) {
	rows, err := s.db.Query(`SELECT id, node_id, version_key, size, content_type, etag, created_by, created_at
		FROM files_versions WHERE node_id=? ORDER BY created_at DESC`, nodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Version
	for rows.Next() {
		var v Version
		var created string
		if err := rows.Scan(&v.ID, &v.NodeID, &v.VersionKey, &v.Size, &v.ContentType, &v.ETag, &v.CreatedBy, &created); err != nil {
			return nil, err
		}
		v.CreatedAt, _ = time.Parse(rfc, created)
		out = append(out, v)
	}
	return out, rows.Err()
}

// Close closes the underlying database.
func (s *Service) Close() error {
	if s.db == nil {
		return nil
	}
	return s.db.Close()
}
