// routes_oauthprovider_test.go — HTTP tests for the OAuth2/OIDC provider
// (OAUTHP-01): client registration + ownership gating, the authorize→consent→
// token→userinfo flow with PKCE, and redirect/secret validation. Uses an
// in-memory auth store; no external calls.
package cproutes

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/vul-os/vulos-management/pkg/auth"
	"github.com/vul-os/vulos-management/pkg/cpdb"
	"github.com/vul-os/vulos-management/pkg/oauthprovider"
)

type opEnv struct {
	mux       *http.ServeMux
	authStore *auth.Store
	svc       *oauthprovider.Service
	token     string // session token
	userID    string
}

func newOPEnv(t *testing.T) *opEnv {
	t.Helper()
	authStore, err := openAuthStoreForTest("file:optest_auth?mode=memory&cache=shared", []byte("test-secret"))
	if err != nil {
		t.Fatalf("OpenAuthStore: %v", err)
	}
	t.Cleanup(func() { authStore.Close() })

	u, token, err := authStore.Signup(context.Background(), "dev@vulos.test", "password-1234", "127.0.0.1", "agent")
	if err != nil {
		t.Fatalf("Signup: %v", err)
	}

	opDB, err := cpdb.OpenSQLiteDSN("file:optest_op?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("cpdb open: %v", err)
	}
	store, err := oauthprovider.Open(opDB)
	if err != nil {
		t.Fatalf("oauthprovider.Open: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	svc, err := oauthprovider.NewService(context.Background(), store, authStore, "https://vulos.test")
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	mux := http.NewServeMux()
	RegisterOAuthProviderRoutes(mux, svc, authStore)
	return &opEnv{mux: mux, authStore: authStore, svc: svc, token: token, userID: u.ID}
}

func (e *opEnv) do(method, path string, body string, withSession bool) *httptest.ResponseRecorder {
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	if withSession {
		r.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: e.token})
	}
	rr := httptest.NewRecorder()
	e.mux.ServeHTTP(rr, r)
	return rr
}

func (e *opEnv) doForm(path string, form url.Values) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	e.mux.ServeHTTP(rr, r)
	return rr
}

func pkce() (verifier, challenge string) {
	verifier = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ012345"
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:])
}

// createApp registers a confidential client via the dev-console API.
func (e *opEnv) createApp(t *testing.T, redirect string, scopes []string) (clientID, secret string) {
	t.Helper()
	b, _ := json.Marshal(map[string]any{
		"name":          "Test App",
		"redirect_uris": []string{redirect},
		"scopes":        scopes,
		"public":        false,
	})
	rr := e.do(http.MethodPost, "/api/oauth/apps", string(b), true)
	if rr.Code != http.StatusCreated {
		t.Fatalf("createApp: want 201, got %d: %s", rr.Code, rr.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("createApp decode: %v", err)
	}
	cid, _ := resp["client_id"].(string)
	sec, _ := resp["client_secret"].(string)
	if cid == "" || sec == "" {
		t.Fatalf("createApp missing client_id/secret: %v", resp)
	}
	return cid, sec
}

func TestAppCRUDRequiresSessionAndOwnership(t *testing.T) {
	e := newOPEnv(t)

	// Unauthenticated create → 401.
	if rr := e.do(http.MethodGet, "/api/oauth/apps", "", false); rr.Code != http.StatusUnauthorized {
		t.Fatalf("unauth list: want 401, got %d", rr.Code)
	}

	cid, _ := e.createApp(t, "https://app.example.com/cb", []string{"openid", "email"})

	// List shows it, and NEVER leaks a secret.
	rr := e.do(http.MethodGet, "/api/oauth/apps", "", true)
	if rr.Code != http.StatusOK {
		t.Fatalf("list: %d", rr.Code)
	}
	if strings.Contains(rr.Body.String(), "client_secret") {
		t.Fatal("list leaked client_secret")
	}

	// A different user cannot see/delete it (ownership gate → 404).
	other, otherTok, err := e.authStore.Signup(context.Background(), "evil@vulos.test", "password-1234", "127.0.0.1", "agent")
	if err != nil {
		t.Fatalf("signup other: %v", err)
	}
	_ = other
	rOther := httptest.NewRequest(http.MethodGet, "/api/oauth/apps/"+cid, nil)
	rOther.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: otherTok})
	rrOther := httptest.NewRecorder()
	e.mux.ServeHTTP(rrOther, rOther)
	if rrOther.Code != http.StatusNotFound {
		t.Fatalf("cross-owner get: want 404, got %d", rrOther.Code)
	}

	// Owner can rotate the secret (new secret returned once).
	rr = e.do(http.MethodPost, "/api/oauth/apps/"+cid+"/rotate-secret", "", true)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "client_secret") {
		t.Fatalf("rotate: %d %s", rr.Code, rr.Body.String())
	}

	// Owner can delete.
	if rr := e.do(http.MethodDelete, "/api/oauth/apps/"+cid, "", true); rr.Code != http.StatusNoContent {
		t.Fatalf("delete: want 204, got %d", rr.Code)
	}
}

