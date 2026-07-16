// device.go -- recognised-device token for vulos.cloud control-plane.
//
// On a successful full login the server mints a long-lived httpOnly cookie
// named "vc_device".  On subsequent logins, if the client sends
// remember_device=true AND the cookie validates against the recomputed device
// fingerprint for THIS user, the TOTP step is skipped.
//
// Fingerprint (v1): SHA-256(ip + "\x00" + user_agent + "\x00" + user_id)
// This is sufficient for v1.  No impossible-travel / geo-velocity detection is
// implemented intentionally — users legitimately log in from multiple
// locations (home, office, VPN, travel).  That feature remains out of scope.
//
// Token format: base64url(nonce_16B || HMAC-SHA256(AUTH_DEVICE_KEK, fp || nonce))
// The fp is the 32-byte raw SHA-256 of the fingerprint string.
// Stored in DB: SHA-256(cookie_value) so we can look it up without storing the
// secret itself.
//
// Rotation: on each successful device-recognised login, the old token row is
// marked rotated=1 and a fresh token is issued.  This limits the window for
// stolen-cookie reuse.
//
// Cross-account confusion prevention: fp encodes user_id, so a token minted for
// user A will not satisfy the fp check for user B.
package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/vul-os/vulos-management/pkg/env"
)

const (
	// DeviceCookieName is the httpOnly cookie carrying the recognised-device token.
	DeviceCookieName = "vc_device"
	// DeviceCookieMaxAge is 90 days in seconds.
	DeviceCookieMaxAge = 90 * 24 * 60 * 60
	// DeviceCookieDuration is the time.Duration form used for expiry arithmetic.
	DeviceCookieDuration = 90 * 24 * time.Hour
)

// ErrNoDeviceToken is returned when no valid device token is found for the user.
var ErrNoDeviceToken = errors.New("auth: no valid device token")

// deviceFallbackKEK is a 32-byte all-zeros key used only in dev mode.
var deviceFallbackKEK = make([]byte, 32)

// LoadDeviceKEK returns the 32-byte HMAC key from AUTH_DEVICE_KEK (base64).
// If unset and VULOS_DEV=true it returns the dev fallback key.
func LoadDeviceKEK() ([]byte, error) {
	raw := os.Getenv("AUTH_DEVICE_KEK")
	if raw == "" {
		// WAVE34: dev fallback requires VULOS_DEV truthy AND env!=prod (cross-check).
		if env.DevKEKFallbackAllowed() {
			return deviceFallbackKEK, nil
		}
		return nil, fmt.Errorf("auth: AUTH_DEVICE_KEK must be set (base64-encoded 32-byte key)")
	}
	key, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return nil, fmt.Errorf("auth: AUTH_DEVICE_KEK is not valid base64: %w", err)
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("auth: AUTH_DEVICE_KEK must decode to exactly 32 bytes, got %d", len(key))
	}
	return key, nil
}

// deviceFingerprint returns SHA-256(ip + "\x00" + ua + "\x00" + userID).
// The NUL separators prevent collisions between adjacent fields.
func deviceFingerprint(ip, ua, userID string) []byte {
	h := sha256.New()
	h.Write([]byte(ip))
	h.Write([]byte{0})
	h.Write([]byte(ua))
	h.Write([]byte{0})
	h.Write([]byte(userID))
	return h.Sum(nil)
}

// mintDeviceToken creates a new vc_device cookie value.
// Format: base64url(nonce_16 || HMAC-SHA256(kek, fp_32 || nonce_16))
// Total: 16 + 32 = 48 bytes → 64-char base64url string (no padding).
func mintDeviceToken(fp, kek []byte) (string, error) {
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("auth: device token nonce: %w", err)
	}
	mac := hmac.New(sha256.New, kek)
	mac.Write(fp)
	mac.Write(nonce)
	sig := mac.Sum(nil)

	raw := append(nonce, sig...)
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// verifyDeviceToken validates a cookie value against fp and kek.
// Returns true only when the HMAC matches.
func verifyDeviceToken(token string, fp, kek []byte) bool {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(raw) != 48 {
		return false
	}
	nonce := raw[:16]
	sig := raw[16:]

	mac := hmac.New(sha256.New, kek)
	mac.Write(fp)
	mac.Write(nonce)
	expected := mac.Sum(nil)

	return hmac.Equal(sig, expected)
}

