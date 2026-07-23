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
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	dbmigrate "vulos/backend/internal/migrate"

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
	ULID             string    `json:"ulid"`
	DisplayName      string    `json:"display_name"`
	Kind             Kind      `json:"kind"`
	EndpointURL      string    `json:"endpoint_url"`
	Ed25519PublicKey string    `json:"ed25519_public_key"`
	Role             Role      `json:"role"`
	Status           Status    `json:"status"`
	LastSeenAt       time.Time `json:"last_seen_at"`

	// Region is the home cell of this instance (default "eu").
	// Phase-0: only "eu" exists; a second cell is config-only later.
	// Additive field — existing rows without this column read back as "eu"
	// via the DEFAULT 'eu' in the DB schema.
	Region string `json:"region,omitempty"`

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

	// ── NODE-CAP-01: store-only members ──────────────────────────────────────
	//
	// StoreOnly marks this instance as a cluster member that SYNCS data but is
	// never an ingress / route target (e.g. a personal laptop that replicates
	// the account but must not serve app traffic to the cluster). The routing
	// table (router.go BuildTable) and notification fan-out (notifyfanout.go)
	// exclude store-only instances; presence/sync are unaffected, so a
	// store-only member still shows online and still replicates.
	//
	// The zero value (false) means "serves normally" — this is deliberate: every
	// existing code path that constructs an Instance without touching the field
	// keeps serving, and existing DB rows default to 0. A box becomes store-only
	// only by explicit opt-in (the Settings toggle, or VULOS_STORE_ONLY on a
	// headless box); it is NEVER derived from NodeMode/DomainMode=local, because
	// a single-box local install is its own only server and must keep serving
	// itself.
	StoreOnly bool `json:"store_only,omitempty"`
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
	storeOnly := 0
	if inst.StoreOnly {
		storeOnly = 1
	}
	region := inst.Region
	if region == "" {
		region = "eu"
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	_, err := r.db.Exec(`
		INSERT INTO instances
			(ulid, display_name, kind, endpoint_url, ed25519_public_key, role, status, last_seen_at,
			 region, prev_ed25519_public_key, prev_key_expires_at, revoked, store_only)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
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
			region                  = excluded.region,
			prev_ed25519_public_key = excluded.prev_ed25519_public_key,
			prev_key_expires_at     = excluded.prev_key_expires_at,
			revoked                 = excluded.revoked,
			-- NODE-CAP-01: an EXISTING owner row's serving posture is
			-- box-authoritative — only SetStoreOnly (a targeted UPDATE) may
			-- change it. A sync/rotation/identity Upsert must never move it, so
			-- the CP (which does not yet round-trip the flag) can't clobber a
			-- locally-set owner store-only back to serving. This is done in SQL,
			-- atomically with the write, so there is no read-then-write TOCTOU
			-- against a concurrent SetStoreOnly. Peers (role != owner) and the
			-- first INSERT (no conflict) take the provided value as before.
			store_only              = CASE
				WHEN instances.role = 'owner' THEN instances.store_only
				ELSE excluded.store_only
			END`,
		inst.ULID,
		inst.DisplayName,
		string(inst.Kind),
		inst.EndpointURL,
		inst.Ed25519PublicKey,
		string(inst.Role),
		string(inst.Status),
		lastSeen,
		region,
		inst.PrevEd25519PublicKey,
		prevExpires,
		revoked,
		storeOnly,
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
		       region, prev_ed25519_public_key, prev_key_expires_at, revoked, store_only
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
		       region, prev_ed25519_public_key, prev_key_expires_at, revoked, store_only
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

// ErrNotFound is returned by Rename and Delete when the ULID is not in the
// registry. Callers map it to a 404 — a rename/remove that silently no-ops on
// an unknown instance would report success for work that never happened.
var ErrNotFound = errors.New("multiinstance: instance not found")

// ErrIsOwner is returned by Delete for the account's own (role=owner) instance.
// The registry describes the fleet FROM this box; removing the box from its own
// fleet would leave a registry with no local identity to route or sync from.
var ErrIsOwner = errors.New("multiinstance: cannot remove the owner instance")

// MaxDisplayNameLen bounds a Rename. Names are shown in the dashboard, not used
// as identifiers, so a modest bound is enough to keep the UI (and the registry)
// from carrying an unbounded blob.
const MaxDisplayNameLen = 64

// Rename sets the display name of one instance and returns the updated row.
// The name is trimmed and validated (non-empty, bounded, no control characters).
// An unknown ULID yields ErrNotFound.
func (r *Registry) Rename(ulid, displayName string) (Instance, error) {
	name := strings.TrimSpace(displayName)
	if name == "" {
		return Instance{}, fmt.Errorf("multiinstance: Rename: display name must not be empty")
	}
	if len([]rune(name)) > MaxDisplayNameLen {
		return Instance{}, fmt.Errorf("multiinstance: Rename: display name longer than %d characters", MaxDisplayNameLen)
	}
	if strings.ContainsFunc(name, func(c rune) bool { return c < 0x20 || c == 0x7f }) {
		return Instance{}, fmt.Errorf("multiinstance: Rename: display name must not contain control characters")
	}

	r.mu.Lock()
	res, err := r.db.Exec(`UPDATE instances SET display_name = ? WHERE ulid = ?`, name, ulid)
	r.mu.Unlock()
	if err != nil {
		return Instance{}, fmt.Errorf("multiinstance: Rename %s: %w", ulid, err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return Instance{}, ErrNotFound
	}

	inst, ok := r.Get(ulid)
	if !ok {
		return Instance{}, ErrNotFound
	}
	return inst, nil
}

// Delete removes one instance from the fleet: the registry row plus the app
// inventory replicated for it (app_registry), so the removed instance stops
// appearing in the roster, in /api/instances/{ulid}/apps, and in the routing
// table. Both writes happen in one transaction — a half-removed instance would
// keep routing traffic to a box the user believes is gone.
//
// It refuses the owner instance (ErrIsOwner) and an unknown ULID (ErrNotFound).
func (r *Registry) Delete(ulid string) error {
	inst, ok := r.Get(ulid)
	if !ok {
		return ErrNotFound
	}
	if inst.Role == RoleOwner {
		return ErrIsOwner
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	tx, err := r.db.Begin()
	if err != nil {
		return fmt.Errorf("multiinstance: Delete %s: begin: %w", ulid, err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DELETE FROM app_registry WHERE instance_ulid = ?`, ulid); err != nil {
		return fmt.Errorf("multiinstance: Delete %s: app_registry: %w", ulid, err)
	}
	res, err := tx.Exec(`DELETE FROM instances WHERE ulid = ?`, ulid)
	if err != nil {
		return fmt.Errorf("multiinstance: Delete %s: instances: %w", ulid, err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return ErrNotFound
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("multiinstance: Delete %s: commit: %w", ulid, err)
	}
	return nil
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

// SetStoreOnly sets the store-only flag on one instance and returns the updated
// row. A store-only instance syncs but is excluded from routing / notification
// fan-out (NODE-CAP-01). An unknown ULID yields ErrNotFound.
//
// Propagation caveat: this writes the LOCAL registry only. For a store-only
// choice to be honored by peers other than this box, the control plane must
// round-trip the flag (store_only in the /api/instances wire format). Setting
// it on a remote peer's row here affects only this box's local view until that
// CP round-trip exists. In practice the Settings toggle targets the owner
// (this box), which takes effect immediately because this box builds its own
// routing table from this registry.
func (r *Registry) SetStoreOnly(ulid string, storeOnly bool) (Instance, error) {
	v := 0
	if storeOnly {
		v = 1
	}

	r.mu.Lock()
	res, err := r.db.Exec(`UPDATE instances SET store_only = ? WHERE ulid = ?`, v, ulid)
	r.mu.Unlock()
	if err != nil {
		return Instance{}, fmt.Errorf("multiinstance: SetStoreOnly %s: %w", ulid, err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return Instance{}, ErrNotFound
	}

	inst, ok := r.Get(ulid)
	if !ok {
		return Instance{}, ErrNotFound
	}
	return inst, nil
}

// --- helpers ---

// storeOnlyEnv reports whether this box was configured store-only via the
// VULOS_STORE_ONLY environment variable — the headless equivalent of the
// Settings toggle. Explicit opt-in only ({1,true,yes}); it is deliberately NOT
// derived from NodeMode/DomainMode=local, because a single-box local install is
// its own only server and must keep serving itself (NODE-CAP-01).
func storeOnlyEnv() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("VULOS_STORE_ONLY"))) {
	case "1", "true", "yes":
		return true
	}
	return false
}

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
		storeOnlyInt   int
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
		&inst.Region,
		&inst.PrevEd25519PublicKey,
		&prevExpiresRaw,
		&revokedInt,
		&storeOnlyInt,
	)
	if err != nil {
		return Instance{}, err
	}
	inst.Kind = Kind(kind)
	inst.Role = Role(role)
	inst.Status = Status(status)
	inst.Revoked = revokedInt != 0
	inst.StoreOnly = storeOnlyInt != 0
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

// migrate applies all embedded SQL migrations via the shared forward-only
// runner (version-tracked, transactional, fail-closed).
func migrate(db *sql.DB) error {
	return dbmigrate.Apply(db, migrationsFS, "migrations")
}
