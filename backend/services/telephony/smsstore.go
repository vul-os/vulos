// Package telephony — SMS SQLite persistence layer.
//
// SMSStore opens (or creates) a SQLite database that holds all SMS messages.
// The schema is intentionally simple:
//
//	messages(id, modem_path, peer, direction, body, state, timestamp_ms)
//
// "peer" is the remote phone number (E.164 or whatever the modem reports).
// "direction" is "in" | "out".
// "state" is "sent" | "delivered" | "failed" | "received".
//
// Threads are derived at query time by grouping on (modem_path, peer).
package telephony

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver (no cgo)
)

const smsSchema = `
CREATE TABLE IF NOT EXISTS sms_messages (
	id            INTEGER PRIMARY KEY AUTOINCREMENT,
	modem_path    TEXT    NOT NULL DEFAULT '',
	peer          TEXT    NOT NULL,
	direction     TEXT    NOT NULL CHECK(direction IN ('in','out')),
	body          TEXT    NOT NULL DEFAULT '',
	state         TEXT    NOT NULL DEFAULT 'received',
	timestamp_ms  INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_sms_peer   ON sms_messages(peer, timestamp_ms);
CREATE INDEX IF NOT EXISTS idx_sms_modem  ON sms_messages(modem_path, timestamp_ms);
`

// SMSRecord is a single SMS message row.
type SMSRecord struct {
	ID          int64  `json:"id"`
	ModemPath   string `json:"modem_path"`
	Peer        string `json:"peer"`
	Direction   string `json:"direction"` // "in" | "out"
	Body        string `json:"body"`
	State       string `json:"state"`        // "sent" | "delivered" | "failed" | "received"
	TimestampMs int64  `json:"timestamp_ms"` // Unix milliseconds
}

// SMSThread summarises one conversation thread.
type SMSThread struct {
	Peer         string `json:"peer"`
	ModemPath    string `json:"modem_path"`
	LastBody     string `json:"last_body"`
	LastTime     int64  `json:"last_timestamp_ms"`
	Direction    string `json:"last_direction"`
	MessageCount int    `json:"message_count"`
}

// SMSStore wraps the SQLite database for SMS history.
type SMSStore struct {
	db *sql.DB
}

// SMSStoreOpener is a function that opens an SMSStore; swap in tests.
type SMSStoreOpener func(dir string) (*SMSStore, error)

// DefaultSMSStoreOpener opens (or creates) the store at dir/sms.db.
func DefaultSMSStoreOpener(dir string) (*SMSStore, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("smsstore: mkdir %q: %w", dir, err)
	}
	return OpenSMSStore(filepath.Join(dir, "sms.db"))
}

// OpenSMSStore opens the SQLite database at dbPath and applies the schema.
func OpenSMSStore(dbPath string) (*SMSStore, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("smsstore: open %q: %w", dbPath, err)
	}
	db.SetMaxOpenConns(1) // SQLite writer serialisation
	if _, err := db.Exec(smsSchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("smsstore: apply schema: %w", err)
	}
	return &SMSStore{db: db}, nil
}

// OpenSMSStoreInMemory opens an in-memory SQLite database (for tests).
func OpenSMSStoreInMemory() (*SMSStore, error) {
	return OpenSMSStore(":memory:")
}

// Close releases the database connection.
func (s *SMSStore) Close() error {
	return s.db.Close()
}

