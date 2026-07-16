package cproutes

// routes_oauthlogin.go — "Sign in with Google / Microsoft" (Vulos as OAuth CLIENT).
//
// Account model (LOCKED, 2026-07): email+password is the CENTRE of every account
// and is MANDATORY. Social login is a CONVENIENCE login on top. This file wires the
// inbound OAuth-client flow and the two rules that keep it safe:
//
//   MANDATORY PASSWORD. A social sign-up creates a users row with NO usable password
//   (the LockedPasswordHash sentinel) and is routed to /onboarding/set-password. The
//   account is not finalised (no recovery codes minted, treated as password_setup_
//   required by GET /api/auth/me) until POST /api/auth/password/set-initial sets a
//   real Vulos password. Fail-closed: no password-less account is ever "usable".
//
//   SAFE LINKING (no takeover). A social login whose VERIFIED email matches a
//   pre-existing account is NEVER silently linked. The callback issues NO session and
//   redirects to /onboarding/link-account with a signed link-token; the user must
//   prove the existing password via POST /api/auth/oauth/link/confirm before the link
//   is created. Only a provider-asserted verified email is eligible to auto-create or
//   link.
//
// CSRF: the authorize→callback round-trip is bound by PKCE (S256) + an unguessable
// `state`, both carried in a short-lived, HMAC-signed, HttpOnly cookie. The callback
// rejects any request whose `state` param does not match the signed cookie.

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/vul-os/vulos-management/pkg/auth"
	"github.com/vul-os/vulos-management/pkg/env"
	"github.com/vul-os/vulos-management/pkg/httpx"
	"github.com/vul-os/vulos-management/pkg/oauthclient"
)

// oauthLoginSecureCookie controls the Secure flag on the transient OAuth cookie.
// Set once at startup (main.go) to match the session-cookie policy; tests set false.
var oauthLoginSecureCookie = true

// SetOAuthLoginSecureCookie is called from main.go with the same value passed to
// auth.SetSecureCookies so the transient OAuth cookie matches the session policy.
func SetOAuthLoginSecureCookie(secure bool) { oauthLoginSecureCookie = secure }

const (
	oauthFlowCookie = "vc_oauth"       // transient state+verifier cookie
	oauthFlowTTL    = 10 * time.Minute // authorize→callback window
	oauthLinkTTL    = 10 * time.Minute // link-token validity
	setPasswordPath = "/onboarding/set-password"
	linkAccountPath = "/onboarding/link-account"
)

// oauthFlowState is the payload of the signed transient cookie.
type oauthFlowState struct {
	Provider string `json:"p"`
	State    string `json:"s"`
	Verifier string `json:"v"`
	Next     string `json:"n"`
	Exp      int64  `json:"e"`
}

// oauthLinkToken is the payload of the signed link-token handed to the frontend when
// a social login collides with a pre-existing account. It authorises linking
// (provider,subject) to UserID ONLY once the existing password is proven.
type oauthLinkToken struct {
	Provider string `json:"p"`
	Subject  string `json:"sub"`
	Email    string `json:"em"`
	UserID   string `json:"uid"`
	Exp      int64  `json:"e"`
}

// signBlob returns base64url(payload) + "." + base64url(HMAC-SHA256(secret,payload)).
func signBlob(secret, payload []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write(payload)
	sig := mac.Sum(nil)
	return base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(sig)
}

