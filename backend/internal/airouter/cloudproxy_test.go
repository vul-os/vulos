package airouter

// cloudproxy_test.go — AIROT-07 tests
//
// Covers:
//   - cloud mode sends device cert and receives streamed response
//   - 429 triggers back-off with retry (up to maxRetries)
//   - 503 triggers back-off with retry
//   - env override redirects to local stub (VULOS_AI_PROXY_URL)
//   - CertProvider refresh is called on retry
//   - UsageReporter receives a record after success
//   - cert acquisition failure returns error without hitting the proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Helper: cert provider implementations for tests
// ---------------------------------------------------------------------------

type fixedCertProvider struct {
	cert []byte
	err  error
}

func (f *fixedCertProvider) DeviceCert(_ context.Context) ([]byte, error) {
	return f.cert, f.err
}

// countingCertProvider counts how many times DeviceCert is called.
type countingCertProvider struct {
	cert  []byte
	calls atomic.Int64
}

func (c *countingCertProvider) DeviceCert(_ context.Context) ([]byte, error) {
	c.calls.Add(1)
	return c.cert, nil
}

// ---------------------------------------------------------------------------
// Helper: usage reporter
// ---------------------------------------------------------------------------

type capturingReporter struct {
	records []UsageRecord
	done    chan struct{}
}

func newCapturingReporter() *capturingReporter {
	return &capturingReporter{done: make(chan struct{}, 16)}
}

func (r *capturingReporter) ReportUsage(_ context.Context, u UsageRecord) {
	r.records = append(r.records, u)
	r.done <- struct{}{}
}

// ---------------------------------------------------------------------------
// Helper: build a zero-delay CloudProxy for fast tests
// ---------------------------------------------------------------------------

func fastProxy(t *testing.T, certProv CertProvider, reporter UsageReporter) *CloudProxy {
	t.Helper()
	p := newCloudProxy(&http.Client{Timeout: 5 * time.Second}, certProv, reporter)
	// Use zero delays so tests don't sleep.
	p.cfg.BaseDelay = 0
	p.cfg.MaxDelay = 0
	return p
}

// ---------------------------------------------------------------------------
// Test: ProxyChat sends device cert and returns streamed response
// ---------------------------------------------------------------------------

func TestCloudProxy_ProxyChat_SendsCertAndStreams(t *testing.T) {
	var gotAuth string
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"proxied\"}}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer stub.Close()

	os.Setenv("VULOS_AI_PROXY_URL", stub.URL)
	defer os.Unsetenv("VULOS_AI_PROXY_URL")

	prov := &fixedCertProvider{cert: []byte("test-cert-pem")}
	proxy := fastProxy(t, prov, nil)

	rc, err := proxy.ProxyChat(context.Background(), ChatRequest{
		Model:    "gpt-4o",
		Messages: []Message{{Role: "user", Content: "hello"}},
	})
	if err != nil {
		t.Fatalf("ProxyChat: %v", err)
	}
	content, err := DrainSSE(rc)
	if err != nil {
		t.Fatalf("DrainSSE: %v", err)
	}
	if content != "proxied" {
		t.Errorf("content: got %q, want %q", content, "proxied")
	}
	if !strings.HasPrefix(gotAuth, "VulosDevice ") {
		t.Errorf("Authorization header: got %q, want VulosDevice prefix", gotAuth)
	}
}

// ---------------------------------------------------------------------------
// Test: 429 triggers back-off retry and eventually succeeds
// ---------------------------------------------------------------------------

func TestCloudProxy_429_BackOffRetry(t *testing.T) {
	attempts := 0
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 3 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		// Third attempt succeeds.
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer stub.Close()

	os.Setenv("VULOS_AI_PROXY_URL", stub.URL)
	defer os.Unsetenv("VULOS_AI_PROXY_URL")

	prov := &fixedCertProvider{cert: []byte("cert")}
	proxy := fastProxy(t, prov, nil)

	rc, err := proxy.ProxyChat(context.Background(), ChatRequest{
		Model:    "gpt-4o",
		Messages: []Message{{Role: "user", Content: "retry test"}},
	})
	if err != nil {
		t.Fatalf("ProxyChat: %v", err)
	}
	rc.Close()
	if attempts != 3 {
		t.Errorf("expected 3 attempts, got %d", attempts)
	}
}

// ---------------------------------------------------------------------------
// Test: 503 triggers back-off retry
// ---------------------------------------------------------------------------

