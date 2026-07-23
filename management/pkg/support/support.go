// Package support implements the SUPPORT-03 three-tier support model.
//
// Tiers:
//   - Base (free)       — Discord + docs only. NO ticket channel. Hard wall.
//   - Pro (pro/managed) — Email tickets, 1-business-day SLA.
//   - Enterprise        — Dedicated Slack channel, named CSM, 1-hour P1 SLA, phone, QBRs.
//
// The rigid escalation wall is enforced in TicketChannelFor: any account on the
// "free" tier receives ErrNoTicketChannel. Pro and managed map to "email". Enterprise
// maps to "dedicated_slack".
//
// SLA tracking is append-only: OpenTicket writes a row; CloseTicket records
// resolved_at, breach_at, and breached. Breach is computed from BusinessDeadline
// which skips weekends. For P1 Enterprise tickets the SLA is 1 clock-hour (not
// business-hours). For all Pro/managed tickets the SLA is 1 business day (8 h).
//
// Pure-Go; no CGO. Uses modernc.org/sqlite via database/sql.
package support

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"time"

	"github.com/vul-os/vulos-management/pkg/cpdb"
)

// ─── Errors ──────────────────────────────────────────────────────────────────

var (
	// ErrNoTicketChannel is returned when the caller's tier does not include a
	// ticket channel (Base/free tier). The client should direct users to Discord.
	ErrNoTicketChannel = errors.New("support: ticket channel not available on Base tier; upgrade to Pro for email support")

	// ErrTicketNotFound is returned when a ticket ID does not exist.
	ErrTicketNotFound = errors.New("support: ticket not found")

	// ErrAlreadyClosed is returned when CloseTicket is called on a closed ticket.
	ErrAlreadyClosed = errors.New("support: ticket is already closed")

	// ErrForbidden is returned when a caller does not own the ticket.
	ErrForbidden = errors.New("support: not the ticket owner")
)

// ─── Constants ───────────────────────────────────────────────────────────────

// Priority levels accepted by OpenTicket.
const (
	PriorityP1 = "P1" // highest — 1-hour SLA on Enterprise
	PriorityP2 = "P2"
	PriorityP3 = "P3"
)

// Channel values stored in the ticket row.
const (
	ChannelEmail          = "email"           // Pro / managed
	ChannelDedicatedSlack = "dedicated_slack" // Enterprise
)

// businessHoursSLASec is the SLA budget for Pro/managed tickets (8 business hours).
const businessHoursSLASec = 8 * 60 * 60

// p1HourSLASec is the SLA budget for Enterprise P1 tickets (1 clock-hour).
const p1HourSLASec = 60 * 60

// ─── Tier classification ──────────────────────────────────────────────────────

// TierClass classifies a billing tier string into Base / Pro / Enterprise.
type TierClass int

const (
	TierBase       TierClass = iota // free
	TierPro                         // pro, team
	TierEnterprise                  // enterprise
)

// ClassifyTier maps a billing tier string to a TierClass.
func ClassifyTier(tier string) TierClass {
	switch tier {
	case "pro", "team":
		return TierPro
	case "enterprise":
		return TierEnterprise
	default:
		return TierBase
	}
}

// TicketChannelFor returns the support channel for tier, or ErrNoTicketChannel
// if the tier does not include a ticket channel (Base wall).
func TicketChannelFor(tier string) (string, error) {
	switch ClassifyTier(tier) {
	case TierEnterprise:
		return ChannelDedicatedSlack, nil
	case TierPro:
		return ChannelEmail, nil
	default:
		return "", ErrNoTicketChannel
	}
}

// SLASeconds returns the SLA deadline budget in seconds for a (tier, priority) pair.
// Enterprise P1 → 3600 s (1 clock-hour). All others → 8 business hours.
func SLASeconds(tier, priority string) int64 {
	if ClassifyTier(tier) == TierEnterprise && priority == PriorityP1 {
		return p1HourSLASec
	}
	return businessHoursSLASec
}

// BusinessDeadline computes the wall-clock time by which a support ticket must be
// resolved to meet its SLA budget. For P1 Enterprise tickets the budget is 1
// clock-hour; for all others the budget is 8 business hours (Mon–Fri, 09:00–17:00
// UTC, weekends skip). The result is always in UTC.
func BusinessDeadline(openedAt time.Time, tier, priority string) time.Time {
	openedAt = openedAt.UTC()
	if ClassifyTier(tier) == TierEnterprise && priority == PriorityP1 {
		// Clock-hour SLA — no business-hours adjustment.
		return openedAt.Add(time.Hour)
	}
	// 1 business day = 8 business hours.
	return addBusinessHours(openedAt, 8)
}

// addBusinessHours advances t by n business hours (Mon–Fri, 09:00–17:00 UTC).
func addBusinessHours(t time.Time, hours int) time.Time {
	remaining := hours
	for remaining > 0 {
		t = t.Add(time.Hour)
		if isBusinessHour(t) {
			remaining--
		}
	}
	return t
}

