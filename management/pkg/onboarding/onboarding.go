// Package onboarding implements SUPPORT-05: cloud-side onboarding flow tracking
// and day-7 migration gate enforcement.
//
// The OS-embedded onboarding UI lives in the OSS vulos repo. This package is
// the control-plane counterpart: it persists onboarding step completions per
// account and enforces the hard gate that blocks migration tools (MAIL-09
// Gmail/M365 import) until the account is 7+ days old.
//
// Gate rationale: migrations are the ticket factory — misconfigured imports,
// partial sync, dedup failures. A 7-day cooling-off period gives the account
// time to validate domain, mail, backup, and device-pair flows before
// importing historical data.
//
// Steps tracked (all optional; OS reports completion):
//
//	domain_connected    — first custom domain wired
//	dns_verified        — DNS propagation confirmed
//	first_mail_sent     — first outbound mail delivered
//	first_backup_done   — first bucket backup confirmed
//	first_device_paired — first second device enrolled
//	first_user_invited  — first team-member invite sent
//
// Pure-Go; no CGO. Uses modernc.org/sqlite via database/sql.
package onboarding

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"time"

	"github.com/vul-os/vulos-management/pkg/cpdb"
)

// MigrationGateDays is the minimum account age required before migration
// tools are unlocked. Changing this constant changes the gate for all accounts.
const MigrationGateDays = 7

// ─── Errors ──────────────────────────────────────────────────────────────────

var (
	// ErrMigrationGated is returned when the account is younger than
	// MigrationGateDays and the caller requests migration-tool access.
	ErrMigrationGated = errors.New("onboarding: migration tools are locked until day 7; complete onboarding steps first")

	// ErrUnknownStep is returned when an unrecognised step name is submitted.
	ErrUnknownStep = errors.New("onboarding: unknown onboarding step name")

	// ErrNotFound is returned when no onboarding record exists for an account.
	ErrNotFound = errors.New("onboarding: no state found for account")
)

// ValidSteps is the set of step names the OS may report as completed.
var ValidSteps = map[string]struct{}{
	"domain_connected":    {},
	"dns_verified":        {},
	"first_mail_sent":     {},
	"first_backup_done":   {},
	"first_device_paired": {},
	"first_user_invited":  {},
}

// ─── Domain types ─────────────────────────────────────────────────────────────

// State is the onboarding record for a single account.
type State struct {
	AccountID         string    `json:"account_id"`
	AccountCreatedAt  time.Time `json:"account_created_at"`
	DomainConnected   bool      `json:"domain_connected"`
	DNSVerified       bool      `json:"dns_verified"`
	FirstMailSent     bool      `json:"first_mail_sent"`
	FirstBackupDone   bool      `json:"first_backup_done"`
	FirstDevicePaired bool      `json:"first_device_paired"`
	FirstUserInvited  bool      `json:"first_user_invited"`
	UpdatedAt         time.Time `json:"updated_at"`
}

// MigrationAllowed reports whether the account has passed the 7-day gate.
// now is passed explicitly so callers (and tests) can control the clock.
func (s State) MigrationAllowed(now time.Time) bool {
	return now.Sub(s.AccountCreatedAt) >= MigrationGateDays*24*time.Hour
}

// DaysUntilMigration returns the number of whole days remaining until the
// migration gate opens. Returns 0 if the gate is already open.
func (s State) DaysUntilMigration(now time.Time) int {
	target := s.AccountCreatedAt.Add(MigrationGateDays * 24 * time.Hour)
	remaining := target.Sub(now)
	if remaining <= 0 {
		return 0
	}
	// Ceiling division: partial day counts as a whole day.
	return int((remaining + 23*time.Hour + 59*time.Minute + 59*time.Second) / (24 * time.Hour))
}

