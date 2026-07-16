package profile

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// MemSigner conformance tests
// ─────────────────────────────────────────────────────────────────────────────

func TestMemSigner_SignVerifyRoundtrip(t *testing.T) {
	t.Parallel()
	s := NewMemSigner()

	payload := []byte(`{"account_id":"ACC1","ulid":"ULID1","profile_name":"alice","expires_at":"2026-01-01T00:00:00Z","nonce":"abc"}`)

	sig, err := s.Sign(payload)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if len(sig) != ed25519.SignatureSize {
		t.Fatalf("signature length: got %d, want %d", len(sig), ed25519.SignatureSize)
	}

	pub, err := s.PublicKeyBytes()
	if err != nil {
		t.Fatalf("PublicKeyBytes: %v", err)
	}
	if len(pub) != ed25519.PublicKeySize {
		t.Fatalf("public key length: got %d, want %d", len(pub), ed25519.PublicKeySize)
	}

	// Verify signature using raw ed25519.Verify.
	if !ed25519.Verify(pub, payload, sig) {
		t.Fatal("signature did not verify")
	}
}

func TestMemSigner_TamperRejection(t *testing.T) {
	t.Parallel()
	s := NewMemSigner()

	payload := []byte(`{"account_id":"ACC1","ulid":"ULID1","profile_name":"alice"}`)
	sig, err := s.Sign(payload)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}

	pub, err := s.PublicKeyBytes()
	if err != nil {
		t.Fatalf("PublicKeyBytes: %v", err)
	}

	// Tamper with the payload.
	tampered := []byte(`{"account_id":"EVIL","ulid":"ULID1","profile_name":"alice"}`)
	if ed25519.Verify(pub, tampered, sig) {
		t.Fatal("tampered payload should not verify")
	}
}

func TestMemSigner_TwoInstancesIndependent(t *testing.T) {
	t.Parallel()
	s1 := NewMemSigner()
	s2 := NewMemSigner()

	payload := []byte("test payload")
	sig1, err := s1.Sign(payload)
	if err != nil {
		t.Fatalf("Sign s1: %v", err)
	}

	pub2, err := s2.PublicKeyBytes()
	if err != nil {
		t.Fatalf("PublicKeyBytes s2: %v", err)
	}

	// Signature from s1 must not verify under s2's public key.
	if ed25519.Verify(pub2, payload, sig1) {
		t.Fatal("s1 signature should not verify under s2 key")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// EnvSigner tests
// ─────────────────────────────────────────────────────────────────────────────

func TestEnvSigner_UnconfiguredReturnsError(t *testing.T) {
	// Cannot use t.Parallel() with t.Setenv.
	// PROFILE_LOGIN_KEY_BASE64 is not set in the test environment.
	t.Setenv("PROFILE_LOGIN_KEY_BASE64", "")
	s := &EnvSigner{}
	_, err := s.Sign([]byte("payload"))
	if !errors.Is(err, ErrSignerUnconfigured) {
		t.Fatalf("Sign without env var: got %v, want ErrSignerUnconfigured", err)
	}
	_, err = s.PublicKeyBytes()
	if !errors.Is(err, ErrSignerUnconfigured) {
		t.Fatalf("PublicKeyBytes without env var: got %v, want ErrSignerUnconfigured", err)
	}
}

func TestEnvSigner_WithSeedKey(t *testing.T) {
	// Generate a 32-byte seed.
	seed := make([]byte, ed25519.SeedSize)
	if _, err := rand.Read(seed); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}
	encoded := base64.StdEncoding.EncodeToString(seed)

	t.Setenv("PROFILE_LOGIN_KEY_BASE64", encoded)

	s, err := NewEnvSigner()
	if err != nil {
		t.Fatalf("NewEnvSigner: %v", err)
	}

	payload := []byte("test payload for env signer")
	sig, err := s.Sign(payload)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}

	pub, err := s.PublicKeyBytes()
	if err != nil {
		t.Fatalf("PublicKeyBytes: %v", err)
	}

	if !ed25519.Verify(pub, payload, sig) {
		t.Fatal("EnvSigner signature did not verify")
	}
}

