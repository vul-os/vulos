package ai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"vulos/backend/services/env"
)

// maxStreamLine bounds a single SSE line the stream scanner will buffer. The
// bufio.Scanner default (64 KiB) can be exceeded by a large content block or a
// verbose error body, in which case Scan() stops with bufio.ErrTooLong; we raise
// the cap to 1 MiB AND check scanner.Err() so an over-long line is surfaced as an
// error rather than silently truncating the stream.
const maxStreamLine = 1 << 20

// Provider identifies an AI backend.
type Provider string

const (
	ProviderClaude Provider = "claude"
	ProviderOpenAI Provider = "openai"
	ProviderOllama Provider = "ollama"
	ProviderCustom Provider = "custom" // any OpenAI-compatible endpoint
)

// Message is a chat message.
type Message struct {
	Role    string `json:"role"` // "system", "user", "assistant"
	Content string `json:"content"`
}

// Config for an AI provider.
type Config struct {
	Provider Provider `json:"provider"`
	APIKey   string   `json:"api_key"`
	Model    string   `json:"model"`
	Endpoint string   `json:"endpoint"` // for Ollama or custom
	System   string   `json:"system"`   // system prompt
	// Tier is the operator's DECLARATION of the sovereignty tier the endpoint
	// sits in (one of "local"/"sovereign"/"brokered"/"external"; empty ⇒ derive
	// from locality). It is how an operator says "this off-box endpoint is an
	// operator-declared in-region sovereign pool (unverified by Vulos)" or "a
	// brokered no-train provider".
	// It is NEVER used to upgrade a loopback endpoint (that stays local) and an
	// unverifiable "local" declaration on an off-box endpoint fails closed to
	// external. See services/assistant.Evaluate for the classification.
	Tier string `json:"tier"`
}

// CompletionRequest is what we send to the provider.
type CompletionRequest struct {
	Messages    []Message `json:"messages"`
	MaxTokens   int       `json:"max_tokens,omitempty"`
	Temperature float64   `json:"temperature,omitempty"`
	Stream      bool      `json:"stream"`
}

// StreamChunk is a piece of a streamed response.
type StreamChunk struct {
	Content string `json:"content"`
	Done    bool   `json:"done"`
	Error   string `json:"error,omitempty"`
}

// Service handles AI completions with pluggable backends.
type Service struct {
	client *http.Client
}

func New() *Service {
	return &Service{
		client: &http.Client{Timeout: 120 * time.Second},
	}
}

