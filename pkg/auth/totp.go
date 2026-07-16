// totp.go -- TOTP 2FA helpers for vulos.cloud control-plane.
//
// Implements RFC 6238 TOTP (SHA-1, 6-digit, 30 s window) via
// github.com/pquerna/otp/totp.
//
// Secret encryption: AES-256-GCM keyed by AUTH_TOTP_KEK (base64-encoded 32 B).
// Recovery codes: 10 single-use 8-char alphanumeric codes (ambiguity-safe),
// hashed with argon2id at storage time.
//
// Replay protection: VerifyTOTPAndRecord uses an in-process LRU-style cache to
// prevent a valid TOTP code from being used more than once within the ±1 step
// (90 s) window allowed by pquerna/otp's default Validate().  The cache key is
// SHA-256(userID + code) so codes from different accounts don't collide.
package auth

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/pquerna/otp/totp"
	"golang.org/x/crypto/argon2"

	"github.com/vul-os/vulos-management/pkg/env"
)

// ──────────────────────────────────────────────────────────────────────────────
// TOTP replay cache (in-process, single-node)
// ──────────────────────────────────────────────────────────────────────────────

// totpReplayTTL is how long a used code is remembered. Must be > 90 s to
// cover the full ±1-step window that pquerna/otp allows.
const totpReplayTTL = 120 * time.Second

type totpUsedEntry struct {
	usedAt time.Time
}

var totpReplayMu sync.Mutex
var totpReplayCache = make(map[string]totpUsedEntry)

// totpReplayCacheKey returns a key for (userID, code).
func totpReplayCacheKey(userID, code string) string {
	h := sha256.Sum256([]byte(userID + "\x00" + code))
	return hex.EncodeToString(h[:])
}

// recordTOTPUse records that (userID, code) has been consumed. Returns false if
// the code has already been used within the replay window.
func recordTOTPUse(userID, code string) bool {
	key := totpReplayCacheKey(userID, code)
	now := time.Now()

	totpReplayMu.Lock()
	defer totpReplayMu.Unlock()

	// GC: evict expired entries on every write.
	for k, v := range totpReplayCache {
		if now.Sub(v.usedAt) > totpReplayTTL {
			delete(totpReplayCache, k)
		}
	}

	if e, found := totpReplayCache[key]; found {
		if now.Sub(e.usedAt) < totpReplayTTL {
			return false // replay detected
		}
	}
	totpReplayCache[key] = totpUsedEntry{usedAt: now}
	return true
}

// ──────────────────────────────────────────────────────────────────────────────
// Sentinel errors
// ──────────────────────────────────────────────────────────────────────────────

var (
	// ErrTOTPInvalid is returned when a TOTP code or recovery code is wrong.
	ErrTOTPInvalid = errors.New("auth: invalid TOTP code")
	// ErrTOTPAlreadyEnabled is returned when the user already has TOTP enabled.
	ErrTOTPAlreadyEnabled = errors.New("auth: TOTP already enabled")
	// ErrTOTPNotEnabled is returned when an operation requires TOTP but it is not enabled.
	ErrTOTPNotEnabled = errors.New("auth: TOTP not enabled")
	// ErrFleetAdminCannotDisableTOTP is returned when a fleet_admin tries to disable TOTP.
	ErrFleetAdminCannotDisableTOTP = errors.New("auth: fleet_admin cannot disable TOTP")
	// ErrNoPendingEnrollment is returned when /totp/confirm is called without a prior enroll.
	ErrNoPendingEnrollment = errors.New("auth: no pending TOTP enrollment")
)

// ──────────────────────────────────────────────────────────────────────────────
// KEK (Key Encryption Key) loading
// ──────────────────────────────────────────────────────────────────────────────

// devFallbackKEK is a 32-byte all-zeros key used only when VULOS_DEV=true and
// AUTH_TOTP_KEK is not set.  It MUST NOT be used in production.
var devFallbackKEK = make([]byte, 32)

