// Package oauthclient implements Vulos-as-an-OAuth-CLIENT: the "Sign in with
// Google / Microsoft" convenience login layered on top of the mandatory Vulos
// email+password credential.
//
// Design (account model LOCKED, 2026-07):
//
//   - email+password is the CENTRE of every account. Social login never replaces
//     it; it is a convenience path that resolves to (or creates) a Vulos account
//     which STILL must carry its own password before it is usable. See
//     internal/auth/oauth_identity.go for the account-side rules; this package is
//     purely the OAuth-client protocol (authorize URL + code exchange + identity
//     extraction).
//
//   - CONFIG SEAM: providers are configured entirely from environment variables
//     (founder-supplied client id/secret). An UNCONFIGURED provider is simply
//     absent from the registry — Get returns ok=false and callers degrade cleanly
//     (never crash, never a hard 500).
//
//   - PKCE (S256) + state are enforced by the route layer; this package builds the
//     authorize URL with the code_challenge and performs the code+verifier
//     exchange.
//
//   - id_token TRUST: the id_token is read back-channel — a direct, server-to-server
//     TLS call to the provider's token endpoint authenticated with our client
//     secret. Per OpenID Connect Core §3.1.3.7(6), signature validation MAY be
//     skipped for a token obtained via that direct TLS channel. We still validate
//     the audience (must be our client id) and expiry, and only ever AUTO-LINK on a
//     provider-asserted verified email (email_verified claim). This is deliberately
//     conservative: an unverified provider email can create/extend its OWN account
//     but can never silently attach to a pre-existing Vulos account.
package oauthclient

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// Identity is the verified external identity extracted from a completed OAuth
// exchange. Subject is the provider's stable, opaque user id (the id_token `sub`).
type Identity struct {
	Provider      string
	Subject       string
	Email         string
	EmailVerified bool
}

// Provider is a single configured OAuth/OIDC identity provider.
type Provider struct {
	ID           string // stable lowercase key: "google", "microsoft"
	DisplayName  string // human label for the button: "Google", "Microsoft"
	ClientID     string
	ClientSecret string
	AuthURL      string
	TokenURL     string
	Scopes       []string
	// issuerPrefix, when non-empty, is required to be a prefix of the id_token
	// `iss` claim. Left loose (Microsoft's issuer is tenant-specific).
	issuerPrefix string
}

// Info is the public description of a configured provider (no secrets), for the
// GET /api/auth/oauth/providers discovery endpoint that drives the login buttons.
type Info struct {
	ID          string `json:"id"`
	DisplayName string `json:"name"`
}

// Registry holds the set of providers configured for this deployment.
type Registry struct {
	providers map[string]Provider
	hc        *http.Client
}

// NewRegistryFromEnv builds the provider registry from environment variables.
// Only providers whose client id AND secret are both present are registered; any
// other provider is absent (callers treat "absent" as not-configured).
//
//	Google     — GOOGLE_OAUTH_CLIENT_ID / GOOGLE_OAUTH_CLIENT_SECRET
//	Microsoft  — MS_OAUTH_CLIENT_ID / MS_OAUTH_CLIENT_SECRET
//	             (MICROSOFT_OAUTH_CLIENT_ID / _SECRET accepted as aliases;
//	              MS_OAUTH_TENANT overrides the tenant, default "common")
func NewRegistryFromEnv() *Registry {
	r := &Registry{
		providers: map[string]Provider{},
		hc:        &http.Client{Timeout: 15 * time.Second},
	}

	if id, sec := os.Getenv("GOOGLE_OAUTH_CLIENT_ID"), os.Getenv("GOOGLE_OAUTH_CLIENT_SECRET"); id != "" && sec != "" {
		r.providers["google"] = Provider{
			ID:           "google",
			DisplayName:  "Google",
			ClientID:     id,
			ClientSecret: sec,
			AuthURL:      "https://accounts.google.com/o/oauth2/v2/auth",
			TokenURL:     "https://oauth2.googleapis.com/token",
			Scopes:       []string{"openid", "email", "profile"},
			issuerPrefix: "https://accounts.google.com",
		}
	}

	msID := firstNonEmpty(os.Getenv("MS_OAUTH_CLIENT_ID"), os.Getenv("MICROSOFT_OAUTH_CLIENT_ID"))
	msSec := firstNonEmpty(os.Getenv("MS_OAUTH_CLIENT_SECRET"), os.Getenv("MICROSOFT_OAUTH_CLIENT_SECRET"))
	if msID != "" && msSec != "" {
		tenant := firstNonEmpty(os.Getenv("MS_OAUTH_TENANT"), "common")
		r.providers["microsoft"] = Provider{
			ID:           "microsoft",
			DisplayName:  "Microsoft",
			ClientID:     msID,
			ClientSecret: msSec,
			AuthURL:      "https://login.microsoftonline.com/" + tenant + "/oauth2/v2.0/authorize",
			TokenURL:     "https://login.microsoftonline.com/" + tenant + "/oauth2/v2.0/token",
			Scopes:       []string{"openid", "email", "profile"},
			issuerPrefix: "https://login.microsoftonline.com/",
		}
	}

	return r
}

