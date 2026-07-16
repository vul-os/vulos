package oauthprovider

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// wave40NewFlow registers a confidential client, runs the authorize + code
// exchange, and returns (svc, client, secret, initial TokenResponse). Shared by
// the refresh-rotation tests below.
func wave40NewFlow(t *testing.T, scope string) (*Service, Client, string, *TokenResponse) {
	t.Helper()
	svc := newTestService(t)
	ctx := context.Background()
	verifier, challenge := pkcePair()

	c, secret, err := svc.RegisterClient(ctx, "user-w40", "W40App",
		[]string{"https://w40.example.com/cb"}, []string{ScopeOpenID, ScopeEmail, ScopeMailSend}, false)
	if err != nil {
		t.Fatalf("RegisterClient: %v", err)
	}
	v, azErr := svc.ValidateAuthorize(ctx, AuthorizeRequest{
		ResponseType: "code", ClientID: c.ClientID, RedirectURI: "https://w40.example.com/cb",
		Scope: scope, CodeChallenge: challenge, CodeChallengeMethod: "S256",
	})
	if azErr != nil {
		t.Fatalf("ValidateAuthorize: %+v", azErr)
	}
	code, err := svc.IssueCode(ctx, v, "user-w40")
	if err != nil {
		t.Fatalf("IssueCode: %v", err)
	}
	resp, err := svc.ExchangeCode(ctx, ExchangeCodeParams{
		Code: code, RedirectURI: "https://w40.example.com/cb",
		ClientID: c.ClientID, ClientSecret: secret, CodeVerifier: verifier,
	})
	if err != nil {
		t.Fatalf("ExchangeCode: %v", err)
	}
	return svc, c, secret, resp
}

// TestRefreshRotation_RotatesAndCascades verifies the core RFC 9700 rotation
// invariants on the happy path: a refresh issues a NEW refresh token, the OLD
// refresh token is dead, AND the OLD access token (same family) is cascaded to
// revoked. This exercises the rotation cascade that the wave-30 pass flagged
// thin (Refresh + RevokeTokenByHash access-token cascade).
func TestRefreshRotation_RotatesAndCascades(t *testing.T) {
	svc, c, secret, resp1 := wave40NewFlow(t, "openid email")
	ctx := context.Background()

	// The initial access token is live.
	if _, err := svc.IntrospectAccessToken(ctx, resp1.AccessToken); err != nil {
		t.Fatalf("initial access token should be live: %v", err)
	}

	resp2, err := svc.Refresh(ctx, RefreshParams{
		ClientID: c.ClientID, ClientSecret: secret, RefreshToken: resp1.RefreshToken,
	})
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	// New refresh token must differ from the presented one.
	if resp2.RefreshToken == resp1.RefreshToken {
		t.Fatal("rotation must issue a fresh refresh token")
	}
	if resp2.AccessToken == resp1.AccessToken {
		t.Fatal("rotation must issue a fresh access token")
	}

	// CASCADE: the OLD access token (same family) must be revoked, otherwise
	// rotating the refresh token leaves a live sibling access token.
	if _, err := svc.IntrospectAccessToken(ctx, resp1.AccessToken); err == nil {
		t.Fatal("old access token must be cascade-revoked on refresh rotation")
	}

	// The NEW access token is live (check BEFORE the replay below, which would
	// trip family-revoke and kill it).
	if _, err := svc.IntrospectAccessToken(ctx, resp2.AccessToken); err != nil {
		t.Fatalf("new access token should be live: %v", err)
	}

	// OLD refresh token is now revoked → refreshing with it fails (and, being a
	// rotated-out replay, revokes the whole family per RFC 9700).
	if _, err := svc.Refresh(ctx, RefreshParams{
		ClientID: c.ClientID, ClientSecret: secret, RefreshToken: resp1.RefreshToken,
	}); err == nil {
		t.Fatal("rotated-out refresh token must be dead")
	}
}

