package oauthprovider

// wave51_crypto_test.go — coverage-driven SECURITY pass on the CP OAuth
// provider's CRYPTO paths (flagged still-light in wave-40). These tests assert
// real crypto CORRECTNESS and fail-closed behaviour rather than plumbing:
//
//   - decryptKEK fails closed on the wrong / short / garbage KEK and on AEAD
//     tamper (nonce, ciphertext, and tag bit-flips) — never yields plaintext.
//   - parseSigningKey rejects malformed PEM and a non-RSA (EC) private key.
//   - a signed id_token verifies against the *published JWKS public key* and
//     carries the correct iss/aud/sub/nonce/exp claims.
//   - ALG-CONFUSION: an `alg:none` token and an HS256-MAC'd token (using the
//     RSA modulus bytes as the HMAC secret, the classic RS256→HS256 confusion
//     attack) must NOT verify against the RS256 public key.
//   - a tampered (payload-swapped) RS256 token fails verification.
//   - the JWKS / PublicJWK output NEVER leaks the RSA private key material.
//
// These respect the wave-37 TestMain VULOS_ENV=local pattern (see
// zz_testmain_test.go); tests needing prod set VULOS_ENV=prod via t.Setenv.

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"strings"
	"testing"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// decryptKEK — fail-closed on wrong/short/garbage key and AEAD tamper
// ─────────────────────────────────────────────────────────────────────────────

// TestDecryptKEK_WrongKeyFailsClosed verifies that ciphertext sealed under one
// KEK cannot be opened under a different (but valid 32-byte) KEK: GCM auth must
// reject it and NO plaintext may leak.
func TestDecryptKEK_WrongKeyFailsClosed(t *testing.T) {
	kek1, _ := testKEK32(1)
	kek2, _ := testKEK32(99) // different key material

	secret := "-----BEGIN PRIVATE KEY-----\nsentinel\n-----END PRIVATE KEY-----"
	enc, err := encryptKEK(secret, kek1)
	if err != nil {
		t.Fatalf("encryptKEK: %v", err)
	}

	plain, err := decryptKEK(enc, kek2)
	if err == nil {
		t.Fatalf("decryptKEK opened ciphertext under the WRONG key (plaintext leak): %q", plain)
	}
	if plain != "" {
		t.Fatalf("decryptKEK returned non-empty plaintext on failure: %q", plain)
	}
}

// TestDecryptKEK_ShortKeyFailsClosed verifies a short (non-32-byte) KEK cannot
// produce plaintext: aes.NewCipher rejects it and decryptKEK fails closed.
func TestDecryptKEK_ShortKeyFailsClosed(t *testing.T) {
	kek, _ := testKEK32(3)
	enc, err := encryptKEK("topsecret", kek)
	if err != nil {
		t.Fatalf("encryptKEK: %v", err)
	}

	for _, n := range []int{0, 1, 15, 16, 31} { // every non-32 length must be rejected
		plain, derr := decryptKEK(enc, kek[:n])
		if derr == nil {
			t.Fatalf("decryptKEK accepted a %d-byte KEK (must require 32): plaintext=%q", n, plain)
		}
		if plain != "" {
			t.Fatalf("decryptKEK leaked plaintext with %d-byte KEK: %q", n, plain)
		}
	}
}

// TestDecryptKEK_GarbageInput verifies non-base64 and too-short ciphertext are
// rejected without a panic and without plaintext.
func TestDecryptKEK_GarbageInput(t *testing.T) {
	kek, _ := testKEK32(5)

	// Not valid base64.
	if plain, err := decryptKEK("!!!not-base64!!!", kek); err == nil || plain != "" {
		t.Fatalf("decryptKEK accepted non-base64 input: plain=%q err=%v", plain, err)
	}
	// Valid base64 but shorter than the GCM nonce → "ciphertext too short".
	short := base64.StdEncoding.EncodeToString([]byte{0x01, 0x02, 0x03})
	if plain, err := decryptKEK(short, kek); err == nil || plain != "" {
		t.Fatalf("decryptKEK accepted too-short ciphertext: plain=%q err=%v", plain, err)
	}
}

