package webapp

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"time"

	"github.com/vul-os/vulos-management/pkg/cpdb"
)

// SQLStore is the production dual-backend implementation of Store.
// Backed by *cpdb.DB (SQLite or Postgres depending on environment).
// Behaviour is semantically identical to MemStore.
type SQLStore struct {
	db *cpdb.DB
}

// OpenSQLStore runs embedded migrations against db and returns a ready *SQLStore.
func OpenSQLStore(db *cpdb.DB) (*SQLStore, error) {
	subFS, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("webapp: embed sub: %w", err)
	}
	if err := db.Migrate(subFS); err != nil {
		return nil, fmt.Errorf("webapp: migrate: %w", err)
	}
	return &SQLStore{db: db}, nil
}

// Close closes the underlying database connection.
func (s *SQLStore) Close() error { return s.db.Close() }

// compile-time assertion: SQLStore satisfies Store.
var _ Store = (*SQLStore)(nil)

// ─────────────────────────────────────────────────────────────────────────────
// ConnectDomain
// ─────────────────────────────────────────────────────────────────────────────

func (s *SQLStore) ConnectDomain(ctx context.Context, req ConnectDomainRequest) (Domain, error) {
	if req.AccountID == "" || req.ULID == "" || req.Domain == "" {
		return Domain{}, ErrInvalidInput
	}
	if req.AppKind == "" {
		req.AppKind = AppKindNative
	}
	if req.AppKind != AppKindNative && req.AppKind != AppKindOCI {
		return Domain{}, ErrInvalidInput
	}
	if req.CacheTTLSec <= 0 {
		req.CacheTTLSec = DefaultCacheTTLSec
	}
	if req.RateLimitRPS <= 0 {
		req.RateLimitRPS = DefaultRateLimitRPS
	}
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.db.ExecContext(ctx, s.db.Rebind(`
		INSERT INTO webapp_domains
		  (account_id, ulid, domain, app_kind, cache_ttl_sec, rate_limit_rps, created_at, updated_at)
		  VALUES (?, ?, ?, ?, ?, ?, ?, ?)`),
		req.AccountID, req.ULID, req.Domain, string(req.AppKind),
		req.CacheTTLSec, req.RateLimitRPS, now, now,
	)
	if err != nil {
		if isUniqueErr(err) {
			return Domain{}, ErrDomainAlreadyExists
		}
		return Domain{}, fmt.Errorf("webapp: connect domain: %w", err)
	}
	return s.GetDomain(ctx, req.ULID, req.Domain)
}

// ─────────────────────────────────────────────────────────────────────────────
// GetDomain
// ─────────────────────────────────────────────────────────────────────────────

func (s *SQLStore) GetDomain(ctx context.Context, ulid, domain string) (Domain, error) {
	row := s.db.QueryRowContext(ctx, s.db.Rebind(`
		SELECT id, account_id, ulid, domain,
		       ns_delegated, ns_verified_at,
		       tls_provisioned, tls_issued_at,
		       published, app_kind, cache_ttl_sec, rate_limit_rps,
		       created_at, updated_at
		  FROM webapp_domains
		 WHERE ulid = ? AND domain = ?`), ulid, domain)
	return scanDomain(row)
}

// ─────────────────────────────────────────────────────────────────────────────
// ListDomains / ListDomainsByAccount
// ─────────────────────────────────────────────────────────────────────────────

func (s *SQLStore) ListDomains(ctx context.Context, ulid string) ([]Domain, error) {
	rows, err := s.db.QueryContext(ctx, s.db.Rebind(`
		SELECT id, account_id, ulid, domain,
		       ns_delegated, ns_verified_at,
		       tls_provisioned, tls_issued_at,
		       published, app_kind, cache_ttl_sec, rate_limit_rps,
		       created_at, updated_at
		  FROM webapp_domains
		 WHERE ulid = ?
		 ORDER BY domain ASC`), ulid)
	if err != nil {
		return nil, fmt.Errorf("webapp: list domains: %w", err)
	}
	return collectDomains(rows)
}

func (s *SQLStore) ListDomainsByAccount(ctx context.Context, accountID string) ([]Domain, error) {
	rows, err := s.db.QueryContext(ctx, s.db.Rebind(`
		SELECT id, account_id, ulid, domain,
		       ns_delegated, ns_verified_at,
		       tls_provisioned, tls_issued_at,
		       published, app_kind, cache_ttl_sec, rate_limit_rps,
		       created_at, updated_at
		  FROM webapp_domains
		 WHERE account_id = ?
		 ORDER BY domain ASC`), accountID)
	if err != nil {
		return nil, fmt.Errorf("webapp: list domains by account: %w", err)
	}
	return collectDomains(rows)
}