// CompletedSteps returns the count of steps the account has finished.
func (s State) CompletedSteps() int {
	n := 0
	if s.DomainConnected {
		n++
	}
	if s.DNSVerified {
		n++
	}
	if s.FirstMailSent {
		n++
	}
	if s.FirstBackupDone {
		n++
	}
	if s.FirstDevicePaired {
		n++
	}
	if s.FirstUserInvited {
		n++
	}
	return n
}

// TotalSteps is the total number of guided onboarding steps.
const TotalSteps = 6

// ─── Store interface ──────────────────────────────────────────────────────────

// Store is the persistence contract for SUPPORT-05 onboarding state.
type Store interface {
	// GetState returns the onboarding state for accountID.
	// Returns ErrNotFound if no row exists yet.
	GetState(ctx context.Context, accountID string) (State, error)

	// UpsertState creates or updates the onboarding state for accountID.
	// accountCreatedAt is stored only on first insert; subsequent calls ignore it.
	// step must be a key in ValidSteps, or ErrUnknownStep is returned.
	// Passing an empty step is valid — it initialises the row without marking any step.
	MarkStep(ctx context.Context, accountID string, accountCreatedAt time.Time, step string) (State, error)

	// CheckMigrationGate returns nil if the account may use migration tools,
	// or ErrMigrationGated if the account is too new.
	// now is passed explicitly; callers should pass time.Now().UTC().
	CheckMigrationGate(ctx context.Context, accountID string, now time.Time) error

	// Close releases resources.
	Close() error
}

// ─── SQLite store ─────────────────────────────────────────────────────────────

// SQLStore is a cpdb-backed onboarding.Store (SQLite or Postgres).
type SQLStore struct {
	db *cpdb.DB
}

// OpenSQLStore applies schema migrations to db and returns a ready-to-use SQLStore.
func OpenSQLStore(db *cpdb.DB) (*SQLStore, error) {
	subFS, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("onboarding: embed sub: %w", err)
	}
	if err := db.Migrate(subFS); err != nil {
		return nil, fmt.Errorf("onboarding: migrate: %w", err)
	}
	return &SQLStore{db: db}, nil
}

// Close closes the underlying database.
func (s *SQLStore) Close() error { return s.db.Close() }

// GetState fetches the onboarding state for accountID.
func (s *SQLStore) GetState(ctx context.Context, accountID string) (State, error) {
	row := s.db.QueryRowContext(ctx, s.db.Rebind(`
		SELECT account_id, account_created_at,
		       domain_connected, dns_verified, first_mail_sent,
		       first_backup_done, first_device_paired, first_user_invited,
		       updated_at
		FROM onboarding_state WHERE account_id = ?`), accountID)
	return scanState(row)
}

// MarkStep upserts the onboarding row for accountID, setting the given step to
// true (if non-empty). accountCreatedAt is stored only on INSERT; subsequent
// calls to MarkStep do not modify it.
func (s *SQLStore) MarkStep(ctx context.Context, accountID string, accountCreatedAt time.Time, step string) (State, error) {
	if step != "" {
		if _, ok := ValidSteps[step]; !ok {
			return State{}, ErrUnknownStep
		}
	}

	now := time.Now().UTC()
	createdStr := accountCreatedAt.UTC().Format(time.RFC3339)
	nowStr := now.Format(time.RFC3339)

	// Insert row if it does not exist (ignore conflict on primary key so the
	// account_created_at is preserved from the first insert).
	_, err := s.db.ExecContext(ctx, s.db.Rebind(`
		INSERT INTO onboarding_state
		  (account_id, account_created_at, updated_at)
		VALUES (?, ?, ?)
		ON CONFLICT(account_id) DO NOTHING`),
		accountID, createdStr, nowStr)
	if err != nil {
		return State{}, fmt.Errorf("onboarding: upsert row: %w", err)
	}

	// Mark the specific step if one was provided.
	if step != "" {
		col := stepColumn(step)
		_, err = s.db.ExecContext(ctx,
			s.db.Rebind(`UPDATE onboarding_state SET `+col+` = 1, updated_at = ? WHERE account_id = ?`),
			nowStr, accountID)
		if err != nil {
			return State{}, fmt.Errorf("onboarding: mark step %s: %w", step, err)
		}
	}

	return s.GetState(ctx, accountID)
}

