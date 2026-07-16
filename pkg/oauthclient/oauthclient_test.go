package oauthclient

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

// makeIDToken builds an unsigned (alg=none) compact JWT with the given claims. The
// exchange reads the id_token back-channel over TLS and does not verify the
// signature (OIDC Core §3.1.3.7), so an alg=none token is a faithful test fixture.
func makeIDToken(t *testing.T, claims map[string]any) string {
	t.Helper()
	hdr := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	pj, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("marshal claims: %v", err)
	}
	pl := base64.RawURLEncoding.EncodeToString(pj)
	sig := base64.RawURLEncoding.EncodeToString([]byte("unsigned"))
	return hdr + "." + pl + "." + sig
}

func testProvider(tokenURL string) Provider {
	return Provider{
		ID:           "test",
		DisplayName:  "Test",
		ClientID:     "cid-123",
		ClientSecret: "secret",
		AuthURL:      "https://idp.example/authorize",
		TokenURL:     tokenURL,
		Scopes:       []string{"openid", "email", "profile"},
	}
}

func TestAuthCodeURL_CarriesPKCEAndState(t *testing.T) {
	p := testProvider("https://idp.example/token")
	raw := p.AuthCodeURL("https://vulos.org/api/auth/oauth/test/callback", "STATE123", "CHALLENGE456")
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse authorize url: %v", err)
	}
	q := u.Query()
	if got := q.Get("response_type"); got != "code" {
		t.Errorf("response_type = %q, want code", got)
	}
	if got := q.Get("client_id"); got != "cid-123" {
		t.Errorf("client_id = %q", got)
	}
	if got := q.Get("code_challenge"); got != "CHALLENGE456" {
		t.Errorf("code_challenge = %q", got)
	}
	if got := q.Get("code_challenge_method"); got != "S256" {
		t.Errorf("code_challenge_method = %q, want S256", got)
	}
	if got := q.Get("state"); got != "STATE123" {
		t.Errorf("state = %q", got)
	}
	if got := q.Get("redirect_uri"); got != "https://vulos.org/api/auth/oauth/test/callback" {
		t.Errorf("redirect_uri = %q", got)
	}
}

func tokenServer(t *testing.T, idToken string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"access_token": "at",
			"id_token":     idToken,
			"token_type":   "Bearer",
		})
	}))
}

func TestExchange_ParsesVerifiedIdentity(t *testing.T) {
	idtok := makeIDToken(t, map[string]any{
		"iss": "https://idp.example", "sub": "SUB-42", "aud": "cid-123",
		"exp": time.Now().Add(time.Hour).Unix(), "email": "Alice@Example.com",
		"email_verified": true,
	})
	srv := tokenServer(t, idtok)
	defer srv.Close()

	reg := &Registry{providers: map[string]Provider{}, hc: srv.Client()}
	p := testProvider(srv.URL)

	id, err := reg.Exchange(context.Background(), p, "https://vulos.org/cb", "authcode", "verifier")
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if id.Subject != "SUB-42" {
		t.Errorf("subject = %q", id.Subject)
	}
	if id.Email != "alice@example.com" {
		t.Errorf("email = %q, want lowercased", id.Email)
	}
	if !id.EmailVerified {
		t.Errorf("email_verified = false, want true")
	}
	if id.Provider != "test" {
		t.Errorf("provider = %q", id.Provider)
	}
}

func TestExchange_EmailVerifiedAsString(t *testing.T) {
	idtok := makeIDToken(t, map[string]any{
		"sub": "S", "aud": "cid-123", "exp": time.Now().Add(time.Hour).Unix(),
		"email": "b@example.com", "email_verified": "true",
	})
	srv := tokenServer(t, idtok)
	defer srv.Close()
	reg := &Registry{providers: map[string]Provider{}, hc: srv.Client()}
	id, err := reg.Exchange(context.Background(), testProvider(srv.URL), "https://x/cb", "c", "v")
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if !id.EmailVerified {
		t.Errorf(`email_verified "true" string not coerced to bool`)
	}
}

func TestExchange_AudienceMismatch_Rejected(t *testing.T) {
	idtok := makeIDToken(t, map[string]any{
		"sub": "S", "aud": "SOMEONE-ELSE", "exp": time.Now().Add(time.Hour).Unix(),
		"email": "c@example.com", "email_verified": true,
	})
	srv := tokenServer(t, idtok)
	defer srv.Close()
	reg := &Registry{providers: map[string]Provider{}, hc: srv.Client()}
	if _, err := reg.Exchange(context.Background(), testProvider(srv.URL), "https://x/cb", "c", "v"); err == nil {
		t.Fatal("expected audience-mismatch error, got nil")
	}
}

func TestExchange_TokenEndpointError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant"})
	}))
	defer srv.Close()
	reg := &Registry{providers: map[string]Provider{}, hc: srv.Client()}
	if _, err := reg.Exchange(context.Background(), testProvider(srv.URL), "https://x/cb", "c", "v"); err == nil {
		t.Fatal("expected token-endpoint error, got nil")
	}
}

