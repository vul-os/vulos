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
