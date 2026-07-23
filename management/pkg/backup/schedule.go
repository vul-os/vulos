package backup

import (
	"context"
	"time"
)

// DefaultInterval is the period between automated backups when none is given.
const DefaultInterval = 6 * time.Hour

// Scheduler runs a Backupper on a fixed interval until its context is
// cancelled. It runs one backup immediately on Start so a freshly-booted
// control plane always has a recent restorable artifact (bounding RPO at boot).
type Scheduler struct {
	b        *Backupper
	interval time.Duration
}

// NewScheduler wraps a Backupper with a periodic runner. A non-positive
// interval falls back to DefaultInterval.
func NewScheduler(b *Backupper, interval time.Duration) *Scheduler {
	if interval <= 0 {
		interval = DefaultInterval
	}
	return &Scheduler{b: b, interval: interval}
}

// Start launches the scheduling loop in a new goroutine and returns
// immediately. The loop runs an initial backup, then one every interval, and
// exits when ctx is cancelled. Backup errors are logged via the Backupper's
// Logf and never crash the loop (durability tooling must not take down the CP).
func (s *Scheduler) Start(ctx context.Context) {
	go s.run(ctx)
}

func (s *Scheduler) run(ctx context.Context) {
	s.once(ctx)
	t := time.NewTicker(s.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			s.b.cfg.Logf("[backup] scheduler stopping: %v", ctx.Err())
			return
		case <-t.C:
			s.once(ctx)
		}
	}
}

func (s *Scheduler) once(ctx context.Context) {
	if _, _, err := s.b.Run(ctx); err != nil && ctx.Err() == nil {
		s.b.cfg.Logf("[backup] scheduled backup FAILED: %v", err)
	}
}
