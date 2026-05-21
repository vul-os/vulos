// Package auth — fingerprint.go
//
// CLOGIN-07: Optional fingerprint unlock via fprintd (libfprint) on Linux.
//
// Design:
//   - FingerprintService wraps fprintd's D-Bus API (net.reactivated.Fprint)
//     using the same gdbus CLI approach used elsewhere in this codebase (e.g.
//     backend/services/telephony/sms.go).  No additional Go D-Bus library is
//     needed; the only hard dependency is the gdbus binary being present on
//     the host, which is available on any Debian/Ubuntu desktop system that
//     has fprintd installed.
//   - Hardware-gated: IsAvailable() returns false when either gdbus or fprintd
//     is absent or reports no enrolled devices.  The Settings UI must call
//     GET /api/auth/fingerprint/status and only render the "Add fingerprint"
//     UI when available=true.
//   - Enrollment: the user triggers fprintd's Enroll flow through the D-Bus
//     Enroll interface.  The service tracks that at least one fingerprint has
//     been enrolled (per-profile flag stored under dataDir) but never stores
//     the biometric data itself — fprintd owns that.
//   - Unlock: POST /api/auth/fingerprint/verify triggers fprintd's Verify
//     flow.  On success the same TPM-wrapped session credential used by
//     CLOGIN-06 (DevicePINService) is returned to the caller.
//   - Failure policy: 3 consecutive fingerprint failures → FingerprintService
//     returns ErrFingerprintMaxFails and the caller must fall back to
//     PIN/password.  The counter resets on success or on a full-auth session.
//   - Per-profile: the enrolled-flag and failure counter are stored under
//     dataDir/fingerprint_{profileID}.json so different users on the same
//     device each have their own state.
//   - No biometric data leaves the device.  Disabling fingerprint unlock
//     requires a full-auth session (enforced at the handler layer).
//
// Hardware support matrix (documented for the Settings UI):
//   - Any libfprint-supported reader registered with fprintd works.
//   - Common supported readers: Synaptics (06cb), Goodix (27c6), ELAN (04f3),
//     AuthenTec (08ff), DigitalPersona (05ba), Validity (138a).
//   - VirtualBox/QEMU guests without USB passthrough: not supported.
//   - macOS/Windows: not supported (fprintd is Linux-only).
//
// D-Bus interface summary:
//
//	net.reactivated.Fprint.Manager (path /net/reactivated/Fprint/Manager)
//	  GetDefaultDevice() → objectpath
//
//	net.reactivated.Fprint.Device (path returned above)
//	  ListEnrolledFingers(username string) → []string
//	  EnrollStart(finger string)
//	  EnrollStop()
//	  VerifyStart(finger string)   — "any" is accepted by modern fprintd
//	  VerifyStop()
//	  Signals: EnrollStatus(finger, done bool), VerifyStatus(result, done bool)
//
// File layout under dataDir (default ~/.vulos/auth/):
//
//	fingerprint_{profileID}.json  — enrolled flag + failure counter

package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ─── constants ────────────────────────────────────────────────────────────────

const (
	// fprintMaxFails is the number of consecutive fingerprint verification
	// failures before the service requires fallback to PIN/password.
	fprintMaxFails = 3

	// fprintd D-Bus service name and object paths.
	fprintService      = "net.reactivated.Fprint"
	fprintManagerPath  = "/net/reactivated/Fprint/Manager"
	fprintManagerIface = "net.reactivated.Fprint.Manager"
	fprintDeviceIface  = "net.reactivated.Fprint.Device"

	// fprintVerifyTimeout is the maximum time to wait for fprintd to report a
	// verify result via its VerifyStatus D-Bus signal.
	fprintVerifyTimeout = 30 * time.Second

	// fprintStateVersion is the on-disk format version for fingerprint state.
	fprintStateVersion = 1
)

// ─── errors ───────────────────────────────────────────────────────────────────

