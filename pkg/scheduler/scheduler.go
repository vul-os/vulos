// Package scheduler provides a dual-backend (SQLite + Postgres) unified job
// scheduler with optimistic advisory-lock leader election. When multiple CP
// instances share the same scheduler database, only one instance runs each job
// per tick — the one that wins the optimistic UPDATE.
//
// Leader election strategy:
//
//	Each job has a row in scheduler_jobs. To run a job, an instance:
//	  1. Ensures the row exists (INSERT … ON CONFLICT(name) DO NOTHING).
//	  2. Tries to claim it with an UPDATE … WHERE locked_until < now.
//	  3. Checks rows_affected == 1. If so, it is the leader for this tick.
//
// The lock duration is 2× the job interval, so a slow execution that lasts
// longer than one interval does not allow a second instance to double-run it
// in the next tick.
package scheduler

import (
	"context"
	"fmt"
	"io/fs"
	"log"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/vul-os/vulos-management/pkg/cpdb"
)

// JobFunc is the function executed when a job is acquired.
type JobFunc func(ctx context.Context) error

// JobConfig describes a single scheduled job.
type JobConfig struct {
	Name     string
	Interval time.Duration
	Fn       JobFunc
}

// Scheduler manages a set of recurring jobs backed by a DB advisory lock.
type Scheduler struct {
	db         *cpdb.DB
	instanceID string // unique per-process (hostname + pid)
	mu         sync.Mutex
	jobs       []JobConfig
	now        func() time.Time // injectable clock
}

// Open applies migrations to db and returns a ready Scheduler.
// The caller must eventually call Run(ctx) in a goroutine; ctx cancellation
// stops all jobs.
//
// db should be obtained from cpdb.Open("scheduler") for production, or from
// cpdb.OpenSQLiteDSN(":memory:") for tests.
func Open(db *cpdb.DB) (*Scheduler, error) {
	subFS, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("scheduler: migrations fs: %w", err)
	}
	if err := db.Migrate(subFS); err != nil {
		return nil, fmt.Errorf("scheduler: migrate: %w", err)
	}

	hostname, _ := os.Hostname()
	pid := os.Getpid()
	instanceID := fmt.Sprintf("%s/%d", hostname, pid)

	return &Scheduler{
		db:         db,
		instanceID: instanceID,
		now:        time.Now,
	}, nil
}

// Now returns the current time from the scheduler's clock. Exposed so callers
// can pass a consistent timestamp to job functions (e.g. SweepExpiredPastDue).
func (s *Scheduler) Now() time.Time { return s.now() }

// Register adds one or more jobs. Call before Run.
func (s *Scheduler) Register(jobs ...JobConfig) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.jobs = append(s.jobs, jobs...)
}

// Run starts one goroutine per registered job and blocks until ctx is
// cancelled. Each goroutine loops on a ticker; on each tick it calls
// tryAcquire and, if successful, runs the job function.
func (s *Scheduler) Run(ctx context.Context) {
	s.mu.Lock()
	jobs := make([]JobConfig, len(s.jobs))
	copy(jobs, s.jobs)
	s.mu.Unlock()

	var wg sync.WaitGroup
	for _, j := range jobs {
		j := j // capture
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.runJob(ctx, j)
		}()
	}
	wg.Wait()
}

// runJob is the per-job goroutine body.
func (s *Scheduler) runJob(ctx context.Context, job JobConfig) {
	ticker := time.NewTicker(job.Interval)
	defer ticker.Stop()
	lockDuration := job.Interval * 2
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			now := s.now()
			acquired, err := s.tryAcquire(ctx, job.Name, now, lockDuration)
			if err != nil {
				log.Printf("[scheduler] %s: tryAcquire error: %v", job.Name, err)
				continue
			}
			if !acquired {
				continue
			}
			if err := job.Fn(ctx); err != nil {
				log.Printf("[scheduler] %s: job error: %v", job.Name, err)
			}
		}
	}
}

// tryAcquire tries to claim the advisory lock for name. Returns true if this
// instance is the leader for this tick.
//
// Uses optimistic UPDATE: only one writer wins when locked_until < now.
func (s *Scheduler) tryAcquire(ctx context.Context, name string, now time.Time, lockDuration time.Duration) (bool, error) {
	// Ensure the row exists before the UPDATE so we never miss.
	// ON CONFLICT(name) DO NOTHING is valid on both SQLite (3.24+) and Postgres.
	_, err := s.db.ExecContext(ctx,
		s.db.Rebind(`INSERT INTO scheduler_jobs (name, locked_by, locked_until, last_run_at, run_count)
		 VALUES (?, NULL, '1970-01-01T00:00:00Z', NULL, 0)
		 ON CONFLICT(name) DO NOTHING`),
		name,
	)
	if err != nil {
		return false, fmt.Errorf("scheduler: ensure row %s: %w", name, err)
	}

	// Use RFC3339Nano for sub-second precision so that a 100ms lock duration
	// produces a timestamp that is lexicographically greater than "now" even
	// when both fall within the same second.
	lockedUntil := now.Add(lockDuration).UTC().Format(time.RFC3339Nano)
	nowStr := now.UTC().Format(time.RFC3339Nano)

	res, err := s.db.ExecContext(ctx,
		s.db.Rebind(`UPDATE scheduler_jobs
		 SET locked_by = ?, locked_until = ?, last_run_at = ?, run_count = run_count + 1
		 WHERE name = ? AND locked_until < ?`),
		s.instanceID, lockedUntil, nowStr, name, nowStr,
	)
	if err != nil {
		return false, fmt.Errorf("scheduler: acquire lock %s: %w", name, err)
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// ──────────────────────────────────────────────────────────────────────────────
// Env helpers (used by callers that want env-driven configuration)
// ──────────────────────────────────────────────────────────────────────────────

// IntervalFromEnv reads a Go duration from env var name, returning dflt if
// unset or invalid.
func IntervalFromEnv(name string, dflt time.Duration) time.Duration {
	raw := os.Getenv(name)
	if raw == "" {
		return dflt
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		log.Printf("[scheduler] WARNING: invalid %s=%q — using default %s", name, raw, dflt)
		return dflt
	}
	return d
}

// IntFromEnv reads an integer from env var name, returning dflt if unset or invalid.
func IntFromEnv(name string, dflt int) int {
	raw := os.Getenv(name)
	if raw == "" {
		return dflt
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return dflt
	}
	return n
}