// DefaultConfig returns config from environment.
func DefaultConfig() Config {
	cfg := Config{
		Provider: Provider(getenv("AI_PROVIDER", "ollama")),
		APIKey:   os.Getenv("AI_API_KEY"),
		Model:    getenv("AI_MODEL", "llama3"),
		Endpoint: getenv("AI_ENDPOINT", "http://localhost:11434"),
		// Operator's sovereignty-tier declaration for the endpoint (the config
		// knob, mirrored on the llmux side by each provider's `tier`). Empty ⇒
		// derive from locality (loopback → local, else external), so nothing
		// silently upgrades; sovereign/brokered are explicit declarations.
		Tier: strings.TrimSpace(os.Getenv("VULOS_AI_TIER")),
		System: getenv("AI_SYSTEM_PROMPT", `You are Vulos, the AI assistant built into Vulos OS. You are helpful, concise, and friendly.

You can generate visual UI by including a <viewport> block in your response. The OS opens it as a window.

## HTML-only viewport (for static/JS-only content):
<viewport title="Window Title">
<!DOCTYPE html>
<html>
<head><style>body{background:#0a0a0a;color:#e5e5e5;font-family:system-ui;margin:0;padding:16px}</style></head>
<body><!-- your HTML + JS here --></body>
</html>
</viewport>

## Viewport with Python backend (for data, APIs, computation):
<viewport title="Window Title">
<script type="text/python">
import http.server, json, os
port = int(os.environ.get("PORT", 9100))

class Handler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Access-Control-Allow-Origin", "*")
        self.end_headers()
        self.wfile.write(json.dumps({"result": "hello"}).encode())

http.server.HTTPServer(("0.0.0.0", port), Handler).serve_forever()
</script>
<!DOCTYPE html>
<html>
<head><style>body{background:#0a0a0a;color:#e5e5e5;font-family:system-ui;margin:0;padding:16px}</style></head>
<body>
<script>
// VULOS_SANDBOX_URL is injected automatically — use it to call your Python backend
fetch(VULOS_SANDBOX_URL + '/').then(r=>r.json()).then(d=>document.body.innerText=JSON.stringify(d))
</script>
</body>
</html>
</viewport>

Rules:
- Dark theme: background #0a0a0a, text #e5e5e5
- Python backend: use stdlib only (http.server, json, os, math, csv, urllib). Read PORT from env.
- HTML frontend: VULOS_SANDBOX_URL variable is auto-injected to reach the Python backend.
- Only generate viewports when visual output is clearly better than text.
- For simple answers, just respond with plain text.

## OS Control
You can control the OS by including <os-action> blocks:
- <os-action type="open-app" app_id="terminal"/> — open an app
- <os-action type="close-app" app_id="terminal"/> — close an app
- <os-action type="notify" title="Done" body="Your task is complete" level="info"/>
- <os-action type="energy-mode" mode="saver"/> — change power mode

You can include multiple actions in one response alongside text and viewports.`),
	}

	// llmux sovereign gateway (opt-in). When the llmux URL is set, route the
	// assistant's completions through the on-box llmux OpenAI-compatible gateway
	// — the single sovereign choke point that enforces default-deny egress and
	// captures observability — instead of talking to Ollama directly. llmux runs
	// on the instance (e.g. http://localhost:4000/v1), so the endpoint stays
	// loopback and assistant.Guard() still classifies it as EgressOnInstance
	// (sovereign). If it is unset, we keep today's direct-Ollama default
	// untouched. The guard is NOT weakened: pointing this at a non-local host
	// would (correctly) be blocked unless external egress is opted into.
	//
	// Canonical variable: LLMUX_URL (matches internal/llmuxclient); VULOS_LLMUX_URL
	// is accepted as an alias so setting either name works. Same for the key.
	if raw := env.FirstNonEmptyEnv("LLMUX_URL", "VULOS_LLMUX_URL"); raw != "" {
		cfg.Provider = ProviderCustom
		cfg.Endpoint = normalizeOpenAIBase(raw)
		if k := env.FirstNonEmptyEnv("LLMUX_KEY", "VULOS_LLMUX_KEY"); k != "" {
			cfg.APIKey = k
		}
	}

	return cfg
}

// normalizeOpenAIBase turns an OpenAI-compatible base URL (e.g.
// "http://localhost:4000/v1") into the origin form that completeOpenAI /
// streamOpenAI expect for ProviderCustom: those append "/v1/chat/completions"
// themselves, so a trailing "/v1" here would double it. We strip a trailing
// "/v1" and any trailing slashes; other paths are left intact.
func normalizeOpenAIBase(base string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	base = strings.TrimSuffix(base, "/v1")
	return strings.TrimRight(base, "/")
}

// Complete sends a non-streaming completion request.
func (s *Service) Complete(ctx context.Context, cfg Config, req CompletionRequest) (string, error) {
	req.Stream = false
	switch cfg.Provider {
	case ProviderClaude:
		return s.completeClaude(ctx, cfg, req)
	case ProviderOpenAI, ProviderCustom:
		return s.completeOpenAI(ctx, cfg, req)
	case ProviderOllama:
		return s.completeOllama(ctx, cfg, req)
	default:
		return "", fmt.Errorf("unknown provider: %s", cfg.Provider)
	}
}

// Stream sends a streaming completion, calling onChunk for each piece.
func (s *Service) Stream(ctx context.Context, cfg Config, req CompletionRequest, onChunk func(StreamChunk)) error {
	req.Stream = true
	switch cfg.Provider {
	case ProviderClaude:
		return s.streamClaude(ctx, cfg, req, onChunk)
	case ProviderOpenAI, ProviderCustom:
		return s.streamOpenAI(ctx, cfg, req, onChunk)
	case ProviderOllama:
		return s.streamOllama(ctx, cfg, req, onChunk)
	default:
		return fmt.Errorf("unknown provider: %s", cfg.Provider)
	}
}

// HealthCheck verifies the provider is reachable.
func (s *Service) HealthCheck(ctx context.Context, cfg Config) error {
	switch cfg.Provider {
	case ProviderClaude:
		if cfg.APIKey == "" {
			return fmt.Errorf("claude API key not set")
		}
		return nil
	case ProviderOpenAI:
		if cfg.APIKey == "" {
			return fmt.Errorf("openai API key not set")
		}
		return nil
	case ProviderOllama:
		req, _ := http.NewRequestWithContext(ctx, "GET", cfg.Endpoint+"/api/tags", nil)
		resp, err := s.client.Do(req)
		if err != nil {
			return fmt.Errorf("ollama unreachable: %w", err)
		}
		resp.Body.Close()
		return nil
	case ProviderCustom:
		return nil
	}
	return fmt.Errorf("unknown provider")
}