var (
	// ErrFingerprintUnavailable is returned when fprintd is not present or
	// reports no supported fingerprint reader.
	ErrFingerprintUnavailable = errors.New("fingerprint: fprintd unavailable or no supported reader")

	// ErrFingerprintNotEnrolled is returned when the profile has no enrolled
	// finger on this device.
	ErrFingerprintNotEnrolled = errors.New("fingerprint: no enrolled fingerprint for this profile")

	// ErrFingerprintMismatch is returned when verification completes but the
	// presented finger does not match.
	ErrFingerprintMismatch = errors.New("fingerprint: finger did not match")

	// ErrFingerprintMaxFails is returned when 3 consecutive mismatches occur.
	// The caller must switch to PIN/password.
	ErrFingerprintMaxFails = errors.New("fingerprint: too many failures — use PIN or password")

	// ErrFingerprintTimeout is returned when fprintd does not respond within
	// fprintVerifyTimeout.
	ErrFingerprintTimeout = errors.New("fingerprint: reader timed out — try again")
)

// ─── state ────────────────────────────────────────────────────────────────────

// fprintState is the per-profile on-disk state for CLOGIN-07.
type fprintState struct {
	Version  int  `json:"v"`
	Enrolled bool `json:"enrolled"` // at least one finger enrolled via this service
	Failures int  `json:"failures"` // consecutive verification failures
	Disabled bool `json:"disabled"` // user explicitly disabled fingerprint unlock
}

// ─── FingerprintService ───────────────────────────────────────────────────────

// FingerprintService manages fingerprint unlock for CLOGIN-07.
// All exported methods are safe for concurrent use.
//
// The service delegates all biometric operations to fprintd via gdbus calls.
// It never handles raw fingerprint data.
type FingerprintService struct {
	mu      sync.Mutex
	dataDir string            // e.g. ~/.vulos/auth
	pinSvc  *DevicePINService // to resolve the session credential on verify success
}

// NewFingerprintService creates a FingerprintService.
//
//   - dataDir is the directory where per-profile state files are stored
//     (default: ~/.vulos/auth/).
//   - pinSvc is the DevicePINService whose wrapped session credential is
//     returned on successful fingerprint verification.
func NewFingerprintService(dataDir string, pinSvc *DevicePINService) (*FingerprintService, error) {
	if dataDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("fingerprint: cannot determine home dir: %w", err)
		}
		dataDir = filepath.Join(home, ".vulos", "auth")
	}
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return nil, fmt.Errorf("fingerprint: mkdir %s: %w", dataDir, err)
	}
	return &FingerprintService{
		dataDir: dataDir,
		pinSvc:  pinSvc,
	}, nil
}

// ─── hardware detection ───────────────────────────────────────────────────────

// IsAvailable returns true when:
//  1. The gdbus binary is present on PATH.
//  2. fprintd is running and its Manager interface is reachable.
//  3. fprintd reports at least one usable fingerprint device (GetDefaultDevice
//     succeeds and the returned path is non-empty).
//
// This is the gate used by the Settings UI: only show "Add fingerprint" when
// IsAvailable() returns true.
func (s *FingerprintService) IsAvailable() bool {
	if _, err := exec.LookPath("gdbus"); err != nil {
		return false
	}
	path, err := s.getDefaultDevice()
	if err != nil || path == "" {
		return false
	}
	return true
}

// getDefaultDevice calls net.reactivated.Fprint.Manager.GetDefaultDevice() via
// gdbus and returns the D-Bus object path of the default fingerprint reader.
// Returns an empty string (not an error) when no device is present.
func (s *FingerprintService) getDefaultDevice() (string, error) {
	out, err := exec.Command("gdbus", "call", "--system",
		"--dest", fprintService,
		"--object-path", fprintManagerPath,
		"--method", fprintManagerIface+".GetDefaultDevice",
	).Output()
	if err != nil {
		// fprintd is not running or not installed — not an error we surface.
		return "", nil
	}
	return parseFprintObjectPath(string(out)), nil
}

// ─── enrollment ───────────────────────────────────────────────────────────────

