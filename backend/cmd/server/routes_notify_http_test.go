package main

// routes_notify_http_test.go — HTTP-level route tests for the previously
// untested NOTIF-02/05/06 surface: the DND status/config endpoints, the inline
// notification-action dispatch endpoint, and the persistent-store history/prune
// endpoints. These are all auth-gated mutation routes that carried 0% coverage.
//
// Coverage target: registerNotifyExtRoutes + registerNotifyPersistRoutes (both
// 0.0% before this file). The tests assert the real authZ contract at the
// transport layer (X-User-ID gate on every mutation), the validation branches
// (bad JSON, bad RFC3339 `until`, unknown action → 422), and correct behavior
// (a registered action actually dispatches; prune falls back to defaults).
//
// One end-to-end test wraps the DND write in the REAL auth.Handler.Middleware
// (reusing the wave-31 pattern) to prove the identity the handler gates on comes
// from a validated session, not a client-supplied header.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"vulos/backend/services/auth"
	"vulos/backend/services/notify"
)

// newNotifyMux registers the ext (DND + action) routes against a fresh temp home
// and returns the mux plus the *notifyExt so tests can register action handlers
// and inspect state directly.
func newNotifyMux(t *testing.T) (*http.ServeMux, *notifyExt) {
	mux, ext, _, _ := newNotifyMuxWithStore(t)
	return mux, ext
}

// newNotifyMuxWithStore is newNotifyMux plus a real auth store holding an admin
// and an ordinary second profile, so the DND routes' admin gate (DND-SCOPE-01)
// can be exercised. It returns their user ids.
//
// DND is BOX-WIDE state, so the write route is admin-only; passing a nil store
// here would both skip the gate and, before it was made nil-safe, panic inside
// it.
func newNotifyMuxWithStore(t *testing.T) (*http.ServeMux, *notifyExt, string, string) {
	t.Helper()
	t.Setenv("VULOS_BOOTSTRAP_ADMIN_EMAIL", "admin@example.com")
	store, err := auth.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("auth.NewStore: %v", err)
	}
	admin := store.FindOrCreateUser("test", "notify-admin", "admin@example.com", "Admin", "", true)
	guest := store.FindOrCreateUser("test", "notify-guest", "guest@example.com", "Guest", "", true)
	if admin == nil || guest == nil {
		t.Fatal("could not create test profiles")
	}
	if p, _ := store.GetProfile(admin.ID); p == nil || p.Role != auth.RoleAdmin {
		t.Fatalf("first profile is not admin — the gate cannot be distinguished")
	}
	if p, _ := store.GetProfile(guest.ID); p == nil || p.Role == auth.RoleAdmin {
		t.Fatal("second profile is admin — the gate cannot be distinguished")
	}

	svc := notify.New()
	mux := http.NewServeMux()
	ext := registerNotifyExtRoutes(mux, svc, t.TempDir(), store)
	return mux, ext, admin.ID, guest.ID
}

