// Package status is the durable backing for the control-plane status page.
//
// The aggregator (cmd/server/routes_status.go) polls each component and, until
// now, kept only an in-memory ring of ~256 samples — about four hours at the 60s
// poll — that vanished on every restart, while still publishing a "7-day uptime"
// it had no data for. This package persists the samples, rolls them up per day so
// a 30/90-day query never scans the raw table, and holds operator-authored
// incidents and maintenance windows (the old incidents were auto-derived from
// status flips and did not survive a restart).
//
// A Store interface with a cpdb-backed SQLStore and an in-memory MemStore mirrors
// internal/customdomain: the MemStore keeps the pre-persistence behaviour available
// as a fallback when the DB cannot be opened, so a status-store outage degrades the
// status page rather than taking the CP down with it.
package status

import (
	"context"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/vul-os/vulos-management/pkg/cpdb"
)

// Sample is one component health reading.
type Sample struct {
	Component string
	CheckedAt time.Time
	Status    string // operational | degraded | down | unknown
	LatencyMS int
}

// Uptime is the up-fraction of a component over a window, plus the sample count it
// is derived from. Known is false when there is no data at all in the window — the
// caller must render "no data", never a fabricated 100%.
type Uptime struct {
	Component string
	Pct       float64
	Samples   int
	Known     bool
}

// Incident is an operator-authored status incident.
type Incident struct {
	ID         string
	Title      string
	Body       string
	Severity   string
	Components []string
	StartedAt  time.Time
	ResolvedAt *time.Time
	CreatedAt  time.Time
	UpdatedAt  time.Time
	Updates    []IncidentUpdate
}

// IncidentUpdate is one post on an incident's timeline.
type IncidentUpdate struct {
	ID         string
	IncidentID string
	Status     string
	Body       string
	PostedAt   time.Time
}

// Maintenance is a scheduled maintenance window.
type Maintenance struct {
	ID         string
	Title      string
	Body       string
	Components []string
	StartsAt   time.Time
	EndsAt     time.Time
	CreatedAt  time.Time
}

// Store is the durable status backing. All methods are safe for concurrent use.
type Store interface {
	// RecordSamples persists one poll cycle's samples and folds them into the daily
	// rollup, in a single transaction (one write per cycle, not per component).
	RecordSamples(ctx context.Context, samples []Sample) error
	// UptimeSince returns the up-fraction per component over [now-window, now],
	// computed from the daily rollup so a long window never scans raw samples.
	UptimeSince(ctx context.Context, components []string, window time.Duration, now time.Time) (map[string]Uptime, error)
	// RecentSamples returns up to limit newest raw samples for a component, oldest
	// first (for the fine-grained recent history bar).
	RecentSamples(ctx context.Context, component string, limit int) ([]Sample, error)
	// PurgeSamplesBefore deletes raw samples older than cutoff (the rollup keeps the
	// long-term history). Returns rows removed.
	PurgeSamplesBefore(ctx context.Context, cutoff time.Time) (int64, error)

	CreateIncident(ctx context.Context, in Incident) (Incident, error)
	AddIncidentUpdate(ctx context.Context, u IncidentUpdate) error
	ResolveIncident(ctx context.Context, id string, at time.Time) error
	ListIncidents(ctx context.Context, limit int) ([]Incident, error)

	ScheduleMaintenance(ctx context.Context, m Maintenance) (Maintenance, error)
	ActiveMaintenance(ctx context.Context, now time.Time) ([]Maintenance, error)

	Close() error
}

const dayFmt = "2006-01-02"

func countsFor(status string) (up, total, down int) {
	switch status {
	case "unknown":
		return 0, 0, 0 // neither up nor down
	case "down":
		return 0, 1, 1
	default: // operational, degraded → up
		return 1, 1, 0
	}
}

func splitComponents(csv string) []string {
	if csv == "" {
		return nil
	}
	return strings.Split(csv, ",")
}

// ─── cpdb-backed store ──────────────────────────────────────────────────────

// SQLStore is the cpdb-backed Store.
type SQLStore struct {
	db *cpdb.DB
}

