package auth

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"vulos/backend/services/signing"
)

// makeTestStore creates a minimal in-memory Store for testing.
func makeTestStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return s
}

// makeTestKeyPair generates a fresh Ed25519 key pair for tests.
func makeTestKeyPair(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return pub, priv
}

// signToken signs a CloudLoginToken with priv and returns (canonicalBytes, sigBase64).
func signToken(t *testing.T, priv ed25519.PrivateKey, tok CloudLoginToken) ([]byte, string) {
	t.Helper()
	b, err := signing.Canonical(tok)
	if err != nil {
		t.Fatalf("canonical: %v", err)
	}
	sig := signing.Sign(priv, b)
	return b, base64.StdEncoding.EncodeToString(sig)
}

// freshToken returns a CloudLoginToken that is valid (not expired) for tests.
func freshToken() CloudLoginToken {
	return CloudLoginToken{
		AccountID: "01HZABCDE12345678",
		ULID:      "01HZDEVICEULID001",
		Email:     "user@example.com",
		Name:      "Test User",
		IssuedAt:  time.Now().Add(-5 * time.Minute),
		ExpiresAt: time.Now().Add(30 * time.Minute),
	}
}

// ─── Tests ────────────────────────────────────────────────────────────────────

func TestCloudLogin_ValidToken(t *testing.T) {
	store := makeTestStore(t)
	pub, priv := makeTestKeyPair(t)

	verifier := NewCloudLoginVerifier(store, pub)
	tok := freshToken()
	tokenBytes, sigB64 := signToken(t, priv, tok)

	info, err := verifier.Login(tokenBytes, sigB64)
	if err != nil {
		t.Fatalf("Login returned unexpected error: %v", err)
	}
	if info.Email != tok.Email {
		t.Errorf("email: got %q, want %q", info.Email, tok.Email)
	}
	if info.AccountID != tok.AccountID {
		t.Errorf("account_id: got %q, want %q", info.AccountID, tok.AccountID)
	}
	if info.SessionToken == "" {
		t.Error("session token is empty")
	}
}

func TestCloudLogin_ExpiredToken(t *testing.T) {
	store := makeTestStore(t)
	pub, priv := makeTestKeyPair(t)

	verifier := NewCloudLoginVerifier(store, pub)
	tok := freshToken()
	tok.ExpiresAt = time.Now().Add(-1 * time.Minute) // already expired
	tokenBytes, sigB64 := signToken(t, priv, tok)

	_, err := verifier.Login(tokenBytes, sigB64)
	if err != ErrTokenExpired {
		t.Errorf("expected ErrTokenExpired, got %v", err)
	}
}

func TestCloudLogin_TamperedToken(t *testing.T) {
	store := makeTestStore(t)
	pub, priv := makeTestKeyPair(t)

	verifier := NewCloudLoginVerifier(store, pub)
	tok := freshToken()
	tokenBytes, sigB64 := signToken(t, priv, tok)

	// Tamper: flip the first byte of the payload.
	tokenBytes[0] ^= 0xFF

	_, err := verifier.Login(tokenBytes, sigB64)
	if err != ErrBadSignature {
		t.Errorf("expected ErrBadSignature for tampered token, got %v", err)
	}
}

func TestCloudLogin_WrongKey(t *testing.T) {
	store := makeTestStore(t)
	_, priv := makeTestKeyPair(t)
	wrongPub, _ := makeTestKeyPair(t) // different key

	verifier := NewCloudLoginVerifier(store, wrongPub)
	tok := freshToken()
	tokenBytes, sigB64 := signToken(t, priv, tok)

	_, err := verifier.Login(tokenBytes, sigB64)
	if err != ErrBadSignature {
		t.Errorf("expected ErrBadSignature for wrong key, got %v", err)
	}
}

func TestCloudLogin_BadSignatureBase64(t *testing.T) {
	store := makeTestStore(t)
	pub, priv := makeTestKeyPair(t)

	verifier := NewCloudLoginVerifier(store, pub)
	tok := freshToken()
	tokenBytes, _ := signToken(t, priv, tok)

	_, err := verifier.Login(tokenBytes, "!!!not-base64!!!")
	if err == nil {
		t.Error("expected error for invalid base64 signature, got nil")
	}
}

func TestCloudLogin_NoBrokerPubkey(t *testing.T) {
	store := makeTestStore(t)

	// nil pubkey means not enrolled.
	verifier := NewCloudLoginVerifier(store, nil)
	tok := freshToken()
	b, _ := json.Marshal(tok)

	_, err := verifier.Login(b, "aabb")
	if err != ErrNoBrokerPubkey {
		t.Errorf("expected ErrNoBrokerPubkey, got %v", err)
	}
}

func TestCloudLogin_SessionReuse(t *testing.T) {
	store := makeTestStore(t)
	pub, priv := makeTestKeyPair(t)

	verifier := NewCloudLoginVerifier(store, pub)
	tok := freshToken()
	tokenBytes, sigB64 := signToken(t, priv, tok)

	info1, err := verifier.Login(tokenBytes, sigB64)
	if err != nil {
		t.Fatalf("first login: %v", err)
	}
	info2, err := verifier.Login(tokenBytes, sigB64)
	if err != nil {
		t.Fatalf("second login: %v", err)
	}
	// Sessions should be reused for the same device.
	if info1.SessionToken != info2.SessionToken {
		t.Errorf("expected session reuse but got different tokens: %q vs %q",
			info1.SessionToken, info2.SessionToken)
	}
}

// ─── LoadBrokerPubkey tests ───────────────────────────────────────────────────

func TestLoadBrokerPubkey_EnvFile(t *testing.T) {
	pub, _ := makeTestKeyPair(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "broker.pub")
	b64 := base64.StdEncoding.EncodeToString(pub)
	os.WriteFile(path, []byte(b64+"\n"), 0600)

	t.Setenv("VULOS_CLOUD_BROKER_PUBKEY", path)

	loaded, err := LoadBrokerPubkey()
	if err != nil {
		t.Fatalf("LoadBrokerPubkey: %v", err)
	}
	if !loaded.Equal(pub) {
		t.Error("loaded pubkey does not match original")
	}
}

func TestLoadBrokerPubkey_Missing(t *testing.T) {
	// Point to a non-existent file.
	t.Setenv("VULOS_CLOUD_BROKER_PUBKEY", "/tmp/vulos-test-nonexistent-broker.pub")

	_, err := LoadBrokerPubkey()
	if err != ErrNoBrokerPubkey {
		t.Errorf("expected ErrNoBrokerPubkey, got %v", err)
	}
}

func TestWriteAndLoadBrokerPubkey(t *testing.T) {
	pub, _ := makeTestKeyPair(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "broker.pub")

	t.Setenv("VULOS_CLOUD_BROKER_PUBKEY", path)

	if err := WriteBrokerPubkey(pub); err != nil {
		t.Fatalf("WriteBrokerPubkey: %v", err)
	}

	loaded, err := LoadBrokerPubkey()
	if err != nil {
		t.Fatalf("LoadBrokerPubkey after write: %v", err)
	}
	if !loaded.Equal(pub) {
		t.Error("round-trip: loaded pubkey does not match original")
	}
}
