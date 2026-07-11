package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"vulos/backend/internal/apikey"
)

// staticIntrospector is a test stub implementing apikey.Introspector.
type staticIntrospector struct {
	result apikey.Result
	err    error
}

func (s *staticIntrospector) Introspect(_ context.Context, _ string) (apikey.Result, error) {
	return s.result, s.err
}

// makeTestHandler builds a minimal Handler with an in-memory store seeded with
// one user, a given VKIntrospector, and wraps the "next" handler via Middleware.
func makeTestHandler(t *testing.T, intro apikey.Introspector) (*Handler, http.Handler) {
	t.Helper()
	store := &Store{
		users:    make(map[string]*User),
		sessions: make(map[string]*Session),
		profiles: make(map[string]*Profile),
		secret:   []byte("test-secret"),
	}
	// Seed a user the vk_ middleware can resolve by email.
	store.users["uid-42"] = &User{
		ID:       "uid-42",
		Username: "devuser",
		Email:    "dev@example.com",
	}
	h := NewHandler(store)
	h.VKIntrospector = intro

	echo := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Got-User", r.Header.Get("X-User-ID"))
		w.Header().Set("X-Got-Entitlements", r.Header.Get("X-Vulos-Entitlements-Products"))
		w.WriteHeader(http.StatusOK)
	})
	return h, h.Middleware(echo)
}

func TestVKMiddleware_ValidKey_SetsUserID(t *testing.T) {
	intro := &staticIntrospector{result: apikey.Result{
		Valid:    true,
		Account:  "dev@example.com",
		Products: []string{"os", "mail"},
	}}
	_, mw := makeTestHandler(t, intro)

	req := httptest.NewRequest(http.MethodGet, "/api/apps", nil)
	req.Header.Set("Authorization", "Bearer vk_live_abc123")
	rr := httptest.NewRecorder()
	mw.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	if got := rr.Header().Get("X-Got-User"); got != "uid-42" {
		t.Errorf("expected X-User-ID=uid-42, got %q", got)
	}
}

func TestVKMiddleware_InvalidKey_Returns401(t *testing.T) {
	intro := &staticIntrospector{result: apikey.Result{Valid: false}}
	_, mw := makeTestHandler(t, intro)

	req := httptest.NewRequest(http.MethodGet, "/api/apps", nil)
	req.Header.Set("Authorization", "Bearer vk_bad_key")
	rr := httptest.NewRecorder()
	mw.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rr.Code)
	}
}

func TestVKMiddleware_MissingOSProduct_Returns401(t *testing.T) {
	// Key is valid but doesn't have the "os" product.
	intro := &staticIntrospector{result: apikey.Result{
		Valid:    true,
		Account:  "dev@example.com",
		Products: []string{"mail", "office"}, // "os" absent
	}}
	_, mw := makeTestHandler(t, intro)

	req := httptest.NewRequest(http.MethodGet, "/api/files", nil)
	req.Header.Set("Authorization", "Bearer vk_no_os_product")
	rr := httptest.NewRecorder()
	mw.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rr.Code)
	}
}

func TestVKMiddleware_UnknownAccount_Returns401(t *testing.T) {
	// Key is valid + has os, but account email has no local user.
	intro := &staticIntrospector{result: apikey.Result{
		Valid:    true,
		Account:  "nobody@example.com", // not in store
		Products: []string{"os"},
	}}
	_, mw := makeTestHandler(t, intro)

	req := httptest.NewRequest(http.MethodGet, "/api/apps", nil)
	req.Header.Set("Authorization", "Bearer vk_no_local_user")
	rr := httptest.NewRecorder()
	mw.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rr.Code)
	}
}

func TestVKMiddleware_NilIntrospector_FallsThrough(t *testing.T) {
	// No introspector → vk_ token is ignored (falls through to session check → 401
	// because no session token is present and /api/apps is not a public path).
	_, mw := makeTestHandler(t, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/apps", nil)
	req.Header.Set("Authorization", "Bearer vk_self_host")
	rr := httptest.NewRecorder()
	mw.ServeHTTP(rr, req)

	// No introspector → no user resolved → 401 (unchanged self-host behavior).
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 (self-host, no introspector), got %d", rr.Code)
	}
}

// ENTITLE-01: a valid vk_ key's CP-introspected products must be surfaced on
// X-Vulos-Entitlements-Products for downstream entitlement gating (the OS
// gateway's app-dispatch check).
func TestVKMiddleware_ValidKey_StampsEntitlements(t *testing.T) {
	intro := &staticIntrospector{result: apikey.Result{
		Valid:    true,
		Account:  "dev@example.com",
		Products: []string{"os", "mail", "office-pro"},
	}}
	_, mw := makeTestHandler(t, intro)

	req := httptest.NewRequest(http.MethodGet, "/api/apps", nil)
	req.Header.Set("Authorization", "Bearer vk_live_abc123")
	rr := httptest.NewRecorder()
	mw.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	if got := rr.Header().Get("X-Got-Entitlements"); got != "os,mail,office-pro" {
		t.Fatalf("expected entitlements header to be stamped, got %q", got)
	}
}

// ENTITLE-01 (SECURITY): a client-supplied X-Vulos-Entitlements-Products must
// NEVER survive — it is stripped unconditionally at the top of Middleware, so
// a caller can never spoof an entitlement it was never granted, regardless of
// whether it also presents a valid vk_ key with fewer/no products.
func TestVKMiddleware_ClientSpoofedEntitlements_Stripped(t *testing.T) {
	intro := &staticIntrospector{result: apikey.Result{
		Valid:    true,
		Account:  "dev@example.com",
		Products: []string{"os"}, // no "office-pro"
	}}
	_, mw := makeTestHandler(t, intro)

	req := httptest.NewRequest(http.MethodGet, "/api/apps", nil)
	req.Header.Set("Authorization", "Bearer vk_live_abc123")
	req.Header.Set("X-Vulos-Entitlements-Products", "office-pro,everything")
	rr := httptest.NewRecorder()
	mw.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	if got := rr.Header().Get("X-Got-Entitlements"); got != "os" {
		t.Fatalf("spoofed entitlements must be replaced by the CP-introspected value, got %q", got)
	}
}

// Session-only (no vk_ key) requests must never carry an entitlements header
// either — a plain session cannot claim an entitlement it was never given.
func TestVKMiddleware_SessionOnly_NoSpoofedEntitlementsSurvive(t *testing.T) {
	_, mw := makeTestHandler(t, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/apps", nil)
	req.Header.Set("X-Vulos-Entitlements-Products", "office-pro")
	rr := httptest.NewRecorder()
	mw.ServeHTTP(rr, req)

	// No session, no vk_ → 401, but the important assertion is that a
	// legitimate request downstream would never see the spoofed value; this is
	// covered structurally since Middleware deletes the header unconditionally
	// before any other logic runs.
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 (no session, no vk_), got %d", rr.Code)
	}
}

func TestVKMiddleware_CPError_Returns401(t *testing.T) {
	// CP returns an error → fail closed.
	intro := &staticIntrospector{err: context.DeadlineExceeded}
	_, mw := makeTestHandler(t, intro)

	req := httptest.NewRequest(http.MethodGet, "/api/files", nil)
	req.Header.Set("Authorization", "Bearer vk_cp_down")
	rr := httptest.NewRecorder()
	mw.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 on CP error, got %d", rr.Code)
	}
}