func TestAuthorizeRedirectsToLoginWhenAnonymous(t *testing.T) {
	e := newOPEnv(t)
	cid, _ := e.createApp(t, "https://app.example.com/cb", []string{"openid"})
	_, challenge := pkce()
	q := url.Values{}
	q.Set("response_type", "code")
	q.Set("client_id", cid)
	q.Set("redirect_uri", "https://app.example.com/cb")
	q.Set("scope", "openid")
	q.Set("state", "st")
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")

	rr := e.do(http.MethodGet, "/oauth/authorize?"+q.Encode(), "", false)
	if rr.Code != http.StatusFound {
		t.Fatalf("anon authorize: want 302, got %d", rr.Code)
	}
	loc := rr.Header().Get("Location")
	if !strings.HasPrefix(loc, "/login?return=") {
		t.Fatalf("anon authorize should bounce to login, got %q", loc)
	}
}

func TestAuthorizeInvalidClientNotRedirected(t *testing.T) {
	e := newOPEnv(t)
	_, challenge := pkce()
	q := url.Values{}
	q.Set("response_type", "code")
	q.Set("client_id", "vcid_unknown")
	q.Set("redirect_uri", "https://evil.example.com/cb")
	q.Set("scope", "openid")
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	rr := e.do(http.MethodGet, "/oauth/authorize?"+q.Encode(), "", true)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("unknown client: want 400 (no redirect), got %d loc=%q", rr.Code, rr.Header().Get("Location"))
	}
}

func TestFullFlowThroughHTTP(t *testing.T) {
	e := newOPEnv(t)
	cid, secret := e.createApp(t, "https://app.example.com/cb", []string{"openid", "email"})
	verifier, challenge := pkce()

	// Authorized user hits /oauth/authorize → 302 to /oauth/consent.
	q := url.Values{}
	q.Set("response_type", "code")
	q.Set("client_id", cid)
	q.Set("redirect_uri", "https://app.example.com/cb")
	q.Set("scope", "openid email")
	q.Set("state", "st-123")
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	q.Set("nonce", "nonce-1")
	rr := e.do(http.MethodGet, "/oauth/authorize?"+q.Encode(), "", true)
	if rr.Code != http.StatusFound || !strings.HasPrefix(rr.Header().Get("Location"), "/oauth/consent?") {
		t.Fatalf("authorize (logged in): want 302→/oauth/consent, got %d %q", rr.Code, rr.Header().Get("Location"))
	}

	// Consent info reflects the client + scopes.
	rr = e.do(http.MethodGet, "/api/oauth/authorize/info?"+q.Encode(), "", true)
	if rr.Code != http.StatusOK {
		t.Fatalf("consent info: %d %s", rr.Code, rr.Body.String())
	}
	var info map[string]any
	json.Unmarshal(rr.Body.Bytes(), &info)
	if info["client_name"] != "Test App" {
		t.Fatalf("consent info bad: %v", info)
	}

	// Approve → returns redirect with code.
	dec, _ := json.Marshal(map[string]any{
		"approve":               true,
		"response_type":         "code",
		"client_id":             cid,
		"redirect_uri":          "https://app.example.com/cb",
		"scope":                 "openid email",
		"state":                 "st-123",
		"code_challenge":        challenge,
		"code_challenge_method": "S256",
		"nonce":                 "nonce-1",
	})
	rr = e.do(http.MethodPost, "/api/oauth/authorize/decision", string(dec), true)
	if rr.Code != http.StatusOK {
		t.Fatalf("decision: %d %s", rr.Code, rr.Body.String())
	}
	var decResp map[string]string
	json.Unmarshal(rr.Body.Bytes(), &decResp)
	redir, err := url.Parse(decResp["redirect"])
	if err != nil {
		t.Fatalf("parse redirect: %v", err)
	}
	code := redir.Query().Get("code")
	if code == "" || redir.Query().Get("state") != "st-123" {
		t.Fatalf("bad redirect: %s", decResp["redirect"])
	}

	// Token exchange (confidential client via client_secret_post + PKCE).
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", "https://app.example.com/cb")
	form.Set("client_id", cid)
	form.Set("client_secret", secret)
	form.Set("code_verifier", verifier)
	rr = e.doForm("/oauth/token", form)
	if rr.Code != http.StatusOK {
		t.Fatalf("token: %d %s", rr.Code, rr.Body.String())
	}
	var tok map[string]any
	json.Unmarshal(rr.Body.Bytes(), &tok)
	access, _ := tok["access_token"].(string)
	idtok, _ := tok["id_token"].(string)
	if access == "" || idtok == "" || tok["token_type"] != "Bearer" {
		t.Fatalf("bad token response: %v", tok)
	}

	// userinfo with the access token returns the email.
	r := httptest.NewRequest(http.MethodGet, "/oauth/userinfo", nil)
	r.Header.Set("Authorization", "Bearer "+access)
	rrUI := httptest.NewRecorder()
	e.mux.ServeHTTP(rrUI, r)
	if rrUI.Code != http.StatusOK {
		t.Fatalf("userinfo: %d %s", rrUI.Code, rrUI.Body.String())
	}
	var ui map[string]any
	json.Unmarshal(rrUI.Body.Bytes(), &ui)
	if ui["sub"] != e.userID || ui["email"] != "dev@vulos.test" {
		t.Fatalf("bad userinfo: %v", ui)
	}

	// Replaying the code must fail.
	rr = e.doForm("/oauth/token", form)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("code replay via HTTP: want 400, got %d", rr.Code)
	}
}

