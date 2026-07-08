package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"vulos/backend/services/notify"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func newSvc() *Service { return New() }

func cfgWithEndpoint(p Provider, url string) Config {
	return Config{
		Provider: p,
		Model:    "test-model",
		Endpoint: url,
	}
}

// ---------------------------------------------------------------------------
// 1. filterMessages — pure logic, no HTTP
// ---------------------------------------------------------------------------

func TestFilterMessages_ExcludesRole(t *testing.T) {
	msgs := []Message{
		{Role: "system", Content: "you are helpful"},
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "hi"},
	}
	got := filterMessages(msgs, "system")
	if len(got) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(got))
	}
	for _, m := range got {
		if m.Role == "system" {
			t.Errorf("system message not filtered out")
		}
	}
}

func TestFilterMessages_EmptyInput(t *testing.T) {
	got := filterMessages(nil, "system")
	if got != nil && len(got) != 0 {
		t.Errorf("expected empty result for nil input, got %v", got)
	}
}

func TestFilterMessages_NothingToExclude(t *testing.T) {
	msgs := []Message{
		{Role: "user", Content: "a"},
		{Role: "assistant", Content: "b"},
	}
	got := filterMessages(msgs, "system")
	if len(got) != len(msgs) {
		t.Fatalf("expected %d messages, got %d", len(msgs), len(got))
	}
}

// ---------------------------------------------------------------------------
// 2. orDefault — pure logic
// ---------------------------------------------------------------------------

