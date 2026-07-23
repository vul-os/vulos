package cproutes

// routes_oauthlogin_test.go — adversarial + happy-path tests for social login
// (Vulos as OAuth CLIENT). Proves the load-bearing rules:
//   - mandatory password: a social sign-up cannot use the account until it sets one
//   - safe linking: a verified-email collision never takes over a pre-existing
//     account without proving its password
//   - PKCE/state (CSRF) enforced on the authorize→callback round-trip
//   - provider-not-configured degrades cleanly

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/vul-os/vulos-management/pkg/auth"
	"github.com/vul-os/vulos-management/pkg/oauthclient"
)

const oauthTestPassword = "securepass1234!"

// mkIDToken builds an unsigned compact JWT for the fake token endpoint.
func mkIDToken(t *testing.T, claims map[string]any) string {
	t.Helper()
	hdr := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	pj, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("marshal claims: %v", err)
	}
	return hdr + "." + base64.RawURLEncoding.EncodeToString(pj) + "." +
		base64.RawURLEncoding.EncodeToString([]byte("x"))
}

// setupOAuthMux wires auth + oauth-login routes with a fake "test" provider whose
// token endpoint always returns idToken.
func setupOAuthMux(t *testing.T, idToken string) (*http.ServeMux, *auth.Store) {
	t.Helper()
	oauthLoginSecureCookie = false
	st := openSessionTestStore(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "at", "id_token": idToken})
	}))
	t.Cleanup(srv.Close)

	reg := oauthclient.NewRegistryFromEnv()
	reg.SetHTTPClient(srv.Client())
	reg.Register(oauthclient.Provider{
		ID: "test", DisplayName: "Test", ClientID: "cid", ClientSecret: "sec",
		AuthURL: "https://idp.example/authorize", TokenURL: srv.URL,
		Scopes: []string{"openid", "email"},
	})

	mux := http.NewServeMux()
	RegisterAuthRoutes(mux, st)
	RegisterOAuthLoginRoutes(mux, st, reg)
	return mux, st
}

// startFlow performs GET /start and returns the transient cookie + the state param.
func startFlow(t *testing.T, mux *http.ServeMux, provider string) (*http.Cookie, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/auth/oauth/"+provider+"/start", nil)
	req.RemoteAddr = "127.0.0.1:5555"
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusFound {
		t.Fatalf("start: expected 302, got %d %s", rr.Code, rr.Body.String())
	}
	loc, _ := url.Parse(rr.Header().Get("Location"))
	state := loc.Query().Get("state")
	if state == "" {
		t.Fatal("start: no state in authorize URL")
	}
	var flow *http.Cookie
	for _, c := range rr.Result().Cookies() {
		if c.Name == oauthFlowCookie {
			flow = c
		}
	}
	if flow == nil {
		t.Fatal("start: no vc_oauth cookie set")
	}
	return flow, state
}

// callback performs GET /callback and returns the recorder.
func callback(t *testing.T, mux *http.ServeMux, provider, code, state string, flow *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet,
		"/api/auth/oauth/"+provider+"/callback?code="+url.QueryEscape(code)+"&state="+url.QueryEscape(state), nil)
	req.RemoteAddr = "127.0.0.1:5555"
	if flow != nil {
		req.AddCookie(flow)
	}
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	return rr
}

func sessionCookie(rr *httptest.ResponseRecorder) string {
	for _, c := range rr.Result().Cookies() {
		if c.Name == auth.SessionCookieName && c.Value != "" && c.MaxAge >= 0 {
			return c.Value
		}
	}
	return ""
}

func verifiedIDToken(t *testing.T, sub, email string, verified bool) string {
	return mkIDToken(t, map[string]any{
		"iss": "https://idp.example", "sub": sub, "aud": "cid",
		"exp": time.Now().Add(time.Hour).Unix(), "email": email, "email_verified": verified,
	})
}

// ── provider discovery + not-configured ──────────────────────────────────────