// LoadTOTPKEK returns the 32-byte AES key from AUTH_TOTP_KEK (base64).
// If the env is unset and VULOS_DEV=true, it returns the dev fallback key with
// a warning.  In any other case it returns an error.
func LoadTOTPKEK() ([]byte, error) {
	raw := os.Getenv("AUTH_TOTP_KEK")
	if raw == "" {
		// WAVE34: dev fallback requires VULOS_DEV truthy AND env!=prod (cross-check).
		if env.DevKEKFallbackAllowed() {
			return devFallbackKEK, nil
		}
		return nil, fmt.Errorf("auth: AUTH_TOTP_KEK must be set (base64-encoded 32-byte key)")
	}
	key, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return nil, fmt.Errorf("auth: AUTH_TOTP_KEK is not valid base64: %w", err)
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("auth: AUTH_TOTP_KEK must decode to exactly 32 bytes, got %d", len(key))
	}
	return key, nil
}

// ──────────────────────────────────────────────────────────────────────────────
// AES-GCM encryption / decryption
// ──────────────────────────────────────────────────────────────────────────────

// EncryptTOTPSecret encrypts a plaintext TOTP secret with AES-256-GCM using kek.
// Returns nonce||ciphertext, base64-encoded.
func EncryptTOTPSecret(plain, kek []byte) ([]byte, error) {
	block, err := aes.NewCipher(kek)
	if err != nil {
		return nil, fmt.Errorf("auth: aes new cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("auth: gcm: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("auth: rand nonce: %w", err)
	}
	ct := gcm.Seal(nonce, nonce, plain, nil) // nonce||ciphertext||tag
	dst := make([]byte, base64.StdEncoding.EncodedLen(len(ct)))
	base64.StdEncoding.Encode(dst, ct)
	return dst, nil
}

// DecryptTOTPSecret decodes and decrypts a value previously produced by
// EncryptTOTPSecret.
func DecryptTOTPSecret(enc, kek []byte) ([]byte, error) {
	raw := make([]byte, base64.StdEncoding.DecodedLen(len(enc)))
	n, err := base64.StdEncoding.Decode(raw, enc)
	if err != nil {
		return nil, fmt.Errorf("auth: base64 decode: %w", err)
	}
	raw = raw[:n]

	block, err := aes.NewCipher(kek)
	if err != nil {
		return nil, fmt.Errorf("auth: aes new cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("auth: gcm: %w", err)
	}
	ns := gcm.NonceSize()
	if len(raw) < ns {
		return nil, fmt.Errorf("auth: ciphertext too short")
	}
	plain, err := gcm.Open(nil, raw[:ns], raw[ns:], nil)
	if err != nil {
		return nil, fmt.Errorf("auth: gcm open: %w", err)
	}
	return plain, nil
}

// ──────────────────────────────────────────────────────────────────────────────
// TOTP generation and validation
// ──────────────────────────────────────────────────────────────────────────────

// GenerateTOTPSecret creates a new TOTP key for email, returns the otpauth://
// provisioning URI and the base32-encoded secret.  Issuer is always "Vulos".
func GenerateTOTPSecret(email string) (uri, secretB32 string, err error) {
	key, err := totp.Generate(totp.GenerateOpts{
		Issuer:      "Vulos",
		AccountName: email,
		// Defaults: SHA-1, 6 digits, 30-second period (RFC 6238 standard).
	})
	if err != nil {
		return "", "", fmt.Errorf("auth: generate totp key: %w", err)
	}
	return key.URL(), key.Secret(), nil
}

// VerifyTOTP validates code against the plaintext base32 secret.
// It uses the pquerna/otp default window (±1 step) for clock skew tolerance.
// NOTE: this function does NOT enforce replay protection. Use
// VerifyTOTPAndRecord when processing an actual login/reset step.
func VerifyTOTP(secretPlain []byte, code string) bool {
	return totp.Validate(code, string(secretPlain))
}

// VerifyTOTPAndRecord validates code and records it in the replay cache.
// Returns false if the code is cryptographically invalid OR if it has already
// been presented within the ±1-step replay window (≈90 s).  userID is used
// as part of the cache key so one account's codes don't block another's.
func VerifyTOTPAndRecord(secretPlain []byte, code, userID string) bool {
	if !totp.Validate(code, string(secretPlain)) {
		return false
	}
	// Reject replay: same code for same user within replay TTL.
	return recordTOTPUse(userID, code)
}

// ──────────────────────────────────────────────────────────────────────────────
// Recovery codes
// ──────────────────────────────────────────────────────────────────────────────

// recoveryCodeAlphabet excludes visually ambiguous characters (0 O I 1 l).
const recoveryCodeAlphabet = "23456789ABCDEFGHJKMNPQRSTVWXYZ"

const recoveryCodeLen = 8
const recoveryCodeCount = 10

// generateRecoveryCode generates a single random 8-char recovery code.
func generateRecoveryCode() (string, error) {
	b := make([]byte, recoveryCodeLen)
	n := big.NewInt(int64(len(recoveryCodeAlphabet)))
	for i := range b {
		idx, err := rand.Int(rand.Reader, n)
		if err != nil {
			return "", err
		}
		b[i] = recoveryCodeAlphabet[idx.Int64()]
	}
	return string(b), nil
}

// HashRecoveryCode returns an argon2id hash of code suitable for storage.
// We use reduced parameters (time=1, mem=64KiB, threads=1) because recovery
// codes are already high-entropy random strings; the hash is for at-rest
// protection, not against dictionary attacks.
func HashRecoveryCode(code string) ([]byte, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	// Parameters: lower than password (already high entropy), but still argon2id.
	h := argon2.IDKey([]byte(code), salt, 1, 64*1024, 1, 32)
	// Store as base64(salt)||"$"||base64(hash) so we can re-derive later.
	return []byte(base64.RawStdEncoding.EncodeToString(salt) + "$" + base64.RawStdEncoding.EncodeToString(h)), nil
}

// verifyRecoveryCode checks code against a stored hash produced by hashRecoveryCode.
// Uses constant-time comparison.
func verifyRecoveryCode(stored []byte, code string) bool {
	parts := strings.SplitN(string(stored), "$", 2)
	if len(parts) != 2 {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[0])
	if err != nil {
		return false
	}
	expected, err := base64.RawStdEncoding.DecodeString(parts[1])
	if err != nil {
		return false
	}
	actual := argon2.IDKey([]byte(code), salt, 1, 64*1024, 1, 32)
	return subtle.ConstantTimeCompare(actual, expected) == 1
}

// GenerateRecoveryCodes returns 10 plaintext recovery codes and their
// corresponding argon2id hashes ready for DB storage.
func GenerateRecoveryCodes() ([]string, [][]byte, error) {
	codes := make([]string, recoveryCodeCount)
	hashes := make([][]byte, recoveryCodeCount)
	for i := range codes {
		c, err := generateRecoveryCode()
		if err != nil {
			return nil, nil, fmt.Errorf("auth: generate recovery code: %w", err)
		}
		h, err := HashRecoveryCode(c)
		if err != nil {
			return nil, nil, fmt.Errorf("auth: hash recovery code: %w", err)
		}
		codes[i] = c
		hashes[i] = h
	}
	return codes, hashes, nil
}

// ──────────────────────────────────────────────────────────────────────────────
// Store methods — TOTP
// ──────────────────────────────────────────────────────────────────────────────

// IsFleetAdmin returns true if the user identified by userID has fleet_admin=1.
func (s *Store) IsFleetAdmin(ctx context.Context, userID string) bool {
	var fa int
	err := s.db.QueryRowContext(ctx, s.db.Rebind(`SELECT fleet_admin FROM users WHERE id = ?`), userID).Scan(&fa)
	if err != nil {
		return false
	}
	return fa != 0
}

// SaveTOTPSecret stores the AES-GCM encrypted TOTP secret for userID.
func (s *Store) SaveTOTPSecret(ctx context.Context, userID string, encSecret []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx,
		s.db.Rebind(`UPDATE users SET totp_secret_enc = ?, updated_at = ? WHERE id = ?`),
		string(encSecret), now().Format(time.RFC3339), userID,
	)
	if err != nil {
		return fmt.Errorf("auth: save totp secret: %w", err)
	}
	return nil
}

// EnableTOTP sets totp_enabled=1 for userID.
func (s *Store) EnableTOTP(ctx context.Context, userID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx,
		s.db.Rebind(`UPDATE users SET totp_enabled = 1, updated_at = ? WHERE id = ?`),
		now().Format(time.RFC3339), userID,
	)
	if err != nil {
		return fmt.Errorf("auth: enable totp: %w", err)
	}
	return nil
}

