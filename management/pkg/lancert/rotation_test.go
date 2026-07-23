// rotation_test.go — SECRET-ROTATE-01: lancert accepts the current OR previous
// CP_SHARED_SECRET during a rotation overlap window.
package lancert

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestCheckAuth_AcceptsCurrentAndPrevious verifies the dual-key rotation gate:
// X-Device-Auth matching EITHER the current or previous secret is accepted, a
// value matching neither is 401, and an unconfigured handler is 503.
func TestCheckAuth_AcceptsCurrentAndPrevious(t *testing.T) {
	const cur = "lancert-new"
	const prev = "lancert-old"
	h := NewHandlerWithRotation(nil, cur, prev)

	check := func(authVal string) int {
		req := httptest.NewRequest(http.MethodGet, "/api/lancert/cert?box_id=b", nil)
		if authVal != "" {
			req.Header.Set("X-Device-Auth", authVal)
		}
		rec := httptest.NewRecorder()
		h.checkAuth(rec, req) // returns bool; we assert on status code written
		return rec.Code
	}

	if code := check(cur); code != http.StatusOK {
		t.Errorf("current secret: want no error (200 default), got %d", code)
	}
	if code := check(prev); code != http.StatusOK {
		t.Errorf("previous secret must be accepted during overlap, got %d", code)
	}
	if code := check("wrong"); code != http.StatusUnauthorized {
		t.Errorf("wrong secret: want 401, got %d", code)
	}
	if code := check(""); code != http.StatusUnauthorized {
		t.Errorf("missing header: want 401, got %d", code)
	}
}

// TestCheckAuth_NoSecretsFailClosed verifies that with neither secret set the
// gate fails closed (503), never silently accepting anonymous traffic.
func TestCheckAuth_NoSecretsFailClosed(t *testing.T) {
	h := NewHandlerWithRotation(nil, "", "")
	req := httptest.NewRequest(http.MethodGet, "/api/lancert/cert?box_id=b", nil)
	req.Header.Set("X-Device-Auth", "anything")
	rec := httptest.NewRecorder()
	if ok := h.checkAuth(rec, req); ok {
		t.Error("checkAuth must reject when no secret is configured")
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("want 503 when unconfigured, got %d", rec.Code)
	}
}