// SetHTTPClient overrides the HTTP client used for the token exchange. Tests use
// this to point the exchange at an httptest server.
func (r *Registry) SetHTTPClient(hc *http.Client) { r.hc = hc }

// Register adds/overrides a provider directly. Used by tests to inject a fake
// provider whose TokenURL points at an httptest server.
func (r *Registry) Register(p Provider) { r.providers[p.ID] = p }

// Get returns the configured provider for id, or ok=false if it is not configured.
func (r *Registry) Get(id string) (Provider, bool) {
	p, ok := r.providers[strings.ToLower(strings.TrimSpace(id))]
	return p, ok
}

// Configured returns the public info for every configured provider, in a stable
// order (google, microsoft, then any others alphabetically). Drives the login
// "or continue with" buttons.
func (r *Registry) Configured() []Info {
	var out []Info
	order := []string{"google", "microsoft"}
	seen := map[string]bool{}
	for _, id := range order {
		if p, ok := r.providers[id]; ok {
			out = append(out, Info{ID: p.ID, DisplayName: p.DisplayName})
			seen[id] = true
		}
	}
	// Any additional providers (extensibility) appended after the well-known two.
	var extra []string
	for id := range r.providers {
		if !seen[id] {
			extra = append(extra, id)
		}
	}
	for _, id := range extra {
		p := r.providers[id]
		out = append(out, Info{ID: p.ID, DisplayName: p.DisplayName})
	}
	return out
}

