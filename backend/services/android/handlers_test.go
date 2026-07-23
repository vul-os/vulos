package android

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const testOwnerID = "owner-1"

// mkHandlerService builds an owner-gated service + a mux with the android
// routes registered, mirroring how the server wires it in main.
func mkHandlerService(t *testing.T) *http.ServeMux {
	t.Helper()
	s := New(nil, func() string { return testOwnerID })
	mux := http.NewServeMux()
	RegisterHandlers(mux, s)
	return mux
}

// callAndroid issues a request with the given method/path/user id and returns
// the recorder. body may be empty.
func callAndroid(t *testing.T, mux *http.ServeMux, method, path, userID, body string) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, path, bytes.NewReader([]byte(body)))
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	if userID != "" {
		r.Header.Set("X-User-ID", userID)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

// androidRoutes is the full API surface, used to assert uniform owner gating.
var androidRoutes = []struct {
	method, path, body string
}{
	{"GET", "/api/android/status", ""},
	{"POST", "/api/android/start", ""},
	{"POST", "/api/android/stop", ""},
	{"POST", "/api/android/location", `{"lat":1,"lng":2}`},
}

// TestEveryRouteOwnerGatedFailClosed asserts that EVERY route rejects both an
// absent X-User-ID and a non-owner id with 403 — fail-closed, no exceptions.
func TestEveryRouteOwnerGatedFailClosed(t *testing.T) {
	mux := mkHandlerService(t)

	for _, rt := range androidRoutes {
		t.Run(rt.method+" "+rt.path, func(t *testing.T) {
			t.Run("empty user id", func(t *testing.T) {
				w := callAndroid(t, mux, rt.method, rt.path, "", rt.body)
				if w.Code != http.StatusForbidden {
					t.Fatalf("empty X-User-ID: status = %d, want 403", w.Code)
				}
				if !strings.Contains(w.Body.String(), "forbidden") {
					t.Fatalf("empty X-User-ID: body = %q, want forbidden", w.Body.String())
				}
			})
			t.Run("other user id", func(t *testing.T) {
				w := callAndroid(t, mux, rt.method, rt.path, "someone-else", rt.body)
				if w.Code != http.StatusForbidden {
					t.Fatalf("non-owner X-User-ID: status = %d, want 403", w.Code)
				}
			})
		})
	}
}

// TestOwnerGateFailsClosedWithNoOwner verifies that when the box has no
// resolvable owner (ownerID returns ""), even a caller sending that empty id is
// denied — the "" == "" trap must not authorize anyone.
func TestOwnerGateFailsClosedWithNoOwner(t *testing.T) {
	s := New(nil, func() string { return "" })
	mux := http.NewServeMux()
	RegisterHandlers(mux, s)

	for _, rt := range androidRoutes {
		// Send the same empty id the owner resolver returns.
		w := callAndroid(t, mux, rt.method, rt.path, "", rt.body)
		if w.Code != http.StatusForbidden {
			t.Fatalf("%s %s with no owner: status = %d, want 403", rt.method, rt.path, w.Code)
		}
	}
}

// TestStatusRouteHonestUnavailable checks the owner-allowed status route returns
// the honest {"available":false, ...} JSON when the hardware gate is closed,
// rather than erroring or panicking.
func TestStatusRouteHonestUnavailable(t *testing.T) {
	if binderDeviceDetected() || binderModuleLoaded() {
		t.Skip("host has real binder support; unavailable status not reproducible")
	}
	mux := mkHandlerService(t)
	w := callAndroid(t, mux, "GET", "/api/android/status", testOwnerID, "")

	if w.Code != http.StatusOK {
		t.Fatalf("status route: code = %d, want 200", w.Code)
	}
	var st Status
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil {
		t.Fatalf("status route: undecodable body %q: %v", w.Body.String(), err)
	}
	if st.Available {
		t.Errorf("status.Available = true, want false")
	}
	if len(st.Missing) == 0 {
		t.Errorf("status.Missing empty, want prerequisites listed")
	}
}

// TestStartRouteCleanErrorWhenUnavailable asserts the owner-allowed start route
// returns a clean JSON error (not a panic, not a 500 crash) when prerequisites
// are missing.
func TestStartRouteCleanErrorWhenUnavailable(t *testing.T) {
	if binderDeviceDetected() || binderModuleLoaded() {
		t.Skip("host has real binder support; start would attempt a real run")
	}
	mux := mkHandlerService(t)
	w := callAndroid(t, mux, "POST", "/api/android/start", testOwnerID, "")

	if w.Code != http.StatusOK {
		t.Fatalf("start route: code = %d, want 200 with JSON error", w.Code)
	}
	var resp map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("start route: undecodable body %q: %v", w.Body.String(), err)
	}
	if !strings.Contains(resp["error"], "unavailable") {
		t.Fatalf("start route: error = %q, want an 'unavailable' message", resp["error"])
	}
}

// TestStopRouteCleanErrorWhenDockerAbsent forces docker off PATH so Stop takes
// its docker-missing branch deterministically (never touching a real daemon)
// and asserts a clean JSON error is returned to the owner.
func TestStopRouteCleanErrorWhenDockerAbsent(t *testing.T) {
	t.Setenv("PATH", "") // exec.LookPath("docker") now fails → dockerPresent()==false
	mux := mkHandlerService(t)
	w := callAndroid(t, mux, "POST", "/api/android/stop", testOwnerID, "")

	if w.Code != http.StatusOK {
		t.Fatalf("stop route: code = %d, want 200 with JSON error", w.Code)
	}
	var resp map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("stop route: undecodable body %q: %v", w.Body.String(), err)
	}
	if !strings.Contains(resp["error"], "unavailable") && !strings.Contains(resp["error"], "docker") {
		t.Fatalf("stop route: error = %q, want an honest docker-missing message", resp["error"])
	}
}

// TestLocationRouteCleanErrorWhenUnavailable asserts the owner-allowed location
// route returns a clean JSON error (no panic) when the hardware gate is closed,
// even for a well-formed body.
func TestLocationRouteCleanErrorWhenUnavailable(t *testing.T) {
	if binderDeviceDetected() || binderModuleLoaded() {
		t.Skip("host has real binder support; location would attempt real adb")
	}
	mux := mkHandlerService(t)
	w := callAndroid(t, mux, "POST", "/api/android/location", testOwnerID, `{"lat":37.77,"lng":-122.41}`)

	if w.Code != http.StatusOK {
		t.Fatalf("location route: code = %d, want 200 with JSON error", w.Code)
	}
	var resp map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("location route: undecodable body %q: %v", w.Body.String(), err)
	}
	if resp["error"] == "" {
		t.Fatalf("location route: expected a clean error, got %q", w.Body.String())
	}
}

// TestLocationRouteRejectsMalformedBody confirms the owner-allowed location
// route returns 400 (not a panic) on undecodable JSON, before any service call.
func TestLocationRouteRejectsMalformedBody(t *testing.T) {
	mux := mkHandlerService(t)
	w := callAndroid(t, mux, "POST", "/api/android/location", testOwnerID, `{not json`)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("malformed body: code = %d, want 400", w.Code)
	}
}
