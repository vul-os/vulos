package lease

// job.go — singleton (run-once-per-cluster) job runner over the bucket lease.
//
// RunOnce acquires leases/job/<jobID>.json (via EnsureLease + Acquire), runs fn
// exactly once per live lease window, renews the lease in the background while
// fn is running, and releases the lease on completion.
//
// Fencing guarantee: if the background renew loop detects that the lease was
// lost (ErrNotHolder / ErrLost), it cancels the job's context so fn can stop
// promptly, and the renew loop does not attempt further renewals.  A stalled
// runner whose lease has expired will get ErrNotHolder on its next renew attempt,
// causing it to cancel — preventing double-execution after takeover.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

// jobScope returns the lease scope for a named job.
func jobScope(jobID string) string {
	return "job/" + jobID
}

// RunOnce runs fn at most once per cluster for the given jobID.
//
//   - It calls EnsureLease to bootstrap the lease object (idempotent).
//   - It attempts to Acquire the lease; if the lease is already held by another
//     instance, it returns (ran=false, nil) immediately.
//   - If Acquire succeeds, a background goroutine renews the lease every
//     renewEvery until fn returns or the lease is lost.
//   - If the renew goroutine detects a lost/stale fence it cancels the job
//     context, signals the fence-stale condition, and stops renewing.
//   - After fn returns (or is cancelled), the lease is released.
//
// Parameters:
//
//	ctx        — parent context; cancellation stops the renew loop and fn.
//	mgr        — lease Manager (LEASE-01).
//	jobID      — unique job identifier; scope becomes "job/<jobID>".
//	ttl        — lease TTL passed to Acquire / Renew.
//	renewEvery — how often the renew goroutine fires while fn is running.
//	fn         — the job body; receives a context that is cancelled on fence loss.
//
// Returns:
//
//	ran=true  — this instance ran fn and acquired the lease.
//	ran=false — another instance holds the lease; fn was not called.
//	err       — non-nil only when this instance ran fn and something went wrong
//	            (fn returned an error, or the lease was lost mid-run).
func RunOnce(
	ctx context.Context,
	mgr *Manager,
	jobID string,
	ttl, renewEvery time.Duration,
	fn func(ctx context.Context) error,
) (ran bool, err error) {
	scope := jobScope(jobID)

	// Bootstrap: create the lease object if it does not yet exist.
	if err := mgr.EnsureLease(ctx, scope); err != nil {
		return false, fmt.Errorf("RunOnce %q: EnsureLease: %w", jobID, err)
	}

	// Build a per-call unique holderID so two concurrent instances of the same
	// job cannot accidentally share a holderID and steal each other's lease.
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return false, fmt.Errorf("RunOnce %q: rand: %w", jobID, err)
	}
	holderID := jobID + "-runner-" + hex.EncodeToString(nonce[:])

	// Attempt to acquire the lease.
	held, err := mgr.Acquire(ctx, scope, holderID, ttl)
	if err != nil {
		if errors.Is(err, ErrNotHolder) || errors.Is(err, ErrLost) {
			// Another instance holds the lease; this one should not run.
			return false, nil
		}
		return false, fmt.Errorf("RunOnce %q: Acquire: %w", jobID, err)
	}

	// We hold the lease.  Set up a cancellable child context for fn so we can
	// cancel it if the fence goes stale.
	jobCtx, cancelJob := context.WithCancel(ctx)
	defer cancelJob()

	// fenceLostErr is set by the renew goroutine when it detects fence loss.
	// It is read only after renewDone is closed (happens-before).
	var fenceLostErr error
	renewDone := make(chan struct{})

	go func() {
		defer close(renewDone)
		ticker := time.NewTicker(renewEvery)
		defer ticker.Stop()
		for {
			select {
			case <-jobCtx.Done():
				// fn finished (or parent cancelled) — stop renewing.
				return
			case <-ticker.C:
				if rerr := mgr.Renew(jobCtx, held, ttl); rerr != nil {
					// Lease lost or fence stale — cancel the job.
					fenceLostErr = rerr
					cancelJob()
					return
				}
			}
		}
	}()

	// Run the job body.
	fnErr := fn(jobCtx)

	// Stop the renew goroutine and wait for it to exit before releasing.
	cancelJob()
	<-renewDone

	// If the fence was lost, report that instead of (or in addition to) fnErr.
	if fenceLostErr != nil {
		// The renew loop cancelled the context.  If fn returned ctx.Err() that
		// is a consequence of our cancellation — report the root cause.
		if fnErr == nil || errors.Is(fnErr, context.Canceled) {
			return true, fmt.Errorf("RunOnce %q: lease lost mid-run: %w", jobID, fenceLostErr)
		}
		// fn returned its own error; wrap both.
		return true, fmt.Errorf("RunOnce %q: lease lost mid-run (%v) and fn error: %w", jobID, fenceLostErr, fnErr)
	}

	// Lease is still ours — release it cleanly.
	// Use a fresh background context in case the parent was cancelled.
	releaseCtx := context.Background()
	if rerr := mgr.Release(releaseCtx, held); rerr != nil {
		if fnErr != nil {
			return true, fmt.Errorf("RunOnce %q: fn error (%v) and release error: %w", jobID, fnErr, rerr)
		}
		return true, fmt.Errorf("RunOnce %q: release: %w", jobID, rerr)
	}

	return true, fnErr
}
