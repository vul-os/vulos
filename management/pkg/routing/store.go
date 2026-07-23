package routing

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"sort"
	"time"

	"github.com/vul-os/vulos-management/pkg/cpdb"
	"github.com/vul-os/vulos-management/pkg/idents"
)

// SQLStore is the production cpdb-backed implementation of Store.
// It supports both SQLite (default) and Postgres (when DATABASE_URL is set).
// Behaviour is byte-for-byte semantically identical to MemStore.
type SQLStore struct {
	db *cpdb.DB
}

// Open applies migrations to db and returns a ready-to-use *SQLStore.
//
// db should be obtained from cpdb.Open("routing") for production, or from
// cpdb.OpenSQLiteDSN(":memory:") for tests. Migrations are applied
// automatically and are idempotent (safe to call on every startup).
func Open(db *cpdb.DB) (*SQLStore, error) {
	subFS, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("routing: embed sub: %w", err)
	}
	if err := db.Migrate(subFS); err != nil {
		return nil, fmt.Errorf("routing: migrate: %w", err)
	}
	return &SQLStore{db: db}, nil
}

// Close closes the underlying database connection.
func (s *SQLStore) Close() error { return s.db.Close() }

// compile-time assertion: SQLStore satisfies Store.
var _ Store = (*SQLStore)(nil)

// --------------------------------------------------------------------------
// Bindings
// --------------------------------------------------------------------------

