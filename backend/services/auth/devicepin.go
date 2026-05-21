// Package auth — devicepin.go
//
// CLOGIN-06: Device-local PIN login.
//
// Design:
//   - After a full-auth session, the user may set a 4–8-digit PIN (minimum
//     length is configurable; default 4).
//   - The PIN is used to wrap a session credential on the device. The wrap key
//     is derived from the PIN via argon2id (time=3, mem=64 MiB, threads=4) with
//     a per-device random salt. The credential is then sealed into a file under
//     ~/.vulos/auth/pin.bin using AES-256-GCM.
//   - Where a hardware TPM is available (via devicekey.KeyStore.Seal/Unseal), the
//     AES-256-GCM ciphertext blob is further wrapped by the TPM so that the sealed
//     credential is bound to this device's TPM state.
//   - Lockout: 5 consecutive wrong PINs → 15-minute temporary lockout. 3
//     lockouts in total → permanent lockout (requires full re-auth to reset).
//   - The PIN and any PIN-derived key material never leave the device. Setting or
//     changing the PIN requires that the current session was opened by full auth
//     (not a PIN session). This is enforced at the handler layer (see handlers.go).
//   - Session credential stored in pin.bin is a random 32-byte secret that maps
//     to an OS session token in a separate index file (pin_sessions.json).
//
// Terminology:
//   - "pin credential"   — the random 32-byte secret stored in pin.bin, used to
//     look up the OS session token. Never exposed over the API.
//   - "session token"    — the standard auth.Session token returned to the browser.
//
// File layout under dataDir (default ~/.vulos/auth/):
//
//	pin.bin            — AES-GCM(argon2id(PIN,salt)) wrapped session credential,
//	                     optionally TPM-re-sealed. JSON envelope.
//	pin_salt.bin       — 16-byte random argon2id salt (per-device, never rotated).
//	pin_state.json     — lockout counters + timestamps.
//	pin_sessions.json  — map[credential_hex]session_token (rotated on PIN change).

package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/crypto/argon2"

	"vulos/backend/services/devicekey"
)

// ─── constants ────────────────────────────────────────────────────────────────

const (
	// argon2id parameters — match the rest of the codebase (peering/identity.go).
	pinArgon2Time    uint32 = 3
	pinArgon2Mem     uint32 = 64 * 1024 // 64 MiB
	pinArgon2Threads uint8  = 4
	pinArgon2KeyLen  uint32 = 32

	// lockout thresholds
	pinMaxAttempts     = 5 // wrong PINs before temporary lockout
	pinLockoutDuration = 15 * time.Minute
	pinMaxLockouts     = 3 // lockout cycles before permanent lockout (requires full re-auth)

	// default minimum PIN length — overridable at DevicePINService construction time
	DefaultPINMinLength = 4
	// maximum PIN length is always 8
	PINMaxLength = 8
)

// ─── state types ──────────────────────────────────────────────────────────────

// pinState persists lockout counters. Written atomically to pin_state.json.
type pinState struct {
	Attempts      int       `json:"attempts"`         // wrong PINs since last reset
	LockoutCount  int       `json:"lockout_count"`    // total lockouts so far
	LockedUntil   time.Time `json:"locked_until"`     // zero if not locked
	PermanentLock bool      `json:"permanent_lock"`   // full re-auth required
	SetByFullAuth bool      `json:"set_by_full_auth"` // PIN was set during a full-auth session
}

// pinEnvelope is the on-disk format for pin.bin. It carries the argon2id
// parameters alongside the ciphertext so the format is self-describing.
type pinEnvelope struct {
	Version    int    `json:"v"`
	Salt       []byte `json:"salt"` // argon2id salt (16 bytes)
	Time       uint32 `json:"time"`
	Memory     uint32 `json:"mem"`
	Threads    uint8  `json:"threads"`
	KeyLen     uint32 `json:"keylen"`
	Ciphertext []byte `json:"ct"`          // AES-GCM ciphertext of the 32-byte credential
	TPMWrapped bool   `json:"tpm_wrapped"` // true → ciphertext was further sealed by TPM
}

// ─── DevicePINService ─────────────────────────────────────────────────────────

// DevicePINService manages device-local PIN login for CLOGIN-06.
// All exported methods are safe for concurrent use.
type DevicePINService struct {
	mu        sync.Mutex
	dataDir   string             // e.g. ~/.vulos/auth
	ks        devicekey.KeyStore // may be nil → software-only fallback
	minLength int
	authStore *Store
}

