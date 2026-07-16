package cdn

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

// SQLStore is the production cpdb-backed Store for BYO-CDN config.
type SQLStore struct {
	db *cpdb.DB
	mu sync.Mutex
}

// Open applies migrations to db and returns a ready *SQLStore.
//
// db should be obtained from cpdb.Open("cdn") for production, or from
// cpdb.OpenSQLiteDSN(":memory:") for tests. Migrations are applied
// automatically and are idempotent (safe to call on every startup).
func Open(db *cpdb.DB) (*SQLStore, error) {
	subFS, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("cdn: embed sub: %w", err)
	}
	if err := db.Migrate(subFS); err != nil {
		return nil, fmt.Errorf("cdn: migrate: %w", err)
	}
	return &SQLStore{db: db}, nil
}

// Close closes the underlying database.
func (s *SQLStore) Close() error { return s.db.Close() }

// compile-time assertion: SQLStore satisfies Store.
var _ Store = (*SQLStore)(nil)

// ─────────────────────────────────────────────────────────────────────────────
// GetConfig / SetConfig / DeleteConfig
// ─────────────────────────────────────────────────────────────────────────────

func (s *SQLStore) GetConfig(ctx context.Context, accountID string) (Config, error) {
	var cfg Config
	var createdAt, updatedAt string
	var mtls, enabled int
	err := s.db.QueryRowContext(ctx,
		s.db.Rebind(`SELECT account_id, provider, origin_host, mtls_enabled, host_header,
		        enabled, created_at, updated_at
		   FROM cdn_configs WHERE account_id = ?`),
		accountID,
	).Scan(&cfg.AccountID, &cfg.Provider, &cfg.OriginHost,
		&mtls, &cfg.HostHeader, &enabled, &createdAt, &updatedAt)
	if err == sql.ErrNoRows {
		return Config{}, ErrNotFound
	}
	if err != nil {
		return Config{}, fmt.Errorf("cdn: get config: %w", err)
	}
	cfg.MTLSEnabled = mtls != 0
	cfg.Enabled = enabled != 0
	cfg.CreatedAt = parseTS(createdAt)
	cfg.UpdatedAt = parseTS(updatedAt)
	return cfg, nil
}

func (s *SQLStore) SetConfig(ctx context.Context, cfg Config) error {
	now := nowTS()

	// Preserve created_at for existing rows.
	existing, err := s.GetConfig(ctx, cfg.AccountID)
	createdAt := now
	if err == nil {
		createdAt = existing.CreatedAt.UTC().Format(time.RFC3339Nano)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	mtls := 0
	if cfg.MTLSEnabled {
		mtls = 1
	}
	enabled := 0
	if cfg.Enabled {
		enabled = 1
	}

	_, err = s.db.ExecContext(ctx,
		s.db.Rebind(`INSERT INTO cdn_configs
		  (account_id, provider, origin_host, mtls_enabled, host_header, enabled, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(account_id) DO UPDATE SET
		   provider     = excluded.provider,
		   origin_host  = excluded.origin_host,
		   mtls_enabled = excluded.mtls_enabled,
		   host_header  = excluded.host_header,
		   enabled      = excluded.enabled,
		   updated_at   = excluded.updated_at`),
		cfg.AccountID, string(cfg.Provider), cfg.OriginHost,
		mtls, cfg.HostHeader, enabled, createdAt, now,
	)
	return err
}

func (s *SQLStore) DeleteConfig(ctx context.Context, accountID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.db.ExecContext(ctx,
		s.db.Rebind(`DELETE FROM cdn_configs WHERE account_id = ?`), accountID)
	if err != nil {
		return fmt.Errorf("cdn: delete config: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// GetIPRanges / SetIPRanges
// ─────────────────────────────────────────────────────────────────────────────

func (s *SQLStore) GetIPRanges(ctx context.Context, provider Provider) ([]IPRange, error) {
	rows, err := s.db.QueryContext(ctx,
		s.db.Rebind(`SELECT provider, cidr, fetched_at FROM cdn_ip_ranges
		  WHERE provider = ? ORDER BY cidr`),
		string(provider),
	)
	if err != nil {
		return nil, fmt.Errorf("cdn: get ip ranges: %w", err)
	}
	defer rows.Close()

	var out []IPRange
	for rows.Next() {
		var r IPRange
		var fetchedAt string
		if err := rows.Scan(&r.Provider, &r.CIDR, &fetchedAt); err != nil {
			return nil, fmt.Errorf("cdn: scan ip range: %w", err)
		}
		r.FetchedAt = parseTS(fetchedAt)
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *SQLStore) SetIPRanges(ctx context.Context, provider Provider, cidrs []string) error {
	now := nowTS()
	sort.Strings(cidrs)

	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("cdn: set ip ranges begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	// Replace all ranges for this provider atomically.
	if _, err := tx.ExecContext(ctx,
		s.db.Rebind(`DELETE FROM cdn_ip_ranges WHERE provider = ?`), string(provider)); err != nil {
		return fmt.Errorf("cdn: delete ip ranges: %w", err)
	}
	for _, cidr := range cidrs {
		if _, err := tx.ExecContext(ctx,
			s.db.Rebind(`INSERT INTO cdn_ip_ranges (provider, cidr, fetched_at) VALUES (?, ?, ?)`),
			string(provider), cidr, now,
		); err != nil {
			return fmt.Errorf("cdn: insert ip range: %w", err)
		}
	}
	return tx.Commit()
}

// ─────────────────────────────────────────────────────────────────────────────
// Time helpers
// ─────────────────────────────────────────────────────────────────────────────

func nowTS() string { return time.Now().UTC().Format(time.RFC3339Nano) }

func parseTS(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		t, _ = time.Parse(time.RFC3339, s)
	}
	return t.UTC()
}
