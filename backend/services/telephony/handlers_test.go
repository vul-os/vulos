package telephony

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// teleMux registers the telephony routes on a fresh mux for a service owned by
// ownerID.
func teleMux(ownerID string) *http.ServeMux {
	mux := http.NewServeMux()
	RegisterHandlers(mux, newTeleService(ownerID))
	return mux
}

// teleRoutes enumerates every /api/telephony route (the WS upgrade included).
// Each must be owner-gated and must fail CLOSED.
var teleRoutes = []struct {
	method, path string
}{
	{"GET", "/api/telephony/status"},
	{"GET", "/api/telephony/"},
	{"GET", "/api/telephony/sms/threads"},
	{"GET", "/api/telephony/sms/thread/+15551234567"},
	{"POST", "/api/telephony/sms/send"},
	{"GET", "/api/telephony/calls"},
	{"POST", "/api/telephony/call"},
	{"POST", "/api/telephony/call/hangup"},
	{"POST", "/api/telephony/call/decline"},
	{"POST", "/api/telephony/call/answer"},
	{"GET", "/api/telephony/ws"},
	// Second/throwaway-number (Provider) line — owner-gated exactly like the SIM
	// line. NB: /api/telephony/provider/inbound is intentionally absent here — it
	// is the provider-facing webhook, authenticated by HMAC rather than the owner
	// gate, and is covered by the webhook tests in provider_test.go.
	{"GET", "/api/telephony/virtual/status"},
	{"GET", "/api/telephony/virtual/sms/threads"},
	{"GET", "/api/telephony/virtual/sms/thread/+15551234567"},
	{"POST", "/api/telephony/virtual/sms/send"},
	{"POST", "/api/telephony/virtual/call"},
}

// A non-owner (or an unauthenticated request with no X-User-ID) must be rejected on
// EVERY route, including the WebSocket upgrade — otherwise a second account on a
// multi-user box could read/send the owner's SMS or watch inbound OTPs live.
func TestOwnerGating_FailClosed(t *testing.T) {
	mux := teleMux("owner-1")

	for _, rt := range teleRoutes {
		for _, uid := range []string{"" /* unauthenticated */, "intruder"} {
			req := httptest.NewRequest(rt.method, rt.path, strings.NewReader("{}"))
			if uid != "" {
				req.Header.Set("X-User-ID", uid)
			}
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)

			if rec.Code != http.StatusForbidden {
				t.Errorf("%s %s uid=%q: got %d, want 403", rt.method, rt.path, uid, rec.Code)
			}
			if !strings.Contains(rec.Body.String(), "forbidden") {
				t.Errorf("%s %s uid=%q: body=%q want forbidden", rt.method, rt.path, uid, rec.Body.String())
			}
		}
	}
}

// When the box owner is UNRESOLVED (no admin yet), telephony is denied to everyone —
// even the id that would eventually be owner. The gate must not fail open on "".
func TestOwnerGating_UnresolvedOwnerDeniesAll(t *testing.T) {
	mux := teleMux("") // ownerID resolves to ""

	req := httptest.NewRequest("GET", "/api/telephony/status", nil)
	req.Header.Set("X-User-ID", "owner-1")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("unresolved owner must deny all: got %d, want 403", rec.Code)
	}
}

// The owner passes the gate and reaches the SMS-send handler. With no modem present
// Send fails, and the handler surfaces that as a 200 JSON {"error":…} envelope
// (NOT a 403 and NOT {"ok":true}) — proving the body decoded and Send was invoked.
func TestSMSSend_OwnerReachesSend(t *testing.T) {
	teleNoMmcli(t) // deterministic "no modem"
	mux := teleMux("owner-1")

	req := httptest.NewRequest("POST", "/api/telephony/sms/send",
		strings.NewReader(`{"to":"+15551234567","body":"hi"}`))
	req.Header.Set("X-User-ID", "owner-1")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("owner send: got %d, want 200", rec.Code)
	}
	var env map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("response not JSON: %v (%q)", err, rec.Body.String())
	}
	if _, isErr := env["error"]; !isErr {
		t.Errorf("expected an {\"error\":…} envelope with no modem, got %v", env)
	}
	if _, isOK := env["ok"]; isOK {
		t.Errorf("must not report ok:true when Send failed, got %v", env)
	}
}

// Malformed JSON must be rejected with 400 (not silently treated as an empty send).
func TestSMSSend_InvalidBody(t *testing.T) {
	teleNoMmcli(t)
	mux := teleMux("owner-1")

	req := httptest.NewRequest("POST", "/api/telephony/sms/send", strings.NewReader("not json"))
	req.Header.Set("X-User-ID", "owner-1")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("malformed body: got %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "invalid body") {
		t.Errorf("body=%q want invalid body", rec.Body.String())
	}
}

// The 16 KiB MaxBytesReader cap must fire on an oversized body: the decode errors
// out and the handler returns 400 rather than buffering unbounded attacker input.
func TestSMSSend_MaxBytesReaderCaps(t *testing.T) {
	teleNoMmcli(t)
	mux := teleMux("owner-1")

	huge := `{"to":"+15551234567","body":"` + strings.Repeat("a", 20000) + `"}` // > 16 KiB
	req := httptest.NewRequest("POST", "/api/telephony/sms/send", strings.NewReader(huge))
	req.Header.Set("X-User-ID", "owner-1")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("oversized body must be rejected: got %d, want 400", rec.Code)
	}
}

// The success shape is exactly {"ok":true} with an application/json content type.
// /call/hangup always succeeds (best-effort; Hangup's error is intentionally
// ignored), so it isolates the success envelope from modem availability.
func TestSuccessEnvelopeShape(t *testing.T) {
	teleNoMmcli(t)
	mux := teleMux("owner-1")

	req := httptest.NewRequest("POST", "/api/telephony/call/hangup", nil)
	req.Header.Set("X-User-ID", "owner-1")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("hangup: got %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Errorf("content-type=%q want application/json", ct)
	}
	var env map[string]bool
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("response not JSON: %v", err)
	}
	if len(env) != 1 || !env["ok"] {
		t.Errorf("success envelope = %v, want {\"ok\":true}", env)
	}
}
