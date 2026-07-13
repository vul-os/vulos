package main

// routes_notify_cpkey_test.go — GET /api/mail/push/cp-key, the box's relay of
// the CP's PUBLIC VAPID key.
//
// webPush.js enableCPPush() fetches this path FIRST and returns false if it does
// not come back; the box registered no such route, so the offline "new mail"
// wake path never enabled on any box and the cp-subscribe handler below it was
// dead code. The relay itself (CP fetch, cache, fail-on-error) is covered in
// services/notify; these are the route-level guarantees.

import (
	"encoding/json"
	"net/http"
	"testing"
)

// TestCPKeyRoute_SelfHostReportsDisabled — with no CP configured the box answers
// enabled:false, which is exactly what the client treats as "no send-on-behalf".
// It must be a real JSON 200, not the SPA fallback.
func TestCPKeyRoute_SelfHostReportsDisabled(t *testing.T) {
	mux, _ := newPushMux(t, true)

	rec := doPush(t, mux, "GET", "/api/mail/push/cp-key", "alice", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("cp-key = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}

	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v (body %q)", err, rec.Body.String())
	}
	if out["enabled"] != false {
		t.Fatalf("self-host cp-key = %+v, want enabled:false", out)
	}
	if _, leaked := out["vapid_public"]; leaked {
		t.Fatalf("disabled cp-key must carry no key: %+v", out)
	}
}

func TestCPKeyRoute_RequiresAuth(t *testing.T) {
	mux, _ := newPushMux(t, true)

	rec := doPush(t, mux, "GET", "/api/mail/push/cp-key", "", "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("cp-key without X-User-ID = %d, want 401", rec.Code)
	}
}