// AuthCodeURL builds the provider authorize URL with PKCE (S256) and state.
func (p Provider) AuthCodeURL(redirectURI, state, codeChallenge string) string {
	q := url.Values{}
	q.Set("response_type", "code")
	q.Set("client_id", p.ClientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("scope", strings.Join(p.Scopes, " "))
	q.Set("state", state)
	q.Set("code_challenge", codeChallenge)
	q.Set("code_challenge_method", "S256")
	// Ask Google to return a stable, verified email + prompt account choice.
	q.Set("access_type", "online")
	q.Set("prompt", "select_account")
	sep := "?"
	if strings.Contains(p.AuthURL, "?") {
		sep = "&"
	}
	return p.AuthURL + sep + q.Encode()
}

// tokenResponse is the subset of the RFC 6749 token endpoint response we read.
type tokenResponse struct {
	IDToken     string `json:"id_token"`
	AccessToken string `json:"access_token"`
	Error       string `json:"error"`
	ErrorDesc   string `json:"error_description"`
}

// Exchange trades an authorization code + PKCE verifier for the caller's verified
// identity. It performs the back-channel token request, then extracts and validates
// the id_token claims (audience must be our client id; token must not be expired).
func (r *Registry) Exchange(ctx context.Context, p Provider, redirectURI, code, codeVerifier string) (*Identity, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", redirectURI)
	form.Set("client_id", p.ClientID)
	form.Set("client_secret", p.ClientSecret)
	form.Set("code_verifier", codeVerifier)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("oauthclient: build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := r.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("oauthclient: token request: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("oauthclient: read token response: %w", err)
	}

	var tr tokenResponse
	if err := json.Unmarshal(body, &tr); err != nil {
		return nil, fmt.Errorf("oauthclient: decode token response: %w", err)
	}
	if resp.StatusCode != http.StatusOK || tr.Error != "" {
		msg := tr.Error
		if tr.ErrorDesc != "" {
			msg += ": " + tr.ErrorDesc
		}
		if msg == "" {
			msg = fmt.Sprintf("status %d", resp.StatusCode)
		}
		return nil, fmt.Errorf("oauthclient: token endpoint rejected exchange (%s)", msg)
	}
	if tr.IDToken == "" {
		return nil, fmt.Errorf("oauthclient: token response has no id_token")
	}

	claims, err := parseIDTokenClaims(tr.IDToken)
	if err != nil {
		return nil, err
	}
	if err := claims.validate(p); err != nil {
		return nil, err
	}
	if claims.Sub == "" {
		return nil, fmt.Errorf("oauthclient: id_token has no subject")
	}

	return &Identity{
		Provider:      p.ID,
		Subject:       claims.Sub,
		Email:         strings.ToLower(strings.TrimSpace(claims.Email)),
		EmailVerified: claims.emailVerifiedBool(),
	}, nil
}

// idClaims is the subset of id_token claims we consume.
type idClaims struct {
	Iss           string          `json:"iss"`
	Sub           string          `json:"sub"`
	Aud           json.RawMessage `json:"aud"`
	Exp           int64           `json:"exp"`
	Email         string          `json:"email"`
	EmailVerified json.RawMessage `json:"email_verified"`
	// Microsoft personal accounts sometimes carry the address in preferred_username.
	PreferredUsername string `json:"preferred_username"`
}

// parseIDTokenClaims decodes (without signature verification — see package doc) the
// claims segment of a compact JWS id_token.
func parseIDTokenClaims(idToken string) (*idClaims, error) {
	parts := strings.Split(idToken, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("oauthclient: malformed id_token")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("oauthclient: decode id_token payload: %w", err)
	}
	var c idClaims
	if err := json.Unmarshal(payload, &c); err != nil {
		return nil, fmt.Errorf("oauthclient: decode id_token claims: %w", err)
	}
	if c.Email == "" && strings.Contains(c.PreferredUsername, "@") {
		c.Email = c.PreferredUsername
	}
	return &c, nil
}

// validate checks audience, expiry and (loosely) issuer.
func (c *idClaims) validate(p Provider) error {
	// Audience must include our client id.
	if !audienceContains(c.Aud, p.ClientID) {
		return fmt.Errorf("oauthclient: id_token audience mismatch")
	}
	// Expiry. An OIDC id_token MUST carry `exp` (OpenID Connect Core §2), so a
	// missing/zero exp is a malformed token, not an eternal one — reject it rather
	// than treat "no expiry" as "never expires". Allow a small clock skew.
	if c.Exp == 0 {
		return fmt.Errorf("oauthclient: id_token missing expiry")
	}
	if time.Now().Add(-2*time.Minute).Unix() > c.Exp {
		return fmt.Errorf("oauthclient: id_token expired")
	}
	if p.issuerPrefix != "" && !strings.HasPrefix(c.Iss, p.issuerPrefix) {
		return fmt.Errorf("oauthclient: id_token issuer mismatch")
	}
	return nil
}

// emailVerifiedBool coerces the email_verified claim, which providers encode as
// either a JSON bool or the string "true"/"false".
func (c *idClaims) emailVerifiedBool() bool {
	s := strings.TrimSpace(strings.Trim(string(c.EmailVerified), `"`))
	return s == "true"
}

// audienceContains reports whether the `aud` claim (a string or array of strings)
// contains want.
func audienceContains(raw json.RawMessage, want string) bool {
	if len(raw) == 0 || want == "" {
		return false
	}
	var single string
	if err := json.Unmarshal(raw, &single); err == nil {
		return single == want
	}
	var many []string
	if err := json.Unmarshal(raw, &many); err == nil {
		for _, a := range many {
			if a == want {
				return true
			}
		}
	}
	return false
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
