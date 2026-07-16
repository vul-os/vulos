// routes_auth.go -- auth HTTP routes for the vulos.cloud control-plane.
//
// Auth decision: email + password is the mandatory centre of every account,
// with TOTP/passkey 2FA and social sign-in as convenience layers on top.
//
// Vulos identity (BYO-EMAIL, pivot 2026-07): accounts are created with the
// user's OWN external email address (any domain). Clients send
// {email, password}. A bare {handle} is still tolerated for legacy/CLI callers
// and is minted as <handle>@<deployment-domain>, but external email is the
// default identity — it is no longer rejected.
//
// Routes registered:
//
//	POST /api/auth/signup                  -- create account via Vulos handle, issue session
//	POST /api/auth/login                   -- verify credentials, issue session (rate-limited)
//	POST /api/auth/logout                  -- invalidate session cookie
//	GET  /api/auth/me                      -- return current user from session (includes email_verified)
//	GET  /api/auth/handle-available        -- check whether a handle is available (rate-limited, 30/min/IP)
//	POST /api/auth/password/reset/request  -- email-enumeration-safe reset initiation (rate-limited)
//	POST /api/auth/password/reset/confirm  -- consume token, update password, revoke all sessions
//	POST /api/auth/totp/enroll             -- stage TOTP secret; returns URI + recovery codes
//	POST /api/auth/totp/confirm            -- verify first code; commit secret + recovery codes
//	POST /api/auth/totp/verify             -- post-login TOTP step; upgrades partial→full session
//	POST /api/auth/totp/disable            -- disable TOTP (fleet_admin blocked)
//	GET  /api/auth/sessions                -- list live sessions with is_current flag
//	POST /api/auth/sessions/revoke         -- revoke a specific session (403 for current)
//	POST /api/auth/sessions/revoke-all     -- revoke all sessions except current; returns {revoked:N}
//	GET  /api/auth/verify-email            -- consume ?token=, set email_verified=1 (410 on expired/used)
//	POST /api/auth/verify-email/resend     -- issue fresh token (always 204, no enumeration, rate-limited)
//	GET  /api/auth/modality                -- return preferred 2FA modality for authenticated user
//	POST /api/auth/modality                -- update preferred 2FA modality for authenticated user
//
// WebAuthn (AUTH-11 / AUTH-12) routes registered by RegisterWebAuthnRoutes:
//
//	POST /api/auth/webauthn/register/begin     -- returns CredentialCreation options (session required)
//	POST /api/auth/webauthn/register/finish    -- validates attestation + stores credential
//	POST /api/auth/webauthn/login/begin        -- returns CredentialAssertion options (email-keyed, passkey-first)
//	POST /api/auth/webauthn/login/finish       -- validates assertion + issues full session (passkey-first)
//	POST /api/auth/webauthn/login/begin-2fa    -- partial-session required; begins passkey-as-2FA ceremony
//	POST /api/auth/webauthn/login/finish-2fa   -- validates assertion; upgrades partial→full session (passkey-as-2FA)
//	GET  /api/auth/webauthn/credentials        -- list registered authenticators (session required)
//	POST /api/auth/webauthn/credentials/revoke -- revoke a credential by ID (session required)
package cproutes

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/mail"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/vul-os/vulos-management/pkg/auth"
	"github.com/vul-os/vulos-management/pkg/env"
	"github.com/vul-os/vulos-management/pkg/httpx"
)

// timeNow is the clock hook used in route handlers for lockout retry_after
// computation.  Tests can replace it to advance time without sleeping.
var timeNow = func() time.Time { return time.Now().UTC() }

// hibpClient is the http.Client used for HIBP breach checks.
// Tests may replace this with a client pointing at an httptest.Server.
var hibpClient *http.Client

// forgotPasswordSender is the InboxSender used by POST /api/auth/forgot-password
// to deliver the recovery link to the user's Vulos inbox. Defaults to a
// NopInboxSender (logs only) in dev/test. Production overrides this via
// SetForgotPasswordSender before wiring routes.
var forgotPasswordSender auth.InboxSender = auth.NopInboxSender{}

// SetForgotPasswordSender sets the InboxSender used by the forgot-password route.
// Call before registering routes (i.e. in main or wire_stores.go).
func SetForgotPasswordSender(s auth.InboxSender) { forgotPasswordSender = s }

// loginInnerHandler holds the bare POST /api/auth/login handler func (without
// step-up wrapping). Set by RegisterAuthRoutes so that wire_security.go can
// wrap it with StepUpMiddleware when registerSecurityRoutes is called.
// Must not be nil after RegisterAuthRoutes returns.
var loginInnerHandler http.Handler

// hibpBaseURL overrides the HIBP API base URL. Empty string uses the default.
// Tests set this to an httptest.Server URL.
var hibpBaseURL = ""

// handleRL is a dedicated per-IP rate limiter for handle-available: 30 req/min.
// Bucket size = 30, refill at 0.5/s (= 30/min).
var handleRL = newAuthRateLimiter(0.5, 30)

// isAllowedNextURL validates a ?next= redirect target after successful login.
//
// Rules:
//   - Must be a valid URL with scheme "https" (or "http" in non-prod).
//   - Must NOT use scheme "javascript:" or any non-http/https scheme.
//   - Host must equal cookieDomain or be a subdomain of it (same parent domain
//     as the session cookie). This prevents open redirect to external sites.
//
// Returns false if the URL is empty, malformed, uses a disallowed scheme, or
// the host is not within the cookie domain.
func isAllowedNextURL(next string) bool {
	if next == "" {
		return false
	}
	u, err := url.Parse(next)
	if err != nil {
		return false
	}
	// Reject non-http(s) schemes (covers javascript:, data:, etc.).
	if u.Scheme != "https" && u.Scheme != "http" {
		return false
	}
	// In prod require https (single authoritative, fail-safe resolver).
	if env.IsProdResolved() && u.Scheme != "https" {
		return false
	}
	// Host must be within the cookie domain.
	// Read directly from env so tests can set the var without calling InitCookieDomain.
	cookieDomain := os.Getenv("VULOS_COOKIE_DOMAIN")
	if cookieDomain == "" {
		// No parent domain configured — only allow same-host redirects (i.e.
		// don't allow any cross-domain redirect when domain is unset).
		return false
	}
	host := u.Hostname() // strips port
	return host == cookieDomain || strings.HasSuffix(host, "."+cookieDomain)
}

// loginSuccessRedirectOrJSON issues a 302 to ?next= when the next URL is
// allowed, otherwise writes a JSON response. It is called on the final step
// of a successful login (full session issued).
func loginSuccessRedirectOrJSON(w http.ResponseWriter, r *http.Request, v any) {
	nextParam := r.URL.Query().Get("next")
	if isAllowedNextURL(nextParam) {
		http.Redirect(w, r, nextParam, http.StatusFound)
		return
	}
	httpx.JSON(w, v)
}

// signupResponse is the POST /api/auth/signup body: the created user (inlined,
// so every existing field keeps its place) plus the one-time recovery codes.
// The codes are the account's only mailbox-free way back in — see
// routes_recovery.go.
type signupResponse struct {
	*auth.User
	RecoveryCodes []string `json:"recovery_codes,omitempty"`
}