func TestEnvSigner_SeedAndFullKeyConsistent(t *testing.T) {
	// Generate a seed, derive the full private key, check both encodings
	// produce the same public key.
	seed := make([]byte, ed25519.SeedSize)
	if _, err := rand.Read(seed); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}
	priv := ed25519.NewKeyFromSeed(seed)

	// Test with full 64-byte key.
	t.Setenv("PROFILE_LOGIN_KEY_BASE64", base64.StdEncoding.EncodeToString(priv))

	s, err := NewEnvSigner()
	if err != nil {
		t.Fatalf("NewEnvSigner (full key): %v", err)
	}

	pub, err := s.PublicKeyBytes()
	if err != nil {
		t.Fatalf("PublicKeyBytes: %v", err)
	}

	expectedPub := priv.Public().(ed25519.PublicKey)
	for i := range expectedPub {
		if pub[i] != expectedPub[i] {
			t.Fatalf("public key mismatch at byte %d", i)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// MemStore conformance tests
// ─────────────────────────────────────────────────────────────────────────────

func TestMemStore_NoActiveKeyInitially(t *testing.T) {
	t.Parallel()
	st := NewMemStore()
	_, err := st.ActiveKey(context.Background())
	if !errors.Is(err, ErrNoActiveKey) {
		t.Fatalf("empty store ActiveKey: got %v, want ErrNoActiveKey", err)
	}
}

func TestMemStore_NoPreviousKeyInitially(t *testing.T) {
	t.Parallel()
	st := NewMemStore()
	_, err := st.PreviousKey(context.Background())
	if !errors.Is(err, ErrKeyNotFound) {
		t.Fatalf("empty store PreviousKey: got %v, want ErrKeyNotFound", err)
	}
}

func TestMemStore_RotateKey_SingleRotation(t *testing.T) {
	t.Parallel()
	st := NewMemStore()

	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}

	if err := st.RotateKey(context.Background(), pub); err != nil {
		t.Fatalf("RotateKey: %v", err)
	}

	active, err := st.ActiveKey(context.Background())
	if err != nil {
		t.Fatalf("ActiveKey after rotate: %v", err)
	}
	if active.ID == "" {
		t.Fatal("active key ID should not be empty")
	}
	if len(active.PublicKey) != ed25519.PublicKeySize {
		t.Fatalf("active public key size: got %d, want %d", len(active.PublicKey), ed25519.PublicKeySize)
	}
	for i := range pub {
		if active.PublicKey[i] != pub[i] {
			t.Fatalf("active key mismatch at byte %d", i)
		}
	}

	// No previous key after first rotation.
	_, err = st.PreviousKey(context.Background())
	if !errors.Is(err, ErrKeyNotFound) {
		t.Fatalf("PreviousKey after first rotate: got %v, want ErrKeyNotFound", err)
	}
}

func TestMemStore_RotateKey_OverlapWindow(t *testing.T) {
	t.Parallel()
	st := NewMemStore()

	pub1, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey 1: %v", err)
	}
	pub2, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey 2: %v", err)
	}

	// First rotation.
	if err := st.RotateKey(context.Background(), pub1); err != nil {
		t.Fatalf("RotateKey 1: %v", err)
	}

	// Second rotation: pub1 becomes previous, pub2 becomes active.
	if err := st.RotateKey(context.Background(), pub2); err != nil {
		t.Fatalf("RotateKey 2: %v", err)
	}

	active, err := st.ActiveKey(context.Background())
	if err != nil {
		t.Fatalf("ActiveKey: %v", err)
	}
	for i := range pub2 {
		if active.PublicKey[i] != pub2[i] {
			t.Fatalf("active key should be pub2, mismatch at byte %d", i)
		}
	}

	prev, err := st.PreviousKey(context.Background())
	if err != nil {
		t.Fatalf("PreviousKey: %v", err)
	}
	for i := range pub1 {
		if prev.PublicKey[i] != pub1[i] {
			t.Fatalf("previous key should be pub1, mismatch at byte %d", i)
		}
	}
}

func TestMemStore_RotateKey_OlderPreviousEvicted(t *testing.T) {
	t.Parallel()
	st := NewMemStore()

	pub1, _, _ := ed25519.GenerateKey(rand.Reader)
	pub2, _, _ := ed25519.GenerateKey(rand.Reader)
	pub3, _, _ := ed25519.GenerateKey(rand.Reader)

	_ = st.RotateKey(context.Background(), pub1)
	_ = st.RotateKey(context.Background(), pub2)
	_ = st.RotateKey(context.Background(), pub3)

	// Active should be pub3, previous should be pub2; pub1 is evicted.
	active, err := st.ActiveKey(context.Background())
	if err != nil {
		t.Fatalf("ActiveKey: %v", err)
	}
	for i := range pub3 {
		if active.PublicKey[i] != pub3[i] {
			t.Fatalf("active key should be pub3 at byte %d", i)
		}
	}

	prev, err := st.PreviousKey(context.Background())
	if err != nil {
		t.Fatalf("PreviousKey: %v", err)
	}
	for i := range pub2 {
		if prev.PublicKey[i] != pub2[i] {
			t.Fatalf("previous key should be pub2 at byte %d", i)
		}
	}
}