// ─────────────────────────────────────────────────────────────────────────────
// DeleteDomain
// ─────────────────────────────────────────────────────────────────────────────

func (s *SQLStore) DeleteDomain(ctx context.Context, ulid, domain string) error {
	res, err := s.db.ExecContext(ctx,
		s.db.Rebind(`DELETE FROM webapp_domains WHERE ulid = ? AND domain = ?`), ulid, domain)
	if err != nil {
		return fmt.Errorf("webapp: delete domain: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrDomainNotFound
	}
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// SetNSDelegated
// ─────────────────────────────────────────────────────────────────────────────

func (s *SQLStore) SetNSDelegated(ctx context.Context, ulid, domain string, delegated bool) error {
	now := time.Now().UTC().Format(time.RFC3339)
	nsDel := 0
	if delegated {
		nsDel = 1
	}
	var nsVerifiedAt any
	if delegated {
		nsVerifiedAt = now
	}
	res, err := s.db.ExecContext(ctx, s.db.Rebind(`
		UPDATE webapp_domains
		   SET ns_delegated = ?, ns_verified_at = ?, updated_at = ?
		 WHERE ulid = ? AND domain = ?`),
		nsDel, nsVerifiedAt, now, ulid, domain)
	if err != nil {
		return fmt.Errorf("webapp: set ns delegated: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrDomainNotFound
	}
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// SetTLSProvisioned
// ─────────────────────────────────────────────────────────────────────────────

func (s *SQLStore) SetTLSProvisioned(ctx context.Context, ulid, domain string, provisioned bool) error {
	now := time.Now().UTC().Format(time.RFC3339)
	tlsProv := 0
	if provisioned {
		tlsProv = 1
	}
	var tlsIssuedAt any
	if provisioned {
		tlsIssuedAt = now
	}
	res, err := s.db.ExecContext(ctx, s.db.Rebind(`
		UPDATE webapp_domains
		   SET tls_provisioned = ?, tls_issued_at = ?, updated_at = ?
		 WHERE ulid = ? AND domain = ?`),
		tlsProv, tlsIssuedAt, now, ulid, domain)
	if err != nil {
		return fmt.Errorf("webapp: set tls provisioned: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrDomainNotFound
	}
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// SetPublished
// ─────────────────────────────────────────────────────────────────────────────

func (s *SQLStore) SetPublished(ctx context.Context, ulid, domain string, published bool) error {
	now := time.Now().UTC().Format(time.RFC3339)
	pub := 0
	if published {
		pub = 1
	}
	res, err := s.db.ExecContext(ctx, s.db.Rebind(`
		UPDATE webapp_domains
		   SET published = ?, updated_at = ?
		 WHERE ulid = ? AND domain = ?`),
		pub, now, ulid, domain)
	if err != nil {
		return fmt.Errorf("webapp: set published: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrDomainNotFound
	}
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

type sqlScanner interface {
	Scan(dest ...any) error
}

func scanDomain(s sqlScanner) (Domain, error) {
	var d Domain
	var nsDel, tlsProv, pub int
	var nsVerStr, tlsIssuedStr sql.NullString
	var createdStr, updatedStr string
	var appKind string
	err := s.Scan(
		&d.ID, &d.AccountID, &d.ULID, &d.Domain,
		&nsDel, &nsVerStr,
		&tlsProv, &tlsIssuedStr,
		&pub, &appKind, &d.CacheTTLSec, &d.RateLimitRPS,
		&createdStr, &updatedStr,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Domain{}, ErrDomainNotFound
	}
	if err != nil {
		return Domain{}, fmt.Errorf("webapp: scan domain: %w", err)
	}
	d.NSDelegated = nsDel == 1
	if nsVerStr.Valid && nsVerStr.String != "" {
		t, _ := time.Parse(time.RFC3339, nsVerStr.String)
		d.NSVerifiedAt = &t
	}
	d.TLSProvisioned = tlsProv == 1
	if tlsIssuedStr.Valid && tlsIssuedStr.String != "" {
		t, _ := time.Parse(time.RFC3339, tlsIssuedStr.String)
		d.TLSIssuedAt = &t
	}
	d.Published = pub == 1
	d.AppKind = AppKind(appKind)
	d.CreatedAt, _ = time.Parse(time.RFC3339, createdStr)
	d.UpdatedAt, _ = time.Parse(time.RFC3339, updatedStr)
	return d, nil
}

func collectDomains(rows *sql.Rows) ([]Domain, error) {
	defer rows.Close()
	var out []Domain
	for rows.Next() {
		d, err := scanDomain(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// isUniqueErr returns true if err is a unique constraint violation (SQLite or Postgres).
func isUniqueErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "UNIQUE constraint failed") ||
		strings.Contains(msg, "duplicate key value") ||
		strings.Contains(msg, "unique_violation")
}
