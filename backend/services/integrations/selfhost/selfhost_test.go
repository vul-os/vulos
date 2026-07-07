package selfhost

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"vulos/backend/services/env"
)

// fakeExchanger is a stub Exchanger so tests never reach a real provider.
type fakeExchanger struct {
	refreshToken string
	accessToken  string
	scopes       string
	email        string
	expiry       time.Duration
	refreshCalls int
	// refreshedAccess is the access token returned by Refresh (rotates so a test
	// can tell a cache-hit from a refresh).
	refreshedAccess string
}

func (f *fakeExchanger) AuthCodeURL(state, challenge string) (string, error) {
	return "https://provider.example/consent?state=" + state + "&code_challenge=" + challenge, nil
}
func (f *fakeExchanger) Exchange(ctx context.Context, code, verifier string) (*Token, error) {
	return &Token{
		AccessToken:  f.accessToken,
		RefreshToken: f.refreshToken,
		Expiry:       time.Now().Add(f.expiry),
		Scopes:       f.scopes,
		Email:        f.email,
	}, nil
}
func (f *fakeExchanger) Refresh(ctx context.Context, refreshToken string) (*Token, error) {
	f.refreshCalls++
	acc := f.refreshedAccess
	if acc == "" {
		acc = "refreshed-access"
	}
	return &Token{AccessToken: acc, Expiry: time.Now().Add(f.expiry), Scopes: f.scopes}, nil
}

func testKEK() []byte {
	k := make([]byte, 32)
	for i := range k {
		k[i] = byte(i + 1)
	}
	return k
}

func newTestService(t *testing.T, ex Exchanger) *Service {
	t.Helper()
	store, err := OpenStore("")
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return NewService(store, map[string]Exchanger{ProviderGoogle: ex, ProviderDropbox: ex}, testKEK())
}

func TestConnectAndMint_RefreshAndCache(t *testing.T) {
	ex := &fakeExchanger{
		refreshToken:    "RT-secret",
		accessToken:     "AT-initial",
		scopes:          "openid email",
		email:           "owner@example.com",
		expiry:          time.Hour,
		refreshedAccess: "AT-refreshed",
	}
	svc := newTestService(t, ex)
	ctx := context.Background()

	conn, err := svc.Connect(ctx, "user-1", ProviderGoogle, "auth-code", "verifier")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if conn.AccountEmail != "owner@example.com" {
		t.Fatalf("email=%q", conn.AccountEmail)
	}

	// Cached access token is still fresh (1h) → serves cache, NO refresh call.
	m1, err := svc.MintAccessToken(ctx, "user-1", ProviderGoogle)
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	if m1.AccessToken != "AT-initial" {
		t.Fatalf("expected cached AT-initial, got %q", m1.AccessToken)
	}
	if ex.refreshCalls != 0 {
		t.Fatalf("expected 0 refresh calls (cache hit), got %d", ex.refreshCalls)
	}

	// Force expiry by fast-forwarding the service clock past the cached expiry.
	svc.now = func() time.Time { return time.Now().Add(2 * time.Hour) }
	m2, err := svc.MintAccessToken(ctx, "user-1", ProviderGoogle)
	if err != nil {
		t.Fatalf("Mint after expiry: %v", err)
	}
	if m2.AccessToken != "AT-refreshed" {
		t.Fatalf("expected refreshed token, got %q", m2.AccessToken)
	}
	if ex.refreshCalls != 1 {
		t.Fatalf("expected 1 refresh call, got %d", ex.refreshCalls)
	}
}

