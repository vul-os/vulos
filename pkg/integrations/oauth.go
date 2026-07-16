package integrations

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

const (
	// ProviderGoogle is the Google connected-account provider (Gmail/Calendar/
	// Drive/GCS).
	ProviderGoogle = "google"
	// ProviderGCS is a MINT-ONLY alias: a box that wants a Google Cloud Storage
	// access token calls the token mint with provider=gcs. There is no separate
	// "gcs" connection — the token is brokered from the user's existing Google
	// connection (which must have granted ScopeDevstorageReadWrite). See the GCS
	// design note below.
	ProviderGCS = "gcs"

	// ScopeDrive is full read/write Google Drive access (broad).
	ScopeDrive = "https://www.googleapis.com/auth/drive"
	// ScopeDriveReadonly is read-only Google Drive access — the least-privilege
	// scope for Vulos Files external (read-only) stores backed by Drive.
	ScopeDriveReadonly = "https://www.googleapis.com/auth/drive.readonly"
	// ScopeDriveFile is per-file Google Drive write access (create/upload + manage
	// files the app created or the user opened). This is the least-privilege WRITE
	// scope for Vulos Files external (read/write) stores backed by Drive — it lets
	// a connected account upload/create without granting blanket access to every
	// existing file in the user's Drive.
	ScopeDriveFile = "https://www.googleapis.com/auth/drive.file"
	// ScopeDevstorageReadWrite is read/write access to the connected Google
	// account's Google Cloud Storage objects. GCS bucket mounts are brokered by
	// reusing the Google OAuth grant with this scope (no separate provider OAuth
	// client); the OS supplies the bucket name, the CP just brokers the token.
	ScopeDevstorageReadWrite = "https://www.googleapis.com/auth/devstorage.read_write"
	// ScopeContactsReadonly is read-only access to the connected account's Google
	// Contacts (People API) — the least-privilege scope the importer needs to read
	// contacts.
	ScopeContactsReadonly = "https://www.googleapis.com/auth/contacts.readonly"
	// ScopeCalendarReadonly is read-only access to the connected account's Google
	// Calendar — the least-privilege scope the importer needs to read events.
	ScopeCalendarReadonly = "https://www.googleapis.com/auth/calendar.readonly"

	googleUserinfoURL = "https://openidconnect.googleapis.com/v1/userinfo"
	stateCookie       = "vc_integ_state"
	stateMaxAgeSec    = 600 // 10 minutes
	stateNonceLen     = 16
)

// Errors surfaced to route handlers.
var (
	ErrOAuthNotConfigured = errors.New("integrations: Google OAuth not configured")
	ErrStateMismatch      = errors.New("integrations: OAuth state mismatch")
	ErrNoRefreshToken     = errors.New("integrations: provider returned no refresh token (re-consent required)")
)

// googleScopes is the connected-account scope set: identify the account
// (openid/email), plus offline data access for the apps that consume it —
// Gmail (read+send+labels), Calendar, Drive. Wave 5 narrows these per-app.
// "profile" is included so the browser-session use-case renders a signed-in
// Google identity.
//
// Both the full-Drive scope and the read-only Drive scope are requested: the
// read-only scope (ScopeDriveReadonly) is the least-privilege grant that Vulos
// Files external (read-only) stores need, requested explicitly so a future
// per-app narrowing can drop ScopeDrive without breaking read-only Drive mounts.
// ScopeDriveFile adds least-privilege per-file WRITE so external Drive stores can
// also upload/create. ScopeDevstorageReadWrite lets a connected Google account
// read/write its Google Cloud Storage objects (GCS bucket mounts are brokered via
// the same Google grant — see the GCS design note on ProviderGCS).
//
// Granting any of these remains additive/opt-in at the consent screen — the user
// must approve them when connecting (or reconnecting) their Google account; an
// account connected before a scope was added simply will not carry it, and the
// mint surfaces a 403 "reconnect to grant" instead of handing back a token that
// would 403 at the provider API.
var googleScopes = []string{
	"openid",
	"email",
	"profile",
	"https://www.googleapis.com/auth/gmail.modify",
	"https://www.googleapis.com/auth/calendar",
	ScopeDrive,
	ScopeDriveReadonly,
	ScopeDriveFile,
	ScopeDevstorageReadWrite,
	// Importer (OAUTH-EVERYWHERE): least-privilege read scopes for Google
	// Contacts and Calendar so the importer can pull contacts/events alongside
	// Drive. drive.readonly above already covers Drive/Docs read.
	ScopeContactsReadonly,
	ScopeCalendarReadonly,
}

// GoogleScopes returns a copy of the requested Google scope set.
func GoogleScopes() []string { return append([]string(nil), googleScopes...) }

// HasScope reports whether the space-separated granted-scope string includes
// the exact scope want.
func HasScope(granted, want string) bool {
	for _, s := range strings.Fields(granted) {
		if s == want {
			return true
		}
	}
	return false
}

