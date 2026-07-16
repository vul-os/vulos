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

// Provider kinds. OIDC providers return a signed id_token from the token
// endpoint (identity read from its claims); API providers return only an OAuth2
// access_token, and the identity + verified email are fetched from the provider's
// REST API with that token.
const (
	KindOIDC    = "oidc"    // Google, Microsoft — id_token
	KindGitHub  = "github"  // GET /user + /user/emails
	KindDiscord = "discord" // GET /users/@me
)

// Provider is a single configured OAuth/OIDC identity provider. New providers are
// added by appending a config block in NewRegistryFromEnv (env-gated) plus, for a
// non-OIDC provider, an identity-fetch branch in Exchange — nothing else changes.
type Provider struct {
	ID           string // stable lowercase key: "google", "microsoft", "github", "discord"
	DisplayName  string // human label for the button: "Google", "Microsoft", …
	Kind         string // KindOIDC (default) | KindGitHub | KindDiscord
	ClientID     string
	ClientSecret string
	AuthURL      string
	TokenURL     string
	Scopes       []string
	// UserInfoURL / EmailsURL are the REST endpoints hit for API-kind providers
	// (GitHub, Discord). Fields (not constants) so tests can retarget them at an
	// httptest server via Register.
	UserInfoURL string
	EmailsURL   string
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
//	GitHub     — GITHUB_OAUTH_CLIENT_ID / GITHUB_OAUTH_CLIENT_SECRET
//	Discord    — DISCORD_OAUTH_CLIENT_ID / DISCORD_OAUTH_CLIENT_SECRET
//
// EVERY provider is chosen so we can obtain the user's EMAIL: Google/Microsoft
// request the `email` scope (OIDC id_token), GitHub requests `user:email` (read
// via /user/emails), and Discord requests `email` (read from /users/@me). A
// provider that cannot yield an email is never added here.
func NewRegistryFromEnv() *Registry {
	r := &Registry{
		providers: map[string]Provider{},
		hc:        &http.Client{Timeout: 15 * time.Second},
	}

	if id, sec := os.Getenv("GOOGLE_OAUTH_CLIENT_ID"), os.Getenv("GOOGLE_OAUTH_CLIENT_SECRET"); id != "" && sec != "" {
		r.providers["google"] = Provider{
			ID:           "google",
			DisplayName:  "Google",
			Kind:         KindOIDC,
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
			Kind:         KindOIDC,
			ClientID:     msID,
			ClientSecret: msSec,
			AuthURL:      "https://login.microsoftonline.com/" + tenant + "/oauth2/v2.0/authorize",
			TokenURL:     "https://login.microsoftonline.com/" + tenant + "/oauth2/v2.0/token",
			Scopes:       []string{"openid", "email", "profile"},
			issuerPrefix: "https://login.microsoftonline.com/",
		}
	}

	if id, sec := os.Getenv("GITHUB_OAUTH_CLIENT_ID"), os.Getenv("GITHUB_OAUTH_CLIENT_SECRET"); id != "" && sec != "" {
		r.providers["github"] = Provider{
			ID:           "github",
			DisplayName:  "GitHub",
			Kind:         KindGitHub,
			ClientID:     id,
			ClientSecret: sec,
			AuthURL:      "https://github.com/login/oauth/authorize",
			TokenURL:     "https://github.com/login/oauth/access_token",
			// user:email is REQUIRED so /user/emails returns the primary verified
			// address even when the profile email is private.
			Scopes:      []string{"read:user", "user:email"},
			UserInfoURL: "https://api.github.com/user",
			EmailsURL:   "https://api.github.com/user/emails",
		}
	}

	if id, sec := os.Getenv("DISCORD_OAUTH_CLIENT_ID"), os.Getenv("DISCORD_OAUTH_CLIENT_SECRET"); id != "" && sec != "" {
		r.providers["discord"] = Provider{
			ID:           "discord",
			DisplayName:  "Discord",
			Kind:         KindDiscord,
			ClientID:     id,
			ClientSecret: sec,
			AuthURL:      "https://discord.com/api/oauth2/authorize",
			TokenURL:     "https://discord.com/api/oauth2/token",
			// `email` is REQUIRED so /users/@me returns the address; `identify`
			// yields the stable user id (subject).
			Scopes:      []string{"identify", "email"},
			UserInfoURL: "https://discord.com/api/users/@me",
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
	order := []string{"google", "microsoft", "github", "discord"}
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
	// OIDC-only hints (Google/Microsoft): ask for a stable, verified email and
	// prompt account choice. GitHub/Discord don't understand these params, so we
	// omit them there to avoid a rejected authorize request.
	if p.Kind == "" || p.Kind == KindOIDC {
		q.Set("access_type", "online")
		q.Set("prompt", "select_account")
	}
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

	// Non-OIDC providers return only an access_token; the identity + verified
	// email are read from the provider's REST API with that bearer token.
	switch p.Kind {
	case KindGitHub:
		return r.fetchGitHubIdentity(ctx, p, tr.AccessToken)
	case KindDiscord:
		return r.fetchDiscordIdentity(ctx, p, tr.AccessToken)
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

// apiGet performs an authenticated GET against a provider REST endpoint and
// decodes the JSON body into out. Bearer-token auth; 1 MiB response cap.
func (r *Registry) apiGet(ctx context.Context, url, accessToken, accept string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("oauthclient: build api request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	if accept == "" {
		accept = "application/json"
	}
	req.Header.Set("Accept", accept)
	// GitHub requires a User-Agent; a static one is fine and provider-neutral.
	req.Header.Set("User-Agent", "vulos-oauthclient")
	resp, err := r.hc.Do(req)
	if err != nil {
		return fmt.Errorf("oauthclient: api request: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("oauthclient: read api response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("oauthclient: api endpoint %s returned status %d", url, resp.StatusCode)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("oauthclient: decode api response: %w", err)
	}
	return nil
}

// fetchGitHubIdentity reads the GitHub profile (stable numeric id = subject) and
// resolves the PRIMARY VERIFIED email from /user/emails (the profile email may be
// private/absent). EmailVerified is true only for a GitHub-verified address.
func (r *Registry) fetchGitHubIdentity(ctx context.Context, p Provider, accessToken string) (*Identity, error) {
	if accessToken == "" {
		return nil, fmt.Errorf("oauthclient: github token response has no access_token")
	}
	var profile struct {
		ID    int64  `json:"id"`
		Login string `json:"login"`
	}
	if err := r.apiGet(ctx, p.UserInfoURL, accessToken, "application/vnd.github+json", &profile); err != nil {
		return nil, err
	}
	if profile.ID == 0 {
		return nil, fmt.Errorf("oauthclient: github profile has no id")
	}

	email, verified := "", false
	if p.EmailsURL != "" {
		var emails []struct {
			Email    string `json:"email"`
			Primary  bool   `json:"primary"`
			Verified bool   `json:"verified"`
		}
		// Best-effort: if the emails call fails, fall through with an empty email so
		// the route layer forces the user to type one (mandatory-email rule).
		if err := r.apiGet(ctx, p.EmailsURL, accessToken, "application/vnd.github+json", &emails); err == nil {
			// Prefer the primary verified address, else any verified address.
			for _, e := range emails {
				if e.Verified && e.Primary {
					email, verified = e.Email, true
					break
				}
			}
			if email == "" {
				for _, e := range emails {
					if e.Verified {
						email, verified = e.Email, true
						break
					}
				}
			}
		}
	}

	return &Identity{
		Provider:      p.ID,
		Subject:       fmt.Sprintf("%d", profile.ID),
		Email:         strings.ToLower(strings.TrimSpace(email)),
		EmailVerified: verified,
	}, nil
}

// fetchDiscordIdentity reads /users/@me: the stable snowflake id (subject), the
// account email, and Discord's own `verified` flag (email verification).
func (r *Registry) fetchDiscordIdentity(ctx context.Context, p Provider, accessToken string) (*Identity, error) {
	if accessToken == "" {
		return nil, fmt.Errorf("oauthclient: discord token response has no access_token")
	}
	var me struct {
		ID       string `json:"id"`
		Email    string `json:"email"`
		Verified bool   `json:"verified"`
	}
	if err := r.apiGet(ctx, p.UserInfoURL, accessToken, "application/json", &me); err != nil {
		return nil, err
	}
	if me.ID == "" {
		return nil, fmt.Errorf("oauthclient: discord profile has no id")
	}
	return &Identity{
		Provider:      p.ID,
		Subject:       me.ID,
		Email:         strings.ToLower(strings.TrimSpace(me.Email)),
		// Discord only returns an email at all when the `email` scope was granted;
		// `verified` reflects whether Discord verified it.
		EmailVerified: me.Verified && me.Email != "",
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