// isBusinessHour reports whether t falls within a business hour (Mon–Fri, 09:00–17:00 UTC).
func isBusinessHour(t time.Time) bool {
	t = t.UTC()
	wd := t.Weekday()
	if wd == time.Saturday || wd == time.Sunday {
		return false
	}
	h := t.Hour()
	return h >= 9 && h < 17
}

// ─── Domain types ─────────────────────────────────────────────────────────────

// Ticket is a single support ticket row.
type Ticket struct {
	ID         int64     `json:"id"`
	AccountID  string    `json:"account_id"`
	Tier       string    `json:"tier"`
	Priority   string    `json:"priority"`
	Channel    string    `json:"channel"`
	Subject    string    `json:"subject"`
	Body       string    `json:"body"`
	State      string    `json:"state"`     // "open" | "closed"
	BreachAt   time.Time `json:"breach_at"` // SLA deadline (UTC)
	Breached   bool      `json:"breached"`  // true once BreachAt is past and still open
	OpenedAt   time.Time `json:"opened_at"`
	ResolvedAt time.Time `json:"resolved_at,omitempty"`
}

// ─── Store interface ──────────────────────────────────────────────────────────

// Store is the persistence contract for support tickets.
type Store interface {
	// OpenTicket creates a new ticket. Returns ErrNoTicketChannel if the tier
	// does not include a ticket channel.
	OpenTicket(ctx context.Context, accountID, tier, priority, subject, body string) (Ticket, error)

	// GetTicket returns a ticket by ID.
	GetTicket(ctx context.Context, id int64) (Ticket, error)

	// ListTickets returns all open tickets for an account (newest first).
	ListTickets(ctx context.Context, accountID string) ([]Ticket, error)

	// CloseTicket marks a ticket as resolved. Returns ErrTicketNotFound,
	// ErrAlreadyClosed, or ErrForbidden if the caller does not own it.
	CloseTicket(ctx context.Context, id int64, callerAccountID string) error

	// MarkBreaches flips breached=true on all open tickets past their BreachAt.
	// Called by a background ticker.
	MarkBreaches(ctx context.Context) (int64, error)

	// Close releases resources.
	Close() error
}

// ─── SQLite store ─────────────────────────────────────────────────────────────

// SQLStore is a dual-backend (SQLite or Postgres) support.Store.
type SQLStore struct {
	db *cpdb.DB
}

// OpenSQLStore runs embedded migrations against db and returns a ready-to-use SQLStore.
func OpenSQLStore(db *cpdb.DB) (*SQLStore, error) {
	subFS, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("support: embed sub: %w", err)
	}
	if err := db.Migrate(subFS); err != nil {
		return nil, fmt.Errorf("support: migrate: %w", err)
	}
	return &SQLStore{db: db}, nil
}

// Close closes the underlying database.
func (s *SQLStore) Close() error { return s.db.Close() }

// OpenTicket creates a new ticket after enforcing the tier wall.
func (s *SQLStore) OpenTicket(ctx context.Context, accountID, tier, priority, subject, body string) (Ticket, error) {
	channel, err := TicketChannelFor(tier)
	if err != nil {
		return Ticket{}, err // ErrNoTicketChannel
	}
	if priority == "" {
		priority = PriorityP3
	}
	now := time.Now().UTC()
	breachAt := BusinessDeadline(now, tier, priority)

	var id int64
	err = s.db.QueryRowContext(ctx, s.db.Rebind(`
		INSERT INTO support_tickets
		  (account_id, tier, priority, channel, subject, body, state, breach_at, breached, opened_at)
		VALUES (?, ?, ?, ?, ?, ?, 'open', ?, 0, ?)
		RETURNING id`),
		accountID, tier, priority, channel,
		subject, body,
		breachAt.Format(time.RFC3339),
		now.Format(time.RFC3339),
	).Scan(&id)
	if err != nil {
		return Ticket{}, fmt.Errorf("support: open ticket: %w", err)
	}
	return Ticket{
		ID:        id,
		AccountID: accountID,
		Tier:      tier,
		Priority:  priority,
		Channel:   channel,
		Subject:   subject,
		Body:      body,
		State:     "open",
		BreachAt:  breachAt,
		Breached:  false,
		OpenedAt:  now,
	}, nil
}

// GetTicket returns a ticket by ID.
func (s *SQLStore) GetTicket(ctx context.Context, id int64) (Ticket, error) {
	return s.scanTicket(s.db.QueryRowContext(ctx, s.db.Rebind(
		`SELECT id, account_id, tier, priority, channel, subject, body, state,
		        breach_at, breached, opened_at, resolved_at
		 FROM support_tickets WHERE id = ?`), id))
}

