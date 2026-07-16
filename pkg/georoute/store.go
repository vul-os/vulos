package georoute

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"sort"
	"sync"
	"time"

	"github.com/vul-os/vulos-management/pkg/cpdb"
)

// ────────────────────────────────────────────────────────────────────────────
// SQLStore — cpdb-backed backend (SQLite or Postgres).
// Mirrors internal/multiloc + internal/servingpool style.
// Embedded migration applied idempotently on Open.
// ────────────────────────────────────────────────────────────────────────────

// SQLStore is the production Store. Concurrency-safe.
type SQLStore struct {
	db *cpdb.DB
	mu sync.Mutex
}

// Open applies migrations to db and returns a ready-to-use *SQLStore.
//
// db should be obtained from cpdb.Open("georoute") for production, or from
// cpdb.OpenSQLiteDSN(":memory:") for tests. Migrations are applied
// automatically and are idempotent (safe to call on every startup).
func Open(db *cpdb.DB) (*SQLStore, error) {
	subFS, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("georoute: embed sub: %w", err)
	}
	if err := db.Migrate(subFS); err != nil {
		return nil, fmt.Errorf("georoute: migrate: %w", err)
	}
	return &SQLStore{db: db}, nil
}

// Close releases the underlying database connection.
func (s *SQLStore) Close() error { return s.db.Close() }

