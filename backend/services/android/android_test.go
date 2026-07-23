package android

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// mkService builds a Service whose owner resolves to the given id. A blank id
// means "no owner" (fail-closed), matching New's contract.
func mkService(owner string) *Service {
	return New(nil, func() string { return owner })
}

// binderPresentNatively reports whether the *current* host (before any stub
// injection) actually has binder support. These tests assume the environment
// has none (macOS/CI without the Android binder driver) so that IsAvailable is
// false; if a runner really does have binder we skip the "honest unavailable"
// assertions rather than fail spuriously.
func binderPresentNatively() bool {
	return binderDeviceDetected() || binderModuleLoaded()
}

// forceAvailableViaStubs makes Prerequisites() pass without real Docker/binder
// hardware by prepending a temp dir of stub executables to PATH:
//
//   - lsmod  → prints a line containing "binder_linux" so binderModuleLoaded()
//     reports the kernel driver present (this is the ONLY stub actually
//     executed by the code paths these tests exercise);
//   - adb    → makes adbPresent()'s exec.LookPath succeed (never run here: the
//     coordinate-rejection cases return before any adb call, which is exactly
//     the property under test);
//   - docker → makes dockerPresent() true and, if containerRunning() is ever
//     reached, exits non-zero so the container reads as "not running" (so a
//     *valid* coordinate falls through the guards to a clean not-running error
//     rather than touching a real daemon).
//
// This is purely to REACH FeedLocation's coordinate validation, which lives
// behind the hardware gate and the adb-present gate and is otherwise
// unreachable in an environment with no binder + no adb.
func forceAvailableViaStubs(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatalf("writing stub %s: %v", name, err)
		}
	}
	write("lsmod", `echo "binder_linux 16384 0 - Live 0x0000000000000000"`)
	write("adb", `exit 0`)
	write("docker", `exit 1`)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	// Sanity: the gate must now be open, otherwise the coordinate-validation
	// assertions below would be testing the unavailable path by accident.
	if !dockerPresent() || !adbPresent() || !binderModuleLoaded() {
		t.Fatalf("stub injection failed: docker=%v adb=%v binder=%v",
			dockerPresent(), adbPresent(), binderModuleLoaded())
	}
	if s := mkService("o"); !s.IsAvailable() {
		t.Fatalf("IsAvailable still false after stub injection: missing=%v", s.Prerequisites())
	}
}

// TestIsAvailableUnavailableWithoutBinder locks the core hardware gate: with no
// binder device and no binder module (this environment), the service reports
// itself unavailable and Prerequisites names the binder gap. It must reach this
// verdict by inspection only — never panic or hang on a real daemon.
func TestIsAvailableUnavailableWithoutBinder(t *testing.T) {
	if binderPresentNatively() {
		t.Skip("host has real binder support; unavailable-path assertions do not apply")
	}
	s := mkService("owner-1")

	if s.IsAvailable() {
		t.Fatalf("IsAvailable = true, want false with no binder support")
	}
	missing := s.Prerequisites()
	if len(missing) == 0 {
		t.Fatalf("Prerequisites empty, want at least the binder gap")
	}
	joined := strings.Join(missing, " | ")
	if !strings.Contains(joined, "binder") {
		t.Fatalf("Prerequisites missing binder entry: %q", joined)
	}
}

// TestPrerequisitesReportsDockerGap forces docker (and lsmod) off PATH and
// asserts BOTH prerequisites are reported — i.e. a box with neither Docker nor
// binder gets both honest reasons, not a panic.
func TestPrerequisitesReportsDockerGap(t *testing.T) {
	if binderPresentNatively() {
		t.Skip("host has real binder support; docker+binder double-gap not reproducible")
	}
	// Empty PATH: exec.LookPath finds neither docker nor lsmod.
	t.Setenv("PATH", "")
	s := mkService("owner-1")

	missing := s.Prerequisites()
	joined := strings.Join(missing, " | ")
	if !strings.Contains(joined, "docker") {
		t.Fatalf("Prerequisites missing docker entry: %q", joined)
	}
	if !strings.Contains(joined, "binder") {
		t.Fatalf("Prerequisites missing binder entry: %q", joined)
	}
	if s.IsAvailable() {
		t.Fatalf("IsAvailable = true with docker+binder absent, want false")
	}
}

// TestStatusShapeWhenUnavailable pins the JSON-facing Status snapshot returned
// when the hardware gate is closed: available/running false, missing populated,
// and none of the "live" fields (image/port/container) leaked.
func TestStatusShapeWhenUnavailable(t *testing.T) {
	if binderPresentNatively() {
		t.Skip("host has real binder support; unavailable Status not reproducible")
	}
	st := mkService("owner-1").Status()

	if st.Available {
		t.Errorf("Status.Available = true, want false")
	}
	if st.Running {
		t.Errorf("Status.Running = true, want false")
	}
	if len(st.Missing) == 0 {
		t.Errorf("Status.Missing empty, want at least one prerequisite")
	}
	if st.Image != "" || st.ADBPort != 0 || st.Container != "" {
		t.Errorf("Status leaked live fields when unavailable: image=%q port=%d container=%q",
			st.Image, st.ADBPort, st.Container)
	}
}