// HasDriveReadAccess reports whether the granted scopes authorize READING Drive.
// Either the read-only scope or the broader full-Drive scope satisfies it, so a
// connection made before ScopeDriveReadonly was requested (full Drive only) is
// still recognised as Drive-read-capable.
func HasDriveReadAccess(granted string) bool {
	return HasScope(granted, ScopeDriveReadonly) || HasScope(granted, ScopeDrive)
}

// HasDriveWriteAccess reports whether the granted scopes authorize WRITING to
// Drive (upload/create). Either the per-file scope (ScopeDriveFile, least
// privilege) or the broader full-Drive scope satisfies it. The read-only scope
// does NOT — a read-only mount cannot upload, so the mint returns 403 (reconnect
// to grant) for a write request against a read-only grant.
func HasDriveWriteAccess(granted string) bool {
	return HasScope(granted, ScopeDriveFile) || HasScope(granted, ScopeDrive)
}

// HasGCSAccess reports whether the granted scopes authorize reading/writing the
// connected account's Google Cloud Storage objects.
func HasGCSAccess(granted string) bool {
	return HasScope(granted, ScopeDevstorageReadWrite)
}

// HasContactsReadAccess reports whether the granted scopes authorize READING
// Google Contacts (importer).
func HasContactsReadAccess(granted string) bool {
	return HasScope(granted, ScopeContactsReadonly)
}

// HasCalendarReadAccess reports whether the granted scopes authorize READING
// Google Calendar. The broad "calendar" (read/write) scope also satisfies it, so
// an account connected before contacts/calendar read scopes were added is still
// recognised as calendar-read-capable.
func HasCalendarReadAccess(granted string) bool {
	return HasScope(granted, ScopeCalendarReadonly) ||
		HasScope(granted, "https://www.googleapis.com/auth/calendar")
}

// Token is the provider-agnostic result of an exchange or refresh. RefreshToken
// is empty on a refresh (Google does not re-issue it) — callers keep the old one.
type Token struct {
	AccessToken  string
	RefreshToken string
	Expiry       time.Time
	Scopes       string // space-separated granted scopes
	Email        string // connected account email (exchange only)
	Sub          string // provider subject id (exchange only)
}

// Exchanger abstracts the provider OAuth calls so tests can stub them without
// reaching Google. GoogleExchanger is the production implementation.
type Exchanger interface {
	// AuthCodeURL returns the provider consent URL for the given CSRF state.
	AuthCodeURL(state string) (string, error)
	// Exchange swaps an authorization code for tokens and fetches account info.
	Exchange(ctx context.Context, code string) (*Token, error)
	// Refresh mints a new access token from a stored refresh token.
	Refresh(ctx context.Context, refreshToken string) (*Token, error)
	// Revoke invalidates a refresh token at the provider (best-effort on
	// disconnect). Returns nil if the provider has no revocation endpoint.
	Revoke(ctx context.Context, refreshToken string) error
}

// googleRevokeURL is Google's OAuth token revocation endpoint.
const googleRevokeURL = "https://oauth2.googleapis.com/revoke"

// GoogleExchanger implements Exchanger via golang.org/x/oauth2 + google.Endpoint.
type GoogleExchanger struct{}

// oauthConfig builds the oauth2.Config from environment.
//
//	GOOGLE_OAUTH_CLIENT_ID, GOOGLE_OAUTH_CLIENT_SECRET, OAUTH_REDIRECT_BASE
//
// The redirect URI is OAUTH_REDIRECT_BASE + /api/integrations/google/callback,
// which must be registered on the single fleet-wide Google OAuth client.
func oauthConfig() (*oauth2.Config, error) {
	clientID := os.Getenv("GOOGLE_OAUTH_CLIENT_ID")
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
		ClientSecret: os.Getenv("GOOGLE_OAUTH_CLIENT_SECRET"),
		RedirectURL:  redirectBase + "/api/integrations/google/callback",
		Scopes:       googleScopes,
		Endpoint:     google.Endpoint,
	}, nil
}

// Configured reports whether Google OAuth env is present (for /status).
func Configured() bool { return os.Getenv("GOOGLE_OAUTH_CLIENT_ID") != "" }

func (GoogleExchanger) AuthCodeURL(state string) (string, error) {
	cfg, err := oauthConfig()
	if err != nil {
		return "", err
	}
	// AccessTypeOffline + ApprovalForce are REQUIRED for Google to return a
	// refresh_token (it only issues one on the first consent unless forced).
	return cfg.AuthCodeURL(state,
		oauth2.AccessTypeOffline,
		oauth2.SetAuthURLParam("prompt", "consent"),
	), nil
}

