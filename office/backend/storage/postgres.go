package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"vulos-office/backend/config"
	"vulos-office/backend/models"

	"github.com/jackc/pgx/v5/pgxpool"
)

type PostgresStorage struct {
	pool *pgxpool.Pool
}

func NewPostgresStorage(cfg *config.Config) (*PostgresStorage, error) {
	pg := cfg.Storage.Postgres
	dsn := fmt.Sprintf(
		"host=%s port=%d user=%s password=%s dbname=%s sslmode=%s",
		pg.Host, pg.Port, pg.User, pg.Password, pg.Database, pg.SSLMode,
	)

	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		return nil, fmt.Errorf("connect to postgres: %w", err)
	}

	s := &PostgresStorage{pool: pool}
	if err := s.migrate(); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return s, nil
}

func (s *PostgresStorage) migrate() error {
	_, err := s.pool.Exec(context.Background(), `
		CREATE TABLE IF NOT EXISTS files (
			id          TEXT PRIMARY KEY,
			name        TEXT NOT NULL,
			type        TEXT NOT NULL,
			content     JSONB,
			created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);
		CREATE TABLE IF NOT EXISTS file_versions (
			id          TEXT NOT NULL,
			file_id     TEXT NOT NULL REFERENCES files(id) ON DELETE CASCADE,
			name        TEXT NOT NULL,
			content     JSONB,
			created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			PRIMARY KEY (file_id, id)
		);
		CREATE INDEX IF NOT EXISTS file_versions_file_id_created ON file_versions (file_id, created_at DESC);
	`)
	return err
}

// migrateSigningSchema creates signing tables on first use (lazy/idempotent).
func (s *PostgresStorage) migrateSigningSchema() {
	_, _ = s.pool.Exec(context.Background(), `
		CREATE TABLE IF NOT EXISTS envelopes (
			id            TEXT PRIMARY KEY,
			data          JSONB NOT NULL,
			created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);
		CREATE TABLE IF NOT EXISTS audit_log (
			id            TEXT PRIMARY KEY,
			envelope_id   TEXT NOT NULL REFERENCES envelopes(id) ON DELETE CASCADE,
			data          JSONB NOT NULL,
			created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);
		CREATE INDEX IF NOT EXISTS audit_log_envelope ON audit_log (envelope_id, created_at ASC);
		CREATE TABLE IF NOT EXISTS signer_tokens (
			token         TEXT PRIMARY KEY,
			envelope_id   TEXT NOT NULL,
			signer_id     TEXT NOT NULL,
			created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);
	`)
}

// ============================================================
// File CRUD
// ============================================================

