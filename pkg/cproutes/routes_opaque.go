// routes_opaque.go -- OPAQUE (asymmetric PAKE) HTTP endpoints (IDENTITY-SERVICE §3).
//
// ADDITIVE + OFF BY DEFAULT. These endpoints exist ALONGSIDE POST /api/auth/login
// (argon2id), which is untouched and remains the live path. Every handler is
// gated behind AUTH_OPAQUE_ENABLED (+ OPAQUE_SERVER_KEY): when the flag is off,
// the routes return 404 so the surface is invisible until explicitly enabled.
//
// Endpoints:
//
//	POST /api/auth/opaque/register/response -- server RegistrationResponse (session required)
//	POST /api/auth/opaque/register/store    -- persist the client's OPAQUE record (session required)
//	POST /api/auth/opaque/login/start       -- server KE2 for {email, ke1}
//	POST /api/auth/opaque/login/finish      -- verify KE3, issue session (honours 2FA)
//
// The two register endpoints require an authenticated session because OPAQUE
// registration is a "dual-write on password set" (§3.5): the account already
// exists (created via the normal argon2 signup) and is enrolling an OPAQUE record
// in addition. Login endpoints are unauthenticated (that IS the login).
package cproutes

import (
	"encoding/base64"
	"log"
	"net"
	"net/http"

	"github.com/vul-os/vulos-management/pkg/auth"
	"github.com/vul-os/vulos-management/pkg/httpx"
)

