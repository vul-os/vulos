package edge

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"time"

	"github.com/vul-os/vulos-management/pkg/cpdb"
)

// SQLStore is the production cpdb-backed implementation of Store.
// Concurrency-safe via the underlying cpdb connection pool (Postgres) or
// WAL+MaxOpenConns(1) (SQLite, managed by cpdb.Open).
// Behaviour is semantically identical to MemStore.
type SQLStore struct {
	db *cpdb.DB
}

// Open applies migrations to db and returns a ready *SQLStore.
//
// db should be obtained from cpdb.Open("edge") for production, or from
// cpdb.OpenSQLiteDSN(":memory:") for tests.
func Open(db *cpdb.DB) (*SQLStore, error) {
	subFS, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("edge: embed sub: %w", err)
	}
	if err := db.Migrate(subFS); err != nil {
		return nil, fmt.Errorf("edge: migrate: %w", err)
	}
	return &SQLStore{db: db}, nil
}

// Close closes the underlying database connection.
func (s *SQLStore) Close() error { return s.db.Close() }

// compile-time assertion: SQLStore satisfies Store.
var _ Store = (*SQLStore)(nil)

// ─────────────────────────────────────────────────────────────────────────────
// Node methods
// ─────────────────────────────────────────────────────────────────────────────

func (s *SQLStore) UpsertNode(ctx context.Context, n Node) (Node, error) {
	if n.ID == "" || n.Region == "" || n.OriginURL == "" {
		return Node{}, ErrInvalidConfig
	}
	now := time.Now().UTC()
	drained := 0
	if n.Drained {
		drained = 1
	}
	_, err := s.db.ExecContext(ctx, s.db.Rebind(`
		INSERT INTO edge_nodes (id, region, origin_url, drained, created_at, updated_at)
		     VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
		     region     = excluded.region,
		     origin_url = excluded.origin_url,
		     drained    = excluded.drained,
		     updated_at = excluded.updated_at`),
		n.ID, n.Region, n.OriginURL, drained,
		now.Format(time.RFC3339), now.Format(time.RFC3339),
	)
	if err != nil {
		return Node{}, fmt.Errorf("edge: upsert node: %w", err)
	}
	return s.GetNode(ctx, n.ID)
}

func (s *SQLStore) GetNode(ctx context.Context, id string) (Node, error) {
	row := s.db.QueryRowContext(ctx, s.db.Rebind(
		`SELECT id, region, origin_url, drained, created_at, updated_at
		   FROM edge_nodes WHERE id = ?`), id)
	return scanNode(row)
}

