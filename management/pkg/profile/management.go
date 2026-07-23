// management.go — Profile management types, ManagementStore interface, and
// reference implementations for the PROFILE-02 endpoints.
//
// The three management endpoints (list, reset, rename) build a ProfileUpdateMsg,
// sign it with the broker key (same Signer from PROFILE-01), and return the
// signed wire-format message to the caller. The OS applies the change locally
// via CLOGIN-03 (OSS side, cross-repo — cloud NEVER holds the OS session).
//
// Audit rows are appended by the handler layer AFTER the signed message is
// emitted; they are append-only and never mutated.
package profile

import (
	"context"
	"fmt"
	"io/fs"
	"sync"
	"time"

	"github.com/vul-os/vulos-management/pkg/cpdb"
)

// ─────────────────────────────────────────────────────────────────────────────
// Domain types
// ─────────────────────────────────────────────────────────────────────────────

// ProfileUpdateType identifies the kind of profile management action.
type ProfileUpdateType string

const (
	UpdateTypeResetPassword ProfileUpdateType = "reset_password"
	UpdateTypeRename        ProfileUpdateType = "rename"
)

// ProfileEntry represents a single OS user profile on a device, as returned by
// GET /api/profile/list. The cloud never stores passwords; it only stores the
// profile names it has been told about via out-of-band means (or a future
// heartbeat extension). For now the list is maintained by the caller (the OS
// registers known profiles).
//
// In the current implementation the ManagementStore keeps a simple set of
// known profile names per device, upserted by the reset/rename operations.
// list returns whatever is stored.
type ProfileEntry struct {
	ULID        string    `json:"ulid"`
	ProfileName string    `json:"profile_name"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// ProfileUpdateMsg is the payload of a cloud-signed profile management message.
// It is serialised to JSON, signed with Ed25519, and returned base64-encoded
// to the API caller; the OS validates the signature against the broker pubkey
// baked at enrollment (CLOGIN-03).
//
// Action: "reset_password" | "rename"
// For reset_password: NewPassword contains the new (plaintext) password that
// the OS will hash locally. Cloud never stores or logs it.
// For rename:         NewName contains the desired new profile name.
type ProfileUpdateMsg struct {
	AccountID   string            `json:"account_id"`
	ULID        string            `json:"ulid"`
	ProfileName string            `json:"profile_name"` // current profile name
	Action      ProfileUpdateType `json:"action"`
	NewPassword string            `json:"new_password,omitempty"` // reset_password only
	NewName     string            `json:"new_name,omitempty"`     // rename only
	IssuedAt    time.Time         `json:"issued_at"`
	ExpiresAt   time.Time         `json:"expires_at"`
	Nonce       string            `json:"nonce"`
}

// AuditEntry is a single audit-log row for a profile management action.
type AuditEntry struct {
	ID          int64     `json:"id"`
	AccountID   string    `json:"account_id"`
	ActorID     string    `json:"actor_id"` // who performed the action
	ULID        string    `json:"ulid"`
	ProfileName string    `json:"profile_name"`
	Action      string    `json:"action"`
	CreatedAt   time.Time `json:"created_at"`
}

// ─────────────────────────────────────────────────────────────────────────────
// ManagementStore interface
// ─────────────────────────────────────────────────────────────────────────────

// ManagementStore persists profile entries (known profiles per device) and the
// append-only audit log. MemManagementStore is the behavioural source of truth.
type ManagementStore interface {
	// UpsertProfile records that profile_name exists on ulid. Called by reset and
	// rename (rename also removes the old name if changed). Idempotent on the same
	// (ulid, profile_name) pair.
	UpsertProfile(ctx context.Context, ulid, profileName string) error

	// RenameProfile renames oldName to newName on ulid. If oldName does not exist
	// it is treated as a no-op insert of newName (OS is the authority).
	RenameProfile(ctx context.Context, ulid, oldName, newName string) error

	// ListProfiles returns all known profiles for a device ULID, ordered by
	// profile_name. Returns an empty slice (not an error) when no profiles are
	// known.
	ListProfiles(ctx context.Context, ulid string) ([]ProfileEntry, error)

	// LogAudit appends a single audit row. The row is immutable after insert.
	LogAudit(ctx context.Context, e AuditEntry) error

	// AuditLog returns audit rows for a ULID, most recent first, limited to n
	// entries (n ≤ 0 means no limit).
	AuditLog(ctx context.Context, ulid string, n int) ([]AuditEntry, error)

	// Close releases underlying resources.
	Close() error
}

// ─────────────────────────────────────────────────────────────────────────────
// MemManagementStore — in-memory reference implementation
// ─────────────────────────────────────────────────────────────────────────────

// MemManagementStore is the concurrency-safe in-memory reference for tests and
// local development. It DEFINES the semantics every other implementation must
// match.
type MemManagementStore struct {
	mu       sync.Mutex
	profiles map[string]map[string]ProfileEntry // ulid -> profileName -> entry
	audit    []AuditEntry
	nextID   int64
}

// NewMemManagementStore returns an empty MemManagementStore.
func NewMemManagementStore() *MemManagementStore {
	return &MemManagementStore{
		profiles: make(map[string]map[string]ProfileEntry),
	}
}

// compile-time assertion: MemManagementStore satisfies ManagementStore.
var _ ManagementStore = (*MemManagementStore)(nil)

func (m *MemManagementStore) UpsertProfile(_ context.Context, ulid, profileName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.profiles[ulid] == nil {
		m.profiles[ulid] = make(map[string]ProfileEntry)
	}
	now := time.Now().UTC()
	existing, ok := m.profiles[ulid][profileName]
	if ok {
		existing.UpdatedAt = now
		m.profiles[ulid][profileName] = existing
	} else {
		m.profiles[ulid][profileName] = ProfileEntry{
			ULID:        ulid,
			ProfileName: profileName,
			UpdatedAt:   now,
		}
	}
	return nil
}

func (m *MemManagementStore) RenameProfile(_ context.Context, ulid, oldName, newName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.profiles[ulid] == nil {
		m.profiles[ulid] = make(map[string]ProfileEntry)
	}
	now := time.Now().UTC()
	delete(m.profiles[ulid], oldName)
	m.profiles[ulid][newName] = ProfileEntry{
		ULID:        ulid,
		ProfileName: newName,
		UpdatedAt:   now,
	}
	return nil
}

func (m *MemManagementStore) ListProfiles(_ context.Context, ulid string) ([]ProfileEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	entries := m.profiles[ulid]
	out := make([]ProfileEntry, 0, len(entries))
	for _, e := range entries {
		out = append(out, e)
	}
	// Sort by profile_name for determinism.
	sortProfileEntries(out)
	return out, nil
}

func (m *MemManagementStore) LogAudit(_ context.Context, e AuditEntry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextID++
	e.ID = m.nextID
	if e.CreatedAt.IsZero() {
		e.CreatedAt = time.Now().UTC()
	}
	m.audit = append(m.audit, e)
	return nil
}

func (m *MemManagementStore) AuditLog(_ context.Context, ulid string, n int) ([]AuditEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []AuditEntry
	// Iterate in reverse (most recent first — audit slice is append-only).
	for i := len(m.audit) - 1; i >= 0; i-- {
		if m.audit[i].ULID == ulid {
			out = append(out, m.audit[i])
			if n > 0 && len(out) >= n {
				break
			}
		}
	}
	return out, nil
}

func (m *MemManagementStore) Close() error { return nil }

// sortProfileEntries sorts in-place by ProfileName (ascending).
func sortProfileEntries(s []ProfileEntry) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j].ProfileName < s[j-1].ProfileName; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// SQLManagementStore — production SQLite-backed implementation
// ─────────────────────────────────────────────────────────────────────────────

//go:generate echo "no code generation needed"

// SQLManagementStore is the cpdb-backed ManagementStore (SQLite or Postgres).
type SQLManagementStore struct {
	db *cpdb.DB
	mu sync.Mutex
}

// OpenSQLManagementStore applies schema migrations to db and returns a ready store.
func OpenSQLManagementStore(db *cpdb.DB) (*SQLManagementStore, error) {
	subFS, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("profile: embed sub: %w", err)
	}
	if err := db.Migrate(subFS); err != nil {
		return nil, fmt.Errorf("profile: migrate: %w", err)
	}
	return &SQLManagementStore{db: db}, nil
}

// compile-time assertion: SQLManagementStore satisfies ManagementStore.
var _ ManagementStore = (*SQLManagementStore)(nil)

// Close closes the underlying database connection.
func (s *SQLManagementStore) Close() error { return s.db.Close() }

func (s *SQLManagementStore) UpsertProfile(_ context.Context, ulid, profileName string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.db.Exec(s.db.Rebind(`
		INSERT INTO profile_entries (ulid, profile_name, updated_at)
		VALUES (?, ?, ?)
		ON CONFLICT(ulid, profile_name) DO UPDATE SET updated_at = excluded.updated_at
	`), ulid, profileName, now)
	return err
}

func (s *SQLManagementStore) RenameProfile(_ context.Context, ulid, oldName, newName string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC().Format(time.RFC3339)
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	// Delete the old name (no-op if it doesn't exist).
	if _, err := tx.Exec(s.db.Rebind(`DELETE FROM profile_entries WHERE ulid = ? AND profile_name = ?`), ulid, oldName); err != nil {
		return err
	}
	// Upsert the new name.
	if _, err := tx.Exec(s.db.Rebind(`
		INSERT INTO profile_entries (ulid, profile_name, updated_at)
		VALUES (?, ?, ?)
		ON CONFLICT(ulid, profile_name) DO UPDATE SET updated_at = excluded.updated_at
	`), ulid, newName, now); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQLManagementStore) ListProfiles(_ context.Context, ulid string) ([]ProfileEntry, error) {
	rows, err := s.db.Query(s.db.Rebind(`
		SELECT ulid, profile_name, updated_at
		FROM profile_entries
		WHERE ulid = ?
		ORDER BY profile_name ASC
	`), ulid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ProfileEntry
	for rows.Next() {
		var e ProfileEntry
		var updatedAt string
		if err := rows.Scan(&e.ULID, &e.ProfileName, &updatedAt); err != nil {
			return nil, err
		}
		e.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *SQLManagementStore) LogAudit(_ context.Context, e AuditEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC().Format(time.RFC3339)
	if !e.CreatedAt.IsZero() {
		now = e.CreatedAt.UTC().Format(time.RFC3339)
	}
	_, err := s.db.Exec(s.db.Rebind(`
		INSERT INTO profile_audit (account_id, actor_id, ulid, profile_name, action, created_at)
		VALUES (?, ?, ?, ?, ?, ?)
	`), e.AccountID, e.ActorID, e.ULID, e.ProfileName, e.Action, now)
	return err
}

func (s *SQLManagementStore) AuditLog(_ context.Context, ulid string, n int) ([]AuditEntry, error) {
	baseQ := `SELECT id, account_id, actor_id, ulid, profile_name, action, created_at FROM profile_audit WHERE ulid = ? ORDER BY created_at DESC, id DESC`
	args := []any{ulid}
	if n > 0 {
		baseQ += " LIMIT ?"
		args = append(args, n)
	}
	rows, err := s.db.Query(s.db.Rebind(baseQ), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []AuditEntry
	for rows.Next() {
		var e AuditEntry
		var createdAt string
		if err := rows.Scan(&e.ID, &e.AccountID, &e.ActorID, &e.ULID, &e.ProfileName, &e.Action, &createdAt); err != nil {
			return nil, err
		}
		e.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
		out = append(out, e)
	}
	return out, rows.Err()
}
