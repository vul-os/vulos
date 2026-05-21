package lease

// job_test.go — unit tests for RunOnce (LEASE-04).
//
// All tests use the in-memory mockBackend defined in lease_test.go.
// No real S3 / MinIO required.

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// ── helpers ───────────────────────────────────────────────────────────────────

// newJobManager returns a Manager backed by a fresh mockBackend with no
// pre-seeded objects (EnsureLease will create them on first call).
func newJobManager() (*Manager, *mockBackend) {
	b := newMockBackend()
	return NewWithBackend(b), b
}

// shortDurations returns ttl=500ms, renewEvery=50ms — fast enough for tests
// without being flaky on loaded CI.
func shortDurations() (ttl, renewEvery time.Duration) {
	return 500 * time.Millisecond, 50 * time.Millisecond
}

// ── TestRunOnce_SingleOwner ───────────────────────────────────────────────────
//
// While one instance holds the lease (fn is running), a second concurrent
// RunOnce call for the same jobID must return ran=false immediately.
//
// We use explicit synchronisation to guarantee true concurrency:
//   - Runner A's fn blocks on fnHeld until runner B has completed its attempt.
//   - Runner B is launched only after Runner A has signalled it is inside fn.

func TestRunOnce_SingleOwner(t *testing.T) {
	mgr, _ := newJobManager()
	ttl, renew := shortDurations()

	// fnHeld is closed when runner A is inside fn (lease is held).
	fnHeld := make(chan struct{})
	// letAFinish is closed to unblock runner A after runner B has attempted.
	letAFinish := make(chan struct{})

	type result struct {
		ran bool
		err error
	}

	resultsA := make(chan result, 1)
	resultsB := make(chan result, 1)

	// Runner A: acquires the lease and holds fn open until we signal.
	go func() {
		ran, err := RunOnce(context.Background(), mgr, "singleton-job", ttl, renew,
			func(ctx context.Context) error {
				close(fnHeld) // signal: lease is now held
				<-letAFinish  // wait until runner B has had its chance
				return nil
			},
		)
		resultsA <- result{ran, err}
	}()

	// Wait for runner A to be inside fn before starting runner B.
	<-fnHeld

	// Runner B: must see the lease as held and return ran=false immediately.
	go func() {
		ran, err := RunOnce(context.Background(), mgr, "singleton-job", ttl, renew,
			func(ctx context.Context) error {
				// Should never be called.
				return nil
			},
		)
		resultsB <- result{ran, err}
	}()

	// Collect runner B's result first, then let A finish.
	rB := <-resultsB
	close(letAFinish) // unblock runner A
	rA := <-resultsA

	if rA.err != nil {
		t.Errorf("runner A error: %v", rA.err)
	}
	if !rA.ran {
		t.Error("runner A: expected ran=true (it held the lease)")
	}

	if rB.err != nil {
		t.Errorf("runner B error: %v", rB.err)
	}
	if rB.ran {
		t.Errorf("runner B: expected ran=false while A holds the lease, got ran=true")
	}
}

// ── TestRunOnce_RenewWhileRunning ─────────────────────────────────────────────
//
// fn sleeps long enough for at least two renew ticks; we verify that the
// fence in the stored doc is higher than 1 after RunOnce completes.

func TestRunOnce_RenewWhileRunning(t *testing.T) {
	mgr, b := newJobManager()
	ttl := 500 * time.Millisecond
	renew := 30 * time.Millisecond

	scope := jobScope("renew-job")

	ran, err := RunOnce(context.Background(), mgr, "renew-job", ttl, renew,
		func(ctx context.Context) error {
			// Sleep long enough for ~3 renew ticks.
			time.Sleep(120 * time.Millisecond)
			return nil
		},
	)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if !ran {
		t.Fatal("RunOnce: expected ran=true")
	}

	// After successful RunOnce the lease should be free again.
	body, _, err := b.getWithETag(context.Background(), objectKey(scope))
	if err != nil {
		t.Fatalf("post-run read: %v", err)
	}
	var doc leaseDoc
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if doc.State != stateFree {
		t.Errorf("post-run state: want free, got %s", doc.State)
	}
	// Acquire bumps to 1; each Renew adds 1 more — expect at least 2.
	if doc.Fence <= 1 {
		t.Errorf("fence: want >1 (renews happened), got %d", doc.Fence)
	}
}

