// Package cgroups implements cgroup v2 resource governance for Vulos published
// webapps.  It ensures that OS system services (sync, mail) can never be starved
// by tenant apps by maintaining a protected system slice, and enforces per-app
// CPU + memory limits.
//
// Linux-specific cgroup file writes are in governor_linux.go.  On non-Linux
// platforms all cgroup operations are no-ops so that `go build ./...` succeeds
// on darwin/windows developer machines.
package cgroups

import (
	"database/sql"
	"embed"
	"fmt"
	"time"

	dbmigrate "vulos/backend/internal/migrate"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Visibility controls which limit profile a slice receives.
type Visibility string

const (
	VisibilityPrivate Visibility = "private"
	VisibilityPublic  Visibility = "public"
)

// SliceType distinguishes the system reservation from per-app slices.
type SliceType string

const (
	SliceTypeSystem SliceType = "system"
	SliceTypeApp    SliceType = "app"
)

// Limits describes the resource limits for a cgroup slice.
type Limits struct {
	// CPUWeight is the relative CPU weight (kernel default 100).
	// System slice gets 300 (≈30%), private app 100 (≈10%), public app 150 (≈15%).
	CPUWeight int64
	// MemMin is the memory.min guarantee in bytes (only used for the system slice).
	MemMin int64
	// MemHigh is the memory.high soft limit in bytes.
	MemHigh int64
	// MemMax is the memory.max hard OOM limit in bytes.
	MemMax int64
}

// Well-known default limit profiles per the PUBWEB-04 spec.
var (
	SystemLimits = Limits{
		CPUWeight: 300, // 30% weight
		MemMin:    512 * MiB,
		MemHigh:   0, // no soft limit on system slice
		MemMax:    0, // no hard limit on system slice
	}
	PrivateAppLimits = Limits{
		CPUWeight: 100, // 10% weight
		MemHigh:   256 * MiB,
		MemMax:    512 * MiB,
	}
	PublicAppLimits = Limits{
		CPUWeight: 150, // 15% weight
		MemHigh:   128 * MiB,
		MemMax:    512 * MiB,
	}
)

// Memory size constants.
const (
	MiB int64 = 1 << 20
	GiB int64 = 1 << 30
)

// SliceSpec is the input to Governor.EnsureSlice.
type SliceSpec struct {
	// AppID is empty for the system slice.
	AppID      string
	SliceType  SliceType
	Visibility Visibility
	Limits     Limits
}

// SliceStatus is the runtime status of a single slice, returned by Status.
type SliceStatus struct {
	ID         string
	AppID      string
	SliceType  SliceType
	Visibility Visibility
	CgroupPath string
	Limits     Limits
	// Live usage (read from cgroup files on Linux; zero on non-Linux).
	MemCurrent int64
	CPUUsageUs int64 // cumulative cpu.stat usage_usec
}

// Governor manages the cgroup v2 hierarchy for Vulos.
type Governor struct {
	db     *sql.DB
	cgRoot string // base path, default "/sys/fs/cgroup/vulos"
	ulid   func() string
}

// NewGovernor opens (or creates) the SQLite store at dbPath and initialises the
// Governor.  cgRoot is the cgroup base directory; pass "" to use the default
// "/sys/fs/cgroup/vulos".
func NewGovernor(dbPath, cgRoot string, ulidFn func() string) (*Governor, error) {
	if cgRoot == "" {
		cgRoot = "/sys/fs/cgroup/vulos"
	}
	db, err := openDB(dbPath)
	if err != nil {
		return nil, fmt.Errorf("cgroups: open db: %w", err)
	}
	if ulidFn == nil {
		ulidFn = defaultULID
	}
	return &Governor{db: db, cgRoot: cgRoot, ulid: ulidFn}, nil
}

// Close releases the database connection.
func (g *Governor) Close() error {
	if g.db == nil {
		return nil
	}
	return g.db.Close()
}

// EnsureSystemSlice creates or updates the protected vulos-system.slice cgroup
// with the SystemLimits profile.  It is idempotent; safe to call on every startup.
func (g *Governor) EnsureSystemSlice() error {
	return g.EnsureSlice(SliceSpec{
		AppID:     "",
		SliceType: SliceTypeSystem,
		Limits:    SystemLimits,
	})
}

// EnsureAppSlice creates or updates the per-app slice for appID.
// Use visibility=Public for published apps (tighter limits per spec).
func (g *Governor) EnsureAppSlice(appID string, vis Visibility) error {
	limits := PrivateAppLimits
	if vis == VisibilityPublic {
		limits = PublicAppLimits
	}
	return g.EnsureSlice(SliceSpec{
		AppID:      appID,
		SliceType:  SliceTypeApp,
		Visibility: vis,
		Limits:     limits,
	})
}

// EnsureSlice is the low-level slice upsert used by EnsureSystemSlice and EnsureAppSlice.
// It persists metadata to SQLite and delegates the actual cgroup file writes to the
// platform-specific applySlice function.
func (g *Governor) EnsureSlice(spec SliceSpec) error {
	path := g.slicePath(spec)

	// Upsert into DB.
	id := g.ulid()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := g.db.Exec(`
		INSERT INTO cgroup_slices
			(id, app_id, slice_type, visibility, cpu_weight, mem_min, mem_high, mem_max, cgroup_path, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO NOTHING`,
		id, spec.AppID, string(spec.SliceType), string(spec.Visibility),
		spec.Limits.CPUWeight, spec.Limits.MemMin, spec.Limits.MemHigh, spec.Limits.MemMax,
		path, now, now,
	)
	if err != nil {
		return fmt.Errorf("cgroups: upsert slice: %w", err)
	}

	// Also update by (app_id, slice_type) if a row already exists.
	_, err = g.db.Exec(`
		UPDATE cgroup_slices
		SET cpu_weight=?, mem_min=?, mem_high=?, mem_max=?, visibility=?, cgroup_path=?, updated_at=?
		WHERE app_id=? AND slice_type=?`,
		spec.Limits.CPUWeight, spec.Limits.MemMin, spec.Limits.MemHigh, spec.Limits.MemMax,
		string(spec.Visibility), path, now,
		spec.AppID, string(spec.SliceType),
	)
	if err != nil {
		return fmt.Errorf("cgroups: update slice: %w", err)
	}

	// Write cgroup files (Linux only; no-op on darwin/windows).
	return applySlice(path, spec.Limits)
}

// Status returns the current status of all tracked slices.
// On non-Linux platforms the live usage fields will be zero.
func (g *Governor) Status() ([]SliceStatus, error) {
	rows, err := g.db.Query(`
		SELECT id, app_id, slice_type, visibility, cpu_weight, mem_min, mem_high, mem_max, cgroup_path
		FROM cgroup_slices ORDER BY slice_type, app_id`)
	if err != nil {
		return nil, fmt.Errorf("cgroups: query slices: %w", err)
	}
	defer rows.Close()

	var out []SliceStatus
	for rows.Next() {
		var s SliceStatus
		if err := rows.Scan(
			&s.ID, &s.AppID, &s.SliceType, &s.Visibility,
			&s.Limits.CPUWeight, &s.Limits.MemMin, &s.Limits.MemHigh, &s.Limits.MemMax,
			&s.CgroupPath,
		); err != nil {
			return nil, err
		}
		// Read live stats (Linux) or return zeros (other platforms).
		s.MemCurrent, s.CPUUsageUs = readLiveStats(s.CgroupPath)
		out = append(out, s)
	}
	return out, rows.Err()
}

// RecordEvent persists a cgroup event (OOM, throttle, restore) to SQLite.
func (g *Governor) RecordEvent(sliceID, eventType, detail string) error {
	id := g.ulid()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := g.db.Exec(`
		INSERT INTO cgroup_events(id, slice_id, event_type, detail, occurred_at)
		VALUES (?, ?, ?, ?, ?)`,
		id, sliceID, eventType, detail, now,
	)
	return err
}

// slicePath constructs the cgroup directory path for a slice spec.
func (g *Governor) slicePath(spec SliceSpec) string {
	switch spec.SliceType {
	case SliceTypeSystem:
		return g.cgRoot + "/vulos-system.slice"
	default:
		if spec.AppID != "" {
			return g.cgRoot + "/vulos-apps-" + spec.AppID + ".slice"
		}
		return g.cgRoot + "/vulos-apps-unknown.slice"
	}
}

// --- DB helpers ---

func openDB(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	if err := runMigrations(db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func runMigrations(db *sql.DB) error {
	return dbmigrate.Apply(db, migrationsFS, "migrations")
}

// defaultULID is a simple timestamp-based ID used when no custom generator is
// provided.  Not a full ULID — just enough entropy for tests and startup.
func defaultULID() string {
	return fmt.Sprintf("%d", time.Now().UnixNano())
}