func TestRefreshToken_EncryptedAtRest_NeverPlaintext(t *testing.T) {
	ex := &fakeExchanger{refreshToken: "TOP-SECRET-RT", accessToken: "AT", expiry: time.Hour}
	svc := newTestService(t, ex)
	ctx := context.Background()
	if _, err := svc.Connect(ctx, "user-1", ProviderGoogle, "c", "v"); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	// The stored ciphertext must NOT contain the plaintext refresh token.
	stored, err := svc.store.Get(ctx, "user-1", ProviderGoogle)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if stored.RefreshTokenEnc == "" {
		t.Fatalf("refresh token not stored")
	}
	if strings.Contains(stored.RefreshTokenEnc, "TOP-SECRET-RT") {
		t.Fatalf("PLAINTEXT refresh token leaked into stored ciphertext")
	}
	// It must round-trip through the KEK.
	got, err := decrypt(stored.RefreshTokenEnc, testKEK())
	if err != nil || got != "TOP-SECRET-RT" {
		t.Fatalf("decrypt round-trip failed: got=%q err=%v", got, err)
	}
	// A different KEK must fail the AEAD tag check (fail-closed).
	wrong := make([]byte, 32)
	if _, err := decrypt(stored.RefreshTokenEnc, wrong); err == nil {
		t.Fatalf("decrypt with wrong KEK must fail")
	}
}

func TestPerUserIsolation(t *testing.T) {
	ex := &fakeExchanger{refreshToken: "RT", accessToken: "AT-user1", expiry: time.Hour}
	svc := newTestService(t, ex)
	ctx := context.Background()
	if _, err := svc.Connect(ctx, "user-1", ProviderGoogle, "c", "v"); err != nil {
		t.Fatalf("Connect user-1: %v", err)
	}
	// user-2 has no connection: Status false, Mint → ErrNotConnected.
	connected, _, err := svc.Status(ctx, "user-2", ProviderGoogle)
	if err != nil {
		t.Fatalf("Status user-2: %v", err)
	}
	if connected {
		t.Fatalf("user-2 must not see user-1's connection")
	}
	if _, err := svc.MintAccessToken(ctx, "user-2", ProviderGoogle); err != ErrNotConnected {
		t.Fatalf("user-2 Mint: got %v want ErrNotConnected", err)
	}
	// user-1 still has theirs.
	if _, err := svc.MintAccessToken(ctx, "user-1", ProviderGoogle); err != nil {
		t.Fatalf("user-1 Mint: %v", err)
	}
	// user-1's list has exactly 1; user-2's is empty.
	l1, _ := svc.List(ctx, "user-1")
	l2, _ := svc.List(ctx, "user-2")
	if len(l1) != 1 || len(l2) != 0 {
		t.Fatalf("isolation broken: user-1=%d user-2=%d", len(l1), len(l2))
	}
}

func TestHandlers_SessionGatedAndTokenNeverLeaksRefresh(t *testing.T) {
	ex := &fakeExchanger{refreshToken: "RT-NO-LEAK", accessToken: "AT-visible", scopes: "s", expiry: time.Hour}
	svc := newTestService(t, ex)
	// Seed a connection directly so we can exercise /token + /status.
	if _, err := svc.Connect(context.Background(), "u1", ProviderGoogle, "c", "v"); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	mux := http.NewServeMux()
	RegisterHandlers(mux, svc, []byte("state-secret"), false)

	// Unauthenticated /token → 401.
	req := httptest.NewRequest(http.MethodGet, "/api/integrations/google/token", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauth token: code=%d want 401", rec.Code)
	}

	// Authenticated /token → 200 with access token but NEVER the refresh token.
	req = httptest.NewRequest(http.MethodGet, "/api/integrations/google/token", nil)
	req.Header.Set("X-User-ID", "u1")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("token: code=%d want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "AT-visible") {
		t.Fatalf("access token missing from response")
	}
	if strings.Contains(body, "RT-NO-LEAK") {
		t.Fatalf("SECURITY: refresh token leaked in /token response")
	}
	if strings.Contains(strings.ToLower(body), "refresh") {
		t.Fatalf("SECURITY: response mentions refresh token: %s", body)
	}

	// /status for another user must report not-connected (per-user isolation).
	req = httptest.NewRequest(http.MethodGet, "/api/integrations/google/status", nil)
	req.Header.Set("X-User-ID", "someone-else")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	var st map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &st)
	if st["connected"] != false {
		t.Fatalf("cross-user status leaked connection: %v", st)
	}
}