// DisableTOTP clears totp_enabled and totp_secret_enc for userID.
// It also drops all unused recovery codes for that user.
func (s *Store) DisableTOTP(ctx context.Context, userID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx,
		s.db.Rebind(`UPDATE users SET totp_enabled = 0, totp_secret_enc = NULL, updated_at = ? WHERE id = ?`),
		now().Format(time.RFC3339), userID,
	)
	if err != nil {
		return fmt.Errorf("auth: disable totp: %w", err)
	}
	_, err = s.db.ExecContext(ctx,
		s.db.Rebind(`DELETE FROM totp_recovery_codes WHERE user_id = ?`), userID,
	)
	if err != nil {
		return fmt.Errorf("auth: drop recovery codes: %w", err)
	}
	return nil
}

// LoadTOTPSecret returns the encrypted TOTP secret and whether TOTP is enabled.
// encSecret is nil if no secret has been stored yet.
func (s *Store) LoadTOTPSecret(ctx context.Context, userID string) (encSecret []byte, enabled bool, err error) {
	var encRaw *string
	var en int
	err = s.db.QueryRowContext(ctx,
		s.db.Rebind(`SELECT totp_secret_enc, totp_enabled FROM users WHERE id = ?`), userID,
	).Scan(&encRaw, &en)
	if err != nil {
		return nil, false, fmt.Errorf("auth: load totp secret: %w", err)
	}
	if encRaw != nil {
		encSecret = []byte(*encRaw)
	}
	return encSecret, en != 0, nil
}