// tokenHash returns the hex-encoded SHA-256 of the cookie value for DB storage.
func tokenHash(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

// fpHash returns the hex-encoded SHA-256 of the fingerprint bytes.
func fpHex(fp []byte) string {
	h := sha256.Sum256(fp)
	return hex.EncodeToString(h[:])
}

// SetDeviceCookie writes the vc_device cookie to the response.
func SetDeviceCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     DeviceCookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   DeviceCookieMaxAge,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})
}

// DeviceTokenFromRequest extracts the vc_device cookie value from r.
// Returns "" if absent.
func DeviceTokenFromRequest(r *http.Request) string {
	c, err := r.Cookie(DeviceCookieName)
	if err != nil {
		return ""
	}
	return c.Value
}

// ─────────────────────────────────────────────────────────────────────────────
// Store methods
// ─────────────────────────────────────────────────────────────────────────────

// IssueDeviceToken mints a new vc_device token for the given user/ip/ua,
// persists it (scoped to the fingerprint), and returns the cookie value.
// Old tokens for the same user+fingerprint are marked rotated.
func (s *Store) IssueDeviceToken(ctx context.Context, userID, ip, ua string) (string, error) {
	kek, err := LoadDeviceKEK()
	if err != nil {
		return "", err
	}

	fp := deviceFingerprint(ip, ua, userID)
	token, err := mintDeviceToken(fp, kek)
	if err != nil {
		return "", err
	}

	th := tokenHash(token)
	fh := fpHex(fp)
	n := now()
	expires := n.Add(DeviceCookieDuration)
	ts := n.Format(time.RFC3339)

	id, err := newULID()
	if err != nil {
		return "", err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Mark any existing active token rows for this user+fp as rotated.
	_, _ = s.db.ExecContext(ctx,
		s.db.Rebind(`UPDATE device_tokens SET rotated = 1
		 WHERE user_id = ? AND fp_hash = ? AND rotated = 0`),
		userID, fh,
	)

	_, err = s.db.ExecContext(ctx,
		s.db.Rebind(`INSERT INTO device_tokens (id, user_id, token_hash, fp_hash, created_at, expires_at, rotated)
		 VALUES (?, ?, ?, ?, ?, ?, 0)`),
		id, userID, th, fh, ts, expires.Format(time.RFC3339),
	)
	if err != nil {
		return "", fmt.Errorf("auth: insert device token: %w", err)
	}

	return token, nil
}

// ValidateAndRotateDeviceToken checks whether the supplied cookie value is a
// valid, unexpired device token for (userID, ip, ua).  On success it rotates
// the token (marks old row superseded, issues a new one) and returns the new
// cookie value.
//
// Returns ErrNoDeviceToken if the token is absent, invalid, expired, or
// belongs to a different user / fingerprint.
func (s *Store) ValidateAndRotateDeviceToken(ctx context.Context, userID, ip, ua, cookieVal string) (string, error) {
	if cookieVal == "" {
		return "", ErrNoDeviceToken
	}

	kek, err := LoadDeviceKEK()
	if err != nil {
		return "", err
	}

	fp := deviceFingerprint(ip, ua, userID)

	// HMAC check first (fast, before any DB hit).
	if !verifyDeviceToken(cookieVal, fp, kek) {
		return "", ErrNoDeviceToken
	}

	th := tokenHash(cookieVal)

	// Look up in DB: must be for this user, not rotated, not expired.
	var dbUserID, expiresAt string
	var rotated int
	err = s.db.QueryRowContext(ctx,
		s.db.Rebind(`SELECT user_id, expires_at, rotated FROM device_tokens WHERE token_hash = ?`), th,
	).Scan(&dbUserID, &expiresAt, &rotated)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNoDeviceToken
	}
	if err != nil {
		return "", fmt.Errorf("auth: lookup device token: %w", err)
	}

	// Cross-account confusion guard: token_hash must belong to THIS user.
	if dbUserID != userID {
		return "", ErrNoDeviceToken
	}
	if rotated != 0 {
		return "", ErrNoDeviceToken
	}
	exp, err := time.Parse(time.RFC3339, expiresAt)
	if err != nil || now().After(exp) {
		return "", ErrNoDeviceToken
	}

	// Token is valid — issue a new one (rotation).
	newToken, err := s.IssueDeviceToken(ctx, userID, ip, ua)
	if err != nil {
		return "", err
	}

	return newToken, nil
}

