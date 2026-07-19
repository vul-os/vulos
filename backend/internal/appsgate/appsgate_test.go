package appsgate

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestParse_EnabledValues(t *testing.T) {
	for _, v := range []string{"", "on", "ON", "1", "true", "TRUE", "yes", " on "} {
		if !parse(v) {
			t.Errorf("parse(%q) = false, want true (surface should be enabled)", v)
		}
	}
}

func TestParse_DisabledValues(t *testing.T) {
	for _, v := range []string{"off", "OFF", "0", "false", "no", " off "} {
		if parse(v) {
			t.Errorf("parse(%q) = true, want false (surface should be disabled)", v)
		}
	}
}

// TestParse_UnrecognisedFailsClosed is the important one: a typo must not
// silently leave the surface exposed.
func TestParse_UnrecognisedFailsClosed(t *testing.T) {
	for _, v := range []string{"offf", "Off-please", "disabled", "enable", "maybe", "2"} {
		if parse(v) {
			t.Errorf("parse(%q) = true, want false — unrecognised values must fail closed", v)
		}
	}
}

func TestIsGated(t *testing.T) {
	gated := []string{"/api/apps", "/api/apps/", "/api/apps/launch", "/api/apps/proxy/abc", "/mcp", "/mcp/x"}
	for _, p := range gated {
		if !isGated(p) {
			t.Errorf("isGated(%q) = false, want true", p)
		}
	}
	// Must not over-match adjacent paths.
	notGated := []string{"/api/appsomething", "/api/aiapps", "/api/appstore", "/mcpx", "/api", "/"}
	for _, p := range notGated {
		if isGated(p) {
			t.Errorf("isGated(%q) = true, want false — over-matched an unrelated path", p)
		}
	}
}

func newProbe(t *testing.T) http.Handler {
	t.Helper()
	return Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("reached"))
	}))
}

// TestMiddleware_OffBlocksTheSurface proves VULOS_APPS=off actually stops
// requests reaching the apps handlers — the behaviour the docs promise.
func TestMiddleware_OffBlocksTheSurface(t *testing.T) {
	t.Setenv("VULOS_APPS", "off")
	h := newProbe(t)

	for _, p := range []string{"/api/apps", "/api/apps/launch", "/mcp"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: status = %d, want 404 (surface must be disabled)", p, rec.Code)
		}
		if rec.Body.String() == "reached" {
			t.Errorf("%s: request reached the downstream handler despite VULOS_APPS=off", p)
		}
	}
}

func TestMiddleware_OffLeavesOtherRoutesAlone(t *testing.T) {
	t.Setenv("VULOS_APPS", "off")
	h := newProbe(t)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/files", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "reached" {
		t.Errorf("unrelated route was blocked: status=%d body=%q", rec.Code, rec.Body.String())
	}
}

func TestMiddleware_DefaultOnAllowsTheSurface(t *testing.T) {
	t.Setenv("VULOS_APPS", "")
	h := newProbe(t)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/apps/launch", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "reached" {
		t.Errorf("default (unset) must leave the surface enabled: status=%d body=%q", rec.Code, rec.Body.String())
	}
}

func TestMiddleware_UnrecognisedValueBlocks(t *testing.T) {
	t.Setenv("VULOS_APPS", "offf")
	h := newProbe(t)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/apps", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("unrecognised VULOS_APPS must fail closed: status = %d, want 404", rec.Code)
	}
}