// InsertRecoveryCodes stores the given argon2id hashes as single-use recovery
// codes for userID.  Existing codes are deleted first so re-enrollment is clean.
func (s *Store) InsertRecoveryCodes(ctx context.Context, userID string, hashes [][]byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, err := s.db.ExecContext(ctx,
		s.db.Rebind(`DELETE FROM totp_recovery_codes WHERE user_id = ?`), userID,
	); err != nil {
		return fmt.Errorf("auth: clear old recovery codes: %w", err)
	}

	ts := now().Format(time.RFC3339)
	for _, h := range hashes {
		id, err := newULID()
		if err != nil {
			return err
		}
		if _, err := s.db.ExecContext(ctx,
			s.db.Rebind(`INSERT INTO totp_recovery_codes (id, user_id, code_hash, used, created_at)
			 VALUES (?, ?, ?, 0, ?)`),
			id, userID, string(h), ts,
		); err != nil {
			return fmt.Errorf("auth: insert recovery code: %w", err)
		}
	}
	return nil
}

// BurnRecoveryCode looks up all unused recovery codes for userID, verifies
// code via constant-time argon2 compare, and deletes the first matching row.
// Returns true if a code was found and burned; false if no match.
func (s *Store) BurnRecoveryCode(ctx context.Context, userID, code string) (bool, error) {
	rows, err := s.db.QueryContext(ctx,
		s.db.Rebind(`SELECT id, code_hash FROM totp_recovery_codes WHERE user_id = ? AND used = 0`), userID,
	)
	if err != nil {
		return false, fmt.Errorf("auth: query recovery codes: %w", err)
	}
	defer rows.Close()

	type row struct {
		id   string
		hash []byte
	}
	var candidates []row
	for rows.Next() {
		var r row
		var h string
		if err := rows.Scan(&r.id, &h); err != nil {
			return false, fmt.Errorf("auth: scan recovery code: %w", err)
		}
		r.hash = []byte(h)
		candidates = append(candidates, r)
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("auth: recovery code rows: %w", err)
	}

	// Iterate all candidates to avoid timing oracle (constant-time per candidate).
	var matchID string
	for _, c := range candidates {
		if verifyRecoveryCode(c.hash, code) {
			matchID = c.id
			// Do NOT break: verify all to avoid early-exit timing leak.
		}
	}
	if matchID == "" {
		return false, nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	// Hard-delete the matched row so it cannot be reused.
	if _, err := s.db.ExecContext(ctx,
		s.db.Rebind(`DELETE FROM totp_recovery_codes WHERE id = ?`), matchID,
	); err != nil {
		return false, fmt.Errorf("auth: burn recovery code: %w", err)
	}
	return true, nil
}

// ──────────────────────────────────────────────────────────────────────────────
// Store methods — pending enrollment staging
// ──────────────────────────────────────────────────────────────────────────────

// SavePendingTOTPSecret stores the plaintext base32 secret for userID.
// It overwrites any existing pending secret (preserves rec_codes if set).
func (s *Store) SavePendingTOTPSecret(ctx context.Context, userID, secretB32 string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ts := now().Format(time.RFC3339)
	_, err := s.db.ExecContext(ctx,
		s.db.Rebind(`INSERT INTO pending_totp_secrets (user_id, secret, rec_codes, created_at)
		 VALUES (?, ?, NULL, ?)
		 ON CONFLICT(user_id) DO UPDATE SET secret=excluded.secret, created_at=excluded.created_at`),
		userID, secretB32, ts,
	)
	if err != nil {
		return fmt.Errorf("auth: save pending totp secret: %w", err)
	}
	return nil
}

// SavePendingRecoveryCodes stores plaintext recovery codes alongside the
// pending TOTP secret so /confirm can hash and persist them.
func (s *Store) SavePendingRecoveryCodes(ctx context.Context, userID string, codes []string) error {
	data, err := json.Marshal(codes)
	if err != nil {
		return fmt.Errorf("auth: marshal recovery codes: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err = s.db.ExecContext(ctx,
		s.db.Rebind(`UPDATE pending_totp_secrets SET rec_codes = ? WHERE user_id = ?`),
		string(data), userID,
	)
	if err != nil {
		return fmt.Errorf("auth: save pending recovery codes: %w", err)
	}
	return nil
}

// LoadPendingRecoveryCodes returns the staged plaintext recovery codes for userID.
// Returns nil if no codes are staged (caller should generate fresh ones).
func (s *Store) LoadPendingRecoveryCodes(ctx context.Context, userID string) ([]string, error) {
	var raw *string
	err := s.db.QueryRowContext(ctx,
		s.db.Rebind(`SELECT rec_codes FROM pending_totp_secrets WHERE user_id = ?`), userID,
	).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) || raw == nil {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("auth: load pending recovery codes: %w", err)
	}
	var codes []string
	if err := json.Unmarshal([]byte(*raw), &codes); err != nil {
		return nil, fmt.Errorf("auth: unmarshal recovery codes: %w", err)
	}
	return codes, nil
}

// DeletePendingRecoveryCodes clears staged recovery codes (called after /confirm).
func (s *Store) DeletePendingRecoveryCodes(ctx context.Context, userID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx,
		s.db.Rebind(`UPDATE pending_totp_secrets SET rec_codes = NULL WHERE user_id = ?`), userID,
	)
	return err
}

