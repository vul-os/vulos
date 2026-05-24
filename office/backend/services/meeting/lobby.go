package meeting

import (
	"database/sql"
	"fmt"
	"log"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// LobbyState persists lobby state in a SQLite database so that server restarts
// survive in-progress meetings. The organizer must be present to admit
// participants, and the DB is authoritative for lobby state.
//
// Status enum: "waiting" | "admitted" | "denied".
// "denied" entries persist until the room ends; room cleanup deletes all rows.
//
// Security: all lobby state changes are gated by the join token claim.
// Only the organizer may call Admit/Deny.

// WaitingEntry is a participant waiting in the lobby.
type WaitingEntry struct {
	Nonce       string    `json:"nonce"`         // from join token — ties lobby slot to a specific request
	AccountID   string    `json:"account_id"`    // empty for anonymous
	DisplayName string    `json:"display_name"`
	Email       string    `json:"email,omitempty"`
	IP          string    `json:"ip"`
	UserAgent   string    `json:"user_agent"`
	ArrivedAt   time.Time `json:"arrived_at"`
}

// LobbyManager manages lobby state backed by SQLite.
type LobbyManager struct {
	mu sync.Mutex
	db *sql.DB
}

var defaultLobby *LobbyManager
var defaultLobbyOnce sync.Once

// Default returns the process-wide LobbyManager (lazy-initialised with an
// in-memory SQLite database suitable for single-node deployments).
func Default() *LobbyManager {
	defaultLobbyOnce.Do(func() {
		lm, err := NewLobbyManager(":memory:")
		if err != nil {
			// Fatal during init — panic with a clear message.
			panic(fmt.Sprintf("meeting: failed to open lobby DB: %v", err))
		}
		defaultLobby = lm
	})
	return defaultLobby
}

// NewLobbyManager opens (or creates) a SQLite-backed LobbyManager at dsn.
// Use ":memory:" for in-process storage or a file path for persistence.
func NewLobbyManager(dsn string) (*LobbyManager, error) {
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("lobby: open db: %w", err)
	}
	if err := initLobbyDB(db); err != nil {
		db.Close()
		return nil, err
	}
	return &LobbyManager{db: db}, nil
}

