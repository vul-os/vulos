//go:build linux

// Package labwc manages the labwc Wayland compositor lifecycle for the v2
// bare-metal window model (D93 BMINIT-17).
//
// In v2 opt-in mode labwc is spawned as the DRM compositor instead of cage.
// Unlike cage (single-app kiosk), labwc supports multiple xdg-toplevels so
// native Linux apps can open real windows alongside the React shell browser.
//
// Lifecycle:
//  1. Start() spawns labwc with a well-known per-session Wayland socket and
//     waits for the socket to appear (up to socketTimeout).
//  2. The caller then launches the shell browser (Cog/Chromium) as an
//     xdg-toplevel under labwc — it appears as a normal window.  labwc's
//     rc.xml pins the browser to the background layer so native app windows
//     float on top.
//  3. Stop() sends SIGTERM to labwc and removes the runtime dir.
//
// labwc must be on PATH; when absent Start() returns ErrNotFound and the
// caller must fall back to the v1 cage path.
package labwc

import (
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// ErrNotFound is returned by Start when the labwc binary cannot be found.
var ErrNotFound = errors.New("labwc not found on PATH")

const (
	// socketTimeout is how long we wait for labwc's Wayland socket to appear.
	socketTimeout = 10 * time.Second
	// socketPoll is the polling interval while waiting.
	socketPoll = 50 * time.Millisecond
)

// Manager holds the running labwc process and associated runtime state.
type Manager struct {
	// WaylandDisplay is the Wayland socket name clients should connect to,
	// e.g. "wayland-vulos-1".
	WaylandDisplay string
	// XDGRuntimeDir is the runtime directory housing the socket.
	XDGRuntimeDir string

	cmd        *exec.Cmd
	runtimeDir string
	socketPath string
}

// Start spawns labwc with the supplied environment and config path, then
// blocks until labwc's Wayland socket is ready (or timeout/error).
//
// env is a complete environment slice (e.g. built from os.Environ() + extra
// vars).  cfgPath is passed to labwc via -C; pass "" to use labwc's default.
//
// On success m.WaylandDisplay and m.XDGRuntimeDir are set and can be passed
// to child processes.
func Start(env []string, cfgPath string) (*Manager, error) {
	bin, err := exec.LookPath("labwc")
	if err != nil {
		return nil, ErrNotFound
	}

	// Unique runtime dir and socket name so multiple sessions don't clash.
	runtimeDir := "/run/user/0"
	if err := os.MkdirAll(runtimeDir, 0o700); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", runtimeDir, err)
	}

	waylandDisplay := "wayland-vulos-1"
	socketPath := filepath.Join(runtimeDir, waylandDisplay)

	// Remove a stale socket if one exists from a previous crash.
	_ = os.Remove(socketPath)

	args := []string{}
	if cfgPath != "" {
		args = append(args, "-C", cfgPath)
	}

	cmd := exec.Command(bin, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	// Inject the socket name into labwc's env so it knows where to bind.
	composed := make([]string, 0, len(env)+3)
	for _, e := range env {
		composed = append(composed, e)
	}
	composed = append(composed,
		"WAYLAND_DISPLAY="+waylandDisplay,
		"XDG_RUNTIME_DIR="+runtimeDir,
	)
	cmd.Env = composed

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start labwc: %w", err)
	}
	log.Printf("[labwc] compositor started (pid=%d, socket=%s)", cmd.Process.Pid, socketPath)

	m := &Manager{
		WaylandDisplay: waylandDisplay,
		XDGRuntimeDir:  runtimeDir,
		cmd:            cmd,
		runtimeDir:     runtimeDir,
		socketPath:     socketPath,
	}

	if err := m.waitForSocket(); err != nil {
		_ = cmd.Process.Kill()
		return nil, fmt.Errorf("labwc socket did not appear: %w", err)
	}
	log.Printf("[labwc] Wayland socket ready: %s", socketPath)
	return m, nil
}

// waitForSocket polls for the labwc Wayland socket file, returning nil when
// it exists or an error when the timeout elapses.
func (m *Manager) waitForSocket() error {
	deadline := time.Now().Add(socketTimeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(m.socketPath); err == nil {
			return nil
		}
		// Also check if labwc exited early (crash at startup).
		if m.cmd.ProcessState != nil {
			return fmt.Errorf("labwc exited prematurely (code=%d)", m.cmd.ProcessState.ExitCode())
		}
		time.Sleep(socketPoll)
	}
	return fmt.Errorf("timed out after %s", socketTimeout)
}

// Stop sends SIGTERM to labwc and waits for it to exit.  The runtime socket
// file is removed on return.  Safe to call on a nil Manager.
func (m *Manager) Stop() {
	if m == nil || m.cmd == nil || m.cmd.Process == nil {
		return
	}
	log.Printf("[labwc] stopping compositor (pid=%d)", m.cmd.Process.Pid)
	_ = m.cmd.Process.Signal(os.Interrupt) // SIGINT → labwc graceful exit
	done := make(chan struct{})
	go func() {
		_ = m.cmd.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		log.Printf("[labwc] graceful stop timed out — killing pid=%d", m.cmd.Process.Pid)
		_ = m.cmd.Process.Kill()
		<-done
	}
	_ = os.Remove(m.socketPath)
	log.Printf("[labwc] compositor stopped")
}

// Pid returns the PID of the labwc process, or 0 if not running.
func (m *Manager) Pid() int {
	if m == nil || m.cmd == nil || m.cmd.Process == nil {
		return 0
	}
	return m.cmd.Process.Pid
}