// verifyBlob checks the HMAC and returns the payload bytes.
func verifyBlob(secret []byte, token string) ([]byte, bool) {
	dot := strings.IndexByte(token, '.')
	if dot < 0 {
		return nil, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(token[:dot])
	if err != nil {
		return nil, false
	}
	gotSig, err := base64.RawURLEncoding.DecodeString(token[dot+1:])
	if err != nil {
		return nil, false
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write(payload)
	want := mac.Sum(nil)
	if subtle.ConstantTimeCompare(gotSig, want) != 1 {
		return nil, false
	}
	return payload, true
}

func randToken(nBytes int) (string, error) {
	b := make([]byte, nBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// oauthRedirectBase is the absolute origin the provider redirects back to. Founder
// registers "<base>/api/auth/oauth/<provider>/callback" in the provider console.
// OAUTH_REDIRECT_BASE overrides the default (the deployment site origin).
func oauthRedirectBase() string {
	if v := strings.TrimSpace(os.Getenv("OAUTH_REDIRECT_BASE")); v != "" {
		return strings.TrimRight(v, "/")
	}
	return env.SiteURL()
}

func oauthCallbackURI(provider string) string {
	return oauthRedirectBase() + "/api/auth/oauth/" + provider + "/callback"
}

// safeNextPath validates the ?return= param: same-origin absolute path only.
func safeNextPath(raw string) string {
	if raw == "" || !strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "//") {
		return ""
	}
	return raw
}

// RegisterOAuthLoginRoutes wires the social-login (OAuth client) endpoints.
func RegisterOAuthLoginRoutes(mux *http.ServeMux, st *auth.Store, reg *oauthclient.Registry) {
	secret := st.Secret()
	rl := newAuthRateLimiter(3, 10)

	// GET /api/auth/oauth/providers — discovery for the login buttons. Returns only
	// providers that are configured (client id+secret present). Never errors: an
	// empty list means "no social providers configured" and the UI renders no row.
	mux.HandleFunc("GET /api/auth/oauth/providers", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, map[string]any{"providers": reg.Configured()})
	})

	// GET /api/auth/oauth/{provider}/start?return=/path
	// Begins the OAuth authorize redirect with PKCE + state.
	mux.HandleFunc("GET /api/auth/oauth/{provider}/start", func(w http.ResponseWriter, r *http.Request) {
		ip, _, _ := net.SplitHostPort(r.RemoteAddr)
		if !rl.Allow(ip) {
			httpx.Err(w, http.StatusTooManyRequests, "rate limit exceeded")
			return
		}
		providerID := strings.ToLower(r.PathValue("provider"))
		p, ok := reg.Get(providerID)
		if !ok {
			// Unconfigured provider degrades cleanly — never a crash/500.
			httpx.JSONStatus(w, http.StatusNotFound, map[string]string{"error": "provider_not_configured"})
			return
		}

		state, err := randToken(32)
		if err != nil {
			httpx.Err(w, http.StatusInternalServerError, "internal error")
			return
		}
		verifier, err := randToken(48) // 64 base64url chars — valid PKCE verifier
		if err != nil {
			httpx.Err(w, http.StatusInternalServerError, "internal error")
			return
		}
		sum := sha256.Sum256([]byte(verifier))
		challenge := base64.RawURLEncoding.EncodeToString(sum[:])

		flow := oauthFlowState{
			Provider: providerID,
			State:    state,
			Verifier: verifier,
			Next:     safeNextPath(r.URL.Query().Get("return")),
			Exp:      time.Now().Add(oauthFlowTTL).Unix(),
		}
		payload, _ := json.Marshal(flow)
		http.SetCookie(w, &http.Cookie{
			Name:     oauthFlowCookie,
			Value:    signBlob(secret, payload),
			Path:     "/api/auth/oauth",
			MaxAge:   int(oauthFlowTTL.Seconds()),
			HttpOnly: true,
			Secure:   oauthLoginSecureCookie,
			SameSite: http.SameSiteLaxMode,
		})

		http.Redirect(w, r, p.AuthCodeURL(oauthCallbackURI(providerID), state, challenge), http.StatusFound)
	})

	// GET /api/auth/oauth/{provider}/callback?code=&state=
	mux.HandleFunc("GET /api/auth/oauth/{provider}/callback", func(w http.ResponseWriter, r *http.Request) {
		providerID := strings.ToLower(r.PathValue("provider"))
		p, ok := reg.Get(providerID)
		if !ok {
			redirectLoginError(w, r, "provider_not_configured")
			return
		}

		// Load + verify the transient flow cookie.
		c, err := r.Cookie(oauthFlowCookie)
		if err != nil {
			redirectLoginError(w, r, "oauth_state_missing")
			return
		}
		// Clear it regardless of outcome (single-use).
		clearOAuthFlowCookie(w)

		payload, ok := verifyBlob(secret, c.Value)
		if !ok {
			redirectLoginError(w, r, "oauth_state_invalid")
			return
		}
		var flow oauthFlowState
		if err := json.Unmarshal(payload, &flow); err != nil {
			redirectLoginError(w, r, "oauth_state_invalid")
			return
		}
		if flow.Provider != providerID || time.Now().Unix() > flow.Exp {
			redirectLoginError(w, r, "oauth_state_expired")
			return
		}
		// CSRF: the state param MUST match the signed-cookie state (constant time).
		gotState := r.URL.Query().Get("state")
		if gotState == "" || subtle.ConstantTimeCompare([]byte(gotState), []byte(flow.State)) != 1 {
			redirectLoginError(w, r, "oauth_state_mismatch")
			return
		}
		// Provider-side error (user denied, etc.).
		if e := r.URL.Query().Get("error"); e != "" {
			redirectLoginError(w, r, "oauth_denied")
			return
		}
		code := r.URL.Query().Get("code")
		if code == "" {
			redirectLoginError(w, r, "oauth_no_code")
			return
		}

		ident, err := reg.Exchange(r.Context(), p, oauthCallbackURI(providerID), code, flow.Verifier)
		if err != nil {
			log.Printf("[oauth-login] exchange failed (%s): %v", providerID, err)
			redirectLoginError(w, r, "oauth_exchange_failed")
			return
		}
		if ident.Email == "" {
			redirectLoginError(w, r, "oauth_no_email")
			return
		}

		ip, _, _ := net.SplitHostPort(r.RemoteAddr)
		ua := r.UserAgent()
		resolveSocialLogin(w, r, st, secret, ident, flow.Next, ip, ua)
	})

	// POST /api/auth/oauth/link/confirm  {link_token, password}
	// Safe-linking proof: verifies the existing account password, then links the
	// social identity and signs in (respecting the account's 2FA).
	mux.HandleFunc("POST /api/auth/oauth/link/confirm", func(w http.ResponseWriter, r *http.Request) {
		ip, _, _ := net.SplitHostPort(r.RemoteAddr)
		if !rl.Allow(ip) {
			httpx.Err(w, http.StatusTooManyRequests, "rate limit exceeded")
			return
		}
		var body struct {
			LinkToken string `json:"link_token"`
			Password  string `json:"password"`
		}
		if !httpx.DecodeJSON(w, r, &body) {
			return
		}
		payload, ok := verifyBlob(secret, body.LinkToken)
		if !ok {
			httpx.Err(w, http.StatusBadRequest, "invalid link token")
			return
		}
		var tok oauthLinkToken
		if err := json.Unmarshal(payload, &tok); err != nil || tok.UserID == "" {
			httpx.Err(w, http.StatusBadRequest, "invalid link token")
			return
		}
		if time.Now().Unix() > tok.Exp {
			httpx.Err(w, http.StatusBadRequest, "link token expired")
			return
		}

		// Prove ownership of the EXISTING account via its password.
		var hash string
		if err := st.QueryPasswordHash(r.Context(), tok.UserID, &hash); err != nil {
			httpx.Err(w, http.StatusUnauthorized, "invalid credentials")
			return
		}
		if err := auth.VerifyPassword(hash, body.Password); err != nil {
			httpx.Err(w, http.StatusUnauthorized, "invalid credentials")
			return
		}
		if suspended, _ := st.IsSuspendedByID(r.Context(), tok.UserID); suspended {
			httpx.Err(w, http.StatusForbidden, "account suspended")
			return
		}

		// Password proven → create the link. Only a verified provider email got
		// this far (callback gate), so record it verified.
		if err := st.LinkOAuthIdentity(r.Context(), tok.Provider, tok.Subject, tok.UserID, tok.Email, true); err != nil {
			if err == auth.ErrOAuthIdentityLinked {
				httpx.Err(w, http.StatusConflict, "this social account is already linked elsewhere")
				return
			}
			log.Printf("[oauth-login] link confirm: %v", err)
			httpx.Err(w, http.StatusInternalServerError, "internal error")
			return
		}

		// Sign in, honouring the account's 2FA.
		res, err := st.IssuePostAuthSession(r.Context(), tok.UserID, ip, r.UserAgent())
		if err != nil {
			httpx.Err(w, http.StatusInternalServerError, "internal error")
			return
		}
		writeLoginResult(w, res)
	})

	// POST /api/auth/password/set-initial  {password}
	// The mandatory-password gate for social sign-ups. Requires an authenticated
	// session for an account that currently has NO password (the sentinel). Sets the
	// first Vulos password and finalises the account (mints recovery codes).
	mux.HandleFunc("POST /api/auth/password/set-initial", func(w http.ResponseWriter, r *http.Request) {
		// Setup-tolerant: THIS is the endpoint that lifts the mandatory-password gate,
		// so it must admit the password-less session RequireSession refuses. SetInitial
		// Password still fails closed (ErrPasswordAlreadySet) for any account that
		// already holds a real password, so this cannot overwrite one.
		u := st.RequireSessionAllowingSetup(r.Context(), w, r)
		if u == nil {
			return
		}
		var body struct {
			Password string `json:"password"`
		}
		if !httpx.DecodeJSON(w, r, &body) {
			return
		}
		// HIBP breach check (fail-open, same as signup).
		if auth.MaybeCheckBreached(r.Context(), hibpClient, body.Password, hibpBaseURL) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnprocessableEntity)
			fmt.Fprint(w, `{"error":"password_breached","message":"password found in breach corpus; choose a different password"}`)
			return
		}
		if err := st.SetInitialPassword(r.Context(), u.ID, body.Password); err != nil {
			switch err {
			case auth.ErrPasswordAlreadySet:
				httpx.Err(w, http.StatusConflict, "account already has a password")
			case auth.ErrNotFound:
				httpx.Err(w, http.StatusUnauthorized, "not authenticated")
			default:
				if isValidationErr(err) {
					httpx.Err(w, http.StatusBadRequest, err.Error())
				} else {
					log.Printf("[oauth-login] set-initial password: %v", err)
					httpx.Err(w, http.StatusInternalServerError, "internal error")
				}
			}
			return
		}
		// Account is now usable — finalise: mint one-time recovery codes (parity with
		// native signup). Non-fatal on error.
		codes, codesErr := st.MintRecoveryCodes(r.Context(), u.ID)
		if codesErr != nil {
			log.Printf("[oauth-login] set-initial mint recovery codes: %v", codesErr)
		}
		httpx.JSON(w, map[string]any{"ok": true, "recovery_codes": codes})
	})
}

