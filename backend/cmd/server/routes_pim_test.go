package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeLilmail records the last request it received so the proxy tests can assert
// on the rewritten path, injected credentials, and forwarded cookie/body.
type fakeLilmail struct {
	srv        *httptest.Server
	lastPath   string
	lastMethod string
	lastHeader http.Header
	lastBody   string
}

func newFakeLilmail(t *testing.T) *fakeLilmail {
	t.Helper()
	f := &fakeLilmail{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		f.lastPath = r.URL.RequestURI()
		f.lastMethod = r.Method
		f.lastHeader = r.Header.Clone()
		f.lastBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"events":[]}`))
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// pimTestMux registers just the PIM routes against a fresh mux (the auth
// middleware, which stamps X-User-ID, is simulated by the tests setting the
// header directly).
func pimTestMux(base string, broker map[string]string) *http.ServeMux {
	mux := http.NewServeMux()
	registerPIMRoutes(mux, base, broker)
	return mux
}

func TestPIMProxy_ForwardsToV1WithBrokeredCreds(t *testing.T) {
	f := newFakeLilmail(t)
	broker := map[string]string{"X-Vulos-Mail-Secret": "s3cr3t", "X-Vulos-Mail-Email": "ada@example.com"}
	mux := pimTestMux(f.srv.URL, broker)

	req := httptest.NewRequest("GET", "/api/pim/calendar/events?from=2026-01-01", nil)
	req.Header.Set("X-User-ID", "u1")
	req.Header.Set("Cookie", "vc_session=abc")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	if f.lastPath != "/v1/calendar/events?from=2026-01-01" {
		t.Errorf("proxied path = %q, want /v1/calendar/events?from=2026-01-01", f.lastPath)
	}
	if got := f.lastHeader.Get("X-Vulos-Mail-Secret"); got != "s3cr3t" {
		t.Errorf("broker secret = %q, want injected s3cr3t", got)
	}
	if got := f.lastHeader.Get("Cookie"); got != "vc_session=abc" {
		t.Errorf("cookie = %q, want forwarded vc_session=abc", got)
	}
	// The OS-internal identity header must not leak downstream.
	if got := f.lastHeader.Get("X-User-ID"); got != "" {
		t.Errorf("X-User-ID leaked downstream = %q, want stripped", got)
	}
}

func TestPIMProxy_StripsClientBrokerHeader(t *testing.T) {
	f := newFakeLilmail(t)
	broker := map[string]string{"X-Vulos-Mail-Secret": "real-secret"}
	mux := pimTestMux(f.srv.URL, broker)

	req := httptest.NewRequest("GET", "/api/pim/contacts/cards", nil)
	req.Header.Set("X-User-ID", "u1")
	// A malicious browser tries to forge the broker credential.
	req.Header.Set("X-Vulos-Mail-Secret", "forged")
	req.Header.Set("X-Vulos-Mail-Email", "attacker@evil.com")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if got := f.lastHeader.Get("X-Vulos-Mail-Secret"); got != "real-secret" {
		t.Errorf("secret = %q, want the box's real-secret (forged value stripped)", got)
	}
	if got := f.lastHeader.Get("X-Vulos-Mail-Email"); got != "" {
		t.Errorf("forged email survived = %q, want stripped", got)
	}
}

func TestPIMProxy_PostBodyForwarded(t *testing.T) {
	f := newFakeLilmail(t)
	mux := pimTestMux(f.srv.URL, nil)

	body := `{"summary":"Standup","start":"2026-01-02T09:00:00Z"}`
	req := httptest.NewRequest("POST", "/api/pim/calendar/events", strings.NewReader(body))
	req.Header.Set("X-User-ID", "u1")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if f.lastMethod != "POST" || f.lastBody != body {
		t.Errorf("method/body = %s %q, want POST %q", f.lastMethod, f.lastBody, body)
	}
}

func TestPIMProxy_RejectsNonPIMPath(t *testing.T) {
	f := newFakeLilmail(t)
	mux := pimTestMux(f.srv.URL, nil)

	// A path outside calendar/contacts (e.g. reading mail bodies) must be 404'd
	// here — this proxy is PIM-only.
	for _, p := range []string{"/api/pim/messages", "/api/pim/drafts", "/api/pim/", "/api/pim/messages/1/send"} {
		req := httptest.NewRequest("GET", p, nil)
		req.Header.Set("X-User-ID", "u1")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s status = %d, want 404", p, rec.Code)
		}
	}
	if f.lastPath != "" {
		t.Errorf("a non-PIM request reached lilmail (%q) — allowlist leaked", f.lastPath)
	}
}

func TestPIMProxy_RequiresAuth(t *testing.T) {
	f := newFakeLilmail(t)
	mux := pimTestMux(f.srv.URL, nil)

	req := httptest.NewRequest("GET", "/api/pim/calendar/events", nil) // no X-User-ID
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
	if f.lastPath != "" {
		t.Errorf("unauthenticated request reached lilmail (%q)", f.lastPath)
	}
}

func TestPIMProxy_UnconfiguredMailBase(t *testing.T) {
	mux := pimTestMux("://bad-url", nil)
	req := httptest.NewRequest("GET", "/api/pim/calendar/events", nil)
	req.Header.Set("X-User-ID", "u1")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503 for misconfigured mail base", rec.Code)
	}
}
