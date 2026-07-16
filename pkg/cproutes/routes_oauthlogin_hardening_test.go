package cproutes

// routes_oauthlogin_hardening_test.go — deeper adversarial tests for social login
// (Vulos as OAuth CLIENT), layered on the load-bearing rules already proven in
// routes_oauthlogin_test.go. These probe the crown-jewel invariants harder:
//
//   P1  mandatory-password gate is unbypassable — a password-less social session
//       is refused on a REAL session-gated route + the SSO mint, not just flagged
//       on /me; it works only AFTER set-initial.
//   P2  no takeover via linking — email-case normalization can't dodge the
//       collision; a tampered or expired link-token is refused.
//   P3  oauth-client flow integrity — a tampered flow cookie is refused; the
//       transient cookie is HttpOnly / scoped / short-lived; provider-not-
//       configured degrades cleanly on the callback too.
//   P5  session issuance honours 2FA — a freshly-linked account whose target has
//       TOTP does NOT skip the 2FA step.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// ── P1: the gate is unbypassable on real session-gated surfaces ───────────────

func TestOAuthSignup_PasswordlessSession_DeniedOnGatedRoutes(t *testing.T) {
	mux, _ := setupOAuthMux(t, verifiedIDToken(t, "gate-sub", "gateprobe@example.com", true))
	// NOTE: the SSO product-token mint (registerSSORoutes) is a cloud-side surface
	// (it depends on the commercial product catalogue) and is asserted there; here
	// we prove the password-less gate on the operational /api/auth/sessions route.

	flow, state := startFlow(t, mux, "test")
	rr := callback(t, mux, "test", "code", state, flow)
	if rr.Header().Get("Location") != setPasswordPath {
		t.Fatalf("new social signup: expected redirect to %s, got %s", setPasswordPath, rr.Header().Get("Location"))
	}
	sess := sessionCookie(rr)
	if sess == "" {
		t.Fatal("no session issued for the set-password step")
	}

	// A genuinely session-gated auth route must FAIL CLOSED for this session.
	gated := getWithCookie(mux, "/api/auth/sessions", sess)
	if gated.Code != http.StatusForbidden {
		t.Fatalf("GET /api/auth/sessions with password-less session: expected 403, got %d %s", gated.Code, gated.Body.String())
	}
	if !strings.Contains(gated.Body.String(), "password_setup_required") {
		t.Errorf("gated route body = %q, want password_setup_required", gated.Body.String())
	}

	// /me is the only tolerant surface: it resolves the account so the SPA can
	// route to set-password.
	me := getWithCookie(mux, "/api/auth/me", sess)
	if me.Code != http.StatusOK || !strings.Contains(me.Body.String(), `"password_setup_required":true`) {
		t.Fatalf("/me: %d %s (want 200 + password_setup_required:true)", me.Code, me.Body.String())
	}

	// Set the mandatory password → the same gated route now works.
	sp := postJSONWithCookie(mux, "/api/auth/password/set-initial", sess, `{"password":"`+oauthTestPassword+`"}`)
	if sp.Code != http.StatusOK {
		t.Fatalf("set-initial: %d %s", sp.Code, sp.Body.String())
	}
	gated2 := getWithCookie(mux, "/api/auth/sessions", sess)
	if gated2.Code != http.StatusOK {
		t.Fatalf("GET /api/auth/sessions after set-initial: expected 200, got %d %s", gated2.Code, gated2.Body.String())
	}
}

// ── P2: email-case normalization can't dodge the collision (no takeover) ──────

func TestOAuthCollision_EmailCaseNormalized_NoTakeover(t *testing.T) {
	// Provider asserts the SAME address in different case than the native account.
	mux, _ := setupOAuthMux(t, verifiedIDToken(t, "case-sub", "MixedCase@Vulos.org", true))
	signupAndGetToken(t, mux, "mixedcase") // native mixedcase@vulos.org

	flow, state := startFlow(t, mux, "test")
	rr := callback(t, mux, "test", "code", state, flow)
	loc := rr.Header().Get("Location")
	if !strings.HasPrefix(loc, linkAccountPath) {
		t.Fatalf("case-variant collision: expected link path, got %s", loc)
	}
	if sessionCookie(rr) != "" {
		t.Fatal("case-variant collision issued a session — takeover")
	}
}

// ── P2: tampered / expired link tokens are refused ────────────────────────────

func TestOAuthLink_TamperedToken_Refused(t *testing.T) {
	mux, _ := setupOAuthMux(t, verifiedIDToken(t, "lt-sub", "ltcollide@vulos.org", true))
	signupAndGetToken(t, mux, "ltcollide")
	flow, state := startFlow(t, mux, "test")
	rr := callback(t, mux, "test", "code", state, flow)
	linkToken := mustLinkToken(t, rr)

	// Flip a character in the payload segment → HMAC fails.
	bad := flipFirstByte(linkToken)
	resp := postJSONNoCookie(mux, "/api/auth/oauth/link/confirm",
		`{"link_token":"`+bad+`","password":"`+oauthTestPassword+`"}`)
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("tampered link token: expected 400, got %d %s", resp.Code, resp.Body.String())
	}
	if sessionCookie(resp) != "" {
		t.Fatal("tampered link token issued a session")
	}
}