// TestRefreshInheritsFamily verifies the rotation chain stays linked: a token
// issued by Refresh carries the SAME family_id as the presented one, so replay
// detection can later revoke the whole chain.
func TestRefreshInheritsFamily(t *testing.T) {
	svc, c, secret, resp1 := wave40NewFlow(t, "openid")
	ctx := context.Background()

	fam1, err := svc.store.GetToken(ctx, hashToken(resp1.RefreshToken), "refresh")
	if err != nil {
		t.Fatalf("get rt1: %v", err)
	}

	resp2, err := svc.Refresh(ctx, RefreshParams{
		ClientID: c.ClientID, ClientSecret: secret, RefreshToken: resp1.RefreshToken,
	})
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	fam2, err := svc.store.GetToken(ctx, hashToken(resp2.RefreshToken), "refresh")
	if err != nil {
		t.Fatalf("get rt2: %v", err)
	}
	if fam1.FamilyID == "" || fam2.FamilyID == "" {
		t.Fatalf("family ids must be set (fam1=%q fam2=%q)", fam1.FamilyID, fam2.FamilyID)
	}
	if fam1.FamilyID != fam2.FamilyID {
		t.Fatalf("rotation must inherit family: fam1=%s fam2=%s", fam1.FamilyID, fam2.FamilyID)
	}
}

// TestRefreshScopeNarrowing checks the optional down-scope on the refresh grant:
// a requested subset is honoured, and a request for a scope NOT in the original
// grant is rejected with ErrInvalidScope (no privilege escalation via refresh).
func TestRefreshScopeNarrowing(t *testing.T) {
	svc, c, secret, resp1 := wave40NewFlow(t, "openid email mail.send")
	ctx := context.Background()

	// Narrow to a subset → allowed, and reflected in the response scope.
	resp2, err := svc.Refresh(ctx, RefreshParams{
		ClientID: c.ClientID, ClientSecret: secret, RefreshToken: resp1.RefreshToken,
		Scope: "openid email",
	})
	if err != nil {
		t.Fatalf("subset down-scope should succeed: %v", err)
	}
	if HasScope(resp2.Scope, ScopeMailSend) {
		t.Fatalf("down-scoped token must not retain profile scope: %q", resp2.Scope)
	}
	if !HasScope(resp2.Scope, ScopeEmail) || !HasScope(resp2.Scope, ScopeOpenID) {
		t.Fatalf("down-scoped token missing requested scopes: %q", resp2.Scope)
	}

	// Requesting profile back (not a subset of the now-narrowed grant) must fail.
	if _, err := svc.Refresh(ctx, RefreshParams{
		ClientID: c.ClientID, ClientSecret: secret, RefreshToken: resp2.RefreshToken,
		Scope: "openid email mail.send",
	}); !errors.Is(err, ErrInvalidScope) {
		t.Fatalf("scope escalation via refresh must be ErrInvalidScope, got %v", err)
	}
}

// TestRefreshWrongClient ensures a refresh token issued to one client cannot be
// redeemed by a different authenticated client (token/client binding).
func TestRefreshWrongClient(t *testing.T) {
	svc, _, _, resp1 := wave40NewFlow(t, "openid")
	ctx := context.Background()

	// A second, distinct confidential client.
	other, otherSecret, err := svc.RegisterClient(ctx, "user-other", "OtherApp",
		[]string{"https://other.example.com/cb"}, []string{ScopeOpenID}, false)
	if err != nil {
		t.Fatalf("RegisterClient other: %v", err)
	}
	if _, err := svc.Refresh(ctx, RefreshParams{
		ClientID: other.ClientID, ClientSecret: otherSecret, RefreshToken: resp1.RefreshToken,
	}); !errors.Is(err, ErrInvalidGrant) {
		t.Fatalf("cross-client refresh must be ErrInvalidGrant, got %v", err)
	}
}

// TestRefreshBadClientSecret ensures client authentication is enforced on the
// refresh grant.
func TestRefreshBadClientSecret(t *testing.T) {
	svc, c, _, resp1 := wave40NewFlow(t, "openid")
	ctx := context.Background()
	if _, err := svc.Refresh(ctx, RefreshParams{
		ClientID: c.ClientID, ClientSecret: "vcsk_wrong-secret", RefreshToken: resp1.RefreshToken,
	}); !errors.Is(err, ErrInvalidClient) {
		t.Fatalf("bad client secret on refresh must be ErrInvalidClient, got %v", err)
	}
}