func (s *PostgresStorage) ListFiles() ([]*models.File, error) {
	rows, err := s.pool.Query(context.Background(),
		`SELECT id, name, type, content, created_at, updated_at FROM files ORDER BY updated_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var files []*models.File
	for rows.Next() {
		var f models.File
		var contentJSON []byte
		if err := rows.Scan(&f.ID, &f.Name, &f.Type, &contentJSON, &f.CreatedAt, &f.UpdatedAt); err != nil {
			return nil, err
		}
		if contentJSON != nil {
			if err := json.Unmarshal(contentJSON, &f.Content); err != nil {
				return nil, err
			}
		}
		files = append(files, &f)
	}
	return files, rows.Err()
}

func (s *PostgresStorage) GetFile(id string) (*models.File, error) {
	var f models.File
	var contentJSON []byte
	err := s.pool.QueryRow(context.Background(),
		`SELECT id, name, type, content, created_at, updated_at FROM files WHERE id=$1`, id,
	).Scan(&f.ID, &f.Name, &f.Type, &contentJSON, &f.CreatedAt, &f.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("file not found")
	}
	if contentJSON != nil {
		if err := json.Unmarshal(contentJSON, &f.Content); err != nil {
			return nil, err
		}
	}
	return &f, nil
}

func (s *PostgresStorage) CreateFile(f *models.File) error {
	contentJSON, err := json.Marshal(f.Content)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(context.Background(),
		`INSERT INTO files (id, name, type, content) VALUES ($1, $2, $3, $4)`,
		f.ID, f.Name, f.Type, contentJSON,
	)
	return err
}

// UpdateFile snapshots current content as a version before overwriting (OFFICE-08).
func (s *PostgresStorage) UpdateFile(f *models.File) error {
	existing, err := s.GetFile(f.ID)
	if err != nil {
		return err
	}
	snap := &models.FileVersion{
		ID:        fmt.Sprintf("%d", existing.UpdatedAt.UnixNano()),
		FileID:    existing.ID,
		Name:      existing.Name,
		Content:   existing.Content,
		CreatedAt: existing.UpdatedAt,
	}
	_ = s.CreateVersion(snap)
	_ = s.PruneVersions(existing.ID, DefaultVersionCap)

	contentJSON, err := json.Marshal(f.Content)
	if err != nil {
		return err
	}
	cmd, err := s.pool.Exec(context.Background(),
		`UPDATE files SET name=$2, content=$3, updated_at=NOW() WHERE id=$1`,
		f.ID, f.Name, contentJSON,
	)
	if err != nil {
		return err
	}
	if cmd.RowsAffected() == 0 {
		return fmt.Errorf("file not found")
	}
	return nil
}

func (s *PostgresStorage) DeleteFile(id string) error {
	cmd, err := s.pool.Exec(context.Background(), `DELETE FROM files WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if cmd.RowsAffected() == 0 {
		return fmt.Errorf("file not found")
	}
	return nil
}

// ============================================================
// Version history (OFFICE-08)
// ============================================================

func (s *PostgresStorage) CreateVersion(v *models.FileVersion) error {
	data, err := json.Marshal(v.Content)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(context.Background(),
		`INSERT INTO file_versions (id, file_id, name, content, created_at) VALUES ($1,$2,$3,$4,$5)
		 ON CONFLICT (file_id, id) DO NOTHING`,
		v.ID, v.FileID, v.Name, data, v.CreatedAt,
	)
	return err
}

func (s *PostgresStorage) ListVersions(fileID string) ([]*models.FileVersion, error) {
	rows, err := s.pool.Query(context.Background(),
		`SELECT id, file_id, name, content, created_at FROM file_versions
		 WHERE file_id=$1 ORDER BY created_at DESC`, fileID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var versions []*models.FileVersion
	for rows.Next() {
		var v models.FileVersion
		var contentJSON []byte
		if err := rows.Scan(&v.ID, &v.FileID, &v.Name, &contentJSON, &v.CreatedAt); err != nil {
			return nil, err
		}
		if contentJSON != nil {
			_ = json.Unmarshal(contentJSON, &v.Content)
		}
		versions = append(versions, &v)
	}
	return versions, rows.Err()
}

func (s *PostgresStorage) GetVersion(fileID, versionID string) (*models.FileVersion, error) {
	var v models.FileVersion
	var contentJSON []byte
	err := s.pool.QueryRow(context.Background(),
		`SELECT id, file_id, name, content, created_at FROM file_versions WHERE file_id=$1 AND id=$2`,
		fileID, versionID,
	).Scan(&v.ID, &v.FileID, &v.Name, &contentJSON, &v.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("version not found")
	}
	if contentJSON != nil {
		_ = json.Unmarshal(contentJSON, &v.Content)
	}
	return &v, nil
}

func (s *PostgresStorage) PruneVersions(fileID string, cap int) error {
	_, err := s.pool.Exec(context.Background(), `
		DELETE FROM file_versions WHERE (file_id, id) IN (
			SELECT file_id, id FROM file_versions
			WHERE file_id=$1
			ORDER BY created_at DESC
			OFFSET $2
		)`, fileID, cap)
	return err
}

// LabelVersion updates the label column on an existing version (OFFICE-28).
// The postgres schema stores label in the versions table (added via ALTER TABLE IF NOT EXISTS).
func (s *PostgresStorage) LabelVersion(fileID, versionID, label string) error {
	// Ensure the label column exists (idempotent migration).
	_, _ = s.pool.Exec(context.Background(),
		`ALTER TABLE file_versions ADD COLUMN IF NOT EXISTS label TEXT NOT NULL DEFAULT ''`)
	cmd, err := s.pool.Exec(context.Background(),
		`UPDATE file_versions SET label=$3 WHERE file_id=$1 AND id=$2`,
		fileID, versionID, label)
	if err != nil {
		return err
	}
	if cmd.RowsAffected() == 0 {
		return fmt.Errorf("version not found")
	}
	return nil
}

// ============================================================
// Signing — Envelope CRUD (OFFICE-40)
// ============================================================

func (s *PostgresStorage) CreateEnvelope(env *models.Envelope) error {
	s.migrateSigningSchema()
	now := time.Now()
	env.CreatedAt = now
	env.UpdatedAt = now
	data, err := json.Marshal(env)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(context.Background(),
		`INSERT INTO envelopes (id, data, created_at, updated_at) VALUES ($1,$2,$3,$4)`,
		env.ID, data, now, now,
	)
	return err
}

func (s *PostgresStorage) GetEnvelope(id string) (*models.Envelope, error) {
	s.migrateSigningSchema()
	var data []byte
	err := s.pool.QueryRow(context.Background(),
		`SELECT data FROM envelopes WHERE id=$1`, id,
	).Scan(&data)
	if err != nil {
		return nil, fmt.Errorf("envelope not found")
	}
	var env models.Envelope
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, err
	}
	return &env, nil
}

func (s *PostgresStorage) ListEnvelopes() ([]*models.Envelope, error) {
	s.migrateSigningSchema()
	rows, err := s.pool.Query(context.Background(),
		`SELECT data FROM envelopes ORDER BY updated_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var envs []*models.Envelope
	for rows.Next() {
		var data []byte
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		var env models.Envelope
		if err := json.Unmarshal(data, &env); err != nil {
			continue
		}
		envs = append(envs, &env)
	}
	return envs, rows.Err()
}

func (s *PostgresStorage) UpdateEnvelope(env *models.Envelope) error {
	env.UpdatedAt = time.Now()
	data, err := json.Marshal(env)
	if err != nil {
		return err
	}
	cmd, err := s.pool.Exec(context.Background(),
		`UPDATE envelopes SET data=$2, updated_at=$3 WHERE id=$1`,
		env.ID, data, env.UpdatedAt,
	)
	if err != nil {
		return err
	}
	if cmd.RowsAffected() == 0 {
		return fmt.Errorf("envelope not found")
	}
	return nil
}

func (s *PostgresStorage) DeleteEnvelope(id string) error {
	cmd, err := s.pool.Exec(context.Background(), `DELETE FROM envelopes WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if cmd.RowsAffected() == 0 {
		return fmt.Errorf("envelope not found")
	}
	return nil
}

// ============================================================
// Signing — Signer management (OFFICE-40)
// ============================================================

func (s *PostgresStorage) UpsertSigner(sg *models.Signer) error {
	env, err := s.GetEnvelope(sg.EnvelopeID)
	if err != nil {
		return err
	}
	found := false
	for i, existing := range env.Signers {
		if existing.ID == sg.ID {
			env.Signers[i] = sg
			found = true
			break
		}
	}
	if !found {
		env.Signers = append(env.Signers, sg)
	}
	return s.UpdateEnvelope(env)
}

func (s *PostgresStorage) GetSigner(id string) (*models.Signer, error) {
	envs, err := s.ListEnvelopes()
	if err != nil {
		return nil, err
	}
	for _, env := range envs {
		for _, sg := range env.Signers {
			if sg.ID == id {
				return sg, nil
			}
		}
	}
	return nil, fmt.Errorf("signer not found")
}

func (s *PostgresStorage) ListSignersByEnvelope(envelopeID string) ([]*models.Signer, error) {
	env, err := s.GetEnvelope(envelopeID)
	if err != nil {
		return nil, err
	}
	signers := env.Signers
	sort.Slice(signers, func(i, j int) bool {
		return signers[i].Order < signers[j].Order
	})
	return signers, nil
}

// ============================================================
// Signing — Append-only audit log (OFFICE-40)
// ============================================================

func (s *PostgresStorage) AppendAuditEvent(ev *models.AuditEvent) error {
	s.migrateSigningSchema()
	data, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(context.Background(),
		`INSERT INTO audit_log (id, envelope_id, data, created_at) VALUES ($1,$2,$3,$4)`,
		ev.ID, ev.EnvelopeID, data, ev.Timestamp,
	)
	return err
}

func (s *PostgresStorage) ListAuditEvents(envelopeID string) ([]*models.AuditEvent, error) {
	s.migrateSigningSchema()
	rows, err := s.pool.Query(context.Background(),
		`SELECT data FROM audit_log WHERE envelope_id=$1 ORDER BY created_at ASC`, envelopeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []*models.AuditEvent
	for rows.Next() {
		var data []byte
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		var ev models.AuditEvent
		if err := json.Unmarshal(data, &ev); err != nil {
			continue
		}
		events = append(events, &ev)
	}
	return events, rows.Err()
}

// ============================================================
// Signing — Token index (OFFICE-42)
// ============================================================

func (s *PostgresStorage) StoreSignerToken(token, envelopeID, signerID string) error {
	s.migrateSigningSchema()
	_, err := s.pool.Exec(context.Background(),
		`INSERT INTO signer_tokens (token, envelope_id, signer_id) VALUES ($1,$2,$3)
		 ON CONFLICT (token) DO NOTHING`,
		token, envelopeID, signerID,
	)
	return err
}

func (s *PostgresStorage) ResolveToken(token string) (string, string, error) {
	s.migrateSigningSchema()
	var envelopeID, signerID string
	err := s.pool.QueryRow(context.Background(),
		`SELECT envelope_id, signer_id FROM signer_tokens WHERE token=$1`, token,
	).Scan(&envelopeID, &signerID)
	if err != nil {
		return "", "", fmt.Errorf("token not found")
	}
	return envelopeID, signerID, nil
}

// ============================================================
// Sealed PDF store (OFFICE-46) — Postgres implementation
// ============================================================

func (s *PostgresStorage) migrateSealedSchema() {
	_, _ = s.pool.Exec(context.Background(), `
		CREATE TABLE IF NOT EXISTS sealed_pdfs (
			envelope_id TEXT PRIMARY KEY,
			data        BYTEA NOT NULL,
			created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);
	`)
}

func (s *PostgresStorage) StoreSealedPDF(envelopeID string, data []byte) error {
	s.migrateSealedSchema()
	_, err := s.pool.Exec(context.Background(),
		`INSERT INTO sealed_pdfs (envelope_id, data) VALUES ($1, $2)
		 ON CONFLICT (envelope_id) DO UPDATE SET data = EXCLUDED.data`,
		envelopeID, data,
	)
	return err
}

func (s *PostgresStorage) GetSealedPDF(envelopeID string) ([]byte, error) {
	s.migrateSealedSchema()
	var data []byte
	err := s.pool.QueryRow(context.Background(),
		`SELECT data FROM sealed_pdfs WHERE envelope_id=$1`, envelopeID,
	).Scan(&data)
	if err != nil {
		return nil, fmt.Errorf("sealed PDF not found")
	}
	return data, nil
}

// ============================================================
// Comments (OFFICE-26) — Postgres stub implementations
// ============================================================

func (s *PostgresStorage) migrateCommentsSchema() {
	s.pool.Exec(context.Background(), `
		CREATE TABLE IF NOT EXISTS comments (
			id TEXT PRIMARY KEY,
			file_id TEXT NOT NULL,
			anchor JSONB NOT NULL DEFAULT '{}',
			author_id TEXT NOT NULL DEFAULT '',
			body TEXT NOT NULL DEFAULT '',
			state TEXT NOT NULL DEFAULT 'open',
			seq_clock TEXT NOT NULL DEFAULT '',
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);
		CREATE TABLE IF NOT EXISTS comment_replies (
			id TEXT PRIMARY KEY,
			comment_id TEXT NOT NULL,
			file_id TEXT NOT NULL,
			author_id TEXT NOT NULL DEFAULT '',
			body TEXT NOT NULL DEFAULT '',
			seq_clock TEXT NOT NULL DEFAULT '',
			deleted BOOLEAN NOT NULL DEFAULT FALSE,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);
	`)
}

func (s *PostgresStorage) CreateComment(c *models.Comment) error {
	s.migrateCommentsSchema()
	anchor, _ := json.Marshal(c.Anchor)
	_, err := s.pool.Exec(context.Background(),
		`INSERT INTO comments (id,file_id,anchor,author_id,body,state,seq_clock,created_at,updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		 ON CONFLICT (id) DO UPDATE SET anchor=$3,body=$5,state=$6,seq_clock=$7,updated_at=$9`,
		c.ID, c.FileID, anchor, c.AuthorID, c.Body, c.State, c.SeqClock, c.CreatedAt, c.UpdatedAt,
	)
	return err
}

func (s *PostgresStorage) GetComment(fileID, commentID string) (*models.Comment, error) {
	s.migrateCommentsSchema()
	var c models.Comment
	var anchor []byte
	err := s.pool.QueryRow(context.Background(),
		`SELECT id,file_id,anchor,author_id,body,state,seq_clock,created_at,updated_at
		 FROM comments WHERE file_id=$1 AND id=$2`, fileID, commentID,
	).Scan(&c.ID, &c.FileID, &anchor, &c.AuthorID, &c.Body, &c.State, &c.SeqClock, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("comment not found")
	}
	_ = json.Unmarshal(anchor, &c.Anchor)
	return &c, nil
}

func (s *PostgresStorage) ListComments(fileID string) ([]*models.Comment, error) {
	s.migrateCommentsSchema()
	rows, err := s.pool.Query(context.Background(),
		`SELECT id,file_id,anchor,author_id,body,state,seq_clock,created_at,updated_at
		 FROM comments WHERE file_id=$1 ORDER BY created_at ASC`, fileID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var comments []*models.Comment
	for rows.Next() {
		var c models.Comment
		var anchor []byte
		if err := rows.Scan(&c.ID, &c.FileID, &anchor, &c.AuthorID, &c.Body, &c.State, &c.SeqClock, &c.CreatedAt, &c.UpdatedAt); err != nil {
			continue
		}
		_ = json.Unmarshal(anchor, &c.Anchor)
		comments = append(comments, &c)
	}
	return comments, rows.Err()
}

func (s *PostgresStorage) UpdateComment(c *models.Comment) error {
	return s.CreateComment(c)
}

func (s *PostgresStorage) DeleteComment(fileID, commentID string) error {
	s.migrateCommentsSchema()
	_, err := s.pool.Exec(context.Background(),
		`DELETE FROM comments WHERE file_id=$1 AND id=$2`, fileID, commentID)
	return err
}

func (s *PostgresStorage) CreateReply(r *models.CommentReply) error {
	s.migrateCommentsSchema()
	_, err := s.pool.Exec(context.Background(),
		`INSERT INTO comment_replies (id,comment_id,file_id,author_id,body,seq_clock,deleted,created_at,updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		 ON CONFLICT (id) DO UPDATE SET body=$5,seq_clock=$6,deleted=$7,updated_at=$9`,
		r.ID, r.CommentID, r.FileID, r.AuthorID, r.Body, r.SeqClock, r.Deleted, r.CreatedAt, r.UpdatedAt,
	)
	return err
}

func (s *PostgresStorage) GetReply(commentID, replyID string) (*models.CommentReply, error) {
	s.migrateCommentsSchema()
	var r models.CommentReply
	err := s.pool.QueryRow(context.Background(),
		`SELECT id,comment_id,file_id,author_id,body,seq_clock,deleted,created_at,updated_at
		 FROM comment_replies WHERE comment_id=$1 AND id=$2`, commentID, replyID,
	).Scan(&r.ID, &r.CommentID, &r.FileID, &r.AuthorID, &r.Body, &r.SeqClock, &r.Deleted, &r.CreatedAt, &r.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("reply not found")
	}
	return &r, nil
}

func (s *PostgresStorage) ListReplies(commentID string) ([]*models.CommentReply, error) {
	s.migrateCommentsSchema()
	rows, err := s.pool.Query(context.Background(),
		`SELECT id,comment_id,file_id,author_id,body,seq_clock,deleted,created_at,updated_at
		 FROM comment_replies WHERE comment_id=$1 ORDER BY created_at ASC`, commentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var replies []*models.CommentReply
	for rows.Next() {
		var r models.CommentReply
		if err := rows.Scan(&r.ID, &r.CommentID, &r.FileID, &r.AuthorID, &r.Body, &r.SeqClock, &r.Deleted, &r.CreatedAt, &r.UpdatedAt); err != nil {
			continue
		}
		replies = append(replies, &r)
	}
	return replies, rows.Err()
}

func (s *PostgresStorage) UpdateReply(r *models.CommentReply) error {
	return s.CreateReply(r)
}

// ============================================================
// Scheduled meetings (OFFICE-65)
// ============================================================

func (s *PostgresStorage) migrateMeetingsSchema() {
	_, _ = s.pool.Exec(context.Background(), `
		CREATE TABLE IF NOT EXISTS meetings (
			id          TEXT PRIMARY KEY,
			data        JSONB NOT NULL,
			created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);
		CREATE INDEX IF NOT EXISTS meetings_updated ON meetings (updated_at DESC);
	`)
}

func (s *PostgresStorage) CreateMeeting(m *models.Meeting) error {
	s.migrateMeetingsSchema()
	now := time.Now()
	m.CreatedAt = now
	m.UpdatedAt = now
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(context.Background(),
		`INSERT INTO meetings (id, data, created_at, updated_at) VALUES ($1,$2,$3,$4)`,
		m.ID, data, m.CreatedAt, m.UpdatedAt,
	)
	return err
}

func (s *PostgresStorage) GetMeeting(id string) (*models.Meeting, error) {
	s.migrateMeetingsSchema()
	var data []byte
	err := s.pool.QueryRow(context.Background(),
		`SELECT data FROM meetings WHERE id=$1`, id,
	).Scan(&data)
	if err != nil {
		return nil, fmt.Errorf("meeting not found")
	}
	var m models.Meeting
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

func (s *PostgresStorage) ListMeetings() ([]*models.Meeting, error) {
	s.migrateMeetingsSchema()
	rows, err := s.pool.Query(context.Background(),
		`SELECT data FROM meetings ORDER BY updated_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var meetings []*models.Meeting
	for rows.Next() {
		var data []byte
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		var m models.Meeting
		if err := json.Unmarshal(data, &m); err != nil {
			continue
		}
		meetings = append(meetings, &m)
	}
	return meetings, rows.Err()
}

func (s *PostgresStorage) UpdateMeeting(m *models.Meeting) error {
	s.migrateMeetingsSchema()
	m.UpdatedAt = time.Now()
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(context.Background(),
		`UPDATE meetings SET data=$1, updated_at=$2 WHERE id=$3`,
		data, m.UpdatedAt, m.ID,
	)
	return err
}

func (s *PostgresStorage) DeleteMeeting(id string) error {
	s.migrateMeetingsSchema()
	_, err := s.pool.Exec(context.Background(),
		`DELETE FROM meetings WHERE id=$1`, id,
	)
	return err
}

// ============================================================
// Suggestions (OFFICE-27) — stub implementations
// ============================================================

func (s *PostgresStorage) CreateSuggestion(sg *models.Suggestion) error {
	return fmt.Errorf("suggestions not yet implemented in postgres storage")
}

func (s *PostgresStorage) GetSuggestion(fileID, suggestionID string) (*models.Suggestion, error) {
	return nil, fmt.Errorf("suggestions not yet implemented in postgres storage")
}

func (s *PostgresStorage) ListSuggestions(fileID string) ([]*models.Suggestion, error) {
	return []*models.Suggestion{}, nil
}

func (s *PostgresStorage) UpdateSuggestion(sg *models.Suggestion) error {
	return fmt.Errorf("suggestions not yet implemented in postgres storage")
}

func (s *PostgresStorage) DeleteSuggestion(fileID, suggestionID string) error {
	return fmt.Errorf("suggestions not yet implemented in postgres storage")
}

