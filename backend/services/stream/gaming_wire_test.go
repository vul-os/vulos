package stream

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestRegisterGamingHandlers_MountsRoutes verifies the gaming HTTP surface is
// actually reachable once wired (previously it was orphaned — never mounted in
// cmd/server). The capability route must respond 200 with a JSON body carrying
// the hardware_encode flag the UI reads; the owner-gated routes must reject an
// unauthenticated caller rather than 404 (proving they are mounted).
func TestRegisterGamingHandlers_MountsRoutes(t *testing.T) {
	mux := http.NewServeMux()
	adminOnly := func(r *http.Request) bool { return r.Header.Get("X-User-ID") == "admin-user" }
	pool := NewPool()
	t.Cleanup(pool.StopAll)
	NewGamingManager(pool).RegisterGamingHandlers(mux, adminOnly, nil)

	// capability — public read, deterministic (reads gpu.Detect(), headless-safe).
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/stream/gaming/capability", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("capability: status=%d want 200", rec.Code)
	}
	var cap GamingCapability
	if err := json.Unmarshal(rec.Body.Bytes(), &cap); err != nil {
		t.Fatalf("capability body not GamingCapability JSON: %v (%s)", err, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "hardware_encode") {
		t.Fatalf("capability body missing hardware_encode: %s", rec.Body.String())
	}

	// active — owner-gated; unauthenticated (no X-User-ID) must 401, NOT 404.
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/stream/gaming/active", nil))
	if rec.Code == http.StatusNotFound {
		t.Fatalf("active route not mounted (404)")
	}
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("active without auth: status=%d want 401", rec.Code)
	}

	// start — owner-gated; unauthenticated must 401, NOT 404.
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/stream/gaming/start", strings.NewReader(`{}`)))
	if rec.Code == http.StatusNotFound {
		t.Fatalf("start route not mounted (404)")
	}
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("start without auth: status=%d want 401", rec.Code)
	}
}

// TestRegisterGamingHandlers_StartIsAdminGated is the regression test for the
// RCE this handler used to expose: POST /api/stream/gaming/start launches an
// arbitrary caller-supplied host Command/Args unsandboxed as the vulos server
// process (see the doc comment on RegisterGamingHandlers). Before the fix,
// any authenticated non-admin profile on the box could reach it. This test
// proves (a) a non-admin authenticated caller is rejected 403 BEFORE any
// launch is attempted, (b) an admin caller is NOT rejected by the gate (it
// proceeds past the 403 check — the actual launch then fails in this test
// environment for unrelated reasons, e.g. no Xvfb binary, which is fine: we
// are only asserting the authorization decision, not the launch mechanics),
// and (c) a nil isAdmin fails closed rather than defaulting open.
func TestRegisterGamingHandlers_StartIsAdminGated(t *testing.T) {
	body := `{"command":"/bin/sh","args":["-c","id > /tmp/pwned"]}`

	// STOP anything the admin case actually launches.
	//
	// The comment above says the launch "fails in this test environment for
	// unrelated reasons, e.g. no Xvfb binary". That is true on a developer's
	// machine and FALSE on a CI runner, which ships Xvfb — so there the launch
	// gets far enough to start it, and nothing here ever stopped it. A leaked
	// child outlives its own package and joins every package that runs after it,
	// which is how an orphaned Xvfb ends up in a job summary hundreds of
	// packages later.
	//
	// The assertions below are about the authorization decision and are
	// unaffected; this only cleans up after them.
	pool := NewPool()
	t.Cleanup(pool.StopAll)

	t.Run("non-admin authenticated caller is rejected before launch", func(t *testing.T) {
		mux := http.NewServeMux()
		NewGamingManager(pool).RegisterGamingHandlers(mux, func(r *http.Request) bool { return false }, nil)
		req := httptest.NewRequest(http.MethodPost, "/api/stream/gaming/start", strings.NewReader(body))
		req.Header.Set("X-User-ID", "non-admin-user")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("SEC REGRESSION: non-admin POST /api/stream/gaming/start got status=%d, want 403 — "+
				"a non-admin session can launch an arbitrary host command as the server process", rec.Code)
		}
	})

	t.Run("nil isAdmin fails closed", func(t *testing.T) {
		mux := http.NewServeMux()
		NewGamingManager(pool).RegisterGamingHandlers(mux, nil, nil)
		req := httptest.NewRequest(http.MethodPost, "/api/stream/gaming/start", strings.NewReader(body))
		req.Header.Set("X-User-ID", "some-user")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("SEC REGRESSION: nil isAdmin gate did not fail closed — status=%d, want 403", rec.Code)
		}
	})

	t.Run("admin caller passes the gate", func(t *testing.T) {
		mux := http.NewServeMux()
		NewGamingManager(pool).RegisterGamingHandlers(mux, func(r *http.Request) bool { return true }, nil)
		req := httptest.NewRequest(http.MethodPost, "/api/stream/gaming/start", strings.NewReader(body))
		req.Header.Set("X-User-ID", "admin-user")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code == http.StatusForbidden {
			t.Fatalf("admin caller was rejected by the admin gate: status=%d", rec.Code)
		}
	})

	t.Run("execDisabled kill-switch blocks start regardless of role", func(t *testing.T) {
		mux := http.NewServeMux()
		NewGamingManager(pool).RegisterGamingHandlers(mux, func(r *http.Request) bool { return true },
			func() bool { return true })
		req := httptest.NewRequest(http.MethodPost, "/api/stream/gaming/start", strings.NewReader(body))
		req.Header.Set("X-User-ID", "admin-user")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("SEC REGRESSION: execDisabled()=true did not block gaming/start — status=%d, want 503", rec.Code)
		}
	})
}

// TestSession_GamingExposedInJSON verifies the Session serialises the gaming
// flag (json:"gaming"). The launch path relies on this: the frontend reads
// data.gaming from the launch-app response to decide whether to engage gaming
// input behaviour (pointer-lock, split channels) — only for real games.
func TestSession_GamingExposedInJSON(t *testing.T) {
	on, err := json.Marshal(&Session{ID: "g1", Gaming: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(on), `"gaming":true`) {
		t.Fatalf("gaming=true not exposed in JSON: %s", on)
	}
	off, err := json.Marshal(&Session{ID: "d1", Gaming: false})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(off), `"gaming":false`) {
		t.Fatalf("gaming=false not exposed in JSON: %s", off)
	}
}
