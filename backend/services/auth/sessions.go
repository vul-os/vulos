package auth

import "time"

// SESSION-TAKEOVER: surfacing a user's concurrent sessions so a new sign-in can
// offer "take over this device (sign out the others)" or "keep the others (sign
// out here)". This is the single-active-session UX; genuine multi-session
// coexistence is a separate, deliberately-unbuilt roadmap item
// (roadmap/MULTI-SESSION.md) — nothing here forcibly enforces one session, it
// only makes the choice visible and actionable.

// SessionSummary is a REDACTED view of a Session, safe to return to the client.
// It deliberately omits Token: listing your own sessions must never hand back a
// bearer credential for any of them (including the current one), or an XSS in
// one tab could lift every device's token from the list response.
type SessionSummary struct {
	ID        string    `json:"id"`
	DeviceID  string    `json:"device_id,omitempty"`
	Provider  string    `json:"provider,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
	// Current marks the session whose token matches the caller's — the one that
	// survives a "take over" and is signed out by "keep the others".
	Current bool `json:"current"`
}

// ListSessionSummaries returns all ACTIVE (unexpired) sessions for userID as
// redacted summaries, flagging the caller's own (token == currentToken) as
// Current. Expired-but-not-yet-pruned sessions are skipped so the client never
// prompts about a dead device.
func (s *Store) ListSessionSummaries(userID, currentToken string) []SessionSummary {
	s.mu.RLock()
	defer s.mu.RUnlock()
	now := time.Now()
	out := make([]SessionSummary, 0)
	for token, sess := range s.sessions {
		if sess.UserID != userID || sess.ExpiresAt.Before(now) {
			continue
		}
		out = append(out, SessionSummary{
			ID:        sess.ID,
			DeviceID:  sess.DeviceID,
			Provider:  sess.Provider,
			CreatedAt: sess.CreatedAt,
			ExpiresAt: sess.ExpiresAt,
			Current:   token == currentToken,
		})
	}
	return out
}

// RevokeOtherSessions revokes every ACTIVE session for userID EXCEPT the one
// whose token == keepToken (the caller's current session), returning the number
// revoked. This is the "take over this device" primitive: the caller stays
// signed in, every other device is signed out on its next request.
//
// keepToken == "" would revoke ALL of the user's sessions (nothing is kept);
// callers that mean "take over" must pass the caller's real token. Mirrors the
// revoke-others loop in ResetPasswordWithSessionKey (delete from the map, then
// delete from the durable store) so behavior stays consistent.
func (s *Store) RevokeOtherSessions(userID, keepToken string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	revoked := make([]string, 0)
	for token, sess := range s.sessions {
		if sess.UserID == userID && token != keepToken {
			delete(s.sessions, token)
			revoked = append(revoked, token)
		}
	}
	for _, token := range revoked {
		s.deleteSession(token)
	}
	return len(revoked)
}
