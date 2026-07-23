// Package telemetry is the SUPPORT-01 root-cause telemetry catalog for the
// vulos.cloud control-plane.
//
// The OS-side agent (vulos OSS) pushes health samples; this package aggregates
// them per-tenant, thresholds each signal, and writes alert rows that feed
// SUPPORT-02 (auto-remediation) and SUPPORT-04 (per-customer status page).
//
// Signals collected:
//   - IP reputation score (0–100)
//   - Disk fill percentage (0–100)
//   - Sync-queue depth (pending items)
//   - Mail-queue depth (pending outbound messages)
//   - DKIM key expiry (days remaining)
//   - DMARC pass rate (0–100 %)
//   - Blocklist status (Spamhaus / Barracuda / SORBS)
//   - Instance uptime (seconds)
//   - Last backup age (seconds since last successful backup)
//
// The frozen contract below (Store interface + MemStore reference) is what
// handlers and tests depend on.  The SQLite-backed SQLStore is the production
// implementation.
package telemetry

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// Sentinel errors
// ─────────────────────────────────────────────────────────────────────────────

var (
	ErrSampleNotFound = errors.New("telemetry: sample not found")
	ErrAlertNotFound  = errors.New("telemetry: alert not found")
)

// ─────────────────────────────────────────────────────────────────────────────
// Core types
// ─────────────────────────────────────────────────────────────────────────────

// Sample is one inbound telemetry push from a device (OS-side agent).
// All integer fields use int so the JSON surface is simple; percentages are
// 0–100, durations are in seconds.
type Sample struct {
	ID    int64
	ULID  string
	OrgID string

	SampledAt time.Time

	// Network / reputation
	IPReputationScore  int // 0–100; lower = worse
	BlocklistSpamhaus  bool
	BlocklistBarracuda bool
	BlocklistSORBS     bool

	// Storage
	DiskUsedPct int // 0–100

	// Queue depths
	SyncQueueDepth int // pending sync items
	MailQueueDepth int // pending outbound mail messages

	// Mail health
	DKIMExpiryDays   int // days until active DKIM key expires
	DMARCPassRatePct int // 0–100 rolling pass rate

	// Instance
	UptimeSec        int
	LastBackupAgeSec int // seconds since last successful backup; 0 = never backed up
}

// Alert is a fired threshold alert.  A nil ResolvedAt means the alert is
// still open.
type Alert struct {
	ID         int64
	ULID       string
	OrgID      string
	Signal     string // e.g. "blocklist_spamhaus"
	Severity   string // "warn" | "critical"
	Detail     string
	FiredAt    time.Time
	ResolvedAt *time.Time // nil = still open
}

// ─────────────────────────────────────────────────────────────────────────────
// Thresholds — each signal has warn and critical levels.
// Callers should use Threshold() to evaluate; constants are exported so
// dashboards and tests can reference them.
// ─────────────────────────────────────────────────────────────────────────────

const (
	// IP reputation: 100 = clean; 0 = fully flagged.
	IPRepWarnThreshold     = 70 // score < 70 → warn
	IPRepCriticalThreshold = 40 // score < 40 → critical

	// Disk fill.
	DiskWarnPct     = 80 // > 80% → warn
	DiskCriticalPct = 95 // > 95% → critical

	// Queue depths.
	SyncQueueWarn     = 500  // > 500 items → warn
	SyncQueueCritical = 2000 // > 2 000 items → critical
	MailQueueWarn     = 200  // > 200 messages → warn
	MailQueueCritical = 1000 // > 1 000 messages → critical

	// DKIM expiry (days remaining).
	DKIMExpiryWarnDays     = 14 // ≤ 14 days → warn
	DKIMExpiryCriticalDays = 3  // ≤ 3 days → critical

	// DMARC pass rate.
	DMARCWarnPct     = 90 // < 90% → warn
	DMARCCriticalPct = 75 // < 75% → critical

	// Last backup age (seconds).
	BackupWarnAgeSec     = 86400 * 2 // > 2 days → warn
	BackupCriticalAgeSec = 86400 * 7 // > 7 days → critical

	// Blocklists: any listing is critical (fires pre-symptom).
	// (No "warn" level — being on a blocklist is immediately actionable.)
)

// Firing represents a threshold crossing for a single signal in one sample.
type Firing struct {
	Signal   string
	Severity string // "warn" | "critical"
	Detail   string
}