func (GoogleExchanger) Exchange(ctx context.Context, code string) (*Token, error) {
	cfg, err := oauthConfig()
	if err != nil {
		return nil, err
	}
	tok, err := cfg.Exchange(ctx, code)
	if err != nil {
		return nil, fmt.Errorf("integrations: oauth exchange: %w", err)
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
	if info, err := fetchUserinfo(ctx, cfg, tok); err == nil {
		out.Email = info.Email
		out.Sub = info.Sub
	}
	return out, nil
}

func (GoogleExchanger) Refresh(ctx context.Context, refreshToken string) (*Token, error) {
	cfg, err := oauthConfig()
	if err != nil {
		return nil, err
	}
	// A token with only a RefreshToken and a past expiry forces the TokenSource
	// to refresh on first Token() call.
	src := cfg.TokenSource(ctx, &oauth2.Token{
		RefreshToken: refreshToken,
		Expiry:       time.Now().Add(-time.Hour),
	})
	tok, err := src.Token()
	if err != nil {
		return nil, fmt.Errorf("integrations: oauth refresh: %w", err)
	}
	return &Token{
		AccessToken:  tok.AccessToken,
		RefreshToken: tok.RefreshToken, // usually empty for Google; caller keeps old
		Expiry:       tok.Expiry,
		Scopes:       grantedScopes(tok, cfg),
	}, nil
}

// Revoke posts the refresh token to Google's revocation endpoint. A 200 (or
// 400 "already invalid") is treated as success; transient errors are returned
// so the caller can log them, but disconnect still proceeds.
func (GoogleExchanger) Revoke(ctx context.Context, refreshToken string) error {
	if refreshToken == "" {
		return nil
	}
	form := strings.NewReader("token=" + url.QueryEscape(refreshToken))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, googleRevokeURL, form)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := providerHTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("integrations: revoke: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<12))
	// Google returns 200 on success, 400 when the token is already invalid.
	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusBadRequest {
		return nil
	}
	return fmt.Errorf("integrations: revoke status %d", resp.StatusCode)
}

// grantedScopes prefers the provider-returned "scope" extra, falling back to the
// requested config scopes.
func grantedScopes(tok *oauth2.Token, cfg *oauth2.Config) string {
	if s, ok := tok.Extra("scope").(string); ok && s != "" {
		return s
	}
	return strings.Join(cfg.Scopes, " ")
}

type userinfo struct {
	Sub   string `json:"sub"`
	Email string `json:"email"`
}

func fetchUserinfo(ctx context.Context, cfg *oauth2.Config, tok *oauth2.Token) (*userinfo, error) {
	client := cfg.Client(ctx, tok)
	resp, err := client.Get(googleUserinfoURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("integrations: userinfo status %d: %s", resp.StatusCode, body)
	}
	var info userinfo
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&info); err != nil {
		return nil, err
	}
	return &info, nil
}

// ---------------------------------------------------------------------------
// CSRF state — HMAC-signed nonce|timestamp, stored in a short-lived cookie.
// Ported from the removed auth/oauth.go (AUTH-01 era) with an added timestamp
// expiry check so a stale state cannot be replayed past stateMaxAgeSec.
// ---------------------------------------------------------------------------

func stateHMAC(secret []byte, payload string) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(payload))
	return hex.EncodeToString(mac.Sum(nil))
}

// GenerateState creates a signed state and writes it to a short-lived cookie.
// Returns the state value to pass to AuthCodeURL.
func GenerateState(w http.ResponseWriter, secret []byte, now time.Time) (string, error) {
	nonceBytes := make([]byte, stateNonceLen)
	if _, err := rand.Read(nonceBytes); err != nil {
		return "", fmt.Errorf("integrations: generate nonce: %w", err)
	}
	nonce := base64.RawURLEncoding.EncodeToString(nonceBytes)
	ts := strconv.FormatInt(now.Unix(), 10)
	payload := nonce + "|" + ts
	state := payload + "|" + stateHMAC(secret, payload)

	http.SetCookie(w, &http.Cookie{
		Name:     stateCookie,
		Value:    base64.RawURLEncoding.EncodeToString([]byte(state)),
		Path:     "/api/integrations",
		MaxAge:   stateMaxAgeSec,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})
	return state, nil
}

// VerifyState validates the returned state against the cookie, the HMAC, and
// the timestamp window. Returns ErrStateMismatch on any discrepancy.
func VerifyState(r *http.Request, returnedState string, secret []byte, now time.Time) error {
	cookie, err := r.Cookie(stateCookie)
	if err != nil {
		return ErrStateMismatch
	}
	cookieBytes, err := base64.RawURLEncoding.DecodeString(cookie.Value)
	if err != nil {
		return ErrStateMismatch
	}
	// The state echoed back by the provider must equal the cookie value.
	if !hmac.Equal(cookieBytes, []byte(returnedState)) {
		return ErrStateMismatch
	}
	parts := strings.SplitN(returnedState, "|", 3)
	if len(parts) != 3 {
		return ErrStateMismatch
	}
	payload := parts[0] + "|" + parts[1]
	if !hmac.Equal([]byte(parts[2]), []byte(stateHMAC(secret, payload))) {
		return ErrStateMismatch
	}
	// Timestamp window: reject stale/forged-future states.
	ts, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return ErrStateMismatch
	}
	age := now.Unix() - ts
	if age < -stateMaxAgeSec || age > stateMaxAgeSec {
		return ErrStateMismatch
	}
	return nil
}

// ClearStateCookie removes the state cookie after use.
func ClearStateCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     stateCookie,
		Path:     "/api/integrations",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})
}