// StartEnroll initiates the fprintd enrollment flow for the given profile.
// It calls fprintd's Device.EnrollStart for the "right-index-finger" slot
// (the conventional default; the user can re-enroll any finger).
//
// Returns ErrFingerprintUnavailable when no device is present.
func (s *FingerprintService) StartEnroll(profileID string) error {
	if !s.IsAvailable() {
		return ErrFingerprintUnavailable
	}
	devPath, err := s.getDefaultDevice()
	if err != nil || devPath == "" {
		return ErrFingerprintUnavailable
	}
	_, err = exec.Command("gdbus", "call", "--system",
		"--dest", fprintService,
		"--object-path", devPath,
		"--method", fprintDeviceIface+".EnrollStart",
		"'right-index-finger'",
	).Output()
	if err != nil {
		return fmt.Errorf("fingerprint: EnrollStart: %w", err)
	}
	log.Printf("[fingerprint] enrollment started for profile %s", profileID)
	return nil
}

// StopEnroll stops an ongoing enrollment flow and, if fprintd now reports an
// enrolled finger, marks the profile as enrolled in the state file.
func (s *FingerprintService) StopEnroll(profileID string) error {
	devPath, _ := s.getDefaultDevice()
	if devPath == "" {
		return ErrFingerprintUnavailable
	}
	_, _ = exec.Command("gdbus", "call", "--system",
		"--dest", fprintService,
		"--object-path", devPath,
		"--method", fprintDeviceIface+".EnrollStop",
	).Output()

	enrolled := s.hasEnrolledFinger(devPath)
	if enrolled {
		s.mu.Lock()
		defer s.mu.Unlock()
		st, _ := s.readState(profileID)
		st.Enrolled = true
		st.Failures = 0
		if err := s.writeState(profileID, st); err != nil {
			return fmt.Errorf("fingerprint: write state: %w", err)
		}
		log.Printf("[fingerprint] enrollment completed for profile %s", profileID)
	}
	return nil
}

// hasEnrolledFinger asks fprintd whether any finger is enrolled.
func (s *FingerprintService) hasEnrolledFinger(devPath string) bool {
	out, err := exec.Command("gdbus", "call", "--system",
		"--dest", fprintService,
		"--object-path", devPath,
		"--method", fprintDeviceIface+".ListEnrolledFingers",
		"''",
	).Output()
	if err != nil {
		return false
	}
	result := strings.TrimSpace(string(out))
	// Any non-empty, non-empty-array response means enrolled.
	return strings.Contains(result, "finger") ||
		(strings.Contains(result, "'") && !strings.Contains(result, "[]"))
}

// IsEnrolled returns true if the given profile has at least one enrolled finger.
func (s *FingerprintService) IsEnrolled(profileID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, _ := s.readState(profileID)
	return st.Enrolled && !st.Disabled
}

// RemoveEnrollment clears the enrolled state for the given profile.
// Requires a full-auth session (enforced at the handler layer).
func (s *FingerprintService) RemoveEnrollment(profileID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, _ := s.readState(profileID)
	st.Enrolled = false
	st.Failures = 0
	return s.writeState(profileID, st)
}

// ─── verification ─────────────────────────────────────────────────────────────

