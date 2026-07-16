package integrations

// microsoft.go — OAUTH-EVERYWHERE (JOB 1): Microsoft (Entra ID / Graph) OAuth
// broker for the upcoming importer. Mirrors the Google/Dropbox integration seam
// (oauth.go, dropbox.go): the CP holds the single fleet-wide app
// client_id/secret + a stable redirect_uri, runs the authorization-code
// exchange, and custodies each account's refresh token ENCRYPTED at rest. Boxes
// never see the refresh token or the app secret — they mint short-lived access
// tokens over the INTEG-SEC-01 device channel
// (GET /api/integrations/microsoft/token).
//
// Microsoft specifics:
//   - Endpoints are the v2.0 ("common" multi-tenant) authorize/token URLs. We
//     use a custom oauth2.Endpoint (kept here rather than pulling in
//     x/oauth2/microsoft) so the tenant is pinned to "common" — both personal
//     (MSA) and work/school (Entra ID) accounts can connect.
//   - A refresh token is only returned when the "offline_access" scope is
//     requested (the analogue of Google's access_type=offline / Dropbox's
//     token_access_type=offline). It is included in microsoftScopes below.
//   - Account identity for display comes from Microsoft Graph /v1.0/me
//     (id → provider sub, mail/userPrincipalName → email).
//   - Microsoft has no simple OAuth refresh-token revocation endpoint (revoking a
//     grant requires a privileged Graph call against the user's sign-in
//     sessions). Revoke is therefore a best-effort no-op: Disconnect still
//     deletes the locally stored credential, after which the box can no longer
//     mint — see Service.Disconnect.
//
// ── box → CP token-mint contract (provider=microsoft) ──────────────────────
// A box mints a short-lived Microsoft Graph access token exactly like Google:
//
//	GET /api/integrations/microsoft/token
//	X-Device-ULID:     <canonical box ULID>
//	X-Device-Sig | X-Integration-Sig (INTEG-SEC-01 device auth)
//	?scope=ms.files | ms.contacts | ms.calendar          (optional coverage hint)
//
// The minted access token carries every scope the connected account granted
// (Files.Read + Contacts.Read + Calendars.Read for the importer), so the SAME
// token works against Graph for OneDrive/Office files, Outlook Contacts and
// Calendar. The refresh token and client secret never leave the CP. If a scope
// hint is supplied and the connection did not authorize it, the mint returns 403
// ("reconnect to grant") instead of a token that would 403 at Graph.

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
	// ProviderMicrosoft is the Microsoft (Entra ID / Graph) connected-account
	// provider: OneDrive/Office files, Outlook Contacts + Calendar.
	ProviderMicrosoft = "microsoft"

	// Microsoft identity platform v2.0 "common" (multi-tenant) endpoints.
	microsoftAuthURL  = "https://login.microsoftonline.com/common/oauth2/v2.0/authorize"
	microsoftTokenURL = "https://login.microsoftonline.com/common/oauth2/v2.0/token"
	microsoftMeURL    = "https://graph.microsoft.com/v1.0/me"

	// Microsoft Graph data scopes for the importer. The v2.0 endpoint accepts
	// both the short form (Files.Read) and the resource-qualified form; we
	// request the qualified form and match either at mint time (HasScope checks
	// both via the Has* helpers below).
	ScopeMSFilesRead    = "https://graph.microsoft.com/Files.Read"     // OneDrive / Office files (read)
	ScopeMSContactsRead = "https://graph.microsoft.com/Contacts.Read"  // Outlook contacts (read)
	ScopeMSCalendarRead = "https://graph.microsoft.com/Calendars.Read" // Outlook calendar (read)

	// Short (unqualified) forms Microsoft may echo back in the granted "scope".
	scopeMSFilesReadShort    = "Files.Read"
	scopeMSContactsReadShort = "Contacts.Read"
	scopeMSCalendarReadShort = "Calendars.Read"
)

// microsoftScopes is the connected-account scope set for the importer:
// openid/email/profile identify the account, offline_access is REQUIRED for a
// refresh token, and the three Graph scopes grant read access to OneDrive/Office
// files, Outlook Contacts and Calendar. All are additive/opt-in at the consent
// screen.
var microsoftScopes = []string{
	"openid",
	"email",
	"profile",
	"offline_access",
	ScopeMSFilesRead,
	ScopeMSContactsRead,
	ScopeMSCalendarRead,
}

// MicrosoftScopes returns a copy of the requested Microsoft scope set.
func MicrosoftScopes() []string { return append([]string(nil), microsoftScopes...) }