// NewDevicePINService creates a DevicePINService.
//
//   - dataDir is the directory where pin.bin and related files are stored
//     (default: ~/.vulos/auth/).
//   - ks is an optional devicekey.KeyStore; pass nil to use the software-only
//     AES-GCM path (no additional TPM wrapping).
//   - minLength is the minimum PIN digit count (clamped to [4, 8]).
//   - store is the auth.Store used to issue/validate OS session tokens.
func NewDevicePINService(dataDir string, ks devicekey.KeyStore, minLength int, store *Store) (*DevicePINService, error) {
	if dataDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("devicepin: cannot determine home dir: %w", err)
		}
		dataDir = filepath.Join(home, ".vulos", "auth")
	}
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return nil, fmt.Errorf("devicepin: mkdir %s: %w", dataDir, err)
	}
	if minLength < DefaultPINMinLength {
		minLength = DefaultPINMinLength
	}
	if minLength > PINMaxLength {
		minLength = PINMaxLength
	}
	return &DevicePINService{
		dataDir:   dataDir,
		ks:        ks,
		minLength: minLength,
		authStore: store,
	}, nil
}

// ─── public API ───────────────────────────────────────────────────────────────

// ErrPINLocked is returned when the device is in temporary or permanent lockout.
var (
	ErrPINLocked          = errors.New("devicepin: too many wrong PINs — locked out")
	ErrPINPermanentLocked = errors.New("devicepin: PIN permanently locked — full re-auth required")
	ErrPINNotSet          = errors.New("devicepin: no PIN set on this device")
	ErrPINWrong           = errors.New("devicepin: wrong PIN")
	ErrPINTooShort        = errors.New("devicepin: PIN too short")
	ErrPINTooLong         = errors.New("devicepin: PIN too long")
	ErrPINNotDigits       = errors.New("devicepin: PIN must be digits only")
)

// SetPIN sets (or replaces) the device PIN, wrapping the given sessionToken.
// The caller must ensure this is only called from a full-auth session.
// Passing an empty pin clears the PIN and removes all PIN-derived state.
func (s *DevicePINService) SetPIN(pin, sessionToken string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if pin == "" {
		return s.clearPIN()
	}

	if err := s.validatePINFormat(pin); err != nil {
		return err
	}

	// 1. Generate (or load) the per-device salt.
	salt, err := s.loadOrCreateSalt()
	if err != nil {
		return fmt.Errorf("devicepin: salt: %w", err)
	}

	// 2. Derive wrap key from PIN + salt using argon2id.
	wrapKey := argon2.IDKey([]byte(pin), salt, pinArgon2Time, pinArgon2Mem, pinArgon2Threads, pinArgon2KeyLen)

	// 3. Generate a random 32-byte credential that maps to the session token.
	cred := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, cred); err != nil {
		return fmt.Errorf("devicepin: generate credential: %w", err)
	}

	// 4. Encrypt the credential with the PIN-derived wrap key (AES-256-GCM).
	ct, err := aesGCMEncrypt(wrapKey, cred)
	if err != nil {
		return fmt.Errorf("devicepin: encrypt credential: %w", err)
	}

	tpmWrapped := false
	// 5. Optionally re-seal the ciphertext with the TPM.
	if s.ks != nil {
		if tpmCT, err2 := s.ks.Seal(ct); err2 == nil {
			ct = tpmCT
			tpmWrapped = true
		} else {
			log.Printf("[devicepin] TPM seal unavailable, using software fallback: %v", err2)
		}
	}

	// 6. Write pin.bin.
	env := pinEnvelope{
		Version:    1,
		Salt:       salt,
		Time:       pinArgon2Time,
		Memory:     pinArgon2Mem,
		Threads:    pinArgon2Threads,
		KeyLen:     pinArgon2KeyLen,
		Ciphertext: ct,
		TPMWrapped: tpmWrapped,
	}
	envBytes, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("devicepin: marshal envelope: %w", err)
	}
	if err := atomicWrite(s.pinBinPath(), envBytes, 0600); err != nil {
		return fmt.Errorf("devicepin: write pin.bin: %w", err)
	}

	// 7. Store credential → session token mapping.
	if err := s.writeSessionMap(hex.EncodeToString(cred), sessionToken); err != nil {
		return fmt.Errorf("devicepin: write session map: %w", err)
	}

	// 8. Reset lockout state.
	if err := s.writeState(pinState{SetByFullAuth: true}); err != nil {
		return fmt.Errorf("devicepin: reset state: %w", err)
	}

	log.Printf("[devicepin] PIN set (tpm_wrapped=%v)", tpmWrapped)
	return nil
}

// HasPIN reports whether a PIN is currently set on this device.
func (s *DevicePINService) HasPIN() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := os.Stat(s.pinBinPath())
	return err == nil
}

