package assistant

// reminders_store.go — the sovereign REMINDERS store + poll-based scheduler
// (Wave 62).
//
// This is the durable, per-user backing store for the assistant's REMINDERS
// capability ("remind me to call the dentist tomorrow at 3pm"). It mirrors the
// OS's other on-box SQLite stores (services/store, notify's persistent store):
// a single SQLite file under the instance data dir, opened with the pure-Go
// modernc driver (no CGo), WAL + busy-timeout, idempotent schema creation.
//
// A reminder is a tiny record — {id, user_id, text, remind_at, created_at, done}
// — and everything the store exposes is USER-SCOPED: every read/mutate takes a
// userID and the SQL is filtered by it, so a user can never list, complete, or
// cancel another user's reminders (the store is the isolation boundary; proven
// by TestRemindersStorePerUserIsolation). The per-user count is BOUNDED
// (maxRemindersPerUser) so a flood cannot grow the table without limit.
//
// SCHEDULER — the OS mail-snooze / Talk-scheduled pattern: a poll loop wakes on
// a short interval, asks the store for reminders whose remind_at has passed and
// are not yet done, and FIRES a notification for each (reusing the OS
// notifications system through the Notifier seam) then marks it done so it fires
// exactly once. Because "due & not done" is a DB query, the scheduler is
// RESTART-SAFE: a reminder set before a crash/restart is caught up the first
// time the loop runs after boot (there is no in-memory timer that a restart
// would lose). Firing is idempotent per row (done flips atomically).
//
// This store introduces NO egress and NO model call: it is a local on-box write
// path (like the mail triage / calendar create executor), so it does not — and
// must not — sit behind the sovereign egress Guard. Guard is the choke point for
// MODEL egress; creating/cancelling a reminder never reaches a model. The
// assistant's reminder TOOLS (reminders.go) are what run inside the guarded
// agent loop, and the mutating ones are ledger-gated exactly like send_email.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver (no CGo — matches services/store)
)

// ErrReminderNotFound is returned when a reminder id does not exist FOR THIS
// USER (unknown, already done, or — critically — owned by another user). It is
// deliberately indistinguishable across those cases so a cross-user probe cannot
// tell "exists but not yours" from "does not exist" (no existence oracle).
var ErrReminderNotFound = errors.New("reminder not found")

// Bounds keep the reminders store cheap and abuse-resistant.
const (
	// maxRemindersPerUser caps how many PENDING (not-done) reminders a single user
	// may hold at once. Setting a reminder past this cap is rejected so a flood
	// cannot grow the table without limit (per-user isolation of the bound too).
	maxRemindersPerUser = 200

	// maxReminderTextLen bounds the stored reminder text so a huge blob cannot be
	// persisted (and later rendered). Longer text is rejected by SetReminder.
	maxReminderTextLen = 500

	// maxReminderHorizon is the furthest into the future a remind_at may be. A
	// reminder further out than this is "absurd" and rejected, alongside any
	// remind_at in the past. Ten years is generous for a personal reminder while
	// still rejecting obvious garbage (year 9999 timestamps, overflow, etc.).
	maxReminderHorizon = 10 * 365 * 24 * time.Hour

	// minReminderLead is the smallest gap into the future a remind_at may sit. A
	// couple of seconds avoids a "reminder for right now" racing the poll loop and
	// firing before the proposal is even approved.
	minReminderLead = 2 * time.Second
)

// Reminder is one durable reminder record. It is the normalized row the store
// persists and the surface renders. Text is the user's own words (bounded, and
// rendered escaped by the shell — never as HTML).
type Reminder struct {
	ID        string    `json:"id"`
	UserID    string    `json:"user_id"`
	Text      string    `json:"text"`
	RemindAt  time.Time `json:"remind_at"`
	CreatedAt time.Time `json:"created_at"`
	Done      bool      `json:"done"`
}

// RemindersStore is the durable, per-user SQLite store of reminders. It is safe
// for concurrent use (SQLite serializes writes; a single-writer connection is
// used, matching services/store).
type RemindersStore struct {
	db *sql.DB
}

