package location

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newTestMux wires the real handlers onto a mux, as main.go does, so these
// tests exercise the actual HTTP surface (routing + auth gate + serialization)
// rather than only the Service layer.
func newTestMux() *http.ServeMux {
	mux := http.NewServeMux()
	RegisterHandlers(mux, New(nil))
	return mux
}

// TestHandlers_RejectUnauthenticated is the security-critical case: an empty
// X-User-ID (no authenticated user) must be denied 401, fail-closed, on both
// POST and GET — never silently scoped to some default/blank user.
func TestHandlers_RejectUnauthenticated(t *testing.T) {
	mux := newTestMux()

	for _, tc := range []struct {
		name   string
		method string
		body   string
	}{
		{"POST no user", "POST", `{"lat":1,"lng":2}`},
		{"GET no user", "GET", ``},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, "/api/location", strings.NewReader(tc.body))
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("empty X-User-ID: got %d, want 401", rec.Code)
			}
		})
	}
}

// TestHandlers_RoundTrip stores a fix for a user and reads it back, asserting
// zero-valued coordinates (equator / prime meridian) survive serialization —
// the ,omitempty bug the review caught would drop lat=0 / lng=0 from the JSON.
func TestHandlers_RoundTrip(t *testing.T) {
	mux := newTestMux()
	const user = "u-alice"

	post := httptest.NewRequest("POST", "/api/location", strings.NewReader(`{"lat":0,"lng":0,"accuracy":5}`))
	post.Header.Set("X-User-ID", user)
	postRec := httptest.NewRecorder()
	mux.ServeHTTP(postRec, post)
	if postRec.Code != http.StatusOK {
		t.Fatalf("POST: got %d, want 200 (body=%s)", postRec.Code, postRec.Body.String())
	}

	get := httptest.NewRequest("GET", "/api/location", nil)
	get.Header.Set("X-User-ID", user)
	getRec := httptest.NewRecorder()
	mux.ServeHTTP(getRec, get)
	if getRec.Code != http.StatusOK {
		t.Fatalf("GET: got %d, want 200", getRec.Code)
	}

	var resp map[string]any
	if err := json.Unmarshal(getRec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("GET body not JSON: %v", err)
	}
	if resp["found"] != true {
		t.Fatalf("expected found=true, got %v (body=%s)", resp["found"], getRec.Body.String())
	}
	// The whole point: lat/lng must be PRESENT even though both are 0.
	if _, ok := resp["lat"]; !ok {
		t.Errorf("lat=0 was dropped from the response (omitempty regression)")
	}
	if _, ok := resp["lng"]; !ok {
		t.Errorf("lng=0 was dropped from the response (omitempty regression)")
	}
}

// TestHandlers_UserIsolation confirms user A can never read user B's fix via
// the HTTP surface.
func TestHandlers_UserIsolation(t *testing.T) {
	mux := newTestMux()

	post := httptest.NewRequest("POST", "/api/location", strings.NewReader(`{"lat":40.7,"lng":-74}`))
	post.Header.Set("X-User-ID", "u-alice")
	mux.ServeHTTP(httptest.NewRecorder(), post)

	get := httptest.NewRequest("GET", "/api/location", nil)
	get.Header.Set("X-User-ID", "u-bob")
	getRec := httptest.NewRecorder()
	mux.ServeHTTP(getRec, get)

	var resp map[string]any
	_ = json.Unmarshal(getRec.Body.Bytes(), &resp)
	if resp["found"] == true {
		t.Errorf("user isolation breach: bob read alice's position")
	}
}