func doNotifyJSON(t *testing.T, mux *http.ServeMux, method, path, user, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if user != "" {
		req.Header.Set("X-User-ID", user)
	}
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// TestNotifyDNDStatusReadable: GET /api/notifications/dnd is read-only and
// returns the default (off) status shape.
func TestNotifyDNDStatusReadable(t *testing.T) {
	mux, _ := newNotifyMux(t)
	rec := doNotifyJSON(t, mux, "GET", "/api/notifications/dnd", "", "")
	if rec.Code != 200 {
		t.Fatalf("GET dnd = %d, want 200", rec.Code)
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out["mode"] != "off" || out["active"] != false {
		t.Errorf("default DND should be off/inactive: %v", out)
	}
	if _, ok := out["effective_mode"]; !ok {
		t.Errorf("status missing effective_mode: %v", out)
	}
}

// TestNotifyDNDSetRequiresAuth: POST /api/notifications/dnd is a mutation and
// must reject an unauthenticated caller with 401, changing nothing.
func TestNotifyDNDSetRequiresAuth(t *testing.T) {
	mux, ext := newNotifyMux(t)
	rec := doNotifyJSON(t, mux, "POST", "/api/notifications/dnd", "", `{"mode":"total"}`)
	if rec.Code != 401 {
		t.Fatalf("unauth POST dnd = %d, want 401", rec.Code)
	}
	if ext.dnd.Status()["mode"] != "off" {
		t.Fatalf("unauthenticated request mutated DND state: %v", ext.dnd.Status())
	}
}

// TestNotifyDNDSetAndClear: an authenticated user can turn DND to total and then
// back off; the status reflects each change.
func TestNotifyDNDSetAndClear(t *testing.T) {
	mux, ext, adminID, _ := newNotifyMuxWithStore(t)

	rec := doNotifyJSON(t, mux, "POST", "/api/notifications/dnd", adminID, `{"mode":"total"}`)
	if rec.Code != 200 {
		t.Fatalf("set total = %d, want 200", rec.Code)
	}
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out["mode"] != "total" || out["active"] != true {
		t.Fatalf("mode should be total/active: %v", out)
	}
	if ext.dnd.Status()["mode"] != "total" {
		t.Fatalf("underlying manager not updated: %v", ext.dnd.Status())
	}

	// Clearing back to off.
	rec = doNotifyJSON(t, mux, "POST", "/api/notifications/dnd", adminID, `{"mode":"off"}`)
	if rec.Code != 200 {
		t.Fatalf("set off = %d, want 200", rec.Code)
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out["mode"] != "off" || out["active"] != false {
		t.Fatalf("mode should be off/inactive after clear: %v", out)
	}
}

// TestNotifyDNDBadUntil: a mode with a malformed `until` timestamp is a 400 and
// leaves DND unchanged (fail-safe: never silently activate "forever").
func TestNotifyDNDBadUntil(t *testing.T) {
	mux, ext, adminID, _ := newNotifyMuxWithStore(t)
	rec := doNotifyJSON(t, mux, "POST", "/api/notifications/dnd", adminID,
		`{"mode":"priority","until":"not-a-timestamp"}`)
	if rec.Code != 400 {
		t.Fatalf("bad until = %d, want 400", rec.Code)
	}
	if ext.dnd.Status()["mode"] != "off" {
		t.Fatalf("bad-until request mutated DND: %v", ext.dnd.Status())
	}
}

// TestNotifyDNDValidUntil: a well-formed RFC3339 `until` is accepted and the
// status echoes a non-nil until.
func TestNotifyDNDValidUntil(t *testing.T) {
	mux, _, adminID, _ := newNotifyMuxWithStore(t)
	rec := doNotifyJSON(t, mux, "POST", "/api/notifications/dnd", adminID,
		`{"mode":"priority","until":"2099-01-02T03:04:05Z"}`)
	if rec.Code != 200 {
		t.Fatalf("valid until = %d, want 200", rec.Code)
	}
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out["mode"] != "priority" || out["until"] == nil {
		t.Fatalf("priority-until status wrong: %v", out)
	}
}

// TestNotifyDNDBadJSON: a malformed body on the authenticated write path is a
// 400 (the auth gate passes, JSON decode fails).
func TestNotifyDNDBadJSON(t *testing.T) {
	mux, _, adminID, _ := newNotifyMuxWithStore(t)
	rec := doNotifyJSON(t, mux, "POST", "/api/notifications/dnd", adminID, `{not json`)
	if rec.Code != 400 {
		t.Fatalf("bad JSON = %d, want 400", rec.Code)
	}
}

// TestNotifyDNDSchedule: a schedule payload with no mode change is applied and
// returns 200 (exercises the SetSchedule branch).
func TestNotifyDNDSchedule(t *testing.T) {
	mux, ext, adminID, _ := newNotifyMuxWithStore(t)
	rec := doNotifyJSON(t, mux, "POST", "/api/notifications/dnd", adminID,
		`{"schedule":[{"days":[1,2,3],"start":"22:00","end":"07:00"}]}`)
	if rec.Code != 200 {
		t.Fatalf("schedule = %d, want 200", rec.Code)
	}
	sched, _ := ext.dnd.Status()["schedule"].([]notify.ScheduleWindow)
	if len(sched) != 1 {
		t.Fatalf("schedule not applied: %v", ext.dnd.Status()["schedule"])
	}
}

// --- inline action dispatch --------------------------------------------------

// TestNotifyActionRequiresAuth: POST /api/notifications/{id}/action rejects an
// unauthenticated caller.
func TestNotifyActionRequiresAuth(t *testing.T) {
	mux, _ := newNotifyMux(t)
	rec := doNotifyJSON(t, mux, "POST", "/api/notifications/n1/action", "", `{"action_id":"archive"}`)
	if rec.Code != 401 {
		t.Fatalf("unauth action = %d, want 401", rec.Code)
	}
}

// sendNotifFor pushes a notification through the REAL service and returns its
// server-assigned id. owner == "" makes it a box-level notification (visible to
// everyone); a non-empty owner makes it that user's PRIVATE notification.
func sendNotifFor(t *testing.T, ext *notifyExt, owner string) string {
	t.Helper()
	n := ext.svc.SendNotification(notify.Notification{
		Title:  "t",
		Body:   "b",
		Level:  notify.LevelInfo,
		Source: "test",
		UserID: owner,
	})
	if n.ID == "" {
		t.Fatal("service did not assign a notification id")
	}
	return n.ID
}

// TestNotifyActionMissingActionID: an authenticated call on a notification the
// caller OWNS but with no action_id is a 400.
func TestNotifyActionMissingActionID(t *testing.T) {
	mux, ext := newNotifyMux(t)
	id := sendNotifFor(t, ext, "user-1")
	rec := doNotifyJSON(t, mux, "POST", "/api/notifications/"+id+"/action", "user-1", `{}`)
	if rec.Code != 400 {
		t.Fatalf("missing action_id = %d, want 400", rec.Code)
	}
}

// TestNotifyActionUnknown: dispatching an action id with no registered handler
// is a 422 (the registry reports action-not-found).
func TestNotifyActionUnknown(t *testing.T) {
	mux, ext := newNotifyMux(t)
	id := sendNotifFor(t, ext, "user-1")
	rec := doNotifyJSON(t, mux, "POST", "/api/notifications/"+id+"/action", "user-1",
		`{"action_id":"does-not-exist"}`)
	if rec.Code != 422 {
		t.Fatalf("unknown action = %d, want 422", rec.Code)
	}
}

// TestNotifyActionDispatches: a REGISTERED action handler is invoked with the
// path's notification id and the body's action id, and returns 200 dispatched.
func TestNotifyActionDispatches(t *testing.T) {
	mux, ext := newNotifyMux(t)
	id := sendNotifFor(t, ext, "user-1")
	var gotNotif, gotAction string
	ext.actions.Register("archive", func(notifID, actionID string) error {
		gotNotif, gotAction = notifID, actionID
		return nil
	})
	rec := doNotifyJSON(t, mux, "POST", "/api/notifications/"+id+"/action", "user-1",
		`{"action_id":"archive"}`)
	if rec.Code != 200 {
		t.Fatalf("dispatch = %d, want 200", rec.Code)
	}
	if gotNotif != id || gotAction != "archive" {
		t.Fatalf("handler got (%q,%q), want (%s,archive)", gotNotif, gotAction, id)
	}
	if !strings.Contains(rec.Body.String(), "dispatched") {
		t.Errorf("expected dispatched status: %s", rec.Body.String())
	}
}

// TestNotifyActionCrossUserDenied is the NOTIF-USER-SCOPE-03 regression: a
// SECOND authenticated profile must not be able to fire the inline action on
// another account's PRIVATE notification by supplying its id. Being signed in is
// not authorization.
//
// It drives registerNotifyExtRoutes — the SAME function main() calls — so a
// regression in the shipped route fails here (a scope test over a duplicated
// handler would not).
func TestNotifyActionCrossUserDenied(t *testing.T) {
	mux, ext := newNotifyMux(t)
	victimNotif := sendNotifFor(t, ext, "victim")

	fired := false
	ext.actions.Register("archive", func(notifID, actionID string) error {
		fired = true
		return nil
	})

	// The attacker is a fully authenticated profile on the same box.
	rec := doNotifyJSON(t, mux, "POST", "/api/notifications/"+victimNotif+"/action",
		"attacker", `{"action_id":"archive"}`)

	if rec.Code != 404 {
		t.Fatalf("cross-user action = %d, want 404 (no existence oracle); body=%s",
			rec.Code, rec.Body.String())
	}
	if fired {
		t.Fatal("SECURITY: attacker fired the action handler on the victim's private notification")
	}

	// Control: the rightful owner still succeeds, so the gate denies the
	// attacker specifically rather than breaking the endpoint for everyone.
	rec = doNotifyJSON(t, mux, "POST", "/api/notifications/"+victimNotif+"/action",
		"victim", `{"action_id":"archive"}`)
	if rec.Code != 200 {
		t.Fatalf("owner action = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if !fired {
		t.Fatal("owner's dispatch did not reach the handler")
	}
}

// TestNotifyActionUnknownNotificationDenied: an id that was never issued is
// rejected 404 rather than dispatched. This pins the FAIL-CLOSED direction — a
// lookup miss must deny, not allow.
func TestNotifyActionUnknownNotificationDenied(t *testing.T) {
	mux, ext := newNotifyMux(t)
	fired := false
	ext.actions.Register("archive", func(notifID, actionID string) error {
		fired = true
		return nil
	})
	rec := doNotifyJSON(t, mux, "POST", "/api/notifications/no-such-id/action",
		"user-1", `{"action_id":"archive"}`)
	if rec.Code != 404 {
		t.Fatalf("unknown notification = %d, want 404", rec.Code)
	}
	if fired {
		t.Fatal("SECURITY: dispatched an action for a notification that does not exist")
	}
}

// TestNotifyActionBoxLevelAllowed: a box-level notification (empty UserID) is
// still actionable by any authenticated profile — the gate must not over-block
// the system notifications every account is meant to see.
func TestNotifyActionBoxLevelAllowed(t *testing.T) {
	mux, ext := newNotifyMux(t)
	id := sendNotifFor(t, ext, "") // box-level
	fired := false
	ext.actions.Register("archive", func(notifID, actionID string) error {
		fired = true
		return nil
	})
	rec := doNotifyJSON(t, mux, "POST", "/api/notifications/"+id+"/action",
		"anyone", `{"action_id":"archive"}`)
	if rec.Code != 200 {
		t.Fatalf("box-level action = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if !fired {
		t.Fatal("box-level dispatch did not reach the handler")
	}
}

// --- persistent store: history + prune --------------------------------------

// newNotifyPersistMux wires the persist+prune routes over a temp auth store
// seeded with one admin and one ordinary user — the first registered profile
// is auto-admin (see auth.Store.Register), matching the idorRegression /
// instances-manage test convention elsewhere in this package. Returns the
// mux plus the two profiles' real IDs (X-User-ID must be the profile ID, not
// the login username).
func newNotifyPersistMux(t *testing.T) (mux *http.ServeMux, adminID, userID string) {
	t.Helper()
	svc := notify.New()
	mux = http.NewServeMux()
	store, err := auth.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("auth.NewStore: %v", err)
	}
	admin, err := store.Register("admin", "adminpw123-secure!", "Admin")
	if err != nil {
		t.Fatalf("register admin: %v", err)
	}
	user, err := store.Register("user-1", "user1pw123-secure!", "User One")
	if err != nil {
		t.Fatalf("register user-1: %v", err)
	}
	registerNotifyPersistRoutes(mux, svc, t.TempDir(), store)
	return mux, admin.ID, user.ID
}

// TestNotifyPersistList: GET /api/notifications/persist returns a (possibly
// empty) list without requiring auth (read-only history badge).
func TestNotifyPersistList(t *testing.T) {
	mux, _, _ := newNotifyPersistMux(t)
	rec := doNotifyJSON(t, mux, "GET", "/api/notifications/persist?limit=5", "", "")
	if rec.Code != 200 {
		t.Fatalf("persist list = %d, want 200", rec.Code)
	}
	// Body is a JSON array (may be null/[] when empty) — must be valid JSON.
	var out any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("persist list not valid JSON: %v (%s)", err, rec.Body.String())
	}
}

// TestNotifyPruneRequiresAuth: POST /api/notifications/prune is a mutation and
// rejects an unauthenticated caller.
func TestNotifyPruneRequiresAuth(t *testing.T) {
	mux, _, _ := newNotifyPersistMux(t)
	rec := doNotifyJSON(t, mux, "POST", "/api/notifications/prune", "", `{}`)
	if rec.Code != 401 {
		t.Fatalf("unauth prune = %d, want 401", rec.Code)
	}
}

// TestNotifyPruneDefaults: an empty body on the authenticated ADMIN prune path
// falls back to defaults and returns a remaining count.
func TestNotifyPruneDefaults(t *testing.T) {
	mux, adminID, _ := newNotifyPersistMux(t)
	rec := doNotifyJSON(t, mux, "POST", "/api/notifications/prune", adminID, ``)
	if rec.Code != 200 {
		t.Fatalf("prune defaults = %d, want 200", rec.Code)
	}
	var out map[string]int
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := out["remaining"]; !ok {
		t.Errorf("prune response missing remaining: %v", out)
	}
}

// TestNotifyPruneCustom: explicit max_n / max_age_hours are accepted (exercises
// the non-default branch), as an admin.
func TestNotifyPruneCustom(t *testing.T) {
	mux, adminID, _ := newNotifyPersistMux(t)
	rec := doNotifyJSON(t, mux, "POST", "/api/notifications/prune", adminID,
		`{"max_n":10,"max_age_hours":24}`)
	if rec.Code != 200 {
		t.Fatalf("prune custom = %d, want 200", rec.Code)
	}
}

// TestNotifyPruneRejectsNonAdmin: NOTIF-USER-SCOPE-02 — the persisted store is
// box-wide with no per-user Prune, so a non-admin authenticated caller must be
// rejected (they could otherwise wipe every account's notification history,
// including other users' private ones, e.g. a fired reminder's text).
func TestNotifyPruneRejectsNonAdmin(t *testing.T) {
	mux, _, userID := newNotifyPersistMux(t)
	rec := doNotifyJSON(t, mux, "POST", "/api/notifications/prune", userID, `{}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("non-admin prune = %d, want 403", rec.Code)
	}
}

// --- REAL auth middleware: identity is session-derived, not client-supplied ---

// TestNotifyDNDThroughMiddleware proves the DND write's X-User-ID gate is
// satisfied by a validated SESSION, not a forged header: a forged X-User-ID with
// no session is 401, a valid session token is 200.
func TestNotifyDNDThroughMiddleware(t *testing.T) {
	// DND writes are admin-only (DND-SCOPE-01), so this needs a real admin to
	// prove the middleware path still reaches the handler.
	t.Setenv("VULOS_BOOTSTRAP_ADMIN_EMAIL", "admin@ex.com")
	store, err := auth.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	h := auth.NewHandler(store)
	mux := http.NewServeMux()
	registerNotifyExtRoutes(mux, notify.New(), t.TempDir(), store)
	srv := httptest.NewServer(h.Middleware(mux))
	t.Cleanup(srv.Close)

	// Forged X-User-ID, no session ⇒ middleware strips it ⇒ 401.
	resp := bearerReq(t, srv, "POST", "/api/notifications/dnd", "", `{"mode":"total"}`,
		map[string]string{"X-User-ID": "attacker"})
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatalf("forged DND write = %d, want 401", resp.StatusCode)
	}

	// A valid session for an ADMIN ⇒ 200.
	tok := sessionToken(t, store, "google", "nuser", "admin@ex.com")
	resp = bearerReq(t, srv, "POST", "/api/notifications/dnd", tok, `{"mode":"priority"}`, nil)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("admin session DND write = %d, want 200", resp.StatusCode)
	}

	// DND-SCOPE-01: a valid session for a NON-admin second profile ⇒ 403. DND is
	// box-wide state, so this would otherwise let any account silence every
	// other account's notifications — including the owner's — on a server whose
	// whole job is to page you about backups, security events and mail.
	guestTok := sessionToken(t, store, "google", "guest", "guest@ex.com")
	resp = bearerReq(t, srv, "POST", "/api/notifications/dnd", guestTok, `{"mode":"total"}`, nil)
	defer resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatalf("non-admin session DND write = %d, want 403", resp.StatusCode)
	}
	// And the box must still be in the admin's chosen mode, not the guest's.
	if got := notifyDNDMode(t, srv, tok); got != "priority" {
		t.Fatalf("a non-admin was refused but the box-wide DND mode became %q", got)
	}
}

// notifyDNDMode reads the box's current DND mode through the public GET route.
func notifyDNDMode(t *testing.T, srv *httptest.Server, tok string) string {
	t.Helper()
	// Authenticated: the status route sits behind the session middleware like
	// every non-public path, so an anonymous read returns the 401 body and
	// decodes to an empty mode rather than the box's real state.
	resp := bearerReq(t, srv, "GET", "/api/notifications/dnd", tok, "", nil)
	defer resp.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode DND status: %v", err)
	}
	mode, _ := out["mode"].(string)
	return mode
}

// A nil auth store must DENY, not panic. GetProfile dereferences the store, so
// the first version of the DND admin gate crashed the handler on any box wired
// without one — and a handler that panics on a request every authenticated
// caller can make is a denial of service, not a gate.
func TestNotifyDNDNilAuthStoreDeniesRatherThanPanics(t *testing.T) {
	mux := http.NewServeMux()
	registerNotifyExtRoutes(mux, notify.New(), t.TempDir(), nil)

	rec := doNotifyJSON(t, mux, "POST", "/api/notifications/dnd", "anyone", `{"mode":"total"}`)
	if rec.Code != 403 {
		t.Fatalf("nil auth store: got %d, want 403", rec.Code)
	}
}
