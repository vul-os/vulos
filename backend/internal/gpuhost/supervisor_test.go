// supervisor_test.go — process-supervisor lifecycle tests.
//
// These tests substitute the external streamer binary with a tiny shell
// script written into a tempdir, so they run on any POSIX dev host without
// needing Sunshine / Moonlight installed.

package gpuhost

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// writeFakeStreamer writes a shell script that sleeps until killed (or until
// the given exitAfter elapses) and returns its path.
//
// behaviour:
//   - "sleep": runs `sleep 60` and exits 0 if killed.
//   - "crash": exits 1 immediately.
//   - "exit-then-sleep": exits 1 once, then on respawn sleeps.
func writeFakeStreamer(t *testing.T, behaviour string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("supervisor uses POSIX signals; skipping on windows")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "fake-streamer.sh")
	stateFile := filepath.Join(dir, "state")

	var script string
	switch behaviour {
	case "crash":
		script = "#!/bin/sh\nexit 1\n"
	case "exit-then-sleep":
		script = "#!/bin/sh\n" +
			"if [ ! -f " + stateFile + " ]; then\n" +
			"  touch " + stateFile + "\n" +
			"  exit 1\n" +
			"fi\n" +
			"trap 'exit 0' TERM\n" +
			"sleep 60 &\n" +
			"wait\n"
	default: // "sleep"
		script = "#!/bin/sh\ntrap 'exit 0' TERM\nsleep 60 &\nwait\n"
	}
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake streamer: %v", err)
	}
	return path
}

// waitForAlive polls Alive() until it matches want or until timeout.
func waitForAlive(t *testing.T, s *Supervisor, want bool, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if s.Alive() == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("supervisor.Alive() = %v, want %v after %s", s.Alive(), want, timeout)
}

func TestSupervisor_StartStop_HappyPath(t *testing.T) {
	bin := writeFakeStreamer(t, "sleep")
	sup := NewSupervisor(bin, nil, 50*time.Millisecond)

	if err := sup.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForAlive(t, sup, true, 2*time.Second)

	if got := sup.Starts(); got != 1 {
		t.Errorf("Starts = %d, want 1", got)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := sup.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if sup.Alive() {
		t.Errorf("supervisor still Alive() after Stop")
	}
}

func TestSupervisor_RestartsOnCrash(t *testing.T) {
	// "exit-then-sleep" exits 1 on first run, sleeps on the second. The
	// supervisor must observe the exit, back off, and respawn — driving
	// Restarts() to ≥1.
	bin := writeFakeStreamer(t, "exit-then-sleep")
	sup := NewSupervisor(bin, nil, 20*time.Millisecond)

	if err := sup.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = sup.Stop(ctx)
	})

	// Wait for at least one restart.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if sup.Restarts() >= 1 && sup.Alive() {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if sup.Restarts() < 1 {
		t.Fatalf("expected ≥1 restart, got %d", sup.Restarts())
	}
	if sup.Exits() < 1 {
		t.Fatalf("expected ≥1 exit, got %d", sup.Exits())
	}
	if !sup.Alive() {
		t.Fatalf("expected supervisor Alive after restart")
	}
}

func TestSupervisor_StartTwiceIsError(t *testing.T) {
	bin := writeFakeStreamer(t, "sleep")
	sup := NewSupervisor(bin, nil, 20*time.Millisecond)
	if err := sup.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = sup.Stop(ctx)
	}()
	if err := sup.Start(context.Background()); err == nil {
		t.Fatal("second Start should return error")
	}
}

func TestSupervisor_StopBeforeStart(t *testing.T) {
	sup := NewSupervisor("/no/such/bin", nil, 20*time.Millisecond)
	if err := sup.Stop(context.Background()); err != nil {
		t.Fatalf("Stop on un-started supervisor: %v", err)
	}
}

func TestSupervisor_ExecFailureSurfaces(t *testing.T) {
	sup := NewSupervisor("/no/such/binary-zzz", nil, 20*time.Millisecond)
	if err := sup.Start(context.Background()); err == nil {
		t.Fatal("Start with non-existent binary should error")
	}
}
