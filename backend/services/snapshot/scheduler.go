package snapshot

import (
	"context"
	"log"
	"time"
)

// Scheduler drives periodic snapshots + retention for one Snapshotter.
type Scheduler struct {
	snap     *Snapshotter
	interval time.Duration
	policy   Policy

	// OnCreated, when set, is invoked (best-effort — its result is not
	// checked, and a panic-free caller is the caller's responsibility) after
	// each scheduled snapshot succeeds. Lets callers (e.g. cmd/server's
	// webhooks wiring) observe snapshot creation without this package
	// importing services/webhooks — same decoupling pattern as
	// auth.Store.sensitiveActionHook.
	OnCreated func(idx *Index)
}

// NewScheduler builds a scheduler that snapshots every interval and applies the
// retention policy after each run. interval <= 0 defaults to 24h; a zero policy
// defaults to DefaultPolicy.
func NewScheduler(snap *Snapshotter, interval time.Duration, policy Policy) *Scheduler {
	if interval <= 0 {
		interval = 24 * time.Hour
	}
	if policy.KeepDaily == 0 && policy.KeepWeekly == 0 {
		policy = DefaultPolicy
	}
	return &Scheduler{snap: snap, interval: interval, policy: policy}
}

// RunOnce takes one scheduled snapshot and prunes. It is exported so it can be
// unit-tested and triggered manually.
func (sc *Scheduler) RunOnce(ctx context.Context) error {
	idx, err := sc.snap.Create(ctx, KindScheduled, "")
	if err != nil {
		return err
	}
	log.Printf("[snapshot] scheduled snapshot %s created (objects=%d added=%dB)", idx.ID, idx.ObjectCount, idx.BlobBytesAdded)
	if sc.OnCreated != nil {
		sc.OnCreated(idx)
	}
	pr, err := sc.snap.Prune(ctx, sc.policy)
	if err != nil {
		return err
	}
	log.Printf("[snapshot] retention: kept=%d pruned=%d blobs_deleted=%d reclaimed=%dB",
		len(pr.Kept), len(pr.Pruned), pr.BlobsDeleted, pr.BytesReclaimed)
	return nil
}

// Start runs the scheduler loop until ctx is cancelled. It does NOT snapshot
// immediately on start (the first snapshot fires after one interval); call
// RunOnce for an immediate run.
func (sc *Scheduler) Start(ctx context.Context) {
	ticker := time.NewTicker(sc.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := sc.RunOnce(ctx); err != nil {
				log.Printf("[snapshot] scheduled run failed: %v", err)
			}
		}
	}
}