func (s *SQLStore) ListNodes(ctx context.Context) ([]Node, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, region, origin_url, drained, created_at, updated_at
		   FROM edge_nodes
		  ORDER BY drained ASC, id ASC`)
	if err != nil {
		return nil, fmt.Errorf("edge: list nodes: %w", err)
	}
	defer rows.Close()
	var out []Node
	for rows.Next() {
		n, err := scanNode(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func (s *SQLStore) DrainNode(ctx context.Context, id string) error {
	return s.setDrain(ctx, id, 1)
}

func (s *SQLStore) UndrainNode(ctx context.Context, id string) error {
	return s.setDrain(ctx, id, 0)
}

func (s *SQLStore) setDrain(ctx context.Context, id string, drained int) error {
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := s.db.ExecContext(ctx, s.db.Rebind(
		`UPDATE edge_nodes SET drained = ?, updated_at = ? WHERE id = ?`),
		drained, now, id)
	if err != nil {
		return fmt.Errorf("edge: set drain: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNodeNotFound
	}
	return nil
}

// scanner is satisfied by *sql.Row and *sql.Rows.
type scanner interface {
	Scan(dest ...any) error
}

func scanNode(s scanner) (Node, error) {
	var n Node
	var drainedInt int
	var createdStr, updatedStr string
	err := s.Scan(&n.ID, &n.Region, &n.OriginURL, &drainedInt, &createdStr, &updatedStr)
	if errors.Is(err, sql.ErrNoRows) {
		return Node{}, ErrNodeNotFound
	}
	if err != nil {
		return Node{}, fmt.Errorf("edge: scan node: %w", err)
	}
	n.Drained = drainedInt == 1
	n.CreatedAt, _ = time.Parse(time.RFC3339, createdStr)
	n.UpdatedAt, _ = time.Parse(time.RFC3339, updatedStr)
	return n, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Domain config methods
// ─────────────────────────────────────────────────────────────────────────────

func (s *SQLStore) SetDomainConfig(ctx context.Context, cfg DomainConfig) (DomainConfig, error) {
	if cfg.ULID == "" || cfg.Domain == "" {
		return DomainConfig{}, ErrInvalidConfig
	}
	if cfg.CacheTTLSec < 0 || cfg.RateLimitRPS < 0 {
		return DomainConfig{}, ErrInvalidConfig
	}
	now := time.Now().UTC().Format(time.RFC3339)
	enabled := 1
	if !cfg.Enabled {
		enabled = 0
	}
	_, err := s.db.ExecContext(ctx, s.db.Rebind(`
		INSERT INTO edge_domain_configs (ulid, domain, cache_ttl_sec, rate_limit_rps, enabled, created_at, updated_at)
		     VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(ulid, domain) DO UPDATE SET
		     cache_ttl_sec  = excluded.cache_ttl_sec,
		     rate_limit_rps = excluded.rate_limit_rps,
		     enabled        = excluded.enabled,
		     updated_at     = excluded.updated_at`),
		cfg.ULID, cfg.Domain, cfg.CacheTTLSec, cfg.RateLimitRPS, enabled, now, now,
	)
	if err != nil {
		return DomainConfig{}, fmt.Errorf("edge: set domain config: %w", err)
	}
	return s.GetDomainConfig(ctx, cfg.ULID, cfg.Domain)
}

func (s *SQLStore) GetDomainConfig(ctx context.Context, ulid, domain string) (DomainConfig, error) {
	row := s.db.QueryRowContext(ctx, s.db.Rebind(
		`SELECT id, ulid, domain, cache_ttl_sec, rate_limit_rps, enabled, created_at, updated_at
		   FROM edge_domain_configs WHERE ulid = ? AND domain = ?`), ulid, domain)
	return scanDomainConfig(row)
}

func (s *SQLStore) ListDomainConfigs(ctx context.Context, ulid string) ([]DomainConfig, error) {
	rows, err := s.db.QueryContext(ctx, s.db.Rebind(
		`SELECT id, ulid, domain, cache_ttl_sec, rate_limit_rps, enabled, created_at, updated_at
		   FROM edge_domain_configs
		  WHERE ulid = ?
		  ORDER BY domain ASC`), ulid)
	if err != nil {
		return nil, fmt.Errorf("edge: list domain configs: %w", err)
	}
	defer rows.Close()
	var out []DomainConfig
	for rows.Next() {
		cfg, err := scanDomainConfig(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, cfg)
	}
	return out, rows.Err()
}

func (s *SQLStore) DeleteDomainConfig(ctx context.Context, ulid, domain string) error {
	res, err := s.db.ExecContext(ctx, s.db.Rebind(
		`DELETE FROM edge_domain_configs WHERE ulid = ? AND domain = ?`), ulid, domain)
	if err != nil {
		return fmt.Errorf("edge: delete domain config: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrDomainNotFound
	}
	return nil
}

func scanDomainConfig(s scanner) (DomainConfig, error) {
	var cfg DomainConfig
	var enabledInt int
	var createdStr, updatedStr string
	err := s.Scan(
		&cfg.ID, &cfg.ULID, &cfg.Domain,
		&cfg.CacheTTLSec, &cfg.RateLimitRPS, &enabledInt,
		&createdStr, &updatedStr,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return DomainConfig{}, ErrDomainNotFound
	}
	if err != nil {
		return DomainConfig{}, fmt.Errorf("edge: scan domain config: %w", err)
	}
	cfg.Enabled = enabledInt == 1
	cfg.CreatedAt, _ = time.Parse(time.RFC3339, createdStr)
	cfg.UpdatedAt, _ = time.Parse(time.RFC3339, updatedStr)
	return cfg, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Bandwidth metering methods
// ─────────────────────────────────────────────────────────────────────────────

func (s *SQLStore) EmitBandwidthEvent(ctx context.Context, ev BandwidthEvent) (BandwidthEvent, error) {
	if ev.AccountID == "" || ev.ULID == "" || ev.Domain == "" {
		return BandwidthEvent{}, ErrInvalidConfig
	}
	now := time.Now().UTC()
	if ev.RecordedAt.IsZero() {
		ev.RecordedAt = now
	}

	if s.db.Backend() == cpdb.BackendPostgres {
		var id int64
		err := s.db.QueryRowContext(ctx, s.db.Rebind(`
			INSERT INTO edge_bandwidth_events
			  (account_id, ulid, domain, bytes_in, bytes_out, period_start, period_end, recorded_at)
			  VALUES (?, ?, ?, ?, ?, ?, ?, ?) RETURNING id`),
			ev.AccountID, ev.ULID, ev.Domain,
			ev.BytesIn, ev.BytesOut,
			ev.PeriodStart.UTC().Format(time.RFC3339),
			ev.PeriodEnd.UTC().Format(time.RFC3339),
			ev.RecordedAt.UTC().Format(time.RFC3339),
		).Scan(&id)
		if err != nil {
			return BandwidthEvent{}, fmt.Errorf("edge: emit bandwidth event: %w", err)
		}
		ev.ID = id
		return ev, nil
	}

	res, err := s.db.ExecContext(ctx, s.db.Rebind(`
		INSERT INTO edge_bandwidth_events
		  (account_id, ulid, domain, bytes_in, bytes_out, period_start, period_end, recorded_at)
		  VALUES (?, ?, ?, ?, ?, ?, ?, ?)`),
		ev.AccountID, ev.ULID, ev.Domain,
		ev.BytesIn, ev.BytesOut,
		ev.PeriodStart.UTC().Format(time.RFC3339),
		ev.PeriodEnd.UTC().Format(time.RFC3339),
		ev.RecordedAt.UTC().Format(time.RFC3339),
	)
	if err != nil {
		return BandwidthEvent{}, fmt.Errorf("edge: emit bandwidth event: %w", err)
	}
	id, _ := res.LastInsertId()
	ev.ID = id
	return ev, nil
}

func (s *SQLStore) ListBandwidthEvents(ctx context.Context, accountID string, limit int) ([]BandwidthEvent, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, s.db.Rebind(`
		SELECT id, account_id, ulid, domain, bytes_in, bytes_out, period_start, period_end, recorded_at
		  FROM edge_bandwidth_events
		 WHERE account_id = ?
		 ORDER BY recorded_at DESC
		 LIMIT ?`), accountID, limit)
	if err != nil {
		return nil, fmt.Errorf("edge: list bandwidth events: %w", err)
	}
	defer rows.Close()
	var out []BandwidthEvent
	for rows.Next() {
		ev, err := scanBandwidthEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, ev)
	}
	return out, rows.Err()
}

func (s *SQLStore) BandwidthTotal(ctx context.Context, ulid string, since time.Time) (BandwidthTotals, error) {
	row := s.db.QueryRowContext(ctx, s.db.Rebind(`
		SELECT COALESCE(SUM(bytes_in),0), COALESCE(SUM(bytes_out),0)
		  FROM edge_bandwidth_events
		 WHERE ulid = ? AND period_end >= ?`),
		ulid, since.UTC().Format(time.RFC3339))
	var tot BandwidthTotals
	if err := row.Scan(&tot.BytesIn, &tot.BytesOut); err != nil {
		return BandwidthTotals{}, fmt.Errorf("edge: bandwidth total: %w", err)
	}
	return tot, nil
}

func scanBandwidthEvent(s scanner) (BandwidthEvent, error) {
	var ev BandwidthEvent
	var psStr, peStr, recStr string
	err := s.Scan(
		&ev.ID, &ev.AccountID, &ev.ULID, &ev.Domain,
		&ev.BytesIn, &ev.BytesOut,
		&psStr, &peStr, &recStr,
	)
	if err != nil {
		return BandwidthEvent{}, fmt.Errorf("edge: scan bandwidth event: %w", err)
	}
	ev.PeriodStart, _ = time.Parse(time.RFC3339, psStr)
	ev.PeriodEnd, _ = time.Parse(time.RFC3339, peStr)
	ev.RecordedAt, _ = time.Parse(time.RFC3339, recStr)
	return ev, nil
}
