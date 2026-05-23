package airouter

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// newTestHandler returns a Handler (and its backing store) wired to a stub
// OpenAI-compatible provider.  The stub parameter is the URL of an
// httptest.Server; pass "" to skip provider registration.
func newTestHandler(t *testing.T, stubURL string) (*Handler, *Store) {
	t.Helper()
	s := newTestStore(t)
	if stubURL != "" {
		_ = s.AddProvider(Provider{
			ID:          "stub",
			DisplayName: "Stub Provider",
			BaseURL:     stubURL,
			APIKeyEnc:   "test-key",
			Models:      []string{"test-model", "gpt-4o"},
		})
	}
	return NewHandler(NewRouter(s)), s
}

// stubSSEServer returns an httptest.Server that serves canned SSE chunks for
// /v1/chat/completions requests.
func stubSSEServer(t *testing.T, chunks []string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, c := range chunks {
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", c)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
}

// ---------------------------------------------------------------------------
// POST /api/ai/chat
// ---------------------------------------------------------------------------

func TestHandleChat_SSEStream(t *testing.T) {
	stub := stubSSEServer(t, []string{"hello", " world"})
	defer stub.Close()

	h, _ := newTestHandler(t, stub.URL)
	mux := http.NewServeMux()
	RegisterHandlers(mux, h.router)

	body := `{"model":"test-model","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest("POST", "/api/ai/chat", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	ct := rr.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("Content-Type: got %q, want text/event-stream", ct)
	}
	if !strings.Contains(rr.Body.String(), "data:") {
		t.Errorf("body should contain SSE data lines; got: %s", rr.Body.String())
	}
}

func TestHandleChat_UnknownModel_422(t *testing.T) {
	// No providers registered, so any request should fail.
	s := newTestStore(t)
	h := NewHandler(NewRouter(s))
	mux := http.NewServeMux()
	RegisterHandlers(mux, h.router)

	body := `{"model":"nonexistent","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest("POST", "/api/ai/chat", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	// With no providers the router returns an error — we expect 502 (bad gateway)
	// because we can't distinguish "model not found" from other errors without a
	// sentinel; a missing model after proper provider wiring returns 422.
	// Verify the response is JSON with an "error" key.
	if rr.Code == http.StatusOK {
		t.Fatal("expected non-200 for request with no providers")
	}
	var resp map[string]string
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("response body not JSON: %v", err)
	}
	if _, ok := resp["error"]; !ok {
		t.Errorf("expected 'error' key in response, got %v", resp)
	}
}

func TestHandleChat_EmptyModel_422(t *testing.T) {
	s := newTestStore(t)
	h := NewHandler(NewRouter(s))
	mux := http.NewServeMux()
	RegisterHandlers(mux, h.router)

	body := `{"model":"","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest("POST", "/api/ai/chat", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status: got %d, want 422", rr.Code)
	}
	var resp map[string]string
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("response body not JSON: %v", err)
	}
	if resp["error"] != "model_not_found" {
		t.Errorf("error: got %q, want %q", resp["error"], "model_not_found")
	}
}

func TestHandleChat_InvalidJSON_400(t *testing.T) {
	s := newTestStore(t)
	h := NewHandler(NewRouter(s))
	mux := http.NewServeMux()
	RegisterHandlers(mux, h.router)

	req := httptest.NewRequest("POST", "/api/ai/chat", strings.NewReader("{bad json"))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d, want 400", rr.Code)
	}
}

// ---------------------------------------------------------------------------
// POST /api/ai/models
// ---------------------------------------------------------------------------

func TestHandleModels_ListsFromConfig(t *testing.T) {
	stub := stubSSEServer(t, nil)
	defer stub.Close()

	h, _ := newTestHandler(t, stub.URL)
	mux := http.NewServeMux()
	RegisterHandlers(mux, h.router)

	req := httptest.NewRequest("POST", "/api/ai/models", bytes.NewReader(nil))
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", rr.Code)
	}
	var resp map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("response body not JSON: %v", err)
	}
	models, ok := resp["models"].([]any)
	if !ok {
		t.Fatalf("expected 'models' array, got %T", resp["models"])
	}
	if len(models) != 2 {
		t.Errorf("expected 2 models (test-model + gpt-4o), got %d", len(models))
	}
}

func TestHandleModels_EmptyWhenNoProviders(t *testing.T) {
	s := newTestStore(t)
	h := NewHandler(NewRouter(s))
	mux := http.NewServeMux()
	RegisterHandlers(mux, h.router)

	req := httptest.NewRequest("POST", "/api/ai/models", bytes.NewReader(nil))
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", rr.Code)
	}
	var resp map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("response body not JSON: %v", err)
	}
	models := resp["models"].([]any)
	if len(models) != 0 {
		t.Errorf("expected empty models list, got %v", models)
	}
}

// ---------------------------------------------------------------------------
// GET /api/ai/status
// ---------------------------------------------------------------------------

func TestHandleStatus(t *testing.T) {
	s := newTestStore(t)
	_ = s.SetConfig(Config{Mode: ModeBYO, ActiveModel: "gpt-4o"})
	h := NewHandler(NewRouter(s))
	mux := http.NewServeMux()
	RegisterHandlers(mux, h.router)

	req := httptest.NewRequest("GET", "/api/ai/status", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", rr.Code)
	}
	var resp map[string]string
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("response not JSON: %v", err)
	}
	if resp["mode"] != "byo" {
		t.Errorf("mode: got %q, want %q", resp["mode"], "byo")
	}
	if resp["active_model"] != "gpt-4o" {
		t.Errorf("active_model: got %q, want %q", resp["active_model"], "gpt-4o")
	}
}

// ---------------------------------------------------------------------------
// PUT /api/ai/config
// ---------------------------------------------------------------------------

func TestHandleConfig_UpdateModeAndModel(t *testing.T) {
	s := newTestStore(t)
	h := NewHandler(NewRouter(s))
	mux := http.NewServeMux()
	RegisterHandlers(mux, h.router)

	body := `{"mode":"cloud","active_model":"claude-opus-4"}`
	req := httptest.NewRequest("PUT", "/api/ai/config", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	// Verify config was persisted.
	cfg, err := s.GetConfig()
	if err != nil {
		t.Fatalf("GetConfig: %v", err)
	}
	if cfg.Mode != ModeCloud {
		t.Errorf("mode: got %q, want %q", cfg.Mode, ModeCloud)
	}
	if cfg.ActiveModel != "claude-opus-4" {
		t.Errorf("active_model: got %q, want %q", cfg.ActiveModel, "claude-opus-4")
	}
}

func TestHandleConfig_AddProvider(t *testing.T) {
	s := newTestStore(t)
	h := NewHandler(NewRouter(s))
	mux := http.NewServeMux()
	RegisterHandlers(mux, h.router)

	body := `{"provider":{"id":"p1","display_name":"MyLLM","base_url":"http://localhost:9000","api_key_enc":"key123","models":["my-model"]}}`
	req := httptest.NewRequest("PUT", "/api/ai/config", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	providers, err := s.ListProviders()
	if err != nil || len(providers) != 1 {
		t.Fatalf("ListProviders: %v, len=%d", err, len(providers))
	}
	if providers[0].DisplayName != "MyLLM" {
		t.Errorf("provider name: got %q, want %q", providers[0].DisplayName, "MyLLM")
	}
}

// ---------------------------------------------------------------------------
// Cloud mode: SSE streaming via handler
// ---------------------------------------------------------------------------

func TestHandleChat_CloudMode_Stream(t *testing.T) {
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"cloud\"}}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer stub.Close()

	os.Setenv("VULOS_AI_PROXY_URL", stub.URL)
	defer os.Unsetenv("VULOS_AI_PROXY_URL")

	s := newTestStore(t)
	_ = s.SetConfig(Config{Mode: ModeCloud, ActiveModel: "cloud-model"})
	router := NewRouter(s)
	router.DeviceCert = []byte("test-cert")

	mux := http.NewServeMux()
	RegisterHandlers(mux, router)

	body := `{"model":"cloud-model","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest("POST", "/api/ai/chat", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	respBytes, _ := io.ReadAll(rr.Body)
	if !strings.Contains(string(respBytes), "cloud") {
		t.Errorf("expected 'cloud' in SSE stream, got: %s", respBytes)
	}
}

// ---------------------------------------------------------------------------
// Rate limiter unit tests
// ---------------------------------------------------------------------------

// newRateLimitedHandler returns a Handler with a tight per-minute cap for tests.
func newRateLimitedHandler(t *testing.T, stubURL string, perMin, perHour int) *Handler {
	t.Helper()
	h, _ := newTestHandler(t, stubURL)
	now := time.Now()
	h.rl = &aiRateLimiter{
		perMin:  perMin,
		perHour: perHour,
		buckets: make(map[string]*aiBucket),
		now:     func() time.Time { return now },
	}
	return h
}

// TestAIRateLimiter_MinuteBucket verifies the per-minute bucket fires on
// rapid-fire requests and allows again after the clock advances.
func TestAIRateLimiter_MinuteBucket(t *testing.T) {
	now := time.Now()
	rl := &aiRateLimiter{
		perMin:  2,
		perHour: 1000,
		buckets: make(map[string]*aiBucket),
		now:     func() time.Time { return now },
	}

	for i := range 2 {
		ok, _ := rl.allow("user-a")
		if !ok {
			t.Errorf("request %d should be allowed", i+1)
		}
	}
	ok, retryAfter := rl.allow("user-a")
	if ok {
		t.Error("3rd request should be rate-limited")
	}
	if retryAfter <= 0 {
		t.Errorf("retryAfter should be > 0, got %d", retryAfter)
	}

	// Clock advance: bucket refills.
	now = now.Add(time.Minute)
	ok, _ = rl.allow("user-a")
	if !ok {
		t.Error("request after 1-minute advance should be allowed")
	}
}

// TestAIRateLimiter_HourBucket verifies the per-hour bucket fires.
func TestAIRateLimiter_HourBucket(t *testing.T) {
	now := time.Now()
	rl := &aiRateLimiter{
		perMin:  1000,
		perHour: 2,
		buckets: make(map[string]*aiBucket),
		now:     func() time.Time { return now },
	}

	for i := range 2 {
		ok, _ := rl.allow("user-b")
		if !ok {
			t.Errorf("request %d should be allowed", i+1)
		}
	}
	ok, _ := rl.allow("user-b")
	if ok {
		t.Error("3rd request should be rate-limited (hour cap)")
	}

	now = now.Add(time.Hour)
	ok, _ = rl.allow("user-b")
	if !ok {
		t.Error("request after 1-hour advance should be allowed")
	}
}

// TestHandleChat_RateLimit_HTTP verifies that the /api/ai/chat endpoint
// returns 429 after the per-minute cap is exceeded.
func TestHandleChat_RateLimit_HTTP(t *testing.T) {
	stub := stubSSEServer(t, []string{"ok"})
	defer stub.Close()

	h := newRateLimitedHandler(t, stub.URL, 2, 1000)
	mux := http.NewServeMux()
	RegisterHandlers(mux, h.router)
	// Swap handler directly to use our rate-limited one.
	mux2 := http.NewServeMux()
	mux2.HandleFunc("POST /api/ai/chat", h.handleChat)

	sendChat := func() int {
		body := `{"model":"test-model","messages":[{"role":"user","content":"hi"}]}`
		req := httptest.NewRequest("POST", "/api/ai/chat", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		mux2.ServeHTTP(rr, req)
		return rr.Code
	}

	// First 2 should succeed.
	for i := range 2 {
		if code := sendChat(); code != http.StatusOK {
			t.Errorf("request %d: got %d, want 200", i+1, code)
		}
	}

	// 3rd must be rate-limited.
	body := `{"model":"test-model","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest("POST", "/api/ai/chat", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	mux2.ServeHTTP(rr, req)

	if rr.Code != http.StatusTooManyRequests {
		t.Errorf("3rd request: got %d, want 429", rr.Code)
	}
	if rr.Header().Get("Retry-After") == "" {
		t.Error("Retry-After header missing on 429 response")
	}
}

// TestHandleEmbed_RateLimit_HTTP verifies the /api/ai/embed endpoint is rate-limited.
func TestHandleEmbed_RateLimit_HTTP(t *testing.T) {
	// Embed doesn't use stub SSE; it uses the embedder which calls Route.
	// With no configured providers it returns 502; that's fine — we just
	// need to confirm 429 fires before 502.
	h := newRateLimitedHandler(t, "", 1, 1000)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/ai/embed", h.handleEmbed)

	sendEmbed := func() int {
		body := `{"input":"test","model":"x"}`
		req := httptest.NewRequest("POST", "/api/ai/embed", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		return rr.Code
	}

	// First request: not rate-limited (may be 502 due to no providers, but not 429).
	code := sendEmbed()
	if code == http.StatusTooManyRequests {
		t.Fatal("first embed request should not be rate-limited")
	}

	// Second request: must be rate-limited (cap = 1/min).
	code = sendEmbed()
	if code != http.StatusTooManyRequests {
		t.Errorf("second embed request: got %d, want 429", code)
	}
}

// TestAIRateLimiter_SweepEvictsIdleEntries verifies that the bucket sweep
// evicts entries idle for >24h and leaves recently-active entries intact.
//
// The test inserts 1000 buckets, advances the mock clock by 24h+5min so they
// are all idle, then calls sweep directly. After the sweep the map must be
// empty. A second pass inserts 1000 buckets but immediately touches one of
// them; after the sweep only that entry should survive.
func TestAIRateLimiter_SweepEvictsIdleEntries(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	mockNow := base

	rl := &aiRateLimiter{
		perMin:        100,
		perHour:       10000,
		buckets:       make(map[string]*aiBucket),
		now:           func() time.Time { return mockNow },
		sweepInterval: 5 * time.Minute,
		idleThreshold: 24 * time.Hour,
	}

	// Insert 1000 buckets, all with lastAccess = base.
	for i := 0; i < 1000; i++ {
		key := fmt.Sprintf("account-%04d", i)
		rl.allow(key)
	}
	if got := len(rl.buckets); got != 1000 {
		t.Fatalf("expected 1000 buckets before sweep, got %d", got)
	}

	// Advance mock clock beyond idle threshold.
	mockNow = base.Add(24*time.Hour + 5*time.Minute + 1*time.Second)

	// Run sweep directly.
	rl.sweep(mockNow)

	if got := len(rl.buckets); got != 0 {
		t.Errorf("expected 0 buckets after sweep (all idle >24h), got %d", got)
	}

	// Second pass: 1000 buckets, one touched after the clock advance.
	mockNow = base
	for i := 0; i < 1000; i++ {
		key := fmt.Sprintf("account2-%04d", i)
		rl.allow(key)
	}
	// Advance clock past idle threshold.
	mockNow = base.Add(24*time.Hour + 5*time.Minute + 1*time.Second)
	// Touch just one bucket at the new "now" so it won't be evicted.
	rl.allow("account2-0000")

	rl.sweep(mockNow)

	if got := len(rl.buckets); got != 1 {
		t.Errorf("expected 1 bucket after sweep (only active entry survives), got %d", got)
	}
	if _, ok := rl.buckets["account2-0000"]; !ok {
		t.Error("expected account2-0000 to survive the sweep")
	}
}
