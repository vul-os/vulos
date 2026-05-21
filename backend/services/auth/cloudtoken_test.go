package auth

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
	"time"

	"vulos/backend/services/signing"
)

// ─── Helpers ──────────────────────────────────────────────────────────────────

// newOfflineVerifier builds a CloudOfflineVerifier pointed at a temp cache
// file and returns the verifier plus a cleanup function.
func newOfflineVerifier(t *testing.T, pub ed25519.PublicKey, grace time.Duration) *CloudOfflineVerifier {
	t.Helper()
	store := makeTestStore(t)
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "cloudtoken-cache.json")
	t.Setenv("VULOS_CLOUDTOKEN_CACHE", cachePath)
	return NewCloudOfflineVerifier(store, pub, grace)
}

// signedRequest produces (tokenBytes, sigB64) for a CloudLoginToken using priv.
func signedRequest(t *testing.T, priv ed25519.PrivateKey, tok CloudLoginToken) ([]byte, string) {
	t.Helper()
	b, err := signing.Canonical(tok)
	if err != nil {
		t.Fatalf("canonical: %v", err)
	}
	sig := signing.Sign(priv, b)
	return b, base64.StdEncoding.EncodeToString(sig)
}

// ─── WriteTokenCache / ReadTokenCache ────────────────────────────────────────

func TestWriteReadTokenCache(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("VULOS_CLOUDTOKEN_CACHE", filepath.Join(dir, "cache.json"))

	now := time.Now().UTC().Truncate(time.Second)
	c := CloudTokenCache{
		AccountID:      "01HZACC001",
		Email:          "alice@example.com",
		Name:           "Alice",
		DeviceULID:     "01HZDEV001",
		TokenExpiresAt: now.Add(30 * time.Minute),
		ValidatedAt:    now,
		GracePeriodStr: "72h0m0s",
	}

	if err := WriteTokenCache(c); err != nil {
		t.Fatalf("WriteTokenCache: %v", err)
	}

	got, err := ReadTokenCache()
	if err != nil {
		t.Fatalf("ReadTokenCache: %v", err)
	}
	if got.AccountID != c.AccountID {
		t.Errorf("AccountID: got %q want %q", got.AccountID, c.AccountID)
	}
	if got.Email != c.Email {
		t.Errorf("Email: got %q want %q", got.Email, c.Email)
	}
	if !got.ValidatedAt.Equal(c.ValidatedAt) {
		t.Errorf("ValidatedAt: got %v want %v", got.ValidatedAt, c.ValidatedAt)
	}
}

func TestReadTokenCache_NotFound(t *testing.T) {
	t.Setenv("VULOS_CLOUDTOKEN_CACHE", filepath.Join(t.TempDir(), "nonexistent.json"))
	_, err := ReadTokenCache()
	if err != ErrCacheNotFound {
		t.Errorf("expected ErrCacheNotFound, got %v", err)
	}
}

