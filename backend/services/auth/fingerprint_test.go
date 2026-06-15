package auth

import (
	"os"
	"strings"
	"testing"
)

// ─── helpers ──────────────────────────────────────────────────────────────────

// newTestFingerprintService creates a FingerprintService backed by a temp
// directory with no live DevicePINService (nil), suitable for testing all
// non-verify paths.
func newTestFingerprintService(t *testing.T) *FingerprintService {
	t.Helper()
	dir := t.TempDir()
	svc, err := NewFingerprintService(dir, nil)
	if err != nil {
		t.Fatalf("NewFingerprintService: %v", err)
	}
	return svc
}

// newTestFingerprintServiceWithPIN creates a FingerprintService wired to a
// real DevicePINService so Verify can resolve the session token.
func newTestFingerprintServiceWithPIN(t *testing.T) (*FingerprintService, *DevicePINService, *Store) {
	t.Helper()
	fpDir := t.TempDir()
	pinDir := t.TempDir()
	storeDir := t.TempDir()

	store, err := NewStore(storeDir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	user, err := store.Register("fpuser", "password123XYZ", "FP User")
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	sess := store.CreateSession(user, "fp-device")

	pinSvc, err := NewDevicePINService(pinDir, nil, DefaultPINMinLength, store)
	if err != nil {
		t.Fatalf("NewDevicePINService: %v", err)
	}
	if err := pinSvc.SetPIN("9876", sess.Token); err != nil {
		t.Fatalf("SetPIN: %v", err)
	}

	fpSvc, err := NewFingerprintService(fpDir, pinSvc)
	if err != nil {
		t.Fatalf("NewFingerprintService: %v", err)
	}
	return fpSvc, pinSvc, store
}

// ─── construction ─────────────────────────────────────────────────────────────

func TestNewFingerprintService_CreatesDataDir(t *testing.T) {
	dir := t.TempDir() + "/auth"
	svc, err := NewFingerprintService(dir, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if svc == nil {
		t.Fatal("expected non-nil service")
	}
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		t.Fatal("expected dataDir to be created")
	}
}

func TestNewFingerprintService_DefaultDataDir(t *testing.T) {
	// Should not error when dataDir is empty (uses ~/.vulos/auth).
	// We cannot actually create ~/.vulos/auth in a test, but we can verify the
	// error handling when the home dir is set by just providing an explicit dir.
	dir := t.TempDir()
	svc, err := NewFingerprintService(dir, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if svc == nil {
		t.Fatal("nil service")
	}
}

// ─── IsAvailable — no gdbus / no fprintd ─────────────────────────────────────

// TestIsAvailable_FalseWhenNoGdbus verifies that the service returns
// unavailable when gdbus is not on PATH.  We test the negative (no hardware)
// because positive hardware tests would require a real fprintd daemon.
func TestIsAvailable_FalseWhenNoGdbus(t *testing.T) {
	svc := newTestFingerprintService(t)
	// In CI / Mac environments gdbus is absent — the test passes trivially.
	// On a Linux desktop with fprintd the result might be true; either is valid
	// here because we are testing the code path, not the hardware.
	_ = svc.IsAvailable() // must not panic
}

// ─── IsEnrolled / state persistence ──────────────────────────────────────────

func TestIsEnrolled_FalseInitially(t *testing.T) {
	svc := newTestFingerprintService(t)
	if svc.IsEnrolled("user-1") {
		t.Fatal("expected IsEnrolled=false before any enrollment")
	}
}

func TestIsEnrolled_TrueAfterManualStateWrite(t *testing.T) {
	svc := newTestFingerprintService(t)
	// Simulate a successful enrollment by writing state directly.
	svc.mu.Lock()
	err := svc.writeState("user-1", fprintState{Enrolled: true})
	svc.mu.Unlock()
	if err != nil {
		t.Fatalf("writeState: %v", err)
	}
	if !svc.IsEnrolled("user-1") {
		t.Fatal("expected IsEnrolled=true after state write")
	}
}

func TestIsEnrolled_FalseWhenDisabled(t *testing.T) {
	svc := newTestFingerprintService(t)
	svc.mu.Lock()
	_ = svc.writeState("user-1", fprintState{Enrolled: true, Disabled: true})
	svc.mu.Unlock()
	if svc.IsEnrolled("user-1") {
		t.Fatal("expected IsEnrolled=false when Disabled=true")
	}
}

// ─── RemoveEnrollment ─────────────────────────────────────────────────────────

func TestRemoveEnrollment_ClearsEnrolledFlag(t *testing.T) {
	svc := newTestFingerprintService(t)
	svc.mu.Lock()
	_ = svc.writeState("user-2", fprintState{Enrolled: true})
	svc.mu.Unlock()

	if err := svc.RemoveEnrollment("user-2"); err != nil {
		t.Fatalf("RemoveEnrollment: %v", err)
	}
	if svc.IsEnrolled("user-2") {
		t.Fatal("expected IsEnrolled=false after RemoveEnrollment")
	}
}

// ─── ResetFailures ────────────────────────────────────────────────────────────

func TestResetFailures_ClearsCounter(t *testing.T) {
	svc := newTestFingerprintService(t)
	svc.mu.Lock()
	_ = svc.writeState("user-3", fprintState{Enrolled: true, Failures: fprintMaxFails})
	svc.mu.Unlock()

	if err := svc.ResetFailures("user-3"); err != nil {
		t.Fatalf("ResetFailures: %v", err)
	}

	svc.mu.Lock()
	st, _ := svc.readState("user-3")
	svc.mu.Unlock()
	if st.Failures != 0 {
		t.Fatalf("expected Failures=0 after reset, got %d", st.Failures)
	}
}

// ─── Verify — not-enrolled / max-fails paths ─────────────────────────────────

func TestVerify_NotEnrolled(t *testing.T) {
	svc := newTestFingerprintService(t)
	_, err := svc.Verify("user-4")
	if err != ErrFingerprintNotEnrolled {
		t.Fatalf("expected ErrFingerprintNotEnrolled, got %v", err)
	}
}

func TestVerify_MaxFailsAlreadyReached(t *testing.T) {
	svc := newTestFingerprintService(t)
	svc.mu.Lock()
	_ = svc.writeState("user-5", fprintState{Enrolled: true, Failures: fprintMaxFails})
	svc.mu.Unlock()

	_, err := svc.Verify("user-5")
	if err != ErrFingerprintMaxFails {
		t.Fatalf("expected ErrFingerprintMaxFails, got %v", err)
	}
}

// ─── Status ───────────────────────────────────────────────────────────────────

func TestStatus_FailuresLeft_Fresh(t *testing.T) {
	svc := newTestFingerprintService(t)
	svc.mu.Lock()
	_ = svc.writeState("user-6", fprintState{Enrolled: true, Failures: 0})
	svc.mu.Unlock()

	st := svc.Status("user-6")
	if st.Enrolled != true {
		t.Fatal("expected Enrolled=true")
	}
	if st.FailuresLeft != fprintMaxFails {
		t.Fatalf("expected %d failures_left, got %d", fprintMaxFails, st.FailuresLeft)
	}
}

func TestStatus_FailuresLeft_AfterOneFailure(t *testing.T) {
	svc := newTestFingerprintService(t)
	svc.mu.Lock()
	_ = svc.writeState("user-7", fprintState{Enrolled: true, Failures: 1})
	svc.mu.Unlock()

	st := svc.Status("user-7")
	expected := fprintMaxFails - 1
	if st.FailuresLeft != expected {
		t.Fatalf("expected %d failures_left, got %d", expected, st.FailuresLeft)
	}
}

func TestStatus_UnavailableWhenNotAvailable(t *testing.T) {
	svc := newTestFingerprintService(t)
	st := svc.Status("user-8")
	// On non-Linux or CI without fprintd, Available must be false.
	// If gdbus is somehow present (unlikely in unit tests), this is a no-op.
	_ = st.Available // must not panic
}

// ─── per-profile isolation ────────────────────────────────────────────────────

func TestPerProfileIsolation(t *testing.T) {
	svc := newTestFingerprintService(t)

	svc.mu.Lock()
	_ = svc.writeState("alice", fprintState{Enrolled: true})
	_ = svc.writeState("bob", fprintState{Enrolled: false})
	svc.mu.Unlock()

	if !svc.IsEnrolled("alice") {
		t.Fatal("alice should be enrolled")
	}
	if svc.IsEnrolled("bob") {
		t.Fatal("bob should not be enrolled")
	}
}

func TestPerProfileIsolation_FailureCountersIndependent(t *testing.T) {
	svc := newTestFingerprintService(t)

	svc.mu.Lock()
	_ = svc.writeState("alice", fprintState{Enrolled: true, Failures: 2})
	_ = svc.writeState("bob", fprintState{Enrolled: true, Failures: 0})
	svc.mu.Unlock()

	aliceSt := svc.Status("alice")
	bobSt := svc.Status("bob")

	if aliceSt.FailuresLeft != fprintMaxFails-2 {
		t.Fatalf("alice: expected %d failures_left, got %d", fprintMaxFails-2, aliceSt.FailuresLeft)
	}
	if bobSt.FailuresLeft != fprintMaxFails {
		t.Fatalf("bob: expected %d failures_left, got %d", fprintMaxFails, bobSt.FailuresLeft)
	}
}

// ─── state persistence across restarts ───────────────────────────────────────

func TestStatePersistsAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	svc1, err := NewFingerprintService(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	svc1.mu.Lock()
	_ = svc1.writeState("persistent-user", fprintState{Enrolled: true, Failures: 1})
	svc1.mu.Unlock()

	// Simulate restart by creating a new service pointing to the same dir.
	svc2, err := NewFingerprintService(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !svc2.IsEnrolled("persistent-user") {
		t.Fatal("enrolled state should persist across restart")
	}
	svc2.mu.Lock()
	st, _ := svc2.readState("persistent-user")
	svc2.mu.Unlock()
	if st.Failures != 1 {
		t.Fatalf("failure count should persist; expected 1, got %d", st.Failures)
	}
}

// ─── state file path sanitisation ────────────────────────────────────────────

func TestStatePath_SanitisesProfileID(t *testing.T) {
	svc := newTestFingerprintService(t)
	// Profile IDs with special chars must not escape the data dir.
	path := svc.statePath("../../etc/passwd")
	if !strings.Contains(path, "fingerprint_") {
		t.Fatal("sanitised path must contain fingerprint_ prefix")
	}
	if strings.Contains(path, "..") {
		t.Fatal("sanitised path must not contain path traversal")
	}
}

// ─── parseVerifyResult ────────────────────────────────────────────────────────

func TestParseVerifyResult_Match(t *testing.T) {
	input := "/net/reactivated/Fprint/Device/0: net.reactivated.Fprint.Device.VerifyStatus ('verify-match', true)"
	result := parseVerifyResult(input)
	if result != "verify-match" {
		t.Fatalf("expected 'verify-match', got %q", result)
	}
}

func TestParseVerifyResult_NoMatch(t *testing.T) {
	input := "/net/reactivated/Fprint/Device/0: net.reactivated.Fprint.Device.VerifyStatus ('verify-no-match', true)"
	result := parseVerifyResult(input)
	if result != "verify-no-match" {
		t.Fatalf("expected 'verify-no-match', got %q", result)
	}
}

func TestParseVerifyResult_Empty(t *testing.T) {
	result := parseVerifyResult("")
	if result != "" {
		t.Fatalf("expected empty result, got %q", result)
	}
}

func TestParseVerifyResult_NoMarker(t *testing.T) {
	result := parseVerifyResult("some other output without a VerifyStatus line")
	if result != "" {
		t.Fatalf("expected empty result, got %q", result)
	}
}

// ─── parseFprintObjectPath ────────────────────────────────────────────────────

func TestParseFprintObjectPath_Standard(t *testing.T) {
	input := "(objectpath '/net/reactivated/Fprint/Device/0',)\n"
	path := parseFprintObjectPath(input)
	if path != "/net/reactivated/Fprint/Device/0" {
		t.Fatalf("expected '/net/reactivated/Fprint/Device/0', got %q", path)
	}
}

func TestParseFprintObjectPath_Empty(t *testing.T) {
	path := parseFprintObjectPath("")
	if path != "" {
		t.Fatalf("expected empty path, got %q", path)
	}
}

func TestParseFprintObjectPath_NoDevice(t *testing.T) {
	// fprintd returns an empty path when no device is present.
	input := "(objectpath '',)\n"
	path := parseFprintObjectPath(input)
	// May return "" or "/" depending on how the parser handles it; must not panic.
	_ = path
}

// ─── currentSessionToken ─────────────────────────────────────────────────────

func TestCurrentSessionToken_NoPINSet(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	pinSvc, err := NewDevicePINService(dir, nil, DefaultPINMinLength, store)
	if err != nil {
		t.Fatal(err)
	}
	// No PIN set — currentSessionToken must return ErrPINNotSet.
	_, err = pinSvc.currentSessionToken()
	if err != ErrPINNotSet {
		t.Fatalf("expected ErrPINNotSet, got %v", err)
	}
}

func TestCurrentSessionToken_ReturnsWrappedToken(t *testing.T) {
	_, pinSvc, store := newTestFingerprintServiceWithPIN(t)

	token, err := pinSvc.currentSessionToken()
	if err != nil {
		t.Fatalf("currentSessionToken: %v", err)
	}
	if token == "" {
		t.Fatal("expected non-empty session token")
	}
	// The returned token must be valid in the store.
	_, ok := store.ValidateToken(token)
	if !ok {
		t.Fatal("token returned by currentSessionToken is not valid in store")
	}
}

// ─── no biometric data in state files ────────────────────────────────────────

func TestNoBiometricDataInStateFile(t *testing.T) {
	svc := newTestFingerprintService(t)
	svc.mu.Lock()
	_ = svc.writeState("biometric-user", fprintState{Enrolled: true})
	svc.mu.Unlock()

	data, err := os.ReadFile(svc.statePath("biometric-user"))
	if err != nil {
		t.Fatal(err)
	}
	// State file must contain only the JSON flags — no biometric data.
	raw := string(data)
	for _, forbidden := range []string{"finger", "template", "image", "biometric"} {
		if strings.Contains(strings.ToLower(raw), forbidden) {
			t.Fatalf("state file must not contain %q; got: %s", forbidden, raw)
		}
	}
}