// Assign sets (tenant → region) on FIRST set only (REGION-SSOT-01 immutability).
// Re-setting the SAME region is an idempotent no-op; changing it returns
// ErrRegionLocked. Use ForceAssign for the gated failover/migration path.
func (s *SQLStore) Assign(ctx context.Context, tenantID, region string) (Assignment, error) {
	if tenantID == "" || region == "" {
		return Assignment{}, ErrInvalidInput
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	// Immutability check under the write lock.
	var existing string
	rerr := s.db.QueryRowContext(ctx,
		s.db.Rebind(`SELECT home_region FROM tenants_regions WHERE tenant_id = ?`), tenantID,
	).Scan(&existing)
	if rerr == nil {
		if existing != region {
			return Assignment{}, ErrRegionLocked
		}
		// Idempotent: return the existing row unchanged.
		return s.homeRegionRow(ctx, tenantID, region)
	}
	if rerr != sql.ErrNoRows {
		return Assignment{}, fmt.Errorf("georoute: assign read: %w", rerr)
	}

	now := time.Now().UTC()
	_, err := s.db.ExecContext(ctx,
		s.db.Rebind(`INSERT INTO tenants_regions (tenant_id, home_region, assigned_at)
		 VALUES (?, ?, ?)`),
		tenantID, region, now.Unix(),
	)
	if err != nil {
		return Assignment{}, fmt.Errorf("georoute: assign: %w", err)
	}
	return Assignment{TenantID: tenantID, HomeRegion: region, AssignedAt: now}, nil
}

// ForceAssign overwrites the home region unconditionally (gated failover path).
func (s *SQLStore) ForceAssign(ctx context.Context, tenantID, region string) (Assignment, error) {
	if tenantID == "" || region == "" {
		return Assignment{}, ErrInvalidInput
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	_, err := s.db.ExecContext(ctx,
		s.db.Rebind(`INSERT INTO tenants_regions (tenant_id, home_region, assigned_at)
		 VALUES (?, ?, ?)
		 ON CONFLICT(tenant_id) DO UPDATE SET
		   home_region = excluded.home_region,
		   assigned_at = excluded.assigned_at`),
		tenantID, region, now.Unix(),
	)
	if err != nil {
		return Assignment{}, fmt.Errorf("georoute: force assign: %w", err)
	}
	return Assignment{TenantID: tenantID, HomeRegion: region, AssignedAt: now}, nil
}

// homeRegionRow loads the assigned_at for an existing (tenant, region) row.
func (s *SQLStore) homeRegionRow(ctx context.Context, tenantID, region string) (Assignment, error) {
	var unix int64
	err := s.db.QueryRowContext(ctx,
		s.db.Rebind(`SELECT assigned_at FROM tenants_regions WHERE tenant_id = ?`), tenantID,
	).Scan(&unix)
	if err != nil {
		return Assignment{TenantID: tenantID, HomeRegion: region, AssignedAt: time.Now().UTC()}, nil
	}
	return Assignment{TenantID: tenantID, HomeRegion: region, AssignedAt: time.Unix(unix, 0).UTC()}, nil
}

// HomeRegion returns the home region for tenantID.
func (s *SQLStore) HomeRegion(ctx context.Context, tenantID string) (string, error) {
	if tenantID == "" {
		return "", ErrInvalidInput
	}
	var region string
	err := s.db.QueryRowContext(ctx,
		s.db.Rebind(`SELECT home_region FROM tenants_regions WHERE tenant_id = ?`),
		tenantID,
	).Scan(&region)
	if err == sql.ErrNoRows {
		return "", ErrUnknownTenant
	}
	if err != nil {
		return "", fmt.Errorf("georoute: home_region: %w", err)
	}
	return region, nil
}

// List returns every (tenant → region) row, ordered by assigned_at ASC.
func (s *SQLStore) List(ctx context.Context) ([]Assignment, error) {
	rows, err := s.db.QueryContext(ctx,
		s.db.Rebind(`SELECT tenant_id, home_region, assigned_at
		 FROM tenants_regions ORDER BY assigned_at ASC, tenant_id ASC`),
	)
	if err != nil {
		return nil, fmt.Errorf("georoute: list: %w", err)
	}
	defer rows.Close()
	var out []Assignment
	for rows.Next() {
		var a Assignment
		var unix int64
		if err := rows.Scan(&a.TenantID, &a.HomeRegion, &unix); err != nil {
			return nil, fmt.Errorf("georoute: scan: %w", err)
		}
		a.AssignedAt = time.Unix(unix, 0).UTC()
		out = append(out, a)
	}
	return out, rows.Err()
}

// ────────────────────────────────────────────────────────────────────────────
// MemStore — in-memory reference impl. Defines the semantics every Store
// implementation must match (matches internal/multiloc + servingpool style).
// ────────────────────────────────────────────────────────────────────────────

// MemStore is the in-memory reference Store.
type MemStore struct {
	mu          sync.Mutex
	assignments map[string]Assignment
}

// NewMemStore returns an empty MemStore.
func NewMemStore() *MemStore {
	return &MemStore{assignments: map[string]Assignment{}}
}

// compile-time assertions.
var (
	_ Store = (*MemStore)(nil)
	_ Store = (*SQLStore)(nil)
)

// Assign satisfies Store on MemStore — immutable after first set.
func (m *MemStore) Assign(_ context.Context, tenantID, region string) (Assignment, error) {
	if tenantID == "" || region == "" {
		return Assignment{}, ErrInvalidInput
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if existing, ok := m.assignments[tenantID]; ok {
		if existing.HomeRegion != region {
			return Assignment{}, ErrRegionLocked
		}
		return existing, nil // idempotent
	}
	a := Assignment{TenantID: tenantID, HomeRegion: region, AssignedAt: time.Now().UTC()}
	m.assignments[tenantID] = a
	return a, nil
}

// ForceAssign satisfies Store on MemStore — unconditional overwrite (failover).
func (m *MemStore) ForceAssign(_ context.Context, tenantID, region string) (Assignment, error) {
	if tenantID == "" || region == "" {
		return Assignment{}, ErrInvalidInput
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	a := Assignment{TenantID: tenantID, HomeRegion: region, AssignedAt: time.Now().UTC()}
	m.assignments[tenantID] = a
	return a, nil
}

// HomeRegion satisfies Store on MemStore.
func (m *MemStore) HomeRegion(_ context.Context, tenantID string) (string, error) {
	if tenantID == "" {
		return "", ErrInvalidInput
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.assignments[tenantID]
	if !ok {
		return "", ErrUnknownTenant
	}
	return a.HomeRegion, nil
}

// List satisfies Store on MemStore.
func (m *MemStore) List(_ context.Context) ([]Assignment, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Assignment, 0, len(m.assignments))
	for _, a := range m.assignments {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].AssignedAt.Equal(out[j].AssignedAt) {
			return out[i].TenantID < out[j].TenantID
		}
		return out[i].AssignedAt.Before(out[j].AssignedAt)
	})
	return out, nil
}

// Close satisfies Store on MemStore.
func (m *MemStore) Close() error { return nil }