// LoadPendingTOTPSecret returns the staged plaintext TOTP secret for userID, or
// ErrNoPendingEnrollment if none exists (or it's >10 min old).
func (s *Store) LoadPendingTOTPSecret(ctx context.Context, userID string) (string, error) {
	var secret, createdAt string
	err := s.db.QueryRowContext(ctx,
		s.db.Rebind(`SELECT secret, created_at FROM pending_totp_secrets WHERE user_id = ?`), userID,
	).Scan(&secret, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNoPendingEnrollment
	}
	if err != nil {
		return "", fmt.Errorf("auth: load pending totp secret: %w", err)
	}
	// Expire stale pending enrollments after 10 minutes.
	if ts, err := time.Parse(time.RFC3339, createdAt); err == nil {
		if now().Sub(ts) > 10*time.Minute {
			_ = s.deletePendingTOTPSecret(ctx, userID)
			return "", ErrNoPendingEnrollment
		}
	}
	return secret, nil
}

// deletePendingTOTPSecret removes the staged secret after confirm or expiry.
func (s *Store) deletePendingTOTPSecret(ctx context.Context, userID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx, s.db.Rebind(`DELETE FROM pending_totp_secrets WHERE user_id = ?`), userID)
	return err
}

// DeletePendingTOTPSecret is the exported form used by the route layer on success.
func (s *Store) DeletePendingTOTPSecret(ctx context.Context, userID string) error {
	return s.deletePendingTOTPSecret(ctx, userID)
}

// ──────────────────────────────────────────────────────────────────────────────
// Store methods — partial sessions
// ──────────────────────────────────────────────────────────────────────────────

