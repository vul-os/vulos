package abuse

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"sync"
	"time"

	"github.com/vul-os/vulos-management/pkg/cpdb"
)

// ErrNotFound is returned when a lookup misses (e.g. report_id unknown).
var ErrNotFound = errors.New("abuse: not found")

// Store is the durable T&S state: suspensions + report queue + signal log.
// All methods are safe for concurrent use.
type Store struct {
	db *cpdb.DB
	mu sync.Mutex // serialises status-change writes
}

// SuspensionRecord is the public view of a suspended_accounts row.
type SuspensionRecord struct {
	AccountID    string  `json:"account_id"`
	SuspendedAt  string  `json:"suspended_at"`
	Reason       string  `json:"reason"`
	Score        float64 `json:"score"`
	Auto         bool    `json:"auto"`
	ReinstatedAt string  `json:"reinstated_at,omitempty"`
	ReinstatedBy string  `json:"reinstated_by,omitempty"`
}

// Report is the public view of an abuse_reports row.
type Report struct {
	ID          string            `json:"id"`
	ReceivedAt  string            `json:"received_at"`
	Source      string            `json:"source"`
	SubjectID   string            `json:"subject_id"`
	Category    string            `json:"category"`
	Description string            `json:"description,omitempty"`
	Evidence    map[string]string `json:"evidence,omitempty"`
	Status      string            `json:"status"`
	HandledAt   string            `json:"handled_at,omitempty"`
	HandledBy   string            `json:"handled_by,omitempty"`
}

// OpenStore applies migrations to db and returns a ready Store.
// db is obtained from cpdb.Open (Postgres) or cpdb.OpenSQLiteDSN (SQLite).
func OpenStore(db *cpdb.DB) (*Store, error) {
	subFS, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("abuse: embed sub: %w", err)
	}
	if err := db.Migrate(subFS); err != nil {
		return nil, fmt.Errorf("abuse: migrate: %w", err)
	}
	return &Store{db: db}, nil
}

// Close closes the underlying DB.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// ─── suspensions ─────────────────────────────────────────────────────────────

