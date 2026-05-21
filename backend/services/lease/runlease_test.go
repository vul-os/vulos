package lease

// runlease_test.go — unit tests for AcquireRunLease, RunLease.Renew/Release,
// and StartRenewLoop (CONC-02).
//
// All tests use the in-memory mockBackend from lease_test.go — no real S3.

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// ── helpers ───────────────────────────────────────────────────────────────────

func acquireRunLeaseHelper(t *testing.T, mgr *Manager, profile, appID, holderID string, ttl time.Duration) *RunLease {
	t.Helper()
	rl, err := AcquireRunLease(context.Background(), mgr, profile, appID, holderID, ttl)
	if err != nil {
		t.Fatalf("AcquireRunLease(%s, %s): unexpected error: %v", profile, appID, err)
	}
	if rl == nil {
		t.Fatalf("AcquireRunLease(%s, %s): returned nil RunLease", profile, appID)
	}
	return rl
}

// ── TestAcquireRunLease_HappyPath ─────────────────────────────────────────────

func TestAcquireRunLease_HappyPath(t *testing.T) {
	b := newMockBackend()
	mgr := NewWithBackend(b)

	rl := acquireRunLeaseHelper(t, mgr, "default", "notes", "node-A", 20*time.Second)

	if rl.Profile != "default" {
		t.Errorf("Profile: want default, got %q", rl.Profile)
	}
	if rl.AppID != "notes" {
		t.Errorf("AppID: want notes, got %q", rl.AppID)
	}
	if rl.Fence() < 1 {
		t.Errorf("Fence: want >= 1, got %d", rl.Fence())
	}
}

// ── TestAcquireRunLease_SecondInstanceBlocked ─────────────────────────────────
//
// While instance A holds the run-lease, instance B must receive ErrNotHolder.

func TestAcquireRunLease_SecondInstanceBlocked(t *testing.T) {
	b := newMockBackend()
	mgr := NewWithBackend(b)

	// Instance A acquires.
	rlA := acquireRunLeaseHelper(t, mgr, "default", "calculator", "node-A", 20*time.Second)
	if rlA == nil {
		t.Fatal("instance A: nil RunLease")
	}

	// Instance B tries to acquire the same lease.
	rlB, err := AcquireRunLease(context.Background(), mgr, "default", "calculator", "node-B", 20*time.Second)
	if !errors.Is(err, ErrNotHolder) {
		t.Errorf("instance B: want ErrNotHolder, got err=%v rl=%v", err, rlB)
	}
	if rlB != nil {
		t.Errorf("instance B: want nil RunLease, got non-nil (fence=%d)", rlB.Fence())
	}
}

// ── TestAcquireRunLease_HolderDeath_Failover ──────────────────────────────────
//
// Simulate holder death by seeding an expired lease.  A new instance must
// acquire the lease (clean failover).

func TestAcquireRunLease_HolderDeath_Failover(t *testing.T) {
	b := newMockBackend()
	mgr := NewWithBackend(b)

	scope := RunScope("default", "filemanager")

	// Seed an expired lease (stalled holder is "dead").
	expiredDoc := leaseDoc{
		State:     stateHeld,
		Holder:    "dead-node",
		Fence:     15,
		ExpiresAt: time.Now().Add(-2 * time.Minute),
	}
	data, _ := json.Marshal(expiredDoc)
	b.seed(objectKey(scope), data)

	// New instance acquires the expired lease — clean failover.
	rl, err := AcquireRunLease(context.Background(), mgr, "default", "filemanager", "new-node", 20*time.Second)
	if err != nil {
		t.Fatalf("failover Acquire: unexpected error: %v", err)
	}
	if rl == nil {
		t.Fatal("failover: nil RunLease")
	}
	// Fence must be > stalled fence (15).
	if rl.Fence() <= 15 {
		t.Errorf("fence: want > 15, got %d", rl.Fence())
	}
}

// ── TestRunLease_FencePreventsDoubleRun ───────────────────────────────────────
//
// While a RunLease is held and its renew loop is running, simulate a takeover
// (write a higher fence + new holder to the bucket).  The renew loop must
// detect ErrNotHolder and call onLost, which signals the stalled runner to stop.