// TestImagePrecedence verifies image() resolution order:
// explicit Service.Image > VULOS_REDROID_IMAGE env > compiled-in default,
// including that a whitespace-only env value falls through to the default.
func TestImagePrecedence(t *testing.T) {
	t.Run("explicit field wins over env", func(t *testing.T) {
		t.Setenv("VULOS_REDROID_IMAGE", "env/redroid:9")
		s := mkService("o")
		s.Image = "explicit/redroid:12"
		if got := s.image(); got != "explicit/redroid:12" {
			t.Fatalf("image() = %q, want explicit field", got)
		}
	})

	t.Run("env used when field empty", func(t *testing.T) {
		t.Setenv("VULOS_REDROID_IMAGE", "env/redroid:9")
		s := mkService("o")
		if got := s.image(); got != "env/redroid:9" {
			t.Fatalf("image() = %q, want env override", got)
		}
	})

	t.Run("default when field and env empty", func(t *testing.T) {
		t.Setenv("VULOS_REDROID_IMAGE", "")
		s := mkService("o")
		if got := s.image(); got != defaultImage {
			t.Fatalf("image() = %q, want default %q", got, defaultImage)
		}
	})

	t.Run("whitespace env falls through to default", func(t *testing.T) {
		t.Setenv("VULOS_REDROID_IMAGE", "   \t ")
		s := mkService("o")
		if got := s.image(); got != defaultImage {
			t.Fatalf("image() = %q, want default %q (whitespace env ignored)", got, defaultImage)
		}
	})
}

// TestFeedLocationRejectsBadCoordinates is the coordinate-validation contract.
// Using stub binaries to open the hardware/adb gate (so the guard is actually
// reached), every NaN/Inf and out-of-range value must be rejected with a
// coordinate error BEFORE any adb call — never a "container connect" error and
// never a panic.
func TestFeedLocationRejectsBadCoordinates(t *testing.T) {
	forceAvailableViaStubs(t)
	s := mkService("owner-1")

	nan := math.NaN()
	posInf := math.Inf(1)
	negInf := math.Inf(-1)

	cases := []struct {
		name     string
		lat, lng float64
		wantSub  string
	}{
		{"nan lat", nan, 0, "NaN/Inf"},
		{"nan lng", 0, nan, "NaN/Inf"},
		{"+inf lat", posInf, 0, "NaN/Inf"},
		{"-inf lat", negInf, 0, "NaN/Inf"},
		{"+inf lng", 0, posInf, "NaN/Inf"},
		{"lat above 90", 90.0001, 0, "latitude"},
		{"lat below -90", -90.0001, 0, "latitude"},
		{"lng above 180", 0, 180.0001, "longitude"},
		{"lng below -180", 0, -180.0001, "longitude"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := s.FeedLocation(tc.lat, tc.lng)
			if err == nil {
				t.Fatalf("FeedLocation(%v,%v) = nil, want rejection", tc.lat, tc.lng)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("FeedLocation(%v,%v) error = %q, want substring %q",
					tc.lat, tc.lng, err.Error(), tc.wantSub)
			}
			// Must be rejected as a coordinate problem, i.e. BEFORE reaching the
			// adb-connect / container path.
			if strings.Contains(err.Error(), "connect") || strings.Contains(err.Error(), "broadcast") {
				t.Fatalf("bad coordinate reached adb layer: %q", err.Error())
			}
		})
	}
}

// TestFeedLocationValidCoordinatesPassGuards confirms an in-range coordinate is
// NOT caught by the validators: it must fall THROUGH the guards to the
// container-state check (stub docker reports not-running here), proving the
// validators accept legal input rather than over-rejecting.
func TestFeedLocationValidCoordinatesPassGuards(t *testing.T) {
	forceAvailableViaStubs(t)
	s := mkService("owner-1")

	err := s.FeedLocation(37.7749, -122.4194)
	if err == nil {
		t.Skip("valid coordinate returned nil (stub container behaved as running); guard-pass still demonstrated")
	}
	if strings.Contains(err.Error(), "NaN/Inf") ||
		strings.Contains(err.Error(), "latitude") ||
		strings.Contains(err.Error(), "longitude") {
		t.Fatalf("valid coordinate wrongly rejected by validator: %q", err.Error())
	}
	if !strings.Contains(err.Error(), "not running") {
		t.Fatalf("expected fall-through to container-state error, got %q", err.Error())
	}
}

// TestFeedLocationUnavailableWithoutHardware checks the honest-unavailable path:
// with no stubs (this environment), FeedLocation returns a clean unavailable
// error and never panics.
func TestFeedLocationUnavailableWithoutHardware(t *testing.T) {
	if binderPresentNatively() {
		t.Skip("host has real binder support; unavailable path not reproducible")
	}
	err := mkService("owner-1").FeedLocation(1, 2)
	if err == nil {
		t.Fatalf("FeedLocation = nil with hardware absent, want unavailable error")
	}
	if !strings.Contains(err.Error(), "unavailable") && !strings.Contains(err.Error(), "adb CLI not found") {
		t.Fatalf("FeedLocation error = %q, want an honest unavailable/adb-missing message", err.Error())
	}
}