func TestDeleteTokenCache(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cache.json")
	t.Setenv("VULOS_CLOUDTOKEN_CACHE", path)

	c := CloudTokenCache{AccountID: "x", ValidatedAt: time.Now().UTC()}
	if err := WriteTokenCache(c); err != nil {
		t.Fatalf("WriteTokenCache: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("cache file not created: %v", err)
	}
	if err := DeleteTokenCache(); err != nil {
		t.Fatalf("DeleteTokenCache: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("expected cache file to be deleted")
	}
	// Second delete should not error.
	if err := DeleteTokenCache(); err != nil {
		t.Errorf("DeleteTokenCache on absent file: %v", err)
	}
}

func TestWriteTokenCache_Atomic(t *testing.T) {
	// Verify no .tmp file is left after a successful write.
	dir := t.TempDir()
	path := filepath.Join(dir, "cache.json")
	t.Setenv("VULOS_CLOUDTOKEN_CACHE", path)

	c := CloudTokenCache{AccountID: "y", ValidatedAt: time.Now().UTC()}
	if err := WriteTokenCache(c); err != nil {
		t.Fatalf("WriteTokenCache: %v", err)
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Error("temp file should have been renamed away")
	}
}

// ─── Online Login writes cache ────────────────────────────────────────────────

func TestCloudOfflineVerifier_OnlineLoginWritesCache(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	v := newOfflineVerifier(t, pub, 72*time.Hour)

	tok := freshToken()
	tokenBytes, sigB64 := signedRequest(t, priv, tok)

	info, err := v.Login(tokenBytes, sigB64)
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if info.Email != tok.Email {
		t.Errorf("email: got %q want %q", info.Email, tok.Email)
	}

	// Cache should now exist.
	cache, err := ReadTokenCache()
	if err != nil {
		t.Fatalf("ReadTokenCache after online login: %v", err)
	}
	if cache.AccountID != tok.AccountID {
		t.Errorf("cached AccountID: got %q want %q", cache.AccountID, tok.AccountID)
	}
	if cache.Email != tok.Email {
		t.Errorf("cached Email: got %q want %q", cache.Email, tok.Email)
	}
}

// ─── Offline login — within grace ────────────────────────────────────────────

func TestCloudOfflineVerifier_OfflineWithinGrace(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	v := newOfflineVerifier(t, pub, 72*time.Hour)

	// Perform online login to seed cache.
	tok := freshToken()
	tokenBytes, sigB64 := signedRequest(t, priv, tok)
	if _, err := v.Login(tokenBytes, sigB64); err != nil {
		t.Fatalf("online Login: %v", err)
	}

	// Offline login should succeed while within grace.
	info, err := v.LoginOffline()
	if err != nil {
		t.Fatalf("LoginOffline: %v", err)
	}
	if info.Email != tok.Email {
		t.Errorf("email: got %q want %q", info.Email, tok.Email)
	}
	if info.AccountID != tok.AccountID {
		t.Errorf("AccountID: got %q want %q", info.AccountID, tok.AccountID)
	}
	if info.SessionToken == "" {
		t.Error("expected non-empty session token")
	}
}

// ─── Offline login — past grace ───────────────────────────────────────────────

func TestCloudOfflineVerifier_OfflinePastGrace(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	v := newOfflineVerifier(t, pub, 72*time.Hour)

	// Seed cache with a ValidatedAt in the past (beyond grace).
	tok := freshToken()
	tokenBytes, sigB64 := signedRequest(t, priv, tok)
	if _, err := v.Login(tokenBytes, sigB64); err != nil {
		t.Fatalf("online Login: %v", err)
	}

	// Overwrite cache with an old ValidatedAt.
	expired := CloudTokenCache{
		AccountID:   tok.AccountID,
		Email:       tok.Email,
		Name:        tok.Name,
		ValidatedAt: time.Now().Add(-73 * time.Hour), // 1 h past default grace
	}
	if err := WriteTokenCache(expired); err != nil {
		t.Fatalf("WriteTokenCache (expired): %v", err)
	}

	_, err := v.LoginOffline()
	if err != ErrGraceExpired {
		t.Errorf("expected ErrGraceExpired, got %v", err)
	}
}

// ─── Offline login — no cache ─────────────────────────────────────────────────

func TestCloudOfflineVerifier_OfflineNoCache(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	v := newOfflineVerifier(t, pub, 72*time.Hour)

	_, err := v.LoginOffline()
	if err != ErrCacheNotFound {
		t.Errorf("expected ErrCacheNotFound, got %v", err)
	}
}

// ─── Tampered token — fail-closed ────────────────────────────────────────────

func TestCloudOfflineVerifier_TamperedTokenFailClosed(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	v := newOfflineVerifier(t, pub, 72*time.Hour)

	// Seed a valid cache first.
	tok := freshToken()
	tokenBytes, sigB64 := signedRequest(t, priv, tok)
	if _, err := v.Login(tokenBytes, sigB64); err != nil {
		t.Fatalf("online Login: %v", err)
	}

	// Now tamper the token.
	tokenBytes[0] ^= 0xFF
	_, err := v.Login(tokenBytes, sigB64)
	if err != ErrBadSignature {
		t.Errorf("expected ErrBadSignature for tampered token, got %v", err)
	}
}

// ─── Expired token — rejected online ─────────────────────────────────────────

func TestCloudOfflineVerifier_ExpiredTokenRejected(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	v := newOfflineVerifier(t, pub, 72*time.Hour)

	tok := freshToken()
	tok.ExpiresAt = time.Now().Add(-1 * time.Minute) // already expired
	tokenBytes, sigB64 := signedRequest(t, priv, tok)

	_, err := v.Login(tokenBytes, sigB64)
	if err != ErrTokenExpired {
		t.Errorf("expected ErrTokenExpired, got %v", err)
	}
}

// ─── Not enrolled — no broker pubkey ─────────────────────────────────────────

func TestCloudOfflineVerifier_NotEnrolled(t *testing.T) {
	v := newOfflineVerifier(t, nil, 72*time.Hour) // nil pubkey = not enrolled

	tok := freshToken()
	b, _ := signing.Canonical(tok)
	_, err := v.Login(b, "aabb")
	if err != ErrNoBrokerPubkey {
		t.Errorf("expected ErrNoBrokerPubkey, got %v", err)
	}
}

// ─── CacheIsWithinGrace ───────────────────────────────────────────────────────

func TestCacheIsWithinGrace_NoCache(t *testing.T) {
	t.Setenv("VULOS_CLOUDTOKEN_CACHE", filepath.Join(t.TempDir(), "none.json"))
	ok, err := CacheIsWithinGrace(72 * time.Hour)
	if err != ErrCacheNotFound {
		t.Errorf("expected ErrCacheNotFound, got %v", err)
	}
	if ok {
		t.Error("expected ok=false when no cache")
	}
}

func TestCacheIsWithinGrace_Within(t *testing.T) {
	t.Setenv("VULOS_CLOUDTOKEN_CACHE", filepath.Join(t.TempDir(), "c.json"))
	c := CloudTokenCache{AccountID: "z", ValidatedAt: time.Now().Add(-1 * time.Hour)}
	if err := WriteTokenCache(c); err != nil {
		t.Fatalf("WriteTokenCache: %v", err)
	}
	ok, err := CacheIsWithinGrace(72 * time.Hour)
	if err != nil {
		t.Fatalf("CacheIsWithinGrace: %v", err)
	}
	if !ok {
		t.Error("expected ok=true when within grace")
	}
}

func TestCacheIsWithinGrace_Expired(t *testing.T) {
	t.Setenv("VULOS_CLOUDTOKEN_CACHE", filepath.Join(t.TempDir(), "c.json"))
	c := CloudTokenCache{AccountID: "z", ValidatedAt: time.Now().Add(-73 * time.Hour)}
	if err := WriteTokenCache(c); err != nil {
		t.Fatalf("WriteTokenCache: %v", err)
	}
	ok, err := CacheIsWithinGrace(72 * time.Hour)
	if err != nil {
		t.Fatalf("CacheIsWithinGrace: %v", err)
	}
	if ok {
		t.Error("expected ok=false when past grace")
	}
}

// ─── GracePeriod env override ─────────────────────────────────────────────────

func TestEffectiveGrace_EnvOverride(t *testing.T) {
	t.Setenv("VULOS_CLOUDTOKEN_GRACE", "48h")
	v := newOfflineVerifier(t, nil, 72*time.Hour)
	if v.GracePeriod() != 48*time.Hour {
		t.Errorf("expected 48h grace from env, got %v", v.GracePeriod())
	}
}

func TestEffectiveGrace_Default(t *testing.T) {
	// Ensure env is not set.
	t.Setenv("VULOS_CLOUDTOKEN_GRACE", "")
	v := newOfflineVerifier(t, nil, 0) // 0 → DefaultGracePeriod
	if v.GracePeriod() != DefaultGracePeriod {
		t.Errorf("expected DefaultGracePeriod (%v), got %v", DefaultGracePeriod, v.GracePeriod())
	}
}
