package auth

import (
	"net/http"
)

// handleListSessions returns the caller's active sessions (redacted — no
// tokens), with the current one flagged. The frontend calls this right after a
// sign-in: if more than one active session exists, it prompts the user to take
// over (sign out the others) or keep them (sign out here). Session-required.
func (h *Handler) handleListSessions(w http.ResponseWriter, r *http.Request) {
	userID := r.Header.Get("X-User-ID")
	if userID == "" {
		writeErr(w, 401, "unauthorized")
		return
	}
	current := extractToken(r)
	sessions := h.store.ListSessionSummaries(userID, current)
	// others = active sessions that are NOT the caller's — the ones a takeover
	// would end. Computed here so the client doesn't have to (and can't
	// miscount by including its own).
	others := 0
	for _, s := range sessions {
		if !s.Current {
			others++
		}
	}
	writeJSON(w, map[string]any{
		"sessions": sessions,
		"others":   others,
	})
}

// handleRevokeOtherSessions is the "take over this device" action: it signs out
// every OTHER active session for the caller, keeping only the current one. The
// caller must be authenticated (a valid session is the authorization — you can
// only ever revoke your OWN other sessions, never another user's), and we keep
// the caller's own token alive so this device stays signed in.
//
// The signed-out devices learn of it on their next request (their token no
// longer validates → 401 → the client shows "signed out: taken over on another
// device"). A proactive web-push notice is a follow-up enhancement, not wired
// here.
func (h *Handler) handleRevokeOtherSessions(w http.ResponseWriter, r *http.Request) {
	userID := r.Header.Get("X-User-ID")
	if userID == "" {
		writeErr(w, 401, "unauthorized")
		return
	}
	keepToken := extractToken(r)
	if keepToken == "" {
		// No identifiable current session → refuse rather than revoke ALL of the
		// user's sessions (which keepToken=="" would do). Fail closed.
		writeErr(w, 401, "no active session")
		return
	}
	// Defensive: confirm the token really belongs to this user before acting.
	if sess, ok := h.store.ValidateToken(keepToken); !ok || sess.UserID != userID {
		writeErr(w, 401, "invalid session")
		return
	}
	revoked := h.store.RevokeOtherSessions(userID, keepToken)
	h.store.Flush()
	// Refresh the caller's (unchanged) cookie so this device unambiguously stays
	// signed in after the takeover.
	http.SetCookie(w, sessionCookie(r, keepToken))
	writeJSON(w, map[string]any{"status": "took over", "revoked": revoked})
}