// resolveSocialLogin maps a verified external identity to a Vulos account and drives
// the correct outcome (sign-in / set-password / safe-link).
func resolveSocialLogin(w http.ResponseWriter, r *http.Request, st *auth.Store, secret []byte, ident *oauthclient.Identity, next, ip, ua string) {
	ctx := r.Context()

	// 1) Already linked → sign in (or finish set-password if the account never set one).
	if userID, err := st.FindOAuthIdentity(ctx, ident.Provider, ident.Subject); err == nil {
		if hasPw, _ := st.HasPassword(ctx, userID); !hasPw {
			// Linked but password-less → force set-password.
			token, serr := st.IssueOAuthSignupSession(ctx, userID, ip, ua)
			if serr != nil {
				redirectLoginError(w, r, "internal_error")
				return
			}
			auth.SetSessionCookie(w, token)
			http.Redirect(w, r, setPasswordPath, http.StatusFound)
			return
		}
		res, serr := st.IssuePostAuthSession(ctx, userID, ip, ua)
		if serr != nil {
			redirectLoginError(w, r, "internal_error")
			return
		}
		redirectLoginResult(w, r, res, next)
		return
	}

	// 2) Not linked. Does a Vulos account already exist for this email?
	existingID, err := st.UserIDByEmail(ctx, ident.Email)
	if err == nil {
		// COLLISION. Never silently take over a pre-existing account. Only offer the
		// link path when the provider VERIFIED the email; otherwise refuse.
		if !ident.EmailVerified {
			redirectLoginError(w, r, "email_unverified")
			return
		}
		tok := oauthLinkToken{
			Provider: ident.Provider,
			Subject:  ident.Subject,
			Email:    ident.Email,
			UserID:   existingID,
			Exp:      time.Now().Add(oauthLinkTTL).Unix(),
		}
		payload, _ := json.Marshal(tok)
		linkToken := signBlob(secret, payload)
		q := url.Values{}
		q.Set("provider", ident.Provider)
		q.Set("email", ident.Email)
		q.Set("token", linkToken)
		http.Redirect(w, r, linkAccountPath+"?"+q.Encode(), http.StatusFound)
		return
	}
	if err != auth.ErrNotFound {
		redirectLoginError(w, r, "internal_error")
		return
	}

	// 3) Brand-new social sign-up. Create a password-less account, link, and force
	// the set-password step. Mandatory-password rule: the account is NOT usable
	// until POST /api/auth/password/set-initial runs.
	newID, err := st.CreateOAuthUser(ctx, ident.Email, ident.EmailVerified)
	if err != nil {
		if err == auth.ErrEmailTaken {
			// Raced with another signup on the same email — fall back to safe-link.
			redirectLoginError(w, r, "email_taken")
			return
		}
		redirectLoginError(w, r, "internal_error")
		return
	}
	if err := st.LinkOAuthIdentity(ctx, ident.Provider, ident.Subject, newID, ident.Email, ident.EmailVerified); err != nil {
		redirectLoginError(w, r, "internal_error")
		return
	}
	token, err := st.IssueOAuthSignupSession(ctx, newID, ip, ua)
	if err != nil {
		redirectLoginError(w, r, "internal_error")
		return
	}
	auth.SetSessionCookie(w, token)
	http.Redirect(w, r, setPasswordPath, http.StatusFound)
}

