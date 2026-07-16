// routes_oauthfosite_test.go — end-to-end HTTP tests through the NOW-LIVE
// ory/fosite-served protocol path: refresh-token rotation + replay detection,
// RFC 7662 introspection (active/inactive + client-auth), id_token issuance +
// RS256/JWKS verification of its claims, and PKCE failure at the token endpoint.
//
// These complement routes_oauthprovider_test.go (which pins the authorize→
// consent→token→userinfo contract). They target the behaviours fosite now owns
// that the contract tests do not exercise over HTTP.
package cproutes

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/vul-os/vulos-management/pkg/oauthprovider"
)

// approveToCode drives the consent-decision endpoint and returns the issued
// authorization code from the redirect.
func (e *opEnv) approveToCode(t *testing.T, cid, challenge, scope, state string) string {
	t.Helper()
	dec, _ := json.Marshal(map[string]any{
		"approve": true, "response_type": "code", "client_id": cid,
		"redirect_uri": "https://app.example.com/cb", "scope": scope,
		"state": state, "code_challenge": challenge, "code_challenge_method": "S256",
		"nonce": "nonce-xyz",
	})
	rr := e.do(http.MethodPost, "/api/oauth/authorize/decision", string(dec), true)
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
	if code == "" {
		t.Fatalf("no code in redirect %q", decResp["redirect"])
	}
	return code
}

// tokenExchange performs an authorization_code exchange and returns the parsed
// token response (fails the test on a non-200).
func (e *opEnv) tokenExchange(t *testing.T, cid, secret, code, verifier string) map[string]any {
	t.Helper()
	form := url.Values{
		"grant_type": {"authorization_code"}, "code": {code},
		"redirect_uri": {"https://app.example.com/cb"}, "client_id": {cid},
		"client_secret": {secret}, "code_verifier": {verifier},
	}
	rr := e.doForm("/oauth/token", form)
	if rr.Code != http.StatusOK {
		t.Fatalf("token exchange: %d %s", rr.Code, rr.Body.String())
	}
	var tok map[string]any
	json.Unmarshal(rr.Body.Bytes(), &tok)
	return tok
}

// TestFositeRefreshRotationAndReplayHTTP proves that over HTTP the refresh grant
// rotates the refresh token and that replaying the old refresh token is rejected
// AND revokes the family (the rotated token then also fails).
func TestFositeRefreshRotationAndReplayHTTP(t *testing.T) {
	e := newOPEnv(t)
	cid, secret := e.createApp(t, "https://app.example.com/cb", []string{"openid", "email"})
	verifier, challenge := pkce()

	code := e.approveToCode(t, cid, challenge, "openid email", "st-ref")
	tok := e.tokenExchange(t, cid, secret, code, verifier)
	rt1, _ := tok["refresh_token"].(string)
	if rt1 == "" {
		t.Fatal("no refresh_token issued on initial exchange")
	}
	if tok["token_type"] != "Bearer" {
		t.Fatalf("token_type = %v, want Bearer", tok["token_type"])
	}

	refresh := func(rt string) *httptest.ResponseRecorder {
		return e.doForm("/oauth/token", url.Values{
			"grant_type": {"refresh_token"}, "refresh_token": {rt},
			"client_id": {cid}, "client_secret": {secret},
		})
	}

	// First refresh rotates.
	rr := refresh(rt1)
	if rr.Code != http.StatusOK {
		t.Fatalf("first refresh: %d %s", rr.Code, rr.Body.String())
	}
	var tok2 map[string]any
	json.Unmarshal(rr.Body.Bytes(), &tok2)
	rt2, _ := tok2["refresh_token"].(string)
	if rt2 == "" || rt2 == rt1 {
		t.Fatalf("refresh token did not rotate: rt1=%q rt2=%q", rt1, rt2)
	}

	// Replaying the OLD refresh token fails (invalid_grant, 400).
	if rr := refresh(rt1); rr.Code != http.StatusBadRequest {
		t.Fatalf("replay of rotated refresh token: want 400, got %d %s", rr.Code, rr.Body.String())
	}

	// Family reuse-detection: rt2 is now dead too.
	if rr := refresh(rt2); rr.Code != http.StatusBadRequest {
		t.Fatalf("family revoke: rt2 should be dead after rt1 replay, got %d", rr.Code)
	}
}

// TestFositeIntrospectionHTTP exercises RFC 7662: an authenticated client sees
// active:true for a live access token and active:false after revocation, and an
// unauthenticated introspection call is rejected.
func TestFositeIntrospectionHTTP(t *testing.T) {
	e := newOPEnv(t)
	cid, secret := e.createApp(t, "https://app.example.com/cb", []string{"openid", "email"})
	verifier, challenge := pkce()
	code := e.approveToCode(t, cid, challenge, "openid email", "st-int")
	tok := e.tokenExchange(t, cid, secret, code, verifier)
	access, _ := tok["access_token"].(string)
	if access == "" {
		t.Fatal("no access token")
	}

	basic := "Basic " + base64.StdEncoding.EncodeToString([]byte(cid+":"+secret))
	introspect := func(token, auth string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/oauth/introspect",
			strings.NewReader(url.Values{"token": {token}}.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if auth != "" {
			r.Header.Set("Authorization", auth)
		}
		rr := httptest.NewRecorder()
		e.mux.ServeHTTP(rr, r)
		return rr
	}

	// Authenticated introspection of a live token → active:true.
	rr := introspect(access, basic)
	if rr.Code != http.StatusOK {
		t.Fatalf("introspect live: %d %s", rr.Code, rr.Body.String())
	}
	var resp map[string]any
	json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp["active"] != true {
		t.Fatalf("live token introspection active=%v, want true (%s)", resp["active"], rr.Body.String())
	}

	// Unauthenticated introspection → 401.
	if rr := introspect(access, ""); rr.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated introspect: want 401, got %d", rr.Code)
	}

	// Revoke, then introspection reports inactive.
	rrRev := e.doForm("/oauth/revoke", url.Values{
		"token": {access}, "token_type_hint": {"access_token"},
		"client_id": {cid}, "client_secret": {secret},
	})
	if rrRev.Code != http.StatusOK {
		t.Fatalf("revoke: %d %s", rrRev.Code, rrRev.Body.String())
	}
	rr = introspect(access, basic)
	if rr.Code != http.StatusOK {
		t.Fatalf("introspect revoked: %d", rr.Code)
	}
	json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp["active"] != false {
		t.Fatalf("revoked token introspection active=%v, want false", resp["active"])
	}
}

