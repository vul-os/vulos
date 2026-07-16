// Package multiloc implements the central-bucket multi-location model for
// Vulos orgs.
//
// Architecture:
//
//	An org's multiple boxes (locations) all point at ONE shared bucket
//	(the managed store for hosted, or a single MinIO for complete-BYO).
//	CRDT + bucket-lease coordinator (already exist) handle concurrent writes.
//	This package manages the locations table and the routing logic that picks
//	a healthy destination for inbound mail.
//
// Storage: cpdb dual-backend seam (SQLite for self-host, Postgres for cloud).
// Migrations are embedded via go:embed and run idempotently on Open.
//
// Tasks addressed:
//
//	STORE-MULTLOC-02: locations table + Enroll/List/HealthyLocations/MarkHealth
//	CP-MULTLOC-01: Store implementation (this file)
//	CP-MULTLOC-02: MemStore for tests + routing logic
//	MAIL-STORE-03: inbound routing (PickLocation / HealthyLocations)
package multiloc

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"math/rand"
	"sync"
	"time"

	"github.com/vul-os/vulos-management/pkg/cpdb"
)

// ─────────────────────────────────────────────────────────────────────────────
// Sentinel errors
// ─────────────────────────────────────────────────────────────────────────────

var (
	// ErrLocationNotFound is returned when a (org_id, location_id) pair does not
	// exist in the store.
	ErrLocationNotFound = errors.New("multiloc: location not found")

	// ErrAlreadyEnrolled is returned by Enroll when the location_id is already
	// registered for that org.
	ErrAlreadyEnrolled = errors.New("multiloc: location already enrolled")

	// ErrAllLocationsUnhealthy is returned by PickLocation when every location in
	// the org is currently marked unhealthy.
	ErrAllLocationsUnhealthy = errors.New("multiloc: all locations are unhealthy")
)

// ─────────────────────────────────────────────────────────────────────────────
// Domain types
// ─────────────────────────────────────────────────────────────────────────────

// Location represents one enrolled box/location for an org.
type Location struct {
	OrgID      string
	LocationID string
	Name       string
	Endpoint   string    // base URL, e.g. "https://box.example.com"
	LastSeenAt time.Time // zero if never seen
	Healthy    bool
	EnrolledAt time.Time
}

// EnrollRequest carries the parameters for enrolling a new location.
type EnrollRequest struct {
	// OrgID is the organisation that owns this location.
	OrgID string

	// LocationID is a caller-supplied stable identifier (e.g. a ULID or UUID).
	// Must be unique within the org.
	LocationID string

	// Name is a human-readable label (e.g. "EU-West-1").
	Name string

	// Endpoint is the base URL of the box (e.g. "https://box.example.com").
	Endpoint string
}

// ─────────────────────────────────────────────────────────────────────────────
// Store interface
// ─────────────────────────────────────────────────────────────────────────────

// Store is the persistence contract for multi-location management.
// Both SQLStore and MemStore implement this interface; handlers depend only
// on the interface.
type Store interface {
	// Enroll registers a new location for an org.  Returns ErrAlreadyEnrolled if
	// the (OrgID, LocationID) pair already exists.
	Enroll(ctx context.Context, req EnrollRequest) (Location, error)

	// List returns all locations registered for orgID.
	List(ctx context.Context, orgID string) ([]Location, error)

	// HealthyLocations returns only the locations whose healthy flag is true.
	HealthyLocations(ctx context.Context, orgID string) ([]Location, error)

	// MarkHealth sets the healthy flag and updates last_seen_at for the
	// given (orgID, locationID) pair.  Returns ErrLocationNotFound when the
	// pair does not exist.
	MarkHealth(ctx context.Context, orgID, locationID string, healthy bool) error

	// PickLocation selects a healthy location for inbound mail delivery using
	// round-robin among HealthyLocations.  Returns ErrAllLocationsUnhealthy when
	// no healthy location exists (caller should hold in queue).
	PickLocation(ctx context.Context, orgID string) (Location, error)

	Close() error
}

// ─────────────────────────────────────────────────────────────────────────────
// SQLStore — production cpdb backend
// ─────────────────────────────────────────────────────────────────────────────

// SQLStore is the cpdb-backed implementation of Store.
// Concurrency-safe via the embedded *cpdb.DB writer discipline.
type SQLStore struct {
	db *cpdb.DB
	mu sync.Mutex // serialises writes
}

