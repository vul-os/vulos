package customdomain

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"sync"
	"time"

	"github.com/vul-os/vulos-management/pkg/cpdb"
)

// Store is the persistence contract for the custom-domain plane. SQLStore is
// the production implementation; MemStore is the reference for tests.
type Store interface {
	Upsert(ctx context.Context, d CustomDomain) error
	Get(ctx context.Context, domain string) (CustomDomain, error)
	ListByTenant(ctx context.Context, tenantID string) ([]CustomDomain, error)
	Delete(ctx context.Context, domain string) error
	Close() error
}

// ──────────────────────────────────────────────────────────────────────────
// SQLStore
// ──────────────────────────────────────────────────────────────────────────

// SQLStore is the cpdb-backed Store.
type SQLStore struct {
	db *cpdb.DB
	mu sync.Mutex
}

// OpenSQLStore opens the custom-domain store using the provided cpdb.DB and
// runs the embedded migration idempotently. The intended_cname column is
// included in 0004_custom_domains.sql so no separate ALTER TABLE is required.
func OpenSQLStore(db *cpdb.DB) (*SQLStore, error) {
	subFS, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("customdomain: embed sub: %w", err)
	}
	if err := db.Migrate(subFS); err != nil {
		return nil, fmt.Errorf("customdomain: migrate: %w", err)
	}
	return &SQLStore{db: db}, nil
}

// Close closes the database connection.
func (s *SQLStore) Close() error { return s.db.Close() }

// Upsert inserts or replaces a row matched by (domain).
func (s *SQLStore) Upsert(ctx context.Context, d CustomDomain) error {
	if d.Domain == "" {
		return ErrInvalidDomain
	}
	d.UpdatedAt = nowUTC()
	if d.CreatedAt.IsZero() {
		d.CreatedAt = d.UpdatedAt
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx, s.db.Rebind(`
		INSERT INTO custom_domains
		  (domain, tenant_id, bundle_node, verify_token, verify_state,
		   acme_status, intended_cname, created_at, updated_at, attached_at, verified_at, last_error)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(domain) DO UPDATE SET
		  tenant_id      = excluded.tenant_id,
		  bundle_node    = excluded.bundle_node,
		  verify_token   = excluded.verify_token,
		  verify_state   = excluded.verify_state,
		  acme_status    = excluded.acme_status,
		  intended_cname = excluded.intended_cname,
		  updated_at     = excluded.updated_at,
		  attached_at    = excluded.attached_at,
		  verified_at    = excluded.verified_at,
		  last_error     = excluded.last_error`),
		strings.ToLower(d.Domain), d.TenantID, d.BundleNode, d.VerifyToken, d.VerifyState,
		d.ACMEStatus, d.IntendedCNAME, fmtT(d.CreatedAt), fmtT(d.UpdatedAt),
		fmtNullableT(d.AttachedAt), fmtNullableT(d.VerifiedAt), d.LastError,
	)
	if err != nil {
		return fmt.Errorf("customdomain: upsert: %w", err)
	}
	return nil
}

// Get returns the row for domain or ErrNotFound.
func (s *SQLStore) Get(ctx context.Context, domain string) (CustomDomain, error) {
	row := s.db.QueryRowContext(ctx, s.db.Rebind(`
		SELECT domain, tenant_id, bundle_node, verify_token, verify_state,
		       acme_status, intended_cname, created_at, updated_at, attached_at, verified_at, last_error
		FROM custom_domains WHERE domain = ?`),
		strings.ToLower(domain))
	return scanDomain(row)
}

