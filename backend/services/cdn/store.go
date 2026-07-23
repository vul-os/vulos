// store.go — SQLite-backed Store for the cdn package (single-owner box
// scope). Pure-Go modernc.org/sqlite; single writer via SetMaxOpenConns(1) +
// WAL, matching every other on-box store (see services/kms, services/webhooks).
// Migrations are embedded and applied via the shared internal/migrate runner.
package cdn

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	dbmigrate "vulos/backend/internal/migrate"

	_ "modernc.org/sqlite" // pure-Go SQLite driver (no CGo)
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// singletonConfigID is the fixed primary key of the one-and-only cdn_config
// row (CHECK(id = 1) enforces this at the schema level too).
const singletonConfigID = 1

// SQLStore is the production SQLite-backed Store for the cdn package.
type SQLStore struct {
	db *sql.DB
	mu sync.Mutex
}

// compile-time assertion: SQLStore satisfies Store.
var _ Store = (*SQLStore)(nil)

// OpenStore opens (or creates) the cdn SQLite store at <dbDir>/cdn.db and
// applies package-owned migrations idempotently. If dbDir is empty an
// in-memory store is used (tests only — production code must always pass a
// real dbDir so config/ranges survive a restart).
func OpenStore(dbDir string) (*SQLStore, error) {
	dsn := "file::memory:?cache=shared&_pragma=busy_timeout(5000)"
	if dbDir != "" {
		path := filepath.Join(dbDir, "cdn.db")
		dsn = "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("cdn: open db: %w", err)
	}
	db.SetMaxOpenConns(1) // single-writer file; serialize writers
	if err := dbmigrate.Apply(db, migrationsFS, "migrations"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("cdn: migrate: %w", err)
	}
	return &SQLStore{db: db}, nil
}

// Close closes the underlying database.
func (s *SQLStore) Close() error { return s.db.Close() }

// GetConfig returns the singleton BYO-CDN config. ErrNotFound if unset.
func (s *SQLStore) GetConfig(ctx context.Context) (Config, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT provider, origin_host, host_header, mtls_enabled, firewall_enabled,
		       ssh_port, extra_allow_ports, last_ruleset, last_ruleset_at,
		       created_at, updated_at
		FROM cdn_config WHERE id = ?`, singletonConfigID)

	var cfg Config
	var mtls, fwEnabled int
	var extraPortsJSON string
	var lastRulesetAt sql.NullInt64
	var createdAt, updatedAt int64
	err := row.Scan(&cfg.Provider, &cfg.OriginHost, &cfg.HostHeader, &mtls, &fwEnabled,
		&cfg.SSHPort, &extraPortsJSON, &cfg.LastRuleset, &lastRulesetAt,
		&createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Config{}, ErrNotFound
	}
	if err != nil {
		return Config{}, fmt.Errorf("cdn: get config: %w", err)
	}
	cfg.MTLSEnabled = mtls != 0
	cfg.FirewallEnabled = fwEnabled != 0
	cfg.ExtraAllowPorts = unmarshalPorts(extraPortsJSON)
	if lastRulesetAt.Valid {
		cfg.LastRulesetAt = time.Unix(lastRulesetAt.Int64, 0).UTC()
	}
	cfg.CreatedAt = time.Unix(createdAt, 0).UTC()
	cfg.UpdatedAt = time.Unix(updatedAt, 0).UTC()
	return cfg, nil
}

// SetConfig upserts the singleton BYO-CDN config. CreatedAt is preserved
// across updates; UpdatedAt is always refreshed to now.
func (s *SQLStore) SetConfig(ctx context.Context, cfg Config) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC()
	createdAt := now
	if existing, err := s.getConfigLocked(ctx); err == nil {
		createdAt = existing.CreatedAt
	}

	mtls, fwEnabled := 0, 0
	if cfg.MTLSEnabled {
		mtls = 1
	}
	if cfg.FirewallEnabled {
		fwEnabled = 1
	}
	var lastRulesetAt sql.NullInt64
	if !cfg.LastRulesetAt.IsZero() {
		lastRulesetAt = sql.NullInt64{Int64: cfg.LastRulesetAt.Unix(), Valid: true}
	}

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO cdn_config
		  (id, provider, origin_host, host_header, mtls_enabled, firewall_enabled,
		   ssh_port, extra_allow_ports, last_ruleset, last_ruleset_at, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET
		  provider=excluded.provider, origin_host=excluded.origin_host,
		  host_header=excluded.host_header, mtls_enabled=excluded.mtls_enabled,
		  firewall_enabled=excluded.firewall_enabled, ssh_port=excluded.ssh_port,
		  extra_allow_ports=excluded.extra_allow_ports, last_ruleset=excluded.last_ruleset,
		  last_ruleset_at=excluded.last_ruleset_at, updated_at=excluded.updated_at`,
		singletonConfigID, string(cfg.Provider), cfg.OriginHost, cfg.HostHeader, mtls, fwEnabled,
		cfg.SSHPort, marshalPorts(cfg.ExtraAllowPorts), cfg.LastRuleset, lastRulesetAt,
		createdAt.Unix(), now.Unix(),
	)
	if err != nil {
		return fmt.Errorf("cdn: set config: %w", err)
	}
	return nil
}

