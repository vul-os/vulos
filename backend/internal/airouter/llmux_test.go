package airouter

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestLLMUXEndpointParsing verifies LLMUX_URL normalisation + fallback.
func TestLLMUXEndpointParsing(t *testing.T) {
	cases := []struct {
		name     string
		url      string
		key      string
		wantOK   bool
		wantBase string
		wantKey  string
	}{
		{"unset", "", "", false, "", ""},
		{"blank", "   ", "", false, "", ""},
		{"with /v1 suffix", "http://llmux:4000/v1", "sk-x", true, "http://llmux:4000", "sk-x"},
		{"with trailing slash", "http://llmux:4000/v1/", "sk-y", true, "http://llmux:4000", "sk-y"},
		{"bare host", "http://llmux:4000", "", true, "http://llmux:4000", ""},
		{"key trimmed", "http://llmux:4000/v1", "  sk-z  ", true, "http://llmux:4000", "sk-z"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("LLMUX_URL", tc.url)
			t.Setenv("LLMUX_KEY", tc.key)
			cfg, ok := llmuxEndpoint()
			if ok != tc.wantOK {
				t.Fatalf("ok: got %v, want %v", ok, tc.wantOK)
			}
			if !ok {
				return
			}
			if cfg.BaseURL != tc.wantBase {
				t.Errorf("base: got %q, want %q", cfg.BaseURL, tc.wantBase)
			}
			if cfg.Key != tc.wantKey {
				t.Errorf("key: got %q, want %q", cfg.Key, tc.wantKey)
			}
		})
	}
}

// TestChatRoutesThroughLLMUX verifies that when LLMUX_URL is set, the chat
// router targets the gateway (base_url + bearer key) instead of any BYO
// provider — even though a BYO provider is configured.
func TestChatRoutesThroughLLMUX(t *testing.T) {
	var gotPath, gotAuth string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	t.Setenv("LLMUX_URL", srv.URL+"/v1")
	t.Setenv("LLMUX_KEY", "sk-gateway")

	s := newTestStore(t)
	// A BYO provider pointing at a dead URL — if llmux routing fails, the call
	// would hit this and the assertions below would fail.
	_ = s.AddProvider(Provider{
		ID: "byo", DisplayName: "BYO", BaseURL: "http://127.0.0.1:1",
		APIKeyEnc: "byo-key", Models: []string{"gpt-4o"},
	})
	r := NewRouter(s)

	stream, err := r.Route(context.Background(), ChatRequest{Model: "gpt-4o", Messages: []Message{{Role: "user", Content: "hello"}}})
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	out, err := DrainSSE(stream)
	if err != nil {
		t.Fatalf("DrainSSE: %v", err)
	}

	if gotPath != "/v1/chat/completions" {
		t.Errorf("path: got %q, want /v1/chat/completions", gotPath)
	}
	if gotAuth != "Bearer sk-gateway" {
		t.Errorf("auth: got %q, want Bearer sk-gateway", gotAuth)
	}
	if gotBody["model"] != "gpt-4o" {
		t.Errorf("model: got %v, want gpt-4o", gotBody["model"])
	}
	if out != "hi" {
		t.Errorf("content: got %q, want hi", out)
	}
}

// TestEmbedRoutesThroughLLMUX verifies embeddings target the gateway when set.
func TestEmbedRoutesThroughLLMUX(t *testing.T) {
	var gotPath, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"embedding": []float32{0.1, 0.2, 0.3}}},
		})
	}))
	defer srv.Close()

	t.Setenv("LLMUX_URL", srv.URL)
	t.Setenv("LLMUX_KEY", "sk-embed")

	s := newTestStore(t)
	// Dead BYO provider — proves llmux wins.
	_ = s.AddProvider(Provider{
		ID: "byo", DisplayName: "BYO", BaseURL: "http://127.0.0.1:1",
		APIKeyEnc: "byo-key", Models: []string{"text-embedding-3-small"},
	})
	r := NewRouter(s)
	e := NewEmbedder(s, r)

	vec, model, err := e.Embed("text-embedding-3-small", "hello world")
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if gotPath != "/v1/embeddings" {
		t.Errorf("path: got %q, want /v1/embeddings", gotPath)
	}
	if gotAuth != "Bearer sk-embed" {
		t.Errorf("auth: got %q, want Bearer sk-embed", gotAuth)
	}
	if len(vec) != 3 || vec[0] != 0.1 {
		t.Errorf("vec: got %v", vec)
	}
	if model != "text-embedding-3-small" {
		t.Errorf("model: got %q", model)
	}
}

// TestChatStandaloneFallback verifies that with LLMUX_URL unset, chat still
// uses the BYO provider path (so vulos runs independently).
func TestChatStandaloneFallback(t *testing.T) {
	t.Setenv("LLMUX_URL", "")

	var hit bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = true
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"byo\"}}]}\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	s := newTestStore(t)
	_ = s.AddProvider(Provider{
		ID: "byo", DisplayName: "BYO", BaseURL: srv.URL,
		APIKeyEnc: "byo-key", Models: []string{"gpt-4o"},
	})
	r := NewRouter(s)

	stream, err := r.Route(context.Background(), ChatRequest{Model: "gpt-4o", Messages: []Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	out, err := DrainSSE(stream)
	if err != nil {
		t.Fatalf("DrainSSE: %v", err)
	}
	if !hit {
		t.Fatal("BYO provider was not hit — fallback broken")
	}
	if out != "byo" {
		t.Errorf("content: got %q, want byo", out)
	}
}
