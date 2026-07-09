package upload

// SQLite persistence for resumable-upload state. Pure-Go modernc.org/sqlite
// (never CGO), matching the files/auth store convention. Persisting the
// committed offset is what makes resume-after-restart work: the box can crash or
// the client can drop mid-upload, and the offset (plus the on-disk staging file)
// survives so a HEAD reports exactly how much to skip.

import (
	"database/sql"
	"errors"
	"time"

	_ "modernc.org/sqlite"
)

const rfc = time.RFC3339Nano

type uStore struct{ db *sql.DB }

const schema = `
CREATE TABLE IF NOT EXISTS resumable_uploads (
	id           TEXT PRIMARY KEY,
	owner_id     TEXT NOT NULL,
	length       INTEGER NOT NULL,
	offset       INTEGER NOT NULL,
	parent_id    TEXT NOT NULL,
	name         TEXT NOT NULL,
	content_type TEXT NOT NULL,
	sha256       TEXT NOT NULL,
	complete     INTEGER NOT NULL DEFAULT 0,
	node_id      TEXT NOT NULL DEFAULT '',
	created_at   TEXT NOT NULL,
	updated_at   TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_resumable_owner   ON resumable_uploads(owner_id);
CREATE INDEX IF NOT EXISTS idx_resumable_updated ON resumable_uploads(updated_at);
`

func openStore(path string) (*uStore, error) {
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
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	return &uStore{db: db}, nil
}

func (s *uStore) close() error {
	if s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *uStore) insert(u *Upload) error {
	comp := 0
	if u.Complete {
		comp = 1
	}
	_, err := s.db.Exec(`INSERT INTO resumable_uploads
		(id, owner_id, length, offset, parent_id, name, content_type, sha256, complete, node_id, created_at, updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
		u.ID, u.OwnerID, u.Length, u.Offset, u.ParentID, u.Name, u.ContentType, u.SHA256,
		comp, u.NodeID, u.CreatedAt.Format(rfc), u.UpdatedAt.Format(rfc))
	return err
}

func (s *uStore) get(id string) (*Upload, error) {
	row := s.db.QueryRow(`SELECT id, owner_id, length, offset, parent_id, name, content_type, sha256, complete, node_id, created_at, updated_at
		FROM resumable_uploads WHERE id=?`, id)
	var (
		u                    Upload
		comp                 int
		createdAt, updatedAt string
	)
	err := row.Scan(&u.ID, &u.OwnerID, &u.Length, &u.Offset, &u.ParentID, &u.Name,
		&u.ContentType, &u.SHA256, &comp, &u.NodeID, &createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	u.Complete = comp == 1
	u.CreatedAt, _ = time.Parse(rfc, createdAt)
	u.UpdatedAt, _ = time.Parse(rfc, updatedAt)
	return &u, nil
}

func (s *uStore) setOffset(id string, offset int64, now time.Time) error {
	_, err := s.db.Exec(`UPDATE resumable_uploads SET offset=?, updated_at=? WHERE id=?`,
		offset, now.Format(rfc), id)
	return err
}

func (s *uStore) complete(id, nodeID string, now time.Time) error {
	_, err := s.db.Exec(`UPDATE resumable_uploads SET complete=1, node_id=?, updated_at=? WHERE id=?`,
		nodeID, now.Format(rfc), id)
	return err
}

func (s *uStore) delete(id string) error {
	_, err := s.db.Exec(`DELETE FROM resumable_uploads WHERE id=?`, id)
	return err
}

// staleIDs returns the ids of uploads whose updated_at predates cutoff. Both
// incomplete (abandoned partials) and complete (bookkeeping) rows qualify.
func (s *uStore) staleIDs(cutoff time.Time) ([]string, error) {
	rows, err := s.db.Query(`SELECT id FROM resumable_uploads WHERE updated_at < ?`, cutoff.Format(rfc))
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
