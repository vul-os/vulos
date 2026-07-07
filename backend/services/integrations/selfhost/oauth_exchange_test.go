package selfhost

// oauth_exchange_test.go — CAPSTONE coverage for the PRODUCTION token-custody
// path (oauth2Exchanger.Exchange / .Refresh), which the fake-Exchanger tests
// deliberately never reach. These drive the real golang.org/x/oauth2 code by
// injecting a mock provider transport via oauth2.HTTPClient in the context, so
// NO real network is used and the provider token/userinfo endpoints are fully
// deterministic. They assert:
//
//   * Exchange forwards the PKCE code_verifier to the token endpoint (S256).
//   * Exchange returns the refresh token + provider-reported granted scopes,
//     and best-effort account email (from the userinfo endpoint).
//   * Exchange fails ErrNoRefreshToken when the provider omits refresh_token
//     (re-consent path) — a cred-custody invariant.
//   * Refresh mints a fresh access token from a stored refresh token and
//     surfaces a rotated refresh token when the provider rotates it.
//   * The Service persists a ROTATED refresh token (store.SetRefreshToken) on a
//     mint-triggered refresh — the branch the fake exchanger never exercises.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

// mockProviderRT routes token/userinfo requests to in-test handlers so the real
// oauth2 library runs end-to-end without a network. It records the last token
// request form so a test can assert PKCE forwarding.
type mockProviderRT struct {
	tokenResp    map[string]any
	tokenStatus  int
	userinfoBody string
	lastTokenReq url.Values
}

func (m *mockProviderRT) RoundTrip(r *http.Request) (*http.Response, error) {
	respond := func(status int, ctype, body string) (*http.Response, error) {
		return &http.Response{
			StatusCode: status,
			Header:     http.Header{"Content-Type": []string{ctype}},
			Body:       io_NopCloser(strings.NewReader(body)),
			Request:    r,
		}, nil
	}

	switch {
	case strings.Contains(r.URL.Host, "userinfo") || strings.Contains(r.URL.Path, "userinfo"):
		return respond(http.StatusOK, "application/json", m.userinfoBody)
	default:
		// token endpoint
		_ = r.ParseForm()
		m.lastTokenReq = r.Form
		status := m.tokenStatus
		if status == 0 {
			status = http.StatusOK
		}
		b, _ := json.Marshal(m.tokenResp)
		return respond(status, "application/json", string(b))
	}
}

// ctxWithMock injects the mock transport so BOTH the token exchange and the
// userinfo fetch go through it (oauth2 honours oauth2.HTTPClient in the ctx).
func ctxWithMock(rt http.RoundTripper) context.Context {
	return context.WithValue(context.Background(), oauth2.HTTPClient, &http.Client{Transport: rt})
}

func TestProdExchanger_Exchange_FullTokenCustody(t *testing.T) {
	t.Setenv("GOOGLE_OAUTH_CLIENT_ID", "cid")
	t.Setenv("GOOGLE_OAUTH_CLIENT_SECRET", "csec")
	t.Setenv("OAUTH_REDIRECT_BASE", "https://box.example.com")

	rt := &mockProviderRT{
		tokenResp: map[string]any{
			"access_token":  "AT-from-provider",
			"refresh_token": "RT-from-provider",
			"expires_in":    3600,
			"token_type":    "Bearer",
			"scope":         "openid email https://www.googleapis.com/auth/gmail.modify",
		},
		userinfoBody: `{"sub":"123","email":"owner@example.com","email_verified":true}`,
	}
	ex := NewExchanger(ProviderGoogle)

	tok, err := ex.Exchange(ctxWithMock(rt), "the-auth-code", "the-pkce-verifier")
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if tok.AccessToken != "AT-from-provider" {
		t.Fatalf("access token = %q", tok.AccessToken)
	}
	if tok.RefreshToken != "RT-from-provider" {
		t.Fatalf("refresh token = %q", tok.RefreshToken)
	}
	// grantedScopes must reflect the provider-reported scope string, not just the
	// requested config scopes.
	if !strings.Contains(tok.Scopes, "gmail.modify") {
		t.Fatalf("granted scopes not surfaced from provider: %q", tok.Scopes)
	}
	// bestEffortEmail extracted from the userinfo endpoint.
	if tok.Email != "owner@example.com" {
		t.Fatalf("best-effort email = %q, want owner@example.com", tok.Email)
	}
	// The token request must carry the PKCE code_verifier and the auth code.
	if got := rt.lastTokenReq.Get("code_verifier"); got != "the-pkce-verifier" {
		t.Fatalf("PKCE code_verifier not forwarded to token endpoint: %q", got)
	}
	if got := rt.lastTokenReq.Get("code"); got != "the-auth-code" {
		t.Fatalf("auth code not forwarded: %q", got)
	}
	// Expiry is in the future (~1h).
	if !tok.Expiry.After(time.Now().Add(30*time.Minute)) {
		t.Fatalf("expiry not honoured: %v", tok.Expiry)
	}
}

