// Package multiinstance implements the local instance registry for MINST-01.
//
// The registry is a SQLite-backed manifest of every Vulos instance belonging
// to the current account (BYO devices and Vulos-provisioned Fly instances).
// It is the source of truth for routing decisions and CRDT sync peer lists.
//
// Only the registry itself is implemented here. Cloud sync (MINST-02), app
// routing (MINST-03), CRDT replication (MINST-04), and the dashboard
// (MINST-07) are out of scope for this package.
package multiinstance

import (
	"database/sql"
	"embed"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed migrations
var migrationsFS embed.FS

// Kind classifies the origin of an instance.
type Kind string

const (
	KindDevice Kind = "device" // BYO physical / self-hosted instance
	KindCloud  Kind = "cloud"  // Vulos-provisioned Fly instance
)

// Role describes this instance's relationship to the local account.
type Role string

const (
	RoleOwner Role = "owner" // the account owner's primary instance
	RolePeer  Role = "peer"  // any other instance in the account
)

// Status is the last-known reachability of an instance.
type Status string

const (
	StatusOnline  Status = "online"
	StatusOffline Status = "offline"
	StatusUnknown Status = "unknown"
)

// Instance represents one entry in the local registry.
type Instance struct {
	ULID            string    `json:"ulid"`
	DisplayName     string    `json:"display_name"`
	Kind            Kind      `json:"kind"`
	EndpointURL     string    `json:"endpoint_url"`
	Ed25519PublicKey string   `json:"ed25519_public_key"`
	Role            Role      `json:"role"`
	Status          Status    `json:"status"`
	LastSeenAt      time.Time `json:"last_seen_at"`

	// ── FABRIC-KEY-01: key rotation overlap + revocation ─────────────────────
	//
	// PrevEd25519PublicKey is the instance's PREVIOUS signing key, retained for a
	// grace window after a rotation so in-flight observations signed under the
	// old key still verify (and thus still count toward quorum) until peers have
	// re-learned the new key. Empty when no rotation overlap is active.
	PrevEd25519PublicKey string `json:"prev_ed25519_public_key,omitempty"`
	// PrevKeyExpiresAt is when the PrevEd25519PublicKey overlap window ends; after
	// this the old key is no longer accepted. Zero == no overlap.
	PrevKeyExpiresAt time.Time `json:"prev_key_expires_at,omitempty"`
	// Revoked marks this peer's key as compromised. A revoked instance's signed
	// observations NEVER count toward quorum (verification fails closed),
	// regardless of which (current or previous) key signed them.
	Revoked bool `json:"revoked,omitempty"`
}

// Registry is the local persistent store of all account instances.
// It is safe for concurrent use.
type Registry struct {
	mu sync.RWMutex
	db *sql.DB
}

// Open opens (or creates) the registry database at dbPath and applies
// migrations. On failure it returns a non-nil error; the caller must not use
// the returned Registry in that case.
func Open(dbPath string) (*Registry, error) {
	db, err := openDB(dbPath)
	if err != nil {
		return nil, fmt.Errorf("multiinstance: open db: %w", err)
	}
	return &Registry{db: db}, nil
}

// Close releases the underlying database connection.
func (r *Registry) Close() error {
	if r == nil || r.db == nil {
		return nil
	}
	return r.db.Close()
}

// DB returns the underlying *sql.DB so that co-located packages (e.g.
// AppSync) can share the same database connection without re-opening the file.
// Callers must not close the returned DB directly; use Registry.Close.
func (r *Registry) DB() *sql.DB {
	return r.db
}

// Upsert inserts or updates an instance. Deduplication is by ULID.
// A zero LastSeenAt is left unchanged on update (the caller should use
// MarkSeen separately when presence is confirmed).
func (r *Registry) Upsert(inst Instance) error {
	if inst.ULID == "" {
		return fmt.Errorf("multiinstance: Upsert: ULID must not be empty")
	}
	if inst.Kind == "" {
		inst.Kind = KindDevice
	}
	if inst.Role == "" {
		inst.Role = RolePeer
	}
	if inst.Status == "" {
		inst.Status = StatusUnknown
	}

	lastSeen := formatTime(inst.LastSeenAt)
	prevExpires := formatTime(inst.PrevKeyExpiresAt)
	revoked := 0
	if inst.Revoked {
		revoked = 1
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	_, err := r.db.Exec(`
		INSERT INTO instances
			(ulid, display_name, kind, endpoint_url, ed25519_public_key, role, status, last_seen_at,
			 prev_ed25519_public_key, prev_key_expires_at, revoked)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(ulid) DO UPDATE SET
			display_name      = excluded.display_name,
			kind              = excluded.kind,
			endpoint_url      = excluded.endpoint_url,
			ed25519_public_key= excluded.ed25519_public_key,
			role              = excluded.role,
			status            = excluded.status,
			last_seen_at      = CASE
				WHEN excluded.last_seen_at != '' THEN excluded.last_seen_at
				ELSE instances.last_seen_at
			END,
			prev_ed25519_public_key = excluded.prev_ed25519_public_key,
			prev_key_expires_at     = excluded.prev_key_expires_at,
			revoked                 = excluded.revoked`,
		inst.ULID,
		inst.DisplayName,
		string(inst.Kind),
		inst.EndpointURL,
		inst.Ed25519PublicKey,
		string(inst.Role),
		string(inst.Status),
		lastSeen,
		inst.PrevEd25519PublicKey,
		prevExpires,
		revoked,
	)
	if err != nil {
		return fmt.Errorf("multiinstance: Upsert %s: %w", inst.ULID, err)
	}
	return nil
}

// Get returns the instance with the given ULID, or (Instance{}, false) if not found.
func (r *Registry) Get(ulid string) (Instance, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	row := r.db.QueryRow(`
		SELECT ulid, display_name, kind, endpoint_url, ed25519_public_key, role, status, last_seen_at,
		       prev_ed25519_public_key, prev_key_expires_at, revoked
		FROM instances WHERE ulid = ?`, ulid)
	inst, err := scanInstance(row)
	if err == sql.ErrNoRows {
		return Instance{}, false
	}
	if err != nil {
		log.Printf("[multiinstance] Get %s: %v", ulid, err)
		return Instance{}, false
	}
	return inst, true
}

// List returns all instances in the registry. Order is by ULID (stable,
// time-ordered because ULIDs embed a millisecond timestamp).
func (r *Registry) List() ([]Instance, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	rows, err := r.db.Query(`
		SELECT ulid, display_name, kind, endpoint_url, ed25519_public_key, role, status, last_seen_at,
		       prev_ed25519_public_key, prev_key_expires_at, revoked
		FROM instances ORDER BY ulid`)
	if err != nil {
		return nil, fmt.Errorf("multiinstance: List: %w", err)
	}
	defer rows.Close()

	var out []Instance
	for rows.Next() {
		inst, err := scanInstance(rows)
		if err != nil {
			return nil, fmt.Errorf("multiinstance: List scan: %w", err)
		}
		out = append(out, inst)
	}
	return out, rows.Err()
}

// MarkSeen updates the status to online and records the current time as
// last_seen_at for the instance with the given ULID. If the ULID is not
// present in the registry the call is a no-op.
func (r *Registry) MarkSeen(ulid string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	_, err := r.db.Exec(`
		UPDATE instances
		SET status = 'online', last_seen_at = ?
		WHERE ulid = ?`,
		formatTime(time.Now()), ulid)
	if err != nil {
		return fmt.Errorf("multiinstance: MarkSeen %s: %w", ulid, err)
	}
	return nil
}

// --- helpers ---

// scanner is the common interface satisfied by *sql.Row and *sql.Rows.
type scanner interface {
	Scan(dest ...any) error
}

func scanInstance(s scanner) (Instance, error) {
	var (
		inst           Instance
		kind           string
		role           string
		status         string
		lastSeenRaw    string
		prevExpiresRaw string
		revokedInt     int
	)
	err := s.Scan(
		&inst.ULID,
		&inst.DisplayName,
		&kind,
		&inst.EndpointURL,
		&inst.Ed25519PublicKey,
		&role,
		&status,
		&lastSeenRaw,
		&inst.PrevEd25519PublicKey,
		&prevExpiresRaw,
		&revokedInt,
	)
	if err != nil {
		return Instance{}, err
	}
	inst.Kind = Kind(kind)
	inst.Role = Role(role)
	inst.Status = Status(status)
	inst.Revoked = revokedInt != 0
	if lastSeenRaw != "" {
		if t, err := time.Parse(time.RFC3339Nano, lastSeenRaw); err == nil {
			inst.LastSeenAt = t
		}
	}
	if prevExpiresRaw != "" {
		if t, err := time.Parse(time.RFC3339Nano, prevExpiresRaw); err == nil {
			inst.PrevKeyExpiresAt = t
		}
	}
	return inst, nil
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

// openDB opens (creating if needed) the SQLite database and runs migrations.
func openDB(path string) (*sql.DB, error) {
	dsn := path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// modernc/sqlite is not safe for unbounded concurrent writers on one file.
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// migrate applies all embedded SQL migration files in lexicographic order.
// Every statement uses IF NOT EXISTS so running the migrations repeatedly is safe.
func migrate(db *sql.DB) error {
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("migrate: read migrations dir: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		sqlBytes, err := migrationsFS.ReadFile("migrations/" + entry.Name())
		if err != nil {
			return fmt.Errorf("migrate: read %s: %w", entry.Name(), err)
		}
		if _, err := db.Exec(string(sqlBytes)); err != nil {
			return fmt.Errorf("migrate: exec %s: %w", entry.Name(), err)
		}
	}
	// Additive columns that an already-migrated DB (created before 0006) lacks.
	// modernc.org/sqlite has no "ADD COLUMN IF NOT EXISTS", so we add each column
	// and tolerate the "duplicate column name" error that a fresh DB (which
	// already carries the column from the 0006 CREATE TABLE) returns.
	if err := ensureObservationEpochColumns(db); err != nil {
		return fmt.Errorf("migrate: observation epoch columns: %w", err)
	}
	if err := ensureInstanceKeyRotationColumns(db); err != nil {
		return fmt.Errorf("migrate: instance key-rotation columns: %w", err)
	}
	return nil
}

// ensureInstanceKeyRotationColumns adds the FABRIC-KEY-01 rotation/revocation
// columns to the instances table for a DB created before these columns existed.
// Idempotent: a "duplicate column name" error means the column is already
// present (fresh DB or a prior run) and is swallowed. modernc.org/sqlite has no
// "ADD COLUMN IF NOT EXISTS" so we add each and tolerate the duplicate error.
func ensureInstanceKeyRotationColumns(db *sql.DB) error {
	stmts := []string{
		`ALTER TABLE instances ADD COLUMN prev_ed25519_public_key TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE instances ADD COLUMN prev_key_expires_at TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE instances ADD COLUMN revoked INTEGER NOT NULL DEFAULT 0`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			if strings.Contains(err.Error(), "duplicate column name") {
				continue
			}
			return err
		}
	}
	return nil
}

// ensureObservationEpochColumns adds the epoch / observer_pubkey columns to a
// pre-0006 app_uninstall_observations table. It is idempotent: on a fresh DB the
// columns already exist (carried by the 0006 CREATE TABLE) and the ALTERs return
// a "duplicate column name" error, which is swallowed.
func ensureObservationEpochColumns(db *sql.DB) error {
	stmts := []string{
		`ALTER TABLE app_uninstall_observations ADD COLUMN epoch INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE app_uninstall_observations ADD COLUMN observer_pubkey TEXT NOT NULL DEFAULT ''`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			if strings.Contains(err.Error(), "duplicate column name") {
				continue
			}
			return err
		}
	}
	// The epoch index can only be created once the column is guaranteed to exist
	// (above), so it lives here rather than in the .sql file (which races 0005).
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_app_uninstall_obs_epoch
		ON app_uninstall_observations(instance_ulid, app_id, epoch)`); err != nil {
		return err
	}
	return nil
}