func TestCloudProxy_503_BackOffRetry(t *testing.T) {
	attempts := 0
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"recovered\"}}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer stub.Close()

	os.Setenv("VULOS_AI_PROXY_URL", stub.URL)
	defer os.Unsetenv("VULOS_AI_PROXY_URL")

	prov := &fixedCertProvider{cert: []byte("cert")}
	proxy := fastProxy(t, prov, nil)

	rc, err := proxy.ProxyChat(context.Background(), ChatRequest{
		Model:    "any",
		Messages: []Message{{Role: "user", Content: "503 test"}},
	})
	if err != nil {
		t.Fatalf("ProxyChat: %v", err)
	}
	rc.Close()
	if attempts < 2 {
		t.Errorf("expected at least 2 attempts, got %d", attempts)
	}
}

// ---------------------------------------------------------------------------
// Test: max retries exceeded returns error
// ---------------------------------------------------------------------------

func TestCloudProxy_MaxRetriesExceeded(t *testing.T) {
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer stub.Close()

	os.Setenv("VULOS_AI_PROXY_URL", stub.URL)
	defer os.Unsetenv("VULOS_AI_PROXY_URL")

	prov := &fixedCertProvider{cert: []byte("cert")}
	proxy := fastProxy(t, prov, nil)
	proxy.cfg.MaxRetries = 2

	_, err := proxy.ProxyChat(context.Background(), ChatRequest{
		Model:    "any",
		Messages: []Message{{Role: "user", Content: "exhaust retries"}},
	})
	if err == nil {
		t.Fatal("expected error after max retries")
	}
	if !strings.Contains(err.Error(), "max retries") {
		t.Errorf("error should mention max retries: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Test: CertProvider is called on each retry attempt
// ---------------------------------------------------------------------------

func TestCloudProxy_CertRefreshedOnRetry(t *testing.T) {
	attempts := 0
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 2 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"cert-refreshed\"}}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer stub.Close()

	os.Setenv("VULOS_AI_PROXY_URL", stub.URL)
	defer os.Unsetenv("VULOS_AI_PROXY_URL")

	prov := &countingCertProvider{cert: []byte("refreshable-cert")}
	proxy := fastProxy(t, prov, nil)

	rc, err := proxy.ProxyChat(context.Background(), ChatRequest{
		Model:    "any",
		Messages: []Message{{Role: "user", Content: "refresh cert"}},
	})
	if err != nil {
		t.Fatalf("ProxyChat: %v", err)
	}
	rc.Close()
	// Initial call + 1 refresh on retry = at least 2 calls.
	calls := prov.calls.Load()
	if calls < 2 {
		t.Errorf("expected cert provider called at least twice (initial + refresh), got %d", calls)
	}
}

// ---------------------------------------------------------------------------
// Test: cert acquisition failure prevents request
// ---------------------------------------------------------------------------

func TestCloudProxy_CertAcquireFailure(t *testing.T) {
	called := false
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer stub.Close()

	os.Setenv("VULOS_AI_PROXY_URL", stub.URL)
	defer os.Unsetenv("VULOS_AI_PROXY_URL")

	prov := &fixedCertProvider{err: fmt.Errorf("keychain locked")}
	proxy := fastProxy(t, prov, nil)

	_, err := proxy.ProxyChat(context.Background(), ChatRequest{
		Model:    "any",
		Messages: []Message{{Role: "user", Content: "no cert"}},
	})
	if err == nil {
		t.Fatal("expected error when cert provider fails")
	}
	if !strings.Contains(err.Error(), "device cert") {
		t.Errorf("error should mention device cert: %v", err)
	}
	if called {
		t.Error("proxy stub should not be called when cert acquisition fails")
	}
}

// ---------------------------------------------------------------------------
// Test: usage reporter receives record after success
// ---------------------------------------------------------------------------

func TestCloudProxy_UsageReporterFired(t *testing.T) {
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"usage\"}}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer stub.Close()

	os.Setenv("VULOS_AI_PROXY_URL", stub.URL)
	defer os.Unsetenv("VULOS_AI_PROXY_URL")

	rep := newCapturingReporter()
	prov := &fixedCertProvider{cert: []byte("cert")}
	proxy := fastProxy(t, prov, rep)

	rc, err := proxy.ProxyChat(context.Background(), ChatRequest{
		Model:    "gpt-4o",
		Messages: []Message{{Role: "user", Content: "report usage"}},
	})
	if err != nil {
		t.Fatalf("ProxyChat: %v", err)
	}
	rc.Close()

	// Wait for the async goroutine to fire.
	select {
	case <-rep.done:
	case <-time.After(2 * time.Second):
		t.Fatal("UsageReporter not called within 2s")
	}

	if len(rep.records) != 1 {
		t.Fatalf("expected 1 usage record, got %d", len(rep.records))
	}
	rec := rep.records[0]
	if rec.Endpoint != "chat" {
		t.Errorf("endpoint: got %q, want %q", rec.Endpoint, "chat")
	}
	if rec.Model != "gpt-4o" {
		t.Errorf("model: got %q, want %q", rec.Model, "gpt-4o")
	}
	if rec.StatusCode != http.StatusOK {
		t.Errorf("status: got %d, want 200", rec.StatusCode)
	}
	if rec.DurationMs < 0 {
		t.Errorf("duration should be non-negative, got %d", rec.DurationMs)
	}
}

// ---------------------------------------------------------------------------
// Test: VULOS_AI_PROXY_URL env override redirects to stub
// ---------------------------------------------------------------------------

func TestCloudProxy_EnvOverride(t *testing.T) {
	hit := false
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = true
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"overridden\"}}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer stub.Close()

	os.Setenv("VULOS_AI_PROXY_URL", stub.URL)
	defer os.Unsetenv("VULOS_AI_PROXY_URL")

	prov := &fixedCertProvider{cert: []byte("cert")}
	proxy := fastProxy(t, prov, nil)

	rc, err := proxy.ProxyChat(context.Background(), ChatRequest{
		Model:    "any",
		Messages: []Message{{Role: "user", Content: "env override"}},
	})
	if err != nil {
		t.Fatalf("ProxyChat: %v", err)
	}
	rc.Close()
	if !hit {
		t.Error("env-overridden stub was not called")
	}
}

// ---------------------------------------------------------------------------
// Test: ProxyEmbed uses VULOS_AI_EMBED_URL and sends device cert
// ---------------------------------------------------------------------------

func TestCloudProxy_ProxyEmbed_SendsCert(t *testing.T) {
	var gotAuth string
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		json.NewEncoder(w).Encode(map[string]any{
			"embedding": []float32{0.1, 0.2, 0.3},
		})
	}))
	defer stub.Close()

	os.Setenv("VULOS_AI_EMBED_URL", stub.URL)
	defer os.Unsetenv("VULOS_AI_EMBED_URL")

	prov := &fixedCertProvider{cert: []byte("embed-cert")}
	proxy := fastProxy(t, prov, nil)

	rc, err := proxy.ProxyEmbed(context.Background(), "text-embedding-3-small", "hello")
	if err != nil {
		t.Fatalf("ProxyEmbed: %v", err)
	}
	vec, err := parseEmbedResponse(rc)
	rc.Close()
	if err != nil {
		t.Fatalf("parseEmbedResponse: %v", err)
	}
	if len(vec) != 3 {
		t.Errorf("embedding length: got %d, want 3", len(vec))
	}
	if !strings.HasPrefix(gotAuth, "VulosDevice ") {
		t.Errorf("Authorization header: got %q, want VulosDevice prefix", gotAuth)
	}
}

