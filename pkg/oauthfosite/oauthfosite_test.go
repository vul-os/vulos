package oauthfosite_test

import (
	"context"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/ory/fosite"
	"github.com/ory/fosite/handler/openid"
	"github.com/ory/fosite/token/jwt"

	"github.com/vul-os/vulos-management/pkg/cpdb"
	"github.com/vul-os/vulos-management/pkg/oauthfosite"
	"github.com/vul-os/vulos-management/pkg/oauthprovider"
)

const (
	testIssuer   = "https://accounts.vulos.test"
	testClientID = "vcid_test_public_client"
	testRedirect = "https://app.vulos.test/callback"
	testSubject  = "usr_01HZXTESTSUBJECT0000000000"
	testNonce    = "nonce-0123456789abcdef"
	testState    = "state-0123456789abcdef"
)

// harness bundles the two stores and the fosite provider for a test.
type harness struct {
	ostore   *oauthprovider.Store
	provider fosite.OAuth2Provider
	pub      *rsa.PublicKey
	kid      string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	// Dev path: all-zeros dev KEK (no INTEGRATIONS_KEK needed) for the wrapped
	// id_token signing key. Production fails closed without a KEK.
	t.Setenv("VULOS_DEV", "true")

	db, err := cpdb.OpenSQLiteDSN(":memory:")
	if err != nil {
		t.Fatalf("cpdb.OpenSQLiteDSN: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ostore, err := oauthprovider.Open(db)
	if err != nil {
		t.Fatalf("oauthprovider.Open: %v", err)
	}
	fstore, err := oauthfosite.Open(db, ostore)
	if err != nil {
		t.Fatalf("oauthfosite.Open: %v", err)
	}

	ctx := context.Background()
	if err := ostore.CreateClient(ctx, oauthprovider.Client{
		ClientID:     testClientID,
		IsPublic:     true, // no secret; PKCE-only
		Name:         "Test Public Client",
		RedirectURIs: []string{testRedirect},
		Scopes:       []string{"profile"},
		OwnerUserID:  "owner-1",
	}); err != nil {
		t.Fatalf("CreateClient: %v", err)
	}

	provider, err := oauthfosite.New(ctx, fstore, testIssuer)
	if err != nil {
		t.Fatalf("oauthfosite.New: %v", err)
	}

	// The signing key is idempotent — this returns the SAME KEK-wrapped key the
	// provider loaded, so its public half verifies the id_token.
	sk, err := ostore.LoadOrCreateSigningKey(ctx)
	if err != nil {
		t.Fatalf("LoadOrCreateSigningKey: %v", err)
	}
	return &harness{ostore: ostore, provider: provider, pub: &sk.Private.PublicKey, kid: sk.KID}
}

func pkcePair() (verifier, challenge string) {
	verifier = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ012345" // 57 chars
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:])
}

func newOIDCSession(subject string) *openid.DefaultSession {
	return &openid.DefaultSession{
		Claims:  &jwt.IDTokenClaims{Subject: subject},
		Headers: &jwt.Headers{},
		Subject: subject,
	}
}

// authorize drives the authorize endpoint and returns the issued code.
func (h *harness) authorize(t *testing.T, challenge string) string {
	t.Helper()
	ctx := context.Background()
	q := url.Values{}
	q.Set("response_type", "code")
	q.Set("client_id", testClientID)
	q.Set("redirect_uri", testRedirect)
	q.Set("scope", "openid offline")
	q.Set("state", testState)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	q.Set("nonce", testNonce)

	req := httptest.NewRequest("GET", "/authorize?"+q.Encode(), nil)
	ar, err := h.provider.NewAuthorizeRequest(ctx, req)
	if err != nil {
		t.Fatalf("NewAuthorizeRequest: %v", err)
	}
	ar.GrantScope("openid")
	ar.GrantScope("offline")

	resp, err := h.provider.NewAuthorizeResponse(ctx, ar, newOIDCSession(testSubject))
	if err != nil {
		t.Fatalf("NewAuthorizeResponse: %v", err)
	}
	code := resp.GetParameters().Get("code")
	if code == "" {
		t.Fatal("no authorization code issued")
	}
	return code
}

