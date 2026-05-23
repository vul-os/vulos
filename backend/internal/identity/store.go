// store.go — SQLite-backed persistence for the identity package (IDENTITY-02).
// Note: SQL table names retain the "vumail_" prefix for schema compatibility
// with existing deployed databases; only the Go package has been renamed.
//
// Uses pure-Go modernc.org/sqlite (never CGO mattn/go-sqlite3).
// The database is opened in WAL mode with a single writer connection.
package identity

import (
	"database/sql"
	"embed"
	"fmt"
	"log"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed migrations/0001_identity.sql
var migrationsFS embed.FS

// openDB opens (or creates) the SQLite database at path, applies migrations,
// and returns a ready-to-use *sql.DB. Returns an error on any failure.
func openDB(path string) (*sql.DB, error) {
	dsn := path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("identity: open db: %w", err)
	}
	// Single writer — modernc sqlite is not safe for concurrent writers.
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("identity: ping db: %w", err)
	}
	if err := migrateDB(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("identity: migrate: %w", err)
	}
	return db, nil
}

// migrateDB applies the embedded SQL migration. Every statement uses
// IF NOT EXISTS so repeated calls are safe.
func migrateDB(db *sql.DB) error {
	sqlBytes, err := migrationsFS.ReadFile("migrations/0001_identity.sql")
	if err != nil {
		return err
	}
	_, err = db.Exec(string(sqlBytes))
	return err
}

// ─── Identity persistence ─────────────────────────────────────────────────────

// saveIdentity writes (or replaces) the identity row.
func (s *Store) saveIdentity(id *Identity) error {
	if s.db == nil {
		return nil
	}
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.db.Exec(`
		INSERT INTO vumail_identity(id, address, public_key_b64, private_key_enc, created_at, updated_at)
		VALUES('default', ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			address        = excluded.address,
			public_key_b64 = excluded.public_key_b64,
			private_key_enc= excluded.private_key_enc,
			updated_at     = excluded.updated_at`,
		id.Address, id.PublicKeyB64, id.PrivateKeyEnc, now, now,
	)
	if err != nil {
		return fmt.Errorf("identity: save identity: %w", err)
	}
	return nil
}

// loadIdentity reads the identity row. Returns (nil, nil) if no row exists.
func (s *Store) loadIdentity() (*Identity, error) {
	if s.db == nil {
		return nil, nil
	}
	var id Identity
	err := s.db.QueryRow(`
		SELECT address, public_key_b64, private_key_enc
		FROM vumail_identity WHERE id='default'`).
		Scan(&id.Address, &id.PublicKeyB64, &id.PrivateKeyEnc)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("identity: load identity: %w", err)
	}
	return &id, nil
}

// ─── Mailbox persistence ──────────────────────────────────────────────────────

// saveMailMessage stores a received message in the mailbox.
func (s *Store) saveMailMessage(m *MailMessage) error {
	if s.db == nil {
		return nil
	}
	_, err := s.db.Exec(`
		INSERT INTO vumail_mailbox(id, from_address, subject, body_encrypted, received_at, read)
		VALUES(?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO NOTHING`,
		m.ID, m.FromAddress, m.Subject, m.BodyEncrypted,
		m.ReceivedAt.UTC().Format(time.RFC3339), boolToInt(m.Read),
	)
	if err != nil {
		return fmt.Errorf("identity: save mail message: %w", err)
	}
	return nil
}

// listMailbox returns all mailbox messages ordered by received_at DESC.
func (s *Store) listMailbox() ([]*MailMessage, error) {
	if s.db == nil {
		return nil, nil
	}
	rows, err := s.db.Query(`
		SELECT id, from_address, subject, body_encrypted, received_at, read
		FROM vumail_mailbox
		ORDER BY received_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("identity: list mailbox: %w", err)
	}
	defer rows.Close()

	var msgs []*MailMessage
	for rows.Next() {
		var m MailMessage
		var receivedStr string
		var readInt int
		if err := rows.Scan(&m.ID, &m.FromAddress, &m.Subject, &m.BodyEncrypted, &receivedStr, &readInt); err != nil {
			return nil, fmt.Errorf("identity: scan mailbox row: %w", err)
		}
		m.ReceivedAt, _ = time.Parse(time.RFC3339, receivedStr)
		m.Read = readInt != 0
		msgs = append(msgs, &m)
	}
	return msgs, rows.Err()
}

// getMailMessage fetches a single mailbox message by id.
// Returns (nil, nil) if not found.
func (s *Store) getMailMessage(id string) (*MailMessage, error) {
	if s.db == nil {
		return nil, nil
	}
	var m MailMessage
	var receivedStr string
	var readInt int
	err := s.db.QueryRow(`
		SELECT id, from_address, subject, body_encrypted, received_at, read
		FROM vumail_mailbox WHERE id=?`, id).
		Scan(&m.ID, &m.FromAddress, &m.Subject, &m.BodyEncrypted, &receivedStr, &readInt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("identity: get mail message: %w", err)
	}
	m.ReceivedAt, _ = time.Parse(time.RFC3339, receivedStr)
	m.Read = readInt != 0
	return &m, nil
}

// listMailboxPaged returns a page of mailbox messages ordered by received_at DESC.
// limit 0 means no limit (return all).
func (s *Store) listMailboxPaged(limit, offset int) ([]*MailMessage, int, error) {
	if s.db == nil {
		return nil, 0, nil
	}
	var total int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM vumail_mailbox`).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("identity: count mailbox: %w", err)
	}
	q := `SELECT id, from_address, subject, body_encrypted, received_at, read
		FROM vumail_mailbox ORDER BY received_at DESC`
	args := []interface{}{}
	if limit > 0 {
		q += " LIMIT ? OFFSET ?"
		args = append(args, limit, offset)
	}
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("identity: list mailbox paged: %w", err)
	}
	defer rows.Close()
	var msgs []*MailMessage
	for rows.Next() {
		var m MailMessage
		var receivedStr string
		var readInt int
		if err := rows.Scan(&m.ID, &m.FromAddress, &m.Subject, &m.BodyEncrypted, &receivedStr, &readInt); err != nil {
			return nil, 0, fmt.Errorf("identity: scan mailbox row: %w", err)
		}
		m.ReceivedAt, _ = time.Parse(time.RFC3339, receivedStr)
		m.Read = readInt != 0
		msgs = append(msgs, &m)
	}
	return msgs, total, rows.Err()
}