// Enroll is idempotent. Binding an already-bound ULID to the SAME account
// returns the existing binding with the mode refreshed. Binding to a DIFFERENT
// account returns ErrULIDBoundToOtherAccount.
func (s *SQLStore) Enroll(ctx context.Context, ulid, accountID string, mode ConnMode) (Binding, error) {
	if ulid == "" || accountID == "" {
		return Binding{}, ErrInvalidULID
	}
	if !ValidConnMode(mode) {
		return Binding{}, ErrInvalidMode
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Binding{}, fmt.Errorf("routing: enroll begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	// Check for existing binding.
	var existing Binding
	var createdStr, updatedStr string
	err = tx.QueryRowContext(ctx,
		s.db.Rebind(`SELECT ulid, account_id, mode, direct_ip, created_at, updated_at
		   FROM bindings WHERE ulid = ?`), ulid,
	).Scan(&existing.ULID, &existing.AccountID, &existing.Mode,
		&existing.DirectIP, &createdStr, &updatedStr)

	switch {
	case err == sql.ErrNoRows:
		// New binding.
		now := time.Now().UTC().Format(time.RFC3339Nano)
		_, err = tx.ExecContext(ctx,
			s.db.Rebind(`INSERT INTO bindings (ulid, account_id, mode, direct_ip, created_at, updated_at)
			 VALUES (?, ?, ?, '', ?, ?)`),
			ulid, accountID, string(mode), now, now,
		)
		if err != nil {
			return Binding{}, fmt.Errorf("routing: enroll insert: %w", err)
		}
		if err = tx.Commit(); err != nil {
			return Binding{}, fmt.Errorf("routing: enroll commit: %w", err)
		}
		t := parseTime(now)
		return Binding{
			ULID:      ulid,
			AccountID: accountID,
			Mode:      mode,
			CreatedAt: t,
			UpdatedAt: t,
		}, nil

	case err != nil:
		return Binding{}, fmt.Errorf("routing: enroll query: %w", err)

	default:
		// Existing binding.
		if existing.AccountID != accountID {
			return Binding{}, ErrULIDBoundToOtherAccount
		}
		// Refresh mode and updated_at.
		now := time.Now().UTC().Format(time.RFC3339Nano)
		_, err = tx.ExecContext(ctx,
			s.db.Rebind(`UPDATE bindings SET mode = ?, updated_at = ? WHERE ulid = ?`),
			string(mode), now, ulid,
		)
		if err != nil {
			return Binding{}, fmt.Errorf("routing: enroll update: %w", err)
		}
		if err = tx.Commit(); err != nil {
			return Binding{}, fmt.Errorf("routing: enroll commit: %w", err)
		}
		existing.Mode = mode
		existing.CreatedAt = parseTime(createdStr)
		existing.UpdatedAt = parseTime(now)
		return existing, nil
	}
}

// GetBinding returns the binding for ulid, or ErrNotEnrolled if not found.
func (s *SQLStore) GetBinding(ctx context.Context, ulid string) (Binding, error) {
	var b Binding
	var createdStr, updatedStr string
	err := s.db.QueryRowContext(ctx,
		s.db.Rebind(`SELECT ulid, account_id, mode, direct_ip, created_at, updated_at
		   FROM bindings WHERE ulid = ?`), ulid,
	).Scan(&b.ULID, &b.AccountID, &b.Mode, &b.DirectIP, &createdStr, &updatedStr)
	if err == sql.ErrNoRows {
		return Binding{}, ErrNotEnrolled
	}
	if err != nil {
		return Binding{}, fmt.Errorf("routing: get binding: %w", err)
	}
	b.CreatedAt = parseTime(createdStr)
	b.UpdatedAt = parseTime(updatedStr)
	return b, nil
}

// SetConnMode updates the connection mode for an already-enrolled ULID.
func (s *SQLStore) SetConnMode(ctx context.Context, ulid string, mode ConnMode) error {
	if !ValidConnMode(mode) {
		return ErrInvalidMode
	}

	// Verify the binding exists first (MemStore semantics: ErrNotEnrolled if not found).
	var dummy string
	err := s.db.QueryRowContext(ctx,
		s.db.Rebind(`SELECT ulid FROM bindings WHERE ulid = ?`), ulid).Scan(&dummy)
	if err == sql.ErrNoRows {
		return ErrNotEnrolled
	}
	if err != nil {
		return fmt.Errorf("routing: set conn mode query: %w", err)
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = s.db.ExecContext(ctx,
		s.db.Rebind(`UPDATE bindings SET mode = ?, updated_at = ? WHERE ulid = ?`),
		string(mode), now, ulid,
	)
	if err != nil {
		return fmt.Errorf("routing: set conn mode: %w", err)
	}
	return nil
}

// SetDirectIP sets the direct IP for a ULID. accountID must own the ULID.
// Returns ErrNotFound for invalid IP, ErrNotEnrolled if ULID unknown,
// ErrNotOwner if accountID doesn't own it.
func (s *SQLStore) SetDirectIP(ctx context.Context, ulid, accountID, ip string) error {
	if !ValidDirectIP(ip) {
		return ErrNotFound
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("routing: set direct ip begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	var existingAccount string
	err = tx.QueryRowContext(ctx,
		s.db.Rebind(`SELECT account_id FROM bindings WHERE ulid = ?`), ulid).Scan(&existingAccount)
	if err == sql.ErrNoRows {
		return ErrNotEnrolled
	}
	if err != nil {
		return fmt.Errorf("routing: set direct ip query: %w", err)
	}
	if existingAccount != accountID {
		return ErrNotOwner
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = tx.ExecContext(ctx,
		s.db.Rebind(`UPDATE bindings SET direct_ip = ?, updated_at = ? WHERE ulid = ?`),
		ip, now, ulid,
	)
	if err != nil {
		return fmt.Errorf("routing: set direct ip update: %w", err)
	}
	return tx.Commit()
}

// --------------------------------------------------------------------------
// Devices
// --------------------------------------------------------------------------

// UpsertDevice inserts or replaces a device record.
func (s *SQLStore) UpsertDevice(ctx context.Context, d Device) error {
	if d.ID == "" || d.ULID == "" {
		return ErrNotFound
	}
	lastSeen := d.LastSeen.UTC().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx,
		s.db.Rebind(`INSERT INTO devices (id, ulid, account_id, mode, last_seen)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET
		     ulid       = excluded.ulid,
		     account_id = excluded.account_id,
		     mode       = excluded.mode,
		     last_seen  = excluded.last_seen`),
		d.ID, d.ULID, d.AccountID, string(d.Mode), lastSeen,
	)
	if err != nil {
		return fmt.Errorf("routing: upsert device: %w", err)
	}
	return nil
}

// ListDevices returns all devices for a given ULID, sorted by ID.
func (s *SQLStore) ListDevices(ctx context.Context, ulid string) ([]Device, error) {
	rows, err := s.db.QueryContext(ctx,
		s.db.Rebind(`SELECT id, ulid, account_id, mode, last_seen
		   FROM devices WHERE ulid = ? ORDER BY id`), ulid,
	)
	if err != nil {
		return nil, fmt.Errorf("routing: list devices: %w", err)
	}
	defer rows.Close()

	var out []Device
	for rows.Next() {
		var d Device
		var lastSeenStr string
		if err := rows.Scan(&d.ID, &d.ULID, &d.AccountID, &d.Mode, &lastSeenStr); err != nil {
			return nil, fmt.Errorf("routing: list devices scan: %w", err)
		}
		d.LastSeen = parseTime(lastSeenStr)
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("routing: list devices rows: %w", err)
	}
	return out, nil
}

// --------------------------------------------------------------------------
// PoPs
// --------------------------------------------------------------------------

// UpsertPoP inserts or replaces a PoP record.
func (s *SQLStore) UpsertPoP(ctx context.Context, p PoP) error {
	if p.ID == "" {
		return ErrNotFound
	}
	lastHealth := p.LastHealth.UTC().Format(time.RFC3339Nano)
	healthy := 0
	if p.Healthy {
		healthy = 1
	}
	_, err := s.db.ExecContext(ctx,
		s.db.Rebind(`INSERT INTO pops (id, region, ip, lat, lon, healthy, last_health)
		 VALUES (?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET
		     region      = excluded.region,
		     ip          = excluded.ip,
		     lat         = excluded.lat,
		     lon         = excluded.lon,
		     healthy     = excluded.healthy,
		     last_health = excluded.last_health`),
		p.ID, p.Region, p.IP, p.Lat, p.Lon, healthy, lastHealth,
	)
	if err != nil {
		return fmt.Errorf("routing: upsert pop: %w", err)
	}
	return nil
}

// SetPoPHealth updates the health flag and last_health timestamp for a PoP.
// Returns ErrNotFound if popID is unknown.
func (s *SQLStore) SetPoPHealth(ctx context.Context, popID string, healthy bool) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	h := 0
	if healthy {
		h = 1
	}
	res, err := s.db.ExecContext(ctx,
		s.db.Rebind(`UPDATE pops SET healthy = ?, last_health = ? WHERE id = ?`),
		h, now, popID,
	)
	if err != nil {
		return fmt.Errorf("routing: set pop health: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("routing: set pop health rows: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// HealthyPoPs returns all healthy PoPs, sorted by ID (matches MemStore).
func (s *SQLStore) HealthyPoPs(ctx context.Context) ([]PoP, error) {
	rows, err := s.db.QueryContext(ctx,
		s.db.Rebind(`SELECT id, region, ip, lat, lon, healthy, last_health
		   FROM pops WHERE healthy = 1`),
	)
	if err != nil {
		return nil, fmt.Errorf("routing: healthy pops: %w", err)
	}
	defer rows.Close()

	var out []PoP
	for rows.Next() {
		var p PoP
		var healthyInt int
		var lastHealthStr string
		if err := rows.Scan(&p.ID, &p.Region, &p.IP, &p.Lat, &p.Lon, &healthyInt, &lastHealthStr); err != nil {
			return nil, fmt.Errorf("routing: healthy pops scan: %w", err)
		}
		p.Healthy = healthyInt != 0
		p.LastHealth = parseTime(lastHealthStr)
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("routing: healthy pops rows: %w", err)
	}
	// Sort by ID to match MemStore determinism.
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// ListPoPs returns all PoPs (healthy and unhealthy), sorted by ID.
func (s *SQLStore) ListPoPs(ctx context.Context) ([]PoP, error) {
	rows, err := s.db.QueryContext(ctx,
		s.db.Rebind(`SELECT id, region, ip, lat, lon, healthy, last_health
		   FROM pops ORDER BY id`),
	)
	if err != nil {
		return nil, fmt.Errorf("routing: list pops: %w", err)
	}
	defer rows.Close()

	var out []PoP
	for rows.Next() {
		var p PoP
		var healthyInt int
		var lastHealthStr string
		if err := rows.Scan(&p.ID, &p.Region, &p.IP, &p.Lat, &p.Lon, &healthyInt, &lastHealthStr); err != nil {
			return nil, fmt.Errorf("routing: list pops scan: %w", err)
		}
		p.Healthy = healthyInt != 0
		p.LastHealth = parseTime(lastHealthStr)
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("routing: list pops rows: %w", err)
	}
	return out, nil
}

// --------------------------------------------------------------------------
// Vanity
// --------------------------------------------------------------------------

// ClaimVanity is idempotent for (name, ulid, accountID). A different owner/target
// for an existing name returns ErrVanityTaken. Invalid names return ErrInvalidName.
func (s *SQLStore) ClaimVanity(ctx context.Context, name, ulid, accountID string) error {
	if !idents.ValidName(name) {
		return ErrInvalidName
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("routing: claim vanity begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	var existingULID, existingAccount string
	err = tx.QueryRowContext(ctx,
		s.db.Rebind(`SELECT ulid, account_id FROM vanity WHERE name = ?`), name,
	).Scan(&existingULID, &existingAccount)

	switch {
	case err == sql.ErrNoRows:
		// New claim.
		_, err = tx.ExecContext(ctx,
			s.db.Rebind(`INSERT INTO vanity (name, ulid, account_id) VALUES (?, ?, ?)`),
			name, ulid, accountID,
		)
		if err != nil {
			return fmt.Errorf("routing: claim vanity insert: %w", err)
		}
		return tx.Commit()

	case err != nil:
		return fmt.Errorf("routing: claim vanity query: %w", err)

	default:
		// Existing claim — idempotent if same owner/target, taken otherwise.
		if existingULID != ulid || existingAccount != accountID {
			return ErrVanityTaken
		}
		return nil // same owner/target: no-op
	}
}

// ResolveVanity looks up a vanity name and returns the Vanity record,
// or ErrNotFound if the name has not been claimed.
func (s *SQLStore) ResolveVanity(ctx context.Context, name string) (Vanity, error) {
	var v Vanity
	err := s.db.QueryRowContext(ctx,
		s.db.Rebind(`SELECT name, ulid, account_id FROM vanity WHERE name = ?`), name,
	).Scan(&v.Name, &v.ULID, &v.AccountID)
	if err == sql.ErrNoRows {
		return Vanity{}, ErrNotFound
	}
	if err != nil {
		return Vanity{}, fmt.Errorf("routing: resolve vanity: %w", err)
	}
	return v, nil
}

// ReleaseVanity removes a vanity name. The caller must be the owner (accountID
// must match); otherwise ErrNotOwner is returned. ErrNotFound if name unknown.
func (s *SQLStore) ReleaseVanity(ctx context.Context, name, accountID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("routing: release vanity begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	var existingAccount string
	err = tx.QueryRowContext(ctx,
		s.db.Rebind(`SELECT account_id FROM vanity WHERE name = ?`), name,
	).Scan(&existingAccount)
	if err == sql.ErrNoRows {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("routing: release vanity query: %w", err)
	}
	if existingAccount != accountID {
		return ErrNotOwner
	}
	_, err = tx.ExecContext(ctx, s.db.Rebind(`DELETE FROM vanity WHERE name = ?`), name)
	if err != nil {
		return fmt.Errorf("routing: release vanity delete: %w", err)
	}
	return tx.Commit()
}

// ListVanityByAccount returns all vanity records owned by accountID, sorted
// by name (empty slice if none).
func (s *SQLStore) ListVanityByAccount(ctx context.Context, accountID string) ([]Vanity, error) {
	rows, err := s.db.QueryContext(ctx,
		s.db.Rebind(`SELECT name, ulid, account_id FROM vanity WHERE account_id = ? ORDER BY name`),
		accountID,
	)
	if err != nil {
		return nil, fmt.Errorf("routing: list vanity: %w", err)
	}
	defer rows.Close()

	var out []Vanity
	for rows.Next() {
		var v Vanity
		if err := rows.Scan(&v.Name, &v.ULID, &v.AccountID); err != nil {
			return nil, fmt.Errorf("routing: list vanity scan: %w", err)
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("routing: list vanity rows: %w", err)
	}
	if out == nil {
		out = []Vanity{}
	}
	return out, nil
}

// --------------------------------------------------------------------------
// Helpers
// --------------------------------------------------------------------------

// parseTime parses an RFC3339 or RFC3339Nano timestamp, returning zero time on failure.
func parseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		t, _ = time.Parse(time.RFC3339, s)
	}
	return t
}
