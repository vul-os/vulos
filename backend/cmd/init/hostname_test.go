//go:build linux

package main

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// BARE-METAL DEFECT, MEASURED 2026-08-17.
//
// build.sh writes /etc/hostname with `echo "vulos" >`, which is correct —
// hostname(5) wants a newline-terminated file. setHostname() then read the
// whole file and handed it to sethostname(2) untrimmed. The kernel does not
// validate hostname bytes, so the box's hostname literally became "vulos\n".
//
// Reproduced by running THIS package's real setHostname() inside a privileged
// Debian trixie container (docker, linux/arm64):
//
//	PROBE: /etc/hostname bytes = "vulos\n"
//	PROBE: os.Hostname() after  = "vulos\n" (err=<nil>)
//	PROBE: uname(2) nodename    = "vulos\n"
//	PROBE: len(after)=6 hex=76756c6f730a
//
// Invisible on the Docker/systemd path because systemd — not this code — sets
// the hostname there, and it trims. On bare metal cmd/init is PID 1 and
// startSystemd() only logs, so this value is what the box runs with.
//
// These tests pin the parse. TestSetHostnameInstallsTrimmedName pins the
// syscall end of it, but only where the test process may actually change the
// UTS namespace — see its comment for why it is opt-in.

func TestParseEtcHostnameStripsTrailingNewline(t *testing.T) {
	// The exact bytes build.sh:392 / build.sh:1257 produce.
	got := parseEtcHostname([]byte("vulos\n"))
	if got != "vulos" {
		t.Fatalf("parseEtcHostname(%q) = %q, want %q — a trailing newline reaches sethostname(2) and the kernel keeps it verbatim", "vulos\n", got, "vulos")
	}
	if len(got) != 5 {
		t.Fatalf("parseEtcHostname returned %d bytes (%x), want 5", len(got), got)
	}
}

func TestParseEtcHostname(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"build.sh form", "vulos\n", "vulos"},
		{"crlf", "vulos\r\n", "vulos"},
		{"no trailing newline", "vulos", "vulos"},
		{"leading/trailing spaces", "  vulos  \n", "vulos"},
		{"tabs", "\tvulos\t\n", "vulos"},
		{"comment first (hostname(5))", "# set by installer\nvulos\n", "vulos"},
		{"blank lines first", "\n\n  \nvulos\n", "vulos"},
		{"extra lines ignored", "vulos\ngarbage\n", "vulos"},
		{"empty file", "", ""},
		{"whitespace only", "\n \t\n", ""},
		{"comment only", "# nothing here\n", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseEtcHostname([]byte(tc.in)); got != tc.want {
				t.Fatalf("parseEtcHostname(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestValidHostnameLabel(t *testing.T) {
	valid := []string{"vulos", "vulos-2", "a", "box01", "a-b-c", "0", strLen(63)}
	for _, s := range valid {
		if !validHostnameLabel(s) {
			t.Errorf("validHostnameLabel(%q) = false, want true", s)
		}
	}
	invalid := []string{
		"",             // empty
		"vulos\n",      // THE defect: must never be accepted
		"vulos ",       // trailing space
		"-vulos",       // leading hyphen
		"vulos-",       // trailing hyphen
		"vul.os",       // a dot is not a single label
		"vulos_box",    // underscore
		"vulös",        // non-ASCII
		strLen(64),     // 64 chars > RFC-1123 label max
		"vulos\x00",    // NUL
		"vulos;reboot", // shell-ish
	}
	for _, s := range invalid {
		if validHostnameLabel(s) {
			t.Errorf("validHostnameLabel(%q) = true, want false", s)
		}
	}
}

func strLen(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = 'a'
	}
	return string(b)
}

func TestResolveHostnameFallsBackToVulos(t *testing.T) {
	dir := t.TempDir()
	orig := etcHostnamePath
	t.Cleanup(func() { etcHostnamePath = orig })

	cases := []struct {
		name    string
		content *string // nil = do not create the file at all
		want    string
	}{
		{"missing file", nil, "vulos"},
		{"build.sh form", ptr("vulos\n"), "vulos"},
		{"operator-set name", ptr("study-box\n"), "study-box"},
		{"empty file", ptr(""), "vulos"},
		{"garbage", ptr("not a hostname!\n"), "vulos"},
		{"newline-only name is rejected, not installed", ptr("\n"), "vulos"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := filepath.Join(dir, "hostname-"+tc.name)
			if tc.content != nil {
				if err := os.WriteFile(p, []byte(*tc.content), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			etcHostnamePath = p
			if got := resolveHostname(); got != tc.want {
				t.Fatalf("resolveHostname() = %q, want %q", got, tc.want)
			}
		})
	}
}

func ptr(s string) *string { return &s }

// TestSetHostnameInstallsTrimmedName exercises the WHOLE function including
// sethostname(2) and reads the value back out of the kernel — the only way to
// prove the bytes the box actually runs with.
//
// It is opt-in (VULOS_TEST_ALLOW_SETHOSTNAME=1) because it mutates the UTS
// namespace of whatever is running it: harmless in a container with its own
// UTS namespace, rude on a developer's machine or a shared CI runner. It is
// run deliberately in the privileged container used to diagnose this defect;
// the unconditional parse tests above are what protect the fix day to day.
func TestSetHostnameInstallsTrimmedName(t *testing.T) {
	if os.Getenv("VULOS_TEST_ALLOW_SETHOSTNAME") != "1" {
		t.Skip("mutates the UTS namespace; set VULOS_TEST_ALLOW_SETHOSTNAME=1 (container only)")
	}
	before, err := os.Hostname()
	if err != nil {
		t.Fatalf("os.Hostname: %v", err)
	}
	t.Cleanup(func() { _ = syscall.Sethostname([]byte(before)) })

	dir := t.TempDir()
	p := filepath.Join(dir, "hostname")
	if err := os.WriteFile(p, []byte("vulos\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	orig := etcHostnamePath
	etcHostnamePath = p
	t.Cleanup(func() { etcHostnamePath = orig })

	setHostname()

	got, err := os.Hostname()
	if err != nil {
		t.Fatalf("os.Hostname after setHostname: %v", err)
	}
	if got != "vulos" {
		t.Fatalf("kernel hostname is %q (% x), want %q — /etc/hostname's trailing newline reached sethostname(2)", got, got, "vulos")
	}
}
