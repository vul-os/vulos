package main

// routes_notify_scope_test.go — HTTP-level regression for NOTIF-USER-SCOPE-02.
//
// POST /api/notifications/read and POST /api/notifications/clear are wired
// inline in main() (not their own registerXxxRoutes function), so — following
// the established convention in idor_regression_test.go for other inline
// main() routes — these tests replicate the exact handler bodies currently in
// main.go and prove the cross-user scenario directly: user A must not be able
// to mark user B's private notification read, silence B's unread badge, or
// wipe B's notification history, merely by holding a valid session of her own.
//
// If a future edit reverts these handlers to call the unscoped
// MarkRead/MarkAllRead/Clear instead of the *ForUser variants, these tests
// fail immediately.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"vulos/backend/services/notify"
)

// buildNotifyReadClearMux wires only /api/notifications/read and
// /api/notifications/clear, matching main.go's current handlers (including
// the JSON body shape: {"id": "..."}).
func buildNotifyReadClearMux(svc *notify.Service) *http.ServeMux {
	// Drive the REAL registration main() uses, not a copy of it. The previous
	// version of this helper rebuilt the handlers inline, so every test below
	// passed while main() still called the unscoped MarkAllRead/MarkRead/Clear
	// — verified by reverting main() and watching them all stay green. A test
	// that cannot fail when the shipped route regresses is not a gate.
	mux := http.NewServeMux()
	registerNotifyReadClearRoutes(mux, svc)
	return mux
}

func doNotifyScopeReq(t *testing.T, mux *http.ServeMux, method, path, userID, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if userID != "" {
		req.Header.Set("X-User-ID", userID)
	}
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// TestNotifyReadRoute_CannotMarkAnotherUsersPrivateNotification is the
// IDOR-NOTIFY-02 regression: alice sends POST /api/notifications/read with
// bob's private notification id — it must stay unread.
func TestNotifyReadRoute_CannotMarkAnotherUsersPrivateNotification(t *testing.T) {
	svc := notify.New()
	svc.SendNotification(notify.Notification{ID: "bob-1", Title: "b", Body: "b-secret", UserID: "bob"})
	mux := buildNotifyReadClearMux(svc)

	rec := doNotifyScopeReq(t, mux, "POST", "/api/notifications/read", "alice", `{"id":"bob-1"}`)
	if rec.Code != 200 {
		t.Fatalf("read = %d, want 200 (route itself always succeeds; scoping happens inside)", rec.Code)
	}
	bobs := svc.ListForUser("bob", 0)
	if len(bobs) != 1 || bobs[0].Read {
		t.Fatalf("IDOR-NOTIFY-02 regression: alice marked bob's private notification read: %+v", bobs)
	}
}

// TestNotifyReadRoute_EmptyIDDoesNotSilenceAnotherAccount proves the
// "mark all read" (empty id) path scopes to the caller, not the whole box.
func TestNotifyReadRoute_EmptyIDDoesNotSilenceAnotherAccount(t *testing.T) {
	svc := notify.New()
	svc.SendNotification(notify.Notification{ID: "bob-1", Title: "b", Body: "b-secret", UserID: "bob"})
	mux := buildNotifyReadClearMux(svc)

	rec := doNotifyScopeReq(t, mux, "POST", "/api/notifications/read", "alice", `{}`)
	if rec.Code != 200 {
		t.Fatalf("mark-all-read = %d, want 200", rec.Code)
	}
	if svc.UnreadForUser("bob") != 1 {
		t.Fatalf("IDOR-NOTIFY-02 regression: alice's empty-id /read silenced bob's unread badge")
	}
}

// TestNotifyClearRoute_PreservesAnotherAccountsHistory proves one account
// cannot wipe another account's notification history via /clear.
func TestNotifyClearRoute_PreservesAnotherAccountsHistory(t *testing.T) {
	svc := notify.New()
	svc.SendNotification(notify.Notification{ID: "bob-1", Title: "b", Body: "b-secret", UserID: "bob"})
	mux := buildNotifyReadClearMux(svc)

	rec := doNotifyScopeReq(t, mux, "POST", "/api/notifications/clear", "alice", ``)
	if rec.Code != 200 {
		t.Fatalf("clear = %d, want 200", rec.Code)
	}
	bobs := svc.ListForUser("bob", 0)
	if len(bobs) != 1 || bobs[0].ID != "bob-1" {
		t.Fatalf("IDOR-NOTIFY-02 regression: alice's /clear wiped bob's notification history: %+v", bobs)
	}
}

// TestNotifyReadClearRoutes_RequireAuth: both mutation routes reject an
// unauthenticated caller.
func TestNotifyReadClearRoutes_RequireAuth(t *testing.T) {
	svc := notify.New()
	mux := buildNotifyReadClearMux(svc)
	if rec := doNotifyScopeReq(t, mux, "POST", "/api/notifications/read", "", `{}`); rec.Code != 401 {
		t.Fatalf("unauth read = %d, want 401", rec.Code)
	}
	if rec := doNotifyScopeReq(t, mux, "POST", "/api/notifications/clear", "", ``); rec.Code != 401 {
		t.Fatalf("unauth clear = %d, want 401", rec.Code)
	}
}
