package ddos

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestIPRateLimiter_AllowsUnderLimit(t *testing.T) {
	policies := []Policy{
		{RouteGlob: "/api/auth/login", WindowSeconds: 60, MaxRequests: 5},
		{RouteGlob: "/**", WindowSeconds: 60, MaxRequests: 120},
	}
	l := NewIPRateLimiter(policies)

	for i := 0; i < 5; i++ {
		r := httptest.NewRequest("POST", "/api/auth/login", nil)
		r.RemoteAddr = "10.0.0.1:1234"
		if !l.Allow(r) {
			t.Fatalf("request %d: expected allow got deny", i)
		}
	}
}

func TestIPRateLimiter_BlocksOverLimit(t *testing.T) {
	policies := []Policy{
		{RouteGlob: "/api/auth/login", WindowSeconds: 60, MaxRequests: 3},
		{RouteGlob: "/**", WindowSeconds: 60, MaxRequests: 120},
	}
	l := NewIPRateLimiter(policies)

	for i := 0; i < 3; i++ {
		r := httptest.NewRequest("POST", "/api/auth/login", nil)
		r.RemoteAddr = "10.0.0.2:5678"
		l.Allow(r)
	}
	r := httptest.NewRequest("POST", "/api/auth/login", nil)
	r.RemoteAddr = "10.0.0.2:5678"
	if l.Allow(r) {
		t.Fatal("4th request should have been denied")
	}
}

func TestIPRateLimiter_DifferentIPsAreIndependent(t *testing.T) {
	policies := []Policy{
		{RouteGlob: "/**", WindowSeconds: 60, MaxRequests: 2},
	}
	l := NewIPRateLimiter(policies)

	for i := 0; i < 2; i++ {
		r := httptest.NewRequest("GET", "/foo", nil)
		r.RemoteAddr = "1.1.1.1:1"
		l.Allow(r)
	}
	// ip2 should still be allowed
	r2 := httptest.NewRequest("GET", "/foo", nil)
	r2.RemoteAddr = "2.2.2.2:2"
	if !l.Allow(r2) {
		t.Fatal("second IP should not be blocked by first IP's usage")
	}
}

func TestMatchPolicy_ExactAndGlob(t *testing.T) {
	policies := DefaultPolicies
	p := matchPolicy(policies, "/api/auth/login")
	if p.MaxRequests != 5 {
		t.Fatalf("want login policy max=5 got %d", p.MaxRequests)
	}

	p2 := matchPolicy(policies, "/api/auth/signup")
	if p2.MaxRequests != 5 {
		t.Fatalf("want signup policy max=5 got %d", p2.MaxRequests)
	}
}

// TestMatchPolicy_OrgAndRelayUsage verifies the expensive session/PoP routes get
// their own tighter buckets (not the generic /api/* policy).
func TestMatchPolicy_OrgAndRelayUsage(t *testing.T) {
	policies := DefaultPolicies

	// /api/relay/usage → its dedicated 60/min policy, NOT the generic 300/min.
	if p := matchPolicy(policies, "/api/relay/usage"); p.MaxRequests != 60 || p.RouteGlob != "/api/relay/usage" {
		t.Errorf("relay/usage policy = %+v, want dedicated 60/min", p)
	}
	// /api/org/* (members) → 120/min org policy, NOT 300/min generic.
	if p := matchPolicy(policies, "/api/org/members"); p.MaxRequests != 120 || p.RouteGlob != "/api/org/*" {
		t.Errorf("org policy = %+v, want /api/org/* 120/min", p)
	}
	if p := matchPolicy(policies, "/api/billing/summary"); p.MaxRequests != 120 {
		t.Errorf("billing/summary policy = %+v, want 120/min", p)
	}
	// A different /api/ route still gets the generic 300/min bucket.
	if p := matchPolicy(policies, "/api/gpu/catalog"); p.MaxRequests != 300 {
		t.Errorf("generic /api policy = %+v, want 300/min", p)
	}
}

// TestIPRateLimit_RelayUsageEnforced429 drives the middleware end-to-end and
// confirms the dedicated relay-usage bucket returns 429 once exhausted.
func TestIPRateLimit_RelayUsageEnforced429(t *testing.T) {
	limiter := NewIPRateLimiter([]Policy{
		{RouteGlob: "/api/relay/usage", WindowSeconds: 60, MaxRequests: 3},
		{RouteGlob: "/api/*", WindowSeconds: 60, MaxRequests: 1000},
		{RouteGlob: "/**", WindowSeconds: 60, MaxRequests: 1000},
	})
	mw := IPRateLimit(limiter)
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))

	send := func() int {
		r := httptest.NewRequest("POST", "/api/relay/usage", nil)
		r.RemoteAddr = "9.9.9.9:1"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec.Code
	}
	for i := 0; i < 3; i++ {
		if code := send(); code != http.StatusOK {
			t.Fatalf("request %d: code %d, want 200", i, code)
		}
	}
	if code := send(); code != http.StatusTooManyRequests {
		t.Fatalf("over-limit request: code %d, want 429", code)
	}
}

// Peek must be READ-ONLY: consulting the limiter (as captcha difficulty scoring
// does) must never spend a token from the real budget. Without this, merely
// issuing a captcha challenge double-charged the limiter and could deny the
// client's actual request early.
func TestIPRateLimiter_PeekDoesNotConsume(t *testing.T) {
	policies := []Policy{
		{RouteGlob: "/api/auth/login", WindowSeconds: 60, MaxRequests: 3},
		{RouteGlob: "/**", WindowSeconds: 60, MaxRequests: 120},
	}
	l := NewIPRateLimiter(policies)
	mk := func() *http.Request {
		r := httptest.NewRequest("POST", "/api/auth/login", nil)
		r.RemoteAddr = "10.0.0.42:9000"
		return r
	}

	// A flurry of Peeks must not record anything.
	for i := 0; i < 50; i++ {
		if !l.Peek(mk()) {
			t.Fatalf("peek %d: should be allowed (nothing recorded yet)", i)
		}
	}
	// The full budget must still be available to real Allow calls.
	for i := 0; i < 3; i++ {
		if !l.Allow(mk()) {
			t.Fatalf("Allow %d should succeed — peeks must not have consumed budget", i)
		}
	}
	if l.Allow(mk()) {
		t.Fatal("4th Allow should be denied (budget of 3 exhausted by Allow, not by Peek)")
	}
	// Now at the limit, Peek reflects that (read-only).
	if l.Peek(mk()) {
		t.Fatal("Peek should report denied once the real budget is exhausted")
	}
}
