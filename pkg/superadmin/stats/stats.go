// Package stats provides daily snapshot collection of key platform metrics.
// Snapshots are taken at 02:00 UTC each day and persisted via the cpdb seam
// (SQLite for self-host, Postgres for cloud deployments).
package stats

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"time"

	"github.com/vul-os/vulos-management/pkg/cpdb"
)

// DailySnapshot is one row of the daily_snapshots table.
type DailySnapshot struct {
	Date              string // YYYY-MM-DD (primary key)
	TotalAccounts     int64
	Active30D         int64 // accounts active in last 30 days
	Active1D          int64 // accounts active in last 1 day (DAU)
	SignupsToday      int64
	SignupsByTierJSON string // JSON: {"free":N,"pro":N,...}
	MRRZARCents       int64  // monthly recurring revenue in ZAR cents
}

// SignupsByTier is the parsed form of SignupsByTierJSON.
type SignupsByTier struct {
	Free       int64 `json:"free"`
	Pro        int64 `json:"pro"`
	Team       int64 `json:"team"`
	Enterprise int64 `json:"enterprise"`
}

// AccountsReader is the minimal interface needed to collect account stats from
// the auth/accounts database. Implementations in production wrap the accounts
// SQL store.
type AccountsReader interface {
	// CountTotal returns the total number of accounts.
	CountTotal(ctx context.Context) (int64, error)
	// CountActiveAfter returns accounts with last_activity_at >= since.
	CountActiveAfter(ctx context.Context, since time.Time) (int64, error)
	// CountSignupsSince returns signups created >= since.
	CountSignupsSince(ctx context.Context, since time.Time) (int64, error)
	// SignupsByTierSince returns signup counts grouped by tier since the given time.
	SignupsByTierSince(ctx context.Context, since time.Time) (SignupsByTier, error)
}

// BillingReader is the minimal interface needed to compute MRR from the
// billing database.
type BillingReader interface {
	// MRRZARCents returns the sum of active subscription amounts in ZAR cents.
	MRRZARCents(ctx context.Context) (int64, error)
}

// Store manages the stats database.
type Store struct {
	db *cpdb.DB
}

// Open applies migrations to db and returns a ready Store.
//
// db should be obtained from cpdb.Open("superadmin_stats") for production,
// or from cpdb.OpenSQLiteDSN(":memory:") for tests. Migrations are applied
// automatically and are idempotent (safe to call on every startup).
func Open(db *cpdb.DB) (*Store, error) {
	subFS, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("stats: embed sub: %w", err)
	}
	if err := db.Migrate(subFS); err != nil {
		return nil, fmt.Errorf("stats: migrate: %w", err)
	}
	return &Store{db: db}, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// RunSnapshot collects stats for today and upserts a row into daily_snapshots.
// Idempotent: calling it twice on the same date overwrites with fresh data.
func (s *Store) RunSnapshot(ctx context.Context, today time.Time, accounts AccountsReader, billing BillingReader) error {
	dateStr := today.UTC().Format("2006-01-02")

	total, err := accounts.CountTotal(ctx)
	if err != nil {
		return fmt.Errorf("stats: count total: %w", err)
	}

	active30, err := accounts.CountActiveAfter(ctx, today.UTC().AddDate(0, 0, -30))
	if err != nil {
		return fmt.Errorf("stats: count active30: %w", err)
	}

	active1, err := accounts.CountActiveAfter(ctx, today.UTC().AddDate(0, 0, -1))
	if err != nil {
		return fmt.Errorf("stats: count active1: %w", err)
	}

	dayStart := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, time.UTC)
	signupsToday, err := accounts.CountSignupsSince(ctx, dayStart)
	if err != nil {
		return fmt.Errorf("stats: count signups today: %w", err)
	}

	tierMix, err := accounts.SignupsByTierSince(ctx, dayStart)
	if err != nil {
		return fmt.Errorf("stats: tier mix: %w", err)
	}
	tierJSON, _ := json.Marshal(tierMix)

	mrr, err := billing.MRRZARCents(ctx)
	if err != nil {
		return fmt.Errorf("stats: mrr: %w", err)
	}

	_, err = s.db.ExecContext(ctx, s.db.Rebind(`
		INSERT INTO daily_snapshots
			(date, total_accounts, active_30d, active_1d, signups_today, signups_by_tier_json, mrr_zar_cents)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(date) DO UPDATE SET
			total_accounts       = excluded.total_accounts,
			active_30d           = excluded.active_30d,
			active_1d            = excluded.active_1d,
			signups_today        = excluded.signups_today,
			signups_by_tier_json = excluded.signups_by_tier_json,
			mrr_zar_cents        = excluded.mrr_zar_cents
	`), dateStr, total, active30, active1, signupsToday, string(tierJSON), mrr)
	return err
}

// Series returns daily snapshots for the given date range [from, to] inclusive.
// Dates are in YYYY-MM-DD format.
func (s *Store) Series(ctx context.Context, from, to time.Time) ([]DailySnapshot, error) {
	fromStr := from.UTC().Format("2006-01-02")
	toStr := to.UTC().Format("2006-01-02")

	rows, err := s.db.QueryContext(ctx, s.db.Rebind(`
		SELECT date, total_accounts, active_30d, active_1d, signups_today, signups_by_tier_json, mrr_zar_cents
		FROM daily_snapshots
		WHERE date >= ? AND date <= ?
		ORDER BY date ASC
	`), fromStr, toStr)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var snaps []DailySnapshot
	for rows.Next() {
		var sn DailySnapshot
		if err := rows.Scan(&sn.Date, &sn.TotalAccounts, &sn.Active30D, &sn.Active1D,
			&sn.SignupsToday, &sn.SignupsByTierJSON, &sn.MRRZARCents); err != nil {
			return nil, err
		}
		snaps = append(snaps, sn)
	}
	return snaps, rows.Err()
}

// BackfillMissingDays inserts zero-value rows for any day in [from, to] that has
// no snapshot, so the time series has no gaps.
func (s *Store) BackfillMissingDays(ctx context.Context, from, to time.Time) error {
	for d := from.UTC(); !d.After(to.UTC()); d = d.AddDate(0, 0, 1) {
		dateStr := d.Format("2006-01-02")
		_, err := s.db.ExecContext(ctx, s.db.Rebind(`
			INSERT INTO daily_snapshots (date, signups_by_tier_json)
			VALUES (?, '{}')
			ON CONFLICT(date) DO NOTHING
		`), dateStr)
		if err != nil {
			return err
		}
	}
	return nil
}

// ─── Daily worker ─────────────────────────────────────────────────────────────

// Worker runs RunSnapshot daily at 02:00 UTC.
type Worker struct {
	Store    *Store
	Accounts AccountsReader
	Billing  BillingReader
}

// Run starts the daily snapshot worker. Blocks until ctx is cancelled.
func (w *Worker) Run(ctx context.Context) {
	for {
		now := time.Now().UTC()
		// Calculate next 02:00 UTC.
		next := time.Date(now.Year(), now.Month(), now.Day(), 2, 0, 0, 0, time.UTC)
		if !next.After(now) {
			next = next.Add(24 * time.Hour)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Until(next)):
			today := time.Now().UTC()
			if err := w.Store.RunSnapshot(ctx, today, w.Accounts, w.Billing); err != nil {
				log.Printf("[stats] snapshot error: %v", err)
			} else {
				log.Printf("[stats] daily snapshot taken for %s", today.Format("2006-01-02"))
			}
		}
	}
}