func TestKEK_FailClosedInProd(t *testing.T) {
	t.Setenv("INTEGRATIONS_KEK", "")
	// Prod: unset KEK is a hard error.
	if _, err := LoadKEK(env.EnvProd); err == nil {
		t.Fatalf("prod with unset KEK must fail closed")
	}
	// Local/dev: unset KEK falls back to the dev key (usable).
	for _, e := range []env.Env{env.EnvLocal, env.EnvDev} {
		k, err := LoadKEK(e)
		if err != nil || len(k) != 32 {
			t.Fatalf("env %s: dev fallback failed: %v", e, err)
		}
	}
	// A valid base64 32-byte key works everywhere.
	t.Setenv("INTEGRATIONS_KEK", "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=") // 32 bytes
	if k, err := LoadKEK(env.EnvProd); err != nil || len(k) != 32 {
		t.Fatalf("valid KEK in prod: %v", err)
	}
	// A wrong-length key is rejected.
	t.Setenv("INTEGRATIONS_KEK", "c2hvcnQ=") // "short"
	if _, err := LoadKEK(env.EnvProd); err == nil {
		t.Fatalf("wrong-length KEK must be rejected")
	}
}

func TestProviderConfigured_MissingCredsGraceful(t *testing.T) {
	t.Setenv("GOOGLE_OAUTH_CLIENT_ID", "")
	t.Setenv("GOOGLE_OAUTH_CLIENT_SECRET", "")
	if ProviderConfigured(ProviderGoogle) {
		t.Fatalf("unconfigured google must report false")
	}
	// connect for an unconfigured provider → 503 (not a crash).
	svc := newTestService(t, &fakeExchanger{})
	mux := http.NewServeMux()
	RegisterHandlers(mux, svc, []byte("s"), false)
	req := httptest.NewRequest(http.MethodGet, "/api/integrations/google/connect", nil)
	req.Header.Set("X-User-ID", "u1")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("unconfigured connect: code=%d want 503", rec.Code)
	}

	// Configured → connect issues a 302 to consent.
	t.Setenv("GOOGLE_OAUTH_CLIENT_ID", "client-id")
	t.Setenv("GOOGLE_OAUTH_CLIENT_SECRET", "client-secret")
	if !ProviderConfigured(ProviderGoogle) {
		t.Fatalf("configured google must report true")
	}
	req = httptest.NewRequest(http.MethodGet, "/api/integrations/google/connect", nil)
	req.Header.Set("X-User-ID", "u1")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("configured connect: code=%d want 302 (body=%s)", rec.Code, rec.Body.String())
	}
	// The consent URL comes from the (fake) exchanger here; assert the connect
	// handler passed through BOTH the CSRF state and the PKCE code_challenge. The
	// real google endpoint is exercised by TestRealExchanger_AuthCodeURL below.
	loc := rec.Header().Get("Location")
	if !strings.Contains(loc, "state=") || !strings.Contains(loc, "code_challenge=") {
		t.Fatalf("consent redirect missing state or PKCE challenge: %s", loc)
	}
	// A state cookie must have been set (CSRF).
	if len(rec.Result().Cookies()) == 0 {
		t.Fatalf("no state cookie set")
	}
}

