// Package android is the box-side redroid (Android-in-a-container) management
// service. It lets the box owner run a containerized Android userspace
// (https://github.com/remote-android/redroid-doc) for the small class of apps
// that ship no web/Linux client, mirroring the "shell out to a CLI" approach
// services/telephony uses for mmcli — here the CLI is `docker`.
//
// This is an OPT-IN, ADVANCED feature and it is HARDWARE/KERNEL-GATED:
// redroid needs the host kernel's binder + ashmem drivers (either built in or
// loaded as modules) in addition to a working Docker install. IsAvailable()
// is true only when BOTH are detected; every method degrades to a clean
// "unavailable" error otherwise rather than pretending to work.
//
// HONESTY NOTE (cannot be verified in this environment): nothing in this file
// has been exercised against a real Docker daemon, real binder/ashmem kernel
// modules, or an actual redroid image. The docker CLI invocations follow
// redroid's documented usage (see redroid-doc's README) as closely as
// possible, but the exact flags/behavior MUST be verified on real hardware
// before this is trusted. Comments below flag the specific spots that most
// need that verification.
package android

import (
	"context"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// containerName is the fixed name of the managed redroid container. Fixed
// (rather than per-user) because this is a single-owner, single-instance
// feature — see package doc and Service.ownerID.
const containerName = "vulos-redroid"

// defaultImage is the redroid image pulled when Service.Image is unset.
// Overridable via the VULOS_REDROID_IMAGE env var (founder/owner choice of
// Android version) — NOT baked into the signed registry entry, since the
// image tag may need to move independently of an OS release.
const defaultImage = "redroid/redroid:11.0.0-latest"

// adbPort is the host port the container's ADB daemon is published on
// (redroid's androidboot.redroid_adbd=1 property enables adbd inside the
// guest; we map it to this fixed host port). Loopback-only in the docker run
// args below — never published on all interfaces.
const adbPort = 5555

// appiumSettingsPkg is the package name of the well-known open-source
// "Appium Settings" helper APK (io.appium.settings, part of the Appium
// project) that exposes a broadcast receiver for injecting a mock location.
// See FeedLocation for why this dependency exists and what it does NOT give
// you (isMock=true, detectable).
const appiumSettingsPkg = "io.appium.settings"

// Notifier is the sovereign-notification seam, unused today but kept as a
// hook (e.g. to notify the owner when a long-running redroid pull finishes).
// nil is safe.
type Notifier interface {
	Send(title, body string, source string)
}

// Service is the box redroid-management service. Safe for concurrent use.
type Service struct {
	notifier Notifier
	// ownerID resolves the box owner's user id, same pattern as
	// services/telephony.Service.ownerID: redroid is a single box-owned
	// container (not per-user), so the entire /api/android surface is gated
	// to the owner. Fail-closed: "" means denied to everyone.
	ownerID func() string

	// Image is the redroid image to run. Empty means defaultImage (or
	// VULOS_REDROID_IMAGE if set).
	Image string

	mu sync.Mutex
}

// New creates the service. notifier may be nil. ownerID resolves the owning
// user; nil is treated as "no owner" (redroid denied to everyone).
func New(notifier Notifier, ownerID func() string) *Service {
	if ownerID == nil {
		ownerID = func() string { return "" }
	}
	return &Service{notifier: notifier, ownerID: ownerID}
}

// isOwner reports whether userID is the box owner (fail-closed).
func (s *Service) isOwner(userID string) bool {
	owner := s.ownerID()
	return owner != "" && userID == owner
}

// image resolves the configured image, falling back to the env override and
// then the compiled-in default.
func (s *Service) image() string {
	if s.Image != "" {
		return s.Image
	}
	if v := strings.TrimSpace(os.Getenv("VULOS_REDROID_IMAGE")); v != "" {
		return v
	}
	return defaultImage
}

// ─── docker plumbing ──────────────────────────────────────────────────────────

// dockerPresent reports whether the docker CLI is installed.
func dockerPresent() bool {
	_, err := exec.LookPath("docker")
	return err == nil
}

// adbPresent reports whether the adb CLI is installed (needed only for
// FeedLocation, not for basic container lifecycle).
func adbPresent() bool {
	_, err := exec.LookPath("adb")
	return err == nil
}

// docker runs `docker <args...>` with the given timeout and returns trimmed
// stdout+stderr combined (docker's useful errors — "no such container",
// "Cannot connect to the Docker daemon" — go to stderr) or an error.
func docker(timeout time.Duration, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// adb runs `adb <args...>` with the given timeout, same output convention.
func adb(timeout time.Duration, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "adb", args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// binderCandidatePaths are device-node locations a working binder driver may
// expose, across the variants seen in the wild: the classic single-instance
// node, binderfs's control node (modern kernels; instances are created
// on-demand under the same directory), and Android-derived kernels that
// additionally expose hwbinder/vndbinder. Finding ANY of these is treated as
// "binder is present" — redroid itself picks whichever the kernel offers.
var binderCandidatePaths = []string{
	"/dev/binder",
	"/dev/binderfs/binder-control",
	"/dev/hwbinder",
	"/dev/vndbinder",
}

// binderDeviceDetected does a best-effort check for a binder device node.
// This is NOT exhaustive: some distros mount binderfs at a different path,
// and the check only proves a node exists, not that it is fully functional
// (e.g. instance-count exhaustion or a mismatched ashmem driver would still
// only surface once `docker run` actually starts the container). Real
// verification requires running against actual hardware.
func binderDeviceDetected() bool {
	for _, p := range binderCandidatePaths {
		if _, err := os.Stat(p); err == nil {
			return true
		}
		// binderfs's control node spawns further nodes in the same dir on
		// request; a glob catches an already-provisioned "binder0" etc.
		if matches, _ := filepath.Glob(p + "*"); len(matches) > 0 {
			return true
		}
	}
	return false
}

// binderModuleLoaded greps `lsmod` for binder_linux/ashmem_linux. BEST-EFFORT
// ONLY and not authoritative in either direction: on most modern distro
// kernels binder/ashmem are compiled in (CONFIG_ANDROID_BINDER_IPC=y) rather
// than loaded as modules, in which case they will NEVER show up in lsmod even
// though the driver is fully present and working — that's why
// binderDeviceDetected (an actual device node) is the primary signal and this
// is only consulted as a secondary hint. Absence of `lsmod` itself (e.g. a
// minimal/hardened install) is treated as "unknown", not "missing".
func binderModuleLoaded() bool {
	out, err := exec.Command("lsmod").Output()
	if err != nil {
		return false
	}
	s := string(out)
	return strings.Contains(s, "binder_linux") || strings.Contains(s, "ashmem_linux")
}

// ─── availability ─────────────────────────────────────────────────────────────

// Prerequisites returns the human-readable list of missing prerequisites.
// Empty means redroid is usable. Both docker and a binder-capable kernel are
// required; either missing degrades every method to a clean error.
func (s *Service) Prerequisites() []string {
	var missing []string
	if !dockerPresent() {
		missing = append(missing, "docker CLI not found in PATH (install Docker to use Android-in-a-container)")
	}
	if !binderDeviceDetected() && !binderModuleLoaded() {
		missing = append(missing, "binder/ashmem kernel support not detected "+
			"(no /dev/binder* device and no binder_linux/ashmem_linux module loaded — "+
			"try `modprobe binder_linux ashmem_linux`, or confirm your kernel has "+
			"CONFIG_ANDROID_BINDER_IPC=y built in)")
	}
	return missing
}

// IsAvailable reports whether redroid is usable at all (docker present AND
// binder detected). Hardware/kernel gate for the whole service.
func (s *Service) IsAvailable() bool {
	return len(s.Prerequisites()) == 0
}

// ─── status ───────────────────────────────────────────────────────────────────

// Status is the redroid snapshot the app-store/settings UI shows.
type Status struct {
	Available bool     `json:"available"`
	Running   bool     `json:"running"`
	Missing   []string `json:"missing_prerequisites,omitempty"`
	Image     string   `json:"image,omitempty"`
	ADBPort   int      `json:"adb_port,omitempty"`
	Container string   `json:"container,omitempty"`
}

// Status returns the current container state. Never errors: an unavailable
// or absent container simply reports Available/Running false.
func (s *Service) Status() Status {
	missing := s.Prerequisites()
	if len(missing) > 0 {
		return Status{Available: false, Missing: missing}
	}
	st := Status{Available: true, Image: s.image(), ADBPort: adbPort, Container: containerName}
	st.Running = s.containerRunning()
	return st
}

// containerRunning asks docker whether containerName exists and is running.
// `docker inspect -f {{.State.Running}}` prints "true"/"false", or errors
// (non-zero exit, "No such object" on stderr) when the container doesn't
// exist at all — both are folded into `false` here since either way redroid
// isn't up.
func (s *Service) containerRunning() bool {
	out, err := docker(10*time.Second, "inspect", "-f", "{{.State.Running}}", containerName)
	if err != nil {
		return false
	}
	return strings.TrimSpace(out) == "true"
}

// containerExists reports whether the container exists at all (running or
// stopped), so Start can `docker start` a stopped container instead of
// failing on `docker run`'s "name already in use".
func (s *Service) containerExists() bool {
	_, err := docker(10*time.Second, "inspect", "-f", "{{.Id}}", containerName)
	return err == nil
}

// ─── lifecycle ────────────────────────────────────────────────────────────────

// Start brings up the redroid container. Idempotent: if it's already
// running, this is a no-op. If it exists but is stopped, it's restarted
// in place (keeping any state under the container's writable layer /
// mounted volume); otherwise a fresh container is created.
//
// VERIFY ON REAL HARDWARE: the `docker run` flags below follow redroid's
// documented usage (--privileged so the container can see the host's
// binder/ashmem devices without enumerating them one by one, plus the
// androidboot.redroid_* boot properties). Exact flags/behavior differ across
// redroid versions and host kernel configurations and MUST be validated
// against a real Docker + redroid image before this is trusted in
// production.
func (s *Service) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if missing := s.Prerequisites(); len(missing) > 0 {
		return fmt.Errorf("android/redroid unavailable: %s", strings.Join(missing, "; "))
	}
	if s.containerRunning() {
		return nil // already up
	}
	if s.containerExists() {
		if _, err := docker(30*time.Second, "start", containerName); err != nil {
			return fmt.Errorf("docker start failed: %w", err)
		}
		return nil
	}

	// First run: pull explicitly (long timeout — a fresh redroid image can be
	// several hundred MB) so a slow pull doesn't get folded into a confusing
	// `docker run` failure. Pull errors are surfaced directly; `docker run`
	// would also implicitly pull, but a clear "pulling <image>" failure here
	// is more actionable than a generic run failure.
	if _, err := docker(5*time.Minute, "pull", s.image()); err != nil {
		return fmt.Errorf("docker pull %s failed: %w", s.image(), err)
	}

	args := []string{
		"run", "-d",
		"--name", containerName,
		// --privileged: gives the container access to the host's binder/ashmem
		// device nodes without enumerating them (their exact path/major-minor
		// varies by kernel — binderfs vs classic /dev/binder). Tighter scoping
		// via explicit `--device` is possible once the real device paths on a
		// given box are confirmed, but --privileged is what redroid's own docs
		// use and is the safer default to get a first boot working.
		"--privileged",
		// Loopback-only ADB port mapping for FeedLocation (below). Never
		// publish on 0.0.0.0 — an open, unauthenticated ADB port is a full
		// device-takeover surface.
		"-p", fmt.Sprintf("127.0.0.1:%d:5555", adbPort),
		s.image(),
		"androidboot.redroid_width=1080",
		"androidboot.redroid_height=1920",
		"androidboot.redroid_gpu_mode=guest",
		"androidboot.redroid_adbd=1",
	}
	if _, err := docker(30*time.Second, args...); err != nil {
		return fmt.Errorf("docker run failed: %w", err)
	}
	return nil
}

// Stop stops (but does not remove) the container, so a later Start resumes
// the same instance. No-op if it isn't running.
func (s *Service) Stop() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !dockerPresent() {
		return fmt.Errorf("android/redroid unavailable: docker CLI not found")
	}
	if !s.containerExists() {
		return nil
	}
	if _, err := docker(30*time.Second, "stop", containerName); err != nil {
		return fmt.Errorf("docker stop failed: %w", err)
	}
	return nil
}

// ─── location injection ───────────────────────────────────────────────────────

// FeedLocation injects a position into the running container.
//
// There are two honest options for this, and this method implements ONLY the
// first:
//
//	(a) MOCK LOCATION (implemented here, best-effort): connect adb to the
//	    container's published ADB port and drive Android's standard mock-
//	    location machinery via the "Appium Settings" helper app
//	    (io.appium.settings — an open-source APK from the Appium project that
//	    registers a broadcast receiver for exactly this purpose). This
//	    requires that helper APK to already be installed inside the redroid
//	    image/container — it is NOT bundled by this Go service, and if it's
//	    absent the broadcast is silently ignored by Android with no error
//	    docker/adb can report, so FeedLocation cannot promise success, only
//	    that it issued the commands. Locations set this way are flagged
//	    Location.isMock == true by the Android framework, so any app that
//	    checks for mock locations (most ride-hailing/banking/anti-fraud SDKs
//	    do) will detect and typically refuse it. This is fine for casual/
//	    testing use, NOT a way to defeat attestation-gated apps.
//
//	(b) VIRTUAL GNSS / NMEA FEED (NOT implemented — the real upgrade path):
//	    the non-mock way is to back Android's GNSS HAL
//	    (android.hardware.gnss) inside the container with a real NMEA sentence
//	    stream (e.g. bind-mount a named pipe/socket into the container that a
//	    custom or patched HAL implementation reads GPGGA/GPRMC sentences
//	    from, similar to how a real GPS receiver's serial output is consumed).
//	    This produces Location.isMock == false — genuinely indistinguishable
//	    from real GPS — but it requires a redroid image with a
//	    custom/patched GNSS HAL, which is a from-scratch Android image build,
//	    not something achievable by shelling out from this host-side Go
//	    service. Left as future work; flagged here so nobody mistakes (a) for
//	    a complete solution.
//
// Coordinates are validated but NOT shell-interpolated (exec.CommandContext
// takes argv directly, no shell), so there is no injection surface here the
// way telephony.validNumber guards against for mmcli's comma-delimited args.
func (s *Service) FeedLocation(lat, lng float64) error {
	if missing := s.Prerequisites(); len(missing) > 0 {
		return fmt.Errorf("android/redroid unavailable: %s", strings.Join(missing, "; "))
	}
	if !adbPresent() {
		return fmt.Errorf("adb CLI not found in PATH — install android-tools/platform-tools to use location injection")
	}
	// NaN/Inf slip past the range checks below (all comparisons against NaN are
	// false), and would reach adb as a literal "NaN"/"+Inf" string. Reject them
	// up front.
	if math.IsNaN(lat) || math.IsInf(lat, 0) || math.IsNaN(lng) || math.IsInf(lng, 0) {
		return fmt.Errorf("invalid coordinates (NaN/Inf)")
	}
	if lat < -90 || lat > 90 {
		return fmt.Errorf("invalid latitude %v (must be between -90 and 90)", lat)
	}
	if lng < -180 || lng > 180 {
		return fmt.Errorf("invalid longitude %v (must be between -180 and 180)", lng)
	}
	if !s.containerRunning() {
		return fmt.Errorf("redroid container is not running — call Start first")
	}

	target := fmt.Sprintf("127.0.0.1:%d", adbPort)
	// `adb connect` is idempotent and safe to call every time; ignore
	// "already connected to" style non-errors, but a real connection failure
	// (container's adbd not up yet, wrong port) should stop here rather than
	// silently no-op the broadcast below.
	if out, err := adb(10*time.Second, "connect", target); err != nil {
		return fmt.Errorf("adb connect %s failed: %w (%s)", target, err, out)
	}

	// Best-effort: grant the mock-location permission to the helper app.
	// Ignored on error (older/newer Android revisions expose this
	// differently, and the helper app may already have it granted) — the
	// broadcast below is the operation whose failure actually matters.
	_, _ = adb(10*time.Second, "-s", target, "shell", "appops", "set", appiumSettingsPkg, "android:mock_location", "allow")

	latStr := strconv.FormatFloat(lat, 'f', -1, 64)
	lngStr := strconv.FormatFloat(lng, 'f', -1, 64)
	out, err := adb(10*time.Second, "-s", target, "shell", "am", "broadcast",
		"-a", appiumSettingsPkg+".location",
		"--es", "longitude", lngStr,
		"--es", "latitude", latStr,
	)
	if err != nil {
		return fmt.Errorf("mock-location broadcast failed: %w (%s)", err, out)
	}
	// `am broadcast` reports "Broadcast completed" whether or not any receiver
	// was actually registered for the action — adb/docker give us no signal
	// that io.appium.settings is actually installed inside the image. Callers
	// should treat a nil error here as "the command was issued", not as
	// confirmed proof the app's location actually changed.
	return nil
}
