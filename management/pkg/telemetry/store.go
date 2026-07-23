package telemetry

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"sync"
	"time"

	"github.com/vul-os/vulos-management/pkg/cpdb"
)

// SQLStore is the production cpdb-backed implementation of Store.
// Concurrency-safe: WAL mode + single writer mutex.
// Behaviour is semantically identical to MemStore.
type SQLStore struct {
	db *cpdb.DB
	mu sync.Mutex // serialises all writes
}

// Open applies migrations to db and returns a ready *SQLStore.
//
// db should be obtained from cpdb.Open("telemetry") for production, or from
// cpdb.OpenSQLiteDSN(":memory:") for tests. Migrations are applied
// automatically and are idempotent (safe to call on every startup).
func Open(db *cpdb.DB) (*SQLStore, error) {
	subFS, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("telemetry: embed sub: %w", err)
	}
	if err := db.Migrate(subFS); err != nil {
		return nil, fmt.Errorf("telemetry: migrate: %w", err)
	}
	return &SQLStore{db: db}, nil
}

// Close closes the underlying database connection.
func (s *SQLStore) Close() error { return s.db.Close() }

// compile-time assertion: SQLStore satisfies Store.
var _ Store = (*SQLStore)(nil)

// ─────────────────────────────────────────────────────────────────────────────
// InsertSample
// ─────────────────────────────────────────────────────────────────────────────

func (s *SQLStore) InsertSample(ctx context.Context, sample Sample) (Sample, []Alert, error) {
	if sample.SampledAt.IsZero() {
		sample.SampledAt = time.Now().UTC()
	}
	ts := sample.SampledAt.UTC().Format(time.RFC3339Nano)

	boolInt := func(b bool) int {
		if b {
			return 1
		}
		return 0
	}

	// Pre-compute rebound queries before acquiring the lock.
	insertSampleQ := s.db.Rebind(
		`INSERT INTO telemetry_samples
		   (ulid, org_id, sampled_at,
		    ip_reputation_score, blocklist_spamhaus, blocklist_barracuda, blocklist_sorbs,
		    disk_used_pct,
		    sync_queue_depth, mail_queue_depth,
		    dkim_expiry_days, dmarc_pass_rate_pct,
		    uptime_sec, last_backup_age_sec)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?) RETURNING id`)
	insertAlertQ := s.db.Rebind(
		`INSERT INTO telemetry_alerts (ulid, org_id, signal, severity, detail, fired_at)
		 VALUES (?, ?, ?, ?, ?, ?) RETURNING id`)

	s.mu.Lock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		s.mu.Unlock()
		return Sample{}, nil, fmt.Errorf("telemetry: insert sample begin: %w", err)
	}

	var sid int64
	if err := tx.QueryRowContext(ctx, insertSampleQ,
		sample.ULID, sample.OrgID, ts,
		sample.IPReputationScore,
		boolInt(sample.BlocklistSpamhaus),
		boolInt(sample.BlocklistBarracuda),
		boolInt(sample.BlocklistSORBS),
		sample.DiskUsedPct,
		sample.SyncQueueDepth, sample.MailQueueDepth,
		sample.DKIMExpiryDays, sample.DMARCPassRatePct,
		sample.UptimeSec, sample.LastBackupAgeSec,
	).Scan(&sid); err != nil {
		tx.Rollback() //nolint:errcheck
		s.mu.Unlock()
		return Sample{}, nil, fmt.Errorf("telemetry: insert sample: %w", err)
	}
	sample.ID = sid

	// Threshold the sample and insert alert rows.
	firings := Threshold(sample)
	var opened []Alert
	for _, f := range firings {
		var aid int64
		if aerr := tx.QueryRowContext(ctx, insertAlertQ,
			sample.ULID, sample.OrgID, f.Signal, f.Severity, f.Detail, ts,
		).Scan(&aid); aerr != nil {
			tx.Rollback() //nolint:errcheck
			s.mu.Unlock()
			return Sample{}, nil, fmt.Errorf("telemetry: insert alert: %w", aerr)
		}
		opened = append(opened, Alert{
			ID:       aid,
			ULID:     sample.ULID,
			OrgID:    sample.OrgID,
			Signal:   f.Signal,
			Severity: f.Severity,
			Detail:   f.Detail,
			FiredAt:  sample.SampledAt,
		})
	}

	if err := tx.Commit(); err != nil {
		s.mu.Unlock()
		return Sample{}, nil, fmt.Errorf("telemetry: insert sample commit: %w", err)
	}
	s.mu.Unlock()

	return sample, opened, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// LatestSample
// ─────────────────────────────────────────────────────────────────────────────