// TestDecryptKEK_AEADTamperFailsClosed flips each byte of the sealed blob
// (nonce, ciphertext body, and GCM tag) and asserts every mutation is rejected
// by the AEAD — no plaintext ever emerges from a tampered blob.
func TestDecryptKEK_AEADTamperFailsClosed(t *testing.T) {
	kek, _ := testKEK32(11)
	enc, err := encryptKEK("crown-jewel-signing-key", kek)
	if err != nil {
		t.Fatalf("encryptKEK: %v", err)
	}
	raw, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		t.Fatalf("decode enc: %v", err)
	}

	// Flip the low bit of each byte in turn (nonce, ciphertext, and auth tag).
	for i := range raw {
		mutated := make([]byte, len(raw))
		copy(mutated, raw)
		mutated[i] ^= 0x01
		tampered := base64.StdEncoding.EncodeToString(mutated)

		plain, derr := decryptKEK(tampered, kek)
		if derr == nil {
			t.Fatalf("decryptKEK accepted tampered blob (byte %d flipped): plaintext=%q", i, plain)
		}
		if plain != "" {
			t.Fatalf("decryptKEK leaked plaintext on tamper at byte %d: %q", i, plain)
		}
	}

	// Sanity: the untampered blob still opens under the correct key.
	if got, derr := decryptKEK(enc, kek); derr != nil || got != "crown-jewel-signing-key" {
		t.Fatalf("untampered decrypt failed: got=%q err=%v", got, derr)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// parseSigningKey — reject malformed / wrong-type keys
// ─────────────────────────────────────────────────────────────────────────────

// TestParseSigningKey_RejectsMalformedPEM verifies non-PEM and truncated-PEM
// inputs are rejected rather than yielding a usable signing key.
func TestParseSigningKey_RejectsMalformedPEM(t *testing.T) {
	cases := map[string]string{
		"empty":           "",
		"not-pem":         "this is not a pem block at all",
		"header-only":     "-----BEGIN PRIVATE KEY-----\n-----END PRIVATE KEY-----",
		"garbage-b64-der": "-----BEGIN PRIVATE KEY-----\nZ2FyYmFnZQ==\n-----END PRIVATE KEY-----",
	}
	for name, pemStr := range cases {
		if k, err := parseSigningKey("kid-x", pemStr); err == nil {
			t.Fatalf("[%s] parseSigningKey accepted malformed PEM (key=%v)", name, k)
		}
	}
}

// TestParseSigningKey_RejectsNonRSA verifies a well-formed PKCS#8 key that is
// NOT RSA (an EC P-256 key) is rejected — the provider only signs RS256, so a
// wrong-type key must never be loaded as the signing key.
func TestParseSigningKey_RejectsNonRSA(t *testing.T) {
	ecKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("ecdsa gen: %v", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(ecKey)
	if err != nil {
		t.Fatalf("marshal ec pkcs8: %v", err)
	}
	ecPEM := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))

	if k, err := parseSigningKey("kid-ec", ecPEM); err == nil {
		t.Fatalf("parseSigningKey accepted a non-RSA (EC) key: %v", k)
	} else if !strings.Contains(err.Error(), "not RSA") {
		t.Fatalf("expected 'not RSA' rejection, got: %v", err)
	}
}