// Suspend marks accountID as suspended. If already actively suspended the
// call is a no-op and returns changed=false — Suspend is idempotent on the
// active state so repeated strong signals don't churn the row.  changed=true
// indicates a fresh suspension (either a brand-new row or a re-suspend after
// a reinstate); callers can use this to gate audit-log emission so the chain
// doesn't fill with duplicates.
//
// score and auto are informational metadata for the human reviewer; they
// don't gate behaviour.
func (s *Store) Suspend(ctx context.Context, accountID, reason string, score float64, auto bool) (changed bool, err error) {
	if accountID == "" {
		return false, errors.New("abuse: empty account_id")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	// Active suspension already? leave it alone.
	var existing string
	err = s.db.QueryRowContext(ctx,
		s.db.Rebind(`SELECT account_id FROM suspended_accounts WHERE account_id = ? AND reinstated_at IS NULL`),
		accountID).Scan(&existing)
	if err == nil {
		return false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, fmt.Errorf("abuse: suspend lookup: %w", err)
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	autoI := 0
	if auto {
		autoI = 1
	}
	_, err = s.db.ExecContext(ctx,
		s.db.Rebind(`INSERT INTO suspended_accounts (account_id, suspended_at, reason, score, auto)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(account_id) DO UPDATE SET
		   suspended_at  = excluded.suspended_at,
		   reason        = excluded.reason,
		   score         = excluded.score,
		   auto          = excluded.auto,
		   reinstated_at = NULL,
		   reinstated_by = NULL`),
		accountID, now, reason, score, autoI)
	if err != nil {
		return false, fmt.Errorf("abuse: suspend insert: %w", err)
	}
	return true, nil
}

// Reinstate clears an active suspension. The byHuman caller-id is required —
// auto-reinstate is intentionally not exposed; only a human (or a script
// running on a human's behalf) may unsuspend.
func (s *Store) Reinstate(ctx context.Context, accountID, byHuman string) error {
	if accountID == "" {
		return errors.New("abuse: empty account_id")
	}
	if strings.TrimSpace(byHuman) == "" {
		return errors.New("abuse: reinstate requires byHuman actor")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC().Format(time.RFC3339Nano)
	res, err := s.db.ExecContext(ctx,
		s.db.Rebind(`UPDATE suspended_accounts
		    SET reinstated_at = ?, reinstated_by = ?
		  WHERE account_id = ? AND reinstated_at IS NULL`),
		now, "human:"+byHuman, accountID)
	if err != nil {
		return fmt.Errorf("abuse: reinstate: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// IsSuspended reports whether accountID currently has an active (non-reinstated)
// suspension row.
func (s *Store) IsSuspended(ctx context.Context, accountID string) (bool, error) {
	if accountID == "" {
		return false, nil
	}
	var one int
	err := s.db.QueryRowContext(ctx,
		s.db.Rebind(`SELECT 1 FROM suspended_accounts WHERE account_id = ? AND reinstated_at IS NULL LIMIT 1`),
		accountID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("abuse: is_suspended: %w", err)
	}
	return one == 1, nil
}

// GetSuspension returns the most recent suspension row for accountID, active or not.
func (s *Store) GetSuspension(ctx context.Context, accountID string) (*SuspensionRecord, error) {
	var (
		rec      SuspensionRecord
		auto     int
		reinstAt sql.NullString
		reinstBy sql.NullString
	)
	err := s.db.QueryRowContext(ctx,
		s.db.Rebind(`SELECT account_id, suspended_at, reason, score, auto, reinstated_at, reinstated_by
		   FROM suspended_accounts WHERE account_id = ?`), accountID).
		Scan(&rec.AccountID, &rec.SuspendedAt, &rec.Reason, &rec.Score, &auto, &reinstAt, &reinstBy)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("abuse: get suspension: %w", err)
	}
	rec.Auto = auto != 0
	if reinstAt.Valid {
		rec.ReinstatedAt = reinstAt.String
	}
	if reinstBy.Valid {
		rec.ReinstatedBy = reinstBy.String
	}
	return &rec, nil
}

// ─── reports ─────────────────────────────────────────────────────────────────

// FileReport appends a new abuse report. The returned Report carries the
// server-assigned report ID + received_at timestamp.
func (s *Store) FileReport(ctx context.Context, source, subjectID, category, description string, evidence map[string]string) (*Report, error) {
	if subjectID == "" {
		return nil, errors.New("abuse: report subject_id required")
	}
	if !validCategory(category) {
		return nil, fmt.Errorf("abuse: invalid category %q", category)
	}
	if source == "" {
		source = "anonymous"
	}

	id, err := randomHex(16)
	if err != nil {
		return nil, fmt.Errorf("abuse: gen report id: %w", err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	evJSON, _ := json.Marshal(evidence)
	if len(evJSON) == 0 {
		evJSON = []byte("{}")
	}

	_, err = s.db.ExecContext(ctx,
		s.db.Rebind(`INSERT INTO abuse_reports
		   (report_id, received_at, source, subject_id, category, description, evidence_json, status)
		   VALUES (?, ?, ?, ?, ?, ?, ?, 'open')`),
		id, now, source, subjectID, category, description, string(evJSON))
	if err != nil {
		return nil, fmt.Errorf("abuse: insert report: %w", err)
	}

	return &Report{
		ID:          id,
		ReceivedAt:  now,
		Source:      source,
		SubjectID:   subjectID,
		Category:    category,
		Description: description,
		Evidence:    evidence,
		Status:      StatusOpen,
	}, nil
}

// ListReports returns reports filtered by status (empty = all), most recent first.
// limit defaults to 100, capped at 1000.
func (s *Store) ListReports(ctx context.Context, status string, limit int) ([]Report, error) {
	if status != "" && !validStatus(status) {
		return nil, fmt.Errorf("abuse: invalid status %q", status)
	}
	if limit <= 0 || limit > 1000 {
		limit = 100
	}

	var (
		rows *sql.Rows
		err  error
	)
	if status == "" {
		rows, err = s.db.QueryContext(ctx,
			s.db.Rebind(`SELECT report_id, received_at, source, subject_id, category,
			        description, evidence_json, status, handled_at, handled_by
			   FROM abuse_reports ORDER BY received_at DESC LIMIT ?`), limit)
	} else {
		rows, err = s.db.QueryContext(ctx,
			s.db.Rebind(`SELECT report_id, received_at, source, subject_id, category,
			        description, evidence_json, status, handled_at, handled_by
			   FROM abuse_reports WHERE status = ? ORDER BY received_at DESC LIMIT ?`),
			status, limit)
	}
	if err != nil {
		return nil, fmt.Errorf("abuse: list reports: %w", err)
	}
	defer rows.Close()

	var out []Report
	for rows.Next() {
		var (
			r         Report
			evJSON    string
			handledAt sql.NullString
			handledBy sql.NullString
		)
		if err := rows.Scan(&r.ID, &r.ReceivedAt, &r.Source, &r.SubjectID, &r.Category,
			&r.Description, &evJSON, &r.Status, &handledAt, &handledBy); err != nil {
			return nil, fmt.Errorf("abuse: scan report: %w", err)
		}
		if evJSON != "" {
			_ = json.Unmarshal([]byte(evJSON), &r.Evidence)
		}
		if handledAt.Valid {
			r.HandledAt = handledAt.String
		}
		if handledBy.Valid {
			r.HandledBy = handledBy.String
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SetReportStatus moves a report to a new triage status. handledBy is required
// — every status change is attributable to a human reviewer.
func (s *Store) SetReportStatus(ctx context.Context, reportID, status, handledBy string) error {
	if !validStatus(status) {
		return fmt.Errorf("abuse: invalid status %q", status)
	}
	if strings.TrimSpace(handledBy) == "" {
		return errors.New("abuse: handled_by required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC().Format(time.RFC3339Nano)
	res, err := s.db.ExecContext(ctx,
		s.db.Rebind(`UPDATE abuse_reports SET status = ?, handled_at = ?, handled_by = ? WHERE report_id = ?`),
		status, now, handledBy, reportID)
	if err != nil {
		return fmt.Errorf("abuse: set status: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ─── signal log (forensic, not on the hot path) ──────────────────────────────

// LogSignal appends a row to abuse_signals for post-hoc review. Best-effort:
// callers may ignore the error to avoid blocking detection on a write hiccup.
func (s *Store) LogSignal(ctx context.Context, sig Signal) error {
	ts := sig.At
	if ts.IsZero() {
		ts = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx,
		s.db.Rebind(`INSERT INTO abuse_signals (ts, kind, account_id, ip, score, reason)
		   VALUES (?, ?, ?, ?, ?, ?)`),
		ts.UTC().Format(time.RFC3339Nano), string(sig.Kind),
		sig.AccountID, sig.IP, sig.Score, sig.Reason)
	if err != nil {
		return fmt.Errorf("abuse: log signal: %w", err)
	}
	return nil
}

// CountSignalsSince returns the number of logged signals at or after t.
// Useful for tests + the T&S dashboard.
func (s *Store) CountSignalsSince(ctx context.Context, t time.Time) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		s.db.Rebind(`SELECT COUNT(*) FROM abuse_signals WHERE ts >= ?`),
		t.UTC().Format(time.RFC3339Nano)).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("abuse: count signals: %w", err)
	}
	return n, nil
}

// ─── helpers ─────────────────────────────────────────────────────────────────

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