// Insert persists a new SMS record and returns its auto-assigned ID.
func (s *SMSStore) Insert(rec SMSRecord) (int64, error) {
	if rec.TimestampMs == 0 {
		rec.TimestampMs = time.Now().UnixMilli()
	}
	res, err := s.db.Exec(
		`INSERT INTO sms_messages(modem_path, peer, direction, body, state, timestamp_ms)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		rec.ModemPath, rec.Peer, rec.Direction, rec.Body, rec.State, rec.TimestampMs,
	)
	if err != nil {
		return 0, fmt.Errorf("smsstore: insert: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	return id, nil
}

// Delete removes a message by its ID. Returns nil even when the row did not
// exist (idempotent).
func (s *SMSStore) Delete(id int64) error {
	_, err := s.db.Exec(`DELETE FROM sms_messages WHERE id = ?`, id)
	return err
}

// List returns messages for a given peer, newest-first.
// If peer is empty, all messages are returned.
// limit ≤ 0 means no limit.
func (s *SMSStore) List(peer string, limit int) ([]SMSRecord, error) {
	var (
		rows *sql.Rows
		err  error
	)
	if peer == "" {
		if limit > 0 {
			rows, err = s.db.Query(
				`SELECT id, modem_path, peer, direction, body, state, timestamp_ms
				 FROM sms_messages ORDER BY timestamp_ms DESC LIMIT ?`, limit)
		} else {
			rows, err = s.db.Query(
				`SELECT id, modem_path, peer, direction, body, state, timestamp_ms
				 FROM sms_messages ORDER BY timestamp_ms DESC`)
		}
	} else {
		if limit > 0 {
			rows, err = s.db.Query(
				`SELECT id, modem_path, peer, direction, body, state, timestamp_ms
				 FROM sms_messages WHERE peer = ? ORDER BY timestamp_ms DESC LIMIT ?`, peer, limit)
		} else {
			rows, err = s.db.Query(
				`SELECT id, modem_path, peer, direction, body, state, timestamp_ms
				 FROM sms_messages WHERE peer = ? ORDER BY timestamp_ms DESC`, peer)
		}
	}
	if err != nil {
		return nil, fmt.Errorf("smsstore: list: %w", err)
	}
	defer rows.Close()
	return scanRecords(rows)
}

// Threads returns a summary of all conversations (one entry per unique peer),
// sorted by last-message time, newest-first.
func (s *SMSStore) Threads() ([]SMSThread, error) {
	// Use a CTE: compute per-peer totals and latest-message details separately,
	// then join so COUNT(*) covers all messages, not just the latest row.
	rows, err := s.db.Query(`
		WITH counts AS (
			SELECT peer, COUNT(*) AS cnt FROM sms_messages GROUP BY peer
		),
		latest AS (
			SELECT m.peer, m.modem_path, m.body, m.timestamp_ms, m.direction
			FROM sms_messages m
			INNER JOIN (
				SELECT peer, MAX(timestamp_ms) AS max_ts FROM sms_messages GROUP BY peer
			) mx ON m.peer = mx.peer AND m.timestamp_ms = mx.max_ts
		)
		SELECT l.peer, l.modem_path, l.body, l.timestamp_ms, l.direction, c.cnt
		FROM latest l
		JOIN counts c ON l.peer = c.peer
		ORDER BY l.timestamp_ms DESC
	`)
	if err != nil {
		return nil, fmt.Errorf("smsstore: threads: %w", err)
	}
	defer rows.Close()

	var threads []SMSThread
	for rows.Next() {
		var t SMSThread
		if err := rows.Scan(&t.Peer, &t.ModemPath, &t.LastBody, &t.LastTime, &t.Direction, &t.MessageCount); err != nil {
			return nil, err
		}
		threads = append(threads, t)
	}
	return threads, rows.Err()
}

// Search performs a case-insensitive substring search on message bodies.
// Results are sorted by timestamp descending.
func (s *SMSStore) Search(query string, limit int) ([]SMSRecord, error) {
	pattern := "%" + strings.ReplaceAll(query, "%", "\\%") + "%"
	var (
		rows *sql.Rows
		err  error
	)
	if limit > 0 {
		rows, err = s.db.Query(
			`SELECT id, modem_path, peer, direction, body, state, timestamp_ms
			 FROM sms_messages
			 WHERE body LIKE ? ESCAPE '\'
			 ORDER BY timestamp_ms DESC LIMIT ?`, pattern, limit)
	} else {
		rows, err = s.db.Query(
			`SELECT id, modem_path, peer, direction, body, state, timestamp_ms
			 FROM sms_messages
			 WHERE body LIKE ? ESCAPE '\'
			 ORDER BY timestamp_ms DESC`, pattern)
	}
	if err != nil {
		return nil, fmt.Errorf("smsstore: search: %w", err)
	}
	defer rows.Close()
	return scanRecords(rows)
}

// scanRecords reads all rows into a slice of SMSRecord.
func scanRecords(rows *sql.Rows) ([]SMSRecord, error) {
	var records []SMSRecord
	for rows.Next() {
		var r SMSRecord
		if err := rows.Scan(&r.ID, &r.ModemPath, &r.Peer, &r.Direction, &r.Body, &r.State, &r.TimestampMs); err != nil {
			return nil, err
		}
		records = append(records, r)
	}
	if records == nil {
		records = []SMSRecord{}
	}
	return records, rows.Err()
}