func TestTokenWrongSecretReturns401(t *testing.T) {
	e := newOPEnv(t)
	cid, _ := e.createApp(t, "https://app.example.com/cb", []string{"openid"})
	verifier, challenge := pkce()
	v, aerr := e.svc.ValidateAuthorize(context.Background(), oauthprovider.AuthorizeRequest{
		ResponseType: "code", ClientID: cid, RedirectURI: "https://app.example.com/cb",
		Scope: "openid", CodeChallenge: challenge, CodeChallengeMethod: "S256",
	})
	if aerr != nil {
		t.Fatalf("validate: %v", aerr)
	}
	code, _ := e.svc.IssueCode(context.Background(), v, e.userID)

	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", "https://app.example.com/cb")
	form.Set("client_id", cid)
	form.Set("client_secret", "vcsk_wrong")
	form.Set("code_verifier", verifier)
	rr := e.doForm("/oauth/token", form)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("wrong secret: want 401, got %d %s", rr.Code, rr.Body.String())
	}
}

func TestDiscoveryAndJWKS(t *testing.T) {
	e := newOPEnv(t)
	rr := e.do(http.MethodGet, "/.well-known/openid-configuration", "", false)
	if rr.Code != http.StatusOK {
		t.Fatalf("discovery: %d", rr.Code)
	}
	var doc map[string]any
	json.Unmarshal(rr.Body.Bytes(), &doc)
	if doc["issuer"] != "https://vulos.test" || doc["token_endpoint"] != "https://vulos.test/oauth/token" {
		t.Fatalf("bad discovery: %v", doc)
	}
	methods, _ := doc["code_challenge_methods_supported"].([]any)
	if len(methods) != 1 || methods[0] != "S256" {
		t.Fatalf("discovery PKCE methods: %v", doc["code_challenge_methods_supported"])
	}

	rr = e.do(http.MethodGet, "/oauth/jwks", "", false)
	if rr.Code != http.StatusOK {
		t.Fatalf("jwks: %d", rr.Code)
	}
	var jwks map[string][]map[string]any
	json.Unmarshal(rr.Body.Bytes(), &jwks)
	if len(jwks["keys"]) != 1 || jwks["keys"][0]["alg"] != "RS256" {
		t.Fatalf("bad jwks: %s", rr.Body.String())
	}
}