// Verify asks fprintd to verify a finger for the given profile and, on
// success, returns the OS session token wrapped by the DevicePINService.
//
// Failure policy:
//   - ErrFingerprintMismatch: single failure; caller can retry.
//   - ErrFingerprintMaxFails: 3rd consecutive failure; switch to PIN/password.
//   - ErrFingerprintTimeout: reader did not respond within fprintVerifyTimeout.
//   - ErrFingerprintNotEnrolled: profile has no enrolled finger.
func (s *FingerprintService) Verify(profileID string) (sessionToken string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	st, _ := s.readState(profileID)

	if !st.Enrolled || st.Disabled {
		return "", ErrFingerprintNotEnrolled
	}
	if st.Failures >= fprintMaxFails {
		return "", ErrFingerprintMaxFails
	}

	if !s.IsAvailable() {
		return "", ErrFingerprintUnavailable
	}

	devPath, err2 := s.getDefaultDevice()
	if err2 != nil || devPath == "" {
		return "", ErrFingerprintUnavailable
	}

	// Start verification — "any" lets fprintd pick the best enrolled finger.
	_, startErr := exec.Command("gdbus", "call", "--system",
		"--dest", fprintService,
		"--object-path", devPath,
		"--method", fprintDeviceIface+".VerifyStart",
		"'any'",
	).Output()
	if startErr != nil {
		return "", fmt.Errorf("fingerprint: VerifyStart: %w", startErr)
	}

	// Wait for the VerifyStatus signal via gdbus monitor.
	result, monErr := waitForVerifyResult(fprintVerifyTimeout)

	// Always stop the verification session, regardless of outcome.
	_, _ = exec.Command("gdbus", "call", "--system",
		"--dest", fprintService,
		"--object-path", devPath,
		"--method", fprintDeviceIface+".VerifyStop",
	).Output()

	if monErr != nil {
		return "", monErr
	}

	if result != "verify-match" {
		st.Failures++
		if st.Failures >= fprintMaxFails {
			_ = s.writeState(profileID, st)
			return "", ErrFingerprintMaxFails
		}
		_ = s.writeState(profileID, st)
		return "", ErrFingerprintMismatch
	}

	// Success — reset failure counter.
	st.Failures = 0
	_ = s.writeState(profileID, st)

	// Return the PIN-wrapped session credential so the fingerprint unlock
	// reuses exactly the same credential as CLOGIN-06 PIN unlock.
	if s.pinSvc == nil {
		return "", fmt.Errorf("fingerprint: no DevicePINService configured")
	}
	token, pinErr := s.pinSvc.currentSessionToken()
	if pinErr != nil {
		return "", fmt.Errorf("fingerprint: cannot retrieve session token: %w", pinErr)
	}
	return token, nil
}

// waitForVerifyResult starts gdbus monitor and returns the first VerifyStatus
// result string emitted by fprintd.  Returns ErrFingerprintTimeout if no
// signal arrives within d.
func waitForVerifyResult(d time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()

	cmd := exec.CommandContext(ctx, "gdbus", "monitor", "--system",
		"--dest", fprintService,
	)
	out, err := cmd.Output()
	if ctx.Err() == context.DeadlineExceeded {
		return "", ErrFingerprintTimeout
	}
	if err != nil {
		return "", fmt.Errorf("fingerprint: monitor: %w", err)
	}
	result := parseVerifyResult(string(out))
	if result == "" {
		return "", ErrFingerprintTimeout
	}
	return result, nil
}

// parseVerifyResult scans gdbus monitor output for a VerifyStatus signal and
// extracts the result string.  fprintd emits:
//
//	/net/reactivated/Fprint/Device/0: net.reactivated.Fprint.Device.VerifyStatus ('verify-match', true)
func parseVerifyResult(monitorOutput string) string {
	const marker = "VerifyStatus ("
	idx := strings.Index(monitorOutput, marker)
	if idx < 0 {
		return ""
	}
	rest := monitorOutput[idx+len(marker):]
	start := strings.Index(rest, "'")
	if start < 0 {
		return ""
	}
	rest = rest[start+1:]
	end := strings.Index(rest, "'")
	if end < 0 {
		return ""
	}
	return rest[:end]
}

// ─── ResetFailures ────────────────────────────────────────────────────────────

// ResetFailures clears the consecutive failure counter for the given profile.
// Must be called after a successful full-auth (password + 2FA) session to
// re-enable fingerprint unlock after 3 failures.
func (s *FingerprintService) ResetFailures(profileID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, _ := s.readState(profileID)
	st.Failures = 0
	return s.writeState(profileID, st)
}

// ─── FingerprintStatus ────────────────────────────────────────────────────────

// FingerprintStatus is the JSON body returned by GET /api/auth/fingerprint/status.
type FingerprintStatus struct {
	Available    bool   `json:"available"`     // fprintd reachable + device present
	Enrolled     bool   `json:"enrolled"`      // profile has enrolled finger
	Disabled     bool   `json:"disabled"`      // user explicitly disabled
	FailuresLeft int    `json:"failures_left"` // remaining attempts before fallback
	HardwareNote string `json:"hardware_note"` // human-readable device name
}