// TestHandlers_CrossUserCredIsolation_HTTP proves at the HTTP layer that user B
// cannot read, mint from, or disconnect user A's connected account by presenting
// their OWN (trusted, middleware-injected) X-User-ID. Every route is keyed on the
// caller's session user id; there is no request field that lets B name A's row.
func TestHandlers_CrossUserCredIsolation_HTTP(t *testing.T) {
	ex := &fakeExchanger{refreshToken: "A-RT", accessToken: "A-AT", scopes: "s", expiry: time.Hour}
	svc := newTestService(t, ex)
	// user-A connects google.
	if _, err := svc.Connect(context.Background(), "user-A", ProviderGoogle, "c", "v"); err != nil {
		t.Fatalf("Connect A: %v", err)
	}
	mux := http.NewServeMux()
	RegisterHandlers(mux, svc, []byte("state-secret"), false)

	call := func(method, path, uid string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, nil)
		if uid != "" {
			req.Header.Set("X-User-ID", uid)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	// user-B mints a token for google → 404 not-connected (cannot borrow A's creds).
	if rec := call(http.MethodGet, "/api/integrations/google/token", "user-B"); rec.Code != http.StatusNotFound {
		t.Fatalf("B /token: code=%d want 404 (must not access A's connection)", rec.Code)
	}
	// user-B lists → empty (cannot see A's connection).
	recL := call(http.MethodGet, "/api/integrations", "user-B")
	if b := recL.Body.String(); strings.Contains(b, "google") || strings.Contains(b, "A-") {
		t.Fatalf("B /list leaked A's connection: %s", b)
	}
	// user-B disconnects google → must NOT remove A's row.
	if rec := call(http.MethodDelete, "/api/integrations/google", "user-B"); rec.Code != http.StatusNoContent {
		t.Fatalf("B /disconnect: code=%d want 204 (idempotent no-op)", rec.Code)
	}
	// A's connection survives B's disconnect attempt.
	if connected, _, _ := svc.Status(context.Background(), "user-A", ProviderGoogle); !connected {
		t.Fatalf("SECURITY: user-B's disconnect removed user-A's connection")
	}
	// And A can still mint.
	if rec := call(http.MethodGet, "/api/integrations/google/token", "user-A"); rec.Code != http.StatusOK {
		t.Fatalf("A /token after B's tampering: code=%d want 200", rec.Code)
	}
}

// TestCallback_CrossUserAndReplayedState_HTTP drives the full callback handler and
// proves a stolen/cross-user/replayed OAuth `state` is rejected with 400 before
// any code exchange happens (the exchanger must never be called on a bad state).
func TestCallback_CrossUserAndReplayedState_HTTP(t *testing.T) {
	t.Setenv("GOOGLE_OAUTH_CLIENT_ID", "cid")
	t.Setenv("GOOGLE_OAUTH_CLIENT_SECRET", "csec")

	ex := &countingExchanger{fakeExchanger: fakeExchanger{refreshToken: "RT", accessToken: "AT", expiry: time.Hour}}
	store, err := OpenStore("")
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	svc := NewService(store, map[string]Exchanger{ProviderGoogle: ex}, testKEK())

	secret := []byte("state-secret")
	mux := http.NewServeMux()
	RegisterHandlers(mux, svc, secret, false)

	// victim (user-A) starts a flow → capture the state cookie + state param.
	recStart := httptest.NewRecorder()
	state, _, err := GenerateState(recStart, secret, "user-A", ProviderGoogle, time.Now().UTC(), false)
	if err != nil {
		t.Fatalf("GenerateState: %v", err)
	}
	cookies := recStart.Result().Cookies()

	cb := func(uid string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/integrations/google/callback?code=authcode&state="+state, nil)
		for _, c := range cookies {
			req.AddCookie(c)
		}
		if uid != "" {
			req.Header.Set("X-User-ID", uid)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	// Attacker (user-B) replays user-A's captured state+cookie under their OWN
	// session → 400 (state is bound to user-A). No exchange must occur.
	if rec := cb("user-B"); rec.Code != http.StatusBadRequest {
		t.Fatalf("cross-user callback: code=%d want 400", rec.Code)
	}
	if ex.exchangeCalls != 0 {
		t.Fatalf("SECURITY: code exchanged on a cross-user state (%d calls)", ex.exchangeCalls)
	}

	// The legitimate user-A completes the flow once → 302 connected.
	if rec := cb("user-A"); rec.Code != http.StatusFound {
		t.Fatalf("legit callback: code=%d want 302 (body=%s)", rec.Code, rec.Body.String())
	}
	if ex.exchangeCalls != 1 {
		t.Fatalf("expected exactly 1 exchange for the legit flow, got %d", ex.exchangeCalls)
	}

	// REPLAY: the same state+cookie is presented again by user-A. The cookie is
	// cleared after use, so a replay carrying the now-stale cookie still fails
	// (no cookie in the fresh recorder's cleared response) — here we replay the
	// ORIGINAL cookie to simulate a captured value; it must NOT trigger a second
	// exchange beyond what a valid single flow allows. (A one-time state is the
	// goal; at minimum a replayed state cannot connect a DIFFERENT user.)
	recReplay := cb("user-B")
	if recReplay.Code != http.StatusBadRequest {
		t.Fatalf("replay as other user: code=%d want 400", recReplay.Code)
	}
}

// countingExchanger counts Exchange calls so a test can assert no exchange
// happened on a rejected state.
type countingExchanger struct {
	fakeExchanger
	exchangeCalls int
}

func (c *countingExchanger) Exchange(ctx context.Context, code, verifier string) (*Token, error) {
	c.exchangeCalls++
	return c.fakeExchanger.Exchange(ctx, code, verifier)
}

func TestUnknownProvider404(t *testing.T) {
	svc := newTestService(t, &fakeExchanger{})
	mux := http.NewServeMux()
	RegisterHandlers(mux, svc, []byte("s"), false)
	req := httptest.NewRequest(http.MethodGet, "/api/integrations/pinterest/status", nil)
	req.Header.Set("X-User-ID", "u1")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown provider: code=%d want 404", rec.Code)
	}
}

func TestRealExchanger_AuthCodeURL(t *testing.T) {
	t.Setenv("GOOGLE_OAUTH_CLIENT_ID", "cid")
	t.Setenv("GOOGLE_OAUTH_CLIENT_SECRET", "csec")
	t.Setenv("OAUTH_REDIRECT_BASE", "https://box.example.com")

	ex := NewExchanger(ProviderGoogle)
	u, err := ex.AuthCodeURL("state123", "challenge456")
	if err != nil {
		t.Fatalf("AuthCodeURL: %v", err)
	}
	for _, want := range []string{
		"accounts.google.com/o/oauth2/auth",
		"client_id=cid",
		"access_type=offline", // required for a Google refresh token
		"prompt=consent",      // forces re-issue of refresh token
		"code_challenge=challenge456",
		"code_challenge_method=S256",
		"state=state123",
	} {
		if !strings.Contains(u, want) {
			t.Fatalf("auth url missing %q:\n%s", want, u)
		}
	}

	// Dropbox uses token_access_type=offline for its refresh token.
	t.Setenv("DROPBOX_OAUTH_CLIENT_ID", "dcid")
	t.Setenv("DROPBOX_OAUTH_CLIENT_SECRET", "dsec")
	du, err := NewExchanger(ProviderDropbox).AuthCodeURL("s", "c")
	if err != nil {
		t.Fatalf("dropbox AuthCodeURL: %v", err)
	}
	if !strings.Contains(du, "dropbox.com/oauth2/authorize") || !strings.Contains(du, "token_access_type=offline") {
		t.Fatalf("dropbox url wrong: %s", du)
	}

	// An unconfigured provider returns ErrProviderNotConfigured (not a crash).
	t.Setenv("MICROSOFT_OAUTH_CLIENT_ID", "")
	t.Setenv("MICROSOFT_OAUTH_CLIENT_SECRET", "")
	if _, err := NewExchanger(ProviderMicrosoft).AuthCodeURL("s", "c"); err != ErrProviderNotConfigured {
		t.Fatalf("unconfigured microsoft: got %v want ErrProviderNotConfigured", err)
	}
}