// registerOpaqueRoutes wires the flag-gated OPAQUE endpoints into mux. Safe to
// call unconditionally: when OPAQUE is disabled every handler short-circuits to
// 404, so wiring it in prod is a no-op until AUTH_OPAQUE_ENABLED is set.
func registerOpaqueRoutes(mux *http.ServeMux, st *auth.Store) {
	rl := newAuthRateLimiter(3, 10)

	// opaqueService constructs a per-request service. Returns nil + writes the
	// response when OPAQUE is unavailable, so callers can `if svc == nil { return }`.
	opaqueService := func(w http.ResponseWriter) *auth.OpaqueService {
		svc, err := auth.NewOpaqueService(st)
		if err != nil {
			switch err {
			case auth.ErrOpaqueDisabled:
				// Feature off — behave as if the route does not exist.
				httpx.Err(w, http.StatusNotFound, "not found")
			case auth.ErrOpaqueNotConfigured:
				log.Printf("[opaque] enabled but OPAQUE_SERVER_KEY not set")
				httpx.Err(w, http.StatusServiceUnavailable, "OPAQUE not configured")
			default:
				log.Printf("[opaque] service init: %v", err)
				httpx.Err(w, http.StatusInternalServerError, "internal error")
			}
			return nil
		}
		return svc
	}

	// ── POST /api/auth/opaque/register/response ────────────────────────────────
	// Session required. Body: {request: base64(RegistrationRequest)}.
	// Returns {response: base64(RegistrationResponse)}.
	mux.HandleFunc("POST /api/auth/opaque/register/response", func(w http.ResponseWriter, r *http.Request) {
		u := st.RequireSession(r.Context(), w, r)
		if u == nil {
			return
		}
		svc := opaqueService(w)
		if svc == nil {
			return
		}
		var body struct {
			Request string `json:"request"`
		}
		if !httpx.DecodeJSON(w, r, &body) {
			return
		}
		reqBytes, err := base64.StdEncoding.DecodeString(body.Request)
		if err != nil {
			httpx.Err(w, http.StatusBadRequest, "invalid request encoding")
			return
		}
		resp, err := svc.OpaqueRegistrationResponse(u.ID, reqBytes)
		if err != nil {
			httpx.Err(w, http.StatusBadRequest, "OPAQUE registration failed")
			return
		}
		httpx.JSON(w, map[string]string{"response": base64.StdEncoding.EncodeToString(resp)})
	})

	// ── POST /api/auth/opaque/register/store ───────────────────────────────────
	// Session required. Body: {record: base64(RegistrationRecord)}. Dual-write.
	mux.HandleFunc("POST /api/auth/opaque/register/store", func(w http.ResponseWriter, r *http.Request) {
		u := st.RequireSession(r.Context(), w, r)
		if u == nil {
			return
		}
		svc := opaqueService(w)
		if svc == nil {
			return
		}
		var body struct {
			Record string `json:"record"`
		}
		if !httpx.DecodeJSON(w, r, &body) {
			return
		}
		recBytes, err := base64.StdEncoding.DecodeString(body.Record)
		if err != nil {
			httpx.Err(w, http.StatusBadRequest, "invalid record encoding")
			return
		}
		if err := svc.StoreOpaqueRecord(r.Context(), u.ID, recBytes); err != nil {
			httpx.Err(w, http.StatusBadRequest, "OPAQUE record store failed")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	// ── POST /api/auth/opaque/login/start ──────────────────────────────────────
	// No session. Body: {email, ke1: base64(KE1)}.
	// Returns {handshake_id, ke2: base64(KE2)}.
	//
	// Enumeration note: this reveals whether an account has an OPAQUE record.
	// During the coexistence window that is acceptable (OPAQUE is opt-in and the
	// live path is still argon2); a production hardening step is to return a fake
	// KE2 for unknown accounts (opaque.GetFakeRecord) — left for the rollout phase.
	mux.HandleFunc("POST /api/auth/opaque/login/start", func(w http.ResponseWriter, r *http.Request) {
		ip, _, _ := net.SplitHostPort(r.RemoteAddr)
		if !rl.Allow(ip) {
			httpx.Err(w, http.StatusTooManyRequests, "rate limit exceeded")
			return
		}
		svc := opaqueService(w)
		if svc == nil {
			return
		}
		var body struct {
			Email string `json:"email"`
			KE1   string `json:"ke1"`
		}
		if !httpx.DecodeJSON(w, r, &body) {
			return
		}
		ke1Bytes, err := base64.StdEncoding.DecodeString(body.KE1)
		if err != nil || body.Email == "" {
			httpx.Err(w, http.StatusBadRequest, "email and ke1 are required")
			return
		}
		// Shared login-attempt cap (same as password login). Checked BEFORE the
		// account lookup so a throttled request reveals nothing. No-op unless
		// AUTH_SHARED_LOGIN_LIMIT is set.
		if throttled := st.CheckLoginThrottle(r.Context(), body.Email, ip); throttled != nil {
			httpx.Err(w, http.StatusTooManyRequests, "too many attempts, try again later")
			return
		}
		userID, err := st.UserIDByEmail(r.Context(), body.Email)
		if err != nil {
			// No oracle beyond "no OPAQUE login available". Count as a failed
			// attempt against the shared cap (unknown-account probing).
			st.RecordLoginAttempt(r.Context(), body.Email, ip, false)
			httpx.Err(w, http.StatusUnprocessableEntity, "OPAQUE login unavailable for this account")
			return
		}
		handshakeID, ke2, err := svc.OpaqueLoginStart(r.Context(), userID, ke1Bytes)
		if err != nil {
			httpx.Err(w, http.StatusUnprocessableEntity, "OPAQUE login unavailable for this account")
			return
		}
		httpx.JSON(w, map[string]string{
			"handshake_id": handshakeID,
			"ke2":          base64.StdEncoding.EncodeToString(ke2),
		})
	})

	// ── POST /api/auth/opaque/login/finish ─────────────────────────────────────
	// No session. Body: {handshake_id, ke3: base64(KE3)}.
	// On success issues a session via the SAME machinery as password login,
	// honouring the email-verification gate and 2FA modalities (§3.5 step 4).
	mux.HandleFunc("POST /api/auth/opaque/login/finish", func(w http.ResponseWriter, r *http.Request) {
		ip, _, _ := net.SplitHostPort(r.RemoteAddr)
		if !rl.Allow(ip) {
			httpx.Err(w, http.StatusTooManyRequests, "rate limit exceeded")
			return
		}
		svc := opaqueService(w)
		if svc == nil {
			return
		}
		var body struct {
			HandshakeID string `json:"handshake_id"`
			KE3         string `json:"ke3"`
		}
		if !httpx.DecodeJSON(w, r, &body) {
			return
		}
		ke3Bytes, err := base64.StdEncoding.DecodeString(body.KE3)
		if err != nil || body.HandshakeID == "" {
			httpx.Err(w, http.StatusBadRequest, "handshake_id and ke3 are required")
			return
		}
		userID, err := svc.OpaqueLoginFinish(r.Context(), body.HandshakeID, ke3Bytes)
		if err != nil {
			// Failed handshake (wrong password / replayed KE3). Count against the
			// per-IP shared cap (no email available at finish; the IP window still
			// throttles credential stuffing).
			st.RecordLoginAttempt(r.Context(), "", ip, false)
			httpx.Err(w, http.StatusUnauthorized, "invalid credentials")
			return
		}

		result, err := st.IssuePostAuthSession(r.Context(), userID, ip, r.UserAgent())
		if err != nil {
			switch err {
			case auth.ErrAccountSuspended:
				httpx.Err(w, http.StatusForbidden, "account suspended")
			default:
				log.Printf("[opaque] issue session: %v", err)
				httpx.Err(w, http.StatusInternalServerError, "internal error")
			}
			return
		}

		// Mirror the password-login response steps (email-verify gate, 2FA).
		if result.EmailVerificationRequired {
			httpx.JSON(w, map[string]string{"step": "email_verification_required"})
			return
		}
		if result.PasskeyAs2FA {
			setPartialSessionCookie(w, result.Token)
			httpx.JSON(w, map[string]string{"step": "passkey_2fa_required"})
			return
		}
		if result.TOTPRequired {
			setPartialSessionCookie(w, result.Token)
			httpx.JSON(w, map[string]string{"step": "totp_required"})
			return
		}
		auth.SetSessionCookie(w, result.Token)
		loginSuccessRedirectOrJSON(w, r, result.User)
	})
}

// setPartialSessionCookie writes a short-lived (5 min) partial-session cookie,
// matching the password-login path's TOTP/passkey-2FA step cookies.
func setPartialSessionCookie(w http.ResponseWriter, token string) {
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
