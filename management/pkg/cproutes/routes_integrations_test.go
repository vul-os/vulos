// routes_integrations_test.go — HTTP tests for the OAuth integration broker
// (INTEG-01/02). Uses in-memory stores + a fake Exchanger; no real Google calls.
package cproutes

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/vul-os/vulos-management/pkg/auth"
	"github.com/vul-os/vulos-management/pkg/integrations"
)

const integTestULID = "01ARZ3NDEKTSV4RRFFQ69G5FAV"

// fakeExchanger implements integrations.Exchanger without touching Google.
type fakeExchanger struct {
	exchangeTok *integrations.Token
	exchangeErr error
	refreshTok  *integrations.Token
	refreshErr  error
}

func (f *fakeExchanger) AuthCodeURL(state string) (string, error) {
	return "https://accounts.google.test/o/oauth2/auth?state=" + url.QueryEscape(state), nil
}
func (f *fakeExchanger) Exchange(context.Context, string) (*integrations.Token, error) {
	return f.exchangeTok, f.exchangeErr
}
func (f *fakeExchanger) Refresh(context.Context, string) (*integrations.Token, error) {
	return f.refreshTok, f.refreshErr
}
func (f *fakeExchanger) Revoke(context.Context, string) error { return nil }

// fakeResolver maps device ULID → owning account.
type fakeResolver struct{ owner map[string]string }

func (f fakeResolver) OwnerOf(_ context.Context, ulid string) (string, error) {
	if id, ok := f.owner[ulid]; ok {
		return id, nil
	}
	return "", errors.New("not enrolled")
}

type integEnv struct {
	mux         *http.ServeMux
	svc         *integrations.Service
	authStore   *auth.Store
	token       string
	accountID   string
	stateSecret []byte
	deviceKeys  integrations.DeviceKeyStore
	caPriv      ed25519.PrivateKey // management-CA key for device-cert tests
	caPub       ed25519.PublicKey
	devicePriv  *ecdsa.PrivateKey // lazily-enrolled per-device key (ensureDeviceKey)
}

func newIntegEnv(t *testing.T, ex integrations.Exchanger) *integEnv {
	t.Helper()
	t.Setenv("GOOGLE_OAUTH_CLIENT_ID", "test-client-id") // makes Configured() true
	t.Setenv("FLEET_REQUIRE_DEVICE_SIG", "1")
	t.Setenv("DEVICE_SHARED_SECRET", "device-shared-secret")
	// The token mint now ALWAYS requires a per-device key (or owner-attested cert);
	// the fleet-wide HMAC is only the enrollment bootstrap channel. Tests that mint
	// enroll a per-device key first (see env.ensureDeviceKey / env.mint).

	authStore, err := openAuthStoreForTest("file::memory:?_pragma=journal_mode(WAL)", []byte("test-secret"))
	if err != nil {
		t.Fatalf("OpenAuthStore: %v", err)
	}
	t.Cleanup(func() { authStore.Close() })

	u, token, err := authStore.Signup(context.Background(), "integ-test@example.com", "password-1234", "127.0.0.1", "test-agent")
	if err != nil {
		t.Fatalf("Signup: %v", err)
	}

	kek := make([]byte, 32)
	for i := range kek {
		kek[i] = byte(i + 7)
	}
	svc := integrations.NewService(integrations.NewMemStore(), ex, kek)

	stateSecret := []byte("cp-shared-secret")
	resolver := fakeResolver{owner: map[string]string{integTestULID: u.ID}}

	deviceKeys := integrations.NewMemDeviceKeyStore()
	caPub, caPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("gen CA key: %v", err)
	}

	mux := http.NewServeMux()
	RegisterIntegrationsRoutes(mux, svc, authStore, stateSecret, resolver, deviceKeys, caPub)

	return &integEnv{mux: mux, svc: svc, authStore: authStore, token: token, accountID: u.ID, stateSecret: stateSecret, deviceKeys: deviceKeys, caPriv: caPriv, caPub: caPub}
}

func (e *integEnv) req(method, path string, withSession bool) *http.Request {
	r := httptest.NewRequest(method, path, nil)
	if withSession {
		r.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: e.token})
	}
	return r
}