func (s *SQLStore) LatestSample(ctx context.Context, ulid string) (Sample, error) {
	row := s.db.QueryRowContext(ctx,
		s.db.Rebind(`SELECT `+sampleCols+`
		   FROM telemetry_samples WHERE ulid = ?
		   ORDER BY sampled_at DESC LIMIT 1`),
		ulid,
	)
	sample, err := scanSample(row)
	if err == sql.ErrNoRows {
		return Sample{}, ErrSampleNotFound
	}
	if err != nil {
		return Sample{}, fmt.Errorf("telemetry: latest sample: %w", err)
	}
	return sample, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// RecentSamples
// ─────────────────────────────────────────────────────────────────────────────

func (s *SQLStore) RecentSamples(ctx context.Context, ulid string, n int) ([]Sample, error) {
	if n <= 0 {
		n = 20
	}
	rows, err := s.db.QueryContext(ctx,
		s.db.Rebind(`SELECT `+sampleCols+`
		   FROM telemetry_samples WHERE ulid = ?
		   ORDER BY sampled_at DESC LIMIT ?`),
		ulid, n,
	)
	if err != nil {
		return nil, fmt.Errorf("telemetry: recent samples: %w", err)
	}
	defer rows.Close()
	return scanSampleRows(rows)
}

// ─────────────────────────────────────────────────────────────────────────────
// OpenAlerts / OrgOpenAlerts / AllOpenAlerts
// ─────────────────────────────────────────────────────────────────────────────

func (s *SQLStore) OpenAlerts(ctx context.Context, ulid string) ([]Alert, error) {
	rows, err := s.db.QueryContext(ctx,
		s.db.Rebind(`SELECT `+alertCols+`
		   FROM telemetry_alerts WHERE ulid = ? AND resolved_at IS NULL
		   ORDER BY fired_at DESC`),
		ulid,
	)
	if err != nil {
		return nil, fmt.Errorf("telemetry: open alerts: %w", err)
	}
	defer rows.Close()
	return scanAlertRows(rows)
}

func (s *SQLStore) OrgOpenAlerts(ctx context.Context, orgID string) ([]Alert, error) {
	rows, err := s.db.QueryContext(ctx,
		s.db.Rebind(`SELECT `+alertCols+`
		   FROM telemetry_alerts WHERE org_id = ? AND resolved_at IS NULL
		   ORDER BY fired_at DESC`),
		orgID,
	)
	if err != nil {
		return nil, fmt.Errorf("telemetry: org open alerts: %w", err)
	}
	defer rows.Close()
	return scanAlertRows(rows)
}

func (s *SQLStore) AllOpenAlerts(ctx context.Context) ([]Alert, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+alertCols+`
		   FROM telemetry_alerts WHERE resolved_at IS NULL
		   ORDER BY fired_at DESC`,
	)
	if err != nil {
		return nil, fmt.Errorf("telemetry: all open alerts: %w", err)
	}
	defer rows.Close()
	return scanAlertRows(rows)
}

// ─────────────────────────────────────────────────────────────────────────────
// ResolveAlert
// ─────────────────────────────────────────────────────────────────────────────

func (s *SQLStore) ResolveAlert(ctx context.Context, alertID int64, resolvedAt time.Time) error {
	ts := resolvedAt.UTC().Format(time.RFC3339Nano)
	s.mu.Lock()
	res, err := s.db.ExecContext(ctx,
		s.db.Rebind(`UPDATE telemetry_alerts SET resolved_at = ? WHERE id = ?`),
		ts, alertID,
	)
	s.mu.Unlock()
	if err != nil {
		return fmt.Errorf("telemetry: resolve alert: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrAlertNotFound
	}
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Scan helpers
// ─────────────────────────────────────────────────────────────────────────────

const sampleCols = `id, ulid, org_id, sampled_at,
       ip_reputation_score, blocklist_spamhaus, blocklist_barracuda, blocklist_sorbs,
       disk_used_pct, sync_queue_depth, mail_queue_depth,
       dkim_expiry_days, dmarc_pass_rate_pct,
       uptime_sec, last_backup_age_sec`

func scanSample(row interface{ Scan(...any) error }) (Sample, error) {
	var s Sample
	var sampledAt string
	var bSpam, bBarr, bSORBS int
	err := row.Scan(
		&s.ID, &s.ULID, &s.OrgID, &sampledAt,
		&s.IPReputationScore, &bSpam, &bBarr, &bSORBS,
		&s.DiskUsedPct, &s.SyncQueueDepth, &s.MailQueueDepth,
		&s.DKIMExpiryDays, &s.DMARCPassRatePct,
		&s.UptimeSec, &s.LastBackupAgeSec,
	)
	if err != nil {
		return Sample{}, err
	}
	s.SampledAt = parseTime(sampledAt)
	s.BlocklistSpamhaus = bSpam != 0
	s.BlocklistBarracuda = bBarr != 0
	s.BlocklistSORBS = bSORBS != 0
	return s, nil
}

func scanSampleRows(rows *sql.Rows) ([]Sample, error) {
	var out []Sample
	for rows.Next() {
		s, err := scanSample(rows)
		if err != nil {
			return nil, fmt.Errorf("telemetry: scan sample: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

const alertCols = `id, ulid, org_id, signal, severity, detail, fired_at, resolved_at`

func scanAlertRows(rows *sql.Rows) ([]Alert, error) {
	var out []Alert
	for rows.Next() {
		var a Alert
		var firedAt string
		var resolvedAt sql.NullString
		if err := rows.Scan(&a.ID, &a.ULID, &a.OrgID,
			&a.Signal, &a.Severity, &a.Detail,
			&firedAt, &resolvedAt); err != nil {
			return nil, fmt.Errorf("telemetry: scan alert: %w", err)
		}
		a.FiredAt = parseTime(firedAt)
		if resolvedAt.Valid && resolvedAt.String != "" {
			t := parseTime(resolvedAt.String)
			a.ResolvedAt = &t
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ─────────────────────────────────────────────────────────────────────────────
// Time helpers (consistent with fleet/store.go and secx/store.go)
// ─────────────────────────────────────────────────────────────────────────────

func parseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		t, _ = time.Parse(time.RFC3339, s)
	}
	return t.UTC()
}