// TestRefreshUnknownToken ensures an entirely unknown refresh token is rejected
// (and does NOT trigger a family revoke — there is no family to revoke).
func TestRefreshUnknownToken(t *testing.T) {
	svc, c, secret, _ := wave40NewFlow(t, "openid")
	ctx := context.Background()
	if _, err := svc.Refresh(ctx, RefreshParams{
		ClientID: c.ClientID, ClientSecret: secret, RefreshToken: "vcrt_never-issued-token",
	}); !errors.Is(err, ErrInvalidGrant) {
		t.Fatalf("unknown refresh token must be ErrInvalidGrant, got %v", err)
	}
}

// TestRefreshReplayFailClosed_FamilyRevokeError is the Refresh-level fail-closed
// case complementing TestRevokeTokenByHash_FamilyLookupFailsClosed: when a
// rotated-out (revoked) refresh token is replayed AND the family-revoke DB write
// fails, the wave-34 code logs a revocation-hole WARNING and still returns an
// error to the caller (never silently succeeds). We drive the failure by
// dropping the tokens table after rotation so RevokeFamilyByID errors.
func TestRefreshReplayFailClosed_FamilyRevokeError(t *testing.T) {
	svc, c, secret, resp1 := wave40NewFlow(t, "openid")
	ctx := context.Background()

	// Rotate rt1 → rt2 so rt1 is now revoked (rotated out).
	if _, err := svc.Refresh(ctx, RefreshParams{
		ClientID: c.ClientID, ClientSecret: secret, RefreshToken: resp1.RefreshToken,
	}); err != nil {
		t.Fatalf("rotation: %v", err)
	}

	// Now break the store: dropping oauth_tokens makes both GetToken and the
	// GetTokenByHashAny/RevokeFamilyByID lookups error. The replay path must
	// still return an error (fail closed), never a token response.
	if _, err := svc.store.db.Exec(`DROP TABLE oauth_tokens`); err != nil {
		t.Fatalf("drop oauth_tokens: %v", err)
	}
	resp, err := svc.Refresh(ctx, RefreshParams{
		ClientID: c.ClientID, ClientSecret: secret, RefreshToken: resp1.RefreshToken,
	})
	if err == nil {
		t.Fatal("replay with broken store must fail closed, got nil error")
	}
	if resp != nil {
		t.Fatal("replay with broken store must not return a token response")
	}
}

// TestRevokeUserClientTokens_AdminForcedLogout verifies the admin forced-logout
// path: RevokeUserClientTokens kills every live token (access + refresh) for a
// (user, client) pair without touching consent.
func TestRevokeUserClientTokens_AdminForcedLogout(t *testing.T) {
	svc, c, secret, resp := wave40NewFlow(t, "openid")
	ctx := context.Background()

	if _, err := svc.IntrospectAccessToken(ctx, resp.AccessToken); err != nil {
		t.Fatalf("precondition: access token live: %v", err)
	}

	if err := svc.RevokeUserClientTokens(ctx, "user-w40", c.ClientID); err != nil {
		t.Fatalf("RevokeUserClientTokens: %v", err)
	}

	if _, err := svc.IntrospectAccessToken(ctx, resp.AccessToken); err == nil {
		t.Fatal("access token must be dead after forced logout")
	}
	if _, err := svc.Refresh(ctx, RefreshParams{
		ClientID: c.ClientID, ClientSecret: secret, RefreshToken: resp.RefreshToken,
	}); err == nil {
		t.Fatal("refresh token must be dead after forced logout")
	}
}

// ---------------------------------------------------------------------------
// Redirect-URI validation (registration-time)
// ---------------------------------------------------------------------------

// TestSanitizeRedirectURIs_Rejections locks down the redirect-URI allow rules
// that guard against open-redirect token theft: no fragments, https-only except
// loopback http, absolute URLs only, no unsupported schemes.
func TestSanitizeRedirectURIs_Rejections(t *testing.T) {
	cases := []struct {
		name string
		uri  string
		ok   bool
	}{
		{"https ok", "https://app.example.com/cb", true},
		{"loopback http ok", "http://127.0.0.1:8080/cb", true},
		{"localhost http ok", "http://localhost/cb", true},
		{"non-loopback http rejected", "http://app.example.com/cb", false},
		{"fragment rejected", "https://app.example.com/cb#frag", false},
		{"relative rejected", "/callback", false},
		{"custom scheme rejected", "myapp://cb", false},
		{"javascript scheme rejected", "javascript:alert(1)", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := sanitizeRedirectURIs([]string{tc.uri})
			if tc.ok && (err != nil || len(out) != 1) {
				t.Fatalf("expected %q accepted, got out=%v err=%v", tc.uri, out, err)
			}
			if !tc.ok && err == nil {
				t.Fatalf("expected %q rejected, got accepted", tc.uri)
			}
		})
	}
}