// connect drives /start → /callback to establish a connection, returning the
// callback recorder.
func (e *integEnv) connect(t *testing.T) {
	t.Helper()
	// /start → 302 with state cookie + Location carrying the state.
	rrStart := httptest.NewRecorder()
	e.mux.ServeHTTP(rrStart, e.req(http.MethodGet, "/api/integrations/google/start", true))
	if rrStart.Code != http.StatusFound {
		t.Fatalf("start: want 302, got %d: %s", rrStart.Code, rrStart.Body.String())
	}
	loc, _ := url.Parse(rrStart.Header().Get("Location"))
	state := loc.Query().Get("state")
	if state == "" {
		t.Fatal("start: no state in redirect")
	}
	var stateCookie *http.Cookie
	for _, c := range rrStart.Result().Cookies() {
		if c.Name == "vc_integ_state" {
			stateCookie = c
		}
	}
	if stateCookie == nil {
		t.Fatal("start: no state cookie set")
	}

	// /callback with code + state + both cookies.
	rrCb := httptest.NewRecorder()
	cb := e.req(http.MethodGet, "/api/integrations/google/callback?code=auth-code&state="+url.QueryEscape(state), true)
	cb.AddCookie(stateCookie)
	e.mux.ServeHTTP(rrCb, cb)
	if rrCb.Code != http.StatusFound {
		t.Fatalf("callback: want 302, got %d: %s", rrCb.Code, rrCb.Body.String())
	}
	if loc := rrCb.Header().Get("Location"); loc == "" || !contains(loc, "status=connected") {
		t.Fatalf("callback: unexpected redirect %q", loc)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func validToken() *integrations.Token {
	return &integrations.Token{
		AccessToken:  "access-1",
		RefreshToken: "refresh-1",
		Expiry:       time.Now().Add(time.Hour),
		Scopes:       "openid email",
		Email:        "u@gmail.com",
		Sub:          "sub-1",
	}
}

// ---------------------------------------------------------------------------
// start / callback
// ---------------------------------------------------------------------------

func TestStartRequiresSession(t *testing.T) {
	env := newIntegEnv(t, &fakeExchanger{})
	rr := httptest.NewRecorder()
	env.mux.ServeHTTP(rr, env.req(http.MethodGet, "/api/integrations/google/start", false))
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", rr.Code)
	}
}

func TestConnectFlowAndStatus(t *testing.T) {
	env := newIntegEnv(t, &fakeExchanger{exchangeTok: validToken()})
	env.connect(t)

	// /status reflects the connection.
	rr := httptest.NewRecorder()
	env.mux.ServeHTTP(rr, env.req(http.MethodGet, "/api/integrations/google/status", true))
	if rr.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d", rr.Code)
	}
	var st map[string]any
	json.NewDecoder(rr.Body).Decode(&st)
	if st["connected"] != true {
		t.Fatalf("status: expected connected, got %+v", st)
	}
	if st["email"] != "u@gmail.com" {
		t.Fatalf("status: expected email, got %+v", st)
	}
}

func TestStartNotConfigured(t *testing.T) {
	env := newIntegEnv(t, &fakeExchanger{})
	// Remove the OAuth client id so Configured() is false.
	t.Setenv("GOOGLE_OAUTH_CLIENT_ID", "")
	rr := httptest.NewRecorder()
	env.mux.ServeHTTP(rr, env.req(http.MethodGet, "/api/integrations/google/start", true))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("not configured: want 503, got %d", rr.Code)
	}
}

func TestCallbackUserDenied(t *testing.T) {
	env := newIntegEnv(t, &fakeExchanger{exchangeTok: validToken()})
	rr := httptest.NewRecorder()
	cb := env.req(http.MethodGet, "/api/integrations/google/callback?error=access_denied", true)
	env.mux.ServeHTTP(rr, cb)
	if rr.Code != http.StatusFound {
		t.Fatalf("denied: want 302, got %d", rr.Code)
	}
	if loc := rr.Header().Get("Location"); !contains(loc, "status=denied") {
		t.Fatalf("denied: unexpected redirect %q", loc)
	}
}