// redirectLoginResult issues cookies for a LoginResult from a browser (redirect)
// social login and navigates to the right SPA destination.
func redirectLoginResult(w http.ResponseWriter, r *http.Request, res *auth.LoginResult, next string) {
	dest := "/login"
	if n := safeNextPath(next); n != "" {
		dest = n
	}
	switch {
	case res.EmailVerificationRequired:
		http.Redirect(w, r, "/login?error=email_verification_required", http.StatusFound)
		return
	case res.TOTPRequired || res.PasskeyAs2FA:
		// Partial session cookie already carries the token; drive the 2FA step.
		http.SetCookie(w, &http.Cookie{
			Name:     auth.SessionCookieName,
			Value:    res.Token,
			Path:     "/",
			MaxAge:   300,
			HttpOnly: true,
			Secure:   oauthLoginSecureCookie,
			SameSite: http.SameSiteLaxMode,
		})
		if res.PasskeyAs2FA {
			http.Redirect(w, r, "/login?step=passkey", http.StatusFound)
			return
		}
		http.Redirect(w, r, "/2fa", http.StatusFound)
		return
	default:
		auth.SetSessionCookie(w, res.Token)
		http.Redirect(w, r, dest, http.StatusFound)
	}
}

// writeLoginResult writes a JSON LoginResult for the link-confirm (fetch) path.
func writeLoginResult(w http.ResponseWriter, res *auth.LoginResult) {
	switch {
	case res.EmailVerificationRequired:
		httpx.JSON(w, map[string]string{"step": "email_verification_required"})
	case res.PasskeyAs2FA:
		http.SetCookie(w, &http.Cookie{
			Name: auth.SessionCookieName, Value: res.Token, Path: "/", MaxAge: 300,
			HttpOnly: true, Secure: oauthLoginSecureCookie, SameSite: http.SameSiteLaxMode,
		})
		httpx.JSON(w, map[string]string{"step": "passkey_2fa_required"})
	case res.TOTPRequired:
		http.SetCookie(w, &http.Cookie{
			Name: auth.SessionCookieName, Value: res.Token, Path: "/", MaxAge: 300,
			HttpOnly: true, Secure: oauthLoginSecureCookie, SameSite: http.SameSiteLaxMode,
		})
		httpx.JSON(w, map[string]string{"step": "totp_required"})
	default:
		auth.SetSessionCookie(w, res.Token)
		httpx.JSON(w, map[string]string{"step": "ok"})
	}
}

func clearOAuthFlowCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: oauthFlowCookie, Value: "", Path: "/api/auth/oauth", MaxAge: -1,
		HttpOnly: true, Secure: oauthLoginSecureCookie, SameSite: http.SameSiteLaxMode,
	})
}

// redirectLoginError sends the browser back to /login with a short error code the
// SPA can render. It never leaks internal detail.
func redirectLoginError(w http.ResponseWriter, r *http.Request, code string) {
	http.Redirect(w, r, "/login?error="+url.QueryEscape(code), http.StatusFound)
}