// TestWellKnownJWKSAlias verifies that /.well-known/jwks.json also returns the
// JWKS document (RFC 8414 alias).
func TestWellKnownJWKSAlias(t *testing.T) {
	e := newOPEnv(t)
	rr := e.do(http.MethodGet, "/.well-known/jwks.json", "", false)
	if rr.Code != http.StatusOK {
		t.Fatalf("/.well-known/jwks.json: %d", rr.Code)
	}
	var jwks map[string][]map[string]any
	json.Unmarshal(rr.Body.Bytes(), &jwks)
	if len(jwks["keys"]) != 1 || jwks["keys"][0]["alg"] != "RS256" {
		t.Fatalf("bad jwks at well-known path: %s", rr.Body.String())
	}
}

// TestRFC9207IssInAuthorizationResponse verifies the IdP mix-up defence
// (RFC 9207): the authorization-response redirect carries an `iss` parameter
// equal to the issuer, and the discovery document advertises support. Additive:
// the existing code+state params are unchanged.
func TestRFC9207IssInAuthorizationResponse(t *testing.T) {
	e := newOPEnv(t)
	cid, _ := e.createApp(t, "https://app.example.com/cb", []string{"openid"})
	_, challenge := pkce()

	// Discovery advertises the flag.
	rr := e.do(http.MethodGet, "/.well-known/openid-configuration", "", false)
	if rr.Code != http.StatusOK {
		t.Fatalf("discovery: %d", rr.Code)
	}
	var doc map[string]any
	json.Unmarshal(rr.Body.Bytes(), &doc)
	if doc["authorization_response_iss_parameter_supported"] != true {
		t.Fatalf("discovery must advertise authorization_response_iss_parameter_supported=true, got %v",
			doc["authorization_response_iss_parameter_supported"])
	}

	// Approve via the consent decision endpoint → redirect carries iss + code + state.
	dec, _ := json.Marshal(map[string]any{
		"approve":               true,
		"response_type":         "code",
		"client_id":             cid,
		"redirect_uri":          "https://app.example.com/cb",
		"scope":                 "openid",
		"state":                 "st-iss",
		"code_challenge":        challenge,
		"code_challenge_method": "S256",
	})
	rr = e.do(http.MethodPost, "/api/oauth/authorize/decision", string(dec), true)
	if rr.Code != http.StatusOK {
		t.Fatalf("decision: %d %s", rr.Code, rr.Body.String())
	}
	var decResp map[string]string
	json.Unmarshal(rr.Body.Bytes(), &decResp)
	redir, err := url.Parse(decResp["redirect"])
	if err != nil {
		t.Fatalf("parse redirect: %v", err)
	}
	q := redir.Query()
	if q.Get("iss") != "https://vulos.test" {
		t.Fatalf("authorization response iss=%q, want issuer https://vulos.test", q.Get("iss"))
	}
	// Existing params unchanged (non-breaking).
	if q.Get("code") == "" || q.Get("state") != "st-iss" {
		t.Fatalf("code/state altered by iss addition: %s", decResp["redirect"])
	}
}

// TestOAuth2PathAliases verifies the /oauth2/* endpoint aliases are live.
func TestOAuth2PathAliases(t *testing.T) {
	e := newOPEnv(t)
	cid, _ := e.createApp(t, "https://app.example.com/cb", []string{"openid"})
	_, challenge := pkce()

	// /oauth2/authorize should redirect to login (anonymous).
	q := url.Values{}
	q.Set("response_type", "code")
	q.Set("client_id", cid)
	q.Set("redirect_uri", "https://app.example.com/cb")
	q.Set("scope", "openid")
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	rr := e.do(http.MethodGet, "/oauth2/authorize?"+q.Encode(), "", false)
	if rr.Code != http.StatusFound {
		t.Fatalf("/oauth2/authorize: want 302, got %d", rr.Code)
	}

	// /oauth2/token with a bogus grant should return unsupported_grant_type.
	form := url.Values{"grant_type": {"magic"}, "client_id": {cid}}
	rr = e.doForm("/oauth2/token", form)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("/oauth2/token bad grant: want 400, got %d", rr.Code)
	}

	// /oauth2/userinfo without a token → 401.
	rr = e.do(http.MethodGet, "/oauth2/userinfo", "", false)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("/oauth2/userinfo no token: want 401, got %d", rr.Code)
	}
}