const partialSessionDuration = 5 * time.Minute

// CreatePartialSession issues a short-lived session cookie with partial=1 for
// use during the TOTP verification step.  The cookie TTL matches the session TTL.
func (s *Store) CreatePartialSession(ctx context.Context, userID, ip, ua string) (string, error) {
	token, err := newSessionToken()
	if err != nil {
		return "", err
	}
	n := now()
	expires := n.Add(partialSessionDuration)
	ts := n.Format(time.RFC3339)

	s.mu.Lock()
	_, err = s.db.ExecContext(ctx,
		s.db.Rebind(`INSERT INTO sessions (id, user_id, created_at, last_seen_at, expires_at, ip, user_agent, revoked, partial)
		 VALUES (?, ?, ?, ?, ?, ?, ?, 0, 1)`),
		token, userID, ts, ts, expires.Format(time.RFC3339), ip, ua,
	)
	s.mu.Unlock()
	if err != nil {
		return "", fmt.Errorf("auth: create partial session: %w", err)
	}
	return token, nil
}

// UpgradePartialSession replaces the partial session with a full one.
// The old partial token is deleted; a new full-session token is returned.
func (s *Store) UpgradePartialSession(ctx context.Context, partialToken, ip, ua string) (string, error) {
	// Look up the partial session to get the userID.
	var userID string
	var expiresAt string
	var partial, revoked int
	err := s.db.QueryRowContext(ctx,
		s.db.Rebind(`SELECT user_id, expires_at, partial, revoked FROM sessions WHERE id = ?`), partialToken,
	).Scan(&userID, &expiresAt, &partial, &revoked)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrSessionNotFound
	}
	if err != nil {
		return "", fmt.Errorf("auth: upgrade partial session lookup: %w", err)
	}
	if revoked != 0 || partial == 0 {
		return "", ErrSessionNotFound
	}
	exp, err := time.Parse(time.RFC3339, expiresAt)
	if err != nil || now().After(exp) {
		return "", ErrSessionNotFound
	}

	// Delete the partial session.
	s.mu.Lock()
	_, err = s.db.ExecContext(ctx, s.db.Rebind(`DELETE FROM sessions WHERE id = ?`), partialToken)
	s.mu.Unlock()
	if err != nil {
		return "", fmt.Errorf("auth: delete partial session: %w", err)
	}

	// Create full session.
	fullToken, err := s.createSession(ctx, userID, ip, ua)
	if err != nil {
		return "", err
	}
	return fullToken, nil
}

// LookupPartialSession validates a session token and returns (userID, error).
// Returns ErrSessionNotFound if the session is not a valid unexpired partial session.
func (s *Store) LookupPartialSession(ctx context.Context, token string) (string, error) {
	if token == "" {
		return "", ErrSessionNotFound
	}
	var userID, expiresAt string
	var revoked, partial int
	err := s.db.QueryRowContext(ctx,
		s.db.Rebind(`SELECT user_id, expires_at, revoked, partial FROM sessions WHERE id = ?`), token,
	).Scan(&userID, &expiresAt, &revoked, &partial)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrSessionNotFound
	}
	if err != nil {
		return "", fmt.Errorf("auth: lookup partial session: %w", err)
	}
	if revoked != 0 || partial == 0 {
		return "", ErrSessionNotFound
	}
	exp, err := time.Parse(time.RFC3339, expiresAt)
	if err != nil || now().After(exp) {
		return "", ErrSessionNotFound
	}
	return userID, nil
}

// IsPartialSession returns true if the given token corresponds to a partial session.
// Returns false for full sessions, expired sessions, and missing sessions.
func (s *Store) IsPartialSession(ctx context.Context, token string) bool {
	if token == "" {
		return false
	}
	var partial int
	// Rebind the ? placeholder: on the Postgres backend a raw ? is invalid and the
	// query errors on every call, silently degrading this pre-check to "not partial"
	// (the RequireSession totp_required hint is lost, though LookupSession still
	// independently rejects partial sessions). Rebind restores correct behaviour.
	err := s.db.QueryRowContext(ctx,
		s.db.Rebind(`SELECT partial FROM sessions WHERE id = ? AND revoked = 0`), token,
	).Scan(&partial)
	if err != nil {
		return false
	}
	return partial != 0
}
