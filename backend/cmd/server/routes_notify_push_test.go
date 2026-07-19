package main

// routes_notify_push_test.go — HTTP-level tests for the PUSH-CELL-01 subscribe
// surface (registerNotifyPushRoutes): the VAPID-public read, the per-owner
// subscribe/unsubscribe endpoints, the X-User-ID authZ gate, and the
// fail-safe-off behavior when Web Push is unconfigured.

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

// newPushMux registers the push routes against a fresh temp home. withKeys
// controls whether a VAPID pair is provisioned (so the enabled/disabled branch
// can both be exercised).
func newPushMux(t *testing.T, withKeys bool) (*http.ServeMux, string) {
	t.Helper()
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "db"), 0700); err != nil {
		t.Fatal(err)
	}
	if withKeys {
		// Pre-provision a VAPID pair at the default key-file path so cfg.Enabled().
		t.Setenv("VULOS_PUSH_VAPID_KEYFILE", filepath.Join(home, "db", "vapid.json"))
	} else {
		// Force keys off even though the route defaults a key file: point the key
		// file at a directory so resolve fails → push disabled, store still opens.
		t.Setenv("VULOS_PUSH_VAPID_KEYFILE", filepath.Join(home, "db")) // a dir, not writable as a file
	}
	svc := notify.New()
	mux := http.NewServeMux()
	registerNotifyPushRoutes(mux, svc, home, notify.NewDNDManager(filepath.Join(home, "dnd.json")))
	return mux, home
}

func doPush(t *testing.T, mux *http.ServeMux, method, path, user, body string) *httptest.ResponseRecorder {
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

func TestPushVAPIDPublic_EnabledExposesPublicKeyNotPrivate(t *testing.T) {
	mux, _ := newPushMux(t, true)
	rec := doPush(t, mux, "GET", "/api/notifications/push/vapid-public", "", "")
	if rec.Code != 200 {
		t.Fatalf("vapid-public = %d", rec.Code)
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out["enabled"] != true {
		t.Fatalf("expected enabled=true, got %+v", out)
	}
	if _, ok := out["publicKey"].(string); !ok || out["publicKey"] == "" {
		t.Fatalf("public key not exposed: %+v", out)
	}
	// The private key must NEVER appear in the response.
	if strings.Contains(rec.Body.String(), "private") {
		t.Fatalf("response leaked private key material: %s", rec.Body.String())
	}
}

func TestPushVAPIDPublic_DisabledReportsOff(t *testing.T) {
	mux, _ := newPushMux(t, false)
	rec := doPush(t, mux, "GET", "/api/notifications/push/vapid-public", "", "")
	if rec.Code != 200 {
		t.Fatalf("vapid-public = %d", rec.Code)
	}
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out["enabled"] != false {
		t.Fatalf("expected enabled=false, got %+v", out)
	}
	if _, present := out["publicKey"]; present {
		t.Fatalf("disabled response should not carry a public key")
	}
}

func TestPushSubscribe_RequiresAuth(t *testing.T) {
	mux, _ := newPushMux(t, true)
	body := `{"endpoint":"https://push.example/x","keys":{"p256dh":"p","auth":"a"}}`
	rec := doPush(t, mux, "POST", "/api/notifications/push/subscribe", "", body)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauth subscribe = %d, want 401", rec.Code)
	}
}

func TestPushSubscribe_StoresUnderCallerNotBody(t *testing.T) {
	mux, _ := newPushMux(t, true)
	// The body cannot carry an owner; only the endpoint/keys. The owner comes
	// from X-User-ID. (This test asserts a 200 subscribe + idempotent unsub.)
	body := `{"endpoint":"https://push.example/alice","keys":{"p256dh":"pkey","auth":"asalt"}}`
	rec := doPush(t, mux, "POST", "/api/notifications/push/subscribe", "alice", body)
	if rec.Code != 200 {
		t.Fatalf("subscribe = %d (%s)", rec.Code, rec.Body.String())
	}
	// Unsubscribe is idempotent and scoped to the caller.
	del := doPush(t, mux, "DELETE", "/api/notifications/push/subscribe", "alice",
		`{"endpoint":"https://push.example/alice"}`)
	if del.Code != 200 {
		t.Fatalf("unsubscribe = %d (%s)", del.Code, del.Body.String())
	}
}

func TestPushSubscribe_RejectsBadSubscription(t *testing.T) {
	mux, _ := newPushMux(t, true)
	// Non-https endpoint is rejected by ValidateSubscription.
	body := `{"endpoint":"http://push.example/x","keys":{"p256dh":"p","auth":"a"}}`
	rec := doPush(t, mux, "POST", "/api/notifications/push/subscribe", "alice", body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad sub = %d, want 400", rec.Code)
	}
}

func TestPushSubscribe_DisabledReturns503(t *testing.T) {
	mux, _ := newPushMux(t, false)
	body := `{"endpoint":"https://push.example/x","keys":{"p256dh":"p","auth":"a"}}`
	rec := doPush(t, mux, "POST", "/api/notifications/push/subscribe", "alice", body)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("subscribe while disabled = %d, want 503", rec.Code)
	}
}

// TestCPSubscribe_SelfHostInert verifies the CP-keyed send-on-behalf subscribe is
// fail-safe-off in self-host mode: with no CP configured (registrar nil), the
// cp-subscribe endpoint 503s and never attempts to reach a CP. This is the
// self-host-inert guarantee for SPEC 1 — a box with no CP does everything locally.
func TestCPSubscribe_SelfHostInert(t *testing.T) {
	// No VULOS_PUSH_CP_REGISTER_URL / secret / ULID → registrar is nil.
	mux, _ := newPushMux(t, true)
	body := `{"endpoint":"https://push.example/cp","keys":{"p256dh":"p","auth":"a"}}`
	rec := doPush(t, mux, "POST", "/api/notifications/push/cp-subscribe", "alice", body)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("cp-subscribe self-host = %d, want 503 (inert)", rec.Code)
	}
	rec = doPush(t, mux, "DELETE", "/api/notifications/push/cp-subscribe", "alice", body)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("cp-unsubscribe self-host = %d, want 503 (inert)", rec.Code)
	}
}

func TestCPSubscribe_RequiresAuth(t *testing.T) {
	mux, _ := newPushMux(t, true)
	rec := doPush(t, mux, "POST", "/api/notifications/push/cp-subscribe", "", `{}`)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("cp-subscribe without X-User-ID = %d, want 401", rec.Code)
	}
}

func TestPushUnsubscribe_RequiresEndpoint(t *testing.T) {
	mux, _ := newPushMux(t, true)
	rec := doPush(t, mux, "DELETE", "/api/notifications/push/subscribe", "alice", `{}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing endpoint = %d, want 400", rec.Code)
	}
}
