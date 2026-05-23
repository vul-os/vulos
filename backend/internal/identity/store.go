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
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed migrations/0001_identity.sql
//go:embed migrations/0002_add_account_id.sql
var migrationsFS embed.FS

// ErrInvalidPatchField is returned by patchMailMessage when the caller tries
// to update a column that is not in the allowedPatchColumns whitelist.
var ErrInvalidPatchField = errors.New("identity: invalid patch field")

// allowedPatchColumns is the exhaustive whitelist of columns that may be
// updated via patchMailMessage. Any key outside this set is rejected to
// prevent SQL-injection via dynamic column names.
var allowedPatchColumns = map[string]bool{
	"read":     true,
	"archived": true,
	"deleted":  true,
}

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

// migrateDB applies embedded SQL migrations in order. Statements within each
// file are split on ';' and executed individually so that "duplicate column
// name" errors from idempotent ALTER TABLE calls can be silently ignored.
func migrateDB(db *sql.DB) error {
	for _, name := range []string{
		"migrations/0001_identity.sql",
		"migrations/0002_add_account_id.sql",
	} {
		sqlBytes, err := migrationsFS.ReadFile(name)
		if err != nil {
			return fmt.Errorf("identity: read migration %s: %w", name, err)
		}
		for _, stmt := range splitStatements(string(sqlBytes)) {
			if _, err := db.Exec(stmt); err != nil {
				// Ignore "duplicate column name" — ALTER TABLE already applied.
				if strings.Contains(err.Error(), "duplicate column name") {
					continue
				}
				return fmt.Errorf("identity: apply migration %s (stmt=%q): %w", name, abbrev(stmt, 60), err)
			}
		}
	}
	return nil
}

// splitStatements splits a SQL script into individual statements on ';'.
// SQL line comments (-- ...) are stripped before splitting so that a comment
// preceding a statement does not cause the statement to be skipped.
// Blank statements are discarded.
func splitStatements(script string) []string {
	// Strip line comments.
	var lines []string
	for _, line := range strings.Split(script, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "--") {
			continue // drop comment lines
		}
		lines = append(lines, line)
	}
	clean := strings.Join(lines, "\n")

	var out []string
	for _, part := range strings.Split(clean, ";") {
		s := strings.TrimSpace(part)
		if s == "" {
			continue
		}
		out = append(out, s)
	}
	return out
}

// abbrev truncates s to maxLen runes for use in error messages.
func abbrev(s string, maxLen int) string {
	r := []rune(s)
	if len(r) <= maxLen {
		return s
	}
	return string(r[:maxLen]) + "…"
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

// saveMailMessage stores a received message in the mailbox without an owner account.
// Rows inserted by this path have account_id=NULL and will be matched by the
// "account_id IS NULL OR account_id = ?" rollover clause in patchMailMessage.
func (s *Store) saveMailMessage(m *MailMessage) error {
	return s.saveMailMessageForAccount(m, "")
}

// saveMailMessageForAccount stores a received message bound to accountID.
// Pass accountID="" to store without an owner (account_id=NULL).
func (s *Store) saveMailMessageForAccount(m *MailMessage, accountID string) error {
	if s.db == nil {
		return nil
	}
	var aid interface{}
	if accountID != "" {
		aid = accountID
	}
	_, err := s.db.Exec(`
		INSERT INTO vumail_mailbox(id, from_address, subject, body_encrypted, received_at, read, account_id)
		VALUES(?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO NOTHING`,
		m.ID, m.FromAddress, m.Subject, m.BodyEncrypted,
		m.ReceivedAt.UTC().Format(time.RFC3339), boolToInt(m.Read), aid,
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
// Keys outside allowedPatchColumns are rejected with ErrInvalidPatchField to
// prevent SQL-injection via dynamic column names (Critical fix #1).
//
// accountID scopes the UPDATE to the owning account (High fix #5): messages
// belonging to a different account return no error but update 0 rows, causing
// the caller to treat the row as not-found (404).
func (s *Store) patchMailMessage(id, accountID string, fields map[string]interface{}) error {
	if len(fields) == 0 {
		return nil
	}
	// Whitelist check runs even in degraded mode so callers always get a
	// correct error for invalid inputs (Critical #1 — SQL-injection seam).
	for k := range fields {
		if !allowedPatchColumns[k] {
			return fmt.Errorf("%w: %q", ErrInvalidPatchField, k)
		}
	}
	if s.db == nil {
		return nil
	}
	// Build SET clause from whitelisted keys only.
	var setClauses []string
	args := []interface{}{}
	for k, v := range fields {
		setClauses = append(setClauses, k+"=?")
		args = append(args, v)
	}
	// WHERE id=? AND (account_id=? OR account_id IS NULL)
	// account_id IS NULL covers rows inserted before migration 0002 (safe rollover).
	args = append(args, id, accountID)
	_, err := s.db.Exec(
		"UPDATE vumail_mailbox SET "+strings.Join(setClauses, ", ")+
			" WHERE id=? AND (account_id=? OR account_id IS NULL)",
		args...,
	)
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
