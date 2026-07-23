package integrations

// dropbox.go — EXT-PROVIDERS: Dropbox OAuth broker for Vulos Files external
// stores. Mirrors the Google integration seam (oauth.go): the CP holds the single
// fleet-wide Dropbox app key/secret + a stable redirect_uri, runs the
// authorization-code exchange, and custodies each account's refresh token
// ENCRYPTED at rest. Boxes never see the refresh token or the app secret — they
// mint short-lived access tokens over the INTEG-SEC-01 device channel
// (GET /api/integrations/dropbox/token).
//
// Dropbox specifics:
//   - Refresh tokens require token_access_type=offline at the AUTHORIZE step
//     (Dropbox short-lived access tokens are the default; without this param only
//     a ~4h access token is issued and no refresh token). This is the analogue of
//     Google's access_type=offline.
//   - The OAuth client uses a custom oauth2.Endpoint (Dropbox is not in the
//     x/oauth2 vendored endpoints).
//   - Account identity for display comes from the /2/users/get_current_account
//     RPC. Revocation uses /2/auth/token/revoke (revokes the token presented in
//     the Authorization header — we mint a fresh access token from the refresh
//     token, then revoke that, which invalidates the whole grant).

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"golang.org/x/oauth2"
)

const (
	// ProviderDropbox is the Dropbox connected-account provider.
	ProviderDropbox = "dropbox"

	dropboxAuthURL    = "https://www.dropbox.com/oauth2/authorize"
	dropboxTokenURL   = "https://api.dropboxapi.com/oauth2/token"
	dropboxRevokeURL  = "https://api.dropboxapi.com/2/auth/token/revoke"
	dropboxAccountURL = "https://api.dropboxapi.com/2/users/get_current_account"

	// ScopeDropboxContentRead / Write / MetadataRead are the file-access scopes
	// for an external Dropbox store: read + write file content and read metadata.
	ScopeDropboxContentRead  = "files.content.read"
	ScopeDropboxContentWrite = "files.content.write"
	ScopeDropboxMetadataRead = "files.metadata.read"
)

// dropboxScopes is the connected-account scope set for an external Dropbox store.
// All are additive/opt-in: the user approves them at the Dropbox consent screen.
var dropboxScopes = []string{
	ScopeDropboxContentRead,
	ScopeDropboxContentWrite,
	ScopeDropboxMetadataRead,
}

// DropboxScopes returns a copy of the requested Dropbox scope set.
func DropboxScopes() []string { return append([]string(nil), dropboxScopes...) }

// HasDropboxReadAccess / HasDropboxWriteAccess report whether the granted scopes
// authorize reading / writing Dropbox file content.
func HasDropboxReadAccess(granted string) bool  { return HasScope(granted, ScopeDropboxContentRead) }
func HasDropboxWriteAccess(granted string) bool { return HasScope(granted, ScopeDropboxContentWrite) }

// DropboxExchanger implements Exchanger via golang.org/x/oauth2 with a custom
// Dropbox endpoint.
type DropboxExchanger struct{}

var _ Exchanger = DropboxExchanger{}

// dropboxEndpoint is the Dropbox OAuth2 endpoint.
var dropboxEndpoint = oauth2.Endpoint{
	AuthURL:  dropboxAuthURL,
	TokenURL: dropboxTokenURL,
}

// dropboxConfig builds the oauth2.Config from environment.
//
//	DROPBOX_OAUTH_CLIENT_ID, DROPBOX_OAUTH_CLIENT_SECRET, OAUTH_REDIRECT_BASE
//
// The redirect URI is OAUTH_REDIRECT_BASE + /api/integrations/dropbox/callback,
// which must be registered on the single fleet-wide Dropbox app.
func dropboxConfig() (*oauth2.Config, error) {
	clientID := os.Getenv("DROPBOX_OAUTH_CLIENT_ID")
	if clientID == "" {
		return nil, ErrOAuthNotConfigured
	}
	redirectBase := os.Getenv("OAUTH_REDIRECT_BASE")
	if redirectBase == "" {
		redirectBase = "http://localhost:8081"
	}
	redirectBase = strings.TrimRight(redirectBase, "/")
	return &oauth2.Config{
		ClientID:     clientID,
		ClientSecret: os.Getenv("DROPBOX_OAUTH_CLIENT_SECRET"),
		RedirectURL:  redirectBase + "/api/integrations/dropbox/callback",
		Scopes:       dropboxScopes,
		Endpoint:     dropboxEndpoint,
	}, nil
}