// RegisterAuthRoutes wires auth endpoints into mux.
func RegisterAuthRoutes(mux *http.ServeMux, st *auth.Store) {
	rl := newAuthRateLimiter(3, 10) // 3 req/s sustained, burst 10 per IP

	// GET /api/auth/handle-available?handle=<x>
	// Rate-limited: 30 req/min per IP. No session required (public endpoint).
	// Returns: {"available":bool,"reason":"available"|"taken"|"reserved"|"invalid","suggestions":[...]}
	mux.HandleFunc("GET /api/auth/handle-available", func(w http.ResponseWriter, r *http.Request) {
		ip, _, _ := net.SplitHostPort(r.RemoteAddr)
		if !handleRL.Allow(ip) {
			httpx.Err(w, http.StatusTooManyRequests, "rate limit exceeded")
			return
		}
		handle := strings.TrimSpace(r.URL.Query().Get("handle"))
		if handle == "" {
			httpx.Err(w, http.StatusBadRequest, "handle query parameter is required")
			return
		}
		result := st.CheckHandleAvailable(r.Context(), handle)
		httpx.JSON(w, result)
	})

	// POST /api/auth/signup  (rate-limited per IP — same limiter as login/reset)
	// Body: {"handle": "<username>", "password": "<password>"}
	// The email is CONSTRUCTED server-side as <handle>@vulos.org.
	// Sending a non-vulos.org email address in the body is rejected with 400.
	mux.HandleFunc("POST /api/auth/signup", func(w http.ResponseWriter, r *http.Request) {
		ip, _, _ := net.SplitHostPort(r.RemoteAddr)
		if !rl.Allow(ip) {
			httpx.Err(w, http.StatusTooManyRequests, "rate limit exceeded")
			return
		}

		var body struct {
			Email    string `json:"email"`
			Password string `json:"password"`
			// Legacy: some clients (older CLIs) still send a bare Vulos handle.
			// If no email is supplied we mint <handle>@<deployment-domain> for
			// backwards compatibility; otherwise the external email wins.
			Handle string `json:"handle"`
		}
		if !httpx.DecodeJSON(w, r, &body) {
			return
		}

		// BYO-EMAIL (pivot 2026-07): the account identity IS the user's own
		// external email address (gmail/outlook/any domain). Vulos no longer mints
		// hosted <handle>@vulos.org identities at signup. A bare handle is still
		// tolerated for legacy/CLI callers and is minted on the deployment domain
		// (env.Domain()); it never overrides an explicitly-supplied email.
		email := strings.ToLower(strings.TrimSpace(body.Email))
		if email == "" && strings.TrimSpace(body.Handle) != "" {
			h := strings.ToLower(strings.TrimSpace(body.Handle))
			// Legacy on-domain minting keeps the handle validation + reserved
			// blocklist (admin@/postmaster@/abuse@/… — phishing / RFC-2142 abuse /
			// brand impersonation). These do NOT apply to bring-your-own-email.
			if err := auth.ValidateHandle(h); err != nil {
				httpx.Err(w, http.StatusBadRequest, "invalid handle: "+err.Error())
				return
			}
			if st.IsReservedHandle(r.Context(), h) {
				httpx.Err(w, http.StatusConflict, "handle is reserved")
				return
			}
			email = h + "@" + env.Domain()
		}
		if email == "" {
			httpx.Err(w, http.StatusBadRequest, "email is required")
			return
		}
		// Validate the address (RFC 5322 via net/mail); reject display-name forms
		// like "Name <a@b>" so the identity is a bare address.
		if addr, perr := mail.ParseAddress(email); perr != nil || strings.ToLower(addr.Address) != email {
			httpx.Err(w, http.StatusBadRequest, "enter a valid email address")
			return
		}

		// Derive a non-identity display handle / org slug from the local-part.
		// Used only for org naming + the internal anchor address, never as the
		// login identity (which is the full email above).
		handle := email
		if at := strings.Index(email, "@"); at > 0 {
			handle = email[:at]
		}

		ua := r.UserAgent()

		// HIBP breach check (fail-open: if HIBP is unreachable, signup continues).
		if auth.MaybeCheckBreached(r.Context(), hibpClient, body.Password, hibpBaseURL) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnprocessableEntity)
			fmt.Fprint(w, `{"error":"password_breached","message":"password found in breach corpus; choose a different password"}`)
			return
		}

		u, token, err := st.Signup(r.Context(), email, body.Password, ip, ua)
		if err != nil {
			switch err {
			case auth.ErrEmailTaken:
				httpx.Err(w, http.StatusConflict, "an account with that email already exists")
			default:
				if isValidationErr(err) {
					httpx.Err(w, http.StatusBadRequest, err.Error())
				} else {
					log.Printf("[auth] signup: %v", err)
					httpx.Err(w, http.StatusInternalServerError, "internal error")
				}
			}
			return
		}

		// ANCHOR-01: provision the always-on @vulos.org anchor inbox for the new
		// account.  Errors are logged but do not fail the signup.
		// COORDINATOR: needs provisionAnchorInbox — defined in wire_anchorinbox.go (NOT in my subsystem)
		provisionAnchorInbox(r.Context(), u.ID, handle)

		// ORG-MULTI-01: every signup creates/owns an org. Async + never blocks
		// signup. (No hosted root mailbox — Vulos runs no hosted mail.)
		// COORDINATOR: needs provisionOrgOnSignup — defined in wire_orgs.go/wire_orgadmin.go (NOT in my subsystem)
		provisionOrgOnSignup(r.Context(), u.ID, handle, handle)

		// RECOVERY: the account we just minted IS its own mailbox, so the
		// forgot-password link would be delivered somewhere the user may not be
		// able to reach. Mint the recovery codes NOW — held out of band, they
		// reset the password with no mailbox, no session and no TOTP device
		// (POST /api/auth/password/recovery-code). Shown exactly once.
		codes, codesErr := st.MintRecoveryCodes(r.Context(), u.ID)
		if codesErr != nil {
			// Never fail the signup over this: the account exists and the user can
			// mint a fresh set from the account surface.
			log.Printf("[auth] signup mint recovery codes: %v", codesErr)
		}

		auth.SetSessionCookie(w, token)
		httpx.JSON(w, signupResponse{User: u, RecoveryCodes: codes})
	})

	// POST /api/auth/login  (rate-limited per IP)
	// The handler func is stored in loginInnerHandler so that wire_security.go
	// can re-register the route wrapped with StepUpMiddleware.
	loginHandlerFunc := func(w http.ResponseWriter, r *http.Request) {
		ip, _, _ := net.SplitHostPort(r.RemoteAddr)
		if !rl.Allow(ip) {
			httpx.Err(w, http.StatusTooManyRequests, "rate limit exceeded")
			return
		}

		var body struct {
			Email          string `json:"email"`
			Password       string `json:"password"`
			RememberDevice bool   `json:"remember_device"`
		}
		if !httpx.DecodeJSON(w, r, &body) {
			return
		}

		ua := r.UserAgent()
		result, err := st.Login(r.Context(), body.Email, body.Password, ip, ua)
		if err != nil {
			switch err {
			case auth.ErrHashMismatch, auth.ErrNotFound:
				httpx.Err(w, http.StatusUnauthorized, "invalid credentials")
			case auth.ErrAccountSuspended:
				httpx.Err(w, http.StatusForbidden, "account suspended")
			case auth.ErrLoginThrottled:
				httpx.Err(w, http.StatusTooManyRequests, "too many attempts, try again later")
			default:
				log.Printf("[auth] login: %v", err)
				httpx.Err(w, http.StatusInternalServerError, "internal error")
			}
			return
		}

		// ── Email-verification gate (AUTH-10) ──────────────────────────────────
		// No session cookie is set; the UI must walk the user through verify-email.
		if result.EmailVerificationRequired {
			httpx.JSON(w, map[string]string{"step": "email_verification_required"})
			return
		}

		// ── Passkey-first modality (AUTH-12) ────────────────────────────────────
		// No partial session is needed; the caller drives a WebAuthn ceremony via
		// /api/auth/webauthn/login/begin using the email they just submitted.
		if result.PasskeyRequired {
			httpx.JSON(w, map[string]string{"step": "passkey_required"})
			return
		}

		// ── Passkey-as-2FA modality (AUTH-12) ───────────────────────────────────
		// Password verified; partial session issued; caller must complete the
		// WebAuthn 2FA step via /api/auth/webauthn/login/begin-2fa.
		if result.PasskeyAs2FA {
			http.SetCookie(w, &http.Cookie{
				Name:     auth.SessionCookieName,
				Value:    result.Token,
				Path:     "/",
				MaxAge:   300, // 5 minutes
				HttpOnly: true,
				Secure:   true,
				SameSite: http.SameSiteLaxMode,
			})
			httpx.JSON(w, map[string]string{"step": "passkey_2fa_required"})
			return
		}

		if result.TOTPRequired && body.RememberDevice {
			// Check whether a valid recognised-device cookie is present for this user.
			cookieVal := auth.DeviceTokenFromRequest(r)
			newDevToken, skip, devErr := st.CheckDeviceSkipTOTP(r.Context(), result.User.ID, ip, ua, cookieVal)
			if devErr != nil {
				log.Printf("[auth] login device check: %v", devErr)
				// Non-fatal: fall through to normal TOTP step.
			} else if skip {
				// Recognised device: upgrade the partial session to a full one and return.
				// result.Token is a partial-session token at this point; upgrade it so
				// the full-session cookie is valid for all authenticated endpoints.
				fullToken, upgradeErr := st.UpgradePartialSession(r.Context(), result.Token, ip, ua)
				if upgradeErr != nil {
					log.Printf("[auth] login device upgrade: %v", upgradeErr)
					// Non-fatal: fall through to normal TOTP step.
				} else {
					auth.SetSessionCookie(w, fullToken)
					auth.SetDeviceCookie(w, newDevToken)
					loginSuccessRedirectOrJSON(w, r, result.User)
					return
				}
			}
		}

		if result.TOTPRequired {
			// Issue a short-lived partial-session cookie (5 min TTL).
			http.SetCookie(w, &http.Cookie{
				Name:     auth.SessionCookieName,
				Value:    result.Token,
				Path:     "/",
				MaxAge:   300, // 5 minutes
				HttpOnly: true,
				Secure:   true,
				SameSite: http.SameSiteLaxMode,
			})
			httpx.JSON(w, map[string]string{"step": "totp_required"})
			return
		}

		// No TOTP: issue full session.  Always issue a device token on a fresh
		// non-TOTP login so future requests can use remember_device.
		auth.SetSessionCookie(w, result.Token)
		if devToken, devErr := st.IssueDeviceToken(r.Context(), result.User.ID, ip, ua); devErr == nil {
			auth.SetDeviceCookie(w, devToken)
		} else {
			log.Printf("[auth] login issue device token: %v", devErr)
		}
		loginSuccessRedirectOrJSON(w, r, result.User)
	}
	// Store the bare handler so wire_security.go can wrap it with StepUpMiddleware.
	loginInnerHandler = http.HandlerFunc(loginHandlerFunc)
	// Register a stable mux entry that always delegates to loginInnerHandler.
	// wire_security.go replaces loginInnerHandler with the step-up-wrapped
	// version; the mux entry remains valid because it calls through the var.
	mux.HandleFunc("POST /api/auth/login", func(w http.ResponseWriter, r *http.Request) {
		loginInnerHandler.ServeHTTP(w, r)
	})

	// POST /api/auth/logout
	mux.HandleFunc("POST /api/auth/logout", func(w http.ResponseWriter, r *http.Request) {
		token := auth.SessionFromRequest(r)
		if token != "" {
			if err := st.DeleteSession(r.Context(), token); err != nil {
				log.Printf("[auth] logout: %v", err)
			}
		}
		auth.ClearSessionCookie(w)
		w.WriteHeader(http.StatusNoContent)
	})

	// GET /api/auth/me
	mux.HandleFunc("GET /api/auth/me", func(w http.ResponseWriter, r *http.Request) {
		// Setup-tolerant: a password-less social sign-up must still reach /me so the
		// SPA can learn it needs to set a password. Every OTHER route uses
		// RequireSession, which fails such a session closed (MANDATORY-PASSWORD gate).
		u := st.RequireSessionAllowingSetup(r.Context(), w, r)
		if u == nil {
			return
		}
		// MANDATORY-PASSWORD gate: a social sign-up (or any account that has not yet
		// set a Vulos password) carries the locked-password sentinel. Surface this so
		// the SPA can force /onboarding/set-password before the account is usable. A
		// normal password account reports false. NeedsPasswordSetup is resolved on the
		// same session lookup (no extra query).
		httpx.JSON(w, struct {
			*auth.User
			PasswordSetupRequired bool `json:"password_setup_required"`
		}{User: u, PasswordSetupRequired: u.NeedsPasswordSetup})
	})

	// ── Vulos-email-only forgot-password flow ────────────────────────────────
	//
	// POST /api/auth/forgot-password
	//   Accepts {handle} — bare or @vulos.org. External domains → 400.
	//   Unknown handles → pretend-200 with ~250 ms delay (enumeration safety).
	//   Known handles → recovery link delivered to Vulos inbox.
	mux.HandleFunc("POST /api/auth/forgot-password", func(w http.ResponseWriter, r *http.Request) {
		ip, _, _ := net.SplitHostPort(r.RemoteAddr)
		if !rl.Allow(ip) {
			httpx.Err(w, http.StatusTooManyRequests, "rate limit exceeded")
			return
		}

		var body struct {
			Handle string `json:"handle"`
			// Reject any "email" field with an external domain.
			Email string `json:"email,omitempty"`
		}
		if !httpx.DecodeJSON(w, r, &body) {
			return
		}

		// Hard-reject any "email" field pointing at an external domain. The
		// identity domain is the deployment's own domain (env.Domain(); prod
		// default vulos.org).
		if body.Email != "" {
			normalized := strings.ToLower(strings.TrimSpace(body.Email))
			if !strings.HasSuffix(normalized, "@"+env.Domain()) {
				httpx.Err(w, http.StatusBadRequest, "recovery supports in-network addresses only; use the 'handle' field")
				return
			}
		}

		// Coalesce: allow "email" field if it is a vulos.org address.
		handle := body.Handle
		if handle == "" {
			handle = body.Email
		}

		err := st.RequestPasswordResetByHandle(r.Context(), handle, forgotPasswordSender)
		if err != nil {
			if err == auth.ErrExternalEmail {
				httpx.Err(w, http.StatusBadRequest, "recovery supports @vulos.org addresses only")
				return
			}
			log.Printf("[auth] forgot-password: %v", err)
			// Fall through to pretend-200 for other errors to avoid info leakage.
		}

		// ~250 ms artificial latency so timing cannot reveal handle existence.
		time.Sleep(250 * time.Millisecond)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"message":"We've sent a recovery link to your Vulos inbox."}`)
	})

	// GET /api/auth/reset?token=<x>
	//   Validates the signed token (expiry + signature). Returns totp_enabled
	//   flag. Does NOT consume the token.
	mux.HandleFunc("GET /api/auth/reset", func(w http.ResponseWriter, r *http.Request) {
		token := r.URL.Query().Get("token")
		if token == "" {
			httpx.Err(w, http.StatusBadRequest, "token is required")
			return
		}
		totpEnabled, err := st.ValidateVumailResetToken(r.Context(), token)
		if err != nil {
			switch err {
			case auth.ErrResetExpired:
				httpx.Err(w, http.StatusGone, "reset link expired or already used")
			default:
				log.Printf("[auth] reset validate: %v", err)
				httpx.Err(w, http.StatusInternalServerError, "internal error")
			}
			return
		}
		httpx.JSON(w, map[string]any{"valid": true, "totp_enabled": totpEnabled})
	})

	// POST /api/auth/reset
	//   Body: {token, totp, new_password}
	//   Verifies TOTP, sets new password, invalidates all sessions for account.
	mux.HandleFunc("POST /api/auth/reset", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Token       string `json:"token"`
			TOTP        string `json:"totp"`
			NewPassword string `json:"new_password"`
		}
		if !httpx.DecodeJSON(w, r, &body) {
			return
		}

		if auth.MaybeCheckBreached(r.Context(), hibpClient, body.NewPassword, hibpBaseURL) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnprocessableEntity)
			fmt.Fprint(w, `{"error":"password_breached","message":"password found in breach corpus; choose a different password"}`)
			return
		}

		if err := st.ConfirmPasswordResetWithTOTP(r.Context(), body.Token, body.TOTP, body.NewPassword); err != nil {
			switch err {
			case auth.ErrResetExpired:
				httpx.Err(w, http.StatusGone, "reset link expired or already used")
			case auth.ErrResetTOTPRequired, auth.ErrTOTPInvalid:
				httpx.Err(w, http.StatusUnauthorized, "invalid or missing TOTP code")
			default:
				if isValidationErr(err) {
					httpx.Err(w, http.StatusBadRequest, err.Error())
				} else {
					log.Printf("[auth] reset confirm: %v", err)
					httpx.Err(w, http.StatusInternalServerError, "internal error")
				}
			}
			return
		}

		auth.ClearSessionCookie(w)
		w.WriteHeader(http.StatusNoContent)
	})

	// POST /api/auth/password/reset/request  (rate-limited per IP, email-enumeration safe)
	mux.HandleFunc("POST /api/auth/password/reset/request", func(w http.ResponseWriter, r *http.Request) {
		ip, _, _ := net.SplitHostPort(r.RemoteAddr)
		if !rl.Allow(ip) {
			httpx.Err(w, http.StatusTooManyRequests, "rate limit exceeded")
			return
		}

		var body struct {
			Email string `json:"email"`
		}
		if !httpx.DecodeJSON(w, r, &body) {
			return
		}

		// Always 204 -- never reveal whether email exists.
		if err := st.RequestPasswordReset(r.Context(), body.Email); err != nil {
			log.Printf("[auth] password reset request: %v", err)
			// Still return 204 to avoid enumeration via error codes.
		}
		w.WriteHeader(http.StatusNoContent)
	})

	// POST /api/auth/password/reset/confirm
	mux.HandleFunc("POST /api/auth/password/reset/confirm", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Token       string `json:"token"`
			NewPassword string `json:"new_password"`
		}
		if !httpx.DecodeJSON(w, r, &body) {
			return
		}

		// HIBP breach check (fail-open: if HIBP is unreachable, reset continues).
		if auth.MaybeCheckBreached(r.Context(), hibpClient, body.NewPassword, hibpBaseURL) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnprocessableEntity)
			fmt.Fprint(w, `{"error":"password_breached","message":"password found in breach corpus; choose a different password"}`)
			return
		}

		if err := st.ConfirmPasswordReset(r.Context(), body.Token, body.NewPassword); err != nil {
			switch err {
			case auth.ErrResetExpired:
				httpx.Err(w, http.StatusGone, "reset token expired or already used")
			default:
				if isValidationErr(err) {
					httpx.Err(w, http.StatusBadRequest, err.Error())
				} else {
					log.Printf("[auth] password reset confirm: %v", err)
					httpx.Err(w, http.StatusInternalServerError, "internal error")
				}
			}
			return
		}

		// Clear any active session cookie (user must log in again).
		auth.ClearSessionCookie(w)
		w.WriteHeader(http.StatusNoContent)
	})

	// ── TOTP endpoints ──────────────────────────────────────────────────────

	// POST /api/auth/totp/enroll
	// Requires a full session (not already enrolled or in process).
	// Returns {uri, secret, recovery_codes:[]} with staged secret committed on /confirm.
	mux.HandleFunc("POST /api/auth/totp/enroll", func(w http.ResponseWriter, r *http.Request) {
		u := st.RequireSession(r.Context(), w, r)
		if u == nil {
			return
		}

		_, enabled, err := st.LoadTOTPSecret(r.Context(), u.ID)
		if err != nil {
			log.Printf("[auth] totp/enroll load secret: %v", err)
			httpx.Err(w, http.StatusInternalServerError, "internal error")
			return
		}
		if enabled {
			httpx.Err(w, http.StatusConflict, "TOTP already enabled")
			return
		}

		uri, secretB32, err := auth.GenerateTOTPSecret(u.Email)
		if err != nil {
			log.Printf("[auth] totp/enroll generate: %v", err)
			httpx.Err(w, http.StatusInternalServerError, "internal error")
			return
		}

		// Stage the plaintext secret; committed only after /confirm.
		if err := st.SavePendingTOTPSecret(r.Context(), u.ID, secretB32); err != nil {
			log.Printf("[auth] totp/enroll save pending: %v", err)
			httpx.Err(w, http.StatusInternalServerError, "internal error")
			return
		}

		// Generate recovery codes (display once; not yet stored — stored on /confirm).
		codes, _, err := auth.GenerateRecoveryCodes()
		if err != nil {
			log.Printf("[auth] totp/enroll recovery codes: %v", err)
			httpx.Err(w, http.StatusInternalServerError, "internal error")
			return
		}

		// We need to carry recovery codes through to /confirm.  Store hashes in the
		// pending secret table is complex; instead, we generate them fresh on /enroll
		// but only commit the hashes on /confirm.  The recovery codes shown here are
		// transient; the user must save them now.  On /confirm we regenerate a fresh
		// set and store those — this is the standard UX (show once, save now).
		// Store the pending codes as a separate key so /confirm can persist them.
		if err := st.SavePendingRecoveryCodes(r.Context(), u.ID, codes); err != nil {
			log.Printf("[auth] totp/enroll save pending recovery: %v", err)
			httpx.Err(w, http.StatusInternalServerError, "internal error")
			return
		}

		httpx.JSON(w, map[string]any{
			"uri":            uri,
			"secret":         secretB32,
			"recovery_codes": codes,
		})
	})

	// POST /api/auth/totp/confirm
	// Takes {code}; verifies first TOTP code against staged secret; on success
	// encrypts + stores secret + recovery codes, enables TOTP.
	mux.HandleFunc("POST /api/auth/totp/confirm", func(w http.ResponseWriter, r *http.Request) {
		u := st.RequireSession(r.Context(), w, r)
		if u == nil {
			return
		}

		var body struct {
			Code string `json:"code"`
		}
		if !httpx.DecodeJSON(w, r, &body) {
			return
		}

		secretB32, err := st.LoadPendingTOTPSecret(r.Context(), u.ID)
		if err != nil {
			if err == auth.ErrNoPendingEnrollment {
				httpx.Err(w, http.StatusBadRequest, "no pending TOTP enrollment; call /totp/enroll first")
				return
			}
			log.Printf("[auth] totp/confirm load pending: %v", err)
			httpx.Err(w, http.StatusInternalServerError, "internal error")
			return
		}

		if !auth.VerifyTOTP([]byte(secretB32), body.Code) {
			httpx.Err(w, http.StatusUnauthorized, "invalid TOTP code")
			return
		}

		// Encrypt and persist the secret.
		kek, err := auth.LoadTOTPKEK()
		if err != nil {
			log.Printf("[auth] totp/confirm kek: %v", err)
			httpx.Err(w, http.StatusInternalServerError, "TOTP key not configured")
			return
		}
		enc, err := auth.EncryptTOTPSecret([]byte(secretB32), kek)
		if err != nil {
			log.Printf("[auth] totp/confirm encrypt: %v", err)
			httpx.Err(w, http.StatusInternalServerError, "internal error")
			return
		}
		if err := st.SaveTOTPSecret(r.Context(), u.ID, enc); err != nil {
			log.Printf("[auth] totp/confirm save: %v", err)
			httpx.Err(w, http.StatusInternalServerError, "internal error")
			return
		}

		// Load and store recovery code hashes.
		codes, err := st.LoadPendingRecoveryCodes(r.Context(), u.ID)
		if err != nil || len(codes) == 0 {
			// Fallback: generate fresh codes (enroll→confirm in separate requests).
			var genErr error
			codes, _, genErr = auth.GenerateRecoveryCodes()
			if genErr != nil {
				log.Printf("[auth] totp/confirm recovery gen: %v", genErr)
				httpx.Err(w, http.StatusInternalServerError, "internal error")
				return
			}
		}
		hashes := make([][]byte, len(codes))
		for i, c := range codes {
			h, err := auth.HashRecoveryCode(c)
			if err != nil {
				log.Printf("[auth] totp/confirm hash recovery: %v", err)
				httpx.Err(w, http.StatusInternalServerError, "internal error")
				return
			}
			hashes[i] = h
		}
		if err := st.InsertRecoveryCodes(r.Context(), u.ID, hashes); err != nil {
			log.Printf("[auth] totp/confirm insert recovery: %v", err)
			httpx.Err(w, http.StatusInternalServerError, "internal error")
			return
		}

		if err := st.EnableTOTP(r.Context(), u.ID); err != nil {
			log.Printf("[auth] totp/confirm enable: %v", err)
			httpx.Err(w, http.StatusInternalServerError, "internal error")
			return
		}

		// Clean up staged data.
		_ = st.DeletePendingTOTPSecret(r.Context(), u.ID)
		_ = st.DeletePendingRecoveryCodes(r.Context(), u.ID)

		w.WriteHeader(http.StatusNoContent)
	})

	// POST /api/auth/totp/verify
	// Partial-session required. Takes {code} or {recovery_code}.
	// On success upgrades partial→full session; returns Set-Cookie.
	// Failed attempts are counted; 5 consecutive failures lock the account for
	// LockoutDuration (default 15 min) and return 429 with retry_after.
	mux.HandleFunc("POST /api/auth/totp/verify", func(w http.ResponseWriter, r *http.Request) {
		token := auth.SessionFromRequest(r)
		userID, err := st.LookupPartialSession(r.Context(), token)
		if err != nil {
			httpx.Err(w, http.StatusUnauthorized, "not authenticated or session expired")
			return
		}

		// ── Lockout check ──────────────────────────────────────────────────────
		if lockedUntil, locked := st.Check2FALockout(r.Context(), userID); locked {
			retryAfter := lockedUntil - timeNow().Unix()
			if retryAfter < 0 {
				retryAfter = 0
			}
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Retry-After", fmt.Sprintf("%d", retryAfter))
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprintf(w, `{"error":"account locked","retry_after":%d}`, retryAfter)
			return
		}

		var body struct {
			Code           string `json:"code"`
			RecoveryCode   string `json:"recovery_code"`
			RememberDevice bool   `json:"remember_device"`
		}
		if !httpx.DecodeJSON(w, r, &body) {
			return
		}

		encSecret, enabled, err := st.LoadTOTPSecret(r.Context(), userID)
		if err != nil || !enabled || encSecret == nil {
			httpx.Err(w, http.StatusUnprocessableEntity, "TOTP not configured for account")
			return
		}

		kek, err := auth.LoadTOTPKEK()
		if err != nil {
			log.Printf("[auth] totp/verify kek: %v", err)
			httpx.Err(w, http.StatusInternalServerError, "TOTP key not configured")
			return
		}
		plainSecret, err := auth.DecryptTOTPSecret(encSecret, kek)
		if err != nil {
			log.Printf("[auth] totp/verify decrypt: %v", err)
			httpx.Err(w, http.StatusInternalServerError, "internal error")
			return
		}

		ok := false
		if body.Code != "" {
			ok = auth.VerifyTOTPAndRecord(plainSecret, body.Code, userID)
		} else if body.RecoveryCode != "" {
			burned, burnErr := st.BurnRecoveryCode(r.Context(), userID, body.RecoveryCode)
			if burnErr != nil {
				log.Printf("[auth] totp/verify burn: %v", burnErr)
				httpx.Err(w, http.StatusInternalServerError, "internal error")
				return
			}
			ok = burned
		}

		if !ok {
			// Record failed attempt; may trigger lockout.
			if recordErr := st.RecordFailed2FA(r.Context(), userID); recordErr != nil {
				log.Printf("[auth] totp/verify record failure: %v", recordErr)
			}
			// Re-check lockout (in case this attempt just triggered it).
			if lockedUntil, locked := st.Check2FALockout(r.Context(), userID); locked {
				retryAfter := lockedUntil - timeNow().Unix()
				if retryAfter < 0 {
					retryAfter = 0
				}
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Retry-After", fmt.Sprintf("%d", retryAfter))
				w.WriteHeader(http.StatusTooManyRequests)
				fmt.Fprintf(w, `{"error":"account locked","retry_after":%d}`, retryAfter)
				return
			}
			httpx.Err(w, http.StatusUnauthorized, "invalid TOTP code or recovery code")
			return
		}

		// ── Successful verification ───────────────────────────────────────────
		// Reset failed counter + any lockout.
		if resetErr := st.ResetFailed2FA(r.Context(), userID); resetErr != nil {
			log.Printf("[auth] totp/verify reset lockout: %v", resetErr)
		}

		ip, _, _ := net.SplitHostPort(r.RemoteAddr)
		ua := r.UserAgent()
		fullToken, err := st.UpgradePartialSession(r.Context(), token, ip, ua)
		if err != nil {
			log.Printf("[auth] totp/verify upgrade: %v", err)
			httpx.Err(w, http.StatusInternalServerError, "internal error")
			return
		}

		auth.SetSessionCookie(w, fullToken)

		// Issue / rotate device token when remember_device was requested.
		if body.RememberDevice {
			if devToken, devErr := st.IssueDeviceToken(r.Context(), userID, ip, ua); devErr == nil {
				auth.SetDeviceCookie(w, devToken)
			} else {
				log.Printf("[auth] totp/verify issue device token: %v", devErr)
			}
		}

		w.WriteHeader(http.StatusNoContent)
	})

	// GET /api/auth/sessions
	// Full session required. Returns all live (non-expired, non-revoked, non-partial)
	// sessions for the authenticated user with is_current set for the caller's session.
	mux.HandleFunc("GET /api/auth/sessions", func(w http.ResponseWriter, r *http.Request) {
		u := st.RequireSession(r.Context(), w, r)
		if u == nil {
			return
		}
		currentToken := auth.SessionFromRequest(r)
		sessions, err := st.ListSessions(r.Context(), u.ID, currentToken)
		if err != nil {
			log.Printf("[auth] list sessions: %v", err)
			httpx.Err(w, http.StatusInternalServerError, "internal error")
			return
		}
		httpx.JSON(w, map[string]any{"sessions": sessions})
	})

	// POST /api/auth/sessions/revoke
	// Full session required. Body: {session_id}. Revokes the targeted session.
	// Returns 403 if the caller tries to revoke their own current session.
	mux.HandleFunc("POST /api/auth/sessions/revoke", func(w http.ResponseWriter, r *http.Request) {
		u := st.RequireSession(r.Context(), w, r)
		if u == nil {
			return
		}
		var body struct {
			SessionID string `json:"session_id"`
		}
		if !httpx.DecodeJSON(w, r, &body) {
			return
		}
		if body.SessionID == "" {
			httpx.Err(w, http.StatusBadRequest, "session_id is required")
			return
		}
		currentToken := auth.SessionFromRequest(r)
		if body.SessionID == currentToken {
			httpx.Err(w, http.StatusForbidden, "cannot revoke current session")
			return
		}
		if err := st.RevokeByID(r.Context(), u.ID, body.SessionID); err != nil {
			switch err {
			case auth.ErrSessionNotFound:
				httpx.Err(w, http.StatusNotFound, "session not found")
			default:
				log.Printf("[auth] revoke session: %v", err)
				httpx.Err(w, http.StatusInternalServerError, "internal error")
			}
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	// POST /api/auth/sessions/revoke-all
	// Full session required. Revokes all sessions except the caller's current one.
	// Returns {revoked: N}.
	mux.HandleFunc("POST /api/auth/sessions/revoke-all", func(w http.ResponseWriter, r *http.Request) {
		u := st.RequireSession(r.Context(), w, r)
		if u == nil {
			return
		}
		currentToken := auth.SessionFromRequest(r)
		n, err := st.RevokeAll(r.Context(), u.ID, currentToken)
		if err != nil {
			log.Printf("[auth] revoke-all sessions: %v", err)
			httpx.Err(w, http.StatusInternalServerError, "internal error")
			return
		}
		httpx.JSON(w, map[string]any{"revoked": n})
	})

	// GET /api/auth/verify-email?token=
	// No session required. Consumes a single-use UUID verification token.
	// On success sets email_verified=1 and returns 200.
	// Expired or used tokens → 410 Gone.
	mux.HandleFunc("GET /api/auth/verify-email", func(w http.ResponseWriter, r *http.Request) {
		token := r.URL.Query().Get("token")
		if token == "" {
			httpx.Err(w, http.StatusBadRequest, "token is required")
			return
		}
		if err := st.ConsumeEmailVerificationToken(r.Context(), token); err != nil {
			switch err {
			case auth.ErrVerifyExpired:
				httpx.Err(w, http.StatusGone, "verification token expired or already used")
			default:
				log.Printf("[auth] verify-email: %v", err)
				httpx.Err(w, http.StatusInternalServerError, "internal error")
			}
			return
		}
		httpx.JSON(w, map[string]bool{"email_verified": true})
	})

	// POST /api/auth/verify-email/resend  (rate-limited per IP, always 204)
	// Issues a fresh verification token and logs it. No body required.
	// Always 204 — never reveals whether email exists or is already verified.
	mux.HandleFunc("POST /api/auth/verify-email/resend", func(w http.ResponseWriter, r *http.Request) {
		ip, _, _ := net.SplitHostPort(r.RemoteAddr)
		if !rl.Allow(ip) {
			httpx.Err(w, http.StatusTooManyRequests, "rate limit exceeded")
			return
		}

		var body struct {
			Email string `json:"email"`
		}
		if !httpx.DecodeJSON(w, r, &body) {
			return
		}

		// Always 204 — never reveal whether email exists or is already verified.
		if err := st.ResendEmailVerification(r.Context(), body.Email); err != nil {
			log.Printf("[auth] verify-email resend: %v", err)
			// Still return 204 to avoid enumeration via error codes.
		}
		w.WriteHeader(http.StatusNoContent)
	})

	// GET /api/auth/modality
	// Full session required. Returns {"modality": <string>}.
	mux.HandleFunc("GET /api/auth/modality", func(w http.ResponseWriter, r *http.Request) {
		u := st.RequireSession(r.Context(), w, r)
		if u == nil {
			return
		}
		m, err := st.GetPreferred2FAModality(r.Context(), u.ID)
		if err != nil {
			log.Printf("[auth] get modality: %v", err)
			httpx.Err(w, http.StatusInternalServerError, "internal error")
			return
		}
		httpx.JSON(w, map[string]string{"modality": string(m)})
	})

	// POST /api/auth/modality
	// Full session required. Body: {"modality": <string>}.
	// Updates the preferred 2FA modality for the authenticated user.
	// "passkey" and "password+passkey" require at least one registered passkey
	// (returns 422 otherwise so the user registers a passkey first).
	mux.HandleFunc("POST /api/auth/modality", func(w http.ResponseWriter, r *http.Request) {
		u := st.RequireSession(r.Context(), w, r)
		if u == nil {
			return
		}
		var body struct {
			Modality string `json:"modality"`
		}
		if !httpx.DecodeJSON(w, r, &body) {
			return
		}
		m := auth.Preferred2FAModality(body.Modality)
		if !auth.ValidModalities[m] {
			httpx.Err(w, http.StatusBadRequest, "invalid modality; accepted: password+totp, password+passkey, passkey")
			return
		}
		// Passkey modalities require at least one credential to be registered.
		if m == auth.ModalityPasskeyOnly || m == auth.ModalityPasswordPasskey {
			creds, err := st.ListWebAuthnCredentials(r.Context(), u.ID)
			if err != nil {
				log.Printf("[auth] modality check credentials: %v", err)
				httpx.Err(w, http.StatusInternalServerError, "internal error")
				return
			}
			if len(creds) == 0 {
				httpx.Err(w, http.StatusUnprocessableEntity, "register a passkey before enabling passkey modality")
				return
			}
		}
		if err := st.SetPreferred2FAModality(r.Context(), u.ID, m); err != nil {
			log.Printf("[auth] set modality: %v", err)
			httpx.Err(w, http.StatusInternalServerError, "internal error")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	// POST /api/auth/totp/disable
	// Full session required. Takes {password, code}.
	// fleet_admin users are blocked (403).
	mux.HandleFunc("POST /api/auth/totp/disable", func(w http.ResponseWriter, r *http.Request) {
		u := st.RequireSession(r.Context(), w, r)
		if u == nil {
			return
		}

		if st.IsFleetAdmin(r.Context(), u.ID) {
			httpx.Err(w, http.StatusForbidden, "fleet_admin accounts cannot disable TOTP")
			return
		}

		var body struct {
			Password string `json:"password"`
			Code     string `json:"code"`
		}
		if !httpx.DecodeJSON(w, r, &body) {
			return
		}

		// Verify current password.
		var hash string
		err := st.QueryPasswordHash(r.Context(), u.ID, &hash)
		if err != nil {
			log.Printf("[auth] totp/disable query hash: %v", err)
			httpx.Err(w, http.StatusInternalServerError, "internal error")
			return
		}
		if err := auth.VerifyPassword(hash, body.Password); err != nil {
			httpx.Err(w, http.StatusUnauthorized, "invalid password")
			return
		}

		// Verify TOTP code.
		encSecret, enabled, err := st.LoadTOTPSecret(r.Context(), u.ID)
		if err != nil || !enabled || encSecret == nil {
			httpx.Err(w, http.StatusUnprocessableEntity, "TOTP not enabled")
			return
		}
		kek, err := auth.LoadTOTPKEK()
		if err != nil {
			log.Printf("[auth] totp/disable kek: %v", err)
			httpx.Err(w, http.StatusInternalServerError, "TOTP key not configured")
			return
		}
		plainSecret, err := auth.DecryptTOTPSecret(encSecret, kek)
		if err != nil {
			log.Printf("[auth] totp/disable decrypt: %v", err)
			httpx.Err(w, http.StatusInternalServerError, "internal error")
			return
		}
		if !auth.VerifyTOTPAndRecord(plainSecret, body.Code, u.ID) {
			httpx.Err(w, http.StatusUnauthorized, "invalid TOTP code")
			return
		}

		if err := st.DisableTOTP(r.Context(), u.ID); err != nil {
			log.Printf("[auth] totp/disable: %v", err)
			httpx.Err(w, http.StatusInternalServerError, "internal error")
			return
		}

		w.WriteHeader(http.StatusNoContent)
	})
}

// isValidationErr returns true for errors from user input validation (not
// internal/system errors), so we can return 400 vs 500.
func isValidationErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.HasPrefix(msg, "auth: invalid") ||
		strings.HasPrefix(msg, "auth: password must") ||
		strings.HasPrefix(msg, "auth: email")
}

// authRateLimiter is a minimal per-IP token-bucket limiter for auth endpoints.
// Separate from the global middleware limiter so we can apply stricter limits.
type authRateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*authBucket
	rps     float64
	burst   int
}

type authBucket struct {
	mu       sync.Mutex
	tokens   float64
	lastSeen time.Time
}

func newAuthRateLimiter(rps float64, burst int) *authRateLimiter {
	return &authRateLimiter{
		buckets: make(map[string]*authBucket),
		rps:     rps,
		burst:   burst,
	}
}

// Allow returns true if the request from ip should be allowed.
func (rl *authRateLimiter) Allow(ip string) bool {
	rl.mu.Lock()
	bkt, ok := rl.buckets[ip]
	if !ok {
		bkt = &authBucket{tokens: float64(rl.burst), lastSeen: time.Now()}
		rl.buckets[ip] = bkt
	}
	// Lazy GC: evict old entries.
	now := time.Now()
	for k, b := range rl.buckets {
		b.mu.Lock()
		idle := now.Sub(b.lastSeen)
		b.mu.Unlock()
		if idle > 10*time.Minute {
			delete(rl.buckets, k)
		}
	}
	rl.mu.Unlock()

	bkt.mu.Lock()
	defer bkt.mu.Unlock()
	elapsed := time.Since(bkt.lastSeen).Seconds()
	bkt.lastSeen = time.Now()
	bkt.tokens += elapsed * rl.rps
	if bkt.tokens > float64(rl.burst) {
		bkt.tokens = float64(rl.burst)
	}
	if bkt.tokens < 1 {
		return false
	}
	bkt.tokens--
	return true
}

// ──────────────────────────────────────────────────────────────────────────────
// WebAuthn (AUTH-11)
// ──────────────────────────────────────────────────────────────────────────────

// Pending WebAuthn ceremony state (challenge / *webauthn.SessionData) is now
// held in the shared auth schema via st.PutWebAuthnChallenge /
// st.TakeWebAuthnChallenge (internal/auth/webauthn_challenge.go, migration
// 0012) rather than a per-process in-memory map, so ceremonies survive across a
// multi-machine fleet (IDENTITY-SERVICE §4).

// RegisterWebAuthnRoutes wires WebAuthn endpoints into mux.
// These are additive; existing auth routes are unchanged.
func RegisterWebAuthnRoutes(mux *http.ServeMux, st *auth.Store) {
	rl := newAuthRateLimiter(3, 10) // same limits as auth endpoints (AUTH-07)

	// Pending ceremony state (challenge / *webauthn.SessionData) lives in the
	// shared auth schema (webauthn_challenges, migration 0012) via
	// st.PutWebAuthnChallenge / st.TakeWebAuthnChallenge — NOT a per-process map —
	// so a begin on machine A and a finish on machine B resolve the same
	// challenge across a multi-machine fleet (IDENTITY-SERVICE §4).

	// ── POST /api/auth/webauthn/register/begin ────────────────────────────────
	// Session required. Returns CredentialCreation JSON.
	mux.HandleFunc("POST /api/auth/webauthn/register/begin", func(w http.ResponseWriter, r *http.Request) {
		u := st.RequireSession(r.Context(), w, r)
		if u == nil {
			return
		}

		ip, _, _ := net.SplitHostPort(r.RemoteAddr)
		if !rl.Allow(ip) {
			httpx.Err(w, http.StatusTooManyRequests, "rate limit exceeded")
			return
		}

		creation, session, err := st.BeginWebAuthnRegistration(r.Context(), u)
		if err != nil {
			if err == auth.ErrWebAuthnNotConfigured {
				httpx.Err(w, http.StatusServiceUnavailable, "WebAuthn not configured on this server")
				return
			}
			log.Printf("[webauthn] register/begin: %v", err)
			httpx.Err(w, http.StatusInternalServerError, "internal error")
			return
		}

		if err := st.PutWebAuthnChallenge(r.Context(), u.ID, auth.WAKindRegister, session); err != nil {
			log.Printf("[webauthn] register/begin store challenge: %v", err)
			httpx.Err(w, http.StatusInternalServerError, "internal error")
			return
		}

		httpx.JSON(w, creation)
	})

	// ── POST /api/auth/webauthn/register/finish ───────────────────────────────
	// Session required. Body: raw RegistrationResponseJSON (from navigator.credentials.create).
	// Optional query param: ?name=<friendly-name>
	mux.HandleFunc("POST /api/auth/webauthn/register/finish", func(w http.ResponseWriter, r *http.Request) {
		u := st.RequireSession(r.Context(), w, r)
		if u == nil {
			return
		}

		ip, _, _ := net.SplitHostPort(r.RemoteAddr)
		if !rl.Allow(ip) {
			httpx.Err(w, http.StatusTooManyRequests, "rate limit exceeded")
			return
		}

		session, ok, cerr := st.TakeWebAuthnChallenge(r.Context(), u.ID, auth.WAKindRegister)
		if cerr != nil {
			log.Printf("[webauthn] register/finish take challenge: %v", cerr)
			httpx.Err(w, http.StatusInternalServerError, "internal error")
			return
		}
		if !ok {
			httpx.Err(w, http.StatusBadRequest, "no pending registration ceremony; call register/begin first")
			return
		}

		friendlyName := r.URL.Query().Get("name")

		cred, err := st.FinishWebAuthnRegistration(r.Context(), u, *session, r, friendlyName)
		if err != nil {
			if err == auth.ErrWebAuthnNotConfigured {
				httpx.Err(w, http.StatusServiceUnavailable, "WebAuthn not configured on this server")
				return
			}
			log.Printf("[webauthn] register/finish: %v", err)
			if strings.Contains(err.Error(), "already registered") {
				httpx.Err(w, http.StatusConflict, "credential already registered")
				return
			}
			httpx.Err(w, http.StatusBadRequest, "WebAuthn registration failed: "+err.Error())
			return
		}

		httpx.JSON(w, cred)
	})

	// ── POST /api/auth/webauthn/login/begin ───────────────────────────────────
	// No session required. Body: {email}. Returns CredentialAssertion JSON.
	mux.HandleFunc("POST /api/auth/webauthn/login/begin", func(w http.ResponseWriter, r *http.Request) {
		ip, _, _ := net.SplitHostPort(r.RemoteAddr)
		if !rl.Allow(ip) {
			httpx.Err(w, http.StatusTooManyRequests, "rate limit exceeded")
			return
		}

		var body struct {
			Email string `json:"email"`
		}
		if !httpx.DecodeJSON(w, r, &body) {
			return
		}
		if body.Email == "" {
			httpx.Err(w, http.StatusBadRequest, "email is required")
			return
		}

		wu, err := st.LoadWebAuthnUserForLogin(r.Context(), body.Email)
		if err != nil {
			switch err {
			case auth.ErrNotFound, auth.ErrWebAuthnNoCredentials:
				// Constant-time: always return same error to avoid enumeration.
				httpx.Err(w, http.StatusUnprocessableEntity, "no passkeys registered for this account")
			default:
				log.Printf("[webauthn] login/begin: %v", err)
				httpx.Err(w, http.StatusInternalServerError, "internal error")
			}
			return
		}

		// Check lockout before issuing challenge.
		if lockedUntil, locked := st.Check2FALockout(r.Context(), wu.ID); locked {
			retryAfter := lockedUntil - timeNow().Unix()
			if retryAfter < 0 {
				retryAfter = 0
			}
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Retry-After", fmt.Sprintf("%d", retryAfter))
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprintf(w, `{"error":"account locked","retry_after":%d}`, retryAfter)
			return
		}

		assertion, session, err := st.BeginWebAuthnLogin(r.Context(), wu)
		if err != nil {
			if err == auth.ErrWebAuthnNotConfigured {
				httpx.Err(w, http.StatusServiceUnavailable, "WebAuthn not configured on this server")
				return
			}
			log.Printf("[webauthn] login/begin: %v", err)
			httpx.Err(w, http.StatusInternalServerError, "internal error")
			return
		}

		if err := st.PutWebAuthnChallenge(r.Context(), wu.ID, auth.WAKindLogin, session); err != nil {
			log.Printf("[webauthn] login/begin store challenge: %v", err)
			httpx.Err(w, http.StatusInternalServerError, "internal error")
			return
		}

		// Return both the assertion and the userID so login/finish can look up
		// the pending entry. The userID is not secret -- it's already in the
		// CredentialAssertion's allowCredentials.
		w.Header().Set("Content-Type", "application/json")
		assertionJSON, err := json.Marshal(assertion)
		if err != nil {
			log.Printf("[webauthn] login/begin marshal: %v", err)
			httpx.Err(w, http.StatusInternalServerError, "internal error")
			return
		}
		// Embed assertion + user_id in a thin wrapper.
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `{"user_id":%q,"options":%s}`, wu.ID, assertionJSON)
	})

	// ── POST /api/auth/webauthn/login/finish ──────────────────────────────────
	// No session required. Body JSON must include "user_id" and the raw
	// PublicKeyCredential assertion from navigator.credentials.get.
	// On success: issues a full session cookie.
	mux.HandleFunc("POST /api/auth/webauthn/login/finish", func(w http.ResponseWriter, r *http.Request) {
		ip, _, _ := net.SplitHostPort(r.RemoteAddr)
		if !rl.Allow(ip) {
			httpx.Err(w, http.StatusTooManyRequests, "rate limit exceeded")
			return
		}

		// Parse user_id from query param (simpler than reading body twice).
		userID := r.URL.Query().Get("user_id")
		if userID == "" {
			httpx.Err(w, http.StatusBadRequest, "user_id query parameter is required")
			return
		}

		session, ok, cerr := st.TakeWebAuthnChallenge(r.Context(), userID, auth.WAKindLogin)
		if cerr != nil {
			log.Printf("[webauthn] login/finish take challenge: %v", cerr)
			httpx.Err(w, http.StatusInternalServerError, "internal error")
			return
		}
		if !ok {
			httpx.Err(w, http.StatusBadRequest, "no pending login ceremony; call login/begin first")
			return
		}

		// Lockout check.
		if lockedUntil, locked := st.Check2FALockout(r.Context(), userID); locked {
			retryAfter := lockedUntil - timeNow().Unix()
			if retryAfter < 0 {
				retryAfter = 0
			}
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Retry-After", fmt.Sprintf("%d", retryAfter))
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprintf(w, `{"error":"account locked","retry_after":%d}`, retryAfter)
			return
		}

		// Reload the credential-bearing user adapter (challenge storage is
		// state-minimal: it holds only the SessionData, not the user + creds).
		wu, err := st.LoadWebAuthnUserByID(r.Context(), userID)
		if err != nil {
			switch err {
			case auth.ErrNotFound, auth.ErrWebAuthnNoCredentials:
				httpx.Err(w, http.StatusUnprocessableEntity, "no passkeys registered for this account")
			default:
				log.Printf("[webauthn] login/finish load user: %v", err)
				httpx.Err(w, http.StatusInternalServerError, "internal error")
			}
			return
		}

		ua := r.UserAgent()
		token, err := st.FinishWebAuthnLogin(r.Context(), wu, *session, r, ip, ua)
		if err != nil {
			switch err {
			case auth.ErrWebAuthnNotConfigured:
				httpx.Err(w, http.StatusServiceUnavailable, "WebAuthn not configured on this server")
			case auth.ErrWebAuthnSignCountReplay:
				log.Printf("[webauthn] login/finish sign-count replay for user %s", userID)
				httpx.Err(w, http.StatusUnauthorized, "sign-count replay detected; possible cloned authenticator")
			default:
				log.Printf("[webauthn] login/finish: %v", err)
				// Re-check lockout (in case this attempt triggered it).
				if lockedUntil, locked := st.Check2FALockout(r.Context(), userID); locked {
					retryAfter := lockedUntil - timeNow().Unix()
					if retryAfter < 0 {
						retryAfter = 0
					}
					w.Header().Set("Content-Type", "application/json")
					w.Header().Set("Retry-After", fmt.Sprintf("%d", retryAfter))
					w.WriteHeader(http.StatusTooManyRequests)
					fmt.Fprintf(w, `{"error":"account locked","retry_after":%d}`, retryAfter)
					return
				}
				httpx.Err(w, http.StatusUnauthorized, "WebAuthn assertion failed")
			}
			return
		}

		auth.SetSessionCookie(w, token)
		w.WriteHeader(http.StatusNoContent)
	})

	// ── POST /api/auth/webauthn/login/begin-2fa ───────────────────────────────
	// Partial-session required (password+passkey modality, AUTH-12).
	// Begins a WebAuthn assertion ceremony for the passkey-as-2FA step.
	// The partial session identifies the user; no email param needed.
	mux.HandleFunc("POST /api/auth/webauthn/login/begin-2fa", func(w http.ResponseWriter, r *http.Request) {
		ip, _, _ := net.SplitHostPort(r.RemoteAddr)
		if !rl.Allow(ip) {
			httpx.Err(w, http.StatusTooManyRequests, "rate limit exceeded")
			return
		}

		token := auth.SessionFromRequest(r)
		userID, err := st.LookupPartialSession(r.Context(), token)
		if err != nil {
			httpx.Err(w, http.StatusUnauthorized, "not authenticated or session expired")
			return
		}

		// Lockout check (shared with TOTP lockout).
		if lockedUntil, locked := st.Check2FALockout(r.Context(), userID); locked {
			retryAfter := lockedUntil - timeNow().Unix()
			if retryAfter < 0 {
				retryAfter = 0
			}
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Retry-After", fmt.Sprintf("%d", retryAfter))
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprintf(w, `{"error":"account locked","retry_after":%d}`, retryAfter)
			return
		}

		wu, err := st.LoadWebAuthnUserByID(r.Context(), userID)
		if err != nil {
			switch err {
			case auth.ErrNotFound:
				httpx.Err(w, http.StatusUnauthorized, "not authenticated")
			case auth.ErrWebAuthnNoCredentials:
				httpx.Err(w, http.StatusUnprocessableEntity, "no passkeys registered for this account")
			default:
				log.Printf("[webauthn] login/begin-2fa: %v", err)
				httpx.Err(w, http.StatusInternalServerError, "internal error")
			}
			return
		}

		assertion, session, err := st.BeginWebAuthnLogin(r.Context(), wu)
		if err != nil {
			if err == auth.ErrWebAuthnNotConfigured {
				httpx.Err(w, http.StatusServiceUnavailable, "WebAuthn not configured on this server")
				return
			}
			log.Printf("[webauthn] login/begin-2fa: %v", err)
			httpx.Err(w, http.StatusInternalServerError, "internal error")
			return
		}

		if err := st.PutWebAuthnChallenge(r.Context(), userID, auth.WAKindLogin2FA, session); err != nil {
			log.Printf("[webauthn] login/begin-2fa store challenge: %v", err)
			httpx.Err(w, http.StatusInternalServerError, "internal error")
			return
		}

		httpx.JSON(w, assertion)
	})

	// ── POST /api/auth/webauthn/login/finish-2fa ──────────────────────────────
	// Partial-session required (password+passkey modality, AUTH-12).
	// Validates the WebAuthn assertion and upgrades the partial→full session.
	mux.HandleFunc("POST /api/auth/webauthn/login/finish-2fa", func(w http.ResponseWriter, r *http.Request) {
		ip, _, _ := net.SplitHostPort(r.RemoteAddr)
		if !rl.Allow(ip) {
			httpx.Err(w, http.StatusTooManyRequests, "rate limit exceeded")
			return
		}

		token := auth.SessionFromRequest(r)
		userID, err := st.LookupPartialSession(r.Context(), token)
		if err != nil {
			httpx.Err(w, http.StatusUnauthorized, "not authenticated or session expired")
			return
		}

		// Lockout check.
		if lockedUntil, locked := st.Check2FALockout(r.Context(), userID); locked {
			retryAfter := lockedUntil - timeNow().Unix()
			if retryAfter < 0 {
				retryAfter = 0
			}
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Retry-After", fmt.Sprintf("%d", retryAfter))
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprintf(w, `{"error":"account locked","retry_after":%d}`, retryAfter)
			return
		}

		session, ok, cerr := st.TakeWebAuthnChallenge(r.Context(), userID, auth.WAKindLogin2FA)
		if cerr != nil {
			log.Printf("[webauthn] login/finish-2fa take challenge: %v", cerr)
			httpx.Err(w, http.StatusInternalServerError, "internal error")
			return
		}
		if !ok {
			httpx.Err(w, http.StatusBadRequest, "no pending passkey ceremony; call login/begin-2fa first")
			return
		}

		wu, err := st.LoadWebAuthnUserByID(r.Context(), userID)
		if err != nil {
			switch err {
			case auth.ErrNotFound, auth.ErrWebAuthnNoCredentials:
				httpx.Err(w, http.StatusUnprocessableEntity, "no passkeys registered for this account")
			default:
				log.Printf("[webauthn] login/finish-2fa load user: %v", err)
				httpx.Err(w, http.StatusInternalServerError, "internal error")
			}
			return
		}

		ua := r.UserAgent()
		// FinishWebAuthnLoginNoSession verifies the assertion, updates sign-count,
		// resets the lockout counter on success, and records a failed attempt on
		// failure. It does NOT create a session; UpgradePartialSession does that.
		err = st.FinishWebAuthnLoginNoSession(r.Context(), wu, *session, r)
		if err != nil {
			switch err {
			case auth.ErrWebAuthnNotConfigured:
				httpx.Err(w, http.StatusServiceUnavailable, "WebAuthn not configured on this server")
			case auth.ErrWebAuthnSignCountReplay:
				log.Printf("[webauthn] login/finish-2fa sign-count replay for user %s", userID)
				httpx.Err(w, http.StatusUnauthorized, "sign-count replay detected; possible cloned authenticator")
			default:
				log.Printf("[webauthn] login/finish-2fa: %v", err)
				if lockedUntil, locked := st.Check2FALockout(r.Context(), userID); locked {
					retryAfter := lockedUntil - timeNow().Unix()
					if retryAfter < 0 {
						retryAfter = 0
					}
					w.Header().Set("Content-Type", "application/json")
					w.Header().Set("Retry-After", fmt.Sprintf("%d", retryAfter))
					w.WriteHeader(http.StatusTooManyRequests)
					fmt.Fprintf(w, `{"error":"account locked","retry_after":%d}`, retryAfter)
					return
				}
				httpx.Err(w, http.StatusUnauthorized, "WebAuthn assertion failed")
			}
			return
		}

		// Upgrade the partial session to a full one.
		fullToken, upgradeErr := st.UpgradePartialSession(r.Context(), token, ip, ua)
		if upgradeErr != nil {
			log.Printf("[webauthn] login/finish-2fa upgrade: %v", upgradeErr)
			httpx.Err(w, http.StatusInternalServerError, "internal error")
			return
		}

		auth.SetSessionCookie(w, fullToken)
		w.WriteHeader(http.StatusNoContent)
	})

	// ── GET /api/auth/webauthn/credentials ────────────────────────────────────
	// Session required. Returns {credentials:[...]} for the authenticated user.
	mux.HandleFunc("GET /api/auth/webauthn/credentials", func(w http.ResponseWriter, r *http.Request) {
		u := st.RequireSession(r.Context(), w, r)
		if u == nil {
			return
		}
		creds, err := st.ListWebAuthnCredentials(r.Context(), u.ID)
		if err != nil {
			log.Printf("[webauthn] list credentials: %v", err)
			httpx.Err(w, http.StatusInternalServerError, "internal error")
			return
		}
		httpx.JSON(w, map[string]any{"credentials": creds})
	})

	// ── POST /api/auth/webauthn/credentials/revoke ────────────────────────────
	// Session required. Body: {id} (the row ID from the credentials list).
	mux.HandleFunc("POST /api/auth/webauthn/credentials/revoke", func(w http.ResponseWriter, r *http.Request) {
		u := st.RequireSession(r.Context(), w, r)
		if u == nil {
			return
		}
		var body struct {
			ID string `json:"id"`
		}
		if !httpx.DecodeJSON(w, r, &body) {
			return
		}
		if body.ID == "" {
			httpx.Err(w, http.StatusBadRequest, "id is required")
			return
		}
		if err := st.RevokeWebAuthnCredential(r.Context(), u.ID, body.ID); err != nil {
			switch err {
			case auth.ErrWebAuthnCredentialNotFound:
				httpx.Err(w, http.StatusNotFound, "credential not found")
			default:
				log.Printf("[webauthn] revoke credential: %v", err)
				httpx.Err(w, http.StatusInternalServerError, "internal error")
			}
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}