// Status returns the fingerprint status for the given profile without modifying state.
func (s *FingerprintService) Status(profileID string) FingerprintStatus {
	available := s.IsAvailable()
	s.mu.Lock()
	defer s.mu.Unlock()
	st, _ := s.readState(profileID)

	left := fprintMaxFails - st.Failures
	if left < 0 {
		left = 0
	}

	note := ""
	if available {
		devPath, _ := s.getDefaultDevice()
		note = s.deviceNote(devPath)
	}

	return FingerprintStatus{
		Available:    available,
		Enrolled:     st.Enrolled && !st.Disabled,
		Disabled:     st.Disabled,
		FailuresLeft: left,
		HardwareNote: note,
	}
}

// deviceNote returns a human-readable description of the fingerprint device.
func (s *FingerprintService) deviceNote(devPath string) string {
	if devPath == "" {
		return ""
	}
	out, err := exec.Command("gdbus", "call", "--system",
		"--dest", fprintService,
		"--object-path", devPath,
		"--method", "org.freedesktop.DBus.Properties.GetAll",
		fprintDeviceIface,
	).Output()
	if err != nil {
		return devPath
	}
	// Extract the 'name' property.
	// fprintd returns: {'name': <'Goodix MOC Fingerprint Sensor'>, ...}
	raw := string(out)
	const nameKey = "'name': <'"
	idx := strings.Index(raw, nameKey)
	if idx < 0 {
		return devPath
	}
	rest := raw[idx+len(nameKey):]
	end := strings.Index(rest, "'")
	if end <= 0 {
		return devPath
	}
	return rest[:end]
}

// currentSessionToken returns the single session token stored in the PIN
// service's session map.  Used by Verify to hand the same credential to the
// fingerprint caller.
func (s *DevicePINService) currentSessionToken() (string, error) {
	if s == nil {
		return "", fmt.Errorf("devicepin: service not configured")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.hasPINUnsafe() {
		return "", ErrPINNotSet
	}
	m, err := s.readSessionMap()
	if err != nil {
		return "", err
	}
	for _, tok := range m {
		return tok, nil // return the first (and typically only) entry
	}
	return "", fmt.Errorf("devicepin: session map is empty")
}

// ─── per-profile state helpers ────────────────────────────────────────────────

func (s *FingerprintService) statePath(profileID string) string {
	safe := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, profileID)
	return filepath.Join(s.dataDir, "fingerprint_"+safe+".json")
}

func (s *FingerprintService) readState(profileID string) (fprintState, error) {
	data, err := os.ReadFile(s.statePath(profileID))
	if os.IsNotExist(err) {
		return fprintState{Version: fprintStateVersion}, nil
	}
	if err != nil {
		return fprintState{Version: fprintStateVersion}, err
	}
	var st fprintState
	if err := json.Unmarshal(data, &st); err != nil {
		return fprintState{Version: fprintStateVersion}, err
	}
	return st, nil
}

func (s *FingerprintService) writeState(profileID string, st fprintState) error {
	st.Version = fprintStateVersion
	data, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return atomicWrite(s.statePath(profileID), data, 0600)
}

// ─── D-Bus path parsing ───────────────────────────────────────────────────────

// parseFprintObjectPath extracts the first D-Bus object path (starting with /)
// from gdbus output.
//
// gdbus returns:  (objectpath '/net/reactivated/Fprint/Device/0',)
func parseFprintObjectPath(out string) string {
	for _, delim := range []string{"objectpath '", "'/", "\"/"} {
		idx := strings.Index(out, delim)
		if idx < 0 {
			continue
		}
		var start int
		if delim == "objectpath '" {
			start = idx + len("objectpath '")
		} else {
			start = idx + 1
		}
		rest := out[start:]
		for _, end := range []string{"'", "\"", ",", ")", " ", "\n"} {
			if ei := strings.Index(rest, end); ei > 0 {
				rest = rest[:ei]
			}
		}
		if strings.HasPrefix(rest, "/") {
			return rest
		}
	}
	return ""
}