func TestProdExchanger_Exchange_NoRefreshToken_ReConsent(t *testing.T) {
	t.Setenv("GOOGLE_OAUTH_CLIENT_ID", "cid")
	t.Setenv("GOOGLE_OAUTH_CLIENT_SECRET", "csec")

	rt := &mockProviderRT{
		tokenResp: map[string]any{
			"access_token": "AT-only",
			"expires_in":   3600,
			"token_type":   "Bearer",
			// no refresh_token → re-consent required
		},
	}
	ex := NewExchanger(ProviderGoogle)
	_, err := ex.Exchange(ctxWithMock(rt), "code", "verifier")
	if err != ErrNoRefreshToken {
		t.Fatalf("Exchange without refresh_token: got %v, want ErrNoRefreshToken", err)
	}
}

func TestProdExchanger_Refresh_MintsAndRotates(t *testing.T) {
	t.Setenv("GOOGLE_OAUTH_CLIENT_ID", "cid")
	t.Setenv("GOOGLE_OAUTH_CLIENT_SECRET", "csec")

	rt := &mockProviderRT{
		tokenResp: map[string]any{
			"access_token":  "AT-refreshed",
			"refresh_token": "RT-rotated",
			"expires_in":    3600,
			"token_type":    "Bearer",
			"scope":         "openid email",
		},
	}
	ex := NewExchanger(ProviderGoogle)
	tok, err := ex.Refresh(ctxWithMock(rt), "old-refresh-token")
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if tok.AccessToken != "AT-refreshed" {
		t.Fatalf("refreshed access token = %q", tok.AccessToken)
	}
	// The token endpoint must have received a refresh_token grant carrying the
	// stored refresh token.
	if got := rt.lastTokenReq.Get("grant_type"); got != "refresh_token" {
		t.Fatalf("grant_type = %q, want refresh_token", got)
	}
	if got := rt.lastTokenReq.Get("refresh_token"); got != "old-refresh-token" {
		t.Fatalf("stored refresh token not sent to provider: %q", got)
	}
	if tok.RefreshToken != "RT-rotated" {
		t.Fatalf("rotated refresh token not surfaced: %q", tok.RefreshToken)
	}
}

// rotatingExchanger's Refresh returns a NEW refresh token so the Service's
// rotation-persistence branch (store.SetRefreshToken) actually fires — the fake
// exchanger elsewhere returns an empty RefreshToken and never exercises it.
type rotatingExchanger struct {
	fakeExchanger
	newRefresh string
}

func (r *rotatingExchanger) Refresh(ctx context.Context, refreshToken string) (*Token, error) {
	return &Token{
		AccessToken:  "AT-after-rotate",
		RefreshToken: r.newRefresh,
		Expiry:       time.Now().Add(time.Hour),
		Scopes:       "openid email",
	}, nil
}

func TestService_RefreshRotation_PersistsNewRefreshToken(t *testing.T) {
	ex := &rotatingExchanger{
		fakeExchanger: fakeExchanger{refreshToken: "RT-original", accessToken: "AT-initial", expiry: time.Hour},
		newRefresh:    "RT-ROTATED-BY-PROVIDER",
	}
	svc := newTestService(t, ex)
	ctx := context.Background()

	if _, err := svc.Connect(ctx, "u1", ProviderGoogle, "code", "verif"); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	// Force expiry so MintAccessToken must refresh (and thus rotate).
	svc.now = func() time.Time { return time.Now().Add(2 * time.Hour) }
	if _, err := svc.MintAccessToken(ctx, "u1", ProviderGoogle); err != nil {
		t.Fatalf("Mint: %v", err)
	}

	// The stored refresh token must now be the rotated value (encrypted). Decrypt
	// and compare against the rotated plaintext, and confirm the ORIGINAL is gone.
	stored, err := svc.store.Get(ctx, "u1", ProviderGoogle)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	got, err := decrypt(stored.RefreshTokenEnc, testKEK())
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if got != "RT-ROTATED-BY-PROVIDER" {
		t.Fatalf("rotated refresh token not persisted: got %q", got)
	}
}

// io_NopCloser mirrors io.NopCloser without importing io at the top just for one
// use (keeps the mock transport self-contained).
func io_NopCloser(r *strings.Reader) *nopCloser { return &nopCloser{r} }

type nopCloser struct{ *strings.Reader }

func (nopCloser) Close() error { return nil }