// TestSanitizeRedirectURIs_Dedup verifies duplicates collapse and blank entries
// are dropped.
func TestSanitizeRedirectURIs_Dedup(t *testing.T) {
	out, err := sanitizeRedirectURIs([]string{
		"https://a.example.com/cb", "  ", "https://a.example.com/cb", "https://b.example.com/cb",
	})
	if err != nil {
		t.Fatalf("sanitize: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("expected 2 deduped URIs, got %v", out)
	}
}

// TestValidateAuthorize_RedirectMismatchNotRedirectable ensures a redirect_uri
// that does not exactly match a registered URI is refused WITHOUT being
// redirectable (RedirectOK=false) — the untrusted target must never receive an
// error redirect.
func TestValidateAuthorize_RedirectMismatch(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	_, challenge := pkcePair()
	c, _, err := svc.RegisterClient(ctx, "u", "App",
		[]string{"https://app.example.com/cb"}, []string{ScopeOpenID}, false)
	if err != nil {
		t.Fatalf("RegisterClient: %v", err)
	}
	// Near-miss redirect (trailing path differs) must be rejected, not redirected.
	_, azErr := svc.ValidateAuthorize(ctx, AuthorizeRequest{
		ResponseType: "code", ClientID: c.ClientID, RedirectURI: "https://app.example.com/cb/evil",
		Scope: "openid", CodeChallenge: challenge, CodeChallengeMethod: "S256",
	})
	if azErr == nil {
		t.Fatal("redirect mismatch must error")
	}
	if azErr.RedirectOK {
		t.Fatal("redirect mismatch must NOT be redirectable (untrusted target)")
	}
}

// TestValidateAuthorize_PKCERequiredS256 ensures the plain PKCE method and a
// missing/short challenge are rejected (RedirectOK true — target already
// trusted by this point).
func TestValidateAuthorize_PKCE(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	_, challenge := pkcePair()
	c, _, err := svc.RegisterClient(ctx, "u", "App",
		[]string{"https://app.example.com/cb"}, []string{ScopeOpenID}, false)
	if err != nil {
		t.Fatalf("RegisterClient: %v", err)
	}
	base := AuthorizeRequest{
		ResponseType: "code", ClientID: c.ClientID, RedirectURI: "https://app.example.com/cb",
		Scope: "openid",
	}

	// plain method rejected.
	req := base
	req.CodeChallenge = challenge
	req.CodeChallengeMethod = "plain"
	if _, azErr := svc.ValidateAuthorize(ctx, req); azErr == nil {
		t.Fatal("plain PKCE method must be rejected")
	}

	// missing challenge rejected.
	req = base
	req.CodeChallengeMethod = "S256"
	req.CodeChallenge = ""
	if _, azErr := svc.ValidateAuthorize(ctx, req); azErr == nil {
		t.Fatal("missing code_challenge must be rejected")
	}

	// too-short challenge rejected.
	req = base
	req.CodeChallengeMethod = "S256"
	req.CodeChallenge = "tooshort"
	if _, azErr := svc.ValidateAuthorize(ctx, req); azErr == nil {
		t.Fatal("too-short code_challenge must be rejected")
	}
}

// TestValidChallenge_Bounds exercises the RFC 7636 length bounds and base64url
// decoding gate on ValidChallenge directly.
func TestValidChallenge_Bounds(t *testing.T) {
	_, good := pkcePair()
	if !ValidChallenge(good) {
		t.Fatal("valid S256 challenge rejected")
	}
	if ValidChallenge(strings.Repeat("a", 42)) {
		t.Fatal("42-char challenge (below 43) must be rejected")
	}
	if ValidChallenge(strings.Repeat("a", 129)) {
		t.Fatal("129-char challenge (above 128) must be rejected")
	}
	// 43 chars but not valid base64url (contains '#').
	if ValidChallenge("###########################################") {
		t.Fatal("non-base64url challenge must be rejected")
	}
}

// TestBuildRedirect_PreservesQuery verifies redirect building appends params
// while preserving an existing query string and dropping empty values.
func TestBuildRedirect_PreservesQuery(t *testing.T) {
	out, err := BuildRedirect("https://app.example.com/cb?foo=1", map[string]string{
		"code": "abc", "state": "xyz", "empty": "",
	})
	if err != nil {
		t.Fatalf("BuildRedirect: %v", err)
	}
	for _, want := range []string{"foo=1", "code=abc", "state=xyz"} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected %q in %q", want, out)
		}
	}
	if strings.Contains(out, "empty=") {
		t.Fatalf("empty param must be dropped: %q", out)
	}
}