// OpenSQLStore opens the status store and runs the embedded migration idempotently.
func OpenSQLStore(db *cpdb.DB) (*SQLStore, error) {
	subFS, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("status: embed sub: %w", err)
	}
	if err := db.Migrate(subFS); err != nil {
		return nil, fmt.Errorf("status: migrate: %w", err)
	}
	return &SQLStore{db: db}, nil
}

func (s *SQLStore) Close() error { return s.db.Close() }

func (s *SQLStore) RecordSamples(ctx context.Context, samples []Sample) error {
	if len(samples) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("status: begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck — no-op after Commit

	insSample := s.db.Rebind(`INSERT INTO status_samples (component, checked_at, status, latency_ms)
		VALUES (?, ?, ?, ?) ON CONFLICT (component, checked_at) DO NOTHING`)
	// Additive rollup upsert: fold this sample's counts into the component's day row.
	upsertDay := s.db.Rebind(`INSERT INTO status_daily (component, day, up_count, total_count, down_count)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (component, day) DO UPDATE SET
			up_count    = status_daily.up_count    + EXCLUDED.up_count,
			total_count = status_daily.total_count + EXCLUDED.total_count,
			down_count  = status_daily.down_count  + EXCLUDED.down_count`)

	for _, sm := range samples {
		at := sm.CheckedAt.UTC()
		res, err := tx.ExecContext(ctx, insSample, sm.Component, at.Format(time.RFC3339), sm.Status, sm.LatencyMS)
		if err != nil {
			return fmt.Errorf("status: insert sample: %w", err)
		}
		// Only fold into the daily rollup when the sample was actually NEW — a
		// duplicate (component, checked_at) is a no-op insert, and counting it
		// again would inflate uptime on a re-recorded poll.
		if n, _ := res.RowsAffected(); n == 0 {
			continue
		}
		up, total, down := countsFor(sm.Status)
		if total == 0 {
			continue // unknown: recorded as a raw sample, but not counted in uptime
		}
		if _, err := tx.ExecContext(ctx, upsertDay, sm.Component, at.Format(dayFmt), up, total, down); err != nil {
			return fmt.Errorf("status: upsert daily: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("status: commit: %w", err)
	}
	return nil
}

func (s *SQLStore) UptimeSince(ctx context.Context, components []string, window time.Duration, now time.Time) (map[string]Uptime, error) {
	cutoff := now.UTC().Add(-window).Format(dayFmt)
	out := make(map[string]Uptime, len(components))
	q := s.db.Rebind(`SELECT COALESCE(SUM(up_count),0), COALESCE(SUM(total_count),0)
		FROM status_daily WHERE component = ? AND day >= ?`)
	for _, c := range components {
		var up, total int
		if err := s.db.QueryRowContext(ctx, q, c, cutoff).Scan(&up, &total); err != nil {
			return nil, fmt.Errorf("status: uptime %s: %w", c, err)
		}
		u := Uptime{Component: c, Samples: total}
		if total > 0 {
			u.Known = true
			u.Pct = float64(up) / float64(total) * 100
		}
		out[c] = u
	}
	return out, nil
}

func (s *SQLStore) RecentSamples(ctx context.Context, component string, limit int) ([]Sample, error) {
	if limit <= 0 {
		limit = 256
	}
	// Newest `limit` rows, then reverse to oldest-first for the history bar.
	q := s.db.Rebind(`SELECT checked_at, status, latency_ms FROM status_samples
		WHERE component = ? ORDER BY checked_at DESC LIMIT ?`)
	rows, err := s.db.QueryContext(ctx, q, component, limit)
	if err != nil {
		return nil, fmt.Errorf("status: recent samples: %w", err)
	}
	defer rows.Close()
	var out []Sample
	for rows.Next() {
		var at, st string
		var lat int
		if err := rows.Scan(&at, &st, &lat); err != nil {
			return nil, fmt.Errorf("status: scan sample: %w", err)
		}
		t, _ := time.Parse(time.RFC3339, at)
		out = append(out, Sample{Component: component, CheckedAt: t, Status: st, LatencyMS: lat})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CheckedAt.Before(out[j].CheckedAt) })
	return out, nil
}

func (s *SQLStore) PurgeSamplesBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		s.db.Rebind(`DELETE FROM status_samples WHERE checked_at < ?`),
		cutoff.UTC().Format(time.RFC3339))
	if err != nil {
		return 0, fmt.Errorf("status: purge samples: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

func (s *SQLStore) CreateIncident(ctx context.Context, in Incident) (Incident, error) {
	now := time.Now().UTC()
	if in.CreatedAt.IsZero() {
		in.CreatedAt = now
	}
	in.UpdatedAt = in.CreatedAt
	if in.StartedAt.IsZero() {
		in.StartedAt = in.CreatedAt
	}
	if in.Severity == "" {
		in.Severity = "minor"
	}
	_, err := s.db.ExecContext(ctx, s.db.Rebind(
		`INSERT INTO status_incidents (id, title, body, severity, components, started_at, resolved_at, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`),
		in.ID, in.Title, in.Body, in.Severity, strings.Join(in.Components, ","),
		in.StartedAt.Format(time.RFC3339), nullTime(in.ResolvedAt),
		in.CreatedAt.Format(time.RFC3339), in.UpdatedAt.Format(time.RFC3339))
	if err != nil {
		return Incident{}, fmt.Errorf("status: create incident: %w", err)
	}
	return in, nil
}

func (s *SQLStore) AddIncidentUpdate(ctx context.Context, u IncidentUpdate) error {
	if u.PostedAt.IsZero() {
		u.PostedAt = time.Now().UTC()
	}
	if u.Status == "" {
		u.Status = "update"
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("status: begin update: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck
	if _, err := tx.ExecContext(ctx, s.db.Rebind(
		`INSERT INTO status_incident_updates (id, incident_id, status, body, posted_at) VALUES (?, ?, ?, ?, ?)`),
		u.ID, u.IncidentID, u.Status, u.Body, u.PostedAt.Format(time.RFC3339)); err != nil {
		return fmt.Errorf("status: insert update: %w", err)
	}
	if _, err := tx.ExecContext(ctx, s.db.Rebind(
		`UPDATE status_incidents SET updated_at = ? WHERE id = ?`),
		u.PostedAt.Format(time.RFC3339), u.IncidentID); err != nil {
		return fmt.Errorf("status: touch incident: %w", err)
	}
	return tx.Commit()
}

func (s *SQLStore) ResolveIncident(ctx context.Context, id string, at time.Time) error {
	at = at.UTC()
	_, err := s.db.ExecContext(ctx, s.db.Rebind(
		`UPDATE status_incidents SET resolved_at = ?, updated_at = ? WHERE id = ?`),
		at.Format(time.RFC3339), at.Format(time.RFC3339), id)
	if err != nil {
		return fmt.Errorf("status: resolve incident: %w", err)
	}
	return nil
}

func (s *SQLStore) ListIncidents(ctx context.Context, limit int) ([]Incident, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, s.db.Rebind(
		`SELECT id, title, body, severity, components, started_at, resolved_at, created_at, updated_at
		 FROM status_incidents ORDER BY started_at DESC LIMIT ?`), limit)
	if err != nil {
		return nil, fmt.Errorf("status: list incidents: %w", err)
	}
	defer rows.Close()
	var out []Incident
	for rows.Next() {
		var in Incident
		var comps, started, created, updated string
		var resolved *string
		if err := rows.Scan(&in.ID, &in.Title, &in.Body, &in.Severity, &comps, &started, &resolved, &created, &updated); err != nil {
			return nil, fmt.Errorf("status: scan incident: %w", err)
		}
		in.Components = splitComponents(comps)
		in.StartedAt, _ = time.Parse(time.RFC3339, started)
		in.CreatedAt, _ = time.Parse(time.RFC3339, created)
		in.UpdatedAt, _ = time.Parse(time.RFC3339, updated)
		if resolved != nil && *resolved != "" {
			if t, err := time.Parse(time.RFC3339, *resolved); err == nil {
				in.ResolvedAt = &t
			}
		}
		out = append(out, in)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		ups, err := s.incidentUpdates(ctx, out[i].ID)
		if err != nil {
			return nil, err
		}
		out[i].Updates = ups
	}
	return out, nil
}

func (s *SQLStore) incidentUpdates(ctx context.Context, incidentID string) ([]IncidentUpdate, error) {
	rows, err := s.db.QueryContext(ctx, s.db.Rebind(
		`SELECT id, incident_id, status, body, posted_at FROM status_incident_updates
		 WHERE incident_id = ? ORDER BY posted_at ASC`), incidentID)
	if err != nil {
		return nil, fmt.Errorf("status: incident updates: %w", err)
	}
	defer rows.Close()
	var out []IncidentUpdate
	for rows.Next() {
		var u IncidentUpdate
		var posted string
		if err := rows.Scan(&u.ID, &u.IncidentID, &u.Status, &u.Body, &posted); err != nil {
			return nil, fmt.Errorf("status: scan update: %w", err)
		}
		u.PostedAt, _ = time.Parse(time.RFC3339, posted)
		out = append(out, u)
	}
	return out, rows.Err()
}

func (s *SQLStore) ScheduleMaintenance(ctx context.Context, m Maintenance) (Maintenance, error) {
	if m.CreatedAt.IsZero() {
		m.CreatedAt = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, s.db.Rebind(
		`INSERT INTO status_maintenance (id, title, body, components, starts_at, ends_at, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`),
		m.ID, m.Title, m.Body, strings.Join(m.Components, ","),
		m.StartsAt.UTC().Format(time.RFC3339), m.EndsAt.UTC().Format(time.RFC3339),
		m.CreatedAt.Format(time.RFC3339))
	if err != nil {
		return Maintenance{}, fmt.Errorf("status: schedule maintenance: %w", err)
	}
	return m, nil
}

func (s *SQLStore) ActiveMaintenance(ctx context.Context, now time.Time) ([]Maintenance, error) {
	n := now.UTC().Format(time.RFC3339)
	rows, err := s.db.QueryContext(ctx, s.db.Rebind(
		`SELECT id, title, body, components, starts_at, ends_at, created_at FROM status_maintenance
		 WHERE starts_at <= ? AND ends_at >= ? ORDER BY starts_at ASC`), n, n)
	if err != nil {
		return nil, fmt.Errorf("status: active maintenance: %w", err)
	}
	defer rows.Close()
	var out []Maintenance
	for rows.Next() {
		var m Maintenance
		var comps, starts, ends, created string
		if err := rows.Scan(&m.ID, &m.Title, &m.Body, &comps, &starts, &ends, &created); err != nil {
			return nil, fmt.Errorf("status: scan maintenance: %w", err)
		}
		m.Components = splitComponents(comps)
		m.StartsAt, _ = time.Parse(time.RFC3339, starts)
		m.EndsAt, _ = time.Parse(time.RFC3339, ends)
		m.CreatedAt, _ = time.Parse(time.RFC3339, created)
		out = append(out, m)
	}
	return out, rows.Err()
}

func nullTime(t *time.Time) any {
	if t == nil || t.IsZero() {
		return nil
	}
	return t.UTC().Format(time.RFC3339)
}

// ─── in-memory fallback ─────────────────────────────────────────────────────

// MemStore is the in-process Store used when the cpdb store cannot be opened. It
// keeps the pre-persistence behaviour (bounded recent samples, in-memory incidents)
// so a status-DB outage degrades the status page rather than the whole CP.
type MemStore struct {
	mu          sync.Mutex
	samples     map[string][]Sample
	daily       map[string]map[string][3]int // component -> day -> {up,total,down}
	incidents   []Incident
	maintenance []Maintenance
	cap         int
}

func NewMemStore() *MemStore {
	return &MemStore{
		samples: map[string][]Sample{},
		daily:   map[string]map[string][3]int{},
		cap:     256,
	}
}

func (m *MemStore) Close() error { return nil }

func (m *MemStore) RecordSamples(_ context.Context, samples []Sample) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, s := range samples {
		at := s.CheckedAt.UTC()
		s.CheckedAt = at
		lst := append(m.samples[s.Component], s)
		if len(lst) > m.cap {
			lst = lst[len(lst)-m.cap:]
		}
		m.samples[s.Component] = lst
		up, total, down := countsFor(s.Status)
		if total == 0 {
			continue
		}
		day := at.Format(dayFmt)
		if m.daily[s.Component] == nil {
			m.daily[s.Component] = map[string][3]int{}
		}
		c := m.daily[s.Component][day]
		m.daily[s.Component][day] = [3]int{c[0] + up, c[1] + total, c[2] + down}
	}
	return nil
}

func (m *MemStore) UptimeSince(_ context.Context, components []string, window time.Duration, now time.Time) (map[string]Uptime, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cutoff := now.UTC().Add(-window).Format(dayFmt)
	out := make(map[string]Uptime, len(components))
	for _, c := range components {
		var up, total int
		for day, v := range m.daily[c] {
			if day >= cutoff {
				up += v[0]
				total += v[1]
			}
		}
		u := Uptime{Component: c, Samples: total}
		if total > 0 {
			u.Known = true
			u.Pct = float64(up) / float64(total) * 100
		}
		out[c] = u
	}
	return out, nil
}

func (m *MemStore) RecentSamples(_ context.Context, component string, limit int) ([]Sample, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	lst := m.samples[component]
	if limit > 0 && len(lst) > limit {
		lst = lst[len(lst)-limit:]
	}
	return append([]Sample(nil), lst...), nil
}

func (m *MemStore) PurgeSamplesBefore(_ context.Context, cutoff time.Time) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var removed int64
	for comp, lst := range m.samples {
		kept := lst[:0]
		for _, s := range lst {
			if s.CheckedAt.Before(cutoff) {
				removed++
				continue
			}
			kept = append(kept, s)
		}
		m.samples[comp] = kept
	}
	return removed, nil
}

func (m *MemStore) CreateIncident(_ context.Context, in Incident) (Incident, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now().UTC()
	if in.CreatedAt.IsZero() {
		in.CreatedAt = now
	}
	in.UpdatedAt = in.CreatedAt
	if in.StartedAt.IsZero() {
		in.StartedAt = in.CreatedAt
	}
	if in.Severity == "" {
		in.Severity = "minor"
	}
	m.incidents = append([]Incident{in}, m.incidents...)
	return in, nil
}

func (m *MemStore) AddIncidentUpdate(_ context.Context, u IncidentUpdate) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if u.PostedAt.IsZero() {
		u.PostedAt = time.Now().UTC()
	}
	for i := range m.incidents {
		if m.incidents[i].ID == u.IncidentID {
			m.incidents[i].Updates = append(m.incidents[i].Updates, u)
			m.incidents[i].UpdatedAt = u.PostedAt
			return nil
		}
	}
	return nil
}

func (m *MemStore) ResolveIncident(_ context.Context, id string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	at = at.UTC()
	for i := range m.incidents {
		if m.incidents[i].ID == id {
			m.incidents[i].ResolvedAt = &at
			m.incidents[i].UpdatedAt = at
			return nil
		}
	}
	return nil
}

func (m *MemStore) ListIncidents(_ context.Context, limit int) ([]Incident, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if limit <= 0 || limit > len(m.incidents) {
		limit = len(m.incidents)
	}
	return append([]Incident(nil), m.incidents[:limit]...), nil
}

func (m *MemStore) ScheduleMaintenance(_ context.Context, mw Maintenance) (Maintenance, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if mw.CreatedAt.IsZero() {
		mw.CreatedAt = time.Now().UTC()
	}
	m.maintenance = append(m.maintenance, mw)
	return mw, nil
}

func (m *MemStore) ActiveMaintenance(_ context.Context, now time.Time) ([]Maintenance, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now = now.UTC()
	var out []Maintenance
	for _, mw := range m.maintenance {
		if !mw.StartsAt.After(now) && !mw.EndsAt.Before(now) {
			out = append(out, mw)
		}
	}
	return out, nil
}

var _ Store = (*SQLStore)(nil)
var _ Store = (*MemStore)(nil)