// --- Claude (Anthropic) ---

func (s *Service) completeClaude(ctx context.Context, cfg Config, req CompletionRequest) (string, error) {
	body := map[string]any{
		"model":      cfg.Model,
		"max_tokens": orDefault(req.MaxTokens, 2048),
		"messages":   filterMessages(req.Messages, "system"),
	}
	if cfg.System != "" {
		body["system"] = cfg.System
	}

	data, _ := json.Marshal(body)
	httpReq, _ := http.NewRequestWithContext(ctx, "POST", "https://api.anthropic.com/v1/messages", bytes.NewReader(data))
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", cfg.APIKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")

	resp, err := s.client.Do(httpReq)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("claude error %d: %s", resp.StatusCode, b)
	}

	var result struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	json.NewDecoder(resp.Body).Decode(&result)
	if len(result.Content) > 0 {
		return result.Content[0].Text, nil
	}
	return "", nil
}

func (s *Service) streamClaude(ctx context.Context, cfg Config, req CompletionRequest, onChunk func(StreamChunk)) error {
	body := map[string]any{
		"model":      cfg.Model,
		"max_tokens": orDefault(req.MaxTokens, 2048),
		"messages":   filterMessages(req.Messages, "system"),
		"stream":     true,
	}
	if cfg.System != "" {
		body["system"] = cfg.System
	}

	data, _ := json.Marshal(body)
	httpReq, _ := http.NewRequestWithContext(ctx, "POST", "https://api.anthropic.com/v1/messages", bytes.NewReader(data))
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", cfg.APIKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")

	resp, err := s.client.Do(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	// A non-2xx status returns a JSON ERROR body, not an SSE stream. Without this
	// check the scanner skips every (non-"data: ") line, the loop ends, and the
	// function returns nil — a SILENT SUCCESS with zero content, so the caller
	// (AgentTurnStream) streams an empty answer instead of surfacing the failure.
	// Mirror completeClaude, which already fails closed on non-200.
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("claude stream error %d: %s", resp.StatusCode, b)
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), maxStreamLine)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := line[6:]
		var event struct {
			Type  string `json:"type"`
			Delta struct {
				Text string `json:"text"`
			} `json:"delta"`
		}
		json.Unmarshal([]byte(payload), &event)
		if event.Type == "content_block_delta" {
			onChunk(StreamChunk{Content: event.Delta.Text})
		}
		if event.Type == "message_stop" {
			onChunk(StreamChunk{Done: true})
			return nil
		}
	}
	// A mid-stream read error (dropped connection, or a line over maxStreamLine)
	// ends the scan early. Surface it instead of silently returning a truncated
	// answer as if it were complete.
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("claude stream read: %w", err)
	}
	onChunk(StreamChunk{Done: true})
	return nil
}

// --- OpenAI / Custom ---

func (s *Service) completeOpenAI(ctx context.Context, cfg Config, req CompletionRequest) (string, error) {
	endpoint := "https://api.openai.com/v1/chat/completions"
	if cfg.Provider == ProviderCustom && cfg.Endpoint != "" {
		endpoint = strings.TrimRight(cfg.Endpoint, "/") + "/v1/chat/completions"
	}

	msgs := req.Messages
	if cfg.System != "" {
		msgs = append([]Message{{Role: "system", Content: cfg.System}}, msgs...)
	}

	body := map[string]any{
		"model":    cfg.Model,
		"messages": msgs,
	}
	if req.MaxTokens > 0 {
		body["max_tokens"] = req.MaxTokens
	}

	data, _ := json.Marshal(body)
	httpReq, _ := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(data))
	httpReq.Header.Set("Content-Type", "application/json")
	if cfg.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	}

	resp, err := s.client.Do(httpReq)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("openai error %d: %s", resp.StatusCode, b)
	}

	var result struct {
		Choices []struct {
			Message struct{ Content string } `json:"message"`
		} `json:"choices"`
	}
	json.NewDecoder(resp.Body).Decode(&result)
	if len(result.Choices) > 0 {
		return result.Choices[0].Message.Content, nil
	}
	return "", nil
}