func TestExchange_IssuerMismatch_Rejected(t *testing.T) {
	p := testProvider("")
	p.issuerPrefix = "https://accounts.google.com"
	idtok := makeIDToken(t, map[string]any{
		"iss": "https://evil.example", "sub": "S", "aud": "cid-123",
		"exp": time.Now().Add(time.Hour).Unix(), "email": "d@example.com", "email_verified": true,
	})
	srv := tokenServer(t, idtok)
	defer srv.Close()
	p.TokenURL = srv.URL
	reg := &Registry{providers: map[string]Provider{}, hc: srv.Client()}
	if _, err := reg.Exchange(context.Background(), p, "https://x/cb", "c", "v"); err == nil {
		t.Fatal("expected issuer-mismatch error, got nil")
	}
}

func TestExchange_ExpiredToken_Rejected(t *testing.T) {
	idtok := makeIDToken(t, map[string]any{
		"iss": "https://idp.example", "sub": "S", "aud": "cid-123",
		"exp":   time.Now().Add(-1 * time.Hour).Unix(), // long expired
		"email": "e@example.com", "email_verified": true,
	})
	srv := tokenServer(t, idtok)
	defer srv.Close()
	reg := &Registry{providers: map[string]Provider{}, hc: srv.Client()}
	if _, err := reg.Exchange(context.Background(), testProvider(srv.URL), "https://x/cb", "c", "v"); err == nil {
		t.Fatal("expected expired-token rejection, got nil")
	}
}

func TestExchange_MissingExp_Rejected(t *testing.T) {
	// An OIDC id_token MUST carry exp; a missing/zero exp is malformed, not eternal.
	idtok := makeIDToken(t, map[string]any{
		"iss": "https://idp.example", "sub": "S", "aud": "cid-123",
		"email": "e@example.com", "email_verified": true,
	})
	srv := tokenServer(t, idtok)
	defer srv.Close()
	reg := &Registry{providers: map[string]Provider{}, hc: srv.Client()}
	if _, err := reg.Exchange(context.Background(), testProvider(srv.URL), "https://x/cb", "c", "v"); err == nil {
		t.Fatal("expected missing-exp rejection, got nil")
	}
}

func TestExchange_EmptyAudience_Rejected(t *testing.T) {
	// No aud claim at all → audienceContains must return false → reject.
	idtok := makeIDToken(t, map[string]any{
		"iss": "https://idp.example", "sub": "S",
		"exp": time.Now().Add(time.Hour).Unix(), "email": "e@example.com", "email_verified": true,
	})
	srv := tokenServer(t, idtok)
	defer srv.Close()
	reg := &Registry{providers: map[string]Provider{}, hc: srv.Client()}
	if _, err := reg.Exchange(context.Background(), testProvider(srv.URL), "https://x/cb", "c", "v"); err == nil {
		t.Fatal("expected empty-audience rejection, got nil")
	}
}

func TestExchange_UnverifiedEmail_PreservedNotForged(t *testing.T) {
	// A provider-unverified email must round-trip as EmailVerified=false so the
	// route layer refuses to auto-link it to a pre-existing account.
	idtok := makeIDToken(t, map[string]any{
		"iss": "https://idp.example", "sub": "S", "aud": "cid-123",
		"exp": time.Now().Add(time.Hour).Unix(), "email": "u@example.com", "email_verified": false,
	})
	srv := tokenServer(t, idtok)
	defer srv.Close()
	reg := &Registry{providers: map[string]Provider{}, hc: srv.Client()}
	id, err := reg.Exchange(context.Background(), testProvider(srv.URL), "https://x/cb", "c", "v")
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if id.EmailVerified {
		t.Error("unverified provider email surfaced as EmailVerified=true")
	}
}

func TestNewRegistryFromEnv_ConfigSeam(t *testing.T) {
	// Nothing configured → provider absent (callers degrade cleanly).
	t.Setenv("GOOGLE_OAUTH_CLIENT_ID", "")
	t.Setenv("GOOGLE_OAUTH_CLIENT_SECRET", "")
	t.Setenv("MS_OAUTH_CLIENT_ID", "")
	t.Setenv("MS_OAUTH_CLIENT_SECRET", "")
	t.Setenv("MICROSOFT_OAUTH_CLIENT_ID", "")
	t.Setenv("MICROSOFT_OAUTH_CLIENT_SECRET", "")
	reg := NewRegistryFromEnv()
	if _, ok := reg.Get("google"); ok {
		t.Error("google configured with no env — expected absent")
	}
	if len(reg.Configured()) != 0 {
		t.Errorf("Configured() = %v, want empty", reg.Configured())
	}

	// Google configured → present and listed.
	t.Setenv("GOOGLE_OAUTH_CLIENT_ID", "gid")
	t.Setenv("GOOGLE_OAUTH_CLIENT_SECRET", "gsec")
	reg2 := NewRegistryFromEnv()
	g, ok := reg2.Get("google")
	if !ok {
		t.Fatal("google not configured despite env")
	}
	if g.ClientID != "gid" {
		t.Errorf("client id = %q", g.ClientID)
	}
	infos := reg2.Configured()
	if len(infos) != 1 || infos[0].ID != "google" || infos[0].DisplayName != "Google" {
		t.Errorf("Configured() = %+v", infos)
	}
}