// TestRevokeEndpoint exercises POST /oauth/revoke (and /oauth2/revoke alias).
func TestRevokeEndpoint(t *testing.T) {
	e := newOPEnv(t)
	cid, secret := e.createApp(t, "https://app.example.com/cb", []string{"openid", "email"})
	verifier, challenge := pkce()

	// Full auth-code flow to get tokens.
	dec, _ := json.Marshal(map[string]any{
		"approve": true, "response_type": "code", "client_id": cid,
		"redirect_uri": "https://app.example.com/cb", "scope": "openid email",
		"state": "s", "code_challenge": challenge, "code_challenge_method": "S256",
	})
	rr := e.do(http.MethodPost, "/api/oauth/authorize/decision", string(dec), true)
	if rr.Code != http.StatusOK {
		t.Fatalf("decision: %d %s", rr.Code, rr.Body.String())
	}
	var decResp map[string]string
	json.Unmarshal(rr.Body.Bytes(), &decResp)
	redir, _ := url.Parse(decResp["redirect"])
	code := redir.Query().Get("code")

	form := url.Values{
		"grant_type": {"authorization_code"}, "code": {code},
		"redirect_uri": {"https://app.example.com/cb"}, "client_id": {cid},
		"client_secret": {secret}, "code_verifier": {verifier},
	}
	rr = e.doForm("/oauth/token", form)
	if rr.Code != http.StatusOK {
		t.Fatalf("token: %d %s", rr.Code, rr.Body.String())
	}
	var tok map[string]any
	json.Unmarshal(rr.Body.Bytes(), &tok)
	accessToken, _ := tok["access_token"].(string)
	if accessToken == "" {
		t.Fatal("no access_token in token response")
	}

	// Revoke the access token via /oauth/revoke.
	revokeForm := url.Values{
		"token": {accessToken}, "token_type_hint": {"access_token"},
		"client_id": {cid}, "client_secret": {secret},
	}
	rr = e.doForm("/oauth/revoke", revokeForm)
	if rr.Code != http.StatusOK {
		t.Fatalf("revoke: want 200, got %d %s", rr.Code, rr.Body.String())
	}

	// Userinfo with the revoked token should now fail.
	r := httptest.NewRequest(http.MethodGet, "/oauth/userinfo", nil)
	r.Header.Set("Authorization", "Bearer "+accessToken)
	rrUI := httptest.NewRecorder()
	e.mux.ServeHTTP(rrUI, r)
	if rrUI.Code != http.StatusUnauthorized {
		t.Fatalf("userinfo with revoked token: want 401, got %d", rrUI.Code)
	}

	// Revoking an unknown token via /oauth2/revoke alias is a no-op (still 200).
	rr = e.doForm("/oauth2/revoke", url.Values{
		"token": {"vcrt_bogus_unknown_token"}, "client_id": {cid}, "client_secret": {secret},
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("revoke unknown token via /oauth2/revoke: want 200, got %d", rr.Code)
	}

	// Wrong client secret → 401.
	rr = e.doForm("/oauth/revoke", url.Values{
		"token": {"some-token"}, "client_id": {cid}, "client_secret": {"vcsk_wrong"},
	})
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("revoke wrong secret: want 401, got %d", rr.Code)
	}
}