func (s *Service) streamOpenAI(ctx context.Context, cfg Config, req CompletionRequest, onChunk func(StreamChunk)) error {
	endpoint := "https://api.openai.com/v1/chat/completions"
	if cfg.Provider == ProviderCustom && cfg.Endpoint != "" {
		endpoint = strings.TrimRight(cfg.Endpoint, "/") + "/v1/chat/completions"
	}

	msgs := req.Messages
	if cfg.System != "" {
		msgs = append([]Message{{Role: "system", Content: cfg.System}}, msgs...)
	}

	body := map[string]any{
		"model":    cfg.Model,
		"messages": msgs,
		"stream":   true,
	}

	data, _ := json.Marshal(body)
	httpReq, _ := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(data))
	httpReq.Header.Set("Content-Type", "application/json")
	if cfg.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	}

	resp, err := s.client.Do(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	// A non-2xx status returns a JSON ERROR body, not an SSE stream — see the note
	// in streamClaude. Without this the loop skips the error body and returns nil,
	// so the caller streams an empty answer instead of surfacing a 401/429/5xx.
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("openai stream error %d: %s", resp.StatusCode, b)
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), maxStreamLine)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := line[6:]
		if payload == "[DONE]" {
			onChunk(StreamChunk{Done: true})
			return nil
		}
		var event struct {
			Choices []struct {
				Delta struct{ Content string } `json:"delta"`
			} `json:"choices"`
		}
		json.Unmarshal([]byte(payload), &event)
		if len(event.Choices) > 0 && event.Choices[0].Delta.Content != "" {
			onChunk(StreamChunk{Content: event.Choices[0].Delta.Content})
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("openai stream read: %w", err)
	}
	onChunk(StreamChunk{Done: true})
	return nil
}

// --- Ollama ---

func (s *Service) completeOllama(ctx context.Context, cfg Config, req CompletionRequest) (string, error) {
	msgs := req.Messages
	if cfg.System != "" {
		msgs = append([]Message{{Role: "system", Content: cfg.System}}, msgs...)
	}

	body := map[string]any{
		"model":    cfg.Model,
		"messages": msgs,
		"stream":   false,
	}

	data, _ := json.Marshal(body)
	httpReq, _ := http.NewRequestWithContext(ctx, "POST", cfg.Endpoint+"/api/chat", bytes.NewReader(data))
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := s.client.Do(httpReq)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var result struct {
		Message struct{ Content string } `json:"message"`
	}
	json.NewDecoder(resp.Body).Decode(&result)
	return result.Message.Content, nil
}

func (s *Service) streamOllama(ctx context.Context, cfg Config, req CompletionRequest, onChunk func(StreamChunk)) error {
	msgs := req.Messages
	if cfg.System != "" {
		msgs = append([]Message{{Role: "system", Content: cfg.System}}, msgs...)
	}

	body := map[string]any{
		"model":    cfg.Model,
		"messages": msgs,
		"stream":   true,
	}

	data, _ := json.Marshal(body)
	httpReq, _ := http.NewRequestWithContext(ctx, "POST", cfg.Endpoint+"/api/chat", bytes.NewReader(data))
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := s.client.Do(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	// Non-2xx from Ollama is a plain error body, not an NDJSON stream. Surface it
	// rather than returning an empty "success" (see streamClaude for why).
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("ollama stream error %d: %s", resp.StatusCode, b)
	}

	decoder := json.NewDecoder(resp.Body)
	for {
		var event struct {
			Message struct{ Content string } `json:"message"`
			Done    bool                     `json:"done"`
			Error   string                   `json:"error"`
		}
		if err := decoder.Decode(&event); err != nil {
			// Clean end of stream (server closed after the last object) is success;
			// a genuine read/parse error mid-stream is surfaced, not swallowed as a
			// silent truncation.
			if err == io.EOF {
				break
			}
			return fmt.Errorf("ollama stream read: %w", err)
		}
		if event.Error != "" {
			return fmt.Errorf("ollama stream error: %s", event.Error)
		}
		if event.Message.Content != "" {
			onChunk(StreamChunk{Content: event.Message.Content})
		}
		if event.Done {
			onChunk(StreamChunk{Done: true})
			return nil
		}
	}
	onChunk(StreamChunk{Done: true})
	return nil
}

func filterMessages(msgs []Message, excludeRole string) []Message {
	var filtered []Message
	for _, m := range msgs {
		if m.Role != excludeRole {
			filtered = append(filtered, m)
		}
	}
	return filtered
}

func orDefault(v, def int) int {
	if v > 0 {
		return v
	}
	return def
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
