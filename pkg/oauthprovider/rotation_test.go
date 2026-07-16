package oauthprovider

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// issueIDToken runs a full authorize→exchange and returns the id_token.
func issueIDToken(t *testing.T, svc *Service, subject string) string {
	t.Helper()
	ctx := context.Background()
	verifier, challenge := pkcePair()
	c, secret, err := svc.RegisterClient(ctx, "owner-"+subject, "RotApp",
		[]string{"https://rot.example.com/cb"}, []string{ScopeOpenID}, false)
	if err != nil {
		t.Fatalf("RegisterClient: %v", err)
	}
	v, aerr := svc.ValidateAuthorize(ctx, AuthorizeRequest{
		ResponseType: "code", ClientID: c.ClientID, RedirectURI: "https://rot.example.com/cb",
		Scope: "openid", CodeChallenge: challenge, CodeChallengeMethod: "S256",
	})
	if aerr != nil {
		t.Fatalf("ValidateAuthorize: %v", aerr)
	}
	code, err := svc.IssueCode(ctx, v, subject)
	if err != nil {
		t.Fatalf("IssueCode: %v", err)
	}
	resp, err := svc.ExchangeCode(ctx, ExchangeCodeParams{
		Code: code, RedirectURI: "https://rot.example.com/cb", ClientID: c.ClientID,
		ClientSecret: secret, CodeVerifier: verifier,
	})
	if err != nil {
		t.Fatalf("ExchangeCode: %v", err)
	}
	if resp.IDToken == "" {
		t.Fatal("no id_token issued")
	}
	return resp.IDToken
}

// jwtKID returns the kid from a JWT header.
func jwtKID(t *testing.T, token string) string {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("malformed JWT")
	}
	hdrJSON, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatalf("decode header: %v", err)
	}
	var hdr map[string]any
	if err := json.Unmarshal(hdrJSON, &hdr); err != nil {
		t.Fatalf("unmarshal header: %v", err)
	}
	kid, _ := hdr["kid"].(string)
	return kid
}

// verifyWithJWKS verifies token against whichever published JWK matches its kid.
func verifyWithJWKS(t *testing.T, svc *Service, token string) bool {
	t.Helper()
	kid := jwtKID(t, token)
	jwks, err := svc.Store().AllPublicJWKs(context.Background())
	if err != nil {
		t.Fatalf("AllPublicJWKs: %v", err)
	}
	for _, jwk := range jwks {
		if jwk.Kid != kid {
			continue
		}
		pub, err := jwk.RSAPublicKey()
		if err != nil {
			t.Fatalf("RSAPublicKey: %v", err)
		}
		if _, err := VerifyIDToken(token, pub); err != nil {
			t.Fatalf("VerifyIDToken with JWKS key kid=%s: %v", kid, err)
		}
		return true
	}
	return false
}

// TestSigningKeyRotation proves the rotation contract:
//   - a token signed BEFORE rotation still verifies afterwards (old public key
//     stays in JWKS by its kid during the overlap window);
//   - new tokens are signed with the NEW kid;
//   - JWKS lists BOTH keys during the overlap;
//   - the rotated-in private key is KEK-wrapped at rest (never plaintext).
func TestSigningKeyRotation(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	oldKID := svc.SigningKey().KID

	// Token signed with the ORIGINAL key.
	oldTok := issueIDToken(t, svc, "subject-old")
	if got := jwtKID(t, oldTok); got != oldKID {
		t.Fatalf("pre-rotation token kid=%s, want %s", got, oldKID)
	}

	// Rotate.
	if err := svc.RotateSigningKey(ctx); err != nil {
		t.Fatalf("RotateSigningKey: %v", err)
	}
	newKID := svc.SigningKey().KID
	if newKID == oldKID || newKID == "" {
		t.Fatalf("rotation did not change active kid: old=%s new=%s", oldKID, newKID)
	}

	// New tokens use the NEW kid.
	newTok := issueIDToken(t, svc, "subject-new")
	if got := jwtKID(t, newTok); got != newKID {
		t.Fatalf("post-rotation token kid=%s, want new %s", got, newKID)
	}

	// JWKS lists BOTH keys.
	jwks, err := svc.Store().AllPublicJWKs(ctx)
	if err != nil {
		t.Fatalf("AllPublicJWKs: %v", err)
	}
	seen := map[string]bool{}
	for _, j := range jwks {
		seen[j.Kid] = true
	}
	if !seen[oldKID] || !seen[newKID] {
		t.Fatalf("JWKS missing a key: old(%s)=%v new(%s)=%v (all=%v)", oldKID, seen[oldKID], newKID, seen[newKID], seen)
	}

	// The OLD token still verifies against its (retired-but-published) key.
	if !verifyWithJWKS(t, svc, oldTok) {
		t.Fatalf("pre-rotation token no longer verifiable via JWKS (kid=%s)", oldKID)
	}
	// The NEW token verifies against the new key.
	if !verifyWithJWKS(t, svc, newTok) {
		t.Fatalf("post-rotation token not verifiable via JWKS (kid=%s)", newKID)
	}

	// The rotated-in private key must be KEK-wrapped at rest (not plaintext PEM).
	// newTestService runs under VULOS_DEV, which supplies the dev KEK, so
	// encodePrivForStore seals the key with AES-256-GCM.
	var stored string
	if err := svc.Store().db.QueryRowContext(ctx,
		svc.Store().db.Rebind(`SELECT private_pem FROM oauth_signing_keys WHERE kid = ?`), newKID).
		Scan(&stored); err != nil {
		t.Fatalf("read rotated private_pem: %v", err)
	}
	if isPlaintextPEM(stored) {
		t.Fatalf("rotated signing key is stored in PLAINTEXT (kid=%s) — must be KEK-wrapped", newKID)
	}
}

// TestRetiredKeyDropsFromJWKSAfterOverlap proves the overlap window is honoured:
// once a retired key's overlap has elapsed it is dropped from JWKS.
func TestRetiredKeyDropsFromJWKSAfterOverlap(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	// Overlap already in the past: the retired key must be pruned immediately.
	svc.SetRotationOverlap(-time.Second)

	oldKID := svc.SigningKey().KID
	if err := svc.RotateSigningKey(ctx); err != nil {
		t.Fatalf("RotateSigningKey: %v", err)
	}
	newKID := svc.SigningKey().KID

	jwks, err := svc.Store().AllPublicJWKs(ctx)
	if err != nil {
		t.Fatalf("AllPublicJWKs: %v", err)
	}
	seen := map[string]bool{}
	for _, j := range jwks {
		seen[j.Kid] = true
	}
	if seen[oldKID] {
		t.Fatalf("retired key kid=%s should have been dropped from JWKS after overlap", oldKID)
	}
	if !seen[newKID] {
		t.Fatalf("new active key kid=%s missing from JWKS", newKID)
	}
}