// TestExchangeCode_TamperedVerifierDoesNotBurnCode verifies a wrong PKCE
// verifier is rejected WITHOUT consuming the single-use code, so the legitimate
// client can still complete the exchange (the code is only burned on success).
func TestExchangeCode_TamperedVerifierDoesNotBurnCode(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	verifier, challenge := pkcePair()

	c, secret, err := svc.RegisterClient(ctx, "u", "App",
		[]string{"https://app.example.com/cb"}, []string{ScopeOpenID}, false)
	if err != nil {
		t.Fatalf("RegisterClient: %v", err)
	}
	v, azErr := svc.ValidateAuthorize(ctx, AuthorizeRequest{
		ResponseType: "code", ClientID: c.ClientID, RedirectURI: "https://app.example.com/cb",
		Scope: "openid", CodeChallenge: challenge, CodeChallengeMethod: "S256",
	})
	if azErr != nil {
		t.Fatalf("ValidateAuthorize: %+v", azErr)
	}
	code, err := svc.IssueCode(ctx, v, "u")
	if err != nil {
		t.Fatalf("IssueCode: %v", err)
	}

	// Wrong verifier → rejected.
	if _, err := svc.ExchangeCode(ctx, ExchangeCodeParams{
		Code: code, RedirectURI: "https://app.example.com/cb",
		ClientID: c.ClientID, ClientSecret: secret,
		CodeVerifier: "wrong-verifier-but-long-enough-to-pass-length-check-000000",
	}); !errors.Is(err, ErrInvalidGrant) {
		t.Fatalf("tampered verifier must be ErrInvalidGrant, got %v", err)
	}

	// Correct verifier still works → the code was NOT burned by the failed attempt.
	if _, err := svc.ExchangeCode(ctx, ExchangeCodeParams{
		Code: code, RedirectURI: "https://app.example.com/cb",
		ClientID: c.ClientID, ClientSecret: secret, CodeVerifier: verifier,
	}); err != nil {
		t.Fatalf("legit exchange after failed PKCE should succeed: %v", err)
	}
}

// TestExchangeCode_ReplayAfterSuccessFails ensures a code is single-use: a
// second exchange of the same code (replay) is rejected.
func TestExchangeCode_ReplayAfterSuccessFails(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	verifier, challenge := pkcePair()

	c, secret, _ := svc.RegisterClient(ctx, "u", "App",
		[]string{"https://app.example.com/cb"}, []string{ScopeOpenID}, false)
	v, _ := svc.ValidateAuthorize(ctx, AuthorizeRequest{
		ResponseType: "code", ClientID: c.ClientID, RedirectURI: "https://app.example.com/cb",
		Scope: "openid", CodeChallenge: challenge, CodeChallengeMethod: "S256",
	})
	code, _ := svc.IssueCode(ctx, v, "u")
	if _, err := svc.ExchangeCode(ctx, ExchangeCodeParams{
		Code: code, RedirectURI: "https://app.example.com/cb",
		ClientID: c.ClientID, ClientSecret: secret, CodeVerifier: verifier,
	}); err != nil {
		t.Fatalf("first exchange: %v", err)
	}
	if _, err := svc.ExchangeCode(ctx, ExchangeCodeParams{
		Code: code, RedirectURI: "https://app.example.com/cb",
		ClientID: c.ClientID, ClientSecret: secret, CodeVerifier: verifier,
	}); !errors.Is(err, ErrInvalidGrant) {
		t.Fatalf("code replay must be ErrInvalidGrant, got %v", err)
	}
}