// ── TestRunOnce_ReleaseOnCompletion ──────────────────────────────────────────
//
// After RunOnce returns successfully, the lease object is in the free state.

func TestRunOnce_ReleaseOnCompletion(t *testing.T) {
	mgr, b := newJobManager()
	ttl, renew := shortDurations()

	ran, err := RunOnce(context.Background(), mgr, "release-job", ttl, renew,
		func(ctx context.Context) error { return nil },
	)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if !ran {
		t.Fatal("expected ran=true")
	}

	scope := jobScope("release-job")
	body, _, err := b.getWithETag(context.Background(), objectKey(scope))
	if err != nil {
		t.Fatalf("post-run read: %v", err)
	}
	var doc leaseDoc
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if doc.State != stateFree {
		t.Errorf("state after RunOnce: want free, got %s", doc.State)
	}
}

// ── TestRunOnce_StalledHolder_OtherInstanceRuns ───────────────────────────────
//
// Seed a held-but-expired lease (stalled holder).  A fresh RunOnce must
// successfully acquire the expired lease and run fn.

func TestRunOnce_StalledHolder_OtherInstanceRuns(t *testing.T) {
	b := newMockBackend()
	mgr := NewWithBackend(b)
	ttl, renew := shortDurations()

	jobID := "stall-job"
	scope := jobScope(jobID)

	// Seed the lease as held-but-expired.
	stalledDoc := leaseDoc{
		State:     stateHeld,
		Holder:    "stalled-node",
		Fence:     7,
		ExpiresAt: time.Now().Add(-2 * time.Minute), // already expired
	}
	stalledBody, _ := json.Marshal(stalledDoc)
	b.seed(objectKey(scope), stalledBody)

	ran, err := RunOnce(context.Background(), mgr, jobID, ttl, renew,
		func(ctx context.Context) error { return nil },
	)
	if err != nil {
		t.Fatalf("RunOnce on expired lease: %v", err)
	}
	if !ran {
		t.Fatal("expected ran=true after stalled holder expired")
	}

	// Fence must be > 7 (new holder acquired over the stalled fence).
	body, _, err := b.getWithETag(context.Background(), objectKey(scope))
	if err != nil {
		t.Fatalf("post-run read: %v", err)
	}
	var doc leaseDoc
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if doc.Fence <= 7 {
		t.Errorf("fence: want >7, got %d", doc.Fence)
	}
	if doc.State != stateFree {
		t.Errorf("state: want free, got %s", doc.State)
	}
}

// ── TestRunOnce_FencePreventsDoubleRun ────────────────────────────────────────
//
// Fencing prevents a stalled runner from double-executing:
//  1. A background goroutine waits until fn is running, then overwrites the
//     lease object to simulate expiry + takeover (higher fence, new holder).
//  2. RunOnce's renew loop detects ErrNotHolder on its next tick and cancels
//     fn's context.
//  3. fn receives context cancellation (cannot complete normally).
//  4. RunOnce returns ran=true with a non-nil lease-lost error.
//
// The takeover goroutine is started BEFORE RunOnce is called so it is already
// listening when fn signals it has started.

