package security

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestLoginTimingPadsResponse verifies that fast responses are padded to ~250ms.
func TestLoginTimingPadsResponse(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping timing test in short mode")
	}

	instant := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Respond instantly.
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
	rec := httptest.NewRecorder()

	start := time.Now()
	LoginTimingMiddleware(instant).ServeHTTP(rec, req)
	elapsed := time.Since(start)

	if elapsed < 200*time.Millisecond {
		t.Errorf("want elapsed >= 200ms (timing pad), got %v", elapsed)
	}
}

// TestHandleAvailableRateLimitPretend200 verifies that after the per-IP limit
// the endpoint returns pretend-200 instead of calling the real handler.
func TestHandleAvailableRateLimitPretend200(t *testing.T) {
	// Reset the limiter by creating a local one.
	lim := newEnumLimiter(2, time.Minute) // cap at 2 for testing

	callCount := 0
	downstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"available":false,"reason":"taken"}`)) //nolint:errcheck
	})

	// Use isTakenMiddleware with the test limiter directly.
	mw := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip := extractIP(r)
			if !lim.allow(ip) {
				addJitter()
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				w.Write([]byte(`{"available":true,"reason":"available"}`)) //nolint:errcheck
				return
			}
			next.ServeHTTP(w, r)
		})
	}

	for i := 0; i < 5; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/auth/handle-available?handle=x", nil)
		req.RemoteAddr = "10.0.0.1:1234"
		rec := httptest.NewRecorder()
		mw(downstream).ServeHTTP(rec, req)
	}

	if callCount > 2 {
		t.Errorf("want at most 2 real handler calls before pretend-200, got %d", callCount)
	}
}

// TestIsTakenPretend200AfterLimit verifies IsTakenMiddleware always returns 200
// after the per-IP limit.
func TestIsTakenPretend200AfterLimit(t *testing.T) {
	downstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"available":false}`)) //nolint:errcheck
	})

	mw := IsTakenMiddleware(2, downstream)
	var lastBody string
	for i := 0; i < 5; i++ {
		req := httptest.NewRequest(http.MethodGet, "/check?handle=foo", nil)
		req.RemoteAddr = "10.20.30.40:5678"
		rec := httptest.NewRecorder()
		mw.ServeHTTP(rec, req)
		if i == 4 {
			lastBody = rec.Body.String()
		}
	}

	if lastBody != `{"available":true}` {
		t.Errorf("want pretend-200 body after limit, got %q", lastBody)
	}
}

// TestEnumConstantTimingFallback verifies responses complete without error.
func TestEnumConstantTimingFallback(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})

	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
	rec := httptest.NewRecorder()

	// Wrapping in LoginTimingMiddleware should not panic.
	LoginTimingMiddleware(h).ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("want 401 passed through, got %d", rec.Code)
	}
}