// OpenRemindersStore opens (or creates) the reminders SQLite database at path.
// An empty path defaults to ~/.vulos/db/reminders.db. A directory path gets
// reminders.db appended. The schema is created idempotently on every open (safe
// each boot).
func OpenRemindersStore(path string) (*RemindersStore, error) {
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("reminders: cannot determine home dir: %w", err)
		}
		path = filepath.Join(home, ".vulos", "db", "reminders.db")
	}
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		path = filepath.Join(path, "reminders.db")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, fmt.Errorf("reminders: cannot create db dir: %w", err)
	}

	dsn := fmt.Sprintf(
		"file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)",
		path,
	)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("reminders: open: %w", err)
	}
	db.SetMaxOpenConns(1) // SQLite: single writer

	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("reminders: ping: %w", err)
	}

	if _, err := db.Exec(remindersSchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("reminders: schema: %w", err)
	}
	return &RemindersStore{db: db}, nil
}

// NewRemindersStoreDB wraps an already-open *sql.DB (tests use an in-memory
// SQLite handle). The schema is created idempotently.
func NewRemindersStoreDB(db *sql.DB) (*RemindersStore, error) {
	if _, err := db.Exec(remindersSchema); err != nil {
		return nil, fmt.Errorf("reminders: schema: %w", err)
	}
	return &RemindersStore{db: db}, nil
}

const remindersSchema = `
CREATE TABLE IF NOT EXISTS reminders (
    id         TEXT PRIMARY KEY,
    user_id    TEXT NOT NULL,
    text       TEXT NOT NULL,
    remind_at  INTEGER NOT NULL,  -- unix seconds
    created_at INTEGER NOT NULL,  -- unix seconds
    done       INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_reminders_user ON reminders(user_id, done);
CREATE INDEX IF NOT EXISTS idx_reminders_due ON reminders(done, remind_at);
`