// TestParseSigningKey_RoundTrips confirms a legitimately generated key parses
// back to the same modulus (positive control for the rejection tests above).
func TestParseSigningKey_RoundTrips(t *testing.T) {
	k, err := generateSigningKey()
	if err != nil {
		t.Fatalf("generateSigningKey: %v", err)
	}
	priv, _, err := k.encodePEM()
	if err != nil {
		t.Fatalf("encodePEM: %v", err)
	}
	got, err := parseSigningKey(k.KID, priv)
	if err != nil {
		t.Fatalf("parseSigningKey: %v", err)
	}
	if got.KID != k.KID || got.Private.N.Cmp(k.Private.N) != 0 {
		t.Fatalf("round-trip mismatch: kid %s/%s", got.KID, k.KID)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// id_token signing correctness + JWKS verification
// ─────────────────────────────────────────────────────────────────────────────

// TestSignIDToken_VerifiesAgainstPublishedJWKS signs an id_token, publishes the
// key via PublicJWK/JWKS, reconstructs the public key from the JWK, and asserts
// the signature verifies and every security-relevant claim is correct.
func TestSignIDToken_VerifiesAgainstPublishedJWKS(t *testing.T) {
	k, err := generateSigningKey()
	if err != nil {
		t.Fatalf("generateSigningKey: %v", err)
	}
	exp := time.Now().Add(time.Hour).Unix()
	claims := IDTokenClaims{
		Issuer:   "https://vulos.test",
		Subject:  "sub-abc",
		Audience: "client-xyz",
		Expiry:   exp,
		IssuedAt: time.Now().Unix(),
		Nonce:    "nonce-123",
	}
	tok, err := k.SignIDToken(claims)
	if err != nil {
		t.Fatalf("SignIDToken: %v", err)
	}

	// The header must advertise RS256 + the key's kid (so RPs pick the right JWK).
	hdr := decodeJWTHeader(t, tok)
	if hdr["alg"] != "RS256" || hdr["kid"] != k.KID || hdr["typ"] != "JWT" {
		t.Fatalf("bad id_token header: %+v", hdr)
	}

	// Reconstruct the RSA public key from the *published* JWK (as an RP would).
	jwk := k.PublicJWK()
	pub, err := jwk.RSAPublicKey()
	if err != nil {
		t.Fatalf("RSAPublicKey from JWK: %v", err)
	}
	if pub.N.Cmp(k.Private.N) != 0 || pub.E != k.Private.E {
		t.Fatalf("JWK-reconstructed public key does not match signing key")
	}

	got, err := VerifyIDToken(tok, pub)
	if err != nil {
		t.Fatalf("VerifyIDToken against JWKS pubkey: %v", err)
	}
	if got["iss"] != "https://vulos.test" || got["sub"] != "sub-abc" ||
		got["aud"] != "client-xyz" || got["nonce"] != "nonce-123" {
		t.Fatalf("id_token claims incorrect: %+v", got)
	}
	if int64(got["exp"].(float64)) != exp {
		t.Fatalf("exp claim mismatch: got %v want %d", got["exp"], exp)
	}
}

// TestVerifyIDToken_RejectsExpired verifies an already-expired token is rejected
// even though its signature is valid.
func TestVerifyIDToken_RejectsExpired(t *testing.T) {
	k, _ := generateSigningKey()
	tok, err := k.SignIDToken(IDTokenClaims{
		Issuer:  "https://vulos.test",
		Subject: "s",
		Expiry:  time.Now().Add(-time.Minute).Unix(), // already expired
	})
	if err != nil {
		t.Fatalf("SignIDToken: %v", err)
	}
	if _, err := VerifyIDToken(tok, &k.Private.PublicKey); err == nil {
		t.Fatal("VerifyIDToken accepted an expired token")
	}
}

// TestVerifyIDToken_RejectsTampered flips the payload of a validly-signed token
// and asserts the RS256 signature no longer verifies.
func TestVerifyIDToken_RejectsTampered(t *testing.T) {
	k, _ := generateSigningKey()
	tok, _ := k.SignIDToken(IDTokenClaims{
		Issuer: "https://vulos.test", Subject: "honest", Audience: "aud",
		Expiry: time.Now().Add(time.Hour).Unix(),
	})
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("unexpected token shape")
	}
	// Swap the payload for an attacker-chosen one (elevate subject) but keep the
	// original signature — must fail.
	forgedClaims, _ := json.Marshal(map[string]any{
		"iss": "https://vulos.test", "sub": "attacker", "aud": "aud",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	tampered := parts[0] + "." + base64.RawURLEncoding.EncodeToString(forgedClaims) + "." + parts[2]
	if _, err := VerifyIDToken(tampered, &k.Private.PublicKey); err == nil {
		t.Fatal("VerifyIDToken accepted a payload-tampered token (signature bypass)")
	}
}

// TestVerifyIDToken_RejectsMalformed covers the structural error branches: wrong
// segment count and a non-base64 signature.
func TestVerifyIDToken_RejectsMalformed(t *testing.T) {
	k, _ := generateSigningKey()
	pub := &k.Private.PublicKey

	if _, err := VerifyIDToken("only.two", pub); err == nil {
		t.Fatal("accepted a 2-segment token")
	}
	if _, err := VerifyIDToken("a.b.c.d", pub); err == nil {
		t.Fatal("accepted a 4-segment token")
	}
	// Valid header/payload but a signature that is not valid base64url.
	valid, _ := k.SignIDToken(IDTokenClaims{Subject: "s", Expiry: time.Now().Add(time.Hour).Unix()})
	p := strings.Split(valid, ".")
	if _, err := VerifyIDToken(p[0]+"."+p[1]+".@@@notb64@@@", pub); err == nil {
		t.Fatal("accepted a non-base64 signature")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// ALG-CONFUSION — the crown-jewel security property
// ─────────────────────────────────────────────────────────────────────────────

// TestAlgConfusion_NoneNotAccepted forges an `alg:none` token (empty signature)
// with attacker-controlled claims and asserts the RS256 verifier rejects it. A
// verifier that keyed off the token header's `alg` would accept this; ours
// enforces RS256 by always running rsa.VerifyPKCS1v15.
func TestAlgConfusion_NoneNotAccepted(t *testing.T) {
	k, _ := generateSigningKey()

	hdr, _ := json.Marshal(map[string]string{"alg": "none", "typ": "JWT", "kid": k.KID})
	claims, _ := json.Marshal(map[string]any{
		"iss": "https://vulos.test", "sub": "attacker", "aud": "victim-client",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	// alg:none → empty signature segment.
	forged := b64url(hdr) + "." + b64url(claims) + "."

	if _, err := VerifyIDToken(forged, &k.Private.PublicKey); err == nil {
		t.Fatal("SECURITY: alg:none token verified against the RS256 key")
	}
}

// TestAlgConfusion_HS256NotAccepted mounts the classic RS256→HS256 confusion
// attack: the attacker knows only the PUBLIC key (its modulus bytes, published
// in the JWKS) and MACs a forged token with HMAC-SHA256 using that public
// material as the secret. A verifier that treats the RSA public key as an HMAC
// secret when the header says HS256 would be fooled. Ours always verifies as
// RS256, so the HMAC bytes are interpreted as an RSA signature and rejected.
func TestAlgConfusion_HS256NotAccepted(t *testing.T) {
	k, _ := generateSigningKey()
	pub := &k.Private.PublicKey

	hdr, _ := json.Marshal(map[string]string{"alg": "HS256", "typ": "JWT", "kid": k.KID})
	claims, _ := json.Marshal(map[string]any{
		"iss": "https://vulos.test", "sub": "attacker", "aud": "victim-client",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	signingInput := b64url(hdr) + "." + b64url(claims)

	// HMAC the signing input using the PUBLIC key modulus as the shared secret —
	// the exact bytes an attacker would derive from the JWKS `n` value.
	mac := hmac.New(sha256.New, pub.N.Bytes())
	mac.Write([]byte(signingInput))
	forged := signingInput + "." + b64url(mac.Sum(nil))

	if _, err := VerifyIDToken(forged, pub); err == nil {
		t.Fatal("SECURITY: HS256-forged token (RS256→HS256 confusion) verified against the RSA key")
	}
}

// TestAlgConfusion_WrongKeyRejected confirms a token signed by a DIFFERENT RSA
// key does not verify against our key (baseline: signatures are key-bound).
func TestAlgConfusion_WrongKeyRejected(t *testing.T) {
	signer, _ := generateSigningKey()
	other, _ := rsa.GenerateKey(rand.Reader, 2048)

	tok, _ := signer.SignIDToken(IDTokenClaims{
		Subject: "s", Expiry: time.Now().Add(time.Hour).Unix(),
	})
	if _, err := VerifyIDToken(tok, &other.PublicKey); err == nil {
		t.Fatal("token verified against an unrelated public key")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// JWKS must never leak private key material
// ─────────────────────────────────────────────────────────────────────────────

// TestPublicJWK_NeverLeaksPrivateKey serializes the JWK and asserts it contains
// only public RSA parameters (kty/use/alg/kid/n/e) and NONE of the private
// components (d, p, q, dp, dq, qi). A JWKS endpoint leaking these would hand out
// the id_token forging key.
func TestPublicJWK_NeverLeaksPrivateKey(t *testing.T) {
	k, err := generateSigningKey()
	if err != nil {
		t.Fatalf("generateSigningKey: %v", err)
	}
	jwk := k.PublicJWK()

	blob, err := json.Marshal(jwk)
	if err != nil {
		t.Fatalf("marshal JWK: %v", err)
	}

	// No private JWK field names may appear.
	var m map[string]any
	if err := json.Unmarshal(blob, &m); err != nil {
		t.Fatalf("unmarshal JWK: %v", err)
	}
	for _, priv := range []string{"d", "p", "q", "dp", "dq", "qi"} {
		if _, ok := m[priv]; ok {
			t.Fatalf("SECURITY: JWK leaked private field %q: %s", priv, blob)
		}
	}

	// The private-key CRT primes must not appear as byte substrings either.
	crtParams := [][]byte{
		k.Private.D.Bytes(),
		k.Private.Primes[0].Bytes(),
		k.Private.Primes[1].Bytes(),
	}
	for i, secret := range crtParams {
		if len(secret) > 0 && strings.Contains(string(blob), b64url(secret)) {
			t.Fatalf("SECURITY: JWK JSON contains private component #%d", i)
		}
	}

	// Positive control: the public modulus + exponent ARE present.
	pub := k.Private.PublicKey
	if jwk.N != b64url(pub.N.Bytes()) || jwk.Kid != k.KID || jwk.Use != "sig" {
		t.Fatalf("JWK missing expected public fields: %+v", jwk)
	}
}

// TestAllPublicJWKs_NoPrivateMaterial drives the store-level JWKS path
// (parseSigningKey → PublicJWK from the encrypted-at-rest key) and asserts the
// exposed JWKS carries no private material, matching the signing key's modulus.
func TestAllPublicJWKs_NoPrivateMaterial(t *testing.T) {
	t.Setenv("VULOS_DEV", "true")
	st := newTestStore(t)
	ctx := t.Context()

	k, err := st.LoadOrCreateSigningKey(ctx)
	if err != nil {
		t.Fatalf("LoadOrCreateSigningKey: %v", err)
	}
	jwks, err := st.AllPublicJWKs(ctx)
	if err != nil || len(jwks) != 1 {
		t.Fatalf("AllPublicJWKs: err=%v len=%d", err, len(jwks))
	}
	blob, _ := json.Marshal(jwks[0])
	var m map[string]any
	_ = json.Unmarshal(blob, &m)
	for _, priv := range []string{"d", "p", "q", "dp", "dq", "qi"} {
		if _, ok := m[priv]; ok {
			t.Fatalf("SECURITY: store JWKS leaked private field %q", priv)
		}
	}
	// The published key must be usable to verify a freshly-signed token.
	pub, err := jwks[0].RSAPublicKey()
	if err != nil {
		t.Fatalf("RSAPublicKey: %v", err)
	}
	if pub.N.Cmp(k.Private.N) != 0 {
		t.Fatalf("published JWK modulus != signing key modulus")
	}
	tok, _ := k.SignIDToken(IDTokenClaims{Subject: "s", Expiry: time.Now().Add(time.Hour).Unix()})
	if _, err := VerifyIDToken(tok, pub); err != nil {
		t.Fatalf("token signed by store key does not verify against published JWK: %v", err)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// helpers
// ─────────────────────────────────────────────────────────────────────────────

func decodeJWTHeader(t *testing.T, tok string) map[string]any {
	t.Helper()
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("token not 3 segments")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatalf("decode header: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal header: %v", err)
	}
	return m
}
