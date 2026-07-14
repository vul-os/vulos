package sandbox

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// defaultPoolSize is the number of pre-warmed Python processes kept ready.
// Override with VULOS_SANDBOX_POOL_SIZE env var.
const defaultPoolSize = 3

// readinessTimeout is the maximum time to wait for a sandbox port to accept
// connections after the process has been handed a script.
const readinessTimeout = 15 * time.Second

// readinessPollInterval is how often to poll during the readiness probe.
const readinessPollInterval = 50 * time.Millisecond

// Script is a running AI-generated Python backend.
type Script struct {
	ID      string `json:"id"`
	Port    int    `json:"port"`
	File    string `json:"file"`
	Running bool   `json:"running"`
	cmd     *exec.Cmd
	done    chan struct{}
}

// warmProcess is a pre-spawned Python process waiting for a script to run.
type warmProcess struct {
	cmd   *exec.Cmd
	port  int
	stdin *os.File // write end of the pipe — we send the script path down this
	done  chan struct{}
}

// envSandboxEnabled is the opt-in env var that enables arbitrary AI code
// execution. MUST be set explicitly in environments that understand and accept
// the risk. When unset, Run() returns an error immediately (C2 fix).
//
// A prior substring blocklist (containsDangerousCode) was removed: it was NOT
// a security boundary and was trivially bypassed by obfuscation. Real
// isolation would require Linux namespaces + setpriv + seccomp (the same
// stack the app launcher uses). Until that is implemented, execution is
// disabled by default.
const envSandboxEnabled = "VULOS_SANDBOX_ENABLED"

// Sandbox manages ephemeral AI-generated scripts.
// The AI generates Python + HTML. Python runs as a backend on a port.
// The HTML viewport connects to it via /api/sandbox/{id}/ (proxied through gateway).
//
// SECURITY NOTE (C2): arbitrary code execution is disabled by default.
// Set VULOS_SANDBOX_ENABLED=1 to opt in.  This flag MUST NOT be set in
// production unless the host has kernel-level isolation (namespaces, seccomp)
// wrapping the Python processes.
type Sandbox struct {
	mu      sync.Mutex
	scripts map[string]*Script
	dir     string
	minPort int
	maxPort int
	enabled bool // false unless VULOS_SANDBOX_ENABLED=1

	// Pool of pre-warmed Python processes.
	pool         []*warmProcess
	poolSize     int
	poolMu       sync.Mutex
	launcherPath string // path to pool_launcher.py
	python       string // resolved python3 binary path
	poolCancel   context.CancelFunc
}

func New(dataDir string) *Sandbox {
	dir := filepath.Join(dataDir, "sandbox")
	os.MkdirAll(dir, 0755)

	// C2 fix: disabled by default; require explicit opt-in.
	enabled := strings.TrimSpace(os.Getenv(envSandboxEnabled)) == "1"
	if enabled {
		log.Printf("[sandbox] WARNING: arbitrary code execution is ENABLED (VULOS_SANDBOX_ENABLED=1). " +
			"Ensure kernel-level isolation (namespaces/seccomp) is active.")
	} else {
		log.Printf("[sandbox] code execution is disabled (VULOS_SANDBOX_ENABLED not set). " +
			"Set VULOS_SANDBOX_ENABLED=1 only in isolated environments.")
	}

	poolSize := defaultPoolSize
	if !enabled {
		// Pool is useless when disabled; suppress background goroutines.
		poolSize = 0
	} else if v := os.Getenv("VULOS_SANDBOX_POOL_SIZE"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			poolSize = n
		}
	}

	// Resolve python binary once.
	python := findPython()

	// The launcher script lives next to this binary's data dir.
	// We embed a copy into the sandbox dir so it is always available.
	launcherPath := filepath.Join(dir, "_pool_launcher.py")
	writeLauncherScript(launcherPath)

	ctx, cancel := context.WithCancel(context.Background())
	s := &Sandbox{
		scripts:      make(map[string]*Script),
		dir:          dir,
		minPort:      9100,
		maxPort:      9199,
		enabled:      enabled,
		poolSize:     poolSize,
		launcherPath: launcherPath,
		python:       python,
		poolCancel:   cancel,
	}

	// Fill the pool asynchronously so New() returns quickly.
	go s.maintainPool(ctx)

	return s
}

