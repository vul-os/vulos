// Package auth — cloudtoken.go
//
// CLOGIN-02: Cloud-token signature verification + offline grace-period cache.
//
// Offline grace-period logic:
//
//   - On every successful cloud login the verified token is persisted to
//     ~/.vulos/auth/cloudtoken-cache.json (atomic write via temp-file rename).
//   - When the caller invokes LoginOffline the cache is consulted.  If the last
//     successful validation is within GracePeriod (default 72 h) login is
//     allowed using the cached credentials.
//   - Outside the grace period the cache is rejected and login fails.
//   - Bad signatures always fail immediately — the cache is NEVER consulted
//     when a token is present but the signature is wrong (fail-closed).
//
// Cache location: VULOS_CLOUDTOKEN_CACHE env overrides ~/.vulos/auth/cloudtoken-cache.json.
// Grace period:   VULOS_CLOUDTOKEN_GRACE env overrides the configured default (e.g. "48h").
package auth

import (
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"vulos/backend/services/signing"
)

// ─── Default configuration ────────────────────────────────────────────────────

const (
	// DefaultGracePeriod is the time window after the last successful online
	// validation during which offline logins are still permitted.
	DefaultGracePeriod = 72 * time.Hour

	// defaultCacheRelPath is the path relative to the user's home directory.
	defaultCacheRelPath = ".vulos/auth/cloudtoken-cache.json"

	cacheFileMode = 0600
	cacheDirMode  = 0700
)

// ─── Cache record ─────────────────────────────────────────────────────────────

// CloudTokenCache is the JSON record persisted to disk after each successful
// online validation.  It carries enough information to reconstruct a session
// without re-contacting the cloud.
type CloudTokenCache struct {
	// AccountID is the cloud account ULID from the last valid token.
	AccountID string `json:"account_id"`
	// Email is the cloud account email from the last valid token.
	Email string `json:"email"`
	// Name is the display name from the last valid token.
	Name string `json:"name"`
	// DeviceULID is the device ULID the token was bound to.
	DeviceULID string `json:"device_ulid"`
	// TokenExpiresAt is the expiry field from the last valid token (informational).
	TokenExpiresAt time.Time `json:"token_expires_at"`
	// ValidatedAt is the wall-clock time the last successful online verification
	// occurred.  This is the anchor for the grace-period window.
	ValidatedAt time.Time `json:"validated_at"`
	// GracePeriodStr records the configured grace duration for auditability.
	// It is informational only; the verifier's own GracePeriod governs behaviour.
	GracePeriodStr string `json:"grace_period"`
	// MAC is a base64 HMAC-SHA256 over the canonical record (with MAC empty),
	// keyed by a device-local secret. It is verified on read so that a local
	// file write cannot forge an offline session (CLOGIN-02 integrity guard).
	MAC string `json:"mac,omitempty"`
}

// ─── Cache path ───────────────────────────────────────────────────────────────

// cacheFilePath returns the effective path for the token cache file.
// VULOS_CLOUDTOKEN_CACHE env takes priority.
func cacheFilePath() (string, error) {
	if env := os.Getenv("VULOS_CLOUDTOKEN_CACHE"); env != "" {
		return env, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cloudtoken: resolve home dir: %w", err)
	}
	return filepath.Join(home, defaultCacheRelPath), nil
}

// ─── Grace period ─────────────────────────────────────────────────────────────

// effectiveGrace returns the grace period, applying the VULOS_CLOUDTOKEN_GRACE
// env override when present.  Falls back to def.
func effectiveGrace(def time.Duration) time.Duration {
	if env := os.Getenv("VULOS_CLOUDTOKEN_GRACE"); env != "" {
		if d, err := time.ParseDuration(env); err == nil && d > 0 {
			return d
		}
	}
	if def <= 0 {
		return DefaultGracePeriod
	}
	return def
}

// ─── Sentinel errors ──────────────────────────────────────────────────────────

var (
	// ErrCacheNotFound means no cache file exists on disk.
	ErrCacheNotFound = errors.New("cloudtoken: no cached token found on this device")

	// ErrGraceExpired means a cache exists but the offline grace period has elapsed.
	ErrGraceExpired = errors.New("cloudtoken: offline grace period expired — full re-authentication required")

	// ErrCacheTampered means the cache file failed its HMAC integrity check —
	// it was modified outside the legitimate write path. Fail closed: the
	// cache is NOT trusted and offline login is refused.
	ErrCacheTampered = errors.New("cloudtoken: cached token failed integrity check — refusing to trust it")
)

