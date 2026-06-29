package apikey

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// mockCP spins up an httptest server implementing the introspection contract and
// counts how many times it was called (to prove caching).
func mockCP(t *testing.T, fn func(req introspectRequest) Result) (*httptest.Server, *int32) {
	t.Helper()
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/keys/introspect" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		atomic.AddInt32(&calls, 1)
		var req introspectRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(fn(req))
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func TestIntrospect_ValidKey(t *testing.T) {
	srv, _ := mockCP(t, func(req introspectRequest) Result {
		return Result{Valid: true, Account: "alice@vulos.org", Scopes: []string{"files.read"}, Products: []string{ProductOS, "mail"}}
	})
	intro := NewIntrospectorWithClient(Config{BaseURL: srv.URL}, srv.Client())

	res, err := intro.Introspect(context.Background(), "vk_live_abc")
	if err != nil {
		t.Fatalf("introspect: %v", err)
	}
	if !res.Valid || res.Account != "alice@vulos.org" {
		t.Fatalf("unexpected result: %+v", res)
	}
	if !res.HasProduct(ProductOS) {
		t.Fatal("expected os product")
	}
	if res.HasProduct("talk") {
		t.Fatal("did not expect talk product")
	}
	if !res.HasScope("files.read") {
		t.Fatal("expected files.read scope")
	}
}

func TestIntrospect_InvalidKey(t *testing.T) {
	srv, _ := mockCP(t, func(req introspectRequest) Result { return Result{Valid: false} })
	intro := NewIntrospectorWithClient(Config{BaseURL: srv.URL}, srv.Client())

	res, err := intro.Introspect(context.Background(), "vk_bogus")
	if err != nil {
		t.Fatalf("introspect: %v", err)
	}
	if res.Valid {
		t.Fatal("expected invalid result")
	}
}

func TestIntrospect_CachesResults(t *testing.T) {
	srv, calls := mockCP(t, func(req introspectRequest) Result {
		return Result{Valid: true, Account: "bob@vulos.org", Products: []string{ProductOS}}
	})
	intro := NewIntrospectorWithClient(Config{BaseURL: srv.URL}, srv.Client())

	for i := 0; i < 5; i++ {
		if _, err := intro.Introspect(context.Background(), "vk_same"); err != nil {
			t.Fatalf("introspect %d: %v", i, err)
		}
	}
	if got := atomic.LoadInt32(calls); got != 1 {
		t.Fatalf("expected 1 CP call (cached), got %d", got)
	}
}

func TestIntrospect_CacheExpires(t *testing.T) {
	srv, calls := mockCP(t, func(req introspectRequest) Result {
		return Result{Valid: true, Account: "carol@vulos.org", Products: []string{ProductOS}}
	})
	intro := NewIntrospectorWithClient(Config{BaseURL: srv.URL}, srv.Client())

	// Controllable clock.
	now := time.Now()
	intro.now = func() time.Time { return now }

	if _, err := intro.Introspect(context.Background(), "vk_exp"); err != nil {
		t.Fatal(err)
	}
	// Advance past the TTL → next call must hit the CP again.
	now = now.Add(cacheTTL + time.Second)
	if _, err := intro.Introspect(context.Background(), "vk_exp"); err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt32(calls); got != 2 {
		t.Fatalf("expected 2 CP calls after expiry, got %d", got)
	}
}

func TestIntrospect_Non200IsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	intro := NewIntrospectorWithClient(Config{BaseURL: srv.URL}, srv.Client())

	if _, err := intro.Introspect(context.Background(), "vk_x"); err == nil {
		t.Fatal("expected error on non-200")
	}
}

func TestIntrospect_SendsServiceToken(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get(HeaderRelayAuth)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(Result{Valid: true, Products: []string{ProductOS}})
	}))
	t.Cleanup(srv.Close)
	intro := NewIntrospectorWithClient(Config{BaseURL: srv.URL, Token: "svc-secret"}, srv.Client())

	if _, err := intro.Introspect(context.Background(), "vk_y"); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "svc-secret" {
		t.Fatalf("expected service token header, got %q", gotAuth)
	}
}

func TestConfig_Enabled(t *testing.T) {
	if (Config{}).Enabled() {
		t.Fatal("empty config should be disabled")
	}
	if !(Config{BaseURL: "https://cp"}).Enabled() {
		t.Fatal("config with base URL should be enabled")
	}
	if NewIntrospector(Config{}) != nil {
		t.Fatal("NewIntrospector should return nil when disabled")
	}
}

// TestNewIntrospector_HTTPSEnforced verifies the M3 fix: NewIntrospector
// returns nil (disabled) when VULOS_CP_BASE_URL uses http:// instead of
// https://, to prevent leaking X-Relay-Auth over plaintext.
func TestNewIntrospector_HTTPSEnforced(t *testing.T) {
	t.Setenv(EnvCPAllowInsecure, "")

	// http:// base → disabled (M3).
	if NewIntrospector(Config{BaseURL: "http://cp.vulos.org"}) != nil {
		t.Fatal("NewIntrospector with http:// base must return nil (M3: HTTPS enforced)")
	}

	// https:// base → not disabled.
	if NewIntrospector(Config{BaseURL: "https://cp.vulos.org"}) == nil {
		t.Fatal("NewIntrospector with https:// base must not return nil")
	}
}

// TestNewIntrospector_InsecureEscapeHatch verifies that VULOS_CP_ALLOW_INSECURE=1
// allows http:// for local dev.
func TestNewIntrospector_InsecureEscapeHatch(t *testing.T) {
	t.Setenv(EnvCPAllowInsecure, "1")
	// With insecure flag, http:// is accepted.
	if NewIntrospector(Config{BaseURL: "http://localhost:8080"}) == nil {
		t.Fatal("NewIntrospector with http:// and VULOS_CP_ALLOW_INSECURE=1 must not return nil")
	}
}

func TestProductOS_MissingProductRejects(t *testing.T) {
	// Key valid but doesn't carry the "os" product.
	srv, _ := mockCP(t, func(req introspectRequest) Result {
		return Result{Valid: true, Account: "dev@vulos.org", Products: []string{"mail", "office"}}
	})
	intro := NewIntrospectorWithClient(Config{BaseURL: srv.URL}, srv.Client())

	res, err := intro.Introspect(context.Background(), "vk_no_os")
	if err != nil {
		t.Fatalf("introspect: %v", err)
	}
	if res.HasProduct(ProductOS) {
		t.Fatal("must not have os product — callers must reject this")
	}
}
