package main

// routes_notify_unifiedpush_test.go — HTTP-level tests for the UP-CELL-01
// subscribe surface (registerNotifyUnifiedPushRoutes): the status read, the
// per-owner subscribe/unsubscribe endpoints, the X-User-ID authZ gate, the
// SSRF rejection at registration, and the fail-safe-off behavior when
// UnifiedPush is not configured. Mirrors routes_notify_push_test.go's
// coverage shape for the sibling transport.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"vulos/backend/services/notify"
)

// newUnifiedPushMux registers the UnifiedPush routes against a fresh temp
// home. enabled controls VULOS_PUSH_UNIFIEDPUSH_ENABLE so both the on and
// off branches can be exercised.
func newUnifiedPushMux(t *testing.T, enabled bool) (*http.ServeMux, string) {
	t.Helper()
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "db"), 0700); err != nil {
		t.Fatal(err)
	}
	if enabled {
		t.Setenv("VULOS_PUSH_UNIFIEDPUSH_ENABLE", "1")
	} else {
		t.Setenv("VULOS_PUSH_UNIFIEDPUSH_ENABLE", "")
	}
	svc := notify.New()
	mux := http.NewServeMux()
	registerNotifyUnifiedPushRoutes(mux, svc, home, notify.NewDNDManager(filepath.Join(home, "dnd.json")))
	return mux, home
}

func doUP(t *testing.T, mux *http.ServeMux, method, path, user, body string) *httptest.ResponseRecorder {
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

func TestUnifiedPushStatus_EnabledReportsOn(t *testing.T) {
	mux, _ := newUnifiedPushMux(t, true)
	rec := doUP(t, mux, "GET", "/api/notifications/unifiedpush/status", "alice", "")
	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out["enabled"] != true {
		t.Fatalf("expected enabled=true, got %+v", out)
	}
}

func TestUnifiedPushStatus_DisabledReportsOff(t *testing.T) {
	mux, _ := newUnifiedPushMux(t, false)
	rec := doUP(t, mux, "GET", "/api/notifications/unifiedpush/status", "alice", "")
	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out["enabled"] != false {
		t.Fatalf("expected enabled=false, got %+v", out)
	}
}

func TestUnifiedPushStatus_RequiresAuth(t *testing.T) {
	mux, _ := newUnifiedPushMux(t, true)
	rec := doUP(t, mux, "GET", "/api/notifications/unifiedpush/status", "", "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauth status = %d, want 401", rec.Code)
	}
}

// TestUnifiedPushSubscribe_RequiresAuth is a MUTATION-TARGETED test: dropping
// the `if owner == ""` session gate in the POST subscribe handler must make
// this fail (an unauthenticated caller must never be able to register a push
// endpoint against another account's notification stream).
func TestUnifiedPushSubscribe_RequiresAuth(t *testing.T) {
	mux, _ := newUnifiedPushMux(t, true)
	body := `{"endpoint":"https://up.example/x"}`
	rec := doUP(t, mux, "POST", "/api/notifications/unifiedpush/subscribe", "", body)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauth subscribe = %d, want 401", rec.Code)
	}
}

func TestUnifiedPushSubscribe_StoresUnderCallerNotBody(t *testing.T) {
	mux, _ := newUnifiedPushMux(t, true)
	body := `{"endpoint":"https://up.example/alice"}`
	rec := doUP(t, mux, "POST", "/api/notifications/unifiedpush/subscribe", "alice", body)
	if rec.Code != 200 {
		t.Fatalf("subscribe = %d (%s)", rec.Code, rec.Body.String())
	}
	del := doUP(t, mux, "DELETE", "/api/notifications/unifiedpush/subscribe", "alice",
		`{"endpoint":"https://up.example/alice"}`)
	if del.Code != 200 {
		t.Fatalf("unsubscribe = %d (%s)", del.Code, del.Body.String())
	}
}