func initLobbyDB(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS lobby_state (
			room_id      TEXT NOT NULL,
			nonce        TEXT NOT NULL,
			account_id   TEXT NOT NULL DEFAULT '',
			display_name TEXT NOT NULL DEFAULT '',
			email        TEXT NOT NULL DEFAULT '',
			ip           TEXT NOT NULL DEFAULT '',
			user_agent   TEXT NOT NULL DEFAULT '',
			status       TEXT NOT NULL DEFAULT 'waiting',
			requested_at INTEGER NOT NULL,
			PRIMARY KEY (room_id, nonce)
		);
		CREATE INDEX IF NOT EXISTS idx_lobby_room_status ON lobby_state(room_id, status);
	`)
	return err
}

// Enter adds a participant to the waiting queue with status "waiting".
// Idempotent on (room_id, nonce): silently ignores duplicates.
func (lm *LobbyManager) Enter(roomID string, e *WaitingEntry) {
	lm.mu.Lock()
	defer lm.mu.Unlock()
	at := e.ArrivedAt
	if at.IsZero() {
		at = time.Now()
	}
	_, err := lm.db.Exec(`
		INSERT OR IGNORE INTO lobby_state
			(room_id, nonce, account_id, display_name, email, ip, user_agent, status, requested_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, 'waiting', ?)`,
		roomID, e.Nonce, e.AccountID, e.DisplayName, e.Email, e.IP, e.UserAgent, at.Unix(),
	)
	if err != nil {
		log.Printf("[lobby] Enter error: %v", err)
	}
}

// List returns all waiting entries for a room (status = "waiting"), ordered by arrival.
func (lm *LobbyManager) List(roomID string) []*WaitingEntry {
	lm.mu.Lock()
	defer lm.mu.Unlock()
	rows, err := lm.db.Query(`
		SELECT nonce, account_id, display_name, email, ip, user_agent, requested_at
		FROM lobby_state
		WHERE room_id = ? AND status = 'waiting'
		ORDER BY requested_at ASC`, roomID,
	)
	if err != nil {
		log.Printf("[lobby] List error: %v", err)
		return nil
	}
	defer rows.Close()

	var out []*WaitingEntry
	for rows.Next() {
		var e WaitingEntry
		var ts int64
		if err := rows.Scan(&e.Nonce, &e.AccountID, &e.DisplayName, &e.Email, &e.IP, &e.UserAgent, &ts); err != nil {
			continue
		}
		e.ArrivedAt = time.Unix(ts, 0)
		out = append(out, &e)
	}
	return out
}

// Admit transitions a participant from "waiting" to "admitted". Returns true if found.
func (lm *LobbyManager) Admit(roomID, nonce string) bool {
	lm.mu.Lock()
	defer lm.mu.Unlock()
	res, err := lm.db.Exec(`
		UPDATE lobby_state SET status = 'admitted'
		WHERE room_id = ? AND nonce = ? AND status = 'waiting'`, roomID, nonce,
	)
	if err != nil {
		log.Printf("[lobby] Admit error: %v", err)
		return false
	}
	n, _ := res.RowsAffected()
	return n > 0
}

// AdmitAll transitions all waiting participants to "admitted" and returns their entries.
func (lm *LobbyManager) AdmitAll(roomID string) []*WaitingEntry {
	lm.mu.Lock()
	defer lm.mu.Unlock()

	rows, err := lm.db.Query(`
		SELECT nonce, account_id, display_name, email, ip, user_agent, requested_at
		FROM lobby_state
		WHERE room_id = ? AND status = 'waiting'`, roomID,
	)
	if err != nil {
		log.Printf("[lobby] AdmitAll query error: %v", err)
		return nil
	}
	var entries []*WaitingEntry
	for rows.Next() {
		var e WaitingEntry
		var ts int64
		if err := rows.Scan(&e.Nonce, &e.AccountID, &e.DisplayName, &e.Email, &e.IP, &e.UserAgent, &ts); err != nil {
			continue
		}
		e.ArrivedAt = time.Unix(ts, 0)
		entries = append(entries, &e)
	}
	rows.Close()

	if len(entries) > 0 {
		_, err = lm.db.Exec(`
			UPDATE lobby_state SET status = 'admitted'
			WHERE room_id = ? AND status = 'waiting'`, roomID,
		)
		if err != nil {
			log.Printf("[lobby] AdmitAll update error: %v", err)
		}
	}
	return entries
}

// Deny transitions a participant from "waiting" to "denied".
// The "denied" row persists until the room is cleaned up via CleanupRoom.
func (lm *LobbyManager) Deny(roomID, nonce string) {
	lm.mu.Lock()
	defer lm.mu.Unlock()
	_, err := lm.db.Exec(`
		INSERT OR REPLACE INTO lobby_state
			(room_id, nonce, account_id, display_name, email, ip, user_agent, status, requested_at)
		SELECT room_id, nonce, account_id, display_name, email, ip, user_agent, 'denied', requested_at
		FROM lobby_state
		WHERE room_id = ? AND nonce = ?
		UNION ALL
		SELECT ?, ?, '', '', '', '', '', 'denied', ?
		WHERE NOT EXISTS (SELECT 1 FROM lobby_state WHERE room_id = ? AND nonce = ?)
		LIMIT 1`,
		roomID, nonce,
		roomID, nonce, time.Now().Unix(),
		roomID, nonce,
	)
	if err != nil {
		// Simpler fallback: just upsert with denied status.
		lm.db.Exec(`
			INSERT INTO lobby_state (room_id, nonce, status, requested_at)
			VALUES (?, ?, 'denied', ?)
			ON CONFLICT(room_id, nonce) DO UPDATE SET status = 'denied'`,
			roomID, nonce, time.Now().Unix(),
		)
	}
}

// IsDenied checks whether a nonce was previously denied for this room.
func (lm *LobbyManager) IsDenied(roomID, nonce string) bool {
	lm.mu.Lock()
	defer lm.mu.Unlock()
	var status string
	err := lm.db.QueryRow(`
		SELECT status FROM lobby_state WHERE room_id = ? AND nonce = ?`, roomID, nonce,
	).Scan(&status)
	return err == nil && status == "denied"
}

// CleanupRoom removes all lobby_state rows for a room (called when the room ends).
func (lm *LobbyManager) CleanupRoom(roomID string) {
	lm.mu.Lock()
	defer lm.mu.Unlock()
	lm.db.Exec(`DELETE FROM lobby_state WHERE room_id = ?`, roomID)
}
