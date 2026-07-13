package main

// Regression tests for the terminal /api/ fallback: an unmatched or
// method-mismatched API call must NEVER be answered by the SPA catch-all with
// 200 text/html (a silent false success for any client that checks only res.ok).

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// apiFallbackMux builds a mux shaped like the real server's: a few concrete API
// routes, the terminal /api/ handler, and the SPA catch-all underneath.
func apiFallbackMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/instances", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, []any{})
	})
	mux.HandleFunc("POST /api/setup/apps", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]bool{"email": true})
	})
	registerAPIFallbackRoutes(mux)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte("<!doctype html><title>vulos</title>")) //nolint:errcheck — test SPA
	})
	return mux
}

func apiFallbackDo(mux *http.ServeMux, method, path string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(method, path, nil))
	return w
}

// TestAPIFallback_UnknownRouteIs404JSON pins the root cause: routes the box does
// not register (here: the CP-only TOTP enroll path, and a DELETE on an instance)
// must be a JSON 404, not the SPA's 200 text/html.
func TestAPIFallback_UnknownRouteIs404JSON(t *testing.T) {
	mux := apiFallbackMux()

	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/api/auth/totp/enroll"},
		{http.MethodPost, "/api/auth/verify-email/resend"},
		{http.MethodDelete, "/api/instances/01ABC"},
		{http.MethodGet, "/api/cgroups/status"},
	} {
		w := apiFallbackDo(mux, tc.method, tc.path)
		if w.Code != http.StatusNotFound {
			t.Fatalf("%s %s: want 404, got %d (body %q)", tc.method, tc.path, w.Code, w.Body.String())
		}
		if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
			t.Fatalf("%s %s: want JSON content-type, got %q", tc.method, tc.path, ct)
		}
		var body struct {
			Error string `json:"error"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body.Error == "" {
			t.Fatalf("%s %s: want a JSON error envelope, got %q (%v)", tc.method, tc.path, w.Body.String(), err)
		}
	}
}

// TestAPIFallback_MethodMismatchIs405 verifies a registered path reached with the
// wrong method returns 405 + Allow, not the SPA page.
func TestAPIFallback_MethodMismatchIs405(t *testing.T) {
	mux := apiFallbackMux()

	w := apiFallbackDo(mux, http.MethodPost, "/api/instances")
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /api/instances: want 405, got %d (body %q)", w.Code, w.Body.String())
	}
	if allow := w.Header().Get("Allow"); allow != http.MethodGet {
		t.Fatalf("POST /api/instances: want Allow: GET, got %q", allow)
	}

	w = apiFallbackDo(mux, http.MethodGet, "/api/setup/apps")
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET /api/setup/apps: want 405, got %d", w.Code)
	}
	if allow := w.Header().Get("Allow"); allow != http.MethodPost {
		t.Fatalf("GET /api/setup/apps: want Allow: POST, got %q", allow)
	}
}

// TestAPIFallback_RealRoutesAndSPAUnaffected verifies the fallback shadows
// nothing: registered API routes still serve, and non-API paths still get the SPA.
func TestAPIFallback_RealRoutesAndSPAUnaffected(t *testing.T) {
	mux := apiFallbackMux()

	if w := apiFallbackDo(mux, http.MethodGet, "/api/instances"); w.Code != http.StatusOK {
		t.Fatalf("GET /api/instances: want 200, got %d", w.Code)
	}

	w := apiFallbackDo(mux, http.MethodGet, "/desktop")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "<!doctype html>") {
		t.Fatalf("GET /desktop: want the SPA page, got %d %q", w.Code, w.Body.String())
	}
}
