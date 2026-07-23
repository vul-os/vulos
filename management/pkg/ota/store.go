package ota

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"sync"
	"time"

	"github.com/vul-os/vulos-management/pkg/cpdb"
)

// SQLStore is the production dual-backend (SQLite + Postgres) implementation
// of Store, backed by a *cpdb.DB.
// Concurrency-safe (WAL mode / connection pool + single writer mutex).
// Behaviour is semantically identical to MemStore.
type SQLStore struct {
	db *cpdb.DB
	mu sync.Mutex // serialises all writes
}

// Open migrates and returns a ready-to-use *SQLStore backed by db.
// db must already be opened via cpdb.Open or cpdb.OpenSQLiteDSN.
func Open(db *cpdb.DB) (*SQLStore, error) {
	subFS, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("ota: migrations sub: %w", err)
	}
	if err := db.Migrate(subFS); err != nil {
		return nil, fmt.Errorf("ota: migrate: %w", err)
	}
	return &SQLStore{db: db}, nil
}

// Close closes the underlying database connection.
func (s *SQLStore) Close() error { return s.db.Close() }

// compile-time assertion.
var _ Store = (*SQLStore)(nil)

// ─────────────────────────────────────────────────────────────────────────────
// InsertRelease
// ─────────────────────────────────────────────────────────────────────────────

