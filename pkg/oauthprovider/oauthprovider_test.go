package oauthprovider

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/vul-os/vulos-management/pkg/cpdb"
)

// newTestStore opens an in-memory SQLite-backed Store for tests. The caller
// must set any KEK-related env (INTEGRATIONS_KEK / VULOS_DEV) BEFORE calling,
// since Open() runs loadKEK().
func newTestStore(t *testing.T) *Store {
	t.Helper()
	db, err := cpdb.OpenSQLiteDSN(":memory:")
	if err != nil {
		t.Fatalf("cpdb.OpenSQLiteDSN: %v", err)
	}
	st, err := Open(db)
	if err != nil {
		db.Close()
		t.Fatalf("oauthprovider.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// fakeUsers is a stub UserResolver for id_token / userinfo claim tests.
type fakeUsers struct {
	email    string
	verified bool
	err      error
}

func (f fakeUsers) ProfileByID(context.Context, string) (string, bool, error) {
	return f.email, f.verified, f.err
}

func newTestService(t *testing.T) *Service {
	t.Helper()
	// Dev path: no INTEGRATIONS_KEK needed (all-zeros dev KEK). Production now
	// fails closed without a KEK (see TestOpenStore_FailsClosedWithoutKEK).
	t.Setenv("VULOS_DEV", "true")
	st := newTestStore(t)
	svc, err := NewService(context.Background(), st, fakeUsers{email: "alice@vulos.test", verified: true}, "https://vulos.test")
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return svc
}

// pkcePair returns a (verifier, S256 challenge) pair.
func pkcePair() (verifier, challenge string) {
	verifier = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ012345" // 57 chars
	sum := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(sum[:])
	return verifier, challenge
}

func TestVerifyS256(t *testing.T) {
	verifier, challenge := pkcePair()
	if !VerifyS256(verifier, challenge) {
		t.Fatal("valid PKCE pair rejected")
	}
	if VerifyS256("wrong-verifier-but-long-enough-to-pass-the-length-check!!", challenge) {
		t.Fatal("wrong verifier accepted")
	}
	if VerifyS256("short", challenge) {
		t.Fatal("too-short verifier accepted")
	}
}

func TestRegisterClientSecretHashedAndScopesValidated(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	c, secret, err := svc.RegisterClient(ctx, "user-1", "My App",
		[]string{"https://app.example.com/cb"}, []string{ScopeOpenID, ScopeEmail}, false)
	if err != nil {
		t.Fatalf("RegisterClient: %v", err)
	}
	if secret == "" || !strings.HasPrefix(secret, "vcsk_") {
		t.Fatalf("expected plaintext secret, got %q", secret)
	}
	// Stored hash must NOT equal the plaintext and must verify.
	got, err := svc.Store().GetClient(ctx, c.ClientID)
	if err != nil {
		t.Fatalf("GetClient: %v", err)
	}
	if got.SecretHash == secret || got.SecretHash == "" {
		t.Fatal("secret stored in plaintext or missing")
	}
	if !verifyClientSecret(got, secret) {
		t.Fatal("stored secret does not verify")
	}
	if verifyClientSecret(got, "vcsk_wrong") {
		t.Fatal("wrong secret verified")
	}

	// Unknown scope is rejected.
	if _, _, err := svc.RegisterClient(ctx, "user-1", "Bad", []string{"https://x/cb"}, []string{"totally.bogus"}, false); err == nil {
		t.Fatal("expected unknown-scope rejection")
	}
	// Non-https redirect (non-loopback) rejected.
	if _, _, err := svc.RegisterClient(ctx, "user-1", "Bad", []string{"http://evil.example.com/cb"}, []string{ScopeOpenID}, false); err == nil {
		t.Fatal("expected http (non-loopback) redirect rejection")
	}
	// Loopback http allowed.
	if _, _, err := svc.RegisterClient(ctx, "user-1", "Local", []string{"http://localhost:3000/cb"}, []string{ScopeOpenID}, true); err != nil {
		t.Fatalf("loopback redirect should be allowed: %v", err)
	}
}

func TestAuthorizeValidation(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	_, verChallenge := pkcePair()
	c, _, _ := svc.RegisterClient(ctx, "user-1", "App",
		[]string{"https://app.example.com/cb"}, []string{ScopeOpenID, ScopeEmail}, false)

	base := AuthorizeRequest{
		ResponseType:        "code",
		ClientID:            c.ClientID,
		RedirectURI:         "https://app.example.com/cb",
		Scope:               "openid email",
		State:               "xyz",
		CodeChallenge:       verChallenge,
		CodeChallengeMethod: "S256",
	}
	if _, aerr := svc.ValidateAuthorize(ctx, base); aerr != nil {
		t.Fatalf("valid request rejected: %v", aerr)
	}

	// Unknown client → not redirectable.
	bad := base
	bad.ClientID = "vcid_nope"
	if _, aerr := svc.ValidateAuthorize(ctx, bad); aerr == nil || aerr.RedirectOK {
		t.Fatal("unknown client should be a non-redirectable error")
	}
	// redirect_uri mismatch → not redirectable.
	bad = base
	bad.RedirectURI = "https://app.example.com/evil"
	if _, aerr := svc.ValidateAuthorize(ctx, bad); aerr == nil || aerr.RedirectOK {
		t.Fatal("redirect mismatch should be a non-redirectable error")
	}
	// Missing PKCE → redirectable invalid_request.
	bad = base
	bad.CodeChallengeMethod = ""
	if _, aerr := svc.ValidateAuthorize(ctx, bad); aerr == nil || !aerr.RedirectOK {
		t.Fatal("missing PKCE should be a redirectable error")
	}
	// plain PKCE method rejected.
	bad = base
	bad.CodeChallengeMethod = "plain"
	if _, aerr := svc.ValidateAuthorize(ctx, bad); aerr == nil {
		t.Fatal("plain PKCE method should be rejected")
	}
	// Scope outside client allow-list.
	bad = base
	bad.Scope = "openid mail.send"
	if _, aerr := svc.ValidateAuthorize(ctx, bad); aerr == nil || aerr.Code != "invalid_scope" {
		t.Fatalf("scope escalation should be invalid_scope, got %v", aerr)
	}
}

func TestFullAuthorizationCodeFlow(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	verifier, challenge := pkcePair()
	c, secret, _ := svc.RegisterClient(ctx, "user-1", "App",
		[]string{"https://app.example.com/cb"}, []string{ScopeOpenID, ScopeEmail, ScopeMailRead}, false)

	v, aerr := svc.ValidateAuthorize(ctx, AuthorizeRequest{
		ResponseType: "code", ClientID: c.ClientID, RedirectURI: "https://app.example.com/cb",
		Scope: "openid email", State: "st", CodeChallenge: challenge, CodeChallengeMethod: "S256", Nonce: "n-1",
	})
	if aerr != nil {
		t.Fatalf("validate: %v", aerr)
	}
	code, err := svc.IssueCode(ctx, v, "subject-42")
	if err != nil {
		t.Fatalf("IssueCode: %v", err)
	}

	// Wrong PKCE verifier must fail.
	if _, err := svc.ExchangeCode(ctx, ExchangeCodeParams{
		Code: code, RedirectURI: "https://app.example.com/cb", ClientID: c.ClientID, ClientSecret: secret,
		CodeVerifier: "this-is-the-wrong-verifier-but-still-long-enough-xxxxxxxx",
	}); err == nil {
		t.Fatal("exchange with wrong PKCE verifier should fail")
	}

	// Wrong client secret must fail with invalid_client (and not consume code yet
	// — but our impl authenticates before consuming, so the code is still usable).
	if _, err := svc.ExchangeCode(ctx, ExchangeCodeParams{
		Code: code, RedirectURI: "https://app.example.com/cb", ClientID: c.ClientID, ClientSecret: "vcsk_wrong",
		CodeVerifier: verifier,
	}); err != ErrInvalidClient {
		t.Fatalf("expected ErrInvalidClient, got %v", err)
	}

	// Correct exchange.
	resp, err := svc.ExchangeCode(ctx, ExchangeCodeParams{
		Code: code, RedirectURI: "https://app.example.com/cb", ClientID: c.ClientID, ClientSecret: secret,
		CodeVerifier: verifier,
	})
	if err != nil {
		t.Fatalf("ExchangeCode: %v", err)
	}
	if resp.AccessToken == "" || resp.RefreshToken == "" || resp.IDToken == "" {
		t.Fatalf("missing tokens in response: %+v", resp)
	}
	if resp.TokenType != "Bearer" {
		t.Fatalf("token_type = %q", resp.TokenType)
	}

	// id_token verifies + carries the right claims.
	claims, err := VerifyIDToken(resp.IDToken, &svc.SigningKey().Private.PublicKey)
	if err != nil {
		t.Fatalf("VerifyIDToken: %v", err)
	}
	if claims["sub"] != "subject-42" || claims["aud"] != c.ClientID || claims["iss"] != "https://vulos.test" {
		t.Fatalf("bad id_token claims: %+v", claims)
	}
	if claims["nonce"] != "n-1" {
		t.Fatalf("nonce not echoed: %+v", claims)
	}
	if claims["email"] != "alice@vulos.test" {
		t.Fatalf("email claim missing: %+v", claims)
	}

	// Single-use: replaying the code must fail.
	if _, err := svc.ExchangeCode(ctx, ExchangeCodeParams{
		Code: code, RedirectURI: "https://app.example.com/cb", ClientID: c.ClientID, ClientSecret: secret,
		CodeVerifier: verifier,
	}); err != ErrInvalidGrant {
		t.Fatalf("code replay should be invalid_grant, got %v", err)
	}

	// userinfo via the access token.
	ui, err := svc.UserInfoForToken(ctx, resp.AccessToken)
	if err != nil {
		t.Fatalf("UserInfoForToken: %v", err)
	}
	if ui.Subject != "subject-42" || ui.Email != "alice@vulos.test" {
		t.Fatalf("bad userinfo: %+v", ui)
	}

	// Refresh-token rotation: old token dies, new tokens issued.
	refreshed, err := svc.Refresh(ctx, RefreshParams{RefreshToken: resp.RefreshToken, ClientID: c.ClientID, ClientSecret: secret})
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if refreshed.AccessToken == resp.AccessToken {
		t.Fatal("refresh should issue a new access token")
	}
	if _, err := svc.Refresh(ctx, RefreshParams{RefreshToken: resp.RefreshToken, ClientID: c.ClientID, ClientSecret: secret}); err != ErrInvalidGrant {
		t.Fatalf("reusing rotated refresh token should fail, got %v", err)
	}
}

func TestRedirectURIBoundAtExchange(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	verifier, challenge := pkcePair()
	c, secret, _ := svc.RegisterClient(ctx, "user-1", "App",
		[]string{"https://app.example.com/cb", "https://app.example.com/cb2"}, []string{ScopeOpenID}, false)
	v, _ := svc.ValidateAuthorize(ctx, AuthorizeRequest{
		ResponseType: "code", ClientID: c.ClientID, RedirectURI: "https://app.example.com/cb",
		Scope: "openid", CodeChallenge: challenge, CodeChallengeMethod: "S256",
	})
	code, _ := svc.IssueCode(ctx, v, "sub")
	// Exchange with a DIFFERENT (but still registered) redirect_uri must fail.
	if _, err := svc.ExchangeCode(ctx, ExchangeCodeParams{
		Code: code, RedirectURI: "https://app.example.com/cb2", ClientID: c.ClientID, ClientSecret: secret, CodeVerifier: verifier,
	}); err != ErrInvalidGrant {
		t.Fatalf("redirect_uri mismatch at exchange should be invalid_grant, got %v", err)
	}
}

func TestPublicClientNoSecret(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	verifier, challenge := pkcePair()
	c, secret, _ := svc.RegisterClient(ctx, "user-1", "SPA",
		[]string{"https://spa.example.com/cb"}, []string{ScopeOpenID}, true)
	if secret != "" {
		t.Fatal("public client should not get a secret")
	}
	v, _ := svc.ValidateAuthorize(ctx, AuthorizeRequest{
		ResponseType: "code", ClientID: c.ClientID, RedirectURI: "https://spa.example.com/cb",
		Scope: "openid", CodeChallenge: challenge, CodeChallengeMethod: "S256",
	})
	code, _ := svc.IssueCode(ctx, v, "sub")
	// Public client exchanges with NO secret, PKCE only.
	resp, err := svc.ExchangeCode(ctx, ExchangeCodeParams{
		Code: code, RedirectURI: "https://spa.example.com/cb", ClientID: c.ClientID, CodeVerifier: verifier,
	})
	if err != nil {
		t.Fatalf("public client exchange: %v", err)
	}
	if resp.AccessToken == "" {
		t.Fatal("no access token for public client")
	}
}

func TestExpiredAuthCode(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	verifier, challenge := pkcePair()
	// Negative TTL → the code's expiry is already in the past (storage is
	// second-granular, so a sub-second TTL would round to the same second).
	svc.SetTTLs(-time.Second, time.Hour, time.Hour)
	c, secret, _ := svc.RegisterClient(ctx, "user-1", "App",
		[]string{"https://app.example.com/cb"}, []string{ScopeOpenID}, false)
	v, _ := svc.ValidateAuthorize(ctx, AuthorizeRequest{
		ResponseType: "code", ClientID: c.ClientID, RedirectURI: "https://app.example.com/cb",
		Scope: "openid", CodeChallenge: challenge, CodeChallengeMethod: "S256",
	})
	code, _ := svc.IssueCode(ctx, v, "sub")
	if _, err := svc.ExchangeCode(ctx, ExchangeCodeParams{
		Code: code, RedirectURI: "https://app.example.com/cb", ClientID: c.ClientID, ClientSecret: secret, CodeVerifier: verifier,
	}); err != ErrInvalidGrant {
		t.Fatalf("expired code should be invalid_grant, got %v", err)
	}
}

func TestSigningKeyPersistsAcrossReopen(t *testing.T) {
	t.Setenv("VULOS_DEV", "true")
	st := newTestStore(t)
	k1, err := st.LoadOrCreateSigningKey(context.Background())
	if err != nil {
		t.Fatalf("LoadOrCreateSigningKey: %v", err)
	}
	k2, err := st.LoadOrCreateSigningKey(context.Background())
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if k1.KID != k2.KID {
		t.Fatalf("signing key kid changed: %s != %s", k1.KID, k2.KID)
	}
	jwks, err := st.AllPublicJWKs(context.Background())
	if err != nil || len(jwks) != 1 {
		t.Fatalf("JWKS: err=%v len=%d", err, len(jwks))
	}
	if jwks[0].Kid != k1.KID || jwks[0].Alg != "RS256" || jwks[0].Kty != "RSA" {
		t.Fatalf("bad JWK: %+v", jwks[0])
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Stored consent grant tests
// ─────────────────────────────────────────────────────────────────────────────

func TestConsentStoredAndAutoApprove(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	verifier, challenge := pkcePair()

	c, secret, _ := svc.RegisterClient(ctx, "user-1", "App",
		[]string{"https://app.example.com/cb"}, []string{ScopeOpenID, ScopeEmail}, false)

	// No consent yet → HasConsent returns false.
	if svc.HasConsent(ctx, "user-1", c.ClientID, []string{ScopeOpenID, ScopeEmail}) {
		t.Fatal("HasConsent should be false before any grant")
	}

	// Run the full flow once through IssueCode so we have a validated authorize.
	v, aerr := svc.ValidateAuthorize(ctx, AuthorizeRequest{
		ResponseType: "code", ClientID: c.ClientID, RedirectURI: "https://app.example.com/cb",
		Scope: "openid email", CodeChallenge: challenge, CodeChallengeMethod: "S256",
	})
	if aerr != nil {
		t.Fatalf("validate: %v", aerr)
	}

	// Store the consent (simulates the user clicking "Allow").
	if err := svc.GrantConsent(ctx, "user-1", v); err != nil {
		t.Fatalf("GrantConsent: %v", err)
	}

	// Now HasConsent returns true for the same scope set.
	if !svc.HasConsent(ctx, "user-1", c.ClientID, []string{ScopeOpenID, ScopeEmail}) {
		t.Fatal("HasConsent should be true after grant")
	}
	// … and also true for a subset.
	if !svc.HasConsent(ctx, "user-1", c.ClientID, []string{ScopeOpenID}) {
		t.Fatal("HasConsent should be true for a subset of the granted scopes")
	}
	// … but false for a scope not in the grant.
	if svc.HasConsent(ctx, "user-1", c.ClientID, []string{ScopeOpenID, ScopeMailRead}) {
		t.Fatal("HasConsent must be false when the grant does not cover all requested scopes")
	}

	// Revoke removes the grant.
	if err := svc.RevokeConsent(ctx, "user-1", c.ClientID); err != nil {
		t.Fatalf("RevokeConsent: %v", err)
	}
	if svc.HasConsent(ctx, "user-1", c.ClientID, []string{ScopeOpenID}) {
		t.Fatal("HasConsent should be false after revocation")
	}

	// Revoking a non-existent consent returns ErrConsentNotFound.
	if err := svc.RevokeConsent(ctx, "user-1", c.ClientID); !errors.Is(err, ErrConsentNotFound) {
		t.Fatalf("expected ErrConsentNotFound, got %v", err)
	}

	// ListUserConsents returns an empty slice after revocation.
	grants, err := svc.ListUserConsents(ctx, "user-1")
	if err != nil {
		t.Fatalf("ListUserConsents: %v", err)
	}
	if len(grants) != 0 {
		t.Fatalf("expected 0 consents after revocation, got %d", len(grants))
	}

	// Re-grant → re-appears in the list.
	if err := svc.GrantConsent(ctx, "user-1", v); err != nil {
		t.Fatalf("re-GrantConsent: %v", err)
	}
	grants, err = svc.ListUserConsents(ctx, "user-1")
	if err != nil || len(grants) != 1 {
		t.Fatalf("expected 1 consent, got %d (err=%v)", len(grants), err)
	}
	if grants[0].ClientID != c.ClientID {
		t.Fatalf("wrong client in consent list: %v", grants)
	}

	// Sanity-check the full exchange still works after consent re-grant.
	code, _ := svc.IssueCode(ctx, v, "user-1")
	resp, err := svc.ExchangeCode(ctx, ExchangeCodeParams{
		Code: code, RedirectURI: "https://app.example.com/cb", ClientID: c.ClientID, ClientSecret: secret, CodeVerifier: verifier,
	})
	if err != nil || resp.AccessToken == "" {
		t.Fatalf("exchange after re-grant: err=%v", err)
	}
	_ = resp
}

// ─────────────────────────────────────────────────────────────────────────────
// RFC 7009 token revocation tests
// ─────────────────────────────────────────────────────────────────────────────

func TestRevokeRaw(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	verifier, challenge := pkcePair()

	c, secret, _ := svc.RegisterClient(ctx, "user-1", "App",
		[]string{"https://app.example.com/cb"}, []string{ScopeOpenID, ScopeEmail}, false)

	v, _ := svc.ValidateAuthorize(ctx, AuthorizeRequest{
		ResponseType: "code", ClientID: c.ClientID, RedirectURI: "https://app.example.com/cb",
		Scope: "openid email", CodeChallenge: challenge, CodeChallengeMethod: "S256",
	})
	code, _ := svc.IssueCode(ctx, v, "user-1")
	resp, err := svc.ExchangeCode(ctx, ExchangeCodeParams{
		Code: code, RedirectURI: "https://app.example.com/cb",
		ClientID: c.ClientID, ClientSecret: secret, CodeVerifier: verifier,
	})
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}

	// Access token is live before revoke.
	if _, err := svc.IntrospectAccessToken(ctx, resp.AccessToken); err != nil {
		t.Fatalf("introspect before revoke: %v", err)
	}

	// Revoke the access token.
	if err := svc.RevokeRaw(ctx, c.ClientID, resp.AccessToken, "access_token"); err != nil {
		t.Fatalf("RevokeRaw access: %v", err)
	}
	// Now it should be gone.
	if _, err := svc.IntrospectAccessToken(ctx, resp.AccessToken); !errors.Is(err, ErrTokenNotFound) {
		t.Fatalf("expected ErrTokenNotFound after revoke, got %v", err)
	}

	// Revoking a non-existent / unknown token is a no-op (RFC 7009 §2.2).
	if err := svc.RevokeRaw(ctx, c.ClientID, "vcrt_totally_bogus", ""); err != nil {
		t.Fatalf("RevokeRaw unknown token should be no-op, got %v", err)
	}

	// A different client cannot revoke a token it didn't issue.
	// Use a public client so we can exchange without a secret.
	c2, _, _ := svc.RegisterClient(ctx, "user-1", "Other",
		[]string{"https://other.example.com/cb"}, []string{ScopeOpenID}, true /* public */)
	// Issue tokens for c2 (public client: PKCE only, no secret needed).
	v2, _ := svc.ValidateAuthorize(ctx, AuthorizeRequest{
		ResponseType: "code", ClientID: c2.ClientID, RedirectURI: "https://other.example.com/cb",
		Scope: "openid", CodeChallenge: challenge, CodeChallengeMethod: "S256",
	})
	code2, _ := svc.IssueCode(ctx, v2, "user-1")
	resp2, err2 := svc.ExchangeCode(ctx, ExchangeCodeParams{
		Code: code2, RedirectURI: "https://other.example.com/cb",
		ClientID: c2.ClientID, CodeVerifier: verifier,
	})
	if err2 != nil || resp2 == nil {
		t.Fatalf("exchange for c2 (public): err=%v", err2)
	}
	// Client c tries to revoke c2's token — must be rejected.
	if err := svc.RevokeRaw(ctx, c.ClientID, resp2.AccessToken, "access_token"); !errors.Is(err, ErrInvalidClient) {
		t.Fatalf("cross-client revoke should be ErrInvalidClient, got %v", err)
	}
}

// TestJWKSVerifiesIDToken performs an end-to-end test: issue an id_token, then
// verify the signature using the public key reconstructed from the JWKS.
func TestJWKSVerifiesIDToken(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	verifier, challenge := pkcePair()

	c, secret, _ := svc.RegisterClient(ctx, "user-1", "App",
		[]string{"https://app.example.com/cb"}, []string{ScopeOpenID, ScopeEmail}, false)

	v, _ := svc.ValidateAuthorize(ctx, AuthorizeRequest{
		ResponseType: "code", ClientID: c.ClientID, RedirectURI: "https://app.example.com/cb",
		Scope: "openid email", CodeChallenge: challenge, CodeChallengeMethod: "S256", Nonce: "n-jwks",
	})
	code, _ := svc.IssueCode(ctx, v, "subject-jwks")
	resp, err := svc.ExchangeCode(ctx, ExchangeCodeParams{
		Code: code, RedirectURI: "https://app.example.com/cb",
		ClientID: c.ClientID, ClientSecret: secret, CodeVerifier: verifier,
	})
	if err != nil || resp.IDToken == "" {
		t.Fatalf("exchange: err=%v idtoken=%q", err, resp.IDToken)
	}

	// Reconstruct the public key from the JWKS document.
	jwks, err := svc.Store().AllPublicJWKs(ctx)
	if err != nil || len(jwks) == 0 {
		t.Fatalf("AllPublicJWKs: err=%v len=%d", err, len(jwks))
	}
	pubKey, err := jwks[0].RSAPublicKey()
	if err != nil {
		t.Fatalf("RSAPublicKey from JWK: %v", err)
	}

	// Verify the id_token signature using the reconstructed key.
	claims, err := VerifyIDToken(resp.IDToken, pubKey)
	if err != nil {
		t.Fatalf("VerifyIDToken with JWKS key: %v", err)
	}
	if claims["sub"] != "subject-jwks" || claims["nonce"] != "n-jwks" {
		t.Fatalf("bad claims from JWKS-verified token: %v", claims)
	}
}

// ---------------------------------------------------------------------------
// Audit security regression tests (cloud-audit-security branch)
// ---------------------------------------------------------------------------

// TestH1_RevokeConsentKillsTokens verifies H1 (audit): revoking app consent
// now also revokes all outstanding access and refresh tokens for that client,
// not just the consent record.
func TestH1_RevokeConsentKillsTokens(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	verifier, challenge := pkcePair()

	c, secret, _ := svc.RegisterClient(ctx, "user-h1", "H1App",
		[]string{"https://h1.example.com/cb"}, []string{ScopeOpenID}, false)

	v, _ := svc.ValidateAuthorize(ctx, AuthorizeRequest{
		ResponseType: "code", ClientID: c.ClientID, RedirectURI: "https://h1.example.com/cb",
		Scope: "openid", CodeChallenge: challenge, CodeChallengeMethod: "S256",
	})

	// Grant consent (simulates the "Allow" click on the consent screen).
	if err := svc.GrantConsent(ctx, "user-h1", v); err != nil {
		t.Fatalf("GrantConsent: %v", err)
	}

	code, _ := svc.IssueCode(ctx, v, "user-h1")
	resp, err := svc.ExchangeCode(ctx, ExchangeCodeParams{
		Code: code, RedirectURI: "https://h1.example.com/cb",
		ClientID: c.ClientID, ClientSecret: secret, CodeVerifier: verifier,
	})
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}

	// Both tokens live before consent revoke.
	if _, err := svc.IntrospectAccessToken(ctx, resp.AccessToken); err != nil {
		t.Fatalf("access token should be live before revoke: %v", err)
	}

	// Revoke consent → must also kill tokens.
	if err := svc.RevokeConsent(ctx, "user-h1", c.ClientID); err != nil {
		t.Fatalf("RevokeConsent: %v", err)
	}

	// Access token must be dead after consent revoke.
	if _, err := svc.IntrospectAccessToken(ctx, resp.AccessToken); !errors.Is(err, ErrTokenNotFound) {
		t.Fatalf("SEC-H1: access token must be revoked after RevokeConsent, got %v", err)
	}
}

// TestM4_RefreshTokenReplay_RevokesFamily verifies M4 (audit): replaying a
// rotated-out refresh token (RFC 9700 §4.14) revokes the entire token family
// so neither the attacker's nor the legitimate owner's next refresh works.
func TestM4_RefreshTokenReplay_RevokesFamily(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	verifier, challenge := pkcePair()

	c, secret, _ := svc.RegisterClient(ctx, "user-m4", "M4App",
		[]string{"https://m4.example.com/cb"}, []string{ScopeOpenID}, false)

	v, _ := svc.ValidateAuthorize(ctx, AuthorizeRequest{
		ResponseType: "code", ClientID: c.ClientID, RedirectURI: "https://m4.example.com/cb",
		Scope: "openid", CodeChallenge: challenge, CodeChallengeMethod: "S256",
	})
	code, _ := svc.IssueCode(ctx, v, "user-m4")
	resp1, err := svc.ExchangeCode(ctx, ExchangeCodeParams{
		Code: code, RedirectURI: "https://m4.example.com/cb",
		ClientID: c.ClientID, ClientSecret: secret, CodeVerifier: verifier,
	})
	if err != nil {
		t.Fatalf("initial exchange: %v", err)
	}
	rt1 := resp1.RefreshToken

	// Rotate: exchange rt1 for rt2. rt1 is now "rotated out" (revoked).
	resp2, err := svc.Refresh(ctx, RefreshParams{
		ClientID: c.ClientID, ClientSecret: secret, RefreshToken: rt1,
	})
	if err != nil {
		t.Fatalf("first refresh: %v", err)
	}
	rt2 := resp2.RefreshToken
	_ = rt2

	// Replay rt1 (already rotated out). This should:
	// 1. Detect the replay (rt1 is revoked).
	// 2. Revoke the entire family, including rt2.
	_, err = svc.Refresh(ctx, RefreshParams{
		ClientID: c.ClientID, ClientSecret: secret, RefreshToken: rt1,
	})
	if err == nil {
		t.Fatal("SEC-M4: replaying rotated-out refresh token must fail")
	}

	// Now rt2 (the "attacker's" token from after the steal) should also be dead.
	_, err = svc.Refresh(ctx, RefreshParams{
		ClientID: c.ClientID, ClientSecret: secret, RefreshToken: rt2,
	})
	if err == nil {
		t.Fatal("SEC-M4: rt2 must also be dead after family revoke on replay")
	}
}

// TestRevokeTokenByHash_FamilyLookupFailsClosed is the WAVE34-5 regression: when
// the family lookup query errors, RevokeTokenByHash must FAIL (return the error)
// rather than silently skipping the cascade — a partial-revocation hole that
// would leave sibling access tokens live.
func TestRevokeTokenByHash_FamilyLookupFailsClosed(t *testing.T) {
	t.Setenv("VULOS_DEV", "true")
	st := newTestStore(t)
	ctx := context.Background()

	// Drop the table so the SELECT family_id query errors (not sql.ErrNoRows).
	if _, err := st.db.Exec(`DROP TABLE oauth_tokens`); err != nil {
		t.Fatalf("drop oauth_tokens: %v", err)
	}
	if err := st.RevokeTokenByHash(ctx, "any-hash"); err == nil {
		t.Fatal("RevokeTokenByHash must fail closed when the family lookup query errors")
	}
}
