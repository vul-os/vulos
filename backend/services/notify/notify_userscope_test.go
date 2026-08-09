package notify

// notify_userscope_test.go — regression coverage for NOTIF-USER-SCOPE.
//
// A Vulos box is multi-user (multiple accounts share one server process and one
// notify.Service). A notification carrying a specific user's PRIVATE content —
// most sharply, a fired reminder's text, which the reminders store isolates
// per-user — must never be delivered to, or listed for, another account. These
// tests prove the leak is closed on ALL read/delivery paths:
//
//   - the live WS broadcast (Service.Handler → SendNotification),
//   - the in-memory history read (Service.ListForUser / UnreadForUser),
//   - the persisted history read (Store.ListForUser).
//
// Untargeted (empty UserID) notifications remain box-level and reach everyone.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// dialAs opens a notify WS stream authenticated as userID (empty ⇒ anonymous).
// The auth Middleware sets X-User-ID upstream in production; here we set it on
// the dial to exercise the Handler exactly as it sees a post-middleware request.
func dialAs(t *testing.T, srvURL, userID string) *websocket.Conn {
	t.Helper()
	u := "ws" + strings.TrimPrefix(srvURL, "http")
	h := http.Header{}
	if userID != "" {
		h.Set("X-User-ID", userID)
	}
	c, _, err := websocket.DefaultDialer.Dial(u, h)
	if err != nil {
		t.Fatalf("dial as %q: %v", userID, err)
	}
	return c
}

// readBody reads one notification frame and returns its Body as a string.
func readBody(t *testing.T, c *websocket.Conn) string {
	t.Helper()
	c.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, data, err := c.ReadMessage()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var n Notification
	if err := json.Unmarshal(data, &n); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	s, _ := n.Body.(string)
	return s
}

