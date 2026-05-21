package auth

import (
	"encoding/json"
	"os"
	"strconv"
	"testing"
	"time"

	"golang.org/x/crypto/argon2"
)

// ─── helpers ──────────────────────────────────────────────────────────────────

// newTestPINService creates a DevicePINService backed by a temp directory,
// with no TPM (software fallback) and the default 4-digit minimum.
func newTestPINService(t *testing.T) (*DevicePINService, *Store) {
	t.Helper()
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	// Register a user + session so we have a real session token to wrap.
	user, err := store.Register("testuser", "password123", "Test User")
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	sess := store.CreateSession(user, "device-test")

	pinDir := t.TempDir()
	svc, err := NewDevicePINService(pinDir, nil, DefaultPINMinLength, store)
	if err != nil {
		t.Fatalf("NewDevicePINService: %v", err)
	}
	// Pre-set a PIN wrapping the real session token.
	if err := svc.SetPIN("1234", sess.Token); err != nil {
		t.Fatalf("SetPIN: %v", err)
	}
	return svc, store
}

// ─── format validation ────────────────────────────────────────────────────────

func TestPINFormat_TooShort(t *testing.T) {
	dir := t.TempDir()
	svc, err := NewDevicePINService(dir, nil, 4, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.SetPIN("123", "tok"); err == nil {
		t.Fatal("expected error for 3-digit PIN")
	}
}

func TestPINFormat_TooLong(t *testing.T) {
	dir := t.TempDir()
	svc, err := NewDevicePINService(dir, nil, 4, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.SetPIN("123456789", "tok"); err == nil {
		t.Fatal("expected error for 9-digit PIN")
	}
}

func TestPINFormat_NonDigits(t *testing.T) {
	dir := t.TempDir()
	svc, err := NewDevicePINService(dir, nil, 4, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.SetPIN("12ab", "tok"); err == nil {
		t.Fatal("expected error for non-digit PIN")
	}
}

func TestPINFormat_MinLength_Configurable(t *testing.T) {
	dir := t.TempDir()
	svc, err := NewDevicePINService(dir, nil, 6, nil)
	if err != nil {
		t.Fatal(err)
	}
	// 5 digits should fail with min=6
	if err := svc.SetPIN("12345", "tok"); err == nil {
		t.Fatal("expected error for 5-digit PIN with min=6")
	}
	// 6 digits should pass format check (no store → session map won't be validated)
	if err := svc.SetPIN("123456", "tok"); err != nil {
		t.Fatalf("unexpected error for 6-digit PIN with min=6: %v", err)
	}
}

// ─── has-PIN / clear ─────────────────────────────────────────────────────────

func TestHasPIN_FalseWhenNotSet(t *testing.T) {
	dir := t.TempDir()
	svc, err := NewDevicePINService(dir, nil, 4, nil)
	if err != nil {
		t.Fatal(err)
	}
	if svc.HasPIN() {
		t.Fatal("expected HasPIN=false")
	}
}

func TestHasPIN_TrueAfterSet(t *testing.T) {
	svc, _ := newTestPINService(t)
	if !svc.HasPIN() {
		t.Fatal("expected HasPIN=true")
	}
}

func TestClearPIN(t *testing.T) {
	svc, _ := newTestPINService(t)
	if err := svc.SetPIN("", ""); err != nil {
		t.Fatalf("clear PIN: %v", err)
	}
	if svc.HasPIN() {
		t.Fatal("expected HasPIN=false after clear")
	}
}

// ─── correct PIN ─────────────────────────────────────────────────────────────

func TestValidatePIN_Correct(t *testing.T) {
	svc, store := newTestPINService(t)

	token, err := svc.ValidatePIN("1234")
	if err != nil {
		t.Fatalf("ValidatePIN correct: %v", err)
	}
	if token == "" {
		t.Fatal("expected non-empty session token")
	}
	// Verify the returned token is valid in the store.
	_, ok := store.ValidateToken(token)
	if !ok {
		t.Fatal("returned token is not valid in store")
	}
}

// ─── wrong PIN / lockout ─────────────────────────────────────────────────────

func TestValidatePIN_WrongPIN(t *testing.T) {
	svc, _ := newTestPINService(t)
	_, err := svc.ValidatePIN("9999")
	if err != ErrPINWrong {
		t.Fatalf("expected ErrPINWrong, got %v", err)
	}
}

func TestValidatePIN_FiveWrong_TriggersLockout(t *testing.T) {
	svc, _ := newTestPINService(t)

	for i := 0; i < pinMaxAttempts-1; i++ {
		_, err := svc.ValidatePIN("0000")
		if err != ErrPINWrong {
			t.Fatalf("attempt %d: expected ErrPINWrong, got %v", i+1, err)
		}
	}

	// 5th wrong attempt triggers lockout.
	_, err := svc.ValidatePIN("0000")
	if err != ErrPINLocked {
		t.Fatalf("expected ErrPINLocked after %d wrong attempts, got %v", pinMaxAttempts, err)
	}
}

func TestValidatePIN_LockedOut_CorrectPINBlocked(t *testing.T) {
	svc, _ := newTestPINService(t)

	// Trigger lockout.
	for i := 0; i < pinMaxAttempts; i++ {
		svc.ValidatePIN("0000") //nolint:errcheck
	}

	// Even the correct PIN should be blocked.
	_, err := svc.ValidatePIN("1234")
	if err != ErrPINLocked {
		t.Fatalf("expected ErrPINLocked, got %v", err)
	}
}

func TestValidatePIN_ThreeLockouts_PermanentLock(t *testing.T) {
	svc, _ := newTestPINService(t)

	// Force 3 lockouts by directly manipulating state (white-box).
	for lockout := 0; lockout < pinMaxLockouts; lockout++ {
		// Trigger a lockout: 5 wrong attempts.
		for attempt := 0; attempt < pinMaxAttempts; attempt++ {
			svc.ValidatePIN("0000") //nolint:errcheck
		}
		// Wind the clock forward past the lockout window by writing a cleared state
		// (simulating lockout expiry) except on the last iteration.
		if lockout < pinMaxLockouts-1 {
			st, _ := svc.readState()
			st.LockedUntil = time.Now().Add(-1 * time.Second) // expired
			st.Attempts = 0
			svc.writeState(st) //nolint:errcheck
		}
	}

	_, err := svc.ValidatePIN("1234")
	if err != ErrPINPermanentLocked {
		t.Fatalf("expected ErrPINPermanentLocked after %d lockouts, got %v", pinMaxLockouts, err)
	}
}

func TestResetLockout_ClearsPermanentLock(t *testing.T) {
	svc, _ := newTestPINService(t)

	// Force permanent lock.
	for lockout := 0; lockout < pinMaxLockouts; lockout++ {
		for attempt := 0; attempt < pinMaxAttempts; attempt++ {
			svc.ValidatePIN("0000") //nolint:errcheck
		}
		if lockout < pinMaxLockouts-1 {
			st, _ := svc.readState()
			st.LockedUntil = time.Now().Add(-1 * time.Second)
			st.Attempts = 0
			svc.writeState(st) //nolint:errcheck
		}
	}

	if err := svc.ResetLockout(); err != nil {
		t.Fatalf("ResetLockout: %v", err)
	}

	// Should be able to validate again.
	_, err := svc.ValidatePIN("1234")
	if err != nil {
		t.Fatalf("expected success after ResetLockout, got %v", err)
	}
}

// ─── argon2id derivation is deterministic ────────────────────────────────────

func TestArgon2id_DerivedKeyDeterministic(t *testing.T) {
	// The same PIN + salt must produce the same key every time.
	salt := make([]byte, 16)
	for i := range salt {
		salt[i] = byte(i)
	}
	k1 := deriveWrapKeyForTest("5678", salt)
	k2 := deriveWrapKeyForTest("5678", salt)
	if string(k1) != string(k2) {
		t.Fatal("argon2id key derivation is not deterministic")
	}
}

func TestArgon2id_DifferentPINsDifferentKeys(t *testing.T) {
	salt := make([]byte, 16)
	k1 := deriveWrapKeyForTest("1111", salt)
	k2 := deriveWrapKeyForTest("2222", salt)
	if string(k1) == string(k2) {
		t.Fatal("different PINs produced the same key")
	}
}

// ─── pin.bin is not human-readable / PIN not stored in plaintext ─────────────

func TestPINNotStoredInPlaintext(t *testing.T) {
	svc, _ := newTestPINService(t)

	data, err := os.ReadFile(svc.pinBinPath())
	if err != nil {
		t.Fatalf("read pin.bin: %v", err)
	}
	// The PIN "1234" must not appear as a literal substring.
	if contains(data, []byte("1234")) {
		t.Fatal("PIN appears in plaintext in pin.bin")
	}
}

// ─── lockout status API ───────────────────────────────────────────────────────

func TestStatus_FreshService(t *testing.T) {
	svc, _ := newTestPINService(t)
	st := svc.Status()
	if st.Locked {
		t.Fatal("expected Locked=false on fresh service")
	}
	if st.AttemptsLeft != pinMaxAttempts {
		t.Fatalf("expected %d attempts left, got %d", pinMaxAttempts, st.AttemptsLeft)
	}
}

func TestStatus_AfterOneWrongAttempt(t *testing.T) {
	svc, _ := newTestPINService(t)
	svc.ValidatePIN("0000") //nolint:errcheck
	st := svc.Status()
	if st.Locked {
		t.Fatal("should not be locked after one wrong attempt")
	}
	if st.AttemptsLeft != pinMaxAttempts-1 {
		t.Fatalf("expected %d attempts left, got %d", pinMaxAttempts-1, st.AttemptsLeft)
	}
}

// ─── session token never leaves device (no network in tests) ─────────────────
// (structural: the service never makes outgoing HTTP calls; the test confirms
// that ValidatePIN returns the token to the caller — who decides what to do with it.)

func TestValidatePIN_ReturnsTokenForCallerToDecide(t *testing.T) {
	svc, store := newTestPINService(t)
	token, err := svc.ValidatePIN("1234")
	if err != nil {
		t.Fatal(err)
	}
	// The token is opaque to the PIN service; the caller validates it.
	sess, ok := store.ValidateToken(token)
	if !ok {
		t.Fatal("token not valid in store")
	}
	if sess.UserID == "" {
		t.Fatal("empty user ID in session")
	}
}

// ─── pin_state.json persists across service restarts ─────────────────────────

func TestState_PersistsAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	storeDir := t.TempDir()
	store, err := NewStore(storeDir)
	if err != nil {
		t.Fatal(err)
	}
	user, _ := store.Register("u2", "pw123456", "U2")
	sess := store.CreateSession(user, "d")

	svc1, _ := NewDevicePINService(dir, nil, 4, store)
	svc1.SetPIN("4321", sess.Token) //nolint:errcheck

	// Two wrong attempts.
	svc1.ValidatePIN("0000") //nolint:errcheck
	svc1.ValidatePIN("0000") //nolint:errcheck

	// Create a new service instance pointing to the same directory.
	svc2, _ := NewDevicePINService(dir, nil, 4, store)
	st := svc2.Status()
	if st.AttemptsLeft != pinMaxAttempts-2 {
		t.Fatalf("expected %d attempts left after restart, got %d", pinMaxAttempts-2, st.AttemptsLeft)
	}
}

// TestValidatePIN_TPMWrapped_NoKeyStore confirms that a pin.bin file whose
// tpm_wrapped=true flag is set cannot be decrypted when no KeyStore is
// available. This asserts the fail-closed behaviour of CLOGIN-06: a device
// that loses its TPM (or is cloned without the TPM) cannot unseal the
// wrapped credential.
func TestValidatePIN_TPMWrapped_NoKeyStore(t *testing.T) {
	dir := t.TempDir()
	storeDir := t.TempDir()
	store, err := NewStore(storeDir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	user, _ := store.Register("utpm", "pw123456", "Utpm")
	sess := store.CreateSession(user, "d")

	// Write a pin.bin with tpm_wrapped=true directly, simulating a file
	// that was originally sealed on a device with a hardware TPM.
	// The inner ciphertext is arbitrary — the point is that the service
	// must refuse to proceed when tpm_wrapped=true but ks==nil.
	svc, err := NewDevicePINService(dir, nil, 4, store)
	if err != nil {
		t.Fatalf("NewDevicePINService: %v", err)
	}

	// First set a PIN without TPM so the salt is created, then manually
	// overwrite pin.bin to set tpm_wrapped=true.
	if err := svc.SetPIN("1234", sess.Token); err != nil {
		t.Fatalf("SetPIN: %v", err)
	}

	// Read back the envelope, flip tpm_wrapped to true, re-write.
	data, err := os.ReadFile(svc.pinBinPath())
	if err != nil {
		t.Fatalf("read pin.bin: %v", err)
	}
	var env pinEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	env.TPMWrapped = true
	out, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal modified envelope: %v", err)
	}
	if err := os.WriteFile(svc.pinBinPath(), out, 0600); err != nil {
		t.Fatalf("write modified pin.bin: %v", err)
	}

	// ValidatePIN must fail because ks==nil but tpm_wrapped==true.
	_, gotErr := svc.ValidatePIN("1234")
	if gotErr == nil {
		t.Fatal("ValidatePIN: expected error for TPM-wrapped blob without KeyStore, got nil (fail-open)")
	}
}

// ─── internal helpers used only by tests ─────────────────────────────────────

func deriveWrapKeyForTest(pin string, salt []byte) []byte {
	return argon2.IDKey([]byte(pin), salt, pinArgon2Time, pinArgon2Mem, pinArgon2Threads, pinArgon2KeyLen)
}

func contains(haystack, needle []byte) bool {
	for i := 0; i <= len(haystack)-len(needle); i++ {
		match := true
		for j := 0; j < len(needle); j++ {
			if haystack[i+j] != needle[j] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

// keep the strconv import used in format tests
var _ = strconv.Itoa