// ─── Cache integrity (HMAC) ───────────────────────────────────────────────────

// cloudtokenMACKey loads (or creates) the device-local 32-byte secret used to
// HMAC the offline token cache. It is colocated with the cache file so that
// each box / test sandbox has its own key. The key file is mode 0600.
//
// This raises the bar against the "local file write forges a session" attack:
// an actor who can write the cache JSON but cannot read this 0600 secret key
// cannot produce a valid MAC, so a forged cache is rejected on read.
func cloudtokenMACKey() ([]byte, error) {
	path, err := cacheFilePath()
	if err != nil {
		return nil, err
	}
	keyPath := filepath.Join(filepath.Dir(path), ".cloudtoken.key")
	return loadOrCreateSecret(keyPath), nil
}

// cacheMAC computes the base64 HMAC-SHA256 over the canonical form of c with
// its MAC field cleared.
func cacheMAC(key []byte, c CloudTokenCache) (string, error) {
	c.MAC = "" // never sign over the MAC itself
	canon, err := signing.Canonical(c)
	if err != nil {
		return "", fmt.Errorf("cloudtoken: canonicalize cache: %w", err)
	}
	mac := hmac.New(sha256.New, key)
	mac.Write(canon)
	return base64.StdEncoding.EncodeToString(mac.Sum(nil)), nil
}

// ─── Persistence helpers ──────────────────────────────────────────────────────

// WriteTokenCache atomically persists c to the cache file.
// Uses a sibling temp file + rename so a crash mid-write never corrupts the cache.
func WriteTokenCache(c CloudTokenCache) error {
	path, err := cacheFilePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), cacheDirMode); err != nil {
		return fmt.Errorf("cloudtoken: mkdir cache dir: %w", err)
	}
	// Integrity: stamp an HMAC over the record so reads can detect tampering.
	key, err := cloudtokenMACKey()
	if err != nil {
		return err
	}
	mac, err := cacheMAC(key, c)
	if err != nil {
		return err
	}
	c.MAC = mac
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("cloudtoken: marshal cache: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, cacheFileMode); err != nil {
		return fmt.Errorf("cloudtoken: write temp cache: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp) //nolint:errcheck — best-effort
		return fmt.Errorf("cloudtoken: rename cache into place: %w", err)
	}
	return nil
}

// ReadTokenCache reads and parses the cache file.
// Returns ErrCacheNotFound when the file does not exist.
func ReadTokenCache() (*CloudTokenCache, error) {
	path, err := cacheFilePath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrCacheNotFound
		}
		return nil, fmt.Errorf("cloudtoken: read cache: %w", err)
	}
	var c CloudTokenCache
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("cloudtoken: parse cache: %w", err)
	}

	// Integrity: verify the HMAC before trusting any field. Fail closed —
	// a missing or wrong MAC means the file was not written by us.
	key, err := cloudtokenMACKey()
	if err != nil {
		return nil, err
	}
	want, err := cacheMAC(key, c)
	if err != nil {
		return nil, err
	}
	if c.MAC == "" || !hmac.Equal([]byte(c.MAC), []byte(want)) {
		log.Printf("[cloudtoken] cache integrity check FAILED — refusing to trust cached token")
		return nil, ErrCacheTampered
	}
	return &c, nil
}

// DeleteTokenCache removes the cache file (no-op if absent).
func DeleteTokenCache() error {
	path, err := cacheFilePath()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("cloudtoken: delete cache: %w", err)
	}
	return nil
}

// ─── CloudOfflineVerifier ─────────────────────────────────────────────────────

// CloudOfflineVerifier wraps a CloudLoginVerifier and adds an offline
// grace-period cache.  It is the primary CLOGIN-02 entry point.
//
// Online path:  call Login — verifies signature, writes cache on success.
// Offline path: call LoginOffline — consults cache, allows login within grace.
type CloudOfflineVerifier struct {
	inner       *CloudLoginVerifier
	gracePeriod time.Duration
}