// ---------------------------------------------------------------------------
// Test: Router.routeCloud (end-to-end via Router) uses CloudProxy
// ---------------------------------------------------------------------------

func TestRouter_RouteCloud_UsesCloudProxy(t *testing.T) {
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"via-proxy\"}}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer stub.Close()

	os.Setenv("VULOS_AI_PROXY_URL", stub.URL)
	defer os.Unsetenv("VULOS_AI_PROXY_URL")

	s := newTestStore(t)
	_ = s.SetConfig(Config{Mode: ModeCloud, ActiveModel: "cloud-model"})

	r := NewRouter(s)
	r.CertProv = &fixedCertProvider{cert: []byte("router-cert")}

	stream, err := r.Route(context.Background(), ChatRequest{
		Model:    "cloud-model",
		Messages: []Message{{Role: "user", Content: "e2e cloud"}},
	})
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	content, err := DrainSSE(stream)
	if err != nil {
		t.Fatalf("DrainSSE: %v", err)
	}
	if content != "via-proxy" {
		t.Errorf("content: got %q, want %q", content, "via-proxy")
	}
}

// ---------------------------------------------------------------------------
// Test: Router.DeviceCert (legacy path) still works
// ---------------------------------------------------------------------------

func TestRouter_DeviceCert_LegacyPath(t *testing.T) {
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"legacy\"}}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer stub.Close()

	os.Setenv("VULOS_AI_PROXY_URL", stub.URL)
	defer os.Unsetenv("VULOS_AI_PROXY_URL")

	s := newTestStore(t)
	_ = s.SetConfig(Config{Mode: ModeCloud})

	r := NewRouter(s)
	r.DeviceCert = []byte("legacy-cert")

	stream, err := r.Route(context.Background(), ChatRequest{
		Model:    "any",
		Messages: []Message{{Role: "user", Content: "legacy cert test"}},
	})
	if err != nil {
		t.Fatalf("Route (legacy cert): %v", err)
	}
	content, err := DrainSSE(stream)
	if err != nil {
		t.Fatalf("DrainSSE: %v", err)
	}
	if content != "legacy" {
		t.Errorf("content: got %q, want %q", content, "legacy")
	}
}
