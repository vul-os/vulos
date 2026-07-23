package main

// routes_accountsecurity.go — account-takeover (ATO) monitoring HTTP surface.
//
// Routes registered:
//
//	GET  /api/accountsecurity/feed              — caller's recent sensitive
//	     actions + alerts, for Settings -> Security.
//	POST /api/accountsecurity/alerts/{id}/dismiss — "this was me", no session
//	     impact.
//	POST /api/accountsecurity/alerts/{id}/lock    — "this wasn't me": revokes
//	     every session for the caller (services/auth.RevokeAllSessions,
//	     forcing a fresh sign-in everywhere, including any attacker session)
//	     and marks the alert resolved. Gated by stepup.Require per the
//	     step-up contract (destructive: it also signs the caller out).
//
// All three are scoped to the verified X-User-ID stamped by
// auth.Handler.Middleware (never a client-supplied value — see
// routes_instances_manage.go's instRequireAdmin for the same pattern applied
// to the admin-role gate). A user only ever sees or mutates their own
// sensitive-action history and alerts; accountsecurity.Service enforces
// ownership again at the store layer (ErrNotOwner) as defense in depth.
//
// This box is single-owner (see accountsecurity package doc); there is no
// cross-account admin dashboard here by design — every profile manages only
// its own security feed.

import (
	"log"
	"net/http"
	"strconv"

	"vulos/backend/services/accountsecurity"
	"vulos/backend/services/auth"
	"vulos/backend/services/stepup"
)

// registerAccountSecurityRoutes wires the ATO monitoring feed + response
// routes onto mux. svc may be nil (e.g. accountsecurity.Open failed at boot);
// the routes then fail closed with 503 rather than serve a broken feature.
// authStore may be nil in degraded boot; the lock route then fails closed
// (403) rather than pretend to revoke sessions that were never checked.
func registerAccountSecurityRoutes(mux *http.ServeMux, svc *accountsecurity.Service, authStore *auth.Store) {
	mux.HandleFunc("GET /api/accountsecurity/feed", func(w http.ResponseWriter, r *http.Request) {
		userID := r.Header.Get("X-User-ID")
		if userID == "" {
			writeErr(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		if svc == nil {
			writeErr(w, http.StatusServiceUnavailable, "account security monitoring unavailable")
			return
		}
		feed, err := svc.Feed(r.Context(), userID)
		if err != nil {
			log.Printf("[accountsecurity] feed %s: %v", userID, err)
			writeErr(w, http.StatusInternalServerError, "could not load security feed")
			return
		}
		writeJSON(w, feed)
	})

	mux.HandleFunc("POST /api/accountsecurity/alerts/{id}/dismiss", func(w http.ResponseWriter, r *http.Request) {
		userID := r.Header.Get("X-User-ID")
		if userID == "" {
			writeErr(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		if svc == nil {
			writeErr(w, http.StatusServiceUnavailable, "account security monitoring unavailable")
			return
		}
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "invalid alert id")
			return
		}
		switch err := svc.Dismiss(r.Context(), userID, id); err {
		case nil:
			writeJSON(w, map[string]string{"status": "dismissed"})
		case accountsecurity.ErrNotOwner:
			writeErr(w, http.StatusNotFound, "alert not found")
		default:
			log.Printf("[accountsecurity] dismiss %s alert=%d: %v", userID, id, err)
			writeErr(w, http.StatusInternalServerError, "could not dismiss alert")
		}
	})

	// POST /api/accountsecurity/alerts/{id}/lock — the "this wasn't me"
	// response: revoke every session for this account (any device, including
	// an attacker's) and require a fresh sign-in everywhere. Step-up gated:
	// re-proving the account password before a self-inflicted, box-wide
	// sign-out is exactly the kind of destructive action the step-up contract
	// calls out.
	mux.HandleFunc("POST /api/accountsecurity/alerts/{id}/lock", func(w http.ResponseWriter, r *http.Request) {
		userID := r.Header.Get("X-User-ID")
		if userID == "" {
			writeErr(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		if svc == nil {
			writeErr(w, http.StatusServiceUnavailable, "account security monitoring unavailable")
			return
		}
		if authStore == nil {
			writeErr(w, http.StatusForbidden, "account lock unavailable")
			return
		}
		if err := stepup.Require(r); err != nil {
			writeErr(w, http.StatusForbidden, "extra security step required")
			return
		}
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "invalid alert id")
			return
		}

		// Revoke sessions FIRST, then record the resolution — if bookkeeping
		// fails after a successful revoke, the account is still safe (the
		// stronger of the two failure directions); the reverse order could
		// mark "locked" without actually having signed anyone out.
		authStore.RevokeAllSessions(userID)

		switch err := svc.ResolveLocked(r.Context(), userID, id); err {
		case nil:
			writeJSON(w, map[string]string{"status": "locked"})
		case accountsecurity.ErrNotOwner:
			// Sessions are already revoked regardless (safe default even if
			// the alert id was bogus/already resolved) — still tell the
			// caller the alert itself wasn't found.
			writeErr(w, http.StatusNotFound, "alert not found")
		default:
			log.Printf("[accountsecurity] lock %s alert=%d: %v", userID, id, err)
			writeErr(w, http.StatusInternalServerError, "sessions revoked, but could not record alert resolution")
		}
	})
}