// Run saves a Python script to disk and executes it.
// If a warm process is available it is reused; otherwise a cold process is
// started. Either way a readiness probe confirms the port is listening before
// Run returns the URL.
// The script receives its port as env var VULOS_PORT.
// Returns the script info including the assigned port.
//
// Run returns an error immediately if VULOS_SANDBOX_ENABLED is not set (C2).
func (s *Sandbox) Run(ctx context.Context, id, code string) (*Script, error) {
	// C2 fix: gate on explicit opt-in flag. The old blocklist was not a
	// security boundary (trivially bypassed). Until kernel-level isolation
	// is integrated, refuse execution when the flag is absent.
	if !s.enabled {
		return nil, fmt.Errorf("sandbox: arbitrary code execution is disabled — " +
			"set VULOS_SANDBOX_ENABLED=1 in an isolated environment to enable (C2)")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Kill existing with same ID
	if existing, ok := s.scripts[id]; ok && existing.Running {
		s.kill(existing)
	}

	// Write script to disk
	scriptPath := filepath.Join(s.dir, id+".py")
	if err := os.WriteFile(scriptPath, []byte(code), 0644); err != nil {
		return nil, fmt.Errorf("write script: %w", err)
	}

	// Try to acquire a warm process from the pool.
	wp := s.acquireWarm()

	var (
		cmd  *exec.Cmd
		port int
	)

	if wp != nil {
		// Reuse the warm process: send it the script path via stdin.
		port = wp.port
		cmd = wp.cmd

		if _, err := fmt.Fprintf(wp.stdin, "%s\n", scriptPath); err != nil {
			// Warm process died before we could use it; fall back to cold start.
			log.Printf("[sandbox] warm process died before use, falling back: %v", err)
			wp.stdin.Close()
			wp = nil
		}
		if wp != nil {
			wp.stdin.Close() // EOF signals the launcher that no more input is coming
		}
	}

	if wp == nil {
		// Cold start.
		var err error
		port, err = s.findPort()
		if err != nil {
			os.Remove(scriptPath)
			return nil, err
		}
		python := s.python
		if python == "" {
			os.Remove(scriptPath)
			return nil, fmt.Errorf("python3 not found")
		}

		// Run with 5-minute timeout
		sandboxCtx, sandboxCancel := context.WithTimeout(ctx, 5*time.Minute)
		_ = sandboxCancel
		cmd = exec.CommandContext(sandboxCtx, python, scriptPath)
		cmd.Dir = s.dir
		cmd.Env = []string{
			"PATH=/usr/local/bin:/usr/bin:/bin",
			fmt.Sprintf("HOME=%s", s.dir),
			fmt.Sprintf("TMPDIR=%s", s.dir),
			fmt.Sprintf("VULOS_PORT=%d", port),
			fmt.Sprintf("PORT=%d", port),
		}
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

		if err := cmd.Start(); err != nil {
			os.Remove(scriptPath)
			return nil, fmt.Errorf("start script: %w", err)
		}
	}

	script := &Script{
		ID:      id,
		Port:    port,
		File:    scriptPath,
		Running: true,
		cmd:     cmd,
		done:    make(chan struct{}),
	}

	go func() {
		cmd.Wait()
		script.Running = false
		close(script.done)
		log.Printf("[sandbox] script %s exited", id)
		// Replenish the pool when a process finishes.
		s.poolMu.Lock()
		need := s.poolSize - len(s.pool)
		s.poolMu.Unlock()
		if need > 0 {
			go s.spawnWarm()
		}
	}()

	s.scripts[id] = script

	// Release the sandbox lock while we wait for readiness so that concurrent
	// Run calls can proceed.
	s.mu.Unlock()
	err := waitForPort(port, readinessTimeout)
	s.mu.Lock()

	if err != nil {
		// Script didn't become ready in time — kill it and report.
		s.kill(script)
		os.Remove(scriptPath)
		delete(s.scripts, id)
		return nil, fmt.Errorf("sandbox readiness timeout on port %d: %w", port, err)
	}

	log.Printf("[sandbox] script %s running on port %d (warm=%v)", id, port, wp != nil)
	return script, nil
}

// Stop kills a running script and cleans up.
func (s *Sandbox) Stop(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if script, ok := s.scripts[id]; ok {
		s.kill(script)
		os.Remove(script.File)
		delete(s.scripts, id)
	}
}

// Get returns a script by ID.
func (s *Sandbox) Get(id string) (*Script, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	script, ok := s.scripts[id]
	return script, ok
}

// List returns all scripts.
func (s *Sandbox) List() []*Script {
	s.mu.Lock()
	defer s.mu.Unlock()
	var result []*Script
	for _, sc := range s.scripts {
		result = append(result, sc)
	}
	return result
}

// StopAll kills everything — active scripts and pool processes.
func (s *Sandbox) StopAll() {
	// Cancel pool maintenance goroutine.
	if s.poolCancel != nil {
		s.poolCancel()
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	for id, sc := range s.scripts {
		s.kill(sc)
		os.Remove(sc.File)
		delete(s.scripts, id)
	}

	// Kill all warm pool processes.
	s.poolMu.Lock()
	defer s.poolMu.Unlock()
	for _, wp := range s.pool {
		killWarm(wp)
	}
	s.pool = nil
}

// ProxyPort returns the local port for a sandbox script (for the gateway to proxy to).
func (s *Sandbox) ProxyPort(id string) (int, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sc, ok := s.scripts[id]
	if !ok || !sc.Running {
		return 0, false
	}
	return sc.Port, true
}

// PoolLen returns the number of currently warm processes (for monitoring/tests).
func (s *Sandbox) PoolLen() int {
	s.poolMu.Lock()
	defer s.poolMu.Unlock()
	return len(s.pool)
}

// ---- internal helpers -------------------------------------------------------

func (s *Sandbox) kill(sc *Script) {
	if sc.cmd != nil && sc.cmd.Process != nil && sc.Running {
		syscall.Kill(-sc.cmd.Process.Pid, syscall.SIGTERM)
		select {
		case <-sc.done:
		case <-time.After(3 * time.Second):
			syscall.Kill(-sc.cmd.Process.Pid, syscall.SIGKILL)
		}
	}
}

func (s *Sandbox) findPort() (int, error) {
	used := make(map[int]bool)
	for _, sc := range s.scripts {
		used[sc.Port] = true
	}
	// Also mark ports claimed by warm pool processes.
	s.poolMu.Lock()
	for _, wp := range s.pool {
		used[wp.port] = true
	}
	s.poolMu.Unlock()

	for p := s.minPort; p <= s.maxPort; p++ {
		if used[p] {
			continue
		}
		// Check if port is actually free
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p))
		if err == nil {
			ln.Close()
			return p, nil
		}
	}
	return 0, fmt.Errorf("no free sandbox ports (%d-%d)", s.minPort, s.maxPort)
}