// OpenStore opens the multiloc store using the provided cpdb.DB and runs
// the embedded schema migration idempotently.
func OpenStore(db *cpdb.DB) (*SQLStore, error) {
	subFS, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("multiloc: embed sub: %w", err)
	}
	if err := db.Migrate(subFS); err != nil {
		return nil, fmt.Errorf("multiloc: migrate: %w", err)
	}
	return &SQLStore{db: db}, nil
}

// Close closes the underlying database connection.
func (s *SQLStore) Close() error { return s.db.Close() }

// Enroll registers a new location for an org.
func (s *SQLStore) Enroll(ctx context.Context, req EnrollRequest) (Location, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC()
	_, err := s.db.ExecContext(ctx,
		s.db.Rebind(`INSERT INTO locations (org_id, location_id, name, endpoint, enrolled_at, healthy, last_seen_at)
		 VALUES (?, ?, ?, ?, ?, 0, 0)`),
		req.OrgID, req.LocationID, req.Name, req.Endpoint, now.Unix(),
	)
	if err != nil {
		// UNIQUE constraint violation → already enrolled.
		if isUniqueViolation(err) {
			return Location{}, ErrAlreadyEnrolled
		}
		return Location{}, fmt.Errorf("multiloc: enroll: %w", err)
	}
	return Location{
		OrgID:      req.OrgID,
		LocationID: req.LocationID,
		Name:       req.Name,
		Endpoint:   req.Endpoint,
		Healthy:    false,
		EnrolledAt: now,
	}, nil
}

// List returns all locations for orgID, ordered by enrolled_at ASC.
func (s *SQLStore) List(ctx context.Context, orgID string) ([]Location, error) {
	rows, err := s.db.QueryContext(ctx,
		s.db.Rebind(`SELECT org_id, location_id, name, endpoint, last_seen_at, healthy, enrolled_at
		 FROM locations WHERE org_id = ? ORDER BY enrolled_at ASC`),
		orgID,
	)
	if err != nil {
		return nil, fmt.Errorf("multiloc: list: %w", err)
	}
	defer rows.Close()
	return scanLocations(rows)
}

// HealthyLocations returns only healthy locations for orgID, ordered by
// enrolled_at ASC.
func (s *SQLStore) HealthyLocations(ctx context.Context, orgID string) ([]Location, error) {
	rows, err := s.db.QueryContext(ctx,
		s.db.Rebind(`SELECT org_id, location_id, name, endpoint, last_seen_at, healthy, enrolled_at
		 FROM locations WHERE org_id = ? AND healthy = 1 ORDER BY enrolled_at ASC`),
		orgID,
	)
	if err != nil {
		return nil, fmt.Errorf("multiloc: healthy_locations: %w", err)
	}
	defer rows.Close()
	return scanLocations(rows)
}

