// Package idp is the isolated Vulos IDENTITY / LOGIN boundary (IDENTITY-SERVICE
// §1). It is the "always-up anchor" — the minimal credential-verify + session
// issuance + validation surface that other products authenticate against — and
// it is deliberately built to depend on ALMOST NOTHING so a billing,
// provisioning, or mail-edge fault in the monolith can never take login down.
//
// ISOLATION CONTRACT (enforced by boundary_test.go): this package imports ONLY
//
//	github.com/vul-os/vulos-management/pkg/auth   — the credential/session core
//	github.com/vul-os/vulos-management/pkg/httpx  — tiny JSON helpers
//	github.com/vul-os/vulos-management/pkg/cpdb   — the auth-schema DB handle (health ping)
//
// It MUST NOT import billing, payments, managed/provisioning, maildomain/
// mail-edge, superadmin, orgadmin, or any other subsystem. A test asserts the
// transitive import graph never grows one of those, so `cmd/idp` can be built
// and deployed as a standalone Fly app that opens ONLY the `auth` schema and
// shares fate with nothing else (§1.5, §7 Phase 2).
//
// WHAT IDP OWNS (§1.2):
//   - credential verify: POST /api/auth/login (argon2id today; OPAQUE when on)
//   - session lifecycle: logout, GET /api/auth/me
//   - the read every OTHER product depends on: POST /api/session/introspect
//   - health: GET /healthz (fail-closed DB ping) + GET /livez (process liveness)
//
// WHAT IDP DELIBERATELY DOES NOT OWN: signup (it provisions a cell/org/mailbox —
// a legitimate monolith concern, NOT a login dependency), billing, provisioning,
// mail. The monolith keeps those and becomes just another introspection client.
//
// The heavy credential logic lives in internal/auth (Store.Login, IntrospectSession,
// createSession) so the handlers here are thin translators — there is no logic
// duplicated from the monolith, only a second, minimal mux over the same Store.
package idp

import (
	"context"
	"encoding/json"
	"log"
	"net"
	"net/http"

	"github.com/vul-os/vulos-management/pkg/auth"
	"github.com/vul-os/vulos-management/pkg/httpx"
)

// serviceAuthChecker validates the CP shared secret presented by a peer product
// on POST /api/session/introspect. It is injected so idp does not import the
// monolith's secret store; cmd/idp supplies a small env-backed implementation
// and the monolith supplies its rotation-aware one. Fail-closed: when
// Configured() is false the introspect endpoint returns 503 (never open).
type serviceAuthChecker interface {
	// Configured reports whether ANY shared secret is set. When false the
	// introspect endpoint fails closed (503) rather than authenticating peers.
	Configured(ctx context.Context) bool
	// Valid reports whether the presented X-Relay-Auth header matches the
	// current (or previous, during rotation) shared secret, in constant time.
	Valid(ctx context.Context, presented string) bool
}

// Deps are the collaborators the identity mux needs. Store is required; Auth may
// be nil, in which case the introspect endpoint fails closed (503) — safe by
// default.
type Deps struct {
	Store *auth.Store
	Auth  serviceAuthChecker
}

// Handler builds the self-contained identity http.Handler: the login-anchor
// routes plus health checks, on a fresh ServeMux with NO other subsystem wired.
// This is the mux cmd/idp serves; it is also what a future consolidator can
// mount to run idp in-process while keeping the dependency boundary explicit.
func Handler(d Deps) http.Handler {
	mux := http.NewServeMux()
	RegisterRoutes(mux, d)
	RegisterHealth(mux, d.Store)
	return mux
}