// patchMailMessage updates the read flag and/or status (archived/deleted via
// a status column extension) of a mailbox message. Only non-empty/non-false
// fields in the patch are applied.
//
// Supported patch keys: "read" (bool), "archived" (bool), "deleted" (bool).
// For archived/deleted the schema uses additional columns added here if absent
// (via ALTER TABLE IF NOT EXISTS pattern). For simplicity the handler controls
// which SQL is run; this method accepts an explicit map.
func (s *Store) patchMailMessage(id string, fields map[string]interface{}) error {
	if s.db == nil {
		return nil
	}
	// Ensure optional columns exist (idempotent).
	for _, col := range []string{
		"ALTER TABLE vumail_mailbox ADD COLUMN archived INTEGER NOT NULL DEFAULT 0",
		"ALTER TABLE vumail_mailbox ADD COLUMN deleted  INTEGER NOT NULL DEFAULT 0",
	} {
		_, _ = s.db.Exec(col) // ignore "duplicate column" errors
	}
	if len(fields) == 0 {
		return nil
	}
	setClauses := ""
	args := []interface{}{}
	for k, v := range fields {
		if setClauses != "" {
			setClauses += ", "
		}
		setClauses += k + "=?"
		args = append(args, v)
	}
	args = append(args, id)
	_, err := s.db.Exec("UPDATE vumail_mailbox SET "+setClauses+" WHERE id=?", args...)
	if err != nil {
		return fmt.Errorf("identity: patch mail message: %w", err)
	}
	return nil
}

// ─── Outbox persistence ───────────────────────────────────────────────────────

// saveOutboxMessage stores a queued outbound message.
func (s *Store) saveOutboxMessage(m *OutboxMessage) error {
	if s.db == nil {
		return nil
	}
	var sentStr interface{}
	if !m.SentAt.IsZero() {
		sentStr = m.SentAt.UTC().Format(time.RFC3339)
	}
	_, err := s.db.Exec(`
		INSERT INTO vumail_outbox(id, to_address, subject, body_encrypted, queued_at, sent_at, status)
		VALUES(?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			sent_at = excluded.sent_at,
			status  = excluded.status`,
		m.ID, m.ToAddress, m.Subject, m.BodyEncrypted,
		m.QueuedAt.UTC().Format(time.RFC3339), sentStr, m.Status,
	)
	if err != nil {
		return fmt.Errorf("identity: save outbox message: %w", err)
	}
	return nil
}

// listOutbox returns all outbox messages ordered by queued_at DESC.
func (s *Store) listOutbox() ([]*OutboxMessage, error) {
	if s.db == nil {
		return nil, nil
	}
	rows, err := s.db.Query(`
		SELECT id, to_address, subject, body_encrypted, queued_at, sent_at, status
		FROM vumail_outbox
		ORDER BY queued_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("identity: list outbox: %w", err)
	}
	defer rows.Close()

	var msgs []*OutboxMessage
	for rows.Next() {
		var m OutboxMessage
		var queuedStr string
		var sentStr sql.NullString
		if err := rows.Scan(&m.ID, &m.ToAddress, &m.Subject, &m.BodyEncrypted, &queuedStr, &sentStr, &m.Status); err != nil {
			return nil, fmt.Errorf("identity: scan outbox row: %w", err)
		}
		m.QueuedAt, _ = time.Parse(time.RFC3339, queuedStr)
		if sentStr.Valid {
			m.SentAt, _ = time.Parse(time.RFC3339, sentStr.String)
		}
		msgs = append(msgs, &m)
	}
	return msgs, rows.Err()
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// Close closes the underlying SQLite database (safe to call multiple times).
func (s *Store) Close() error {
	if s.db == nil {
		return nil
	}
	log.Printf("[identity] closing database")
	return s.db.Close()
}

// Store wraps a SQLite database and provides typed access to identity tables
// (SQL table names use the legacy "vumail_" prefix for schema compatibility).
type Store struct {
	db *sql.DB
}

// NewStore opens (or creates) the SQLite database at dbPath and runs migrations.
// Pass an empty string to run in degraded (in-memory-only) mode.
func NewStore(dbPath string) (*Store, error) {
	if dbPath == "" {
		log.Printf("[identity] no db path — running in degraded (in-memory) mode")
		return &Store{}, nil
	}
	db, err := openDB(dbPath)
	if err != nil {
		// Degraded mode: log and continue without persistence.
		log.Printf("[identity] store: open failed (%v) — degraded mode", err)
		return &Store{}, nil
	}
	return &Store{db: db}, nil
}