// Close closes the underlying database. Safe on a nil receiver.
func (s *RemindersStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// SetReminder durably stores a reminder for userID, to fire at remindAt. It
// VALIDATES the inputs — user identity present, text non-empty and bounded,
// remindAt strictly in the (bounded) future — and enforces the per-user pending
// cap. It returns the stored Reminder (with its generated id) or an error; on
// any validation failure NOTHING is written (fail-closed).
func (s *RemindersStore) SetReminder(userID, text string, remindAt time.Time) (Reminder, error) {
	uid := strings.TrimSpace(userID)
	if uid == "" {
		return Reminder{}, fmt.Errorf("reminders: no user identity")
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return Reminder{}, fmt.Errorf("reminders: reminder text is required")
	}
	if len(text) > maxReminderTextLen {
		return Reminder{}, fmt.Errorf("reminders: text too long (max %d characters)", maxReminderTextLen)
	}
	if err := validateRemindAt(remindAt, time.Now()); err != nil {
		return Reminder{}, err
	}

	// Enforce the per-user PENDING cap (this user's rows only — isolation).
	var pending int
	if err := s.db.QueryRow(
		`SELECT COUNT(*) FROM reminders WHERE user_id = ? AND done = 0`, uid,
	).Scan(&pending); err != nil {
		return Reminder{}, fmt.Errorf("reminders: count: %w", err)
	}
	if pending >= maxRemindersPerUser {
		return Reminder{}, fmt.Errorf("reminders: too many pending reminders (max %d) — clear some first", maxRemindersPerUser)
	}

	r := Reminder{
		ID:        newReminderID(),
		UserID:    uid,
		Text:      text,
		RemindAt:  remindAt.UTC().Truncate(time.Second),
		CreatedAt: time.Now().UTC().Truncate(time.Second),
		Done:      false,
	}
	if _, err := s.db.Exec(
		`INSERT INTO reminders (id, user_id, text, remind_at, created_at, done) VALUES (?, ?, ?, ?, ?, 0)`,
		r.ID, r.UserID, r.Text, r.RemindAt.Unix(), r.CreatedAt.Unix(),
	); err != nil {
		return Reminder{}, fmt.Errorf("reminders: insert: %w", err)
	}
	return r, nil
}

// validateRemindAt enforces that a reminder time is strictly in the future
// (past a small lead) and within the sane horizon. Past/absurd times are
// rejected so a reminder can neither fire in the past nor be parked years in the
// future by an overflow/garbage timestamp.
func validateRemindAt(remindAt, now time.Time) error {
	if remindAt.IsZero() {
		return fmt.Errorf("reminders: a reminder time is required")
	}
	if remindAt.Before(now.Add(minReminderLead)) {
		return fmt.Errorf("reminders: the reminder time is in the past — pick a future time")
	}
	if remindAt.After(now.Add(maxReminderHorizon)) {
		return fmt.Errorf("reminders: the reminder time is too far in the future (max ~10 years out)")
	}
	return nil
}

// ListReminders returns a user's reminders (their OWN only), newest-set first.
// includeDone controls whether already-fired/cancelled reminders are included;
// the common "what are my reminders" call passes false to show only pending.
// limit caps the result (<=0 ⇒ a sane default).
func (s *RemindersStore) ListReminders(userID string, includeDone bool, limit int) ([]Reminder, error) {
	uid := strings.TrimSpace(userID)
	if uid == "" {
		return nil, fmt.Errorf("reminders: no user identity")
	}
	if limit <= 0 || limit > maxRemindersPerUser {
		limit = maxRemindersPerUser
	}
	q := `SELECT id, user_id, text, remind_at, created_at, done FROM reminders WHERE user_id = ?`
	if !includeDone {
		q += ` AND done = 0`
	}
	q += ` ORDER BY remind_at ASC LIMIT ?`
	rows, err := s.db.Query(q, uid, limit)
	if err != nil {
		return nil, fmt.Errorf("reminders: list: %w", err)
	}
	defer rows.Close()
	return scanReminders(rows)
}

// CancelReminder marks a user's OWN reminder done (so it will not fire), scoped
// by BOTH id and user_id — a cancel for another user's id affects zero rows and
// returns ErrReminderNotFound (no cross-user cancel, no existence probe). It
// returns the cancelled reminder on success.
func (s *RemindersStore) CancelReminder(userID, id string) (Reminder, error) {
	uid := strings.TrimSpace(userID)
	if uid == "" {
		return Reminder{}, fmt.Errorf("reminders: no user identity")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return Reminder{}, ErrReminderNotFound
	}
	// Fetch the row SCOPED TO THIS USER first (so we can return it) — a foreign id
	// simply does not match and yields not-found.
	r, err := s.getForUser(uid, id)
	if err != nil {
		return Reminder{}, err
	}
	res, err := s.db.Exec(
		`UPDATE reminders SET done = 1 WHERE id = ? AND user_id = ? AND done = 0`, id, uid,
	)
	if err != nil {
		return Reminder{}, fmt.Errorf("reminders: cancel: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// Already done (or vanished): report not-found rather than a false success.
		return Reminder{}, ErrReminderNotFound
	}
	r.Done = true
	return r, nil
}

// getForUser reads a single reminder scoped to userID. A row that exists but
// belongs to another user is invisible here (WHERE user_id filters it out), so
// this can never leak another user's reminder.
func (s *RemindersStore) getForUser(userID, id string) (Reminder, error) {
	row := s.db.QueryRow(
		`SELECT id, user_id, text, remind_at, created_at, done FROM reminders WHERE id = ? AND user_id = ?`,
		id, userID,
	)
	r, err := scanReminder(row)
	if err == sql.ErrNoRows {
		return Reminder{}, ErrReminderNotFound
	}
	if err != nil {
		return Reminder{}, fmt.Errorf("reminders: get: %w", err)
	}
	return r, nil
}

// DueReminders returns all reminders whose remind_at is at/before now and which
// are not yet done, across ALL users — this is the scheduler's query, so a
// reminder set before a restart is caught up the first pass after boot. Callers
// fire each and then MarkFired it. limit bounds a single sweep so a huge backlog
// is drained in batches rather than all at once.
func (s *RemindersStore) DueReminders(now time.Time, limit int) ([]Reminder, error) {
	if limit <= 0 {
		limit = 500
	}
	rows, err := s.db.Query(
		`SELECT id, user_id, text, remind_at, created_at, done FROM reminders
		 WHERE done = 0 AND remind_at <= ? ORDER BY remind_at ASC LIMIT ?`,
		now.UTC().Unix(), limit,
	)
	if err != nil {
		return nil, fmt.Errorf("reminders: due: %w", err)
	}
	defer rows.Close()
	return scanReminders(rows)
}

// MarkFired flips a reminder to done ATOMICALLY, returning whether THIS call was
// the one that flipped it (RowsAffected == 1). The scheduler uses the boolean to
// ensure a reminder fires EXACTLY ONCE even if two sweeps race: only the winner
// (done 0→1) sends the notification.
func (s *RemindersStore) MarkFired(id string) (bool, error) {
	res, err := s.db.Exec(`UPDATE reminders SET done = 1 WHERE id = ? AND done = 0`, id)
	if err != nil {
		return false, fmt.Errorf("reminders: mark fired: %w", err)
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// ---- scanning helpers ------------------------------------------------------

type rowScanner interface {
	Scan(dest ...any) error
}

func scanReminder(row rowScanner) (Reminder, error) {
	var (
		r              Reminder
		remindAt, made int64
		done           int
	)
	if err := row.Scan(&r.ID, &r.UserID, &r.Text, &remindAt, &made, &done); err != nil {
		return Reminder{}, err
	}
	r.RemindAt = time.Unix(remindAt, 0).UTC()
	r.CreatedAt = time.Unix(made, 0).UTC()
	r.Done = done != 0
	return r, nil
}

func scanReminders(rows *sql.Rows) ([]Reminder, error) {
	var out []Reminder
	for rows.Next() {
		r, err := scanReminder(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ---- scheduler -------------------------------------------------------------

// Notifier is the minimal seam the reminder scheduler needs to raise an OS
// notification when a reminder fires. It is deliberately tiny so services/
// assistant does not import services/notify directly (mirroring FilesBackend);
// the cmd/server adapter satisfies it with *notify.Service.
type Notifier interface {
	// NotifyReminder raises a notification for a fired reminder, targeted at
	// userID. The implementation decides the notification's channel/priority; the
	// scheduler only supplies the identity + the (bounded, user-authored) text.
	NotifyReminder(userID, reminderID, text string)
}

// reminderPollInterval is how often the scheduler sweeps for due reminders. A
// short interval keeps reminders punctual without a per-reminder timer; the
// sweep is a single indexed DB query so it is cheap.
const reminderPollInterval = 15 * time.Second

// ReminderScheduler polls the store for due reminders and fires notifications.
// It owns no in-memory timers, so it is fully restart-safe: whatever is due when
// it (re)starts is caught up on the first sweep (boot catch-up).
type ReminderScheduler struct {
	store    *RemindersStore
	notifier Notifier
	interval time.Duration
	now      func() time.Time // injectable clock for tests
}

// NewReminderScheduler builds a scheduler over the store + notifier. Either may
// be nil, in which case Sweep/Run are safe no-ops (the feature degrades to "off"
// rather than crashing, mirroring the rest of the assistant's seams).
func NewReminderScheduler(store *RemindersStore, notifier Notifier) *ReminderScheduler {
	return &ReminderScheduler{
		store:    store,
		notifier: notifier,
		interval: reminderPollInterval,
		now:      time.Now,
	}
}

// Sweep fires every reminder that is due as of now, exactly once each. It is the
// unit the poll loop calls, and it is safe to call directly (tests drive it to
// assert firing + boot catch-up). Returns the number of reminders fired.
func (s *ReminderScheduler) Sweep(ctx context.Context) (int, error) {
	if s == nil || s.store == nil {
		return 0, nil
	}
	due, err := s.store.DueReminders(s.now(), 500)
	if err != nil {
		return 0, err
	}
	fired := 0
	for _, r := range due {
		if ctx.Err() != nil {
			return fired, ctx.Err()
		}
		// Flip done FIRST and only notify if THIS call won the race — a reminder
		// fires exactly once even across overlapping sweeps.
		won, err := s.store.MarkFired(r.ID)
		if err != nil {
			continue // best-effort: skip this row, keep draining the rest
		}
		if !won {
			continue
		}
		if s.notifier != nil {
			s.notifier.NotifyReminder(r.UserID, r.ID, r.Text)
		}
		fired++
	}
	return fired, nil
}

// Run drives the poll loop until ctx is cancelled. It sweeps ONCE immediately
// (boot catch-up: anything due while the process was down fires right away),
// then on every interval tick. It blocks, so callers run it in a goroutine.
func (s *ReminderScheduler) Run(ctx context.Context) {
	if s == nil || s.store == nil {
		return
	}
	_, _ = s.Sweep(ctx) // boot catch-up
	t := time.NewTicker(s.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			_, _ = s.Sweep(ctx)
		}
	}
}
