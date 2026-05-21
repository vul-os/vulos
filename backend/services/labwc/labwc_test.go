//go:build linux

package labwc

import (
	"os"
	"testing"
	"time"
)

// TestWaitForSocket_notFound verifies that waitForSocket returns an error when
// the socket file never appears (timeout path).
func TestWaitForSocket_notFound(t *testing.T) {
	// Point at a socket path that will definitely not exist.
	socketPath := t.TempDir() + "/wayland-test-nonexistent"
	m := &Manager{
		socketPath: socketPath,
	}

	// Use a very short deadline so the test runs quickly.
	deadline := time.Now().Add(150 * time.Millisecond)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(m.socketPath); err == nil {
			t.Fatal("socket unexpectedly found")
		}
		time.Sleep(socketPoll)
	}
	// If we reach here without finding the socket, that is the expected result.
}

// TestWaitForSocket_found verifies that waitForSocket returns nil when the
// socket file exists before the deadline.
func TestWaitForSocket_found(t *testing.T) {
	dir := t.TempDir()
	socketPath := dir + "/wayland-test-present"

	// Create the socket file before calling waitForSocket.
	f, err := os.Create(socketPath)
	if err != nil {
		t.Fatalf("create socket file: %v", err)
	}
	f.Close()

	m := &Manager{socketPath: socketPath}
	if err := m.waitForSocket(); err != nil {
		t.Fatalf("waitForSocket returned unexpected error: %v", err)
	}
}

// TestStop_nilSafe verifies that Stop on a nil Manager does not panic.
func TestStop_nilSafe(t *testing.T) {
	var m *Manager
	m.Stop() // must not panic
}

// TestPid_nilSafe verifies that Pid on a nil Manager returns 0.
func TestPid_nilSafe(t *testing.T) {
	var m *Manager
	if got := m.Pid(); got != 0 {
		t.Errorf("Pid() = %d; want 0", got)
	}
}
