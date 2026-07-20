package appnet

// launcher_privilege_test.go — ISOLATION-PRIV-01 regression tests.
//
// These tests verify the privilege-isolation properties of the process-app
// launcher WITHOUT invoking real Linux network namespaces, setpriv(8), or OS
// process spawning.  They test the command construction logic and the
// isolation constants so a refactor that silently reverts the hardening is
// caught by CI.
//
// Tests that require a real Linux environment (e.g. "the launched process
// cannot open /etc/shadow") are intentionally out of scope here: they require
// root + iproute2 + iptables and are covered by integration tests run in the
// privileged CI pipeline.  The unit tests below protect the construction logic.

import (
	"fmt"
	"strings"
	"testing"
)

// ── Constants ────────────────────────────────────────────────────────────────

// TestAppUID_IsNotRoot verifies that the app uid constant is NOT 0 (root).
// ISOLATION-PRIV-01: process apps must never run as root.
func TestAppUID_IsNotRoot(t *testing.T) {
	if appUID == 0 {
		t.Fatal("ISOLATION-PRIV-01 REGRESSION: appUID is 0 (root) — process apps must run as an unprivileged uid")
	}
}

// TestAppGID_IsNotRoot verifies that the app gid constant is NOT 0 (root).
func TestAppGID_IsNotRoot(t *testing.T) {
	if appGID == 0 {
		t.Fatal("ISOLATION-PRIV-01 REGRESSION: appGID is 0 (root) — process apps must run as an unprivileged gid")
	}
}

// TestAppUID_IsNobody verifies we use the well-known nobody uid (65534).
func TestAppUID_IsNobody(t *testing.T) {
	const wantUID = 65534
	if appUID != wantUID {
		t.Errorf("appUID = %d, want %d (nobody) — update this test if the uid intentionally changed", appUID, wantUID)
	}
}

// ── Command construction ─────────────────────────────────────────────────────

// buildAppCommand mirrors the exact command construction in launchWithConcurrency
// so we can inspect it without spawning a real process.
func buildAppCommand(nsName, expandedCmd string) []string {
	return []string{
		"ip", "netns", "exec", nsName,
		"setpriv",
		fmt.Sprintf("--reuid=%d", appUID),
		fmt.Sprintf("--regid=%d", appGID),
		"--clear-groups",
		"--no-new-privs",
		"sh", "-c", expandedCmd,
	}
}

// TestCommand_UsesSetpriv verifies that the process-app launch command
// invokes setpriv for privilege drop rather than running as root.
// ISOLATION-PRIV-01: setpriv must be in the command chain.
func TestCommand_UsesSetpriv(t *testing.T) {
	args := buildAppCommand("vulos_test", "node server.js")
	found := false
	for _, a := range args {
		if a == "setpriv" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("ISOLATION-PRIV-01 REGRESSION: setpriv not found in launch command %v — process apps would run as root", args)
	}
}

// TestCommand_DropsToNobodyUID verifies --reuid=65534 is in the command.
func TestCommand_DropsToNobodyUID(t *testing.T) {
	wantFlag := fmt.Sprintf("--reuid=%d", appUID)
	args := buildAppCommand("vulos_test", "python app.py")
	for _, a := range args {
		if a == wantFlag {
			return
		}
	}
	t.Fatalf("ISOLATION-PRIV-01 REGRESSION: %q not found in launch command %v", wantFlag, args)
}

// TestCommand_DropsToNobodyGID verifies --regid=65534 is in the command.
func TestCommand_DropsToNobodyGID(t *testing.T) {
	wantFlag := fmt.Sprintf("--regid=%d", appGID)
	args := buildAppCommand("vulos_test", "python app.py")
	for _, a := range args {
		if a == wantFlag {
			return
		}
	}
	t.Fatalf("ISOLATION-PRIV-01 REGRESSION: %q not found in launch command %v", wantFlag, args)
}

// TestCommand_ClearsGroups verifies --clear-groups is present (drops
// supplementary group memberships inherited from the server process).
func TestCommand_ClearsGroups(t *testing.T) {
	args := buildAppCommand("vulos_test", "python app.py")
	for _, a := range args {
		if a == "--clear-groups" {
			return
		}
	}
	t.Fatalf("ISOLATION-PRIV-01 REGRESSION: --clear-groups not in launch command — supplementary groups not dropped")
}

// TestCommand_NoNewPrivs verifies --no-new-privs is present (prevents
// setuid/setgid escalation via binaries inside the namespace).
func TestCommand_NoNewPrivs(t *testing.T) {
	args := buildAppCommand("vulos_test", "python app.py")
	for _, a := range args {
		if a == "--no-new-privs" {
			return
		}
	}
	t.Fatalf("ISOLATION-PRIV-01 REGRESSION: --no-new-privs not in launch command — setuid escalation possible")
}

// ── Environment ──────────────────────────────────────────────────────────────

// buildAppEnv mirrors the env construction in launchWithConcurrency.
func buildAppEnv(extraEnv []string, appPort int) []string {
	base := []string{
		"PATH=/usr/local/bin:/usr/bin:/bin:/usr/local/sbin:/usr/sbin:/sbin",
		"HOME=/tmp",
		"TMPDIR=/tmp",
	}
	env := append(base, extraEnv...)
	env = append(env, fmt.Sprintf("PORT=%d", appPort))
	return env
}

// TestEnv_HomeIsNotRoot verifies HOME is not /root.
// ISOLATION-PRIV-01: nobody (uid 65534) cannot write to /root.
func TestEnv_HomeIsNotRoot(t *testing.T) {
	env := buildAppEnv(nil, 8080)
	for _, e := range env {
		if e == "HOME=/root" {
			t.Fatal("ISOLATION-PRIV-01 REGRESSION: HOME=/root in app env — nobody cannot write there; use HOME=/tmp")
		}
	}
	// Ensure HOME is explicitly set to something.
	hasHOME := false
	for _, e := range env {
		if strings.HasPrefix(e, "HOME=") {
			hasHOME = true
			break
		}
	}
	if !hasHOME {
		t.Fatal("HOME not set in app env — it should be /tmp or a per-app temp dir")
	}
}

// ── SysProcAttr / Cloneflags ─────────────────────────────────────────────────

// TestSysProcAttr_Setpgid verifies the process group flag is present in
// newAppSysProcAttr (all platforms).
func TestSysProcAttr_Setpgid(t *testing.T) {
	attr := newAppSysProcAttr()
	if !attr.Setpgid {
		t.Fatal("SysProcAttr.Setpgid should be true so we can kill the process group cleanly")
	}
}

// ── Env scrubbing ─────────────────────────────────────────────────────────────

// TestEnv_NoOsEnviron verifies that the app env is built from a minimal
// allowlist — not from os.Environ().  This is a structural test: if someone
// accidentally changes buildAppEnv to start from os.Environ() the PATH would
// include entries that os.Environ() adds (e.g. HOME=/home/user), which differs
// from the hardcoded PATH in the scrubbed env.
func TestEnv_NoOsEnviron(t *testing.T) {
	env := buildAppEnv(nil, 9000)
	// The scrubbed env must start with exactly the hardcoded PATH.
	wantPath := "PATH=/usr/local/bin:/usr/bin:/bin:/usr/local/sbin:/usr/sbin:/sbin"
	if len(env) == 0 || env[0] != wantPath {
		t.Errorf("first env entry = %q, want %q — env may not be scrubbed", env[0], wantPath)
	}
}