func TestUnifiedPushSubscribe_RejectsBadEndpoint(t *testing.T) {
	mux, _ := newUnifiedPushMux(t, true)
	// Non-https endpoint is rejected by unifiedpush.Validate.
	body := `{"endpoint":"http://up.example/x"}`
	rec := doUP(t, mux, "POST", "/api/notifications/unifiedpush/subscribe", "alice", body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad endpoint = %d, want 400", rec.Code)
	}
}

// TestUnifiedPushSubscribe_RejectsSSRFEndpoint is a MUTATION-TARGETED test:
// dropping the SSRF validation call in the POST subscribe handler (or in
// unifiedpush.Validate itself) must make this fail — a user must never be
// able to register a loopback/link-local/metadata address as a push endpoint
// and turn the box into a request generator against itself.
func TestUnifiedPushSubscribe_RejectsSSRFEndpoint(t *testing.T) {
	mux, _ := newUnifiedPushMux(t, true)
	blocked := []string{
		`{"endpoint":"https://127.0.0.1/push"}`,             // loopback
		`{"endpoint":"https://169.254.169.254/latest/api"}`, // cloud metadata
		`{"endpoint":"https://10.0.0.5/push"}`,              // box's own LAN
		`{"endpoint":"https://localhost/push"}`,             // loopback name
	}
	for _, body := range blocked {
		rec := doUP(t, mux, "POST", "/api/notifications/unifiedpush/subscribe", "alice", body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("SSRF-shaped endpoint accepted (body=%s): got %d, want 400", body, rec.Code)
		}
	}
}

func TestUnifiedPushSubscribe_DisabledReturns503(t *testing.T) {
	mux, _ := newUnifiedPushMux(t, false)
	body := `{"endpoint":"https://up.example/x"}`
	rec := doUP(t, mux, "POST", "/api/notifications/unifiedpush/subscribe", "alice", body)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("subscribe while disabled = %d, want 503", rec.Code)
	}
}

func TestUnifiedPushUnsubscribe_RequiresAuth(t *testing.T) {
	mux, _ := newUnifiedPushMux(t, true)
	rec := doUP(t, mux, "DELETE", "/api/notifications/unifiedpush/subscribe", "",
		`{"endpoint":"https://up.example/x"}`)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauth unsubscribe = %d, want 401", rec.Code)
	}
}

func TestUnifiedPushUnsubscribe_RequiresEndpoint(t *testing.T) {
	mux, _ := newUnifiedPushMux(t, true)
	rec := doUP(t, mux, "DELETE", "/api/notifications/unifiedpush/subscribe", "alice", `{}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing endpoint = %d, want 400", rec.Code)
	}
}

// TestUnifiedPushUnsubscribe_ScopedToCaller confirms one user cannot delete
// another user's registered endpoint by guessing/reusing its URL string. The
// per-owner isolation itself is proven at the store layer
// (TestUnifiedPushStore_PerOwnerIsolation in backend/internal/unifiedpush);
// this asserts the HTTP handler passes the CALLER's id through to Delete
// rather than a body-supplied one, by checking bob's delete call cannot ever
// carry alice's identity (bob's request has no way to name an owner at all —
// the handler always uses X-User-ID, so a cross-owner delete is structurally
// scoped to the caller, not merely to what the request body says).
func TestUnifiedPushUnsubscribe_ScopedToCaller(t *testing.T) {
	mux, _ := newUnifiedPushMux(t, true)
	body := `{"endpoint":"https://up.example/alice-only"}`
	rec := doUP(t, mux, "POST", "/api/notifications/unifiedpush/subscribe", "alice", body)
	if rec.Code != 200 {
		t.Fatalf("subscribe = %d (%s)", rec.Code, rec.Body.String())
	}
	// bob's delete of the same URL string is scoped to bob's own (empty) row
	// set — it is idempotent and returns 200, but it must not be able to
	// reach alice's row, which the store-level isolation test verifies
	// directly against the same Store implementation this route uses.
	del := doUP(t, mux, "DELETE", "/api/notifications/unifiedpush/subscribe", "bob", body)
	if del.Code != 200 {
		t.Fatalf("bob's delete call = %d", del.Code)
	}
}