// getConfigLocked is GetConfig without acquiring s.mu — callers must already
// hold it (used by SetConfig to preserve CreatedAt).
func (s *SQLStore) getConfigLocked(ctx context.Context) (Config, error) {
	row := s.db.QueryRowContext(ctx, `SELECT created_at FROM cdn_config WHERE id = ?`, singletonConfigID)
	var createdAt int64
	if err := row.Scan(&createdAt); err != nil {
		return Config{}, err
	}
	return Config{CreatedAt: time.Unix(createdAt, 0).UTC()}, nil
}

// DeleteConfig removes the singleton config row. ErrNotFound if none exists.
func (s *SQLStore) DeleteConfig(ctx context.Context) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM cdn_config WHERE id = ?`, singletonConfigID)
	if err != nil {
		return fmt.Errorf("cdn: delete config: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// GetIPRanges returns cached IP ranges for provider, sorted by CIDR.
func (s *SQLStore) GetIPRanges(ctx context.Context, provider Provider) ([]IPRange, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT cidr, fetched_at FROM cdn_ip_ranges WHERE provider = ? ORDER BY cidr ASC`,
		string(provider))
	if err != nil {
		return nil, fmt.Errorf("cdn: get ip ranges: %w", err)
	}
	defer rows.Close()

	var out []IPRange
	for rows.Next() {
		var cidr string
		var fetchedAt int64
		if err := rows.Scan(&cidr, &fetchedAt); err != nil {
			return nil, fmt.Errorf("cdn: scan ip range: %w", err)
		}
		out = append(out, IPRange{Provider: provider, CIDR: cidr, FetchedAt: time.Unix(fetchedAt, 0).UTC()})
	}
	return out, rows.Err()
}

// SetIPRanges replaces all cached IP ranges for provider in one transaction
// (delete-then-insert), so a refresh never leaves a mix of old and new
// entries visible to a concurrent reader for more than the transaction's
// duration.
func (s *SQLStore) SetIPRanges(ctx context.Context, provider Provider, cidrs []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("cdn: set ip ranges: begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op if committed

	if _, err := tx.ExecContext(ctx, `DELETE FROM cdn_ip_ranges WHERE provider = ?`, string(provider)); err != nil {
		return fmt.Errorf("cdn: set ip ranges: clear: %w", err)
	}

	now := time.Now().UTC().Unix()
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO cdn_ip_ranges (provider, cidr, fetched_at) VALUES (?,?,?)
		ON CONFLICT(provider, cidr) DO UPDATE SET fetched_at=excluded.fetched_at`)
	if err != nil {
		return fmt.Errorf("cdn: set ip ranges: prepare: %w", err)
	}
	defer stmt.Close()

	for _, c := range sortCIDRs(cidrs) {
		if _, err := stmt.ExecContext(ctx, string(provider), c, now); err != nil {
			return fmt.Errorf("cdn: set ip ranges: insert %q: %w", c, err)
		}
	}

	return tx.Commit()
}