// TestFositeIDTokenClaimsViaJWKS verifies that the id_token minted over the live
// path is RS256, verifies against the published JWKS by kid, and carries the
// correct iss / aud / sub / nonce / email claims.
func TestFositeIDTokenClaimsViaJWKS(t *testing.T) {
	e := newOPEnv(t)
	cid, secret := e.createApp(t, "https://app.example.com/cb", []string{"openid", "email"})
	verifier, challenge := pkce()
	code := e.approveToCode(t, cid, challenge, "openid email", "st-idt")
	tok := e.tokenExchange(t, cid, secret, code, verifier)
	idt, _ := tok["id_token"].(string)
	if idt == "" {
		t.Fatal("no id_token issued")
	}

	// Header: RS256 + a kid.
	parts := strings.Split(idt, ".")
	if len(parts) != 3 {
		t.Fatalf("malformed id_token: %d parts", len(parts))
	}
	hdrJSON, _ := base64.RawURLEncoding.DecodeString(parts[0])
	var hdr struct{ Alg, Kid string }
	json.Unmarshal(hdrJSON, &hdr)
	if hdr.Alg != "RS256" || hdr.Kid == "" {
		t.Fatalf("id_token header alg=%q kid=%q", hdr.Alg, hdr.Kid)
	}

	// Fetch JWKS and locate the signing key by kid.
	rr := e.do(http.MethodGet, "/oauth/jwks", "", false)
	if rr.Code != http.StatusOK {
		t.Fatalf("jwks: %d", rr.Code)
	}
	var jwks struct {
		Keys []oauthprovider.JWK `json:"keys"`
	}
	json.Unmarshal(rr.Body.Bytes(), &jwks)
	var matched bool
	for _, k := range jwks.Keys {
		if k.Kid != hdr.Kid {
			continue
		}
		pub, err := k.RSAPublicKey()
		if err != nil {
			t.Fatalf("reconstruct JWK: %v", err)
		}
		claims, err := oauthprovider.VerifyIDToken(idt, pub)
		if err != nil {
			t.Fatalf("id_token does not verify against JWKS key: %v", err)
		}
		if claims["iss"] != "https://vulos.test" {
			t.Fatalf("iss = %v", claims["iss"])
		}
		if claims["sub"] != e.userID {
			t.Fatalf("sub = %v, want raw user id %v", claims["sub"], e.userID)
		}
		if claims["nonce"] != "nonce-xyz" {
			t.Fatalf("nonce = %v", claims["nonce"])
		}
		if claims["email"] != "dev@vulos.test" {
			t.Fatalf("email = %v, want dev@vulos.test", claims["email"])
		}
		if !audContains(claims["aud"], cid) {
			t.Fatalf("aud = %v, want to contain %v", claims["aud"], cid)
		}
		matched = true
	}
	if !matched {
		t.Fatalf("no JWKS key matched id_token kid %q", hdr.Kid)
	}
}

func audContains(aud any, want string) bool {
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

// TestFositeWrongPKCEVerifierHTTP proves a wrong PKCE verifier is rejected at
// the token endpoint (invalid_grant, 400) — the code is bound to its challenge.
func TestFositeWrongPKCEVerifierHTTP(t *testing.T) {
	e := newOPEnv(t)
	cid, secret := e.createApp(t, "https://app.example.com/cb", []string{"openid"})
	_, challenge := pkce()
	code := e.approveToCode(t, cid, challenge, "openid", "st-pkce")

	rr := e.doForm("/oauth/token", url.Values{
		"grant_type": {"authorization_code"}, "code": {code},
		"redirect_uri": {"https://app.example.com/cb"}, "client_id": {cid},
		"client_secret": {secret}, "code_verifier": {"wrong-verifier-but-sufficiently-long-000000000000"},
	})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("wrong PKCE verifier: want 400, got %d %s", rr.Code, rr.Body.String())
	}
}

// TestFositeDiscoveryAdvertisesIntrospectionAndRevocation checks the discovery
// document now advertises the fosite-served introspection + revocation endpoints.
func TestFositeDiscoveryAdvertisesIntrospectionAndRevocation(t *testing.T) {
	e := newOPEnv(t)
	rr := e.do(http.MethodGet, "/.well-known/openid-configuration", "", false)
	if rr.Code != http.StatusOK {
		t.Fatalf("discovery: %d", rr.Code)
	}
	var doc map[string]any
	json.Unmarshal(rr.Body.Bytes(), &doc)
	if doc["introspection_endpoint"] != "https://vulos.test/oauth/introspect" {
		t.Fatalf("introspection_endpoint = %v", doc["introspection_endpoint"])
	}
	if doc["revocation_endpoint"] != "https://vulos.test/oauth/revoke" {
		t.Fatalf("revocation_endpoint = %v", doc["revocation_endpoint"])
	}
}
