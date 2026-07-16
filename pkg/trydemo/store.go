package trydemo

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"time"

	"github.com/vul-os/vulos-management/pkg/cpdb"
)

// store is the concrete cpdb-backed implementation of Store.
type store struct {
	db *cpdb.DB
}

// Open applies migrations to db and returns a ready Store.
//
// db should be obtained from cpdb.Open("trydemo") for production, or from
// cpdb.OpenSQLiteDSN(":memory:") for tests. Migrations are applied
// automatically and are idempotent.
func Open(db *cpdb.DB) (Store, error) {
	subFS, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("trydemo: embed sub: %w", err)
	}
	if err := db.Migrate(subFS); err != nil {
		return nil, fmt.Errorf("trydemo: migrate: %w", err)
	}
	return &store{db: db}, nil
}

func (s *store) LastDriverClaim(ctx context.Context, ip string) (time.Time, error) {
	var raw sql.NullString
	err := s.db.QueryRowContext(ctx,
		s.db.Rebind(`SELECT last_driver_claim FROM trydemo_ip_counters WHERE ip = ?`), ip,
	).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("trydemo: last_driver_claim: %w", err)
	}
	if !raw.Valid || raw.String == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339, raw.String)
	if err != nil {
		return time.Time{}, fmt.Errorf("trydemo: parse last_driver_claim: %w", err)
	}
	return t, nil
}

func (s *store) RecordDriverClaim(ctx context.Context, ip string, at time.Time) error {
	_, err := s.db.ExecContext(ctx,
		s.db.Rebind(`INSERT INTO trydemo_ip_counters (ip, last_driver_claim)
		 VALUES (?, ?)
		 ON CONFLICT(ip) DO UPDATE SET last_driver_claim = excluded.last_driver_claim`),
		ip, at.UTC().Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("trydemo: record_driver_claim: %w", err)
	}
	return nil
}

func (s *store) AccumulateUsage(ctx context.Context, yyyymm string, computeMinDelta float64, egressBytesDelta int64, estCostDelta float64) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.db.ExecContext(ctx,
		s.db.Rebind(`INSERT INTO trydemo_month_accum (yyyymm, compute_minutes, egress_bytes, estimated_cost_usd, updated_at)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(yyyymm) DO UPDATE SET
		     compute_minutes    = trydemo_month_accum.compute_minutes    + excluded.compute_minutes,
		     egress_bytes       = trydemo_month_accum.egress_bytes       + excluded.egress_bytes,
		     estimated_cost_usd = trydemo_month_accum.estimated_cost_usd + excluded.estimated_cost_usd,
		     updated_at         = excluded.updated_at`),
		yyyymm, computeMinDelta, egressBytesDelta, estCostDelta, now,
	)
	if err != nil {
		return fmt.Errorf("trydemo: accumulate_usage: %w", err)
	}
	return nil
}

func (s *store) MonthlyCostUSD(ctx context.Context, yyyymm string) (float64, error) {
	var cost float64
	err := s.db.QueryRowContext(ctx,
		s.db.Rebind(`SELECT estimated_cost_usd FROM trydemo_month_accum WHERE yyyymm = ?`), yyyymm,
	).Scan(&cost)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("trydemo: monthly_cost_usd: %w", err)
	}
	return cost, nil
}

func (s *store) LogEvent(ctx context.Context, eventType, ip, sessionID, reason string, at time.Time) error {
	_, err := s.db.ExecContext(ctx,
		s.db.Rebind(`INSERT INTO trydemo_session_events (event_type, ip, session_id, reason, at)
		 VALUES (?, ?, ?, ?, ?)`),
		eventType, nullStr(ip), nullStr(sessionID), nullStr(reason),
		at.UTC().Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("trydemo: log_event: %w", err)
	}
	return nil
}

func (s *store) Close() error { return s.db.Close() }

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}
