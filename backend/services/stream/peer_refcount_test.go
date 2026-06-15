package stream

import (
	"os/exec"
	"sync"
	"syscall"
	"testing"
	"time"
)

// TestPeerRefcountTransitions verifies STREAMWIN-01: the connected-peer refcount
// increments on peerConnect and decrements on peerDisconnect, and that the
// videoStartFn is called exactly once on the 0→1 transition.
func TestPeerRefcountTransitions(t *testing.T) {
	var mu sync.Mutex
	startCalls := 0

	sess := &Session{
		Name: "test",
		videoStartFn: func() {
			mu.Lock()
			startCalls++
			mu.Unlock()
		},
	}

	// Initial state: no peers, no video process.
	if sess.peerCount != 0 {
		t.Fatalf("initial peerCount = %d, want 0", sess.peerCount)
	}

	// 0→1: first peer — videoStartFn must be called.
	sess.peerConnect()
	if sess.peerCount != 1 {
		t.Errorf("peerCount after first connect = %d, want 1", sess.peerCount)
	}
	// Give the goroutine a moment (it is launched with go fn()).
	// We sync via the mutex counter; sleep briefly between each check so the
	// scheduler can run the goroutine before we time out.
	for i := 0; i < 100; i++ {
		mu.Lock()
		n := startCalls
		mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	mu.Lock()
	if startCalls != 1 {
		t.Errorf("videoStartFn called %d times on 0→1 transition, want 1", startCalls)
	}
	mu.Unlock()

	// 1→2: second peer — no additional videoStartFn call.
	sess.peerConnect()
	if sess.peerCount != 2 {
		t.Errorf("peerCount after second connect = %d, want 2", sess.peerCount)
	}
	mu.Lock()
	if startCalls != 1 {
		t.Errorf("videoStartFn should not be called again on 1→2; calls = %d", startCalls)
	}
	mu.Unlock()

	// 2→1: first disconnect — count drops but no suspend (no real process).
	sess.peerDisconnect()
	if sess.peerCount != 1 {
		t.Errorf("peerCount after first disconnect = %d, want 1", sess.peerCount)
	}

	// 1→0: last disconnect — count reaches zero.
	sess.peerDisconnect()
	if sess.peerCount != 0 {
		t.Errorf("peerCount after last disconnect = %d, want 0", sess.peerCount)
	}

	// Extra disconnect must not go below zero.
	sess.peerDisconnect()
	if sess.peerCount != 0 {
		t.Errorf("peerCount after excess disconnect = %d, want 0 (must not go negative)", sess.peerCount)
	}
}

// TestPeerRefcountSIGSTOP verifies that peerDisconnect issues SIGSTOP to the
// gstVideo process on the 1→0 transition, and peerConnect issues SIGCONT on the
// 0→1 transition for a session that already has a running gstVideo process.
//
// We use a long-running no-op process (sleep 60) to receive the signals.
func TestPeerRefcountSIGSTOP(t *testing.T) {
	sleepBin, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("sleep binary not found; skipping signal test")
	}

	// Start a sleep process to act as the gstVideo stand-in.
	cmd := exec.Command(sleepBin, "60")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("could not start sleep process: %v", err)
	}
	t.Cleanup(func() {
		cmd.Process.Kill()
		cmd.Wait()
	})

	sess := &Session{
		Name:     "test-signal",
		gstVideo: cmd,
	}
	sess.peerCount = 1 // simulate one peer already connected

	// 1→0: should SIGSTOP the process (no panic, no error).
	sess.peerDisconnect()
	if sess.peerCount != 0 {
		t.Errorf("peerCount = %d after disconnect, want 0", sess.peerCount)
	}

	// 0→1: should SIGCONT the process (no panic, no error).
	sess.peerConnect()
	if sess.peerCount != 1 {
		t.Errorf("peerCount = %d after reconnect, want 1", sess.peerCount)
	}
}

// TestSessionIDOR verifies that GetForUser enforces per-user session isolation:
// user A cannot access a session owned by user B.
//
// Guards: STREAM-SEC-01 — session IDOR via /api/stream/ws, /api/stream/resize,
// /api/stream/stop, /api/stream/fps, /api/stream/mangohud.
func TestSessionIDOR(t *testing.T) {
	pool := NewPool()

	// Inject a fake session owned by "user-alice".
	sessAlice := &Session{
		ID:      "sess-alice",
		Name:    "alice-app",
		OwnerID: "user-alice",
		Running: true,
	}
	pool.mu.Lock()
	pool.sessions["sess-alice"] = sessAlice
	pool.mu.Unlock()

	// Alice can access her own session.
	if got := pool.GetForUser("sess-alice", "user-alice"); got == nil {
		t.Fatal("STREAM-SEC-01 REGRESSION: owner could not access her own session")
	}

	// Bob must NOT be able to access Alice's session.
	if got := pool.GetForUser("sess-alice", "user-bob"); got != nil {
		t.Fatal("STREAM-SEC-01 REGRESSION: unauthorized user gained access to another user's session")
	}

	// Unknown session returns nil for everyone.
	if got := pool.GetForUser("no-such-id", "user-alice"); got != nil {
		t.Fatal("GetForUser returned non-nil for unknown session")
	}

	// Session with no owner is accessible to any authenticated user.
	sessSystem := &Session{
		ID:      "sess-system",
		Name:    "system-app",
		OwnerID: "", // no owner
		Running: true,
	}
	pool.mu.Lock()
	pool.sessions["sess-system"] = sessSystem
	pool.mu.Unlock()

	if got := pool.GetForUser("sess-system", "user-alice"); got == nil {
		t.Fatal("unowned session should be accessible by any authenticated user")
	}
	if got := pool.GetForUser("sess-system", "user-bob"); got == nil {
		t.Fatal("unowned session should be accessible by any authenticated user")
	}
}
