// store.go — cpdb-backed Store for the residency package.
//
// Storage: cpdb dual-backend seam (SQLite for self-host, Postgres for cloud).
// Embedded migration via go:embed runs idempotently on Open.
package residency

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"sync"
	"time"

	"github.com/vul-os/vulos-management/pkg/cpdb"
)

// SQLStore is the production cpdb-backed Store.
type SQLStore struct {
	db *cpdb.DB
	mu sync.Mutex // serialises writes
}

// OpenSQLStore opens the residency store using the provided cpdb.DB,
// runs the embedded migration idempotently, and returns a ready *SQLStore.
func OpenSQLStore(db *cpdb.DB) (*SQLStore, error) {
	subFS, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("residency: embed sub: %w", err)
	}
	if err := db.Migrate(subFS); err != nil {
		return nil, fmt.Errorf("residency: migrate: %w", err)
	}
	return &SQLStore{db: db}, nil
}

// Close closes the underlying database connection.
func (s *SQLStore) Close() error { return s.db.Close() }

// compile-time assertion.
var _ Store = (*SQLStore)(nil)

// GetRegion returns the OrgRegion for orgID.
// Returns ErrUnknownOrg if no row exists.
func (s *SQLStore) GetRegion(ctx context.Context, orgID string) (OrgRegion, error) {
	var r OrgRegion
	var updatedStr string
	err := s.db.QueryRowContext(ctx,
		s.db.Rebind(`SELECT org_id, region, updated_at FROM org_residency WHERE org_id = ?`), orgID,
	).Scan(&r.OrgID, &r.Region, &updatedStr)
	if errors.Is(err, sql.ErrNoRows) {
		return OrgRegion{}, ErrUnknownOrg
	}
	if err != nil {
		return OrgRegion{}, fmt.Errorf("residency: get region: %w", err)
	}
	r.UpdatedAt, _ = time.Parse(time.RFC3339, updatedStr)
	return r, nil
}

// AllRegions returns every org's residency region (region reconciler).
func (s *SQLStore) AllRegions(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT org_id, region FROM org_residency`)
	if err != nil {
		return nil, fmt.Errorf("residency: all regions: %w", err)
	}
	defer rows.Close()
	out := make(map[string]string)
	for rows.Next() {
		var org, region string
		if err := rows.Scan(&org, &region); err != nil {
			return nil, fmt.Errorf("residency: all regions scan: %w", err)
		}
		out[org] = region
	}
	return out, rows.Err()
}

// SetRegion sets the region for an org. The region is IMMUTABLE after the first
// set (item 11): a first set succeeds, re-setting the SAME region is an
// idempotent no-op, and any attempt to CHANGE it returns ErrRegionLocked.
// Returns ErrInvalidRegion if region is not in SupportedRegions.
func (s *SQLStore) SetRegion(ctx context.Context, orgID, region string) error {
	if !IsValidRegion(region) {
		return ErrInvalidRegion
	}
	now := time.Now().UTC().Format(time.RFC3339)
	s.mu.Lock()
	defer s.mu.Unlock()

	// IMMUTABILITY (item 11): read existing under the write lock; reject a change.
	var existing string
	rerr := s.db.QueryRowContext(ctx,
		s.db.Rebind(`SELECT region FROM org_residency WHERE org_id = ?`), orgID,
	).Scan(&existing)
	if rerr == nil {
		if existing != region {
			return ErrRegionLocked
		}
		return nil // same region — idempotent no-op
	}
	if !errors.Is(rerr, sql.ErrNoRows) {
		return fmt.Errorf("residency: set region read: %w", rerr)
	}

	_, err := s.db.ExecContext(ctx,
		s.db.Rebind(`INSERT INTO org_residency (org_id, region, updated_at)
		   VALUES (?, ?, ?)
		 ON CONFLICT(org_id) DO UPDATE SET
		   region     = excluded.region,
		   updated_at = excluded.updated_at`),
		orgID, region, now,
	)
	if err != nil {
		return fmt.Errorf("residency: set region: %w", err)
	}
	return nil
}
