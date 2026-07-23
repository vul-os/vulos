package auth

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestSessionSummary_NeverLeaksToken is the crown-jewel property: a summary must
// carry no bearer credential, so listing your sessions can't hand an attacker
// (or an XSS) a token for any device.
func TestSessionSummary_NeverLeaksToken(t *testing.T) {
	b, err := json.Marshal(SessionSummary{ID: "s1", DeviceID: "dev", Provider: "local", Current: true})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(strings.ToLower(string(b)), "token") {
		t.Errorf("SessionSummary JSON leaks a token field: %s", b)
	}
}

// TestListSessionSummaries_FlagsCurrentAndListsAll confirms every active session
// is listed and exactly the caller's own is marked Current.
func TestListSessionSummaries_FlagsCurrentAndListsAll(t *testing.T) {
	s := newTestStore(t)
	u := s.FindOrCreateUser("local", "u1", "alice@example.com", "Alice", "", true)
	current := s.CreateSession(u, "device-A")
	s.CreateSession(u, "device-B")

	got := s.ListSessionSummaries(u.ID, current.Token)
	if len(got) != 2 {
		t.Fatalf("want 2 sessions, got %d", len(got))
	}
	currentCount := 0
	for _, ss := range got {
		if ss.Current {
			currentCount++
		}
	}
	if currentCount != 1 {
		t.Errorf("want exactly 1 session flagged current, got %d", currentCount)
	}
}

// TestListSessionSummaries_SkipsExpired ensures a dead-but-not-pruned session is
// never surfaced (so the client won't prompt about a device that's already gone).
func TestListSessionSummaries_SkipsExpired(t *testing.T) {
	s := newTestStore(t)
	u := s.FindOrCreateUser("local", "u1", "a@x.com", "Alice", "", true)
	live := s.CreateSession(u, "device-A")

	// Force an expired session directly into the map.
	s.mu.Lock()
	s.sessions["dead-token"] = &Session{
		ID: "dead", UserID: u.ID, Token: "dead-token",
		ExpiresAt: time.Now().Add(-time.Hour), CreatedAt: time.Now().Add(-2 * time.Hour),
	}
	s.mu.Unlock()

	got := s.ListSessionSummaries(u.ID, live.Token)
	if len(got) != 1 {
		t.Fatalf("want 1 live session (expired skipped), got %d", len(got))
	}
	if !got[0].Current {
		t.Errorf("the single live session should be the caller's current one")
	}
}

// TestListSessionSummaries_UserIsolation: a user's list contains only their own
// sessions, never another user's.
func TestListSessionSummaries_UserIsolation(t *testing.T) {
	s := newTestStore(t)
	alice := s.FindOrCreateUser("local", "a", "a@x.com", "Alice", "", true)
	bob := s.FindOrCreateUser("local", "b", "b@x.com", "Bob", "", true)
	aliceSess := s.CreateSession(alice, "A1")
	s.CreateSession(bob, "B1")

	got := s.ListSessionSummaries(alice.ID, aliceSess.Token)
	if len(got) != 1 {
		t.Fatalf("alice should see only her 1 session, got %d", len(got))
	}
}

// TestRevokeOtherSessions_KeepsCurrent is the "take over this device" happy path.
func TestRevokeOtherSessions_KeepsCurrent(t *testing.T) {
	s := newTestStore(t)
	u := s.FindOrCreateUser("local", "u1", "a@x.com", "Alice", "", true)
	keep := s.CreateSession(u, "A")
	other1 := s.CreateSession(u, "B")
	other2 := s.CreateSession(u, "C")

	n := s.RevokeOtherSessions(u.ID, keep.Token)
	if n != 2 {
		t.Fatalf("want 2 revoked, got %d", n)
	}
	if _, ok := s.ValidateToken(keep.Token); !ok {
		t.Error("the current session must survive a takeover")
	}
	if _, ok := s.ValidateToken(other1.Token); ok {
		t.Error("other1 should be revoked")
	}
	if _, ok := s.ValidateToken(other2.Token); ok {
		t.Error("other2 should be revoked")
	}
}

// TestRevokeOtherSessions_UserIsolation is the critical safety property: taking
// over one account must never touch another user's sessions.
func TestRevokeOtherSessions_UserIsolation(t *testing.T) {
	s := newTestStore(t)
	alice := s.FindOrCreateUser("local", "a", "a@x.com", "Alice", "", true)
	bob := s.FindOrCreateUser("local", "b", "b@x.com", "Bob", "", true)
	aliceKeep := s.CreateSession(alice, "A1")
	s.CreateSession(alice, "A2")
	bobSess := s.CreateSession(bob, "B1")

	n := s.RevokeOtherSessions(alice.ID, aliceKeep.Token)
	if n != 1 {
		t.Fatalf("alice's takeover should revoke only her 1 other session, got %d", n)
	}
	if _, ok := s.ValidateToken(bobSess.Token); !ok {
		t.Error("bob's session was revoked by alice's takeover — user isolation breach")
	}
}
