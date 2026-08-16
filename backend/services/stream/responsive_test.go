package stream

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"vulos/backend/internal/proctl"
)

// A Wayland session must not be probed over X. cage sessions get no DISPLAY at
// all — services/stream sets WAYLAND_DISPLAY and never puts DISPLAY in the
// app's environment — so the display number the session was allocated names an
// X server that was never started. Probing it would return
// display_not_responding for every GPU-path session on the box: a confident,
// permanent, entirely wrong badge.
func TestSessionResponsiveness_WaylandSessionIsNotProbedOverX(t *testing.T) {
	dialed := false
	orig := x11Socket
	x11Socket = func(n int) string { dialed = true; return orig(n) }
	t.Cleanup(func() { x11Socket = orig })

	s := &Session{Running: true, displayNum: 11, cage: &exec.Cmd{}, cageRTDir: "/tmp/rt"}
	got := s.Responsiveness(context.Background())

	if dialed {
		t.Error("a cage session had its X socket path computed; the X probe must not run on the Wayland path")
	}
	if got.Status != proctl.StatusUnknown {
		t.Fatalf("Status = %q, want %q (detail: %s)", got.Status, proctl.StatusUnknown, got.Detail)
	}
	if got.Method == proctl.MethodX11Ping {
		t.Errorf("Method = %q — no ping was sent and none could be", got.Method)
	}
	if !strings.Contains(got.Detail, "compositor") {
		t.Errorf("detail %q does not explain why the Wayland path cannot be asked", got.Detail)
	}
}

func TestSessionResponsiveness_StoppedSessionClaimsNothing(t *testing.T) {
	s := &Session{Running: false, displayNum: 12, xvfb: &exec.Cmd{}}
	got := s.Responsiveness(context.Background())
	if got.Status != proctl.StatusUnknown {
		t.Fatalf("Status = %q, want %q", got.Status, proctl.StatusUnknown)
	}
}

// A session whose display never came up is not an app verdict either.
func TestSessionResponsiveness_NoDisplayAtAllIsUnknown(t *testing.T) {
	s := &Session{Running: true, displayNum: 13}
	got := s.Responsiveness(context.Background())
	if got.Status != proctl.StatusUnknown {
		t.Fatalf("Status = %q, want %q", got.Status, proctl.StatusUnknown)
	}
}

// The X11 path routes to the real ping and dials the path this package
// computes. The socket here accepts the connection and then says nothing —
// which is what a wedged X server looks like from outside — so the expected
// answer is the DISPLAY status, and the method must show a ping was the
// mechanism attempted.
func TestSessionResponsiveness_X11SessionUsesThePing(t *testing.T) {
	dir, err := os.MkdirTemp("", "xs")
	if err != nil {
		t.Fatalf("tempdir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "X0")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c // accepted and ignored: a stalled server, not a closed one
		}
	}()

	var asked int
	orig := x11Socket
	x11Socket = func(n int) string { asked = n; return sock }
	t.Cleanup(func() { x11Socket = orig })

	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	s := &Session{Running: true, displayNum: 14, xvfb: &exec.Cmd{}}
	got := s.Responsiveness(ctx)

	if asked != 14 {
		t.Errorf("probe asked for display %d, want the session's own display 14", asked)
	}
	if got.Method != proctl.MethodX11Ping {
		t.Fatalf("Method = %q, want %q (detail: %s)", got.Method, proctl.MethodX11Ping, got.Detail)
	}
	if got.Status != proctl.StatusDisplayNotResponding {
		t.Fatalf("Status = %q, want %q (detail: %s)", got.Status, proctl.StatusDisplayNotResponding, got.Detail)
	}
	if got.Status == proctl.StatusNotResponding {
		t.Error("a server that never completed a handshake was blamed on the app")
	}
}

// The probe must dial the socket Xvfb actually creates. `Xvfb :12` creates
// /tmp/.X11-unix/X12; this is the one place that mapping is written down, and
// getting it wrong yields a probe that reports every session's display as dead.
func TestX11SocketPathMatchesWhatXvfbCreates(t *testing.T) {
	if got := x11Socket(12); got != "/tmp/.X11-unix/X12" {
		t.Fatalf("x11Socket(12) = %q, want /tmp/.X11-unix/X12", got)
	}
}

// One definition of the socket path, enforced. The launch-time readiness wait,
// Stop's cleanup and the probe must agree; a private copy in any of them is a
// check that can drift away from the thing it checks.
func TestX11SocketPathHasOneDefinition(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	var offenders []string
	found := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		src, err := os.ReadFile(e.Name())
		if err != nil {
			continue
		}
		for i, line := range strings.Split(string(src), "\n") {
			if !strings.Contains(line, ".X11-unix") {
				continue
			}
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "//") {
				continue
			}
			found++
			if e.Name() != "responsive.go" && e.Name() != "responsive_test.go" {
				offenders = append(offenders, e.Name()+":"+itoa(i+1)+": "+trimmed)
			}
		}
	}
	// A real count, not >0: if the scan stopped finding the literal entirely,
	// this gate would pass while checking nothing.
	if found < 2 {
		t.Fatalf("the scan found the socket literal %d times; it is not reading this package", found)
	}
	if len(offenders) > 0 {
		t.Errorf("the X11 socket path is spelled out away from x11Socket. Use the helper, or the "+
			"probe and the thing it probes can drift apart:\n%s", strings.Join(offenders, "\n"))
	}
}