// exchange drives the token endpoint for an authorization_code grant.
func (h *harness) exchange(ctx context.Context, code, verifier string) (fosite.AccessResponder, error) {
	f := url.Values{}
	f.Set("grant_type", "authorization_code")
	f.Set("code", code)
	f.Set("redirect_uri", testRedirect)
	f.Set("client_id", testClientID)
	f.Set("code_verifier", verifier)
	req := httptest.NewRequest("POST", "/token", strings.NewReader(f.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	ar, err := h.provider.NewAccessRequest(ctx, req, newOIDCSession(""))
	if err != nil {
		return nil, err
	}
	return h.provider.NewAccessResponse(ctx, ar)
}

// refresh drives the token endpoint for a refresh_token grant.
func (h *harness) refresh(ctx context.Context, refreshToken string) (fosite.AccessResponder, error) {
	f := url.Values{}
	f.Set("grant_type", "refresh_token")
	f.Set("refresh_token", refreshToken)
	f.Set("client_id", testClientID)
	req := httptest.NewRequest("POST", "/token", strings.NewReader(f.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	ar, err := h.provider.NewAccessRequest(ctx, req, newOIDCSession(""))
	if err != nil {
		return nil, err
	}
	return h.provider.NewAccessResponse(ctx, ar)
}

// TestAuthCodePKCERoundTrip proves a registered public client + PKCE authorize
// code round-trips through fosite to a valid token set, and the access token
// validates.
func TestAuthCodePKCERoundTrip(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	verifier, challenge := pkcePair()

	code := h.authorize(t, challenge)
	resp, err := h.exchange(ctx, code, verifier)
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	at := resp.GetAccessToken()
	if at == "" {
		t.Fatal("no access token")
	}
	if resp.GetExtra("refresh_token") == nil {
		t.Fatal("no refresh token issued (offline scope should trigger one)")
	}
	if resp.GetExtra("id_token") == nil {
		t.Fatal("no id_token issued (openid scope should trigger one)")
	}

	// The access token validates via introspection.
	use, ar, err := h.provider.IntrospectToken(ctx, at, fosite.AccessToken, newOIDCSession(""))
	if err != nil {
		t.Fatalf("IntrospectToken: %v", err)
	}
	if use != fosite.AccessToken {
		t.Fatalf("expected access token use, got %q", use)
	}
	if ar.GetClient().GetID() != testClientID {
		t.Fatalf("introspected client mismatch: %q", ar.GetClient().GetID())
	}
}

// TestPKCEInvalidVerifierRejected proves a wrong / missing PKCE verifier is
// rejected at the token endpoint.
func TestPKCEInvalidVerifierRejected(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	_, challenge := pkcePair()

	// Wrong verifier.
	code := h.authorize(t, challenge)
	if _, err := h.exchange(ctx, code, "totally-wrong-verifier-but-long-enough-1234567890"); err == nil {
		t.Fatal("expected rejection for wrong PKCE verifier, got nil")
	}

	// Missing verifier entirely.
	code2 := h.authorize(t, challenge)
	if _, err := h.exchange(ctx, code2, ""); err == nil {
		t.Fatal("expected rejection for missing PKCE verifier, got nil")
	}
}

// TestRefreshRotationAndReplayRejected proves a refresh token rotates and a
// replayed (already-rotated) refresh token is rejected — the family
// reuse-detection property.
func TestRefreshRotationAndReplayRejected(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	verifier, challenge := pkcePair()

	code := h.authorize(t, challenge)
	resp, err := h.exchange(ctx, code, verifier)
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	rt1, ok := resp.GetExtra("refresh_token").(string)
	if !ok || rt1 == "" {
		t.Fatal("no initial refresh token")
	}

	// First refresh rotates to a new refresh token.
	resp2, err := h.refresh(ctx, rt1)
	if err != nil {
		t.Fatalf("first refresh: %v", err)
	}
	rt2, ok := resp2.GetExtra("refresh_token").(string)
	if !ok || rt2 == "" {
		t.Fatal("no rotated refresh token")
	}
	if rt2 == rt1 {
		t.Fatal("refresh token did not rotate")
	}

	// Replaying the OLD refresh token must fail.
	if _, err := h.refresh(ctx, rt1); err == nil {
		t.Fatal("expected replayed refresh token to be rejected")
	}

	// And reuse-detection revokes the family: the rotated token rt2 is now dead.
	if _, err := h.refresh(ctx, rt2); err == nil {
		t.Fatal("expected family revocation to invalidate rt2 after replay of rt1")
	}
}

// TestIDTokenRS256Claims proves the id_token is RS256, signed by the shared
// signing key, and carries the correct iss / aud / sub / nonce.
func TestIDTokenRS256Claims(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	verifier, challenge := pkcePair()

	code := h.authorize(t, challenge)
	resp, err := h.exchange(ctx, code, verifier)
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	idt, ok := resp.GetExtra("id_token").(string)
	if !ok || idt == "" {
		t.Fatal("no id_token")
	}

	// Header: RS256 + correct kid.
	parts := strings.Split(idt, ".")
	if len(parts) != 3 {
		t.Fatalf("malformed id_token: %d parts", len(parts))
	}
	hdrJSON, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatalf("decode header: %v", err)
	}
	var hdr struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if err := json.Unmarshal(hdrJSON, &hdr); err != nil {
		t.Fatalf("unmarshal header: %v", err)
	}
	if hdr.Alg != "RS256" {
		t.Fatalf("id_token alg = %q, want RS256", hdr.Alg)
	}
	if hdr.Kid != h.kid {
		t.Fatalf("id_token kid = %q, want %q", hdr.Kid, h.kid)
	}

	// Signature verifies with the shared signing key's public half.
	claims, err := oauthprovider.VerifyIDToken(idt, h.pub)
	if err != nil {
		t.Fatalf("id_token signature/verify failed: %v", err)
	}
	if got := claims["iss"]; got != testIssuer {
		t.Fatalf("iss = %v, want %v", got, testIssuer)
	}
	if got := claims["sub"]; got != testSubject {
		t.Fatalf("sub = %v, want %v", got, testSubject)
	}
	if got := claims["nonce"]; got != testNonce {
		t.Fatalf("nonce = %v, want %v", got, testNonce)
	}
	if !audienceContains(claims["aud"], testClientID) {
		t.Fatalf("aud = %v, want to contain %v", claims["aud"], testClientID)
	}
}

// audienceContains handles aud being either a string or a []any.
func audienceContains(aud any, want string) bool {
	switch v := aud.(type) {
	case string:
		return v == want
	case []any:
		for _, a := range v {
			if s, ok := a.(string); ok && s == want {
				return true
			}
		}
	}
	return false
}