// ValidatePIN attempts to unlock using the given PIN. On success it returns the
// OS session token that was wrapped at SetPIN time. The caller should validate
// the returned token with Store.ValidateToken before issuing cookies.
//
// Lockout rules:
//   - 5 consecutive wrong PINs → 15-minute lockout → ErrPINLocked
//   - 3 lockouts → permanent lockout → ErrPINPermanentLocked
func (s *DevicePINService) ValidatePIN(pin string) (sessionToken string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.hasPINUnsafe() {
		return "", ErrPINNotSet
	}

	st, err := s.readState()
	if err != nil {
		// Fail closed: if we can't read state, refuse.
		return "", fmt.Errorf("devicepin: read state: %w", err)
	}

	// Check permanent lock.
	if st.PermanentLock {
		return "", ErrPINPermanentLocked
	}

	// Check temporary lockout.
	if !st.LockedUntil.IsZero() && time.Now().Before(st.LockedUntil) {
		return "", ErrPINLocked
	}
	// Lockout expired — reset attempt counter (lockout count persists).
	if !st.LockedUntil.IsZero() && time.Now().After(st.LockedUntil) {
		st.LockedUntil = time.Time{}
		st.Attempts = 0
	}

	// Attempt to decrypt.
	token, err2 := s.tryDecrypt(pin)
	if err2 != nil {
		// Wrong PIN — increment counter.
		st.Attempts++
		if st.Attempts >= pinMaxAttempts {
			st.LockoutCount++
			st.Attempts = 0
			if st.LockoutCount >= pinMaxLockouts {
				st.PermanentLock = true
				_ = s.writeState(st)
				return "", ErrPINPermanentLocked
			}
			st.LockedUntil = time.Now().Add(pinLockoutDuration)
			_ = s.writeState(st)
			return "", ErrPINLocked
		}
		_ = s.writeState(st)
		return "", ErrPINWrong
	}

	// Success — reset attempt counter.
	st.Attempts = 0
	st.LockedUntil = time.Time{}
	_ = s.writeState(st)

	return token, nil
}

// ResetLockout clears all lockout state (including permanent lock). Must only
// be called after the user completes a full re-auth (password + 2FA). The PIN
// itself is NOT removed — only the lockout counters are reset.
func (s *DevicePINService) ResetLockout() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writeState(pinState{})
}

// LockoutStatus returns a human-readable status for the UI.
type LockoutStatus struct {
	Locked        bool      `json:"locked"`
	PermanentLock bool      `json:"permanent_lock"`
	AttemptsLeft  int       `json:"attempts_left"`
	LockedUntil   time.Time `json:"locked_until,omitempty"`
	LockoutCount  int       `json:"lockout_count"`
}

// Status returns the current lockout status without modifying state.
func (s *DevicePINService) Status() LockoutStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.readState()
	if err != nil {
		return LockoutStatus{Locked: true, PermanentLock: true}
	}
	if st.PermanentLock {
		return LockoutStatus{Locked: true, PermanentLock: true, LockoutCount: st.LockoutCount}
	}
	if !st.LockedUntil.IsZero() && time.Now().Before(st.LockedUntil) {
		return LockoutStatus{
			Locked:       true,
			LockedUntil:  st.LockedUntil,
			AttemptsLeft: 0,
			LockoutCount: st.LockoutCount,
		}
	}
	remaining := pinMaxAttempts - st.Attempts
	if remaining < 0 {
		remaining = 0
	}
	return LockoutStatus{
		Locked:       false,
		AttemptsLeft: remaining,
		LockoutCount: st.LockoutCount,
	}
}

// ─── internal helpers ─────────────────────────────────────────────────────────

func (s *DevicePINService) pinBinPath() string   { return filepath.Join(s.dataDir, "pin.bin") }
func (s *DevicePINService) pinSaltPath() string  { return filepath.Join(s.dataDir, "pin_salt.bin") }
func (s *DevicePINService) pinStatePath() string { return filepath.Join(s.dataDir, "pin_state.json") }
func (s *DevicePINService) pinSessionsPath() string {
	return filepath.Join(s.dataDir, "pin_sessions.json")
}

func (s *DevicePINService) hasPINUnsafe() bool {
	_, err := os.Stat(s.pinBinPath())
	return err == nil
}

func (s *DevicePINService) validatePINFormat(pin string) error {
	if len(pin) < s.minLength {
		return fmt.Errorf("%w (min %d digits)", ErrPINTooShort, s.minLength)
	}
	if len(pin) > PINMaxLength {
		return ErrPINTooLong
	}
	for _, c := range pin {
		if c < '0' || c > '9' {
			return ErrPINNotDigits
		}
	}
	return nil
}

// clearPIN removes all PIN-related files.
func (s *DevicePINService) clearPIN() error {
	for _, p := range []string{
		s.pinBinPath(),
		s.pinStatePath(),
		s.pinSessionsPath(),
	} {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("devicepin: clear %s: %w", filepath.Base(p), err)
		}
	}
	// salt is kept — it is per-device, not per-PIN.
	log.Printf("[devicepin] PIN cleared")
	return nil
}