// CheckMigrationGate returns nil if the account may use migration tools, or
// ErrMigrationGated if the account is younger than MigrationGateDays.
// If no onboarding row exists yet the gate is closed (account is new).
func (s *SQLStore) CheckMigrationGate(ctx context.Context, accountID string, now time.Time) error {
	st, err := s.GetState(ctx, accountID)
	if errors.Is(err, ErrNotFound) {
		return ErrMigrationGated
	}
	if err != nil {
		return fmt.Errorf("onboarding: gate check: %w", err)
	}
	if !st.MigrationAllowed(now) {
		return ErrMigrationGated
	}
	return nil
}

// stepColumn maps a step name to its SQLite column name.
// Callers must have already validated step ∈ ValidSteps.
func stepColumn(step string) string {
	return step // column names match step names exactly
}

// scanState reads one row from either a *sql.Row or sql.Rows.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanState(row rowScanner) (State, error) {
	var s State
	var createdStr, updatedStr string
	var domainConnected, dnsVerified, firstMail, firstBackup, firstDevice, firstUser int

	err := row.Scan(
		&s.AccountID, &createdStr,
		&domainConnected, &dnsVerified, &firstMail,
		&firstBackup, &firstDevice, &firstUser,
		&updatedStr,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return State{}, ErrNotFound
	}
	if err != nil {
		return State{}, fmt.Errorf("onboarding: scan: %w", err)
	}

	if v, e := time.Parse(time.RFC3339, createdStr); e == nil {
		s.AccountCreatedAt = v
	}
	if v, e := time.Parse(time.RFC3339, updatedStr); e == nil {
		s.UpdatedAt = v
	}
	s.DomainConnected = domainConnected != 0
	s.DNSVerified = dnsVerified != 0
	s.FirstMailSent = firstMail != 0
	s.FirstBackupDone = firstBackup != 0
	s.FirstDevicePaired = firstDevice != 0
	s.FirstUserInvited = firstUser != 0
	return s, nil
}

// ─── MemStore ─────────────────────────────────────────────────────────────────

// MemStore is an in-memory Store for tests and local-dev fallback.
type MemStore struct {
	states map[string]State
}

// NewMemStore returns an empty MemStore.
func NewMemStore() *MemStore {
	return &MemStore{states: make(map[string]State)}
}

// Close is a no-op on MemStore.
func (m *MemStore) Close() error { return nil }

func (m *MemStore) GetState(_ context.Context, accountID string) (State, error) {
	s, ok := m.states[accountID]
	if !ok {
		return State{}, ErrNotFound
	}
	return s, nil
}

func (m *MemStore) MarkStep(_ context.Context, accountID string, accountCreatedAt time.Time, step string) (State, error) {
	if step != "" {
		if _, ok := ValidSteps[step]; !ok {
			return State{}, ErrUnknownStep
		}
	}

	s, exists := m.states[accountID]
	if !exists {
		s = State{
			AccountID:        accountID,
			AccountCreatedAt: accountCreatedAt.UTC(),
		}
	}
	s.UpdatedAt = time.Now().UTC()

	switch step {
	case "domain_connected":
		s.DomainConnected = true
	case "dns_verified":
		s.DNSVerified = true
	case "first_mail_sent":
		s.FirstMailSent = true
	case "first_backup_done":
		s.FirstBackupDone = true
	case "first_device_paired":
		s.FirstDevicePaired = true
	case "first_user_invited":
		s.FirstUserInvited = true
	}

	m.states[accountID] = s
	return s, nil
}

func (m *MemStore) CheckMigrationGate(_ context.Context, accountID string, now time.Time) error {
	s, ok := m.states[accountID]
	if !ok {
		return ErrMigrationGated
	}
	if !s.MigrationAllowed(now) {
		return ErrMigrationGated
	}
	return nil
}