func TestRunOnce_FencePreventsDoubleRun(t *testing.T) {
	b := newMockBackend()
	mgr := NewWithBackend(b)

	jobID := "fence-job"
	ttl := 500 * time.Millisecond
	renewEvery := 30 * time.Millisecond

	// fnStarted is closed once fn begins executing.
	fnStarted := make(chan struct{})
	var fnContextCancelled int32 // atomically set to 1 when fn sees ctx.Done

	// Start the takeover goroutine BEFORE RunOnce so it is ready immediately.
	takeoverDone := make(chan struct{})
	go func() {
		defer close(takeoverDone)

		// Wait for fn to start running.
		<-fnStarted

		// Give the renew loop a moment to arm its ticker.
		time.Sleep(10 * time.Millisecond)

		scope := jobScope(jobID)

		// Read the current held doc.
		body, etag, err := b.getWithETag(context.Background(), objectKey(scope))
		if err != nil {
			return
		}
		var doc leaseDoc
		if err := json.Unmarshal(body, &doc); err != nil {
			return
		}

		// Write a new doc that simulates another node having taken over with a
		// much higher fence.  The original holder's Renew will now get
		// ErrNotHolder because the holder name and fence no longer match.
		newDoc := leaseDoc{
			State:     stateHeld,
			Holder:    "takeover-node",
			Fence:     doc.Fence + 100,
			ExpiresAt: time.Now().Add(5 * time.Minute),
		}
		newBody, _ := json.Marshal(newDoc)
		// This CAS write will succeed because we hold the current etag.
		_, _ = b.putIfMatch(context.Background(), objectKey(scope), newBody, etag)
		// After this point the original RunOnce's next Renew returns ErrNotHolder,
		// causing the renew loop to cancel jobCtx and abort fn.
	}()

	// RunOnce blocks here until fn returns (or ctx is cancelled).
	ran, err := RunOnce(context.Background(), mgr, jobID, ttl, renewEvery,
		func(ctx context.Context) error {
			// Signal the takeover goroutine that we are inside fn.
			close(fnStarted)

			// Block until the context is cancelled (fence stolen) or a safety
			// timeout fires.  We must NOT return nil here — that would mean fn
			// completed "successfully" despite the stolen fence.
			select {
			case <-ctx.Done():
				atomic.StoreInt32(&fnContextCancelled, 1)
				return ctx.Err()
			case <-time.After(5 * time.Second):
				return errors.New("fn: safety timeout — ctx was never cancelled")
			}
		},
	)

	// Wait for takeover to finish (defensive — it should already be done).
	<-takeoverDone

	if !ran {
		t.Error("expected ran=true: this instance acquired and started the job")
	}
	if err == nil {
		t.Error("expected non-nil error: lease was stolen mid-run")
	}
	if atomic.LoadInt32(&fnContextCancelled) != 1 {
		t.Errorf("fn should have seen ctx.Done when fence was stolen; err=%v", err)
	}
}

// ── TestRunOnce_FnError_Propagated ────────────────────────────────────────────
//
// When fn returns a non-nil error, RunOnce surfaces it.

func TestRunOnce_FnError_Propagated(t *testing.T) {
	mgr, _ := newJobManager()
	ttl, renew := shortDurations()

	sentinelErr := errors.New("job failed")
	ran, err := RunOnce(context.Background(), mgr, "err-job", ttl, renew,
		func(ctx context.Context) error { return sentinelErr },
	)
	if !ran {
		t.Error("expected ran=true")
	}
	if !errors.Is(err, sentinelErr) {
		t.Errorf("want sentinelErr wrapped in err, got %v", err)
	}
}

// ── TestRunOnce_Idempotent_EnsureLease ───────────────────────────────────────
//
// Calling RunOnce twice sequentially on the same jobID must succeed both
// times: the second call re-acquires the (now-free) lease and runs fn again.

func TestRunOnce_Idempotent_EnsureLease(t *testing.T) {
	mgr, _ := newJobManager()
	ttl, renew := shortDurations()

	for i := 0; i < 2; i++ {
		ran, err := RunOnce(context.Background(), mgr, "idempotent-job", ttl, renew,
			func(ctx context.Context) error { return nil },
		)
		if err != nil {
			t.Errorf("call %d: unexpected error: %v", i+1, err)
		}
		if !ran {
			t.Errorf("call %d: expected ran=true", i+1)
		}
	}
}
