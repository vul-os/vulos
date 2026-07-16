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
	"context"
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
	oauthFlowCookie       = "vc_oauth"       // transient state+verifier cookie
	oauthFlowTTL          = 10 * time.Minute // authorize→callback window
	oauthLinkTTL          = 10 * time.Minute // link-token validity
	setPasswordPath       = "/onboarding/set-password"
	linkAccountPath       = "/onboarding/link-account"
	oauthEmailPath        = "/onboarding/oauth-email" // mandatory-email entry page
	connectedAccountsPath = "/account/social"         // connect/disconnect surface
)

// oauthFlowMode distinguishes the two authorize→callback flows carried by the
// transient cookie: "" / "login" (sign in or sign up) vs "connect" (link an
// additional provider to the ALREADY-authenticated account in UserID).
const oauthFlowModeConnect = "connect"

// oauthFlowState is the payload of the signed transient cookie.
type oauthFlowState struct {
	Provider string `json:"p"`
	State    string `json:"s"`
	Verifier string `json:"v"`
	Next     string `json:"n"`
	Mode     string `json:"m,omitempty"`   // "" | "connect"
	UserID   string `json:"uid,omitempty"` // connect mode: the account to link onto
	Exp      int64  `json:"e"`
}

// oauthEmailToken is the signed token handed to the mandatory-email entry page
// when a provider returns NO email. It authorises finishing sign-in/up for
// (provider, subject) ONCE the user supplies an email (which is unverified).
type oauthEmailToken struct {
	Provider string `json:"p"`
	Subject  string `json:"sub"`
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

		// CONNECT mode (multi-provider linking): when ?mode=connect the caller must
		// already hold a live session — the callback binds the new provider to THAT
		// account rather than creating/logging in. A connect request with no session
		// is refused (you cannot connect a provider to "nobody").
		mode := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("mode")))
		var connectUID string
		if mode == oauthFlowModeConnect {
			if tok := auth.SessionFromRequest(r); tok != "" {
				if u, uerr := st.LookupSession(r.Context(), tok); uerr == nil && u != nil {
					connectUID = u.ID
				}
			}
			if connectUID == "" {
				httpx.Err(w, http.StatusUnauthorized, "sign in before connecting a social account")
				return
			}
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
			Mode:     mode,
			UserID:   connectUID,
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

		// CONNECT mode: bind this provider to the already-authenticated account and
		// return to the connected-accounts surface. No email is required here — the
		// account already exists.
		if flow.Mode == oauthFlowModeConnect && flow.UserID != "" {
			handleConnectLink(w, r, st, flow.UserID, ident)
			return
		}

		// MANDATORY-EMAIL rule: if the provider returned no email, block completion
		// and force the user to type one before any account is created or linked.
		// Redirect to the email-entry page with a signed token that authorises
		// finishing THIS (provider, subject).
		if ident.Email == "" {
			et := oauthEmailToken{
				Provider: ident.Provider,
				Subject:  ident.Subject,
				Next:     flow.Next,
				Exp:      time.Now().Add(oauthLinkTTL).Unix(),
			}
			payload, _ := json.Marshal(et)
			q := url.Values{}
			q.Set("provider", ident.Provider)
			q.Set("token", signBlob(secret, payload))
			http.Redirect(w, r, oauthEmailPath+"?"+q.Encode(), http.StatusFound)
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

	// POST /api/auth/oauth/complete-email  {email_token, email}
	// The mandatory-email completion for a provider that returned NO email. The
	// user-typed email is UNVERIFIED, so it can create its OWN account or, on a
	// collision, be routed to the safe-link flow (which still requires the existing
	// account's password) — it can never silently take over an account.
	mux.HandleFunc("POST /api/auth/oauth/complete-email", func(w http.ResponseWriter, r *http.Request) {
		ip, _, _ := net.SplitHostPort(r.RemoteAddr)
		if !rl.Allow(ip) {
			httpx.Err(w, http.StatusTooManyRequests, "rate limit exceeded")
			return
		}
		var body struct {
			EmailToken string `json:"email_token"`
			Email      string `json:"email"`
		}
		if !httpx.DecodeJSON(w, r, &body) {
			return
		}
		payload, ok := verifyBlob(secret, body.EmailToken)
		if !ok {
			httpx.Err(w, http.StatusBadRequest, "invalid email token")
			return
		}
		var et oauthEmailToken
		if err := json.Unmarshal(payload, &et); err != nil || et.Provider == "" || et.Subject == "" {
			httpx.Err(w, http.StatusBadRequest, "invalid email token")
			return
		}
		if time.Now().Unix() > et.Exp {
			httpx.Err(w, http.StatusBadRequest, "email token expired")
			return
		}
		email := strings.ToLower(strings.TrimSpace(body.Email))
		if !looksLikeEmail(email) {
			httpx.Err(w, http.StatusBadRequest, "a valid email address is required")
			return
		}
		ident := &oauthclient.Identity{
			Provider:      et.Provider,
			Subject:       et.Subject,
			Email:         email,
			EmailVerified: false, // user-typed → never provider-verified
		}
		ua := r.UserAgent()
		jsonSocialOutcome(w, r, st, secret, ident, ip, ua)
	})

	// GET /api/auth/oauth/identities — the caller's linked social providers plus
	// the providers still available to connect. Drives the account-settings
	// connect/disconnect surface (multi-provider linking).
	mux.HandleFunc("GET /api/auth/oauth/identities", func(w http.ResponseWriter, r *http.Request) {
		u := st.RequireSession(r.Context(), w, r)
		if u == nil {
			return
		}
		list, err := st.ListOAuthIdentities(r.Context(), u.ID)
		if err != nil {
			log.Printf("[oauth-login] list identities: %v", err)
			httpx.Err(w, http.StatusInternalServerError, "internal error")
			return
		}
		if list == nil {
			list = []auth.LinkedIdentity{}
		}
		httpx.JSON(w, map[string]any{"identities": list, "available": reg.Configured()})
	})

	// DELETE /api/auth/oauth/identities/{provider} — disconnect one provider from
	// the caller's account. Always safe: email+password is mandatory, so removing a
	// social link never strands the account without a sign-in method.
	mux.HandleFunc("DELETE /api/auth/oauth/identities/{provider}", func(w http.ResponseWriter, r *http.Request) {
		u := st.RequireSession(r.Context(), w, r)
		if u == nil {
			return
		}
		provider := strings.ToLower(r.PathValue("provider"))
		if err := st.UnlinkOAuthIdentity(r.Context(), u.ID, provider); err != nil {
			if err == auth.ErrNotFound {
				httpx.Err(w, http.StatusNotFound, "no such linked account")
				return
			}
			log.Printf("[oauth-login] unlink identity: %v", err)
			httpx.Err(w, http.StatusInternalServerError, "internal error")
			return
		}
		httpx.JSON(w, map[string]any{"ok": true})
	})
}

// looksLikeEmail is a minimal syntactic gate for a user-typed email in the
// mandatory-email completion flow. Authoritative validation happens in the auth
// store (CreateOAuthUser); this only rejects the obviously-malformed early.
func looksLikeEmail(s string) bool {
	at := strings.IndexByte(s, '@')
	if at <= 0 || at == len(s)-1 || strings.ContainsAny(s, " \t\r\n") {
		return false
	}
	return strings.IndexByte(s[at+1:], '.') >= 0
}

// Social-outcome kinds — the pure result of mapping a verified external identity
// to a Vulos account, turned into a response by the adapters below.
const (
	outcomeSignIn      = "signin"      // account exists + has password → issue session
	outcomeSetPassword = "setpassword" // password-less account (new or linked) → force set-password
	outcomeCollision   = "collision"   // email matches a pre-existing account → prove password to link
)

// socialOutcome is the resolution of a social identity with NO HTTP side effects
// (the adapters below emit the browser-redirect or JSON response). computeSocialOutcome
// may still perform DB writes (create/link a brand-new sign-up).
type socialOutcome struct {
	kind    string
	login   *auth.LoginResult // outcomeSignIn
	session string            // outcomeSetPassword: full session cookie value
	userID  string            // outcomeCollision: the pre-existing account id
}

// computeSocialOutcome resolves ident to an account. ident.Email MUST be non-empty
// (callers enforce the mandatory-email rule). It never writes to the ResponseWriter.
func computeSocialOutcome(ctx context.Context, st *auth.Store, ident *oauthclient.Identity, ip, ua string) (socialOutcome, error) {
	// 1) Already linked → sign in (or finish set-password if never set one).
	if userID, err := st.FindOAuthIdentity(ctx, ident.Provider, ident.Subject); err == nil {
		if hasPw, _ := st.HasPassword(ctx, userID); !hasPw {
			tok, serr := st.IssueOAuthSignupSession(ctx, userID, ip, ua)
			if serr != nil {
				return socialOutcome{}, serr
			}
			return socialOutcome{kind: outcomeSetPassword, session: tok}, nil
		}
		res, serr := st.IssuePostAuthSession(ctx, userID, ip, ua)
		if serr != nil {
			return socialOutcome{}, serr
		}
		return socialOutcome{kind: outcomeSignIn, login: res}, nil
	}

	// 2) Not linked. A pre-existing account for this email → collision (link path).
	existingID, err := st.UserIDByEmail(ctx, ident.Email)
	if err == nil {
		return socialOutcome{kind: outcomeCollision, userID: existingID}, nil
	}
	if err != auth.ErrNotFound {
		return socialOutcome{}, err
	}

	// 3) Brand-new social sign-up. Create a password-less account, link, and force
	// the set-password step (mandatory-password rule: not usable until then).
	newID, err := st.CreateOAuthUser(ctx, ident.Email, ident.EmailVerified)
	if err != nil {
		return socialOutcome{}, err
	}
	if err := st.LinkOAuthIdentity(ctx, ident.Provider, ident.Subject, newID, ident.Email, ident.EmailVerified); err != nil {
		return socialOutcome{}, err
	}
	tok, err := st.IssueOAuthSignupSession(ctx, newID, ip, ua)
	if err != nil {
		return socialOutcome{}, err
	}
	return socialOutcome{kind: outcomeSetPassword, session: tok}, nil
}

// buildLinkToken signs the collision link-token that /api/auth/oauth/link/confirm
// consumes after the existing account's password is proven.
func buildLinkToken(secret []byte, ident *oauthclient.Identity, userID string) string {
	tok := oauthLinkToken{
		Provider: ident.Provider,
		Subject:  ident.Subject,
		Email:    ident.Email,
		UserID:   userID,
		Exp:      time.Now().Add(oauthLinkTTL).Unix(),
	}
	payload, _ := json.Marshal(tok)
	return signBlob(secret, payload)
}

// resolveSocialLogin is the BROWSER (redirect) adapter for a provider-asserted
// identity from the callback. On a collision it only offers the link path when the
// provider VERIFIED the email (the typed-email JSON path relaxes this — see
// jsonSocialOutcome — because there the password proof is the security gate).
func resolveSocialLogin(w http.ResponseWriter, r *http.Request, st *auth.Store, secret []byte, ident *oauthclient.Identity, next, ip, ua string) {
	out, err := computeSocialOutcome(r.Context(), st, ident, ip, ua)
	if err != nil {
		if err == auth.ErrEmailTaken {
			redirectLoginError(w, r, "email_taken")
			return
		}
		redirectLoginError(w, r, "internal_error")
		return
	}
	switch out.kind {
	case outcomeSignIn:
		redirectLoginResult(w, r, out.login, next)
	case outcomeSetPassword:
		auth.SetSessionCookie(w, out.session)
		http.Redirect(w, r, setPasswordPath, http.StatusFound)
	case outcomeCollision:
		if !ident.EmailVerified {
			redirectLoginError(w, r, "email_unverified")
			return
		}
		q := url.Values{}
		q.Set("provider", ident.Provider)
		q.Set("email", ident.Email)
		q.Set("token", buildLinkToken(secret, ident, out.userID))
		http.Redirect(w, r, linkAccountPath+"?"+q.Encode(), http.StatusFound)
	}
}

// jsonSocialOutcome is the JSON (fetch) adapter used by the mandatory-email
// completion endpoint. The typed email is unverified, but the collision path still
// requires proving the existing account's password, so it is offered regardless.
func jsonSocialOutcome(w http.ResponseWriter, r *http.Request, st *auth.Store, secret []byte, ident *oauthclient.Identity, ip, ua string) {
	out, err := computeSocialOutcome(r.Context(), st, ident, ip, ua)
	if err != nil {
		if err == auth.ErrEmailTaken {
			httpx.Err(w, http.StatusConflict, "an account with that email already exists")
			return
		}
		if isValidationErr(err) {
			httpx.Err(w, http.StatusBadRequest, "a valid email address is required")
			return
		}
		log.Printf("[oauth-login] complete-email resolve: %v", err)
		httpx.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	switch out.kind {
	case outcomeSignIn:
		writeLoginResult(w, out.login)
	case outcomeSetPassword:
		auth.SetSessionCookie(w, out.session)
		httpx.JSON(w, map[string]string{"step": "set_password"})
	case outcomeCollision:
		httpx.JSON(w, map[string]string{
			"step":       "link",
			"provider":   ident.Provider,
			"email":      ident.Email,
			"link_token": buildLinkToken(secret, ident, out.userID),
		})
	}
}

// handleConnectLink binds a provider identity to the already-authenticated account
// (multi-provider linking). It re-verifies the live session still matches the
// account captured at authorize-time (defence in depth) before linking, and always
// returns to the connected-accounts surface with a result code.
func handleConnectLink(w http.ResponseWriter, r *http.Request, st *auth.Store, userID string, ident *oauthclient.Identity) {
	tok := auth.SessionFromRequest(r)
	if tok == "" {
		redirectConnect(w, r, "", "not_authenticated")
		return
	}
	u, err := st.LookupSession(r.Context(), tok)
	if err != nil || u == nil || u.ID != userID {
		redirectConnect(w, r, "", "not_authenticated")
		return
	}
	if err := st.LinkOAuthIdentity(r.Context(), ident.Provider, ident.Subject, userID, ident.Email, ident.EmailVerified); err != nil {
		if err == auth.ErrOAuthIdentityLinked {
			redirectConnect(w, r, ident.Provider, "already_linked")
			return
		}
		log.Printf("[oauth-login] connect link: %v", err)
		redirectConnect(w, r, ident.Provider, "connect_failed")
		return
	}
	redirectConnect(w, r, ident.Provider, "")
}

// redirectConnect sends the browser back to the connected-accounts surface with a
// success (?connected=<provider>) or error (?error=<code>) marker.
func redirectConnect(w http.ResponseWriter, r *http.Request, provider, errCode string) {
	q := url.Values{}
	if errCode != "" {
		q.Set("error", errCode)
		if provider != "" {
			q.Set("provider", provider)
		}
	} else {
		q.Set("connected", provider)
	}
	http.Redirect(w, r, connectedAccountsPath+"?"+q.Encode(), http.StatusFound)
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
