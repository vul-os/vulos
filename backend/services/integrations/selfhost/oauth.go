package selfhost

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"golang.org/x/oauth2"
)

// Provider ids. These match the CP's provider labels so downstream consumers and
// UI are identical across cloud and self-host.
const (
	ProviderGoogle    = "google"
	ProviderMicrosoft = "microsoft"
	ProviderDropbox   = "dropbox"
)

// Errors surfaced to the HTTP layer.
var (
	// ErrProviderNotConfigured means the owner's client_id/secret for a provider
	// are unset. The route treats that provider as simply unavailable (not a crash).
	ErrProviderNotConfigured = errors.New("selfhost: provider OAuth client credentials not configured")
	// ErrUnknownProvider is an unrecognized provider id → 404.
	ErrUnknownProvider = errors.New("selfhost: unknown provider")
	// ErrNoRefreshToken means the provider returned no refresh token (re-consent
	// required) — usually a missing offline-access / prompt=consent.
	ErrNoRefreshToken = errors.New("selfhost: provider returned no refresh token (re-consent required)")
	// ErrNotConnected means the account has no stored connection for a provider.
	ErrNotConnected = errors.New("selfhost: account has not connected this provider")
)

// providerEndpoints are the OAuth2 authorization/token endpoints, hardcoded so we
// do NOT pull golang.org/x/oauth2/google (and its cloud.google.com/go metadata
// dependency) into the sovereign box just for two URLs.
var providerEndpoints = map[string]oauth2.Endpoint{
	ProviderGoogle: {
		AuthURL:  "https://accounts.google.com/o/oauth2/auth",
		TokenURL: "https://oauth2.googleapis.com/token",
	},
	ProviderMicrosoft: {
		// The "common" tenant works for both personal and work/school accounts.
		AuthURL:  "https://login.microsoftonline.com/common/oauth2/v2.0/authorize",
		TokenURL: "https://login.microsoftonline.com/common/oauth2/v2.0/token",
	},
	ProviderDropbox: {
		AuthURL:  "https://www.dropbox.com/oauth2/authorize",
		TokenURL: "https://api.dropboxapi.com/oauth2/token",
	},
}

// defaultScopes are the requested scope sets. Kept intentionally close to the CP
// so a connection made on a self-host box carries the same capabilities the
// assistant/mail/Files consumers expect.
var defaultScopes = map[string][]string{
	ProviderGoogle: {
		"openid", "email", "profile",
		"https://www.googleapis.com/auth/gmail.modify",
		"https://www.googleapis.com/auth/calendar",
		"https://www.googleapis.com/auth/drive",
		"https://www.googleapis.com/auth/drive.readonly",
		"https://www.googleapis.com/auth/drive.file",
	},
	ProviderMicrosoft: {
		// offline_access is REQUIRED for Microsoft to return a refresh token.
		"offline_access", "openid", "email", "profile",
		"https://graph.microsoft.com/Mail.ReadWrite",
		"https://graph.microsoft.com/Calendars.ReadWrite",
		"https://graph.microsoft.com/Files.ReadWrite",
		"https://graph.microsoft.com/Contacts.Read",
	},
	ProviderDropbox: {
		"files.content.read", "files.content.write", "account_info.read",
	},
}

// envKeys maps a provider to its owner-set client-credential env var names.
// A self-hoster registers their own OAuth app and sets these on the box.
var envKeys = map[string]struct{ id, secret string }{
	ProviderGoogle:    {"GOOGLE_OAUTH_CLIENT_ID", "GOOGLE_OAUTH_CLIENT_SECRET"},
	ProviderMicrosoft: {"MICROSOFT_OAUTH_CLIENT_ID", "MICROSOFT_OAUTH_CLIENT_SECRET"},
	ProviderDropbox:   {"DROPBOX_OAUTH_CLIENT_ID", "DROPBOX_OAUTH_CLIENT_SECRET"},
}

// KnownProvider reports whether p is a provider this package understands.
func KnownProvider(p string) bool {
	_, ok := envKeys[p]
	return ok
}