// DeleteAllDeviceTokens removes all device tokens for a user (e.g. on password reset).
func (s *Store) DeleteAllDeviceTokens(ctx context.Context, userID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx, s.db.Rebind(`DELETE FROM device_tokens WHERE user_id = ?`), userID)
	return err
}

// ─────────────────────────────────────────────────────────────────────────────
// Store methods — 2FA lockout
// ─────────────────────────────────────────────────────────────────────────────

// maxFailed2FAAttempts is the number of consecutive failed TOTP attempts before
// the account is locked.
const maxFailed2FAAttempts = 5

// LockoutDuration is the window for which a locked account cannot attempt TOTP.
// Made package-level var (not const) so tests can inject a shorter duration
// without sleeping 15 minutes.
var LockoutDuration = 15 * time.Minute

// Check2FALockout checks whether the user is currently locked out.
// Returns (lockedUntilUnixSec, isLocked).  If isLocked the caller should
// return 429 with retry_after = lockedUntilUnixSec - now().Unix().
func (s *Store) Check2FALockout(ctx context.Context, userID string) (int64, bool) {
	var lockedUntil sql.NullInt64
	err := s.db.QueryRowContext(ctx,
		s.db.Rebind(`SELECT locked_until FROM users WHERE id = ?`), userID,
	).Scan(&lockedUntil)
	if err != nil || !lockedUntil.Valid {
		return 0, false
	}
	if lockedUntil.Int64 <= now().Unix() {
		return 0, false
	}
	return lockedUntil.Int64, true
}

// RecordFailed2FA increments the failed TOTP counter for userID.
// When the counter reaches maxFailed2FAAttempts, sets locked_until = now + LockoutDuration.
func (s *Store) RecordFailed2FA(ctx context.Context, userID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Increment counter.
	_, err := s.db.ExecContext(ctx,
		s.db.Rebind(`UPDATE users SET failed_2fa_count = failed_2fa_count + 1, updated_at = ? WHERE id = ?`),
		now().Format(time.RFC3339), userID,
	)
	if err != nil {
		return fmt.Errorf("auth: increment failed_2fa_count: %w", err)
	}

	// Check whether we just crossed the threshold.
	var count int
	if err := s.db.QueryRowContext(ctx,
		s.db.Rebind(`SELECT failed_2fa_count FROM users WHERE id = ?`), userID,
	).Scan(&count); err != nil {
		return fmt.Errorf("auth: read failed_2fa_count: %w", err)
	}

	if count >= maxFailed2FAAttempts {
		lockUntil := now().Add(LockoutDuration).Unix()
		_, err = s.db.ExecContext(ctx,
			s.db.Rebind(`UPDATE users SET locked_until = ?, updated_at = ? WHERE id = ?`),
			lockUntil, now().Format(time.RFC3339), userID,
		)
		if err != nil {
			return fmt.Errorf("auth: set locked_until: %w", err)
		}
	}
	return nil
}

// ResetFailed2FA clears the failed counter and lockout after a successful TOTP
// verification.
func (s *Store) ResetFailed2FA(ctx context.Context, userID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Clear the locked_until using a string representation of NULL.
	_, err := s.db.ExecContext(ctx,
		s.db.Rebind(`UPDATE users
		 SET failed_2fa_count = 0,
		     locked_until = NULL,
		     updated_at = ?
		 WHERE id = ?`),
		now().Format(time.RFC3339), userID,
	)
	if err != nil {
		return fmt.Errorf("auth: reset failed_2fa: %w", err)
	}
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Helpers for the login handler — device-token shortcut
// ─────────────────────────────────────────────────────────────────────────────

// loginBodyHasRememberDevice is a small helper used by routes_auth.go to decode
// the extra field without changing the existing login body struct.
// We embed it here to keep device logic in one file.

// CheckAndIssueFull login skips the TOTP partial-session step when:
//  1. remember_device is true
//  2. a valid vc_device cookie is present and matches userID/ip/ua
//
// Returns (newDeviceToken, skipTOTP, err).
// If skipTOTP is true the caller should issue a full session immediately.
func (s *Store) CheckDeviceSkipTOTP(ctx context.Context, userID, ip, ua, cookieVal string) (newToken string, skip bool, err error) {
	if cookieVal == "" {
		return "", false, nil
	}
	newToken, err = s.ValidateAndRotateDeviceToken(ctx, userID, ip, ua, cookieVal)
	if errors.Is(err, ErrNoDeviceToken) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return newToken, true, nil
}