// Threshold evaluates all signals in a Sample and returns every threshold
// crossing.  It is a pure function (no I/O, no clock).
func Threshold(s Sample) []Firing {
	var out []Firing

	// Blocklists — critical on any listing.
	if s.BlocklistSpamhaus {
		out = append(out, Firing{"blocklist_spamhaus", "critical", "IP listed on Spamhaus"})
	}
	if s.BlocklistBarracuda {
		out = append(out, Firing{"blocklist_barracuda", "critical", "IP listed on Barracuda"})
	}
	if s.BlocklistSORBS {
		out = append(out, Firing{"blocklist_sorbs", "critical", "IP listed on SORBS"})
	}

	// IP reputation score.
	if s.IPReputationScore < IPRepCriticalThreshold {
		out = append(out, Firing{"ip_reputation", "critical",
			formatInt("IP reputation score critical: ", s.IPReputationScore, "/100")})
	} else if s.IPReputationScore < IPRepWarnThreshold {
		out = append(out, Firing{"ip_reputation", "warn",
			formatInt("IP reputation score low: ", s.IPReputationScore, "/100")})
	}

	// Disk fill.
	if s.DiskUsedPct > DiskCriticalPct {
		out = append(out, Firing{"disk_fill", "critical",
			formatInt("Disk usage critical: ", s.DiskUsedPct, "%")})
	} else if s.DiskUsedPct > DiskWarnPct {
		out = append(out, Firing{"disk_fill", "warn",
			formatInt("Disk usage high: ", s.DiskUsedPct, "%")})
	}

	// Sync-queue depth.
	if s.SyncQueueDepth > SyncQueueCritical {
		out = append(out, Firing{"sync_queue_depth", "critical",
			formatInt("Sync queue depth critical: ", s.SyncQueueDepth, " items")})
	} else if s.SyncQueueDepth > SyncQueueWarn {
		out = append(out, Firing{"sync_queue_depth", "warn",
			formatInt("Sync queue depth high: ", s.SyncQueueDepth, " items")})
	}

	// Mail-queue depth.
	if s.MailQueueDepth > MailQueueCritical {
		out = append(out, Firing{"mail_queue_depth", "critical",
			formatInt("Mail queue depth critical: ", s.MailQueueDepth, " msgs")})
	} else if s.MailQueueDepth > MailQueueWarn {
		out = append(out, Firing{"mail_queue_depth", "warn",
			formatInt("Mail queue depth high: ", s.MailQueueDepth, " msgs")})
	}

	// DKIM expiry.
	if s.DKIMExpiryDays <= DKIMExpiryCriticalDays {
		out = append(out, Firing{"dkim_expiry", "critical",
			formatInt("DKIM key expires in ", s.DKIMExpiryDays, " day(s)")})
	} else if s.DKIMExpiryDays <= DKIMExpiryWarnDays {
		out = append(out, Firing{"dkim_expiry", "warn",
			formatInt("DKIM key expires in ", s.DKIMExpiryDays, " day(s)")})
	}

	// DMARC pass rate.
	if s.DMARCPassRatePct < DMARCCriticalPct {
		out = append(out, Firing{"dmarc_pass_rate", "critical",
			formatInt("DMARC pass rate critical: ", s.DMARCPassRatePct, "%")})
	} else if s.DMARCPassRatePct < DMARCWarnPct {
		out = append(out, Firing{"dmarc_pass_rate", "warn",
			formatInt("DMARC pass rate low: ", s.DMARCPassRatePct, "%")})
	}

	// Last backup age (only when UptimeSec indicates the instance has been up
	// long enough that a backup should have run).
	if s.UptimeSec >= BackupWarnAgeSec {
		if s.LastBackupAgeSec > BackupCriticalAgeSec {
			out = append(out, Firing{"last_backup_age", "critical",
				formatInt("No backup in over ", s.LastBackupAgeSec/86400, " day(s)")})
		} else if s.LastBackupAgeSec > BackupWarnAgeSec {
			out = append(out, Firing{"last_backup_age", "warn",
				formatInt("Backup overdue: ", s.LastBackupAgeSec/86400, " day(s) since last backup")})
		}
	}

	return out
}

// formatInt builds a simple string: prefix + n + suffix without importing fmt.
func formatInt(prefix string, n int, suffix string) string {
	return prefix + itoa(n) + suffix
}

// itoa converts an integer to its decimal string representation.
// Used to avoid a fmt import in the pure-functions file.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := false
	if n < 0 {
		neg = true
		n = -n
	}
	buf := [20]byte{}
	pos := len(buf)
	for n > 0 {
		pos--
		buf[pos] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}

// ─────────────────────────────────────────────────────────────────────────────
// Store interface — frozen contract
// ─────────────────────────────────────────────────────────────────────────────