func TestRunLease_FencePreventsDoubleRun(t *testing.T) {
	b := newMockBackend()
	mgr := NewWithBackend(b)

	ttl := 500 * time.Millisecond
	renewEvery := 30 * time.Millisecond

	rl, err := AcquireRunLease(context.Background(), mgr, "default", "editor", "node-A", ttl)
	if err != nil {
		t.Fatalf("AcquireRunLease: %v", err)
	}

	stopRenew := make(chan struct{})
	var lostCalled int32
	lostDone := make(chan struct{})

	rl.StartRenewLoop(context.Background(), ttl, renewEvery, stopRenew, func(err error) {
		atomic.StoreInt32(&lostCalled, 1)
		close(lostDone)
	})

	// Read the current lease doc and write a new one simulating a takeover.
	scope := RunScope("default", "editor")
	body, etag, err2 := b.getWithETag(context.Background(), objectKey(scope))
	if err2 != nil {
		t.Fatalf("read for takeover: %v", err2)
	}
	var doc leaseDoc
	_ = json.Unmarshal(body, &doc)

	takeoverDoc := leaseDoc{
		State:     stateHeld,
		Holder:    "takeover-node",
		Fence:     doc.Fence + 100,
		ExpiresAt: time.Now().Add(5 * time.Minute),
	}
	newBody, _ := json.Marshal(takeoverDoc)
	_, _ = b.putIfMatch(context.Background(), objectKey(scope), newBody, etag)

	// Wait for the renew loop to notice the fence loss.
	select {
	case <-lostDone:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout: renew loop did not call onLost after takeover")
	}

	if atomic.LoadInt32(&lostCalled) != 1 {
		t.Error("onLost was not called after fence takeover")
	}

	close(stopRenew) // clean up
}

// ── TestRunLease_Release_FlipsToFree ─────────────────────────────────────────

func TestRunLease_Release_FlipsToFree(t *testing.T) {
	b := newMockBackend()
	mgr := NewWithBackend(b)

	rl := acquireRunLeaseHelper(t, mgr, "default", "terminal", "node-A", 20*time.Second)
	scope := RunScope("default", "terminal")

	rl.Release(context.Background())

	body, _, err := b.getWithETag(context.Background(), objectKey(scope))
	if err != nil {
		t.Fatalf("post-release read: %v", err)
	}
	var doc leaseDoc
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if doc.State != stateFree {
		t.Errorf("state after Release: want free, got %s", doc.State)
	}
}

// ── TestRunLease_Renew_BumpsFence ─────────────────────────────────────────────

func TestRunLease_Renew_BumpsFence(t *testing.T) {
	b := newMockBackend()
	mgr := NewWithBackend(b)

	rl := acquireRunLeaseHelper(t, mgr, "work", "browser", "node-A", 20*time.Second)
	fenceBefore := rl.Fence()

	if err := rl.Renew(context.Background(), 20*time.Second); err != nil {
		t.Fatalf("Renew: %v", err)
	}
	if rl.Fence() <= fenceBefore {
		t.Errorf("fence not bumped by Renew: before=%d after=%d", fenceBefore, rl.Fence())
	}
}

// ── TestRunLease_ReplicatedBypass ─────────────────────────────────────────────
//
// The run-lease subsystem does not enforce any singleton constraint itself —
// that gating lives in the launcher.  This test confirms that AcquireRunLease
// can be called independently for different (profile, appID) pairs without
// contention, which is the behaviour non-singleton apps rely on (they simply
// don't call AcquireRunLease at all, but this documents the scope isolation).

func TestRunLease_ScopeIsolation(t *testing.T) {
	b := newMockBackend()
	mgr := NewWithBackend(b)

	// Two different apps in the same profile — they must not block each other.
	rlA := acquireRunLeaseHelper(t, mgr, "default", "app-one", "node-A", 20*time.Second)
	rlB := acquireRunLeaseHelper(t, mgr, "default", "app-two", "node-A", 20*time.Second)

	if rlA.Fence() < 1 {
		t.Errorf("app-one fence: want >= 1, got %d", rlA.Fence())
	}
	if rlB.Fence() < 1 {
		t.Errorf("app-two fence: want >= 1, got %d", rlB.Fence())
	}
}

// ── TestRunScope ──────────────────────────────────────────────────────────────

func TestRunScope(t *testing.T) {
	cases := []struct {
		profile, appID, want string
	}{
		{"default", "notes", "run/default/notes"},
		{"work", "browser", "run/work/browser"},
		{"home", "filemanager", "run/home/filemanager"},
	}
	for _, c := range cases {
		got := RunScope(c.profile, c.appID)
		if got != c.want {
			t.Errorf("RunScope(%q, %q) = %q, want %q", c.profile, c.appID, got, c.want)
		}
	}
}
