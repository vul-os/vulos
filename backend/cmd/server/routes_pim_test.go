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
//
// owner is the box owner the broker-mode gate checks against (IDOR-PIM-01).
// It defaults to "u1", the caller every test below uses, so the existing cases
// keep exercising the allowed path; the owner-gate tests pass it explicitly to
// make the caller a non-owner.
func pimTestMux(base string, broker map[string]string, owner ...string) *http.ServeMux {
	boxOwner := "u1"
	if len(owner) > 0 {
		boxOwner = owner[0]
	}
	mux := http.NewServeMux()
	registerPIMRoutes(mux, base, broker, func() string { return boxOwner })
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

func TestPIMProxy_AllowlistRejectsPrefixConfusion(t *testing.T) {
	f := newFakeLilmail(t)
	mux := pimTestMux(f.srv.URL, nil)

	// Paths that merely START with the allowed word but are NOT the calendar/
	// or contacts/ subtree must 404 — an attacker must not reach e.g.
	// /v1/calendar-secrets by prefixing an allowed name.
	for _, p := range []string{
		"/api/pim/calendarfoo", "/api/pim/calendar-secrets",
		"/api/pim/contactsX", "/api/pim/contacts-export/all",
		"/api/pim/CALENDAR/events", // case-sensitive: not the allowed subtree
	} {
		req := httptest.NewRequest("GET", p, nil)
		req.Header.Set("X-User-ID", "u1")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s status = %d, want 404 (allowlist prefix confusion)", p, rec.Code)
		}
	}
	if f.lastPath != "" {
		t.Errorf("a non-PIM prefix-confusion request reached lilmail (%q)", f.lastPath)
	}
}

func TestPIMProxy_ForwardsWriteMethods(t *testing.T) {
	// PUT/PATCH/DELETE on an allowed subtree must proxy through unchanged (full
	// CRUD, not read-only), with the rewritten /v1 path.
	for _, m := range []string{"PUT", "PATCH", "DELETE"} {
		f := newFakeLilmail(t)
		mux := pimTestMux(f.srv.URL, nil)
		req := httptest.NewRequest(m, "/api/pim/contacts/u1", nil)
		req.Header.Set("X-User-ID", "u1")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("%s status = %d, want 200", m, rec.Code)
		}
		if f.lastMethod != m || f.lastPath != "/v1/contacts/u1" {
			t.Errorf("%s proxied as %s %q, want %s /v1/contacts/u1", m, f.lastMethod, f.lastPath, m)
		}
	}
}

func TestPIMProxy_UnreachableMailReturns502(t *testing.T) {
	// A lilmail that parses as a valid base but is DOWN must degrade to 502 (the
	// widget's honest "unavailable" state), never a hang or a 200.
	f := newFakeLilmail(t)
	base := f.srv.URL
	f.srv.Close() // kill lilmail so the proxy's dial is refused
	mux := pimTestMux(base, nil)

	req := httptest.NewRequest("GET", "/api/pim/calendar/events", nil)
	req.Header.Set("X-User-ID", "u1")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502 for an unreachable mail service", rec.Code)
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

// --- IDOR-PIM-01: broker mode is owner-only --------------------------------
//
// In broker mode the X-Vulos-Mail-* credential is the box's single
// environment-configured mail account — the owner's — and routes_pim.go deletes
// X-User-ID before proxying, so lilmail answers about the OWNER's calendar and
// address book whoever asks. Vulos is multi-profile, so without this gate a
// second account could read and (PUT/PATCH/DELETE proxy through) WRITE the
// owner's PIM data.

func TestPIMProxy_BrokerMode_DeniesNonOwner(t *testing.T) {
	f := newFakeLilmail(t)
	broker := map[string]string{"X-Vulos-Mail-Secret": "s3cr3t", "X-Vulos-Mail-Email": "ada@example.com"}
	// Box owner is u1; the caller below is u2, a second profile on the same box.
	mux := pimTestMux(f.srv.URL, broker, "u1")

	req := httptest.NewRequest("GET", "/api/pim/calendar/events?from=2026-01-01", nil)
	req.Header.Set("X-User-ID", "u2")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("non-owner reached the owner's brokered mailbox: got status=%d, want 403", rec.Code)
	}
	// The proxy must not have been reached at all — a 403 that still spent the
	// credential downstream would have already exposed the owner's calendar to
	// the upstream call (and its logs).
	if f.lastPath != "" {
		t.Fatalf("non-owner was denied but the request still proxied upstream (%q)", f.lastPath)
	}
}

func TestPIMProxy_BrokerMode_AllowsOwner(t *testing.T) {
	f := newFakeLilmail(t)
	broker := map[string]string{"X-Vulos-Mail-Secret": "s3cr3t", "X-Vulos-Mail-Email": "ada@example.com"}
	mux := pimTestMux(f.srv.URL, broker, "u1")

	req := httptest.NewRequest("GET", "/api/pim/calendar/events?from=2026-01-01", nil)
	req.Header.Set("X-User-ID", "u1")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("box owner was blocked from their own mailbox: got status=%d, want 200", rec.Code)
	}
}

func TestPIMProxy_BrokerMode_FailsClosedWithNoResolvableOwner(t *testing.T) {
	f := newFakeLilmail(t)
	broker := map[string]string{"X-Vulos-Mail-Secret": "s3cr3t"}

	for _, tc := range []struct {
		name    string
		ownerID func() string
	}{
		{"nil resolver", nil},
		{"owner unresolved (no admin user yet)", func() string { return "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mux := http.NewServeMux()
			registerPIMRoutes(mux, f.srv.URL, broker, tc.ownerID)

			req := httptest.NewRequest("GET", "/api/pim/calendar/events", nil)
			req.Header.Set("X-User-ID", "u1")
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)

			if rec.Code != http.StatusForbidden {
				t.Fatalf("unresolvable owner did not fail closed: got status=%d, want 403", rec.Code)
			}
		})
	}
}

// Session-cookie mode (no broker headers) has no box-wide credential to
// protect: the downstream credential is the caller's OWN forwarded cookie, so
// every authenticated profile must still get through and lilmail does the
// scoping. This is the case the owner gate must NOT break.
func TestPIMProxy_SessionMode_AllowsAnyAuthenticatedProfile(t *testing.T) {
	f := newFakeLilmail(t)
	mux := pimTestMux(f.srv.URL, nil, "u1")

	req := httptest.NewRequest("GET", "/api/pim/calendar/events", nil)
	req.Header.Set("X-User-ID", "u2") // not the owner
	req.Header.Set("Cookie", "vc_session=u2-cookie")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("session-cookie mode wrongly gated a non-owner: got status=%d, want 200", rec.Code)
	}
}
