package scheduler

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vul-os/vulos-management/pkg/cpdb"
)

// TestScheduler_LeaderElection verifies that two Scheduler instances sharing
// the same SQLite DB execute a job at most once per tick. The advisory lock
// must be exclusive within a single tick window.
func TestScheduler_LeaderElection(t *testing.T) {
	dir := t.TempDir()
	dsn := "file:" + filepath.Join(dir, "sched_leader.db")

	db1, err := cpdb.OpenSQLiteDSN(dsn)
	if err != nil {
		t.Fatalf("OpenSQLiteDSN db1: %v", err)
	}
	s1, err := Open(db1)
	if err != nil {
		t.Fatalf("Open s1: %v", err)
	}
	db2, err := cpdb.OpenSQLiteDSN(dsn)
	if err != nil {
		t.Fatalf("OpenSQLiteDSN db2: %v", err)
	}
	s2, err := Open(db2)
	if err != nil {
		t.Fatalf("Open s2: %v", err)
	}

	// Give them distinct instance IDs so the lock logic can tell them apart.
	s1.instanceID = "instance-1"
	s2.instanceID = "instance-2"

	const jobInterval = 10 * time.Millisecond
	const runDuration = 200 * time.Millisecond

	var count atomic.Int64

	job := JobConfig{
		Name:     "test-job",
		Interval: jobInterval,
		Fn: func(ctx context.Context) error {
			count.Add(1)
			return nil
		},
	}
	s1.Register(job)
	s2.Register(job)

	ctx, cancel := context.WithTimeout(context.Background(), runDuration)
	defer cancel()

	go s1.Run(ctx)
	go s2.Run(ctx)

	<-ctx.Done()

	total := count.Load()
	// Must have run at least once.
	if total == 0 {
		t.Error("expected at least one job invocation, got 0")
	}
	// With lockDuration = 2×interval = 20ms, within the run window of 200ms
	// we should see at most ~10 ticks, each firing at most once across both
	// schedulers. Add generous slack for scheduling jitter.
	expectedMaxTicks := int64(runDuration/jobInterval) + 2
	if total > expectedMaxTicks {
		t.Errorf("too many invocations: got %d, expected <= %d (leader election not exclusive?)", total, expectedMaxTicks)
	}
}

// TestScheduler_JobRunsOnInterval verifies that a single Scheduler instance
// fires a job at least 3 times within a generous window. With lock duration =
// 2×interval, a run happens roughly every 2×interval. Using interval=30ms and
// a 500ms window gives ~8 expected runs; asserting >= 3 is conservative.
func TestScheduler_JobRunsOnInterval(t *testing.T) {
	dir := t.TempDir()
	dsn := "file:" + filepath.Join(dir, "sched_interval.db")

	db, err := cpdb.OpenSQLiteDSN(dsn)
	if err != nil {
		t.Fatalf("OpenSQLiteDSN: %v", err)
	}
	s, err := Open(db)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	var count atomic.Int64
	s.Register(JobConfig{
		Name:     "interval-job",
		Interval: 30 * time.Millisecond,
		Fn: func(ctx context.Context) error {
			count.Add(1)
			return nil
		},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	go s.Run(ctx)

	<-ctx.Done()

	total := count.Load()
	if total < 3 {
		t.Errorf("expected >= 3 invocations in 500ms@30ms interval, got %d", total)
	}
}
