package scheduler_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/vul-os/vulos-management/pkg/cpdb"
	"github.com/vul-os/vulos-management/pkg/scheduler"
)

func openPGScheduler(t *testing.T) *scheduler.Scheduler {
	t.Helper()
	dsn := os.Getenv("VULOS_TEST_POSTGRES")
	if dsn == "" {
		t.Skip("set VULOS_TEST_POSTGRES to run Postgres integration tests")
	}
	t.Setenv("DATABASE_URL", dsn)
	t.Setenv("VULOS_DATABASE_URL", "")
	db, err := cpdb.Open("scheduler_pgtest")
	if err != nil {
		t.Fatalf("cpdb.Open: %v", err)
	}
	s, err := scheduler.Open(db)
	if err != nil {
		db.Close()
		t.Fatalf("scheduler.Open: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`DROP SCHEMA IF EXISTS scheduler_pgtest CASCADE`)
		_ = db.Close()
	})
	return s
}

func TestPG_Scheduler_Open(t *testing.T) {
	s := openPGScheduler(t)
	if s == nil {
		t.Fatal("expected non-nil Scheduler")
	}
}

func TestPG_Scheduler_RegisterAndRun(t *testing.T) {
	s := openPGScheduler(t)

	done := make(chan struct{})
	s.Register(scheduler.JobConfig{
		Name:     "pg-test-job",
		Interval: 20 * time.Millisecond,
		Fn: func(ctx context.Context) error {
			select {
			case done <- struct{}{}:
			default:
			}
			return nil
		},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	go s.Run(ctx)

	select {
	case <-done:
		// job fired at least once — Postgres advisory lock works
	case <-ctx.Done():
		t.Error("job never fired within 500ms")
	}
}