// ProviderConfigured reports whether the owner's client credentials for p are set.
// An unconfigured provider is simply unavailable — never a crash.
func ProviderConfigured(p string) bool {
	k, ok := envKeys[p]
	if !ok {
		return false
	}
	return strings.TrimSpace(os.Getenv(k.id)) != "" && strings.TrimSpace(os.Getenv(k.secret)) != ""
}

// OAuthRedirectBaseEnv is the box's externally-reachable base URL used to build
// the redirect URI. The redirect URI the owner registers with each provider is
// <base>/api/integrations/{provider}/callback.
const OAuthRedirectBaseEnv = "OAUTH_REDIRECT_BASE"

func redirectBase() string {
	base := strings.TrimSpace(os.Getenv(OAuthRedirectBaseEnv))
	if base == "" {
		base = "http://localhost:8080"
	}
	return strings.TrimRight(base, "/")
}

// oauthConfig builds the oauth2.Config for a provider from the owner's env.
// Returns ErrProviderNotConfigured if the client id is unset.
func oauthConfig(provider string) (*oauth2.Config, error) {
	ep, ok := providerEndpoints[provider]
	if !ok {
		return nil, ErrUnknownProvider
	}
	k := envKeys[provider]
	clientID := strings.TrimSpace(os.Getenv(k.id))
	if clientID == "" {
		return nil, ErrProviderNotConfigured
	}
	return &oauth2.Config{
		ClientID:     clientID,
		ClientSecret: strings.TrimSpace(os.Getenv(k.secret)),
		RedirectURL:  buildRedirectURL(provider),
		Scopes:       defaultScopes[provider],
		Endpoint:     ep,
	}, nil
}

// Token is the provider-agnostic exchange/refresh result. It mirrors
// services/integrations.Token's fields so downstream consumers are unchanged.
// RefreshToken is empty on a refresh that does not rotate it (caller keeps old).
type Token struct {
	AccessToken  string
	RefreshToken string
	Expiry       time.Time
	Scopes       string // space-separated granted scopes
	Email        string // connected-account email (exchange only, best-effort)
}

// Exchanger abstracts the provider OAuth calls so tests can stub them.
type Exchanger interface {
	// AuthCodeURL returns the consent URL for state + PKCE verifier.
	AuthCodeURL(state, pkceChallenge string) (string, error)
	// Exchange swaps an auth code (+ PKCE verifier) for tokens.
	Exchange(ctx context.Context, code, pkceVerifier string) (*Token, error)
	// Refresh mints a new access token from a stored refresh token.
	Refresh(ctx context.Context, refreshToken string) (*Token, error)
}

// oauth2Exchanger is the production Exchanger backed by golang.org/x/oauth2. One
// instance serves any provider (the provider id selects the config).
type oauth2Exchanger struct {
	provider string
}

// NewExchanger returns the production Exchanger for a provider.
func NewExchanger(provider string) Exchanger { return &oauth2Exchanger{provider: provider} }

func (e *oauth2Exchanger) AuthCodeURL(state, pkceChallenge string) (string, error) {
	cfg, err := oauthConfig(e.provider)
	if err != nil {
		return "", err
	}
	opts := []oauth2.AuthCodeOption{
		oauth2.AccessTypeOffline, // Google: required to receive a refresh token
	}
	// prompt=consent forces Google to re-issue a refresh token even on re-consent.
	if e.provider == ProviderGoogle {
		opts = append(opts, oauth2.SetAuthURLParam("prompt", "consent"))
	}
	if e.provider == ProviderDropbox {
		// Dropbox needs token_access_type=offline to return a refresh token.
		opts = append(opts, oauth2.SetAuthURLParam("token_access_type", "offline"))
	}
	// PKCE (S256) — CSRF/interception defense on the auth-code flow.
	if pkceChallenge != "" {
		opts = append(opts,
			oauth2.SetAuthURLParam("code_challenge", pkceChallenge),
			oauth2.SetAuthURLParam("code_challenge_method", "S256"),
		)
	}
	return cfg.AuthCodeURL(state, opts...), nil
}

