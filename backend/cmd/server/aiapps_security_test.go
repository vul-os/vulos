package main

// aiapps_security_test.go — SECAUDIT2 / SEC-I adversarial regressions.
//
// SEC-I replaced the inline AI-apps handlers with charset-validated,
// realpath-contained id handling. These tests assert the id validator and
// the path-containment helper reject traversal/encoded/symlink escapes so a
// regression (e.g. someone widening secI_idRe or dropping the prefix check)
// fails the build.

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestSECI_IDRegex_RejectsTraversalAndEncoding(t *testing.T) {
	bad := []string{
		"..",
		"../etc",
		"../../etc/passwd",
		"a/../../b",
		"%2e%2e%2f",       // url-encoded ../
		"..%2fetc",        // mixed
		"foo/bar",         // slash
		"foo\\bar",        // backslash
		"/abs",            // absolute
		"CAPITALS",        // charset (uppercase not allowed)
		"with space",      // charset
		"x.y",             // dot not in charset
		"-leadingdash",    // must start [a-z0-9]
		"",                // empty
		string(rune(0)),   // NUL
		"a" + string(rune(0)) + "b",
	}
	for _, id := range bad {
		if secI_idRe.MatchString(id) {
			t.Fatalf("SEC-I REGRESSION: id %q passed secI_idRe but must be "+
				"rejected — AI-apps id traversal is possible.", id)
		}
	}

	good := []string{"ai-1715900000000", "myapp", "a", "app-v2", "0", "a-b-c"}
	for _, id := range good {
		if !secI_idRe.MatchString(id) {
			t.Fatalf("SEC-I: legitimate id %q was rejected by secI_idRe", id)
		}
	}
}

func TestSECI_SafeAppDir_ContainsWithinBase(t *testing.T) {
	base := t.TempDir()

	// Valid id → path strictly under base.
	got, ok := secI_safeAppDir(base, "ai-123")
	if !ok {
		t.Fatalf("SEC-I: valid id rejected by secI_safeAppDir")
	}
	want := filepath.Join(base, "ai-123")
	if got != want {
		// EvalSymlinks may canonicalise /var → /private/var on macOS; compare
		// suffix instead of exact string in that case.
		if filepath.Base(got) != "ai-123" {
			t.Fatalf("SEC-I: resolved path %q not under base %q", got, want)
		}
	}

	// Traversal ids must all be refused.
	for _, id := range []string{"..", "../../etc", "a/../../b", "%2e%2e", "/etc"} {
		if _, ok := secI_safeAppDir(base, id); ok {
			t.Fatalf("SEC-I REGRESSION: secI_safeAppDir accepted traversal "+
				"id %q — AI-apps directory escape is possible.", id)
		}
	}
}

// TestSECI_SafeAppDir_SymlinkEscapeRejected: if an attacker pre-creates a
// symlink inside the base that points outside it, secI_safeAppDir must
// resolve it and refuse (realpath-containment).
func TestSECI_SafeAppDir_SymlinkEscapeRejected(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on windows")
	}
	base := t.TempDir()
	outside := t.TempDir()

	// base/escape -> outside  (a valid-charset id whose dir is a symlink out)
	link := filepath.Join(base, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	resolved, ok := secI_safeAppDir(base, "escape")
	if ok {
		t.Fatalf("SEC-I REGRESSION: secI_safeAppDir returned ok=true for an "+
			"id whose directory is a symlink escaping the base (resolved=%q, "+
			"outside=%q). Realpath-containment is broken.", resolved, outside)
	}
}
