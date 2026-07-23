package support

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	dbmigrate "vulos/backend/internal/migrate"

	_ "modernc.org/sqlite" // pure-Go SQLite driver (no CGo)
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Store is the box-local support-requests store. Single-owner scope: every
// method is account-scoped (account_id = the caller's local user ID), the
// same isolation pattern used by services/integrations/selfhost.Store.
type Store struct {
	db *sql.DB
}

// OpenStore opens (or creates) the support-requests SQLite store at
// <dbDir>/support.db. If dbDir is empty an in-memory store is used (tests).
// WAL + busy-timeout, idempotent schema.
func OpenStore(dbDir string) (*Store, error) {
	dsn := "file::memory:?cache=shared&_pragma=busy_timeout(5000)"
	if dbDir != "" {
		path := filepath.Join(dbDir, "support.db")
		dsn = "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("support: open db: %w", err)
	}
	db.SetMaxOpenConns(1) // single-writer file; serialize writers
	s := &Store{db: db}
	if err := dbmigrate.Apply(db, migrationsFS, "migrations"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("support: migrate: %w", err)
	}
	return s, nil
}

// Close closes the underlying DB.
func (s *Store) Close() error { return s.db.Close() }

// Submit creates a new support request after enforcing the tier wall (see
// TicketChannelFor). priority defaults to PriorityP3 when empty.
func (s *Store) Submit(ctx context.Context, accountID, tier, priority, subject, body string) (Request, error) {
	channel, err := TicketChannelFor(tier)
	if err != nil {
		return Request{}, err // ErrNoTicketChannel
	}
	switch priority {
	case PriorityP1, PriorityP2, PriorityP3:
		// valid
	default:
		priority = PriorityP3
	}
	now := time.Now().UTC()
	breachAt := BusinessDeadline(now, tier, priority)

	res, err := s.db.ExecContext(ctx, `
INSERT INTO support_requests
  (account_id, tier, priority, channel, subject, body, state, breach_at, opened_at)
VALUES (?, ?, ?, ?, ?, ?, 'open', ?, ?)`,
		accountID, tier, priority, channel, subject, body,
		breachAt.Unix(), now.Unix())
	if err != nil {
		return Request{}, fmt.Errorf("support: submit: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Request{}, fmt.Errorf("support: submit: last insert id: %w", err)
	}
	return Request{
		ID:        id,
		AccountID: accountID,
		Tier:      tier,
		Priority:  priority,
		Channel:   channel,
		Subject:   subject,
		Body:      body,
		State:     "open",
		BreachAt:  breachAt,
		OpenedAt:  now,
	}, nil
}

// Get returns a request by ID.
func (s *Store) Get(ctx context.Context, id int64) (Request, error) {
	return s.scanRequest(s.db.QueryRowContext(ctx, `
SELECT id, account_id, tier, priority, channel, subject, body, state,
       breach_at, opened_at, resolved_at
FROM support_requests WHERE id = ?`, id))
}

// List returns an account's requests, newest first.
func (s *Store) List(ctx context.Context, accountID string) ([]Request, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, account_id, tier, priority, channel, subject, body, state,
       breach_at, opened_at, resolved_at
FROM support_requests WHERE account_id = ?
ORDER BY opened_at DESC`, accountID)
	if err != nil {
		return nil, fmt.Errorf("support: list: %w", err)
	}
	defer rows.Close()

	out := []Request{}
	for rows.Next() {
		req, err := s.scanRequest(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, req)
	}
	return out, rows.Err()
}

// CloseRequest marks a request resolved. callerAccountID must match the
// request's owner (ErrForbidden otherwise).
func (s *Store) CloseRequest(ctx context.Context, id int64, callerAccountID string) error {
	req, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	if req.AccountID != callerAccountID {
		return ErrForbidden
	}
	if req.State == "closed" {
		return ErrAlreadyClosed
	}
	now := time.Now().UTC()
	_, err = s.db.ExecContext(ctx,
		`UPDATE support_requests SET state='closed', resolved_at=? WHERE id=?`,
		now.Unix(), id)
	if err != nil {
		return fmt.Errorf("support: close: %w", err)
	}
	return nil
}

// ─── scanRequest parses a single row from either QueryRowContext or rows.Scan ─

type rowScanner interface {
	Scan(dest ...any) error
}

func (s *Store) scanRequest(row rowScanner) (Request, error) {
	var req Request
	var breachAt, openedAt int64
	var resolvedAt sql.NullInt64

	err := row.Scan(
		&req.ID, &req.AccountID, &req.Tier, &req.Priority, &req.Channel,
		&req.Subject, &req.Body, &req.State,
		&breachAt, &openedAt, &resolvedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Request{}, ErrNotFound
	}
	if err != nil {
		return Request{}, fmt.Errorf("support: scan: %w", err)
	}

	req.BreachAt = time.Unix(breachAt, 0).UTC()
	req.OpenedAt = time.Unix(openedAt, 0).UTC()
	if resolvedAt.Valid {
		req.ResolvedAt = time.Unix(resolvedAt.Int64, 0).UTC()
	}
	// Breached is computed, not stored: true once an open request's SLA
	// target has passed.
	req.Breached = req.State == "open" && time.Now().UTC().After(req.BreachAt)
	return req, nil
}