func TestOAuthProviders_ListsConfigured(t *testing.T) {
	mux, _ := setupOAuthMux(t, verifiedIDToken(t, "s", "a@example.com", true))
	req := httptest.NewRequest(http.MethodGet, "/api/auth/oauth/providers", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("providers: %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), `"test"`) {
		t.Errorf("providers body missing test provider: %s", rr.Body.String())
	}
}

func TestOAuthStart_UnconfiguredProvider_DegradesCleanly(t *testing.T) {
	mux, _ := setupOAuthMux(t, verifiedIDToken(t, "s", "a@example.com", true))
	// google is NOT registered in the test registry.
	req := httptest.NewRequest(http.MethodGet, "/api/auth/oauth/google/start", nil)
	req.RemoteAddr = "127.0.0.1:5555"
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("unconfigured start: expected 404, got %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "provider_not_configured") {
		t.Errorf("expected provider_not_configured, got %s", rr.Body.String())
	}
}

// ── PKCE / state (CSRF) enforcement ──────────────────────────────────────────

func TestOAuthStart_SetsPKCEChallenge(t *testing.T) {
	mux, _ := setupOAuthMux(t, verifiedIDToken(t, "s", "a@example.com", true))
	req := httptest.NewRequest(http.MethodGet, "/api/auth/oauth/test/start", nil)
	req.RemoteAddr = "127.0.0.1:5555"
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	loc, _ := url.Parse(rr.Header().Get("Location"))
	q := loc.Query()
	if q.Get("code_challenge") == "" || q.Get("code_challenge_method") != "S256" {
		t.Errorf("authorize URL missing S256 PKCE: %s", rr.Header().Get("Location"))
	}
}

func TestOAuthCallback_StateMismatch_Rejected(t *testing.T) {
	mux, _ := setupOAuthMux(t, verifiedIDToken(t, "s", "a@example.com", true))
	flow, _ := startFlow(t, mux, "test")
	rr := callback(t, mux, "test", "code", "WRONG-STATE", flow)
	if rr.Code != http.StatusFound || !strings.Contains(rr.Header().Get("Location"), "oauth_state_mismatch") {
		t.Fatalf("state mismatch not rejected: %d %s", rr.Code, rr.Header().Get("Location"))
	}
	if sessionCookie(rr) != "" {
		t.Error("state mismatch issued a session — CSRF hole")
	}
}

func TestOAuthCallback_MissingCookie_Rejected(t *testing.T) {
	mux, _ := setupOAuthMux(t, verifiedIDToken(t, "s", "a@example.com", true))
	_, state := startFlow(t, mux, "test")
	rr := callback(t, mux, "test", "code", state, nil) // no cookie
	if !strings.Contains(rr.Header().Get("Location"), "oauth_state_missing") {
		t.Fatalf("missing state cookie not rejected: %s", rr.Header().Get("Location"))
	}
	if sessionCookie(rr) != "" {
		t.Error("missing cookie issued a session")
	}
}

// ── mandatory password on a social sign-up ───────────────────────────────────

func TestOAuthSignup_ForcesPasswordBeforeUsable(t *testing.T) {
	mux, _ := setupOAuthMux(t, verifiedIDToken(t, "new-sub", "newuser@example.com", true))
	flow, state := startFlow(t, mux, "test")
	rr := callback(t, mux, "test", "code", state, flow)

	if got := rr.Header().Get("Location"); got != consolePath(setPasswordPath) {
		t.Fatalf("new social signup: expected redirect to %s, got %s", consolePath(setPasswordPath), got)
	}
	sess := sessionCookie(rr)
	if sess == "" {
		t.Fatal("new social signup: no session issued for the set-password step")
	}

	// /me must flag that a password is still required (account not usable yet).
	me := getWithCookie(mux, "/api/auth/me", sess)
	if me.Code != http.StatusOK {
		t.Fatalf("/me: %d", me.Code)
	}
	var meBody struct {
		Email                 string `json:"email"`
		PasswordSetupRequired bool   `json:"password_setup_required"`
	}
	_ = json.Unmarshal(me.Body.Bytes(), &meBody)
	if meBody.Email != "newuser@example.com" {
		t.Errorf("/me email = %q", meBody.Email)
	}
	if !meBody.PasswordSetupRequired {
		t.Error("password_setup_required = false on a password-less social account")
	}

	// The account must NOT be usable via a password login (it has none).
	loginBody := `{"email":"newuser@example.com","password":"anything-at-all-123"}`
	lr := postJSONNoCookie(mux, "/api/auth/login", loginBody)
	if lr.Code != http.StatusUnauthorized {
		t.Errorf("password-less account login: expected 401, got %d", lr.Code)
	}

	// Set the mandatory password → account becomes usable.
	sp := postJSONWithCookie(mux, "/api/auth/password/set-initial", sess, `{"password":"`+oauthTestPassword+`"}`)
	if sp.Code != http.StatusOK {
		t.Fatalf("set-initial: expected 200, got %d %s", sp.Code, sp.Body.String())
	}
	if !strings.Contains(sp.Body.String(), "recovery_codes") {
		t.Error("set-initial did not finalise the account (no recovery codes)")
	}

	// /me no longer flags password setup.
	me2 := getWithCookie(mux, "/api/auth/me", sess)
	var meBody2 struct {
		PasswordSetupRequired bool `json:"password_setup_required"`
	}
	_ = json.Unmarshal(me2.Body.Bytes(), &meBody2)
	if meBody2.PasswordSetupRequired {
		t.Error("password_setup_required still true after set-initial")
	}

	// Setting it again must be refused (this endpoint never overwrites a password).
	sp2 := postJSONWithCookie(mux, "/api/auth/password/set-initial", sess, `{"password":"anotherpass9876!"}`)
	if sp2.Code != http.StatusConflict {
		t.Errorf("second set-initial: expected 409, got %d", sp2.Code)
	}

	// And a password login now works.
	lr2 := postJSONNoCookie(mux, "/api/auth/login", `{"email":"newuser@example.com","password":"`+oauthTestPassword+`"}`)
	if lr2.Code != http.StatusOK {
		t.Errorf("login after set-initial: expected 200, got %d %s", lr2.Code, lr2.Body.String())
	}
}

// ── safe linking: verified-email collision must not take over ────────────────

func TestOAuthCollision_RequiresPasswordProof_NoTakeover(t *testing.T) {
	mux, _ := setupOAuthMux(t, verifiedIDToken(t, "ext-sub", "collide@vulos.org", true))
	// Pre-existing native account with the SAME email the social provider asserts.
	signupAndGetToken(t, mux, "collide") // → collide@vulos.org / oauthTestPassword

	flow, state := startFlow(t, mux, "test")
	rr := callback(t, mux, "test", "code", state, flow)

	loc := rr.Header().Get("Location")
	if !strings.HasPrefix(loc, consolePath(linkAccountPath)) {
		t.Fatalf("collision: expected redirect to %s, got %s", consolePath(linkAccountPath), loc)
	}
	if sessionCookie(rr) != "" {
		t.Fatal("collision SILENTLY issued a session — account takeover")
	}
	u, _ := url.Parse(loc)
	linkToken := u.Query().Get("token")
	if linkToken == "" {
		t.Fatal("collision: no link token in redirect")
	}

	// Wrong password → no link.
	bad := postJSONNoCookie(mux, "/api/auth/oauth/link/confirm",
		`{"link_token":"`+linkToken+`","password":"wrong-password-xyz"}`)
	if bad.Code != http.StatusUnauthorized {
		t.Fatalf("link with wrong password: expected 401, got %d", bad.Code)
	}
	if sessionCookie(bad) != "" {
		t.Fatal("failed link proof still issued a session")
	}

	// Correct password → linked + signed in.
	ok := postJSONNoCookie(mux, "/api/auth/oauth/link/confirm",
		`{"link_token":"`+linkToken+`","password":"`+oauthTestPassword+`"}`)
	if ok.Code != http.StatusOK {
		t.Fatalf("link with correct password: expected 200, got %d %s", ok.Code, ok.Body.String())
	}
	if sessionCookie(ok) == "" {
		t.Fatal("successful link did not sign the user in")
	}

	// A subsequent social login for the SAME subject now signs in directly.
	flow2, state2 := startFlow(t, mux, "test")
	rr2 := callback(t, mux, "test", "code", state2, flow2)
	if sessionCookie(rr2) == "" {
		t.Fatal("linked social login did not issue a session on repeat")
	}
	if strings.HasPrefix(rr2.Header().Get("Location"), consolePath(linkAccountPath)) {
		t.Error("linked social login incorrectly re-prompted for linking")
	}
}

func TestOAuthCollision_UnverifiedEmail_Refused(t *testing.T) {
	mux, _ := setupOAuthMux(t, verifiedIDToken(t, "ext2", "collide2@vulos.org", false /* NOT verified */))
	signupAndGetToken(t, mux, "collide2")

	flow, state := startFlow(t, mux, "test")
	rr := callback(t, mux, "test", "code", state, flow)
	if !strings.Contains(rr.Header().Get("Location"), "email_unverified") {
		t.Fatalf("unverified collision: expected email_unverified refusal, got %s", rr.Header().Get("Location"))
	}
	if sessionCookie(rr) != "" {
		t.Fatal("unverified collision issued a session")
	}
	if strings.Contains(rr.Header().Get("Location"), consolePath(linkAccountPath)) {
		t.Error("unverified collision offered a link token")
	}
}

// postJSON posts a JSON body with no cookie.
func postJSONNoCookie(mux *http.ServeMux, path, bodyJSON string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(bodyJSON))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "127.0.0.1:5555"
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	return rr
}