func TestListConnections(t *testing.T) {
	env := newIntegEnv(t, &fakeExchanger{exchangeTok: validToken()})
	env.connect(t)
	rr := httptest.NewRecorder()
	env.mux.ServeHTTP(rr, env.req(http.MethodGet, "/api/integrations", true))
	if rr.Code != http.StatusOK {
		t.Fatalf("list: want 200, got %d", rr.Code)
	}
	var list []map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&list); err != nil {
		t.Fatalf("list decode: %v", err)
	}
	if len(list) != 1 || list[0]["provider"] != "google" || list[0]["email"] != "u@gmail.com" {
		t.Fatalf("list: unexpected payload %+v", list)
	}
}

func TestListRequiresSession(t *testing.T) {
	env := newIntegEnv(t, &fakeExchanger{})
	rr := httptest.NewRecorder()
	env.mux.ServeHTTP(rr, env.req(http.MethodGet, "/api/integrations", false))
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("list no session: want 401, got %d", rr.Code)
	}
}

func TestCallbackRejectsBadState(t *testing.T) {
	env := newIntegEnv(t, &fakeExchanger{exchangeTok: validToken()})
	// No state cookie + arbitrary state → 400.
	rr := httptest.NewRecorder()
	cb := env.req(http.MethodGet, "/api/integrations/google/callback?code=c&state=forged", true)
	env.mux.ServeHTTP(rr, cb)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("want 400 for bad state, got %d", rr.Code)
	}
}

func TestDisconnect(t *testing.T) {
	env := newIntegEnv(t, &fakeExchanger{exchangeTok: validToken()})
	env.connect(t)
	rr := httptest.NewRecorder()
	env.mux.ServeHTTP(rr, env.req(http.MethodDelete, "/api/integrations/google", true))
	if rr.Code != http.StatusNoContent {
		t.Fatalf("disconnect: want 204, got %d", rr.Code)
	}
	// status now disconnected.
	rr = httptest.NewRecorder()
	env.mux.ServeHTTP(rr, env.req(http.MethodGet, "/api/integrations/google/status", true))
	var st map[string]any
	json.NewDecoder(rr.Body).Decode(&st)
	if st["connected"] != false {
		t.Fatalf("expected disconnected, got %+v", st)
	}
}

// ---------------------------------------------------------------------------
// device-authenticated token mint
// ---------------------------------------------------------------------------

func integSig(secret, ulid string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("integrations:token:google:" + ulid))
	return hex.EncodeToString(mac.Sum(nil))
}

func TestMintTokenHappyPath(t *testing.T) {
	env := newIntegEnv(t, &fakeExchanger{exchangeTok: validToken()})
	env.connect(t)

	rr := env.mint(t, "google", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("mint: want 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var out map[string]any
	json.NewDecoder(rr.Body).Decode(&out)
	if out["access_token"] != "access-1" {
		t.Fatalf("mint: want cached access-1, got %+v", out)
	}
}

func TestMintTokenRateLimited(t *testing.T) {
	env := newIntegEnv(t, &fakeExchanger{exchangeTok: validToken()})
	env.connect(t)

	// The default limit is 30/min per ULID. The first 30 succeed; the 31st is 429.
	// All are authenticated with the env's enrolled per-device key.
	for i := 0; i < 30; i++ {
		if rr := env.mint(t, "google", ""); rr.Code != http.StatusOK {
			t.Fatalf("request %d: want 200, got %d", i, rr.Code)
		}
	}
	if rr := env.mint(t, "google", ""); rr.Code != http.StatusTooManyRequests {
		t.Fatalf("31st request: want 429, got %d", rr.Code)
	}
}

func TestMintTokenBadSig(t *testing.T) {
	env := newIntegEnv(t, &fakeExchanger{exchangeTok: validToken()})
	env.connect(t)
	// Enroll a real device key, then present a signature from a DIFFERENT key →
	// the pinned-key check rejects it.
	env.ensureDeviceKey(t)
	attacker, _ := genDeviceKey(t)
	if rr := env.mintWithDeviceSig(t, attacker, integTestULID); rr.Code != http.StatusUnauthorized {
		t.Fatalf("mint wrong device key: want 401, got %d", rr.Code)
	}
}

func TestMintTokenMissingHeaders(t *testing.T) {
	env := newIntegEnv(t, &fakeExchanger{exchangeTok: validToken()})
	env.connect(t)
	rr := httptest.NewRecorder()
	env.mux.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/integrations/google/token", nil))
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("mint no headers: want 401, got %d", rr.Code)
	}
}

func TestMintTokenNotConnected(t *testing.T) {
	// Valid device sig + enrolled device, but the account never connected Google.
	env := newIntegEnv(t, &fakeExchanger{exchangeTok: validToken()})
	rr := env.mint(t, "google", "")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("mint not connected: want 404, got %d: %s", rr.Code, rr.Body.String())
	}
}