// waitClients blocks until the service has registered n live connections (the
// Handler adds to s.clients just after the upgrade returns, a hair after Dial).
func waitClients(t *testing.T, s *Service, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.RLock()
		got := len(s.clients)
		s.mu.RUnlock()
		if got >= n {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d clients", n)
}

// TestWSDelivery_UserPrivateNotTargetedCrossUser is the core leak regression: a
// user-private notification reaches ONLY its owner's stream, and an untargeted
// one reaches everyone. Proven deterministically by frame ORDER on Bob's single
// connection: if the private "secret" had leaked to Bob it would be his first
// read; instead his first read is the later untargeted "hi".
func TestWSDelivery_UserPrivateNotTargetedCrossUser(t *testing.T) {
	svc := New()
	srv := httptest.NewServer(svc.Handler())
	defer srv.Close()

	alice := dialAs(t, srv.URL, "alice")
	defer alice.Close()
	bob := dialAs(t, srv.URL, "bob")
	defer bob.Close()
	waitClients(t, svc, 2)

	// Private to Alice — must NOT reach Bob.
	svc.SendNotification(Notification{Title: "Reminder", Body: "secret", UserID: "alice"})
	if got := readBody(t, alice); got != "secret" {
		t.Fatalf("alice should receive her private notification, got %q", got)
	}

	// Untargeted / box-level — must reach BOTH.
	svc.SendNotification(Notification{Title: "System", Body: "hi"})
	if got := readBody(t, bob); got != "hi" {
		t.Fatalf("bob's first frame must be the untargeted 'hi' (the private 'secret' must never have been delivered to bob), got %q", got)
	}
	if got := readBody(t, alice); got != "hi" {
		t.Fatalf("alice should also receive the untargeted notification, got %q", got)
	}
}

// TestWSDelivery_AnonymousStreamGetsSystemOnly proves an unauthenticated stream
// (empty user id) receives box-level notifications but no user-private ones.
func TestWSDelivery_AnonymousStreamGetsSystemOnly(t *testing.T) {
	svc := New()
	srv := httptest.NewServer(svc.Handler())
	defer srv.Close()

	anon := dialAs(t, srv.URL, "")
	defer anon.Close()
	waitClients(t, svc, 1)

	svc.SendNotification(Notification{Title: "Reminder", Body: "private", UserID: "alice"})
	svc.SendNotification(Notification{Title: "System", Body: "broadcast"})
	if got := readBody(t, anon); got != "broadcast" {
		t.Fatalf("anonymous stream must skip the user-private notification and get the broadcast, got %q", got)
	}
}

// TestListForUser_ScopesHistory proves the in-memory history read path is scoped.
func TestListForUser_ScopesHistory(t *testing.T) {
	svc := New()
	svc.SendNotification(Notification{Title: "sys", Body: "sys", Source: "system"})   // untargeted
	svc.SendNotification(Notification{Title: "a", Body: "a-secret", UserID: "alice"}) // alice-private
	svc.SendNotification(Notification{Title: "b", Body: "b-secret", UserID: "bob"})   // bob-private

	alice := svc.ListForUser("alice", 0)
	if len(alice) != 2 {
		t.Fatalf("alice should see 2 (own + system), got %d: %+v", len(alice), alice)
	}
	for _, n := range alice {
		if n.UserID == "bob" {
			t.Fatalf("alice must never see bob's private notification: %+v", n)
		}
	}
	if c := svc.UnreadForUser("alice"); c != 2 {
		t.Fatalf("alice unread should be 2, got %d", c)
	}
	if c := svc.UnreadForUser("bob"); c != 2 {
		t.Fatalf("bob unread should be 2, got %d", c)
	}
}

// TestStoreListForUser_ScopesPersistedHistory proves the persisted read path is
// scoped identically.
func TestStoreListForUser_ScopesPersistedHistory(t *testing.T) {
	st, err := OpenStore(filepath.Join(t.TempDir(), "n.json"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()
	st.Append(&Notification{ID: "1", Title: "sys", Body: "sys"})
	st.Append(&Notification{ID: "2", Title: "a", Body: "a-secret", UserID: "alice"})
	st.Append(&Notification{ID: "3", Title: "b", Body: "b-secret", UserID: "bob"})

	got := st.ListForUser("alice", 0)
	if len(got) != 2 {
		t.Fatalf("alice should see 2 persisted (own + system), got %d", len(got))
	}
	for _, n := range got {
		if n.UserID == "bob" {
			t.Fatalf("persisted list leaked bob's private notification to alice: %+v", n)
		}
	}
}

// TestMarkReadForUser_CannotTouchAnotherUsersPrivateNotification proves the
// core NOTIF-USER-SCOPE-02 mutation leak is closed: alice cannot flip the
// read state of bob's private notification by supplying its ID, even though
// she can mark her own.
func TestMarkReadForUser_CannotTouchAnotherUsersPrivateNotification(t *testing.T) {
	svc := New()
	svc.SendNotification(Notification{ID: "bob-1", Title: "b", Body: "b-secret", UserID: "bob"})
	svc.SendNotification(Notification{ID: "alice-1", Title: "a", Body: "a-secret", UserID: "alice"})

	svc.MarkReadForUser("bob-1", "alice")
	svc.MarkReadForUser("alice-1", "alice")

	bobs := svc.ListForUser("bob", 0)
	if len(bobs) != 1 || bobs[0].Read {
		t.Fatalf("alice must not be able to mark bob's private notification read: %+v", bobs)
	}
	alices := svc.ListForUser("alice", 0)
	if len(alices) != 1 || !alices[0].Read {
		t.Fatalf("alice should be able to mark her own notification read: %+v", alices)
	}
}

// TestMarkAllReadForUser_LeavesOtherAccountsUnread proves the "empty id"
// bulk-mark path only touches notifications visible to the caller.
func TestMarkAllReadForUser_LeavesOtherAccountsUnread(t *testing.T) {
	svc := New()
	svc.SendNotification(Notification{ID: "sys-1", Title: "s", Body: "sys"}) // untargeted
	svc.SendNotification(Notification{ID: "bob-1", Title: "b", Body: "b-secret", UserID: "bob"})
	svc.SendNotification(Notification{ID: "alice-1", Title: "a", Body: "a-secret", UserID: "alice"})

	svc.MarkAllReadForUser("alice")

	if svc.UnreadForUser("bob") != 1 {
		t.Fatalf("alice's MarkAllReadForUser must not silence bob's unread badge")
	}
	if svc.UnreadForUser("alice") != 0 {
		t.Fatalf("alice's own + the untargeted notification should now be read")
	}
}

// TestClearForUser_PreservesOtherAccountsHistory proves one account cannot
// wipe another account's notification history via the scoped Clear.
func TestClearForUser_PreservesOtherAccountsHistory(t *testing.T) {
	svc := New()
	svc.SendNotification(Notification{ID: "sys-1", Title: "s", Body: "sys"})
	svc.SendNotification(Notification{ID: "bob-1", Title: "b", Body: "b-secret", UserID: "bob"})
	svc.SendNotification(Notification{ID: "alice-1", Title: "a", Body: "a-secret", UserID: "alice"})

	svc.ClearForUser("alice")

	bobs := svc.ListForUser("bob", 0)
	if len(bobs) != 1 || bobs[0].ID != "bob-1" {
		t.Fatalf("bob's private notification must survive alice's ClearForUser: %+v", bobs)
	}
	if got := svc.ListForUser("alice", 0); len(got) != 0 {
		t.Fatalf("alice should have cleared everything visible to her, got %+v", got)
	}
}

// TestUnscopedMarkReadAndClear_StillGlobal pins the documented UNSCOPED
// behaviour of the box-level/internal MarkRead, MarkAllRead and Clear — they
// must remain global (for internal/system callers) and must NEVER be the
// function an HTTP mutation route calls directly; that contract is enforced
// at the route layer (see cmd/server/routes_notify_scope_test.go).
func TestUnscopedMarkReadAndClear_StillGlobal(t *testing.T) {
	svc := New()
	svc.SendNotification(Notification{ID: "bob-1", Title: "b", Body: "b-secret", UserID: "bob"})
	svc.MarkRead("bob-1")
	if got := svc.ListForUser("bob", 0); len(got) != 1 || !got[0].Read {
		t.Fatalf("unscoped MarkRead should still mark the notification: %+v", got)
	}
	svc.Clear()
	if got := svc.ListForUser("bob", 0); len(got) != 0 {
		t.Fatalf("unscoped Clear should still wipe everything: %+v", got)
	}
}
