//go:build linux

package main

import (
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The bare-metal image (init=/sbin/vulos-init) has exactly one user interface:
// the kiosk browser. When findKioskBrowser came up empty the box booted to a
// frozen 100% splash — plymouthQuitRetainSplash had already handed the
// framebuffer to a compositor that then never started — and said so only in the
// journal, which a user staring at a dead screen cannot read.
//
// These tests pin both halves of the fix: that the lookup finds the browser the
// image now ships, and that when it does NOT, the failure is actually reported
// where a human can see it.

// stubBin writes an executable script into dir. The body may use only shell
// builtins and redirections: these tests set PATH to dir alone, so `touch` and
// `cat` would not resolve (measured elsewhere today — exit 127, which reads as
// an ordinary command failure and silently hides whether the exec happened).
func stubBin(t *testing.T, dir, name, body string) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

// fakeDRM points drmRoot at a connector tree the test writes, and restores it
// afterwards.
//
// Every assertion about the kiosk URL needs this. Left on the real
// /sys/class/drm, a test asserting the bare URL is really asserting that the
// machine running it has no connected output — true on an Azure CI runner with
// no DRM device, false on anything with a monitor, and the same host-dependent
// assertion binRoot exists to have removed once already.
func fakeDRM(t *testing.T, status map[string]string) {
	t.Helper()
	root := t.TempDir()
	for name, st := range status {
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		// Trailing newline: that is how sysfs writes it, and reading it as if it
		// were not there is a real way to see "disconnected" as connected.
		if err := os.WriteFile(filepath.Join(dir, "status"), []byte(st+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	old := drmRoot
	drmRoot = root
	t.Cleanup(func() { drmRoot = old })
}

// plymouthRecorder installs a fake `plymouth` that appends its argv to a file,
// so a test can prove what this process tried to put on the screen.
func plymouthRecorder(t *testing.T) (dir, logPath string) {
	t.Helper()
	dir = t.TempDir()
	logPath = filepath.Join(dir, "plymouth.argv")
	stubBin(t, dir, "plymouth", `printf '%s\n' "$*" >> `+logPath)
	return dir, logPath
}

func readLines(t *testing.T, p string) []string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

// ---------------------------------------------------------------------------
// findKioskBrowser
// ---------------------------------------------------------------------------

func TestFindKioskBrowserPrefersCog(t *testing.T) {
	fakeDRM(t, nil) // no connector → the bare URL, on any host
	dir := t.TempDir()
	stubBin(t, dir, "cog", ":")
	stubBin(t, dir, "chromium", ":")
	t.Setenv("PATH", dir)

	bin, args := findKioskBrowser()

	if bin != filepath.Join(dir, "cog") {
		t.Fatalf("findKioskBrowser() bin = %q, want the cog stub; chromium must not win when cog exists", bin)
	}
	// The rootfs runs a cage/labwc Wayland seat: the Wayland platform flag is
	// what makes cog usable there at all.
	if !containsArg(args, "--platform=wl") {
		t.Errorf("cog args %v missing --platform=wl", args)
	}
	if !containsArg(args, "http://localhost:8080") {
		t.Errorf("cog args %v missing the shell URL", args)
	}
}

func TestFindKioskBrowserFallsBackToChromium(t *testing.T) {
	fakeDRM(t, nil)
	dir := t.TempDir()
	stubBin(t, dir, "chromium", ":")
	t.Setenv("PATH", dir)

	bin, args := findKioskBrowser()

	if bin != filepath.Join(dir, "chromium") {
		t.Fatalf("findKioskBrowser() bin = %q, want the chromium stub", bin)
	}
	if !containsArg(args, "--kiosk") {
		t.Errorf("chromium args %v missing --kiosk", args)
	}
}

// The precondition the whole bug rested on: with neither binary present the
// lookup yields nothing, and the caller must treat that as a failure rather
// than proceeding.
func TestFindKioskBrowserEmptyWhenNoBrowserInstalled(t *testing.T) {
	// BOTH probes have to be blinded. Emptying $PATH only hides the LookPath
	// candidates; the absolute ones (/usr/bin/cog, /usr/bin/chromium, …) are
	// os.Stat'd directly, so without binRoot this test was asserting that the
	// machine running it happened to have no browser in /usr/bin. It passed on
	// developer machines because this package is `//go:build linux` and never ran
	// there at all, then failed on the first CI runner it met.
	t.Setenv("PATH", t.TempDir())
	root := t.TempDir()
	old := binRoot
	binRoot = root
	t.Cleanup(func() { binRoot = old })

	bin, args := findKioskBrowser()

	if bin != "" || args != nil {
		t.Fatalf("findKioskBrowser() = (%q, %v), want (\"\", nil) when no browser is installed", bin, args)
	}
}

// ---------------------------------------------------------------------------
// The screen identity on the URL
// ---------------------------------------------------------------------------

// frontend/src/providers/screenIdentity.ts refuses anything but the complete
// triple, so the only two acceptable outputs are the full identity or a bare
// URL. These run the real derivation against a controlled connector tree —
// backend/internal/docsref's TestKioskURLIdentityMatchesInit pins this copy
// against scripts/vulos-kiosk.sh, but only the source text; it cannot see what
// the code does with a directory.

func TestKioskURLNamesTheConnectedOutput(t *testing.T) {
	fakeDRM(t, map[string]string{
		// "disconnected" contains "connected", so a substring test would pick
		// the wrong output here — and card0-DP-1 sorts first, so it would pick
		// it every time rather than intermittently.
		"card0-DP-1":     "disconnected",
		"card0-HDMI-A-1": "connected",
	})

	got := kioskURL()
	want := "http://localhost:8080?screen=HDMI-A-1&screens=1&screenIndex=1"
	if got != want {
		t.Errorf("kioskURL() = %q, want %q", got, want)
	}
}

// The card number is the kernel's, not the compositor's. labwc's MoveToOutput
// and screenWindowTitle both speak HDMI-A-1; card0-HDMI-A-1 matches no output
// and no window, and places nothing while logging nothing.
func TestKioskURLStripsTheCardPrefix(t *testing.T) {
	fakeDRM(t, map[string]string{"card1-eDP-1": "connected"})
	if got := kioskURL(); !strings.Contains(got, "screen=eDP-1&") {
		t.Errorf("kioskURL() = %q, want the connector name with the card prefix stripped", got)
	}
}

// No connector → NO parameters, not guessed ones. A half-set identity is
// discarded by the parser anyway, and an invented one names an output the box
// is not on. vulos.kiosk=force lands here: virtio-gpu reports nothing
// connected while still rendering.
func TestKioskURLIsBareWithoutAConnector(t *testing.T) {
	fakeDRM(t, map[string]string{"card0-HDMI-A-1": "disconnected"})
	if got := kioskURL(); got != "http://localhost:8080" {
		t.Errorf("kioskURL() = %q, want the bare URL when no output is connected", got)
	}
}

// ---------------------------------------------------------------------------
// The loud path
// ---------------------------------------------------------------------------

// THE test the fix exists for. Asserting "findKioskBrowser returned nil" would
// be hollow — that is true both before and after the fix. What changed is that
// the failure now reaches the SCREEN, so that is what this asserts: a real
// plymouth invocation, with display-message, carrying the package names.
func TestReportNoKioskBrowserPutsTheFailureOnScreen(t *testing.T) {
	dir, logPath := plymouthRecorder(t)
	t.Setenv("PATH", dir)

	reportNoKioskBrowser()

	lines := readLines(t, logPath)
	if len(lines) == 0 {
		t.Fatal("plymouth was never invoked: the failure never reached the screen, " +
			"which is exactly the silent dead-screen this fix removes")
	}

	var shown string
	for _, l := range lines {
		if strings.HasPrefix(l, "display-message") {
			shown = l
		}
	}
	if shown == "" {
		t.Fatalf("plymouth was invoked as %q but never with display-message", lines)
	}
	for _, want := range kioskBrowserPackages {
		if !strings.Contains(shown, want) {
			t.Errorf("on-screen message %q does not name the package %q that would fix it", shown, want)
		}
	}
	// A user on a dead screen needs somewhere to go.
	if !strings.Contains(shown, "8080") {
		t.Errorf("on-screen message %q does not tell the user how to reach the box", shown)
	}
}

// The reporter must not tear down the splash it needs to draw on. If plymouth
// is quit first (the pre-fix ordering) there is no surface left to render the
// message to, and the box is silent again.
func TestReportNoKioskBrowserDoesNotQuitTheSplash(t *testing.T) {
	dir, logPath := plymouthRecorder(t)
	t.Setenv("PATH", dir)

	reportNoKioskBrowser()

	for _, l := range readLines(t, logPath) {
		if strings.HasPrefix(l, "quit") {
			t.Errorf("reportNoKioskBrowser ran `plymouth %s`, destroying the surface it must draw on", l)
		}
	}
}

func TestReportNoKioskBrowserLogsActionably(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // no plymouth: the journal is all that is left

	var buf strings.Builder
	old := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(old)

	noKioskBrowserReported = false
	reportNoKioskBrowser()

	if !noKioskBrowserReported {
		t.Error("reportNoKioskBrowser did not record that it ran")
	}
	got := buf.String()
	for _, want := range []string{"cog", "chromium", "8080"} {
		if !strings.Contains(got, want) {
			t.Errorf("log output does not mention %q; it must say what to install and where to connect.\n%s", want, got)
		}
	}
	// "skipping kiosk" understated a total loss of the user interface.
	if !strings.Contains(got, "no user interface") {
		t.Errorf("log output does not state the severity (a machine with no UI):\n%s", got)
	}
}

// The message must not drift from the lookup: every binary named in the
// diagnosis has to be one findKioskBrowser actually probes, or the operator is
// told to install something that would not help.
func TestKioskBrowserPackagesMatchTheProbedBinaries(t *testing.T) {
	bins := strings.Join(kioskBrowserBinaries(), " ")
	for _, pkg := range kioskBrowserPackages {
		if !strings.Contains(bins, pkg) {
			t.Errorf("package %q is advertised as the fix but %q is not among the probed binaries %v",
				pkg, pkg, kioskBrowserBinaries())
		}
	}
}

func containsArg(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}