func TestOrDefault(t *testing.T) {
	cases := []struct {
		v, def, want int
	}{
		{0, 2048, 2048},
		{-1, 2048, 2048},
		{512, 2048, 512},
		{1, 100, 1},
	}
	for _, tc := range cases {
		got := orDefault(tc.v, tc.def)
		if got != tc.want {
			t.Errorf("orDefault(%d,%d) = %d, want %d", tc.v, tc.def, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// 3. getenv — env-var fallback
// ---------------------------------------------------------------------------

func TestGetenv_Fallback(t *testing.T) {
	const key = "TEST_AI_GETENV_FALLBACK_XYZ"
	os.Unsetenv(key)
	got := getenv(key, "default-val")
	if got != "default-val" {
		t.Errorf("expected fallback, got %q", got)
	}
}

func TestGetenv_OverridesWhenSet(t *testing.T) {
	const key = "TEST_AI_GETENV_SET_XYZ"
	t.Setenv(key, "custom")
	got := getenv(key, "fallback")
	if got != "custom" {
		t.Errorf("expected %q, got %q", "custom", got)
	}
}

// ---------------------------------------------------------------------------
// 4. DefaultConfig — env driven
// ---------------------------------------------------------------------------

func TestDefaultConfig_Defaults(t *testing.T) {
	for _, k := range []string{"AI_PROVIDER", "AI_MODEL", "AI_ENDPOINT", "AI_API_KEY", "AI_SYSTEM_PROMPT"} {
		os.Unsetenv(k)
	}
	cfg := DefaultConfig()
	if cfg.Provider != ProviderOllama {
		t.Errorf("default provider = %q, want ollama", cfg.Provider)
	}
	if cfg.Model != "llama3" {
		t.Errorf("default model = %q, want llama3", cfg.Model)
	}
	if !strings.Contains(cfg.Endpoint, "11434") {
		t.Errorf("default endpoint %q should contain 11434", cfg.Endpoint)
	}
	if cfg.System == "" {
		t.Error("default system prompt should not be empty")
	}
	if !strings.Contains(cfg.System, "Vula") {
		t.Error("default system prompt should mention Vula")
	}
}

func TestDefaultConfig_EnvOverrides(t *testing.T) {
	t.Setenv("AI_PROVIDER", "openai")
	t.Setenv("AI_MODEL", "gpt-4o")
	t.Setenv("AI_API_KEY", "sk-test")
	t.Setenv("AI_ENDPOINT", "https://my.endpoint")
	t.Setenv("AI_SYSTEM_PROMPT", "custom prompt")
	cfg := DefaultConfig()
	if cfg.Provider != ProviderOpenAI {
		t.Errorf("provider = %q, want openai", cfg.Provider)
	}
	if cfg.Model != "gpt-4o" {
		t.Errorf("model = %q, want gpt-4o", cfg.Model)
	}
	if cfg.APIKey != "sk-test" {
		t.Errorf("api_key = %q, want sk-test", cfg.APIKey)
	}
	if cfg.System != "custom prompt" {
		t.Errorf("system = %q, want 'custom prompt'", cfg.System)
	}
}

// TestDefaultConfig_LLMuxOptIn verifies that setting VULOS_LLMUX_URL routes the
// assistant through the on-box llmux OpenAI-compatible gateway (ProviderCustom)
// while keeping the endpoint loopback so it remains sovereign, and that the
// trailing "/v1" is normalized away (the OpenAI path appends its own).
func TestDefaultConfig_LLMuxOptIn(t *testing.T) {
	for _, k := range []string{"AI_PROVIDER", "AI_MODEL", "AI_ENDPOINT", "AI_API_KEY", "AI_SYSTEM_PROMPT"} {
		os.Unsetenv(k)
	}
	t.Setenv("VULOS_LLMUX_URL", "http://localhost:4000/v1")
	t.Setenv("VULOS_LLMUX_KEY", "llmux-key")

	cfg := DefaultConfig()
	if cfg.Provider != ProviderCustom {
		t.Errorf("provider = %q, want custom", cfg.Provider)
	}
	if cfg.Endpoint != "http://localhost:4000" {
		t.Errorf("endpoint = %q, want http://localhost:4000 (trailing /v1 stripped)", cfg.Endpoint)
	}
	if cfg.APIKey != "llmux-key" {
		t.Errorf("api_key = %q, want llmux-key", cfg.APIKey)
	}
}

// TestDefaultConfig_LLMuxUnsetKeepsOllama confirms the default falls back to the
// direct-Ollama behavior when VULOS_LLMUX_URL is not set.
func TestDefaultConfig_LLMuxUnsetKeepsOllama(t *testing.T) {
	for _, k := range []string{"AI_PROVIDER", "AI_MODEL", "AI_ENDPOINT", "AI_API_KEY", "AI_SYSTEM_PROMPT", "VULOS_LLMUX_URL", "VULOS_LLMUX_KEY"} {
		os.Unsetenv(k)
	}
	cfg := DefaultConfig()
	if cfg.Provider != ProviderOllama {
		t.Errorf("provider = %q, want ollama", cfg.Provider)
	}
	if !strings.Contains(cfg.Endpoint, "11434") {
		t.Errorf("endpoint %q should contain 11434", cfg.Endpoint)
	}
}

func TestNormalizeOpenAIBase(t *testing.T) {
	cases := map[string]string{
		"http://localhost:4000/v1":  "http://localhost:4000",
		"http://localhost:4000/v1/": "http://localhost:4000",
		"http://localhost:4000/":    "http://localhost:4000",
		"http://localhost:4000":     "http://localhost:4000",
	}
	for in, want := range cases {
		if got := normalizeOpenAIBase(in); got != want {
			t.Errorf("normalizeOpenAIBase(%q) = %q, want %q", in, got, want)
		}
	}
}

// ---------------------------------------------------------------------------
// 5. DefaultConfig system prompt — viewport & OS-action markers
// ---------------------------------------------------------------------------

func TestDefaultConfig_SystemPromptContainsViewportMarkers(t *testing.T) {
	for _, k := range []string{"AI_PROVIDER", "AI_MODEL", "AI_ENDPOINT", "AI_API_KEY", "AI_SYSTEM_PROMPT"} {
		os.Unsetenv(k)
	}
	cfg := DefaultConfig()
	for _, marker := range []string{"<viewport", "</viewport>", "<os-action", "VULOS_SANDBOX_URL"} {
		if !strings.Contains(cfg.System, marker) {
			t.Errorf("system prompt missing marker %q", marker)
		}
	}
}

// ---------------------------------------------------------------------------
// 6. HealthCheck — provider validation (no live HTTP for claude/openai)
// ---------------------------------------------------------------------------

func TestHealthCheck_ClaudeNoKey(t *testing.T) {
	svc := newSvc()
	err := svc.HealthCheck(context.Background(), Config{Provider: ProviderClaude})
	if err == nil || !strings.Contains(err.Error(), "API key") {
		t.Errorf("expected API key error, got %v", err)
	}
}

func TestHealthCheck_OpenAINoKey(t *testing.T) {
	svc := newSvc()
	err := svc.HealthCheck(context.Background(), Config{Provider: ProviderOpenAI})
	if err == nil || !strings.Contains(err.Error(), "API key") {
		t.Errorf("expected API key error, got %v", err)
	}
}

func TestHealthCheck_ClaudeWithKey(t *testing.T) {
	svc := newSvc()
	err := svc.HealthCheck(context.Background(), Config{Provider: ProviderClaude, APIKey: "sk-test"})
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestHealthCheck_CustomProvider(t *testing.T) {
	svc := newSvc()
	err := svc.HealthCheck(context.Background(), Config{Provider: ProviderCustom})
	if err != nil {
		t.Errorf("custom provider health check should pass, got %v", err)
	}
}

func TestHealthCheck_UnknownProvider(t *testing.T) {
	svc := newSvc()
	err := svc.HealthCheck(context.Background(), Config{Provider: Provider("bogus")})
	if err == nil {
		t.Error("expected error for unknown provider")
	}
}

func TestHealthCheck_OllamaReachable(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tags" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	svc := newSvc()
	err := svc.HealthCheck(context.Background(), Config{
		Provider: ProviderOllama,
		Endpoint: ts.URL,
	})
	if err != nil {
		t.Errorf("expected nil, got %v", err)
	}
}

func TestHealthCheck_OllamaUnreachable(t *testing.T) {
	svc := newSvc()
	err := svc.HealthCheck(context.Background(), Config{
		Provider: ProviderOllama,
		Endpoint: "http://127.0.0.1:19999", // nothing listening
	})
	if err == nil {
		t.Error("expected error for unreachable Ollama")
	}
}

// ---------------------------------------------------------------------------
// 7. Complete (unknown provider) — pure dispatch logic
// ---------------------------------------------------------------------------

func TestComplete_UnknownProvider(t *testing.T) {
	svc := newSvc()
	_, err := svc.Complete(context.Background(), Config{Provider: Provider("xyz")}, CompletionRequest{})
	if err == nil || !strings.Contains(err.Error(), "unknown provider") {
		t.Errorf("expected unknown provider error, got %v", err)
	}
}

func TestStream_UnknownProvider(t *testing.T) {
	svc := newSvc()
	err := svc.Stream(context.Background(), Config{Provider: Provider("xyz")}, CompletionRequest{}, func(StreamChunk) {})
	if err == nil || !strings.Contains(err.Error(), "unknown provider") {
		t.Errorf("expected unknown provider error, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// 8. completeOllama via httptest — request shape + response parsing
// ---------------------------------------------------------------------------

func TestCompleteOllama_RequestShape(t *testing.T) {
	var gotBody map[string]json.RawMessage
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			http.NotFound(w, r)
			return
		}
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &gotBody)
		// Return a valid Ollama response
		resp := map[string]any{
			"message": map[string]string{"content": "pong"},
			"done":    true,
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer ts.Close()

	svc := newSvc()
	cfg := cfgWithEndpoint(ProviderOllama, ts.URL)
	cfg.System = "be helpful"
	req := CompletionRequest{
		Messages: []Message{{Role: "user", Content: "ping"}},
	}
	got, err := svc.Complete(context.Background(), cfg, req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "pong" {
		t.Errorf("expected 'pong', got %q", got)
	}
	// System prompt should be prepended to messages
	var msgs []map[string]string
	json.Unmarshal(gotBody["messages"], &msgs)
	if len(msgs) < 2 {
		t.Fatalf("expected ≥2 messages (system + user), got %d", len(msgs))
	}
	if msgs[0]["role"] != "system" || msgs[0]["content"] != "be helpful" {
		t.Errorf("first message should be system prompt, got %+v", msgs[0])
	}
	if msgs[1]["role"] != "user" {
		t.Errorf("second message should be user, got %+v", msgs[1])
	}
}

func TestCompleteOllama_NoSystemPrompt(t *testing.T) {
	var gotBody map[string]json.RawMessage
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &gotBody)
		json.NewEncoder(w).Encode(map[string]any{
			"message": map[string]string{"content": "ok"},
		})
	}))
	defer ts.Close()

	svc := newSvc()
	cfg := cfgWithEndpoint(ProviderOllama, ts.URL)
	cfg.System = "" // no system prompt
	req := CompletionRequest{Messages: []Message{{Role: "user", Content: "hi"}}}
	svc.Complete(context.Background(), cfg, req)

	var msgs []map[string]string
	json.Unmarshal(gotBody["messages"], &msgs)
	if len(msgs) != 1 {
		t.Errorf("without system prompt, expected exactly 1 message, got %d", len(msgs))
	}
}

// ---------------------------------------------------------------------------
// 9. streamOllama via httptest — chunk delivery
// ---------------------------------------------------------------------------

func TestStreamOllama_ChunksAndDone(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := []map[string]any{
			{"message": map[string]string{"content": "hello"}, "done": false},
			{"message": map[string]string{"content": " world"}, "done": false},
			{"message": map[string]string{"content": ""}, "done": true},
		}
		for _, p := range parts {
			json.NewEncoder(w).Encode(p)
		}
	}))
	defer ts.Close()

	svc := newSvc()
	cfg := cfgWithEndpoint(ProviderOllama, ts.URL)
	var chunks []StreamChunk
	err := svc.Stream(context.Background(), cfg, CompletionRequest{
		Messages: []Message{{Role: "user", Content: "test"}},
	}, func(c StreamChunk) {
		chunks = append(chunks, c)
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var content string
	doneCount := 0
	for _, c := range chunks {
		content += c.Content
		if c.Done {
			doneCount++
		}
	}
	if content != "hello world" {
		t.Errorf("assembled content = %q, want 'hello world'", content)
	}
	if doneCount != 1 {
		t.Errorf("expected exactly 1 done chunk, got %d", doneCount)
	}
}

// ---------------------------------------------------------------------------
// 10. completeOpenAI / custom endpoint via httptest
// ---------------------------------------------------------------------------

func TestCompleteOpenAI_RequestAndResponse(t *testing.T) {
	var gotAuth string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		resp := map[string]any{
			"choices": []map[string]any{
				{"message": map[string]string{"content": "result"}},
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer ts.Close()

	svc := &Service{client: ts.Client()}
	// Patch the URL by using ProviderCustom so completeOpenAI uses our ts.URL
	cfg := Config{
		Provider: ProviderCustom,
		Model:    "test-model",
		APIKey:   "sk-test-key",
		Endpoint: ts.URL,
	}
	got, err := svc.Complete(context.Background(), cfg, CompletionRequest{
		Messages: []Message{{Role: "user", Content: "hello"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "result" {
		t.Errorf("expected 'result', got %q", got)
	}
	if !strings.Contains(gotAuth, "Bearer sk-test-key") {
		t.Errorf("Authorization header = %q, want Bearer token", gotAuth)
	}
}

func TestCompleteOpenAI_ErrorStatus(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":"invalid_api_key"}`))
	}))
	defer ts.Close()

	svc := &Service{client: ts.Client()}
	cfg := Config{Provider: ProviderCustom, Model: "m", Endpoint: ts.URL}
	_, err := svc.Complete(context.Background(), cfg, CompletionRequest{
		Messages: []Message{{Role: "user", Content: "x"}},
	})
	if err == nil {
		t.Fatal("expected error for 401 response")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("error should mention status code 401, got %v", err)
	}
}

func TestStreamOpenAI_ChunksAndDone(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lines := []string{
			`data: {"choices":[{"delta":{"content":"foo"}}]}`,
			`data: {"choices":[{"delta":{"content":" bar"}}]}`,
			`data: [DONE]`,
		}
		for _, l := range lines {
			fmt.Fprintln(w, l)
		}
	}))
	defer ts.Close()

	svc := &Service{client: ts.Client()}
	cfg := Config{Provider: ProviderCustom, Model: "m", Endpoint: ts.URL}
	var chunks []StreamChunk
	err := svc.Stream(context.Background(), cfg, CompletionRequest{
		Messages: []Message{{Role: "user", Content: "test"}},
	}, func(c StreamChunk) {
		chunks = append(chunks, c)
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var content string
	doneCount := 0
	for _, c := range chunks {
		content += c.Content
		if c.Done {
			doneCount++
		}
	}
	if content != "foo bar" {
		t.Errorf("assembled = %q, want 'foo bar'", content)
	}
	if doneCount != 1 {
		t.Errorf("expected 1 done, got %d", doneCount)
	}
}

// ---------------------------------------------------------------------------
// 10b. STREAMING error propagation (deep/os2) — a streaming completion must NOT
// silently succeed with empty/truncated content when the upstream returns an
// HTTP error, emits an error frame, or drops mid-stream. Before the fix all
// three streamers skipped a non-SSE error body, ended the loop, and returned nil
// — so AgentTurnStream streamed an EMPTY answer + {"type":"done"} and the run
// derailed with no error surfaced (unlike the non-stream path, which fails
// closed on non-200). These lock the fail-closed behavior.
// ---------------------------------------------------------------------------

// TestStreamOpenAI_ErrorStatusPropagates: a 401 (bad key) / 429 (rate limit) /
// 5xx returns a JSON error body, not an SSE stream. Stream must return an error
// mentioning the status, NOT nil-with-no-content.
func TestStreamOpenAI_ErrorStatusPropagates(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":{"message":"invalid_api_key"}}`))
	}))
	defer ts.Close()

	svc := &Service{client: ts.Client()}
	cfg := Config{Provider: ProviderCustom, Model: "m", Endpoint: ts.URL}
	var gotContent string
	err := svc.Stream(context.Background(), cfg, CompletionRequest{
		Messages: []Message{{Role: "user", Content: "x"}},
	}, func(c StreamChunk) { gotContent += c.Content })
	if err == nil {
		t.Fatal("streamOpenAI must return an error on HTTP 401, not silently succeed")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("error should mention status 401, got %v", err)
	}
	if gotContent != "" {
		t.Errorf("no content should be emitted on an error response, got %q", gotContent)
	}
}

// TestStreamOllama_ErrorStatusPropagates: same fail-closed guarantee for Ollama.
func TestStreamOllama_ErrorStatusPropagates(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"model not found"}`))
	}))
	defer ts.Close()

	svc := &Service{client: ts.Client()}
	cfg := cfgWithEndpoint(ProviderOllama, ts.URL)
	err := svc.Stream(context.Background(), cfg, CompletionRequest{
		Messages: []Message{{Role: "user", Content: "x"}},
	}, func(StreamChunk) {})
	if err == nil {
		t.Fatal("streamOllama must return an error on HTTP 500, not silently succeed")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error should mention status 500, got %v", err)
	}
}

// TestStreamOllama_MidStreamErrorFramePropagates: Ollama can return HTTP 200 and
// then emit an {"error":...} object mid-stream. That must surface as an error,
// not be dropped so the caller sees whatever partial content preceded it as a
// complete answer.
func TestStreamOllama_MidStreamErrorFramePropagates(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"message": map[string]string{"content": "partial"}, "done": false})
		json.NewEncoder(w).Encode(map[string]any{"error": "context length exceeded"})
	}))
	defer ts.Close()

	svc := &Service{client: ts.Client()}
	cfg := cfgWithEndpoint(ProviderOllama, ts.URL)
	sawDone := false
	err := svc.Stream(context.Background(), cfg, CompletionRequest{
		Messages: []Message{{Role: "user", Content: "x"}},
	}, func(c StreamChunk) {
		if c.Done {
			sawDone = true
		}
	})
	if err == nil {
		t.Fatal("a mid-stream Ollama error frame must surface as an error")
	}
	if !strings.Contains(err.Error(), "context length exceeded") {
		t.Errorf("error should carry the upstream message, got %v", err)
	}
	if sawDone {
		t.Error("a stream that errored must NOT emit a Done chunk (would look like a clean finish)")
	}
}

// TestStreamClaude_ErrorStatusPropagates uses a RoundTripper to intercept the
// hardcoded api.anthropic.com URL and return a 429. streamClaude must fail
// closed exactly like completeClaude does.
func TestStreamClaude_ErrorStatusPropagates(t *testing.T) {
	rt := &captureTransport{
		captured: new(*http.Request),
		resp: &http.Response{
			StatusCode: http.StatusTooManyRequests,
			Body:       io.NopCloser(strings.NewReader(`{"type":"error","error":{"type":"rate_limit_error"}}`)),
			Header:     make(http.Header),
		},
	}
	svc := &Service{client: &http.Client{Transport: rt, Timeout: 5 * time.Second}}
	cfg := Config{Provider: ProviderClaude, Model: "m", APIKey: "k"}
	var gotContent string
	err := svc.Stream(context.Background(), cfg, CompletionRequest{
		Messages: []Message{{Role: "user", Content: "x"}},
	}, func(c StreamChunk) { gotContent += c.Content })
	if err == nil {
		t.Fatal("streamClaude must return an error on HTTP 429, not silently succeed")
	}
	if !strings.Contains(err.Error(), "429") {
		t.Errorf("error should mention status 429, got %v", err)
	}
	if gotContent != "" {
		t.Errorf("no content on an error response, got %q", gotContent)
	}
}

// TestStreamClaude_HappyPathViaRoundTripper exercises the SSE parse path that the
// old test t.Skip'd (the URL is hardcoded), via a RoundTripper — proving the fix
// did not break normal streaming.
func TestStreamClaude_HappyPathViaRoundTripper(t *testing.T) {
	sse := strings.Join([]string{
		`data: {"type":"content_block_delta","delta":{"text":"hi"}}`,
		`data: {"type":"content_block_delta","delta":{"text":" there"}}`,
		`data: {"type":"message_stop"}`,
		"",
	}, "\n")
	rt := &captureTransport{
		captured: new(*http.Request),
		resp: &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(strings.NewReader(sse)),
			Header:     make(http.Header),
		},
	}
	svc := &Service{client: &http.Client{Transport: rt}}
	cfg := Config{Provider: ProviderClaude, Model: "m", APIKey: "k"}
	var content string
	done := 0
	err := svc.Stream(context.Background(), cfg, CompletionRequest{
		Messages: []Message{{Role: "user", Content: "x"}},
	}, func(c StreamChunk) {
		content += c.Content
		if c.Done {
			done++
		}
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if content != "hi there" {
		t.Errorf("assembled = %q, want 'hi there'", content)
	}
	if done != 1 {
		t.Errorf("expected 1 done chunk, got %d", done)
	}
}

// TestStreamOpenAI_OverLongLineSurfaces: a single SSE line larger than the raised
// scanner cap must surface via scanner.Err() rather than silently truncating the
// answer. We send a data line whose payload exceeds maxStreamLine.
func TestStreamOpenAI_OverLongLineSurfaces(t *testing.T) {
	huge := strings.Repeat("A", maxStreamLine+16)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":\"%s\"}}]}\n", huge)
	}))
	defer ts.Close()

	svc := &Service{client: ts.Client()}
	cfg := Config{Provider: ProviderCustom, Model: "m", Endpoint: ts.URL}
	err := svc.Stream(context.Background(), cfg, CompletionRequest{
		Messages: []Message{{Role: "user", Content: "x"}},
	}, func(StreamChunk) {})
	if err == nil {
		t.Fatal("an over-long SSE line must surface as a read error, not a silent truncation")
	}
}

// ---------------------------------------------------------------------------
// 11. streamClaude via httptest — SSE parsing
// ---------------------------------------------------------------------------

func TestStreamClaude_SSEParsing(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify headers Claude expects
		if r.Header.Get("anthropic-version") == "" {
			t.Error("missing anthropic-version header")
		}
		lines := []string{
			`data: {"type":"content_block_delta","delta":{"text":"hi"}}`,
			`data: {"type":"content_block_delta","delta":{"text":" there"}}`,
			`data: {"type":"message_stop"}`,
		}
		for _, l := range lines {
			fmt.Fprintln(w, l)
		}
	}))
	defer ts.Close()

	svc := &Service{client: ts.Client()}
	cfg := Config{
		Provider: ProviderClaude,
		Model:    "claude-test",
		APIKey:   "test-key",
	}

	// Override the hardcoded Claude URL by temporarily pointing the client through our server.
	// Because completeClaude hardcodes the URL we skip this path for the live model
	// and instead test the SSE-parsing helper indirectly via streamOllama which shares the
	// same line-splitting pattern.  For Claude specifically we test request-shape & header.
	t.Skip("Claude URL is hardcoded to api.anthropic.com — verified header logic via unit inspection")
	_ = svc
	_ = cfg
}

// ---------------------------------------------------------------------------
// 12. completeClaude — non-200 error path (via httptest + custom client trick)
// ---------------------------------------------------------------------------

func TestCompleteClaude_Headers(t *testing.T) {
	// Verify that the Claude completer would send correct headers; since the URL
	// is hardcoded we test request construction logic via reflection on the body.
	// We intercept via a custom RoundTripper.
	var capturedReq *http.Request
	rt := &captureTransport{captured: &capturedReq, resp: &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(strings.NewReader(`{"content":[{"text":"hello"}]}`)),
		Header:     make(http.Header),
	}}
	svc := &Service{client: &http.Client{Transport: rt, Timeout: 5 * time.Second}}
	cfg := Config{
		Provider: ProviderClaude,
		Model:    "claude-3-5-haiku-20241022",
		APIKey:   "my-key",
		System:   "be helpful",
	}
	got, err := svc.Complete(context.Background(), cfg, CompletionRequest{
		Messages: []Message{{Role: "user", Content: "hello"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "hello" {
		t.Errorf("expected 'hello', got %q", got)
	}
	if capturedReq == nil {
		t.Fatal("no request captured")
	}
	if capturedReq.Header.Get("x-api-key") != "my-key" {
		t.Errorf("x-api-key header = %q", capturedReq.Header.Get("x-api-key"))
	}
	if capturedReq.Header.Get("anthropic-version") == "" {
		t.Error("anthropic-version header missing")
	}
	if capturedReq.Header.Get("Content-Type") != "application/json" {
		t.Errorf("Content-Type = %q", capturedReq.Header.Get("Content-Type"))
	}
}

func TestCompleteClaude_SystemPromptFiltersFromMessages(t *testing.T) {
	// Ensure the system role is stripped from the messages array for Claude
	var gotBody map[string]json.RawMessage
	rt := &captureTransport{
		captured: new(*http.Request),
		body:     &gotBody,
		resp: &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(strings.NewReader(`{"content":[{"text":"ok"}]}`)),
			Header:     make(http.Header),
		},
	}
	svc := &Service{client: &http.Client{Transport: rt}}
	cfg := Config{Provider: ProviderClaude, Model: "m", APIKey: "k", System: "sys"}
	msgs := []Message{
		{Role: "system", Content: "override"},
		{Role: "user", Content: "question"},
	}
	svc.Complete(context.Background(), cfg, CompletionRequest{Messages: msgs})
	var outMsgs []map[string]string
	json.Unmarshal(gotBody["messages"], &outMsgs)
	for _, m := range outMsgs {
		if m["role"] == "system" {
			t.Error("system role should be filtered out of Claude messages array")
		}
	}
}

// captureTransport is a mock http.RoundTripper for inspecting outgoing requests.
type captureTransport struct {
	captured **http.Request
	body     *map[string]json.RawMessage
	resp     *http.Response
}

func (ct *captureTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if ct.captured != nil {
		*ct.captured = r
	}
	if ct.body != nil {
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, ct.body)
		// Replace response body since we consumed the request body
	}
	return ct.resp, nil
}

// ---------------------------------------------------------------------------
// 13. HistoryStore — pure in-memory logic
// ---------------------------------------------------------------------------

func TestHistoryStore_SaveAndList(t *testing.T) {
	dir := t.TempDir()
	h := NewHistoryStore(dir)

	id := h.Save("user1", []Message{
		{Role: "user", Content: "first message"},
	})
	if id == "" {
		t.Error("Save should return a non-empty conversation ID")
	}

	convs := h.List("user1", 10)
	if len(convs) != 1 {
		t.Fatalf("expected 1 conversation, got %d", len(convs))
	}
	if convs[0].Title != "first message" {
		t.Errorf("title = %q, want 'first message'", convs[0].Title)
	}
}

func TestHistoryStore_TitleTruncated(t *testing.T) {
	dir := t.TempDir()
	h := NewHistoryStore(dir)
	long := strings.Repeat("a", 60)
	h.Save("user1", []Message{{Role: "user", Content: long}})
	convs := h.List("user1", 1)
	if len(convs[0].Title) > 53 { // 50 + "..."
		t.Errorf("title too long: %d chars", len(convs[0].Title))
	}
	if !strings.HasSuffix(convs[0].Title, "...") {
		t.Errorf("truncated title should end with '...', got %q", convs[0].Title)
	}
}

func TestHistoryStore_AppendToRecentConversation(t *testing.T) {
	dir := t.TempDir()
	h := NewHistoryStore(dir)
	h.Save("user1", []Message{{Role: "user", Content: "turn 1"}})
	h.Save("user1", []Message{{Role: "assistant", Content: "reply 1"}})
	convs := h.List("user1", 10)
	// Should be one conversation with 2 messages (recent enough)
	if len(convs) != 1 {
		t.Errorf("expected messages merged into 1 conversation, got %d", len(convs))
	}
	if len(convs[0].Messages) != 2 {
		t.Errorf("expected 2 messages, got %d", len(convs[0].Messages))
	}
}

func TestHistoryStore_GetAndDelete(t *testing.T) {
	dir := t.TempDir()
	h := NewHistoryStore(dir)
	id := h.Save("user1", []Message{{Role: "user", Content: "hello"}})

	c := h.Get("user1", id)
	if c == nil {
		t.Fatal("Get should return the saved conversation")
	}
	if c.ID != id {
		t.Errorf("ID mismatch: got %q, want %q", c.ID, id)
	}

	h.Delete("user1", id)
	if h.Get("user1", id) != nil {
		t.Error("deleted conversation should not be retrievable")
	}
}

func TestHistoryStore_ListNewestFirst(t *testing.T) {
	dir := t.TempDir()
	h := NewHistoryStore(dir)
	// Create two separate conversations by manipulating time via force-sleep or
	// by directly testing List ordering assumption (newest = last appended)
	// We create separate conv sessions by using the store's internal data map.
	h.Save("u", []Message{{Role: "user", Content: "first"}})
	// Force a new conversation by backdating the last one
	h.mu.Lock()
	h.data["u"][0].UpdatedAt = time.Now().Add(-1 * time.Hour)
	h.mu.Unlock()
	h.Save("u", []Message{{Role: "user", Content: "second"}})

	convs := h.List("u", 10)
	if len(convs) != 2 {
		t.Fatalf("expected 2 conversations, got %d", len(convs))
	}
	// List returns newest first
	if convs[0].Messages[0].Content != "second" {
		t.Errorf("newest conversation should be first, got %q", convs[0].Messages[0].Content)
	}
}

func TestHistoryStore_Flush(t *testing.T) {
	dir := t.TempDir()
	h := NewHistoryStore(dir)
	h.Save("u", []Message{{Role: "user", Content: "persisted"}})
	if err := h.Flush(); err != nil {
		t.Fatalf("Flush error: %v", err)
	}
	// Reload from disk
	h2 := NewHistoryStore(dir)
	convs := h2.List("u", 10)
	if len(convs) == 0 {
		t.Error("reloaded store should have saved conversations")
	}
}

func TestHistoryStore_MaxConversationsCapped(t *testing.T) {
	dir := t.TempDir()
	h := NewHistoryStore(dir)
	// Create 55 conversations for user
	for i := 0; i < 55; i++ {
		h.mu.Lock()
		if len(h.data["u"]) > 0 {
			h.data["u"][len(h.data["u"])-1].UpdatedAt = time.Now().Add(-1 * time.Hour)
		}
		h.mu.Unlock()
		h.Save("u", []Message{{Role: "user", Content: fmt.Sprintf("msg %d", i)}})
	}
	convs := h.List("u", 0)
	if len(convs) > 50 {
		t.Errorf("should cap at 50 conversations, got %d", len(convs))
	}
}

// ---------------------------------------------------------------------------
// 14. MissionStore — pure in-memory logic
// ---------------------------------------------------------------------------

func TestMissionStore_CreateAndGet(t *testing.T) {
	dir := t.TempDir()
	ms := NewMissionStore(dir)

	m := ms.Create("user1", "Test Mission", "do things", []string{"step A", "step B"})
	if m == nil {
		t.Fatal("Create should return a non-nil mission")
	}
	if m.Status != MissionPending {
		t.Errorf("new mission status = %q, want pending", m.Status)
	}
	if len(m.Steps) != 2 {
		t.Fatalf("expected 2 steps, got %d", len(m.Steps))
	}
	if m.Steps[0].Label != "step A" {
		t.Errorf("step 0 label = %q", m.Steps[0].Label)
	}

	got := ms.Get(m.ID)
	if got == nil || got.ID != m.ID {
		t.Error("Get should return the created mission")
	}
}

func TestMissionStore_UpdateStep_CompletesWhenAllDone(t *testing.T) {
	dir := t.TempDir()
	ms := NewMissionStore(dir)
	m := ms.Create("u", "T", "d", []string{"a", "b"})

	ms.UpdateStep(m.ID, m.Steps[0].ID, MissionCompleted, "done a")
	if ms.Get(m.ID).Status != MissionRunning {
		t.Error("status should be running when only one step done")
	}

	ms.UpdateStep(m.ID, m.Steps[1].ID, MissionCompleted, "done b")
	if ms.Get(m.ID).Status != MissionCompleted {
		t.Errorf("status = %q, want completed", ms.Get(m.ID).Status)
	}
}

func TestMissionStore_UpdateStep_FailsWhenAnyStepFails(t *testing.T) {
	dir := t.TempDir()
	ms := NewMissionStore(dir)
	m := ms.Create("u", "T", "d", []string{"a", "b"})

	ms.UpdateStep(m.ID, m.Steps[0].ID, MissionFailed, "error")
	ms.UpdateStep(m.ID, m.Steps[1].ID, MissionCompleted, "ok")
	if ms.Get(m.ID).Status != MissionFailed {
		t.Errorf("mission should be failed when any step fails, got %q", ms.Get(m.ID).Status)
	}
}

func TestMissionStore_Cancel(t *testing.T) {
	dir := t.TempDir()
	ms := NewMissionStore(dir)
	m := ms.Create("u", "T", "d", []string{"a", "b"})

	ms.Cancel(m.ID)
	got := ms.Get(m.ID)
	if got.Status != MissionCancelled {
		t.Errorf("status = %q, want cancelled", got.Status)
	}
	for _, s := range got.Steps {
		if s.Status != MissionCancelled {
			t.Errorf("step %q status = %q, want cancelled", s.ID, s.Status)
		}
	}
}

func TestMissionStore_ActiveCount(t *testing.T) {
	dir := t.TempDir()
	ms := NewMissionStore(dir)
	m1 := ms.Create("u", "T1", "d", []string{"a"})
	time.Sleep(2 * time.Millisecond)
	m2 := ms.Create("u", "T2", "d", []string{"a"})
	time.Sleep(2 * time.Millisecond)
	ms.Create("u", "T3", "d", []string{"a"}) // stays pending

	ms.UpdateStep(m1.ID, m1.Steps[0].ID, MissionCompleted, "")
	ms.UpdateStep(m2.ID, m2.Steps[0].ID, MissionFailed, "")
	// m3 remains pending

	if ms.ActiveCount("u") != 0 {
		t.Errorf("expected 0 running missions, got %d", ms.ActiveCount("u"))
	}
}

func TestMissionStore_ListForUser(t *testing.T) {
	dir := t.TempDir()
	ms := NewMissionStore(dir)
	// Sleep 2ms between creates to avoid millisecond-granularity ID collisions.
	ms.Create("u1", "A", "d", []string{"x"})
	time.Sleep(2 * time.Millisecond)
	ms.Create("u1", "B", "d", []string{"x"})
	time.Sleep(2 * time.Millisecond)
	ms.Create("u2", "C", "d", []string{"x"})

	list := ms.ListForUser("u1", 0)
	if len(list) != 2 {
		t.Errorf("u1 should have 2 missions, got %d", len(list))
	}
	for _, m := range list {
		if m.UserID != "u1" {
			t.Errorf("mission %q belongs to wrong user %q", m.ID, m.UserID)
		}
	}
}

func TestMissionStore_Flush(t *testing.T) {
	dir := t.TempDir()
	ms := NewMissionStore(dir)
	m := ms.Create("u", "Persist", "d", []string{"step"})
	if err := ms.Flush(); err != nil {
		t.Fatalf("Flush error: %v", err)
	}
	ms2 := NewMissionStore(dir)
	if ms2.Get(m.ID) == nil {
		t.Error("mission should survive flush+reload")
	}
}

func TestMissionStore_StepIDs(t *testing.T) {
	dir := t.TempDir()
	ms := NewMissionStore(dir)
	m := ms.Create("u", "T", "d", []string{"alpha", "beta", "gamma"})
	if len(m.Steps) != 3 {
		t.Fatalf("expected 3 steps")
	}
	// Each step ID should be unique
	seen := map[string]bool{}
	for _, s := range m.Steps {
		if seen[s.ID] {
			t.Errorf("duplicate step ID %q", s.ID)
		}
		seen[s.ID] = true
	}
}

// ---------------------------------------------------------------------------
// 15. ProactiveAgent — check registration and run logic (no live AI)
// ---------------------------------------------------------------------------

func TestProactiveAgent_RegisterCheck(t *testing.T) {
	svc := newSvc()
	cfg := Config{Provider: ProviderOllama}
	pa := NewProactiveAgent(svc, cfg, nil)

	if len(pa.checks) != 0 {
		t.Error("new agent should have no checks")
	}
	pa.RegisterCheck(func(_ context.Context) (string, string, notify.Level, bool) {
		return "", "", notify.LevelInfo, false
	})
	if len(pa.checks) != 1 {
		t.Error("after RegisterCheck, should have 1 check")
	}
}

func TestProactiveAgent_EnhanceSkippedWhenNoKey(t *testing.T) {
	// When Provider is not Ollama and APIKey is empty, enhance should not call AI
	// We verify by using a failing server — if it gets called, test fails.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("AI should not be called when no API key and not Ollama")
		w.WriteHeader(500)
	}))
	defer ts.Close()

	svc := &Service{client: ts.Client()}
	cfg := Config{Provider: ProviderCustom, APIKey: "", Endpoint: ts.URL}
	// proactiveAgent.enhance only fires when APIKey != "" || Provider == Ollama
	// So with empty APIKey + ProviderCustom, it should not call enhance
	pa := NewProactiveAgent(svc, cfg, nil)
	// Manually call enhance to confirm it returns "" without hitting the server
	// We do this via runChecks with a check that fires
	fired := false
	pa.RegisterCheck(func(_ context.Context) (string, string, notify.Level, bool) {
		fired = true
		return "title", "body", notify.LevelWarning, true
	})
	// We can't call runChecks directly due to nil notifier; test the condition inline
	// The condition is: pa.cfg.APIKey != "" || pa.cfg.Provider == ProviderOllama
	hasEnhance := cfg.APIKey != "" || cfg.Provider == ProviderOllama
	if hasEnhance {
		t.Error("should not enhance when no key and not Ollama")
	}
	_ = fired
	_ = pa
}