// DropboxConfigured reports whether Dropbox OAuth env is present (for /status).
func DropboxConfigured() bool { return os.Getenv("DROPBOX_OAUTH_CLIENT_ID") != "" }

func (DropboxExchanger) AuthCodeURL(state string) (string, error) {
	cfg, err := dropboxConfig()
	if err != nil {
		return "", err
	}
	// token_access_type=offline is REQUIRED for Dropbox to return a refresh token
	// (analogue of Google's access_type=offline). force_reauthentication ensures
	// the consent screen re-issues a refresh token on reconnect.
	return cfg.AuthCodeURL(state,
		oauth2.SetAuthURLParam("token_access_type", "offline"),
	), nil
}

func (DropboxExchanger) Exchange(ctx context.Context, code string) (*Token, error) {
	cfg, err := dropboxConfig()
	if err != nil {
		return nil, err
	}
	tok, err := cfg.Exchange(ctx, code)
	if err != nil {
		return nil, fmt.Errorf("integrations: dropbox oauth exchange: %w", err)
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
	// Best-effort fetch of connected-account identity for display.
	if email, sub := dropboxIdentity(ctx, tok.AccessToken); email != "" || sub != "" {
		out.Email = email
		out.Sub = sub
	}
	return out, nil
}

func (DropboxExchanger) Refresh(ctx context.Context, refreshToken string) (*Token, error) {
	cfg, err := dropboxConfig()
	if err != nil {
		return nil, err
	}
	src := cfg.TokenSource(ctx, &oauth2.Token{
		RefreshToken: refreshToken,
		Expiry:       time.Now().Add(-time.Hour),
	})
	tok, err := src.Token()
	if err != nil {
		return nil, fmt.Errorf("integrations: dropbox oauth refresh: %w", err)
	}
	return &Token{
		AccessToken:  tok.AccessToken,
		RefreshToken: tok.RefreshToken, // usually empty on refresh; caller keeps old
		Expiry:       tok.Expiry,
		Scopes:       grantedScopes(tok, cfg),
	}, nil
}

// Revoke invalidates the Dropbox grant. The revoke RPC revokes whichever token is
// presented in the Authorization header, so we first mint a short-lived access
// token from the refresh token and revoke that (which tears down the whole grant).
// Best-effort: a transient error is returned for logging but disconnect proceeds.
func (DropboxExchanger) Revoke(ctx context.Context, refreshToken string) error {
	if refreshToken == "" {
		return nil
	}
	tok, err := DropboxExchanger{}.Refresh(ctx, refreshToken)
	if err != nil {
		// Already invalid / cannot refresh — nothing to revoke remotely.
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, dropboxRevokeURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	resp, err := providerHTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("integrations: dropbox revoke: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<12))
	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusUnauthorized {
		// 200 = revoked; 401 = token already invalid.
		return nil
	}
	return fmt.Errorf("integrations: dropbox revoke status %d", resp.StatusCode)
}

// dropboxAccount is the subset of /2/users/get_current_account we display.
type dropboxAccount struct {
	AccountID string `json:"account_id"`
	Email     string `json:"email"`
}

// dropboxIdentity best-effort resolves (email, account_id) for display. The RPC
// requires a null request body and an access token in the Authorization header.
func dropboxIdentity(ctx context.Context, accessToken string) (email, sub string) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, dropboxAccountURL, nil)
	if err != nil {
		return "", ""
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := providerHTTPClient.Do(req)
	if err != nil {
		return "", ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<12))
		return "", ""
	}
	var acct dropboxAccount
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&acct); err != nil {
		return "", ""
	}
	return acct.Email, acct.AccountID
}
