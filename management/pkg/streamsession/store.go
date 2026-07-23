package streamsession

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"time"

	"github.com/vul-os/vulos-management/pkg/cpdb"
)

// HostKind discriminates between a cloud-allocated GPU instance and a
// user-owned (BYO) GPU box.
type HostKind string

const (
	// HostKindCloudGPU means the host is a cloud-provisioned GPU instance and
	// the gpusku per-GPU-hour meter is active for the session's lifetime.
	HostKindCloudGPU HostKind = "cloud_gpu"
	// HostKindBYOGPU means the host is a user-owned BYO GPU box registered
	// with the fabric (no cloud GPU charge).
	HostKindBYOGPU HostKind = "byo_gpu"
)

// SessionState is the lifecycle state machine for a streaming session.
type SessionState string

const (
	StateAllocating SessionState = "allocating"
	StateActive     SessionState = "active"
	StateTornDown   SessionState = "torn_down"
	StateFailed     SessionState = "failed"
)

// Session is one client→host streaming negotiation.
type Session struct {
	ID            string       `json:"id"`
	AccountID     string       `json:"account_id"`
	HostKind      HostKind     `json:"host_kind"`
	HostULID      string       `json:"host_ulid"`
	GPUType       string       `json:"gpu_type"` // catalogue key for cloud_gpu; "" for byo
	RelayEndpoint string       `json:"relay_endpoint"`
	Gaming        bool         `json:"gaming"` // GAME-SESSION-01: GPU cloud-gaming session
	State         SessionState `json:"state"`
	CreatedAt     time.Time    `json:"created_at"`
	UpdatedAt     time.Time    `json:"updated_at"`
	ClosedAt      *time.Time   `json:"closed_at,omitempty"`
}

// ErrSessionNotFound is returned when no row matches the requested id.
var ErrSessionNotFound = errors.New("streamsession: session not found")

// Store persists session lifecycle rows via the cpdb dual-backend seam.
type Store struct {
	db *cpdb.DB
}

// Open runs the embedded migrations against db and returns a Store.
func Open(db *cpdb.DB) (*Store, error) {
	subFS, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("streamsession: embed sub: %w", err)
	}
	if err := db.Migrate(subFS); err != nil {
		return nil, fmt.Errorf("streamsession: migrate: %w", err)
	}
	return &Store{db: db}, nil
}

// Close closes the underlying database.
func (s *Store) Close() error { return s.db.Close() }

// Ping pings the DB (for /readyz).
func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