// HasMicrosoftFilesRead / Contacts / Calendar report whether the granted scopes
// authorize the corresponding Graph read. Both the resource-qualified and the
// short form are accepted, since Microsoft is inconsistent about which it
// echoes back in the token's "scope".
func HasMicrosoftFilesRead(granted string) bool {
	return HasScope(granted, ScopeMSFilesRead) || HasScope(granted, scopeMSFilesReadShort)
}
func HasMicrosoftContactsRead(granted string) bool {
	return HasScope(granted, ScopeMSContactsRead) || HasScope(granted, scopeMSContactsReadShort)
}
func HasMicrosoftCalendarRead(granted string) bool {
	return HasScope(granted, ScopeMSCalendarRead) || HasScope(granted, scopeMSCalendarReadShort)
}

// MicrosoftExchanger implements Exchanger via golang.org/x/oauth2 with a custom
// Microsoft v2.0 endpoint.
type MicrosoftExchanger struct{}

var _ Exchanger = MicrosoftExchanger{}

// microsoftEndpoint is the Microsoft identity platform v2.0 OAuth2 endpoint.
var microsoftEndpoint = oauth2.Endpoint{
	AuthURL:  microsoftAuthURL,
	TokenURL: microsoftTokenURL,
}

// microsoftConfig builds the oauth2.Config from environment.
//
//	MICROSOFT_OAUTH_CLIENT_ID, MICROSOFT_OAUTH_CLIENT_SECRET, OAUTH_REDIRECT_BASE
//
// The redirect URI is OAUTH_REDIRECT_BASE + /api/integrations/microsoft/callback,
// which must be registered on the single fleet-wide Entra ID app registration.
func microsoftConfig() (*oauth2.Config, error) {
	clientID := os.Getenv("MICROSOFT_OAUTH_CLIENT_ID")
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
		ClientSecret: os.Getenv("MICROSOFT_OAUTH_CLIENT_SECRET"),
		RedirectURL:  redirectBase + "/api/integrations/microsoft/callback",
		Scopes:       microsoftScopes,
		Endpoint:     microsoftEndpoint,
	}, nil
}

// MicrosoftConfigured reports whether Microsoft OAuth env is present (for /status).
func MicrosoftConfigured() bool { return os.Getenv("MICROSOFT_OAUTH_CLIENT_ID") != "" }

func (MicrosoftExchanger) AuthCodeURL(state string) (string, error) {
	cfg, err := microsoftConfig()
	if err != nil {
		return "", err
	}
	// prompt=consent forces the consent screen so a reconnect re-issues a refresh
	// token (offline_access is in the scope set; without prompt Microsoft may skip
	// consent and omit the refresh token on a silent re-auth).
	return cfg.AuthCodeURL(state,
		oauth2.SetAuthURLParam("prompt", "consent"),
	), nil
}

func (MicrosoftExchanger) Exchange(ctx context.Context, code string) (*Token, error) {
	cfg, err := microsoftConfig()
	if err != nil {
		return nil, err
	}
	tok, err := cfg.Exchange(ctx, code)
	if err != nil {
		return nil, fmt.Errorf("integrations: microsoft oauth exchange: %w", err)
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
	if email, sub := microsoftIdentity(ctx, tok.AccessToken); email != "" || sub != "" {
		out.Email = email
		out.Sub = sub
	}
	return out, nil
}

func (MicrosoftExchanger) Refresh(ctx context.Context, refreshToken string) (*Token, error) {
	cfg, err := microsoftConfig()
	if err != nil {
		return nil, err
	}
	src := cfg.TokenSource(ctx, &oauth2.Token{
		RefreshToken: refreshToken,
		Expiry:       time.Now().Add(-time.Hour),
	})
	tok, err := src.Token()
	if err != nil {
		return nil, fmt.Errorf("integrations: microsoft oauth refresh: %w", err)
	}
	return &Token{
		AccessToken:  tok.AccessToken,
		RefreshToken: tok.RefreshToken, // Microsoft rotates the refresh token; caller persists it
		Expiry:       tok.Expiry,
		Scopes:       grantedScopes(tok, cfg),
	}, nil
}

// Revoke is a best-effort no-op: Microsoft has no simple OAuth refresh-token
// revocation endpoint. Disconnect deletes the locally stored credential, after
// which no further tokens can be minted.
func (MicrosoftExchanger) Revoke(_ context.Context, _ string) error { return nil }

// microsoftAccount is the subset of Graph /me we display.
type microsoftAccount struct {
	ID                string `json:"id"`
	Mail              string `json:"mail"`
	UserPrincipalName string `json:"userPrincipalName"`
}

// microsoftIdentity best-effort resolves (email, sub) for display from Graph /me.
// Prefers the routable "mail"; falls back to userPrincipalName when mail is unset
// (common for some Entra accounts).
func microsoftIdentity(ctx context.Context, accessToken string) (email, sub string) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, microsoftMeURL, nil)
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
	var acct microsoftAccount
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&acct); err != nil {
		return "", ""
	}
	email = acct.Mail
	if email == "" {
		email = acct.UserPrincipalName
	}
	return email, acct.ID
}