// MarkHealth updates the healthy flag and last_seen_at for a location.
func (s *SQLStore) MarkHealth(ctx context.Context, orgID, locationID string, healthy bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	healthVal := 0
	if healthy {
		healthVal = 1
	}
	now := time.Now().UTC().Unix()
	res, err := s.db.ExecContext(ctx,
		s.db.Rebind(`UPDATE locations SET healthy = ?, last_seen_at = ? WHERE org_id = ? AND location_id = ?`),
		healthVal, now, orgID, locationID,
	)
	if err != nil {
		return fmt.Errorf("multiloc: mark_health: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrLocationNotFound
	}
	return nil
}

// PickLocation selects a healthy location using round-robin.
func (s *SQLStore) PickLocation(ctx context.Context, orgID string) (Location, error) {
	locs, err := s.HealthyLocations(ctx, orgID)
	if err != nil {
		return Location{}, err
	}
	if len(locs) == 0 {
		return Location{}, ErrAllLocationsUnhealthy
	}
	// Round-robin: pick a random healthy location.
	return locs[rand.Intn(len(locs))], nil
}

// ─────────────────────────────────────────────────────────────────────────────
// MemStore — in-memory reference implementation (used in tests + MemStore fallback)
// ─────────────────────────────────────────────────────────────────────────────

// MemStore is the in-memory reference Store.  Concurrency-safe.
// It DEFINES the semantics every other implementation must match.
type MemStore struct {
	mu        sync.Mutex
	locations map[string]map[string]Location // orgID -> locationID -> Location
}

// NewMemStore returns an empty MemStore.
func NewMemStore() *MemStore {
	return &MemStore{
		locations: map[string]map[string]Location{},
	}
}

// compile-time assertion: MemStore satisfies Store.
var _ Store = (*MemStore)(nil)

func (m *MemStore) Enroll(_ context.Context, req EnrollRequest) (Location, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.locations[req.OrgID] == nil {
		m.locations[req.OrgID] = map[string]Location{}
	}
	if _, exists := m.locations[req.OrgID][req.LocationID]; exists {
		return Location{}, ErrAlreadyEnrolled
	}
	now := time.Now().UTC()
	loc := Location{
		OrgID:      req.OrgID,
		LocationID: req.LocationID,
		Name:       req.Name,
		Endpoint:   req.Endpoint,
		Healthy:    false,
		EnrolledAt: now,
	}
	m.locations[req.OrgID][req.LocationID] = loc
	return loc, nil
}

func (m *MemStore) List(_ context.Context, orgID string) ([]Location, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var out []Location
	for _, loc := range m.locations[orgID] {
		out = append(out, loc)
	}
	sortLocations(out)
	return out, nil
}

func (m *MemStore) HealthyLocations(_ context.Context, orgID string) ([]Location, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var out []Location
	for _, loc := range m.locations[orgID] {
		if loc.Healthy {
			out = append(out, loc)
		}
	}
	sortLocations(out)
	return out, nil
}

func (m *MemStore) MarkHealth(_ context.Context, orgID, locationID string, healthy bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	locs, ok := m.locations[orgID]
	if !ok {
		return ErrLocationNotFound
	}
	loc, ok := locs[locationID]
	if !ok {
		return ErrLocationNotFound
	}
	loc.Healthy = healthy
	loc.LastSeenAt = time.Now().UTC()
	locs[locationID] = loc
	return nil
}

func (m *MemStore) PickLocation(ctx context.Context, orgID string) (Location, error) {
	locs, err := m.HealthyLocations(ctx, orgID)
	if err != nil {
		return Location{}, err
	}
	if len(locs) == 0 {
		return Location{}, ErrAllLocationsUnhealthy
	}
	return locs[rand.Intn(len(locs))], nil
}

func (m *MemStore) Close() error { return nil }

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

func scanLocations(rows *sql.Rows) ([]Location, error) {
	var out []Location
	for rows.Next() {
		var loc Location
		var lastSeenUnix, enrolledUnix int64
		var healthyInt int
		if err := rows.Scan(
			&loc.OrgID,
			&loc.LocationID,
			&loc.Name,
			&loc.Endpoint,
			&lastSeenUnix,
			&healthyInt,
			&enrolledUnix,
		); err != nil {
			return nil, fmt.Errorf("multiloc: scan: %w", err)
		}
		if lastSeenUnix > 0 {
			loc.LastSeenAt = time.Unix(lastSeenUnix, 0).UTC()
		}
		loc.EnrolledAt = time.Unix(enrolledUnix, 0).UTC()
		loc.Healthy = healthyInt != 0
		out = append(out, loc)
	}
	return out, rows.Err()
}

func sortLocations(locs []Location) {
	// Sort by enrolled_at ASC, then location_id for stability.
	for i := 1; i < len(locs); i++ {
		for j := i; j > 0; j-- {
			a, b := locs[j-1], locs[j]
			if a.EnrolledAt.After(b.EnrolledAt) ||
				(a.EnrolledAt.Equal(b.EnrolledAt) && a.LocationID > b.LocationID) {
				locs[j-1], locs[j] = locs[j], locs[j-1]
			} else {
				break
			}
		}
	}
}

// isUniqueViolation reports whether the error is a UNIQUE constraint
// violation (SQLite "UNIQUE constraint failed" or Postgres code 23505 /
// "duplicate key value violates unique constraint").
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return contains(msg, "UNIQUE constraint failed") ||
		contains(msg, "constraint failed") ||
		contains(msg, "duplicate key value violates unique constraint")
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && indexStr(s, sub) >= 0)
}

func indexStr(s, sub string) int {
	if len(sub) == 0 {
		return 0
	}
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