// RegisterRoutes wires ONLY the identity/login/session routes onto mux. Callers
// that want health too should use Handler or also call RegisterHealth.
func RegisterRoutes(mux *http.ServeMux, d Deps) {
	st := d.Store
	// Per-machine first line; the authoritative distributed cap is the shared
	// DB-backed login-attempt limiter enforced inside Store.Login (§2.4/§6).
	rl := auth.NewRateBucketSet(3, 10) // 3 req/s sustained, burst 10, per IP

	// POST /api/auth/login — credential verify + session issuance. This is the
	// front door. It calls Store.Login (argon2id, or OPAQUE-preferred when the
	// flag is on) which itself enforces lockout + the shared login-attempt cap;
	// idp adds a cheap per-machine IP bucket in front. Fail-closed on every error.
	mux.HandleFunc("POST /api/auth/login", func(w http.ResponseWriter, r *http.Request) {
		ip := clientIP(r)
		if !rl.Allow(ip) {
			httpx.Err(w, http.StatusTooManyRequests, "rate limit exceeded")
			return
		}
		var body struct {
			Email    string `json:"email"`
			Password string `json:"password"`
		}
		if !httpx.DecodeJSON(w, r, &body) {
			return
		}
		result, err := st.Login(r.Context(), body.Email, body.Password, ip, r.UserAgent())
		if err != nil {
			switch err {
			case auth.ErrHashMismatch, auth.ErrNotFound:
				httpx.Err(w, http.StatusUnauthorized, "invalid credentials")
			case auth.ErrAccountSuspended:
				httpx.Err(w, http.StatusForbidden, "account suspended")
			case auth.ErrLoginThrottled:
				httpx.Err(w, http.StatusTooManyRequests, "too many attempts, try again later")
			default:
				log.Printf("[idp] login: %v", err)
				httpx.Err(w, http.StatusInternalServerError, "internal error")
			}
			return
		}
		// Mirror the monolith's step responses so an idp deployment is a drop-in
		// for the login route. The account is ALWAYS server-derived (result.User),
		// never read from the request body.
		if result.EmailVerificationRequired {
			httpx.JSON(w, map[string]string{"step": "email_verification_required"})
			return
		}
		if result.PasskeyRequired {
			httpx.JSON(w, map[string]string{"step": "passkey_required"})
			return
		}
		if result.PasskeyAs2FA {
			writePartialCookie(w, result.Token)
			httpx.JSON(w, map[string]string{"step": "passkey_2fa_required"})
			return
		}
		if result.TOTPRequired {
			writePartialCookie(w, result.Token)
			httpx.JSON(w, map[string]string{"step": "totp_required"})
			return
		}
		auth.SetSessionCookie(w, result.Token)
		if devToken, derr := st.IssueDeviceToken(r.Context(), result.User.ID, ip, r.UserAgent()); derr == nil {
			auth.SetDeviceCookie(w, devToken)
		}
		httpx.JSON(w, result.User)
	})

	// POST /api/auth/logout — best-effort session delete + cookie clear. Never
	// fails a request; clearing the cookie is the security-relevant action.
	mux.HandleFunc("POST /api/auth/logout", func(w http.ResponseWriter, r *http.Request) {
		if token := auth.SessionFromRequest(r); token != "" {
			if err := st.DeleteSession(r.Context(), token); err != nil {
				log.Printf("[idp] logout delete: %v", err)
			}
		}
		auth.ClearSessionCookie(w)
		w.WriteHeader(http.StatusNoContent)
	})

	// GET /api/auth/me — resolve the current full session to its account. The
	// acting user is derived ENTIRELY from the session cookie; there is no path
	// to name another account. Partial (pre-2FA) sessions are rejected.
	mux.HandleFunc("GET /api/auth/me", func(w http.ResponseWriter, r *http.Request) {
		u := st.RequireSession(r.Context(), w, r)
		if u == nil {
			return // RequireSession already wrote 401/403
		}
		httpx.JSON(w, u)
	})

	// POST /api/session/introspect — the fail-closed, no-oracle session read that
	// EVERY other product depends on (§1.2). It moves into the always-up service
	// so a monolith outage cannot break session validation. Service-auth gated by
	// the injected checker; content-blind on every failure.
	mux.HandleFunc("POST /api/session/introspect", func(w http.ResponseWriter, r *http.Request) {
		if d.Auth == nil || !d.Auth.Configured(r.Context()) {
			// Fail-closed: without a shared secret we cannot authenticate a peer,
			// so we refuse rather than answer.
			httpx.Err(w, http.StatusServiceUnavailable, "no shared secret configured")
			return
		}
		if !d.Auth.Valid(r.Context(), r.Header.Get("X-Relay-Auth")) {
			httpx.Err(w, http.StatusUnauthorized, "invalid or missing X-Relay-Auth")
			return
		}
		var body struct {
			Session string `json:"session"`
		}
		// Cap the body: a session token is short.
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
		if err := dec.Decode(&body); err != nil {
			httpx.Err(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		intro := st.IntrospectSession(r.Context(), body.Session)
		httpx.JSON(w, map[string]any{
			"valid":     intro.Valid,
			"userId":    intro.UserID,
			"tenantId":  intro.TenantID,
			"expiresAt": intro.ExpiresAt,
		})
	})
}

// writePartialCookie mirrors the monolith's short-lived (5 min) partial-session
// cookie used for the TOTP / passkey-as-2FA step.
func writePartialCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     auth.SessionCookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   300,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})
}

// clientIP extracts the peer IP (no port) for the per-IP limiter.
func clientIP(r *http.Request) string {
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	return ip
}