// TestConsentStoredGrantSkipsScreen verifies that a second authorize request
// (same user, same client, same scopes) auto-approves without hitting the
// consent screen when a grant is already stored.
func TestConsentStoredGrantSkipsScreen(t *testing.T) {
	e := newOPEnv(t)
	cid, secret := e.createApp(t, "https://app.example.com/cb", []string{"openid", "email"})
	verifier, challenge := pkce()

	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {cid},
		"redirect_uri":          {"https://app.example.com/cb"},
		"scope":                 {"openid email"},
		"state":                 {"st"},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
	}

	// First visit: logged-in user → redirected to consent screen.
	rr := e.do(http.MethodGet, "/oauth/authorize?"+q.Encode(), "", true)
	if rr.Code != http.StatusFound || !strings.HasPrefix(rr.Header().Get("Location"), "/oauth/consent?") {
		t.Fatalf("first authorize: want 302→/oauth/consent, got %d %q", rr.Code, rr.Header().Get("Location"))
	}

	// User approves → decision endpoint stores the grant.
	dec, _ := json.Marshal(map[string]any{
		"approve": true, "response_type": "code", "client_id": cid,
		"redirect_uri": "https://app.example.com/cb", "scope": "openid email",
		"state": "st", "code_challenge": challenge, "code_challenge_method": "S256",
	})
	rr = e.do(http.MethodPost, "/api/oauth/authorize/decision", string(dec), true)
	if rr.Code != http.StatusOK {
		t.Fatalf("decision: %d %s", rr.Code, rr.Body.String())
	}
	var decResp map[string]string
	json.Unmarshal(rr.Body.Bytes(), &decResp)
	redir, _ := url.Parse(decResp["redirect"])
	code := redir.Query().Get("code")
	// Consume the code so it's not replayed.
	form := url.Values{
		"grant_type": {"authorization_code"}, "code": {code},
		"redirect_uri": {"https://app.example.com/cb"}, "client_id": {cid},
		"client_secret": {secret}, "code_verifier": {verifier},
	}
	if rr := e.doForm("/oauth/token", form); rr.Code != http.StatusOK {
		t.Fatalf("first exchange: %d %s", rr.Code, rr.Body.String())
	}

	// Second authorize: same user+client+scopes → auto-approve (302 directly to the redirect URI, not /oauth/consent).
	// We need a fresh PKCE pair.
	_, challenge2 := pkce()
	// Override the challenge in query params.
	q.Set("code_challenge", challenge2)
	q.Set("state", "st2")
	rr = e.do(http.MethodGet, "/oauth/authorize?"+q.Encode(), "", true)
	if rr.Code != http.StatusFound {
		t.Fatalf("second authorize: want 302, got %d", rr.Code)
	}
	loc := rr.Header().Get("Location")
	if strings.HasPrefix(loc, "/oauth/consent") {
		t.Fatalf("second authorize should NOT go to consent screen (stored grant), got %q", loc)
	}
	// The redirect target must be the client's redirect URI with a code.
	parsedLoc, _ := url.Parse(loc)
	if parsedLoc.Query().Get("code") == "" {
		t.Fatalf("second authorize auto-approve: expected code in redirect, got %q", loc)
	}
}

// TestConsentListAndRevoke exercises GET/DELETE /api/oauth/consents.
func TestConsentListAndRevoke(t *testing.T) {
	e := newOPEnv(t)
	cid, secret := e.createApp(t, "https://app.example.com/cb", []string{"openid", "email"})
	verifier, challenge := pkce()

	// Build + consume a full flow so a consent is stored.
	dec, _ := json.Marshal(map[string]any{
		"approve": true, "response_type": "code", "client_id": cid,
		"redirect_uri": "https://app.example.com/cb", "scope": "openid email",
		"code_challenge": challenge, "code_challenge_method": "S256",
	})
	rr := e.do(http.MethodPost, "/api/oauth/authorize/decision", string(dec), true)
	if rr.Code != http.StatusOK {
		t.Fatalf("decision: %d", rr.Code)
	}
	var decResp map[string]string
	json.Unmarshal(rr.Body.Bytes(), &decResp)
	redir, _ := url.Parse(decResp["redirect"])
	code := redir.Query().Get("code")
	e.doForm("/oauth/token", url.Values{
		"grant_type": {"authorization_code"}, "code": {code},
		"redirect_uri": {"https://app.example.com/cb"}, "client_id": {cid},
		"client_secret": {secret}, "code_verifier": {verifier},
	})

	// List consents.
	rr = e.do(http.MethodGet, "/api/oauth/consents", "", true)
	if rr.Code != http.StatusOK {
		t.Fatalf("list consents: %d %s", rr.Code, rr.Body.String())
	}
	var consents []map[string]any
	json.Unmarshal(rr.Body.Bytes(), &consents)
	if len(consents) != 1 || consents[0]["client_id"] != cid {
		t.Fatalf("expected 1 consent for client %s, got %v", cid, consents)
	}

	// Revoke.
	rr = e.do(http.MethodDelete, "/api/oauth/consents/"+cid, "", true)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("revoke consent: want 204, got %d %s", rr.Code, rr.Body.String())
	}

	// List is now empty.
	rr = e.do(http.MethodGet, "/api/oauth/consents", "", true)
	json.Unmarshal(rr.Body.Bytes(), &consents)
	if len(consents) != 0 {
		t.Fatalf("expected 0 consents after revoke, got %v", consents)
	}

	// Revoking again → 404.
	rr = e.do(http.MethodDelete, "/api/oauth/consents/"+cid, "", true)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("revoke non-existent consent: want 404, got %d", rr.Code)
	}
}