// NewID returns a fresh URL-safe random session id.
func NewID() (string, error) {
	b := make([]byte, 18) // 24-char base64
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("streamsession: random: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// Insert persists a new session row in state 'allocating'.
func (s *Store) Insert(ctx context.Context, sess Session) (Session, error) {
	if sess.ID == "" || sess.AccountID == "" || sess.HostULID == "" {
		return Session{}, errors.New("streamsession: id, account_id, host_ulid required")
	}
	if sess.HostKind != HostKindCloudGPU && sess.HostKind != HostKindBYOGPU {
		return Session{}, fmt.Errorf("streamsession: unknown host_kind %q", string(sess.HostKind))
	}
	now := time.Now().UTC()
	sess.CreatedAt = now
	sess.UpdatedAt = now
	if sess.State == "" {
		sess.State = StateAllocating
	}
	gaming := 0
	if sess.Gaming {
		gaming = 1
	}
	_, err := s.db.ExecContext(ctx, s.db.Rebind(`
		INSERT INTO stream_sessions
			(id, account_id, host_kind, host_ulid, gpu_type, relay_endpoint,
			 gaming, state, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`),
		sess.ID, sess.AccountID, string(sess.HostKind), sess.HostULID,
		sess.GPUType, sess.RelayEndpoint, gaming, string(sess.State),
		now.Format(time.RFC3339), now.Format(time.RFC3339),
	)
	if err != nil {
		return Session{}, fmt.Errorf("streamsession: insert: %w", err)
	}
	return sess, nil
}

// Get returns the session for id. ErrSessionNotFound when absent.
func (s *Store) Get(ctx context.Context, id string) (Session, error) {
	var (
		sess    Session
		created string
		updated string
		closed  sql.NullString
		kind    string
		state   string
		gaming  int
	)
	err := s.db.QueryRowContext(ctx, s.db.Rebind(`
		SELECT id, account_id, host_kind, host_ulid, gpu_type, relay_endpoint,
		       gaming, state, created_at, updated_at, closed_at
		  FROM stream_sessions WHERE id = ?`), id,
	).Scan(&sess.ID, &sess.AccountID, &kind, &sess.HostULID, &sess.GPUType,
		&sess.RelayEndpoint, &gaming, &state, &created, &updated, &closed)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, ErrSessionNotFound
	}
	if err != nil {
		return Session{}, fmt.Errorf("streamsession: get: %w", err)
	}
	sess.HostKind = HostKind(kind)
	sess.Gaming = gaming != 0
	sess.State = SessionState(state)
	sess.CreatedAt, _ = time.Parse(time.RFC3339, created)
	sess.UpdatedAt, _ = time.Parse(time.RFC3339, updated)
	if closed.Valid {
		if t, perr := time.Parse(time.RFC3339, closed.String); perr == nil {
			sess.ClosedAt = &t
		}
	}
	return sess, nil
}

// SetState transitions a session to newState and updates updated_at. When
// newState is StateTornDown, closed_at is also set.
func (s *Store) SetState(ctx context.Context, id string, newState SessionState) error {
	now := time.Now().UTC()
	var (
		res sql.Result
		err error
	)
	if newState == StateTornDown {
		res, err = s.db.ExecContext(ctx, s.db.Rebind(`
			UPDATE stream_sessions
			   SET state = ?, updated_at = ?, closed_at = ?
			 WHERE id = ?`),
			string(newState), now.Format(time.RFC3339), now.Format(time.RFC3339), id,
		)
	} else {
		res, err = s.db.ExecContext(ctx, s.db.Rebind(`
			UPDATE stream_sessions
			   SET state = ?, updated_at = ?
			 WHERE id = ?`),
			string(newState), now.Format(time.RFC3339), id,
		)
	}
	if err != nil {
		return fmt.Errorf("streamsession: set state: %w", err)
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		return ErrSessionNotFound
	}
	return nil
}

// ListByAccount returns sessions for accountID newest-first, capped at limit.
func (s *Store) ListByAccount(ctx context.Context, accountID string, limit int) ([]Session, error) {
	q := `SELECT id, account_id, host_kind, host_ulid, gpu_type, relay_endpoint,
	             gaming, state, created_at, updated_at, closed_at
	        FROM stream_sessions WHERE account_id = ?
	       ORDER BY created_at DESC`
	args := []any{accountID}
	if limit > 0 {
		q += " LIMIT ?"
		args = append(args, limit)
	}
	rows, err := s.db.QueryContext(ctx, s.db.Rebind(q), args...)
	if err != nil {
		return nil, fmt.Errorf("streamsession: list: %w", err)
	}
	defer rows.Close()
	var out []Session
	for rows.Next() {
		var (
			sess    Session
			created string
			updated string
			closed  sql.NullString
			kind    string
			state   string
			gaming  int
		)
		if err := rows.Scan(&sess.ID, &sess.AccountID, &kind, &sess.HostULID, &sess.GPUType,
			&sess.RelayEndpoint, &gaming, &state, &created, &updated, &closed); err != nil {
			return nil, fmt.Errorf("streamsession: scan: %w", err)
		}
		sess.HostKind = HostKind(kind)
		sess.Gaming = gaming != 0
		sess.State = SessionState(state)
		sess.CreatedAt, _ = time.Parse(time.RFC3339, created)
		sess.UpdatedAt, _ = time.Parse(time.RFC3339, updated)
		if closed.Valid {
			if t, perr := time.Parse(time.RFC3339, closed.String); perr == nil {
				sess.ClosedAt = &t
			}
		}
		out = append(out, sess)
	}
	return out, rows.Err()
}

// CountActiveGamingByAccount returns how many gaming sessions the account has in
// a non-terminal state (allocating OR active). Used to enforce the
// one-active-gaming-session-per-user policy and the per-tier concurrent GPU
// session cap (gpusku.SessionCapForTier). Terminal states (torn_down, failed)
// are excluded so a torn-down session never counts against the live cap.
func (s *Store) CountActiveGamingByAccount(ctx context.Context, accountID string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, s.db.Rebind(`
		SELECT COUNT(*) FROM stream_sessions
		 WHERE account_id = ? AND gaming = 1 AND state IN (?, ?)`),
		accountID, string(StateAllocating), string(StateActive),
	).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("streamsession: count active gaming: %w", err)
	}
	return n, nil
}
