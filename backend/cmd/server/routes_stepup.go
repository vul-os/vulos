package main

// routes_stepup.go — the generic "extra security step" gate's HTTP surface.
//
// Route registered:
//
//	POST /api/auth/stepup/verify — re-prove the caller's account password;
//	  on success, mints a short-lived (~5 min) elevated token (services/stepup)
//	  and sets it as the vulos_stepup cookie. Privileged handlers elsewhere in
//	  the OS call stepup.Require(r) before performing a sensitive/destructive
//	  action; this endpoint is how the client obtains that elevation.
//
// The verified X-User-ID is read the same way every other privileged route in
// this package reads it: stamped by auth.Handler.Middleware from the
// validated session, never trusted from the client directly (see
// routes_instances_manage.go's instRequireAdmin for the same pattern applied
// to the admin-role gate).
//
// Fail-closed: a nil auth store (degraded boot) refuses rather than elevates
// anyone, and a wrong/missing password never mints a token.

import (
	"encoding/json"
	"net/http"
	"strings"

	"vulos/backend/services/auth"
	"vulos/backend/services/stepup"
)

// stepupRateLimiter throttles password re-verify attempts against this
// endpoint the same way auth.Handler throttles login attempts (5 failures /
// 10 min window / 30 min ban) — step-up re-proves the same secret a brute
// forcer would want, so it gets the same protection.
var stepupRateLimiter = auth.DefaultRateLimiter()

// registerStepupRoutes mounts the step-up verify route against authStore.
// authStore may be nil in degraded boot (no auth store); the route then
// fails closed (403) rather than elevate anyone.
func registerStepupRoutes(mux *http.ServeMux, authStore *auth.Store) {
	mux.HandleFunc("POST /api/auth/stepup/verify", func(w http.ResponseWriter, r *http.Request) {
		userID := r.Header.Get("X-User-ID")
		if userID == "" {
			writeErr(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		if authStore == nil {
			writeErr(w, http.StatusForbidden, "step-up unavailable")
			return
		}

		ip := stepupClientIP(r)
		if stepupRateLimiter.IsBanned(ip) {
			writeErr(w, http.StatusTooManyRequests, "too many attempts")
			return
		}

		var body struct {
			Password string `json:"password"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		if body.Password == "" {
			writeErr(w, http.StatusBadRequest, "password required")
			return
		}

		// VerifyUserPassword is auth.Store's documented step-up primitive: it
		// re-proves possession of the account password for an already
		// authenticated user, with a dummy-hash compare on every failure path
		// so this cannot become a timing oracle.
		if !authStore.VerifyUserPassword(userID, body.Password) {
			stepupRateLimiter.RecordFailure(ip)
			writeErr(w, http.StatusUnauthorized, "incorrect password")
			return
		}
		stepupRateLimiter.RecordSuccess(ip)

		token, expires, err := stepup.Mint(userID)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "could not elevate session")
			return
		}
		stepup.SetCookie(w, r, token, expires)
		writeJSON(w, map[string]any{
			"status":     "elevated",
			"token":      token,
			"expires_at": expires,
		})
	})
}

// stepupClientIP mirrors the intent of auth package's unexported extractIP
// (proxy-aware IP for rate limiting) without reaching into that package: it
// prefers the loopback-trusted X-Forwarded-For header, falling back to
// RemoteAddr. Good enough for a rate-limit key — this is defense in depth
// on top of the fail-closed VerifyUserPassword check, not the only guard.
func stepupClientIP(r *http.Request) string {
	remoteHost := r.RemoteAddr
	if i := strings.LastIndex(remoteHost, ":"); i >= 0 {
		remoteHost = remoteHost[:i]
	}
	if remoteHost == "127.0.0.1" || remoteHost == "::1" {
		if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
			return strings.TrimSpace(strings.Split(fwd, ",")[0])
		}
	}
	return remoteHost
}