// NewCloudOfflineVerifier creates a CloudOfflineVerifier.
//
//   - store   — auth store (find-or-create users / sessions)
//   - pubkey  — login-broker Ed25519 public key; nil means "not enrolled"
//   - grace   — offline grace window; 0 uses DefaultGracePeriod (72 h).
//     VULOS_CLOUDTOKEN_GRACE env overrides this at runtime.
func NewCloudOfflineVerifier(store *Store, pubkey ed25519.PublicKey, grace time.Duration) *CloudOfflineVerifier {
	return &CloudOfflineVerifier{
		inner:       NewCloudLoginVerifier(store, pubkey),
		gracePeriod: effectiveGrace(grace),
	}
}

// GracePeriod returns the effective offline grace period for this verifier.
func (v *CloudOfflineVerifier) GracePeriod() time.Duration {
	return v.gracePeriod
}

// Login verifies tokenBytes + sigB64 (online path).
//
// On success it persists the validated token to the grace-period cache and
// returns a CloudSessionInfo with a fresh OS session token.
//
// Fail-closed: a bad signature always returns ErrBadSignature regardless of
// whether a valid cache entry exists.
func (v *CloudOfflineVerifier) Login(tokenBytes []byte, sigB64 string) (*CloudSessionInfo, error) {
	info, err := v.inner.Login(tokenBytes, sigB64)
	if err != nil {
		return nil, err
	}

	// Persist the successful validation for future offline logins. Bind the
	// cache to this device's ULID so a cache copied to another box is rejected.
	cache := CloudTokenCache{
		AccountID:      info.AccountID,
		Email:          info.Email,
		Name:           info.Name,
		DeviceULID:     localDeviceULID(),
		TokenExpiresAt: info.ExpiresAt,
		ValidatedAt:    time.Now().UTC(),
		GracePeriodStr: v.gracePeriod.String(),
	}
	if err := WriteTokenCache(cache); err != nil {
		// Non-fatal: log but do not fail the login.
		log.Printf("[cloudtoken] warning: could not write token cache: %v", err)
	}

	return info, nil
}

// LoginOffline attempts a login using the persisted grace-period cache.
//
// Returns:
//   - (info, nil)          — cache valid and within grace; OS session created.
//   - (nil, ErrCacheNotFound) — no cache on disk.
//   - (nil, ErrGraceExpired)  — cache exists but grace period has elapsed.
func (v *CloudOfflineVerifier) LoginOffline() (*CloudSessionInfo, error) {
	cache, err := ReadTokenCache()
	if err != nil {
		return nil, err
	}

	// Device binding: a cache minted for another device's ULID must not be
	// usable here (e.g. file copied between boxes). Enforced when this device
	// knows its own ULID; skipped on self-host boxes without VULOS_DEVICE_ULID.
	if dev := localDeviceULID(); dev != "" && cache.DeviceULID != "" && cache.DeviceULID != dev {
		log.Printf("[cloudtoken] offline reject: cache bound to device %s, this device is %s",
			cache.DeviceULID, dev)
		return nil, ErrDeviceMismatch
	}

	age := time.Since(cache.ValidatedAt)
	if age > v.gracePeriod {
		log.Printf("[cloudtoken] offline grace expired: last validated %s ago (grace=%s)",
			age.Round(time.Minute), v.gracePeriod)
		return nil, ErrGraceExpired
	}

	log.Printf("[cloudtoken] offline grace login OK: account=%s email=%s validated=%s ago (grace=%s)",
		cache.AccountID, cache.Email, age.Round(time.Minute), v.gracePeriod)

	user := v.inner.store.FindOrCreateUser(
		"cloud", cache.AccountID, cache.Email, cache.Name, "",
	)
	sess := v.inner.store.CreateSession(user, "cloud:"+cache.AccountID)

	return &CloudSessionInfo{
		SessionToken: sess.Token,
		UserID:       user.ID,
		Email:        cache.Email,
		Name:         cache.Name,
		AccountID:    cache.AccountID,
		ExpiresAt:    sess.ExpiresAt,
	}, nil
}

// ─── Package-level helpers ────────────────────────────────────────────────────

// CacheIsWithinGrace reports whether the persisted cache is still within grace.
// Returns (false, ErrCacheNotFound) when no cache file exists.
func CacheIsWithinGrace(grace time.Duration) (bool, error) {
	cache, err := ReadTokenCache()
	if err != nil {
		return false, err
	}
	return time.Since(cache.ValidatedAt) <= grace, nil
}