func (s *SQLStore) InsertRelease(ctx context.Context, r Release) (int64, error) {
	if r.CreatedAt.IsZero() {
		r.CreatedAt = time.Now().UTC()
	}
	secInt := 0
	if r.Security {
		secInt = 1
	}
	s.mu.Lock()
	if s.db.Backend() == cpdb.BackendPostgres {
		var id int64
		err := s.db.QueryRowContext(ctx,
			s.db.Rebind(`INSERT INTO ota_releases
			   (version, channel, artifact_url, sha256, min_from, security,
			    rollout_pct, signature_b64, defer_max_sec, created_at, halted)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0) RETURNING id`),
			r.Version, r.Channel, r.ArtifactURL, r.Sha256, r.MinFrom, secInt,
			r.RolloutPct, r.SignatureB64, r.DeferMaxSec,
			r.CreatedAt.UTC().Format(time.RFC3339),
		).Scan(&id)
		s.mu.Unlock()
		if err != nil {
			if isUnique(err) {
				return 0, ErrDuplicateRelease
			}
			return 0, fmt.Errorf("ota: insert release: %w", err)
		}
		return id, nil
	}
	res, err := s.db.ExecContext(ctx,
		s.db.Rebind(`INSERT INTO ota_releases
		   (version, channel, artifact_url, sha256, min_from, security,
		    rollout_pct, signature_b64, defer_max_sec, created_at, halted)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0)`),
		r.Version, r.Channel, r.ArtifactURL, r.Sha256, r.MinFrom, secInt,
		r.RolloutPct, r.SignatureB64, r.DeferMaxSec,
		r.CreatedAt.UTC().Format(time.RFC3339),
	)
	s.mu.Unlock()
	if err != nil {
		if isUnique(err) {
			return 0, ErrDuplicateRelease
		}
		return 0, fmt.Errorf("ota: insert release: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("ota: insert release last id: %w", err)
	}
	return id, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// HaltRelease
// ─────────────────────────────────────────────────────────────────────────────

func (s *SQLStore) HaltRelease(ctx context.Context, releaseID int64) error {
	s.mu.Lock()
	res, err := s.db.ExecContext(ctx,
		s.db.Rebind(`UPDATE ota_releases SET halted = 1 WHERE id = ?`), releaseID,
	)
	s.mu.Unlock()
	if err != nil {
		return fmt.Errorf("ota: halt release: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("ota: halt release rows: %w", err)
	}
	if n == 0 {
		return ErrReleaseNotFound
	}
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// GetPolicy / SetPolicy
// ─────────────────────────────────────────────────────────────────────────────

func (s *SQLStore) GetPolicy(ctx context.Context, ulid string) (DevicePolicy, error) {
	var p DevicePolicy
	var pinVersion sql.NullString
	var deferUntil sql.NullString
	var optOut int
	var updatedAt string
	err := s.db.QueryRowContext(ctx,
		s.db.Rebind(`SELECT ulid, channel, pin_version, defer_until, opt_out_features, updated_at
		   FROM ota_device_policy WHERE ulid = ?`), ulid,
	).Scan(&p.ULID, &p.Channel, &pinVersion, &deferUntil, &optOut, &updatedAt)
	if err == sql.ErrNoRows {
		return DevicePolicy{}, ErrUnknownULID
	}
	if err != nil {
		return DevicePolicy{}, fmt.Errorf("ota: get policy: %w", err)
	}
	p.PinVersion = pinVersion.String
	if deferUntil.Valid && deferUntil.String != "" {
		t := parseTime(deferUntil.String)
		p.DeferUntil = &t
	}
	p.OptOutFeatures = optOut != 0
	p.UpdatedAt = parseTime(updatedAt)
	return p, nil
}

func (s *SQLStore) SetPolicy(ctx context.Context, p DevicePolicy) error {
	now := time.Now().UTC().Format(time.RFC3339)
	var pinVersion sql.NullString
	if p.PinVersion != "" {
		pinVersion = sql.NullString{String: p.PinVersion, Valid: true}
	}
	var deferUntil sql.NullString
	if p.DeferUntil != nil {
		deferUntil = sql.NullString{String: p.DeferUntil.UTC().Format(time.RFC3339), Valid: true}
	}
	optOut := 0
	if p.OptOutFeatures {
		optOut = 1
	}
	s.mu.Lock()
	_, err := s.db.ExecContext(ctx,
		s.db.Rebind(`INSERT INTO ota_device_policy
		   (ulid, channel, pin_version, defer_until, opt_out_features, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT(ulid) DO UPDATE SET
		   channel         = excluded.channel,
		   pin_version     = excluded.pin_version,
		   defer_until     = excluded.defer_until,
		   opt_out_features = excluded.opt_out_features,
		   updated_at      = excluded.updated_at`),
		p.ULID, p.Channel, pinVersion, deferUntil, optOut, now,
	)
	s.mu.Unlock()
	if err != nil {
		return fmt.Errorf("ota: set policy: %w", err)
	}
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// FeedFor
// ─────────────────────────────────────────────────────────────────────────────

// FeedFor implements the upgrade-eligibility logic on the SQL store.
// We load all non-halted releases for the channel into memory (releases are
// few; this keeps the logic identical to MemStore and avoids complex SQL).
func (s *SQLStore) FeedFor(ctx context.Context, ulid, channel, currentVersion string) (Release, error) {
	// Resolve device policy (missing ULID → default policy).
	policy, err := s.GetPolicy(ctx, ulid)
	if err != nil {
		// ErrUnknownULID → use defaults.
		policy = DevicePolicy{
			ULID:    ulid,
			Channel: "stable",
		}
	}

	effectiveChannel := channel
	if effectiveChannel == "" {
		effectiveChannel = policy.Channel
	}

	rows, err := s.db.QueryContext(ctx,
		s.db.Rebind(`SELECT id, version, channel, artifact_url, sha256, min_from, security,
		        rollout_pct, signature_b64, defer_max_sec, created_at, halted
		   FROM ota_releases
		  WHERE halted = 0 AND channel = ?`),
		effectiveChannel,
	)
	if err != nil {
		return Release{}, fmt.Errorf("ota: feed query: %w", err)
	}
	defer rows.Close()

	var releases []Release
	for rows.Next() {
		var r Release
		var secInt, haltedInt int
		var createdAt string
		if err := rows.Scan(
			&r.ID, &r.Version, &r.Channel, &r.ArtifactURL, &r.Sha256, &r.MinFrom,
			&secInt, &r.RolloutPct, &r.SignatureB64, &r.DeferMaxSec, &createdAt, &haltedInt,
		); err != nil {
			return Release{}, fmt.Errorf("ota: feed scan: %w", err)
		}
		r.Security = secInt != 0
		r.Halted = haltedInt != 0
		r.CreatedAt = parseTime(createdAt)
		releases = append(releases, r)
	}
	if err := rows.Err(); err != nil {
		return Release{}, fmt.Errorf("ota: feed rows: %w", err)
	}

	now := time.Now().UTC()

	// Apply the same eligibility logic as MemStore.
	var candidates []Release
	for _, r := range releases {
		if !semverGT(r.Version, currentVersion) {
			continue
		}
		if !semverGTE(currentVersion, r.MinFrom) {
			continue
		}
		if int(CohortHash(ulid, r.Version)) >= r.RolloutPct {
			continue
		}
		if !r.Security {
			if policy.PinVersion != "" && policy.PinVersion != r.Version {
				continue
			}
			if policy.DeferUntil != nil && now.Before(*policy.DeferUntil) {
				continue
			}
			if policy.OptOutFeatures {
				continue
			}
		}
		candidates = append(candidates, r)
	}

	if len(candidates) == 0 {
		return Release{}, ErrNoUpgrade
	}

	// Sort descending by version — pick highest applicable.
	best := candidates[0]
	for _, c := range candidates[1:] {
		if semverGT(c.Version, best.Version) {
			best = c
		}
	}
	return best, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// InsertReport
// ─────────────────────────────────────────────────────────────────────────────

func (s *SQLStore) InsertReport(ctx context.Context, ulid, version, result string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	s.mu.Lock()
	_, err := s.db.ExecContext(ctx,
		s.db.Rebind(`INSERT INTO ota_device_reports (ulid, version, result, received_at)
		 VALUES (?, ?, ?, ?)`),
		ulid, version, result, now,
	)
	s.mu.Unlock()
	if err != nil {
		return fmt.Errorf("ota: insert report: %w", err)
	}
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// ListReleases
// ─────────────────────────────────────────────────────────────────────────────

// ListReleases returns all releases ordered by id descending with pagination.
func (s *SQLStore) ListReleases(ctx context.Context, limit, offset int) ([]Release, error) {
	limit = clampLimit(limit)
	rows, err := s.db.QueryContext(ctx,
		s.db.Rebind(`SELECT id, version, channel, artifact_url, sha256, min_from, security,
		        rollout_pct, signature_b64, defer_max_sec, created_at, halted
		   FROM ota_releases
		  ORDER BY id DESC
		  LIMIT ? OFFSET ?`),
		limit, offset,
	)
	if err != nil {
		return nil, fmt.Errorf("ota: list releases: %w", err)
	}
	defer rows.Close()

	var out []Release
	for rows.Next() {
		var r Release
		var secInt, haltedInt int
		var createdAt string
		if err := rows.Scan(
			&r.ID, &r.Version, &r.Channel, &r.ArtifactURL, &r.Sha256, &r.MinFrom,
			&secInt, &r.RolloutPct, &r.SignatureB64, &r.DeferMaxSec, &createdAt, &haltedInt,
		); err != nil {
			return nil, fmt.Errorf("ota: list releases scan: %w", err)
		}
		r.Security = secInt != 0
		r.Halted = haltedInt != 0
		r.CreatedAt = parseTime(createdAt)
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("ota: list releases rows: %w", err)
	}
	if out == nil {
		return []Release{}, nil
	}
	return out, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// AdoptionCounts
// ─────────────────────────────────────────────────────────────────────────────

// AdoptionCounts returns adoption breakdown for a release.
// Cross-store note: fleet_devices lives in a separate database; a SQL
// JOIN is not possible. total_devices and in_cohort are therefore derived from
// ota_device_reports (distinct ULIDs that have submitted a report for the
// release version). The fleet.Store can be used by the HTTP handler layer to
// supplement total_devices with the global fleet count if required.
func (s *SQLStore) AdoptionCounts(ctx context.Context, releaseID int64) (Adoption, error) {
	// Look up the release to get version and rollout_pct.
	var rel Release
	var secInt, haltedInt int
	var createdAt string
	err := s.db.QueryRowContext(ctx,
		s.db.Rebind(`SELECT id, version, channel, artifact_url, sha256, min_from, security,
		        rollout_pct, signature_b64, defer_max_sec, created_at, halted
		   FROM ota_releases WHERE id = ?`), releaseID,
	).Scan(
		&rel.ID, &rel.Version, &rel.Channel, &rel.ArtifactURL, &rel.Sha256, &rel.MinFrom,
		&secInt, &rel.RolloutPct, &rel.SignatureB64, &rel.DeferMaxSec, &createdAt, &haltedInt,
	)
	if err == sql.ErrNoRows {
		return Adoption{}, ErrReleaseNotFound
	}
	if err != nil {
		return Adoption{}, fmt.Errorf("ota: adoption counts fetch release: %w", err)
	}
	rel.Security = secInt != 0
	rel.Halted = haltedInt != 0
	rel.CreatedAt = parseTime(createdAt)

	// Fetch per-result counts from ota_device_reports.
	rows, err := s.db.QueryContext(ctx,
		s.db.Rebind(`SELECT ulid, result FROM ota_device_reports WHERE version = ?`),
		rel.Version,
	)
	if err != nil {
		return Adoption{}, fmt.Errorf("ota: adoption counts query: %w", err)
	}
	defer rows.Close()

	// Track latest result per ULID (same logic as MemStore: last write wins;
	// since ORDER BY is not guaranteed without it, we collect all rows and pick
	// the last-inserted result per ULID using a map).
	latestResult := map[string]string{} // ulid -> result (last row wins per insert order)
	for rows.Next() {
		var ulid, result string
		if err := rows.Scan(&ulid, &result); err != nil {
			return Adoption{}, fmt.Errorf("ota: adoption counts scan: %w", err)
		}
		latestResult[ulid] = result
	}
	if err := rows.Err(); err != nil {
		return Adoption{}, fmt.Errorf("ota: adoption counts rows: %w", err)
	}

	var a Adoption
	a.TotalDevices = len(latestResult)
	for ulid, result := range latestResult {
		if int(CohortHash(ulid, rel.Version)) < rel.RolloutPct {
			a.InCohort++
		}
		switch result {
		case "applied":
			a.Applied++
		case "failed":
			a.Failed++
		case "rolled-back":
			a.RolledBack++
		}
	}
	return a, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

func parseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		t, _ = time.Parse(time.RFC3339, s)
	}
	return t
}

func isUnique(err error) bool {
	if err == nil {
		return false
	}
	return contains(err.Error(), "UNIQUE") || contains(err.Error(), "unique")
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && containsStr(s, sub))
}

func containsStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
