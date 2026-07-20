package gpuhost

import (
	"context"
	"testing"
	"time"
)

// TestStop_InterruptsRestartBackoff pins the fix for a supervisor that ignored
// Stop while it was sleeping between restarts.
//
// The supervise loop backs off exponentially up to SupervisorMaxBackoff (30s)
// after each child exit. Its backoff sleep used to be a single-case select on the
// timer, so a Stop arriving mid-sleep was not observed until the sleep ran out --
// even though the comment above it claimed the sleep was "responsive to Stop".
// Stop only waits SupervisorTerminationGrace (5s) for the loop to clear, so every
// such Stop burned the full grace and then returned while the goroutine was still
// alive for the remainder of the backoff.
//
// A shell that exits immediately, so the supervisor is reliably inside its backoff
// sleep when Stop lands.
func TestStop_InterruptsRestartBackoff(t *testing.T) {
	s := NewSupervisor("/bin/sh", []string{"-c", "exit 1"}, 10*time.Second)
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Let the child exit and the loop settle into its 10s backoff.
	deadline := time.Now().Add(2 * time.Second)
	for s.Alive() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if s.Alive() {
		t.Fatal("child never exited; test cannot reach the backoff path")
	}
	time.Sleep(50 * time.Millisecond)

	start := time.Now()
	if err := s.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	elapsed := time.Since(start)

	// Before the fix this took the full SupervisorTerminationGrace (5s) because
	// the loop could not clear doneCh until its 10s sleep expired.
	if elapsed > 2*time.Second {
		t.Fatalf("Stop took %v during backoff; it must interrupt the sleep, not wait it out", elapsed)
	}
}

// TestStop_IsIdempotent guards the close(stopCh) added above: a second Stop must
// not panic on an already-closed channel.
func TestStop_IsIdempotent(t *testing.T) {
	s := NewSupervisor("/bin/sh", []string{"-c", "exit 1"}, 10*time.Second)
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	if err := s.Stop(context.Background()); err != nil {
		t.Fatalf("first Stop: %v", err)
	}
	if err := s.Stop(context.Background()); err != nil {
		t.Fatalf("second Stop: %v", err)
	}
}