// driveToken is a connected-account token whose grant includes Drive read.
func driveToken() *integrations.Token {
	return &integrations.Token{
		AccessToken:  "access-drive",
		RefreshToken: "refresh-1",
		Expiry:       time.Now().Add(time.Hour),
		Scopes:       "openid email " + integrations.ScopeDriveReadonly,
		Email:        "u@gmail.com",
		Sub:          "sub-1",
	}
}

// TestMintTokenDriveScopeGranted: ?scope=drive on a Drive-authorized connection
// mints the (same) Google access token and echoes the required scope.
func TestMintTokenDriveScopeGranted(t *testing.T) {
	env := newIntegEnv(t, &fakeExchanger{exchangeTok: driveToken()})
	env.connect(t)

	rr := env.mint(t, "google", "drive")
	if rr.Code != http.StatusOK {
		t.Fatalf("mint drive: want 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var out map[string]any
	json.NewDecoder(rr.Body).Decode(&out)
	if out["access_token"] != "access-drive" {
		t.Fatalf("mint drive: want access-drive, got %+v", out)
	}
	if out["required_scope"] != integrations.ScopeDriveReadonly {
		t.Fatalf("mint drive: want required_scope=%s, got %+v", integrations.ScopeDriveReadonly, out["required_scope"])
	}
}

// TestMintTokenDriveScopeNotGranted: ?scope=drive on a connection that never
// authorized Drive → 403 (reconnect to grant), no token leaked.
func TestMintTokenDriveScopeNotGranted(t *testing.T) {
	env := newIntegEnv(t, &fakeExchanger{exchangeTok: validToken()}) // scopes: openid email
	env.connect(t)

	rr := env.mint(t, "google", "drive")
	if rr.Code != http.StatusForbidden {
		t.Fatalf("mint drive not granted: want 403, got %d: %s", rr.Code, rr.Body.String())
	}
	if contains(rr.Body.String(), "access-1") {
		t.Fatal("403 response must not contain the access token")
	}
}

// TestMintTokenUnknownScopeHint: an unrecognized scope alias fails fast with 400.
func TestMintTokenUnknownScopeHint(t *testing.T) {
	env := newIntegEnv(t, &fakeExchanger{exchangeTok: driveToken()})
	env.connect(t)

	rr := env.mint(t, "google", "bogus")
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("mint unknown scope: want 400, got %d: %s", rr.Code, rr.Body.String())
	}
}

// TestMintTokenUnknownDevice: an enrolled device whose ULID is owned by no
// account is rejected at owner resolution (403) — even with a valid per-device
// signature. (Enrollment does not assert ownership; the mint does.)
func TestMintTokenUnknownDevice(t *testing.T) {
	env := newIntegEnv(t, &fakeExchanger{exchangeTok: validToken()})
	env.connect(t)
	const otherULID = "01BX5ZZKBKACTAV9WEVGEMMVRZ" // valid ULID, owned by no account
	priv, der := genDeviceKey(t)
	if rr := env.registerDevice(t, priv, der, otherULID); rr.Code != http.StatusOK {
		t.Fatalf("register other device: want 200, got %d: %s", rr.Code, rr.Body.String())
	}
	rr := env.mintWithDeviceSig(t, priv, otherULID)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("mint unknown device: want 403, got %d: %s", rr.Code, rr.Body.String())
	}
}
