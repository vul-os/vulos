package backup

// drill_schedule.go — DR-DRILL-CADENCE-01: a config-gated periodic restore
// drill. The backup Scheduler proves we keep MAKING snapshots; this proves the
// snapshots actually RESTORE — turning the durability claim from asserted into
// continuously-exercised (RISKS-HUMAN-TEAM.md §5).
//
// On each tick the DrillScheduler runs RestoreDrill against the latest snapshot
// under BackupRoot into a throwaway staging dir, records the structured outcome
// (PASS/FAIL + RTO/RPO + timestamp), and prunes the staging dir. It NEVER
// touches production data (RestoreDrill only writes to its isolated staging
// dir). A drill failure is recorded + logged but never crashes the loop.

import (
	"context"
	"os"
	"sync"
	"time"
)

// DefaultDrillInterval is the period between automated restore drills when none
// is given. Drills are heavier than backups (they copy + open + integrity-check
// every DB), so the default cadence is daily rather than the backup's 6h.
const DefaultDrillInterval = 24 * time.Hour

// DrillOutcome is a recorded result of one periodic restore drill.
type DrillOutcome struct {
	At         time.Time   `json:"at"`
	Status     DrillStatus `json:"status"`
	RTOSeconds float64     `json:"rto_seconds"`
	RPOSeconds float64     `json:"rpo_seconds"`
	DBs        int         `json:"dbs"`
	Accounts   int         `json:"accounts"`
	Err        string      `json:"err,omitempty"`
}

// DrillScheduler runs RestoreDrill on a fixed interval and retains a bounded
// history of outcomes for an ops/health endpoint to read. Safe for concurrent
// reads while the loop writes.
type DrillScheduler struct {
	backupRoot string
	interval   time.Duration
	logf       func(format string, args ...any)
	// runImmediately controls whether a drill fires on Start before the first
	// tick. Default true so a freshly-booted CP records a drill outcome promptly.
	runImmediately bool

	mu      sync.RWMutex
	history []DrillOutcome // most-recent-last, capped at historyCap
}

const drillHistoryCap = 32

// DrillScheduleConfig configures a DrillScheduler.
type DrillScheduleConfig struct {
	// BackupRoot is the directory holding timestamped snapshots (the same root
	// the Backupper writes). The drill restores the LATEST snapshot found here.
	BackupRoot string
	// Interval is the period between drills. <=0 → DefaultDrillInterval.
	Interval time.Duration
	// Logf receives progress lines; defaults to no-op.
	Logf func(format string, args ...any)
	// SkipInitialRun, when true, suppresses the immediate drill on Start (the
	// first drill then fires only after one Interval). Default false.
	SkipInitialRun bool
}

// NewDrillScheduler constructs a DrillScheduler. A non-positive interval falls
// back to DefaultDrillInterval.
func NewDrillScheduler(cfg DrillScheduleConfig) *DrillScheduler {
	interval := cfg.Interval
	if interval <= 0 {
		interval = DefaultDrillInterval
	}
	logf := cfg.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &DrillScheduler{
		backupRoot:     cfg.BackupRoot,
		interval:       interval,
		logf:           logf,
		runImmediately: !cfg.SkipInitialRun,
	}
}

// Start launches the drill loop in a goroutine and returns immediately. The
// loop runs an initial drill (unless SkipInitialRun) then one per interval, and
// exits when ctx is cancelled.
func (s *DrillScheduler) Start(ctx context.Context) {
	go s.run(ctx)
}

func (s *DrillScheduler) run(ctx context.Context) {
	if s.runImmediately {
		s.RunOnce(ctx)
	}
	t := time.NewTicker(s.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			s.logf("[restore-drill] scheduler stopping: %v", ctx.Err())
			return
		case <-t.C:
			s.RunOnce(ctx)
		}
	}
}

// RunOnce performs a single drill against the latest snapshot, records the
// outcome, and returns it. Exposed so an ops endpoint (or a test) can trigger a
// drill on demand. Never panics; a failure is captured in the outcome's Err.
func (s *DrillScheduler) RunOnce(ctx context.Context) DrillOutcome {
	staging, mkErr := os.MkdirTemp("", "vulos-drill-cadence-*")
	if mkErr != nil {
		out := DrillOutcome{At: time.Now().UTC(), Status: StatusFail, Err: "mkdir staging: " + mkErr.Error()}
		s.record(out)
		s.logf("[restore-drill] FAILED to create staging: %v", mkErr)
		return out
	}
	// Throwaway staging dir — always cleaned up.
	defer os.RemoveAll(staging)

	rep, err := RestoreDrill(ctx, RestoreDrillConfig{
		BackupRoot: s.backupRoot,
		StagingDir: staging,
		Logf:       s.logf,
	})
	out := DrillOutcome{At: time.Now().UTC()}
	if err != nil {
		out.Status = StatusFail
		out.Err = err.Error()
		s.record(out)
		s.logf("[restore-drill] scheduled drill ERROR: %v", err)
		return out
	}
	out.Status = rep.Status
	out.RTOSeconds = rep.RTOSeconds
	out.RPOSeconds = rep.RPOSeconds
	out.DBs = len(rep.DBs)
	out.Accounts = rep.Accounts
	if rep.Status == StatusFail && len(rep.Errors) > 0 {
		out.Err = rep.Errors[0]
	}
	s.record(out)
	s.logf("[restore-drill] scheduled drill status=%s RTO=%.3fs RPO=%.0fs", out.Status, out.RTOSeconds, out.RPOSeconds)
	return out
}

func (s *DrillScheduler) record(o DrillOutcome) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.history = append(s.history, o)
	if len(s.history) > drillHistoryCap {
		s.history = s.history[len(s.history)-drillHistoryCap:]
	}
}

// History returns a copy of the recorded drill outcomes (oldest-first).
func (s *DrillScheduler) History() []DrillOutcome {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]DrillOutcome, len(s.history))
	copy(out, s.history)
	return out
}

// LastOutcome returns the most recent drill outcome and true, or a zero value
// and false when no drill has run yet.
func (s *DrillScheduler) LastOutcome() (DrillOutcome, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.history) == 0 {
		return DrillOutcome{}, false
	}
	return s.history[len(s.history)-1], true
}