// loadOrCreateSalt returns the per-device argon2id salt, generating and
// persisting a new 16-byte random salt if it does not yet exist.
func (s *DevicePINService) loadOrCreateSalt() ([]byte, error) {
	path := s.pinSaltPath()
	if data, err := os.ReadFile(path); err == nil && len(data) == 16 {
		return data, nil
	}
	salt := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return nil, err
	}
	if err := atomicWrite(path, salt, 0600); err != nil {
		return nil, err
	}
	return salt, nil
}

// tryDecrypt attempts to unseal the credential using the given PIN.
// Returns the session token on success, error on failure.
func (s *DevicePINService) tryDecrypt(pin string) (string, error) {
	envBytes, err := os.ReadFile(s.pinBinPath())
	if err != nil {
		return "", fmt.Errorf("read pin.bin: %w", err)
	}
	var env pinEnvelope
	if err := json.Unmarshal(envBytes, &env); err != nil {
		return "", fmt.Errorf("parse pin.bin: %w", err)
	}

	ct := env.Ciphertext

	// 1. If TPM-wrapped, unseal with TPM first.
	if env.TPMWrapped {
		if s.ks == nil {
			return "", fmt.Errorf("devicepin: pin.bin is TPM-wrapped but no KeyStore available")
		}
		ct, err = s.ks.Unseal(ct)
		if err != nil {
			return "", fmt.Errorf("devicepin: TPM unseal: %w", err)
		}
	}

	// 2. Derive wrap key from PIN using stored argon2id parameters.
	wrapKey := argon2.IDKey([]byte(pin), env.Salt, env.Time, env.Memory, env.Threads, env.KeyLen)

	// 3. Decrypt the credential.
	cred, err := aesGCMDecrypt(wrapKey, ct)
	if err != nil {
		return "", ErrPINWrong
	}

	// 4. Look up the session token.
	token, err := s.lookupSessionToken(hex.EncodeToString(cred))
	if err != nil {
		return "", fmt.Errorf("devicepin: session lookup: %w", err)
	}
	return token, nil
}

// pinSessionMap is the in-memory/on-disk structure mapping credential → token.
type pinSessionMap map[string]string // credential_hex → session_token

func (s *DevicePINService) readSessionMap() (pinSessionMap, error) {
	data, err := os.ReadFile(s.pinSessionsPath())
	if os.IsNotExist(err) {
		return pinSessionMap{}, nil
	}
	if err != nil {
		return nil, err
	}
	var m pinSessionMap
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return m, nil
}

func (s *DevicePINService) writeSessionMap(credHex, token string) error {
	// Replace the entire map — each SetPIN creates a fresh credential.
	m := pinSessionMap{credHex: token}
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return atomicWrite(s.pinSessionsPath(), data, 0600)
}

func (s *DevicePINService) lookupSessionToken(credHex string) (string, error) {
	m, err := s.readSessionMap()
	if err != nil {
		return "", err
	}
	token, ok := m[credHex]
	if !ok {
		return "", fmt.Errorf("credential not found in session map")
	}
	return token, nil
}

func (s *DevicePINService) readState() (pinState, error) {
	data, err := os.ReadFile(s.pinStatePath())
	if os.IsNotExist(err) {
		return pinState{}, nil
	}
	if err != nil {
		return pinState{}, err
	}
	var st pinState
	if err := json.Unmarshal(data, &st); err != nil {
		return pinState{}, err
	}
	return st, nil
}

func (s *DevicePINService) writeState(st pinState) error {
	data, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return atomicWrite(s.pinStatePath(), data, 0600)
}

// ─── local AES-256-GCM + atomic-write helpers ─────────────────────────────────
// (mirrors the same helpers in services/devicekey; kept local to avoid package
// coupling — this is intentionally a small duplication.)

// pinGCMEnvelope is the wire format used by the PIN service's own AES-GCM layer.
type pinGCMEnvelope struct {
	Version    int    `json:"v"`
	Nonce      []byte `json:"n"`
	Ciphertext []byte `json:"c"`
}

func aesGCMEncrypt(key, plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	ct := gcm.Seal(nil, nonce, plaintext, nil)
	env := pinGCMEnvelope{Version: 1, Nonce: nonce, Ciphertext: ct}
	return json.Marshal(env)
}

func aesGCMDecrypt(key, data []byte) ([]byte, error) {
	var env pinGCMEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, fmt.Errorf("bad gcm envelope: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return gcm.Open(nil, env.Nonce, env.Ciphertext, nil)
}

// atomicWrite writes data to path via a temp file + rename for crash-safety.
func atomicWrite(path string, data []byte, perm os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return err
	}
	if err := os.Chmod(tmp, perm); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}