// ListTickets returns tickets for an account, newest first.
func (s *SQLStore) ListTickets(ctx context.Context, accountID string) ([]Ticket, error) {
	rows, err := s.db.QueryContext(ctx, s.db.Rebind(
		`SELECT id, account_id, tier, priority, channel, subject, body, state,
		        breach_at, breached, opened_at, resolved_at
		 FROM support_tickets WHERE account_id = ?
		 ORDER BY opened_at DESC`), accountID)
	if err != nil {
		return nil, fmt.Errorf("support: list tickets: %w", err)
	}
	defer rows.Close()

	var out []Ticket
	for rows.Next() {
		t, err := s.scanTicket(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	if out == nil {
		out = []Ticket{}
	}
	return out, rows.Err()
}

// CloseTicket marks a ticket resolved.
func (s *SQLStore) CloseTicket(ctx context.Context, id int64, callerAccountID string) error {
	t, err := s.GetTicket(ctx, id)
	if err != nil {
		return err
	}
	if t.AccountID != callerAccountID {
		return ErrForbidden
	}
	if t.State == "closed" {
		return ErrAlreadyClosed
	}
	now := time.Now().UTC()
	_, err = s.db.ExecContext(ctx,
		s.db.Rebind(`UPDATE support_tickets SET state='closed', resolved_at=? WHERE id=?`),
		now.Format(time.RFC3339), id)
	if err != nil {
		return fmt.Errorf("support: close ticket: %w", err)
	}
	return nil
}

// MarkBreaches flips breached=true on all open tickets past their BreachAt.
func (s *SQLStore) MarkBreaches(ctx context.Context) (int64, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := s.db.ExecContext(ctx,
		s.db.Rebind(`UPDATE support_tickets SET breached=1 WHERE state='open' AND breached=0 AND breach_at <= ?`), now)
	if err != nil {
		return 0, fmt.Errorf("support: mark breaches: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// ─── scanTicket parses a single row from either QueryRowContext or rows.Scan ──

type rowScanner interface {
	Scan(dest ...any) error
}

func (s *SQLStore) scanTicket(row rowScanner) (Ticket, error) {
	var t Ticket
	var breachAtStr, openedAtStr string
	var resolvedAtStr sql.NullString
	var breached int

	err := row.Scan(
		&t.ID, &t.AccountID, &t.Tier, &t.Priority, &t.Channel,
		&t.Subject, &t.Body, &t.State,
		&breachAtStr, &breached, &openedAtStr, &resolvedAtStr,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Ticket{}, ErrTicketNotFound
	}
	if err != nil {
		return Ticket{}, fmt.Errorf("support: scan ticket: %w", err)
	}

	t.Breached = breached != 0
	if v, e := time.Parse(time.RFC3339, breachAtStr); e == nil {
		t.BreachAt = v
	}
	if v, e := time.Parse(time.RFC3339, openedAtStr); e == nil {
		t.OpenedAt = v
	}
	if resolvedAtStr.Valid && resolvedAtStr.String != "" {
		if v, e := time.Parse(time.RFC3339, resolvedAtStr.String); e == nil {
			t.ResolvedAt = v
		}
	}
	return t, nil
}

// ─── MemStore ─────────────────────────────────────────────────────────────────

// MemStore is an in-memory Store used for tests and local-dev fallback.
type MemStore struct {
	tickets map[int64]Ticket
	nextID  int64
}

// NewMemStore returns an empty MemStore.
func NewMemStore() *MemStore {
	return &MemStore{tickets: make(map[int64]Ticket)}
}

// Close is a no-op on MemStore.
func (m *MemStore) Close() error { return nil }

func (m *MemStore) OpenTicket(_ context.Context, accountID, tier, priority, subject, body string) (Ticket, error) {
	channel, err := TicketChannelFor(tier)
	if err != nil {
		return Ticket{}, err
	}
	if priority == "" {
		priority = PriorityP3
	}
	now := time.Now().UTC()
	m.nextID++
	t := Ticket{
		ID:        m.nextID,
		AccountID: accountID,
		Tier:      tier,
		Priority:  priority,
		Channel:   channel,
		Subject:   subject,
		Body:      body,
		State:     "open",
		BreachAt:  BusinessDeadline(now, tier, priority),
		OpenedAt:  now,
	}
	m.tickets[t.ID] = t
	return t, nil
}

func (m *MemStore) GetTicket(_ context.Context, id int64) (Ticket, error) {
	t, ok := m.tickets[id]
	if !ok {
		return Ticket{}, ErrTicketNotFound
	}
	return t, nil
}

func (m *MemStore) ListTickets(_ context.Context, accountID string) ([]Ticket, error) {
	var out []Ticket
	for _, t := range m.tickets {
		if t.AccountID == accountID {
			out = append(out, t)
		}
	}
	if out == nil {
		out = []Ticket{}
	}
	return out, nil
}

func (m *MemStore) CloseTicket(_ context.Context, id int64, callerAccountID string) error {
	t, ok := m.tickets[id]
	if !ok {
		return ErrTicketNotFound
	}
	if t.AccountID != callerAccountID {
		return ErrForbidden
	}
	if t.State == "closed" {
		return ErrAlreadyClosed
	}
	t.State = "closed"
	t.ResolvedAt = time.Now().UTC()
	m.tickets[id] = t
	return nil
}

func (m *MemStore) MarkBreaches(_ context.Context) (int64, error) {
	now := time.Now().UTC()
	var n int64
	for id, t := range m.tickets {
		if t.State == "open" && !t.Breached && now.After(t.BreachAt) {
			t.Breached = true
			m.tickets[id] = t
			n++
		}
	}
	return n, nil
}