// ListByTenant returns every row owned by tenantID.
func (s *SQLStore) ListByTenant(ctx context.Context, tenantID string) ([]CustomDomain, error) {
	rows, err := s.db.QueryContext(ctx, s.db.Rebind(`
		SELECT domain, tenant_id, bundle_node, verify_token, verify_state,
		       acme_status, intended_cname, created_at, updated_at, attached_at, verified_at, last_error
		FROM custom_domains WHERE tenant_id = ? ORDER BY created_at ASC`), tenantID)
	if err != nil {
		return nil, fmt.Errorf("customdomain: list: %w", err)
	}
	defer rows.Close()
	var out []CustomDomain
	for rows.Next() {
		d, err := scanDomain(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// Delete removes the row for domain. Returns nil if the row was already gone.
func (s *SQLStore) Delete(ctx context.Context, domain string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx, s.db.Rebind(`DELETE FROM custom_domains WHERE domain = ?`), strings.ToLower(domain))
	if err != nil {
		return fmt.Errorf("customdomain: delete: %w", err)
	}
	return nil
}

// rowScanner abstracts *sql.Row and *sql.Rows so scanDomain works for both.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanDomain(row rowScanner) (CustomDomain, error) {
	var d CustomDomain
	var createdAt, updatedAt string
	var attachedAt, verifiedAt sql.NullString
	err := row.Scan(
		&d.Domain, &d.TenantID, &d.BundleNode, &d.VerifyToken, &d.VerifyState,
		&d.ACMEStatus, &d.IntendedCNAME, &createdAt, &updatedAt, &attachedAt, &verifiedAt, &d.LastError,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return CustomDomain{}, ErrNotFound
	}
	if err != nil {
		return CustomDomain{}, fmt.Errorf("customdomain: scan: %w", err)
	}
	d.CreatedAt = parseT(createdAt)
	d.UpdatedAt = parseT(updatedAt)
	if attachedAt.Valid {
		t := parseT(attachedAt.String)
		d.AttachedAt = &t
	}
	if verifiedAt.Valid {
		t := parseT(verifiedAt.String)
		d.VerifiedAt = &t
	}
	return d, nil
}

func nowUTC() time.Time { return time.Now().UTC() }
func fmtT(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}
func fmtNullableT(t *time.Time) any {
	if t == nil || t.IsZero() {
		return nil
	}
	return fmtT(*t)
}
func parseT(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		// Fall back to plain RFC3339 for migration robustness.
		t, _ = time.Parse(time.RFC3339, s)
	}
	return t.UTC()
}

// ──────────────────────────────────────────────────────────────────────────
// MemStore — reference implementation for tests and dev mode.
// ──────────────────────────────────────────────────────────────────────────

// MemStore is the in-memory reference Store. Concurrency-safe.
type MemStore struct {
	mu      sync.Mutex
	domains map[string]CustomDomain
}

// NewMemStore constructs an empty MemStore.
func NewMemStore() *MemStore {
	return &MemStore{domains: map[string]CustomDomain{}}
}

// Upsert implements Store.
func (m *MemStore) Upsert(_ context.Context, d CustomDomain) error {
	if d.Domain == "" {
		return ErrInvalidDomain
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	d.UpdatedAt = nowUTC()
	if d.CreatedAt.IsZero() {
		d.CreatedAt = d.UpdatedAt
	}
	m.domains[strings.ToLower(d.Domain)] = d
	return nil
}

// Get implements Store.
func (m *MemStore) Get(_ context.Context, domain string) (CustomDomain, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.domains[strings.ToLower(domain)]
	if !ok {
		return CustomDomain{}, ErrNotFound
	}
	return d, nil
}

// ListByTenant implements Store.
func (m *MemStore) ListByTenant(_ context.Context, tenantID string) ([]CustomDomain, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []CustomDomain
	for _, d := range m.domains {
		if d.TenantID == tenantID {
			out = append(out, d)
		}
	}
	return out, nil
}

// Delete implements Store.
func (m *MemStore) Delete(_ context.Context, domain string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.domains, strings.ToLower(domain))
	return nil
}

// Close implements Store.
func (m *MemStore) Close() error { return nil }

// compile-time assertions
var _ Store = (*SQLStore)(nil)
var _ Store = (*MemStore)(nil)
