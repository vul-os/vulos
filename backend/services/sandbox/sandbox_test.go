package sandbox

import (
	"context"
	"net"
	"os"
	"strings"
	"testing"
	"time"
)

// ---- containsDangerousCode --------------------------------------------------
//
// NOTE (C2): containsDangerousCode is NOT a security boundary. It is a
// best-effort heuristic to catch accidental mistakes by the AI model. Real
// security requires kernel-level isolation (namespaces, seccomp). The sandbox
// is disabled by default (VULOS_SANDBOX_ENABLED=1 required). These tests
// document the heuristic's coverage but must NOT be used to justify the
// blocklist as an isolation mechanism.

func TestContainsDangerousCode_safe(t *testing.T) {
	safe := []string{
		`import flask\napp = Flask(__name__)\n@app.route('/')\ndef index():\n    return 'hello'\napp.run(port=int(os.environ['VULOS_PORT']))`,
		`import http.server\nimport os\nport = int(os.environ.get('VULOS_PORT', 8080))`,
	}
	for _, code := range safe {
		if containsDangerousCode(code) {
			t.Errorf("heuristic flagged safe code (may indicate over-blocking):\n%s", code)
		}
	}
}

func TestContainsDangerousCode_dangerous(t *testing.T) {
	// These tests document the heuristic — NOT a security guarantee.
	// The blocklist is bypassable via obfuscation. Security requires
	// VULOS_SANDBOX_ENABLED to be unset (default) or kernel isolation.
	cases := []struct {
		name string
		code string
	}{
		{"subprocess", "import subprocess\nsubprocess.run(['ls'])"},
		{"os.system", "os.system('rm -rf /')"},
		{"eval", "eval('__import__(\"os\")')"},
		{"exec", "exec('import os')"},
		{"importlib", "import importlib\nimportlib.import_module('os')"},
		{"__import__", "m = __import__('os')"},
		{"base64", "base64.b64decode('aW1wb3J0IG9z')"},
		{"compile", "compile('import os', '<string>', 'exec')"},
		{"globals", "print(globals())"},
		{"ctypes", "import ctypes"},
		{"open_etc", "open('/etc/passwd')"},
		{"open_proc", "open('/proc/self/mem')"},
		{"too_large", strings.Repeat("x", 101*1024)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !containsDangerousCode(tc.code) {
				t.Errorf("heuristic missed pattern %q (update blocklist)", tc.name)
			}
		})
	}
}

// TestSandboxDisabledByDefault confirms that Run() is refused when
// VULOS_SANDBOX_ENABLED is not set (C2 fix: default-closed).
func TestSandboxDisabledByDefault(t *testing.T) {
	// Do NOT set VULOS_SANDBOX_ENABLED — test the default-off behaviour.
	t.Setenv(envSandboxEnabled, "")

	dir, err := os.MkdirTemp("", "sandbox-disabled-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	sb := New(dir)
	defer sb.StopAll()

	_, err = sb.Run(context.Background(), "test-disabled", "print('hello')")
	if err == nil {
		t.Fatal("expected Run() to be refused when VULOS_SANDBOX_ENABLED is unset — got nil (C2 regression)")
	}
	if !strings.Contains(err.Error(), "disabled") && !strings.Contains(err.Error(), "VULOS_SANDBOX_ENABLED") {
		t.Errorf("expected 'disabled'/'VULOS_SANDBOX_ENABLED' in error, got: %v", err)
	}
	t.Logf("correctly refused: %v", err)
}

// ---- waitForPort ------------------------------------------------------------

func TestWaitForPort_success(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	if err := waitForPort(port, 2*time.Second); err != nil {
		t.Fatalf("expected port to be ready: %v", err)
	}
}

func TestWaitForPort_timeout(t *testing.T) {
	// Use a port with nothing listening.
	port := 19999
	err := waitForPort(port, 100*time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout error but got nil")
	}
}

// ---- pool PoolLen -----------------------------------------------------------

func TestNew_poolSize_envVar(t *testing.T) {
	dir, err := os.MkdirTemp("", "sandbox-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	t.Setenv("VULOS_SANDBOX_POOL_SIZE", "0")
	sb := New(dir)
	defer sb.StopAll()

	// With pool size 0 the pool should stay empty.
	time.Sleep(200 * time.Millisecond)
	if n := sb.PoolLen(); n != 0 {
		t.Errorf("expected pool size 0, got %d", n)
	}
}

func TestNew_stopAll_drains_pool(t *testing.T) {
	py := findPython()
	if py == "" {
		t.Skip("python3 not found, skipping pool test")
	}

	dir, err := os.MkdirTemp("", "sandbox-pool-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	t.Setenv("VULOS_SANDBOX_POOL_SIZE", "2")
	sb := New(dir)

	// Give the pool a moment to spawn processes.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if sb.PoolLen() > 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	// StopAll should drain the pool without leaving orphan processes.
	sb.StopAll()
	if n := sb.PoolLen(); n != 0 {
		t.Errorf("expected pool to be empty after StopAll, got %d", n)
	}
}
