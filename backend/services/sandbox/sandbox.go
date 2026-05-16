package sandbox

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Script is a running AI-generated Python backend.
type Script struct {
	ID      string `json:"id"`
	Port    int    `json:"port"`
	File    string `json:"file"`
	Running bool   `json:"running"`
	cmd     *exec.Cmd
	done    chan struct{}
}

// Sandbox manages ephemeral AI-generated scripts.
// The AI generates Python + HTML. Python runs as a backend on a port.
// The HTML viewport connects to it via /api/sandbox/{id}/ (proxied through gateway).
type Sandbox struct {
	mu      sync.Mutex
	scripts map[string]*Script
	dir     string
	minPort int
	maxPort int
}

func New(dataDir string) *Sandbox {
	dir := filepath.Join(dataDir, "sandbox")
	os.MkdirAll(dir, 0755)
	return &Sandbox{
		scripts: make(map[string]*Script),
		dir:     dir,
		minPort: 9100,
		maxPort: 9199,
	}
}

// Run saves a Python script to disk and executes it with a free port.
// The script receives its port as env var VULOS_PORT.
// Returns the script info including the assigned port.
func (s *Sandbox) Run(ctx context.Context, id, code string) (*Script, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Kill existing with same ID
	if existing, ok := s.scripts[id]; ok && existing.Running {
		s.kill(existing)
	}

	// Find free port
	port, err := s.findPort()
	if err != nil {
		return nil, err
	}

	// Write script to disk
	scriptPath := filepath.Join(s.dir, id+".py")
	if err := os.WriteFile(scriptPath, []byte(code), 0644); err != nil {
		return nil, fmt.Errorf("write script: %w", err)
	}

	// Validate: reject obviously dangerous code
	if containsDangerousCode(code) {
		return nil, fmt.Errorf("script contains disallowed operations")
	}

	// Find Python
	python := findPython()
	if python == "" {
		return nil, fmt.Errorf("python3 not found")
	}

	// Run with 5-minute timeout
	sandboxCtx, sandboxCancel := context.WithTimeout(ctx, 5*time.Minute)
	_ = sandboxCancel // stored in script for cleanup
	cmd := exec.CommandContext(sandboxCtx, python, scriptPath)
	cmd.Dir = s.dir
	// Minimal scrubbed environment — never inherit os.Environ() to avoid
	// leaking secrets (API keys, tokens, etc.) present in the server process.
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
		return nil, fmt.Errorf("start script: %w", err)
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
	}()

	s.scripts[id] = script
	log.Printf("[sandbox] script %s running on port %d", id, port)
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

// StopAll kills everything.
func (s *Sandbox) StopAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, sc := range s.scripts {
		s.kill(sc)
		os.Remove(sc.File)
		delete(s.scripts, id)
	}
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

func findPython() string {
	for _, name := range []string{"python3", "python"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	return ""
}

// containsDangerousCode checks for obviously harmful patterns.
// Not a full sandbox — defense in depth alongside process isolation.
func containsDangerousCode(code string) bool {
	// Direct dangerous calls
	dangerous := []string{
		"subprocess", "os.system", "os.popen",
		"os.exec", "os.spawn", "shutil.rmtree",
		"eval(", "exec(",
		"rm -rf", ":(){ :|:",
	}
	// Import/introspection bypasses
	bypasses := []string{
		"__import__", "importlib", "getattr(",
		"base64.b64decode", "codecs.decode",
		"compile(", "globals()", "locals()",
		"__builtins__", "__subclasses__",
		"chr(", "bytearray(",
		"ctypes", "cffi", "multiprocessing",
		"signal.signal", "pty.",
	}
	// Filesystem access outside data dir
	fsAccess := []string{
		"open('/etc", "open('/proc", "open('/sys",
		"open('/dev", "open('/root", "open('/home",
		"open('/var", "open('/tmp",
		"pathlib.Path('/'",
	}

	lower := strings.ToLower(code)
	for _, lists := range [][]string{dangerous, bypasses, fsAccess} {
		for _, d := range lists {
			if strings.Contains(lower, strings.ToLower(d)) {
				return true
			}
		}
	}
	if len(code) > 100*1024 {
		return true
	}
	return false
}