func TestMemStore_ConcurrencySafe(t *testing.T) {
	t.Parallel()
	st := NewMemStore()
	ctx := context.Background()

	done := make(chan struct{}, 10)
	for i := 0; i < 10; i++ {
		go func() {
			pub, _, _ := ed25519.GenerateKey(rand.Reader)
			_ = st.RotateKey(ctx, pub)
			_, _ = st.ActiveKey(ctx)
			_, _ = st.PreviousKey(ctx)
			done <- struct{}{}
		}()
	}
	for i := 0; i < 10; i++ {
		<-done
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// LoginToken tests
// ─────────────────────────────────────────────────────────────────────────────

func TestLoginToken_IsExpired(t *testing.T) {
	t.Parallel()

	tok := LoginToken{
		ExpiresAt: time.Now().UTC().Add(-1 * time.Second),
	}
	if !tok.IsExpired(time.Now().UTC()) {
		t.Fatal("past token should be expired")
	}

	tok2 := LoginToken{
		ExpiresAt: time.Now().UTC().Add(60 * time.Second),
	}
	if tok2.IsExpired(time.Now().UTC()) {
		t.Fatal("future token should not be expired")
	}
}

func TestLoginToken_TTLConstraint(t *testing.T) {
	t.Parallel()
	// Verify that a token with 120s TTL respects the contract maximum.
	now := time.Now().UTC()
	maxTTL := 120 * time.Second
	tok := LoginToken{
		ExpiresAt: now.Add(maxTTL),
	}
	if tok.IsExpired(now) {
		t.Fatal("token with 120s TTL should not be expired at issuance")
	}
	// At exactly TTL + 1ns it should be expired.
	if !tok.IsExpired(now.Add(maxTTL + 1)) {
		t.Fatal("token should be expired 1ns past TTL")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// NewNonce tests
// ─────────────────────────────────────────────────────────────────────────────

func TestNewNonce_UniqueAndDecodable(t *testing.T) {
	t.Parallel()
	n1, err := NewNonce()
	if err != nil {
		t.Fatalf("NewNonce: %v", err)
	}
	n2, err := NewNonce()
	if err != nil {
		t.Fatalf("NewNonce: %v", err)
	}
	if n1 == n2 {
		t.Fatal("two nonces should not be equal (astronomically unlikely)")
	}
	b, err := base64.RawURLEncoding.DecodeString(n1)
	if err != nil {
		t.Fatalf("decode nonce: %v", err)
	}
	if len(b) != 16 {
		t.Fatalf("nonce decoded length: got %d, want 16", len(b))
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Issue→Verify roundtrip (MemSigner + LoginToken)
// ─────────────────────────────────────────────────────────────────────────────

func TestIssueVerifyRoundtrip(t *testing.T) {
	t.Parallel()

	s := NewMemSigner()

	nonce, err := NewNonce()
	if err != nil {
		t.Fatalf("NewNonce: %v", err)
	}

	tok := LoginToken{
		AccountID:   "ACC_TEST",
		ULID:        "01ARZ3NDEKTSV4RRFFQ69G5FAV",
		ProfileName: "alice",
		ExpiresAt:   time.Now().UTC().Add(120 * time.Second),
		Nonce:       nonce,
	}

	payload, err := json.Marshal(tok)
	if err != nil {
		t.Fatalf("marshal token: %v", err)
	}

	sig, err := s.Sign(payload)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}

	pub, err := s.PublicKeyBytes()
	if err != nil {
		t.Fatalf("PublicKeyBytes: %v", err)
	}

	// Verify as the OS would: verify sig over payload using the broker pubkey.
	if !ed25519.Verify(pub, payload, sig) {
		t.Fatal("roundtrip: signature did not verify")
	}
}

func TestIssueVerifyRoundtrip_TamperRejected(t *testing.T) {
	t.Parallel()

	s := NewMemSigner()

	nonce, _ := NewNonce()
	tok := LoginToken{
		AccountID:   "ACC_TEST",
		ULID:        "01ARZ3NDEKTSV4RRFFQ69G5FAV",
		ProfileName: "alice",
		ExpiresAt:   time.Now().UTC().Add(120 * time.Second),
		Nonce:       nonce,
	}

	payload, _ := json.Marshal(tok)
	sig, _ := s.Sign(payload)
	pub, _ := s.PublicKeyBytes()

	// Change the profile_name in the payload.
	tamperedTok := LoginToken{
		AccountID:   tok.AccountID,
		ULID:        tok.ULID,
		ProfileName: "evil",
		ExpiresAt:   tok.ExpiresAt,
		Nonce:       tok.Nonce,
	}
	tamperedPayload, _ := json.Marshal(tamperedTok)

	if ed25519.Verify(pub, tamperedPayload, sig) {
		t.Fatal("tampered payload should not verify")
	}
}

func TestIssueVerifyRoundtrip_ExpiredToken(t *testing.T) {
	t.Parallel()

	s := NewMemSigner()
	nonce, _ := NewNonce()

	tok := LoginToken{
		AccountID:   "ACC_TEST",
		ULID:        "01ARZ3NDEKTSV4RRFFQ69G5FAV",
		ProfileName: "alice",
		ExpiresAt:   time.Now().UTC().Add(-1 * time.Second), // already expired
		Nonce:       nonce,
	}

	payload, _ := json.Marshal(tok)
	sig, _ := s.Sign(payload)
	pub, _ := s.PublicKeyBytes()

	// Signature still verifies (cloud signed it) but the OS checks expires_at.
	if !ed25519.Verify(pub, payload, sig) {
		t.Fatal("valid signature should still verify even on expired token")
	}
	// The OS-side expiry check:
	if !tok.IsExpired(time.Now().UTC()) {
		t.Fatal("expired token should report IsExpired=true")
	}
}

// UNIFIED-SIGNIN: the additive identity claims survive a JSON round-trip under
// the exact wire keys the OS verifier reads (email, name, issued_at,
// email_verified, jti), while the original contract fields stay unchanged.
func TestLoginToken_UnifiedSigninClaims_Roundtrip(t *testing.T) {
	t.Parallel()

	nonce, err := NewNonce()
	if err != nil {
		t.Fatalf("NewNonce: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	tok := LoginToken{
		AccountID:     "ACC_TEST",
		ULID:          "01ARZ3NDEKTSV4RRFFQ69G5FAV",
		ProfileName:   "alice",
		ExpiresAt:     now.Add(120 * time.Second),
		Nonce:         nonce,
		Email:         "alice@vulos.org",
		Name:          "alice",
		IssuedAt:      now,
		EmailVerified: true,
		JTI:           nonce,
	}

	payload, err := json.Marshal(tok)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	// Wire keys the OS verifier depends on must be present verbatim.
	var raw map[string]any
	if err := json.Unmarshal(payload, &raw); err != nil {
		t.Fatalf("unmarshal to map: %v", err)
	}
	for _, key := range []string{"account_id", "ulid", "profile_name", "expires_at", "nonce",
		"email", "name", "issued_at", "email_verified", "jti"} {
		if _, ok := raw[key]; !ok {
			t.Fatalf("wire payload missing key %q: %s", key, payload)
		}
	}

	var back LoginToken
	if err := json.Unmarshal(payload, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.Email != tok.Email || back.Name != tok.Name || back.JTI != tok.JTI ||
		back.EmailVerified != tok.EmailVerified || !back.IssuedAt.Equal(tok.IssuedAt) {
		t.Fatalf("round-trip mismatch: %+v vs %+v", back, tok)
	}
}

// UNIFIED-SIGNIN: a legacy payload WITHOUT the new keys still parses — the new
// fields are strictly additive.
func TestLoginToken_LegacyPayload_StillParses(t *testing.T) {
	t.Parallel()

	legacy := []byte(`{"account_id":"A","ulid":"01ARZ3NDEKTSV4RRFFQ69G5FAV","profile_name":"p","expires_at":"2030-01-01T00:00:00Z","nonce":"n"}`)
	var tok LoginToken
	if err := json.Unmarshal(legacy, &tok); err != nil {
		t.Fatalf("legacy payload should parse: %v", err)
	}
	if tok.AccountID != "A" || tok.Email != "" || tok.EmailVerified {
		t.Fatalf("legacy parse produced unexpected values: %+v", tok)
	}
}
