package stream

import (
	"sync"
	"testing"
)

// TestLaunchConcurrentSameIDNoOrphan verifies the check-then-insert TOCTOU fix:
// two concurrent Launch calls for the same explicit ID must resolve to exactly
// one registered session, never two — the second caller dedupes against the
// reservation placeholder installed by the first instead of building a second,
// orphaning session.
//
// Launch cannot fully succeed in CI (no Xvfb/GStreamer binaries), so it returns
// an error after reserving+releasing. That is fine for this test: what we assert
// is the registry invariant. We drive the race by pre-installing a session under
// the target ID (simulating "the first launch already reserved it"), then firing
// many concurrent Launches with that same ID. Every one must return the SAME
// existing pointer via the dedup fast-path, and the pool must still hold exactly
// that one session — proving the check+return is atomic and no launch overwrote
// or orphaned the reservation.
func TestLaunchConcurrentSameIDNoOrphan(t *testing.T) {
	p := NewPool()

	const id = "explicit-dup-id"
	reserved := &Session{ID: id, Name: "pre-reserved"}
	p.mu.Lock()
	p.sessions[id] = reserved
	p.mu.Unlock()

	const callers = 32
	var start sync.WaitGroup
	start.Add(1)
	var wg sync.WaitGroup
	wg.Add(callers)

	results := make([]*Session, callers)
	errs := make([]error, callers)

	// STOP whatever actually started. This test asserts on Launch's reservation
	// behaviour and used to walk away from any session that got as far as
	// starting Xvfb — leaving it running for the remainder of `go test ./...`,
	// which is where the orphaned Xvfb in CI's job summary comes from. A test
	// that leaks a process leaks it into every package that runs after it.
	t.Cleanup(func() {
		for _, sess := range results {
			if sess != nil {
				sess.Stop()
			}
		}
	})

	for i := 0; i < callers; i++ {
		go func(idx int) {
			defer wg.Done()
			start.Wait()
			results[idx], errs[idx] = p.Launch(LaunchOpts{
				ID:      id,
				Name:    "dup",
				Command: "/nonexistent/binary",
			})
		}(i)
	}

	start.Done()
	wg.Wait()

	// Every caller must have deduped to the exact reserved pointer with no error.
	for i := 0; i < callers; i++ {
		if errs[i] != nil {
			t.Fatalf("caller %d: unexpected error %v (expected dedup to existing session)", i, errs[i])
		}
		if results[i] != reserved {
			t.Fatalf("caller %d: got session %p, want the reserved %p — a duplicate session was launched (orphan)", i, results[i], reserved)
		}
	}

	// The pool must still hold exactly one session under the ID: the original.
	p.mu.Lock()
	got := p.sessions[id]
	n := len(p.sessions)
	p.mu.Unlock()
	if got != reserved {
		t.Fatalf("registry entry for %q = %p, want reserved %p (it was overwritten)", id, got, reserved)
	}
	if n != 1 {
		t.Fatalf("pool holds %d sessions, want 1 (no orphans)", n)
	}
}

// TestLaunchReservationReleasedOnFailure verifies that when a launch fails after
// reserving the ID (e.g. Xvfb/app cannot start in CI), the reservation is
// released so the ID is not permanently squatted and can be retried.
func TestLaunchReservationReleasedOnFailure(t *testing.T) {
	p := NewPool()

	// This will reserve "retry-me", then fail at the Xvfb/app stage (binaries
	// absent in CI) and release the reservation.
	_, err := p.Launch(LaunchOpts{ID: "retry-me", Name: "x", Command: "/nonexistent/binary"})
	if err == nil {
		t.Skip("Launch unexpectedly succeeded (test host has full streaming stack); skipping release assertion")
	}

	p.mu.Lock()
	_, stillReserved := p.sessions["retry-me"]
	p.mu.Unlock()
	if stillReserved {
		t.Fatal("failed Launch left its reservation behind — ID is permanently squatted")
	}
}