func (e *oauth2Exchanger) Exchange(ctx context.Context, code, pkceVerifier string) (*Token, error) {
	cfg, err := oauthConfig(e.provider)
	if err != nil {
		return nil, err
	}
	var opts []oauth2.AuthCodeOption
	if pkceVerifier != "" {
		opts = append(opts, oauth2.SetAuthURLParam("code_verifier", pkceVerifier))
	}
	tok, err := cfg.Exchange(ctx, code, opts...)
	if err != nil {
		return nil, fmt.Errorf("selfhost: oauth exchange: %w", err)
	}
	if tok.RefreshToken == "" {
		return nil, ErrNoRefreshToken
	}
	out := &Token{
		AccessToken:  tok.AccessToken,
		RefreshToken: tok.RefreshToken,
		Expiry:       tok.Expiry,
		Scopes:       grantedScopes(tok, cfg),
	}
	out.Email = bestEffortEmail(ctx, e.provider, cfg, tok)
	return out, nil
}

func (e *oauth2Exchanger) Refresh(ctx context.Context, refreshToken string) (*Token, error) {
	cfg, err := oauthConfig(e.provider)
	if err != nil {
		return nil, err
	}
	// A token with only a RefreshToken and a past expiry forces the TokenSource to
	// refresh on the first Token() call.
	src := cfg.TokenSource(ctx, &oauth2.Token{
		RefreshToken: refreshToken,
		Expiry:       time.Now().Add(-time.Hour),
	})
	tok, err := src.Token()
	if err != nil {
		return nil, fmt.Errorf("selfhost: oauth refresh: %w", err)
	}
	return &Token{
		AccessToken:  tok.AccessToken,
		RefreshToken: tok.RefreshToken, // often empty; caller keeps the old one
		Expiry:       tok.Expiry,
		Scopes:       grantedScopes(tok, cfg),
	}, nil
}

// grantedScopes prefers the provider-returned "scope" extra, else the requested
// config scopes.
func grantedScopes(tok *oauth2.Token, cfg *oauth2.Config) string {
	if s, ok := tok.Extra("scope").(string); ok && s != "" {
		return s
	}
	return strings.Join(cfg.Scopes, " ")
}

// bestEffortEmail fetches the connected-account email for display. Failures are
// swallowed (the connection is still valid without the display email).
func bestEffortEmail(ctx context.Context, provider string, cfg *oauth2.Config, tok *oauth2.Token) string {
	var infoURL string
	switch provider {
	case ProviderGoogle:
		infoURL = "https://openidconnect.googleapis.com/v1/userinfo"
	case ProviderMicrosoft:
		infoURL = "https://graph.microsoft.com/v1.0/me"
	default:
		return ""
	}
	client := cfg.Client(ctx, tok)
	client.Timeout = 10 * time.Second
	resp, err := client.Get(infoURL)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	// Cheap extraction without a struct-per-provider: both return a JSON object
	// with an email-ish field. Look for "email" or "userPrincipalName".
	for _, key := range []string{`"email":"`, `"userPrincipalName":"`, `"mail":"`} {
		if i := strings.Index(string(body), key); i >= 0 {
			rest := string(body)[i+len(key):]
			if j := strings.IndexByte(rest, '"'); j > 0 {
				return rest[:j]
			}
		}
	}
	return ""
}

// buildRedirectURL is the single source of truth for the OAuth callback URI: the
// redirect the owner must register with each provider is
// <OAUTH_REDIRECT_BASE>/api/integrations/{provider}/callback. oauthConfig uses it
// so the registered redirect and the one sent in the auth-code flow cannot drift.
func buildRedirectURL(provider string) string {
	return redirectBase() + "/api/integrations/" + provider + "/callback"
}

// escape is a tiny helper for building error redirect query values.
func escape(s string) string { return url.QueryEscape(s) }
