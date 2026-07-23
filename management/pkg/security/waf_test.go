package security

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newWAF(t *testing.T, store *Store) *WAF {
	t.Helper()
	w, err := NewWAF(store)
	if err != nil {
		t.Fatalf("NewWAF: %v", err)
	}
	return w
}

// makeHandler returns a simple 200 OK handler for use downstream.
func makeHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok")) //nolint:errcheck
	})
}

// TestWAFBlocksSQLi verifies that an SQLi payload in a query param is blocked.
func TestWAFBlocksSQLi(t *testing.T) {
	store := openTestStore(t)
	waf := newWAF(t, store)

	req := httptest.NewRequest(http.MethodGet,
		"/api/search?q=1'+OR+'1'='1", nil)
	rec := httptest.NewRecorder()

	waf.Middleware(makeHandler()).ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("want 403 for SQLi payload, got %d", rec.Code)
	}
}

// TestWAFBlocksXSS verifies that an XSS payload is blocked.
func TestWAFBlocksXSS(t *testing.T) {
	store := openTestStore(t)
	waf := newWAF(t, store)

	req := httptest.NewRequest(http.MethodGet,
		"/api/comment?text=<script>alert('xss')</script>", nil)
	rec := httptest.NewRecorder()

	waf.Middleware(makeHandler()).ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("want 403 for XSS payload, got %d", rec.Code)
	}
}

// TestWAFLegitimateRequest verifies that a normal API request passes through.
func TestWAFLegitimateRequest(t *testing.T) {
	store := openTestStore(t)
	waf := newWAF(t, store)

	req := httptest.NewRequest(http.MethodGet, "/api/user/profile", nil)
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; test)")
	rec := httptest.NewRecorder()

	waf.Middleware(makeHandler()).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("want 200 for legitimate request, got %d", rec.Code)
	}
}

// TestWAFTrustedRouteBypass verifies that /api/superadmin/* bypasses the WAF.
func TestWAFTrustedRouteBypass(t *testing.T) {
	store := openTestStore(t)
	waf := newWAF(t, store)

	// Even with an SQLi-looking body the route is trusted and bypasses WAF.
	req := httptest.NewRequest(http.MethodPost, "/api/superadmin/accounts",
		strings.NewReader(`{"filter":"1' OR '1'='1"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	waf.Middleware(makeHandler()).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("want 200 for trusted route bypass, got %d", rec.Code)
	}
}