// Store is the telemetry persistence contract.  MemStore defines semantics;
// SQLStore is the production backend.  Handlers depend ONLY on this interface.
type Store interface {
	// InsertSample persists one inbound sample and fires alerts for any
	// threshold crossings found in the sample.  Returns the persisted sample
	// (with its generated ID) and the list of alerts that were opened.
	InsertSample(ctx context.Context, s Sample) (Sample, []Alert, error)

	// LatestSample returns the most recent sample for the given ULID.
	// Returns ErrSampleNotFound if none exists.
	LatestSample(ctx context.Context, ulid string) (Sample, error)

	// RecentSamples returns the n most recent samples for ulid, newest first.
	// If n <= 0 it defaults to 20.
	RecentSamples(ctx context.Context, ulid string, n int) ([]Sample, error)

	// OpenAlerts returns all unresolved alerts for a ULID, newest first.
	OpenAlerts(ctx context.Context, ulid string) ([]Alert, error)

	// OrgOpenAlerts returns all unresolved alerts for an org, newest first.
	OrgOpenAlerts(ctx context.Context, orgID string) ([]Alert, error)

	// ResolveAlert marks an alert as resolved.  Returns ErrAlertNotFound if
	// the alert does not exist.
	ResolveAlert(ctx context.Context, alertID int64, resolvedAt time.Time) error

	// AllOpenAlerts returns all unresolved alerts across all tenants, newest
	// first.  Used by the ops dashboard and SUPPORT-02.
	AllOpenAlerts(ctx context.Context) ([]Alert, error)

	Close() error
}

// ─────────────────────────────────────────────────────────────────────────────
// MemStore — in-memory reference implementation
// ─────────────────────────────────────────────────────────────────────────────

// MemStore is the in-memory reference Store.  Concurrency-safe.
// It DEFINES the semantics every other implementation must match.
type MemStore struct {
	mu      sync.Mutex
	samples []Sample
	alerts  []Alert
	nextSID int64
	nextAID int64
}

// NewMemStore returns an empty reference store.
func NewMemStore() *MemStore {
	return &MemStore{nextSID: 1, nextAID: 1}
}

// compile-time assertion: MemStore satisfies Store.
var _ Store = (*MemStore)(nil)

func (m *MemStore) InsertSample(_ context.Context, s Sample) (Sample, []Alert, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if s.SampledAt.IsZero() {
		s.SampledAt = time.Now().UTC()
	}
	s.ID = m.nextSID
	m.nextSID++
	m.samples = append(m.samples, s)

	firings := Threshold(s)
	var opened []Alert
	for _, f := range firings {
		a := Alert{
			ID:       m.nextAID,
			ULID:     s.ULID,
			OrgID:    s.OrgID,
			Signal:   f.Signal,
			Severity: f.Severity,
			Detail:   f.Detail,
			FiredAt:  s.SampledAt,
		}
		m.nextAID++
		m.alerts = append(m.alerts, a)
		opened = append(opened, a)
	}
	return s, opened, nil
}

func (m *MemStore) LatestSample(_ context.Context, ulid string) (Sample, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var found *Sample
	for i := len(m.samples) - 1; i >= 0; i-- {
		if m.samples[i].ULID == ulid {
			cp := m.samples[i]
			found = &cp
			break
		}
	}
	if found == nil {
		return Sample{}, ErrSampleNotFound
	}
	return *found, nil
}

func (m *MemStore) RecentSamples(_ context.Context, ulid string, n int) ([]Sample, error) {
	if n <= 0 {
		n = 20
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var all []Sample
	for _, s := range m.samples {
		if s.ULID == ulid {
			all = append(all, s)
		}
	}
	// newest first
	sort.Slice(all, func(i, j int) bool { return all[i].SampledAt.After(all[j].SampledAt) })
	if n > 0 && n < len(all) {
		all = all[:n]
	}
	return all, nil
}

func (m *MemStore) OpenAlerts(_ context.Context, ulid string) ([]Alert, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Alert
	for _, a := range m.alerts {
		if a.ULID == ulid && a.ResolvedAt == nil {
			out = append(out, a)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].FiredAt.After(out[j].FiredAt) })
	return out, nil
}

func (m *MemStore) OrgOpenAlerts(_ context.Context, orgID string) ([]Alert, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Alert
	for _, a := range m.alerts {
		if a.OrgID == orgID && a.ResolvedAt == nil {
			out = append(out, a)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].FiredAt.After(out[j].FiredAt) })
	return out, nil
}

func (m *MemStore) ResolveAlert(_ context.Context, alertID int64, resolvedAt time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, a := range m.alerts {
		if a.ID == alertID {
			t := resolvedAt.UTC()
			m.alerts[i].ResolvedAt = &t
			return nil
		}
	}
	return ErrAlertNotFound
}

func (m *MemStore) AllOpenAlerts(_ context.Context) ([]Alert, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Alert
	for _, a := range m.alerts {
		if a.ResolvedAt == nil {
			out = append(out, a)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].FiredAt.After(out[j].FiredAt) })
	return out, nil
}

func (m *MemStore) Close() error { return nil }