// acquireWarm removes and returns a warm process from the pool, or nil if empty.
func (s *Sandbox) acquireWarm() *warmProcess {
	s.poolMu.Lock()
	defer s.poolMu.Unlock()
	if len(s.pool) == 0 {
		return nil
	}
	wp := s.pool[len(s.pool)-1]
	s.pool = s.pool[:len(s.pool)-1]
	return wp
}

// maintainPool periodically ensures the pool has poolSize warm processes.
func (s *Sandbox) maintainPool(ctx context.Context) {
	if s.poolSize == 0 || s.python == "" {
		return
	}
	// Initial fill.
	for i := 0; i < s.poolSize; i++ {
		select {
		case <-ctx.Done():
			return
		default:
			go s.spawnWarm()
		}
	}
	// Periodic refill check every 5 seconds.
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.poolMu.Lock()
			need := s.poolSize - len(s.pool)
			s.poolMu.Unlock()
			for i := 0; i < need; i++ {
				go s.spawnWarm()
			}
		}
	}
}

// spawnWarm starts one pre-warmed Python process and adds it to the pool.
func (s *Sandbox) spawnWarm() {
	if s.python == "" || s.launcherPath == "" {
		return
	}

	s.mu.Lock()
	port, err := s.findPort()
	s.mu.Unlock()
	if err != nil {
		log.Printf("[sandbox] pool: no free port for warm process: %v", err)
		return
	}

	// Create a pipe: Go writes the script path, Python reads it.
	pr, pw, err := os.Pipe()
	if err != nil {
		log.Printf("[sandbox] pool: pipe error: %v", err)
		return
	}

	cmd := exec.Command(s.python, s.launcherPath)
	cmd.Dir = s.dir
	cmd.Env = []string{
		"PATH=/usr/local/bin:/usr/bin:/bin",
		fmt.Sprintf("HOME=%s", s.dir),
		fmt.Sprintf("TMPDIR=%s", s.dir),
		fmt.Sprintf("VULOS_PORT=%d", port),
		fmt.Sprintf("PORT=%d", port),
	}
	cmd.Stdin = pr // launcher reads from here
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		pr.Close()
		pw.Close()
		log.Printf("[sandbox] pool: start warm process: %v", err)
		return
	}
	pr.Close() // parent only writes, child reads

	wp := &warmProcess{
		cmd:   cmd,
		port:  port,
		stdin: pw,
		done:  make(chan struct{}),
	}

	go func() {
		cmd.Wait()
		close(wp.done)
		// Remove from pool if it dies unexpectedly before being acquired.
		s.poolMu.Lock()
		for i, p := range s.pool {
			if p == wp {
				s.pool = append(s.pool[:i], s.pool[i+1:]...)
				break
			}
		}
		s.poolMu.Unlock()
	}()

	s.poolMu.Lock()
	s.pool = append(s.pool, wp)
	s.poolMu.Unlock()
	log.Printf("[sandbox] pool: warm process ready on port %d (pool size %d)", port, len(s.pool))
}