func TestOAuthLink_ExpiredToken_Refused(t *testing.T) {
	mux, st := setupOAuthMux(t, verifiedIDToken(t, "exp-sub", "expcollide@vulos.org", true))
	uid, _ := signupAndGetToken(t, mux, "expcollide")

	// Forge a well-signed but EXPIRED link token using the store's real secret.
	tok := oauthLinkToken{
		Provider: "test", Subject: "exp-sub", Email: "expcollide@vulos.org",
		UserID: uid, Exp: time.Now().Add(-1 * time.Minute).Unix(),
	}
	payload, _ := json.Marshal(tok)
	expired := signBlob(st.Secret(), payload)

	resp := postJSONNoCookie(mux, "/api/auth/oauth/link/confirm",
		`{"link_token":"`+expired+`","password":"`+oauthTestPassword+`"}`)
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("expired link token: expected 400, got %d %s", resp.Code, resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), "expired") {
		t.Errorf("expired link token body = %q, want 'expired'", resp.Body.String())
	}
	if sessionCookie(resp) != "" {
		t.Fatal("expired link token issued a session")
	}
}

// ── P3: tampered flow cookie is refused; cookie attributes are locked down ─────

func TestOAuthCallback_TamperedCookie_Refused(t *testing.T) {
	mux, _ := setupOAuthMux(t, verifiedIDToken(t, "s", "a@example.com", true))
	flow, state := startFlow(t, mux, "test")
	flow.Value = flipFirstByte(flow.Value) // break the HMAC
	rr := callback(t, mux, "test", "code", state, flow)
	if !strings.Contains(rr.Header().Get("Location"), "oauth_state_invalid") {
		t.Fatalf("tampered flow cookie: expected oauth_state_invalid, got %s", rr.Header().Get("Location"))
	}
	if sessionCookie(rr) != "" {
		t.Fatal("tampered flow cookie issued a session")
	}
}

func TestOAuthStart_FlowCookie_HttpOnlyScopedShortLived(t *testing.T) {
	mux, _ := setupOAuthMux(t, verifiedIDToken(t, "s", "a@example.com", true))
	req := httptest.NewRequest(http.MethodGet, "/api/auth/oauth/test/start", nil)
	req.RemoteAddr = "127.0.0.1:5555"
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	var flow *http.Cookie
	for _, c := range rr.Result().Cookies() {
		if c.Name == oauthFlowCookie {
			flow = c
		}
	}
	if flow == nil {
		t.Fatal("no vc_oauth cookie set")
	}
	if !flow.HttpOnly {
		t.Error("vc_oauth cookie is not HttpOnly (JS-readable state/verifier)")
	}
	if flow.SameSite != http.SameSiteLaxMode {
		t.Errorf("vc_oauth SameSite = %v, want Lax", flow.SameSite)
	}
	if flow.Path != "/api/auth/oauth" {
		t.Errorf("vc_oauth Path = %q, want /api/auth/oauth", flow.Path)
	}
	if flow.MaxAge <= 0 || flow.MaxAge > int(oauthFlowTTL.Seconds()) {
		t.Errorf("vc_oauth MaxAge = %d, want short-lived (0<MaxAge<=%d)", flow.MaxAge, int(oauthFlowTTL.Seconds()))
	}
}

func TestOAuthCallback_UnconfiguredProvider_DegradesCleanly(t *testing.T) {
	mux, _ := setupOAuthMux(t, verifiedIDToken(t, "s", "a@example.com", true))
	// "google" is not registered in the test registry; the callback must not crash.
	req := httptest.NewRequest(http.MethodGet, "/api/auth/oauth/google/callback?code=x&state=y", nil)
	req.RemoteAddr = "127.0.0.1:5555"
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusFound || !strings.Contains(rr.Header().Get("Location"), "provider_not_configured") {
		t.Fatalf("unconfigured callback: expected redirect w/ provider_not_configured, got %d %s", rr.Code, rr.Header().Get("Location"))
	}
	if sessionCookie(rr) != "" {
		t.Fatal("unconfigured callback issued a session")
	}
}

// ── P5: a freshly-linked account does not skip its 2FA ────────────────────────

func TestOAuthLink_HonoursExistingTOTP_NoSkip(t *testing.T) {
	mux, st := setupOAuthMux(t, verifiedIDToken(t, "2fa-sub", "twofa@vulos.org", true))
	uid, _ := signupAndGetToken(t, mux, "twofa")
	e2eEnableTOTP(t, st, uid, "twofa@vulos.org")

	flow, state := startFlow(t, mux, "test")
	rr := callback(t, mux, "test", "code", state, flow)
	linkToken := mustLinkToken(t, rr)

	ok := postJSONNoCookie(mux, "/api/auth/oauth/link/confirm",
		`{"link_token":"`+linkToken+`","password":"`+oauthTestPassword+`"}`)
	if ok.Code != http.StatusOK {
		t.Fatalf("link/confirm: %d %s", ok.Code, ok.Body.String())
	}
	// The link succeeded but 2FA must be REQUIRED — not a full sign-in.
	var body struct {
		Step string `json:"step"`
	}
	_ = json.Unmarshal(ok.Body.Bytes(), &body)
	if body.Step != "totp_required" {
		t.Fatalf("freshly-linked TOTP account: step = %q, want totp_required (2FA must not be skipped)", body.Step)
	}
}

// ── helpers ───────────────────────────────────────────────────────────────────

func mustLinkToken(t *testing.T, rr *httptest.ResponseRecorder) string {
	t.Helper()
	loc := rr.Header().Get("Location")
	if !strings.HasPrefix(loc, linkAccountPath) {
		t.Fatalf("expected collision→link redirect, got %s", loc)
	}
	u, _ := url.Parse(loc)
	tok := u.Query().Get("token")
	if tok == "" {
		t.Fatal("no link token in redirect")
	}
	return tok
}

// flipFirstByte mutates the first base64url character of the payload segment so a
// signed blob's payload no longer matches its HMAC.
func flipFirstByte(signed string) string {
	if signed == "" {
		return "A"
	}
	b := []byte(signed)
	if b[0] == 'A' {
		b[0] = 'B'
	} else {
		b[0] = 'A'
	}
	return string(b)
}