// killWarm terminates a warm process without waiting (used during StopAll).
func killWarm(wp *warmProcess) {
	if wp.stdin != nil {
		wp.stdin.Close()
	}
	if wp.cmd != nil && wp.cmd.Process != nil {
		syscall.Kill(-wp.cmd.Process.Pid, syscall.SIGTERM)
		select {
		case <-wp.done:
		case <-time.After(2 * time.Second):
			syscall.Kill(-wp.cmd.Process.Pid, syscall.SIGKILL)
		}
	}
}

// waitForPort polls until the given TCP port accepts connections or the timeout elapses.
func waitForPort(port int, timeout time.Duration) error {
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, readinessPollInterval)
		if err == nil {
			conn.Close()
			return nil
		}
		time.Sleep(readinessPollInterval)
	}
	return fmt.Errorf("port %d not ready after %s", port, timeout)
}

// writeLauncherScript writes the trusted pool_launcher.py to disk.
// This is the bootstrap that warm processes run while waiting for a script path.
func writeLauncherScript(path string) {
	const launcher = `"""
Vula OS Sandbox Pool Launcher — trusted bootstrap, NOT user code.
Reads a single line from stdin: the path to the user script.
Then executes it in the current process, inheriting VULOS_PORT / PORT
from the environment already set by the Go host.
"""
import os
import sys

def main():
    line = sys.stdin.readline().rstrip("\n")
    if not line:
        sys.exit(1)
    script_path = line
    if not os.path.isfile(script_path):
        sys.stderr.write(f"pool_launcher: script not found: {script_path}\n")
        sys.exit(1)
    with open(script_path, "r") as f:
        source = f.read()
    g = {
        "__name__": "__main__",
        "__file__": script_path,
        "__builtins__": __builtins__,
    }
    code = compile(source, script_path, "exec")
    exec(code, g)

if __name__ == "__main__":
    main()
`
	// Ignore write errors (e.g., read-only fs) — cold start still works.
	_ = os.WriteFile(path, []byte(launcher), 0644)
}

func findPython() string {
	for _, name := range []string{"python3", "python"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	return ""
}
