package airouter

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Message is a single chat message.
type Message struct {
	Role    string `json:"role"`    // "system" | "user" | "assistant"
	Content string `json:"content"`
}

// ChatRequest is the input to Route.
type ChatRequest struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
	Stream   bool      `json:"stream"`
}

// Router is the AI request router. It uses a Store for config and a DeviceCert
// source for cloud-mode authentication.
type Router struct {
	store      *Store
	httpClient *http.Client
	// DeviceCert is the PEM-encoded OS device certificate used in cloud mode.
	// Setting this field replaces any previously-set CertProv with a staticCertProvider.
	// Leave nil to disable cloud mode (requests will fail with an error).
	// Deprecated: prefer setting CertProv for cert refresh support.
	DeviceCert []byte

	// CertProv supplies device certificates for cloud mode.  If nil and DeviceCert
	// is set, a staticCertProvider is used automatically.
	CertProv CertProvider

	// UsageReporter receives usage records after each successful cloud request.
	// Defaults to a no-op reporter.
	UsageReporter UsageReporter
}

// NewRouter creates a Router backed by the given Store.
func NewRouter(store *Store) *Router {
	return &Router{
		store:      store,
		httpClient: &http.Client{Timeout: 120 * time.Second},
	}
}

// cloudProxy returns a CloudProxy wired with the Router's cert provider and
// usage reporter.  It falls back to DeviceCert if CertProv is not set.
func (r *Router) cloudProxy() *CloudProxy {
	prov := r.CertProv
	if prov == nil {
		prov = &staticCertProvider{cert: r.DeviceCert}
	}
	return newCloudProxy(r.httpClient, prov, r.UsageReporter)
}

// Route dispatches a chat request to the appropriate backend.
// The returned io.ReadCloser carries raw Server-Sent Events (SSE) bytes.
// Callers must close the stream when done.
//
//   - byo mode: selects the provider whose Models list contains req.Model (or
//     the first provider if the list is empty / unspecified) and forwards to
//     its OpenAI-compatible /v1/chat/completions endpoint.
//   - cloud mode: POSTs to the Vulos cloud proxy URL
//     (VULOS_AI_PROXY_URL env var, default https://api.vulos.org/ai/proxy),
//     adding the device-cert header for auth.
func (r *Router) Route(ctx context.Context, req ChatRequest) (io.ReadCloser, error) {
	cfg, err := r.store.GetConfig()
	if err != nil {
		return nil, fmt.Errorf("airouter: load config: %w", err)
	}

	// When the llmux gateway is configured (LLMUX_URL set), route chat through
	// it regardless of mode — llmux is OpenAI-compatible, so this is just a
	// (base_url, key) swap.  Falls through to the normal byo/cloud paths when
	// unset, keeping the OS standalone-capable.
	if lm, ok := llmuxEndpoint(); ok {
		model := req.Model
		if model == "" {
			model = cfg.ActiveModel
		}
		return r.callOpenAICompat(ctx, lm.BaseURL, lm.Key, model, req)
	}

	switch cfg.Mode {
	case ModeCloud:
		return r.routeCloud(ctx, req)
	default:
		return r.routeBYO(ctx, req, cfg)
	}
}

// --- BYO mode ---

func (r *Router) routeBYO(ctx context.Context, req ChatRequest, cfg Config) (io.ReadCloser, error) {
	providers, err := r.store.ListProviders()
	if err != nil {
		return nil, fmt.Errorf("airouter: list providers: %w", err)
	}
	if len(providers) == 0 {
		return nil, fmt.Errorf("airouter: no providers configured")
	}

	// Select provider: first that claims the requested model, else first provider.
	provider := providers[0]
	if req.Model != "" {
		for _, p := range providers {
			for _, m := range p.Models {
				if m == req.Model {
					provider = p
					break
				}
			}
		}
	}

	model := req.Model
	if model == "" {
		model = cfg.ActiveModel
	}
	if model == "" && len(provider.Models) > 0 {
		model = provider.Models[0]
	}

	return r.callOpenAICompat(ctx, provider.BaseURL, provider.APIKeyEnc, model, req)
}

// callOpenAICompat POSTs to an OpenAI-compatible /v1/chat/completions endpoint
// and returns a raw SSE stream.
func (r *Router) callOpenAICompat(ctx context.Context, baseURL, apiKey, model string, req ChatRequest) (io.ReadCloser, error) {
	endpoint := strings.TrimRight(baseURL, "/") + "/v1/chat/completions"

	body := map[string]any{
		"model":    model,
		"messages": req.Messages,
		"stream":   true,
	}
	data, _ := json.Marshal(body)

	httpReq, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	}

	resp, err := r.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("airouter byo: request failed: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("airouter byo: provider returned %d: %s", resp.StatusCode, b)
	}
	return resp.Body, nil
}

// --- Cloud mode ---

// cloudProxyURL returns the Vulos cloud proxy URL (overridable for testing).
func cloudProxyURL() string {
	return getenv("VULOS_AI_PROXY_URL", "https://api.vulos.org/ai/proxy")
}

func (r *Router) routeCloud(ctx context.Context, req ChatRequest) (io.ReadCloser, error) {
	return r.cloudProxy().ProxyChat(ctx, req)
}

// encodeDeviceCert base64-URL-encodes the raw PEM bytes for use in a header.
// We use a simple hex representation to avoid a base64 import; production can
// swap to base64.StdEncoding.
func encodeDeviceCert(pem []byte) string {
	// Use hex for simplicity — the cloud side just needs to decode it.
	out := make([]byte, len(pem)*2)
	const hextable = "0123456789abcdef"
	for i, b := range pem {
		out[i*2] = hextable[b>>4]
		out[i*2+1] = hextable[b&0x0f]
	}
	return string(out)
}

// --- SSE helpers (used by callers or tests) ---

// SSEEvent is a single parsed SSE event.
type SSEEvent struct {
	Data string
	Done bool
}

// ReadSSE reads one SSE event from the stream.  Returns (zero, io.EOF) at end.
func ReadSSE(r io.Reader) (SSEEvent, error) {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data: ") {
			payload := line[6:]
			if payload == "[DONE]" {
				return SSEEvent{Done: true}, nil
			}
			return SSEEvent{Data: payload}, nil
		}
	}
	if err := scanner.Err(); err != nil {
		return SSEEvent{}, err
	}
	return SSEEvent{Done: true}, io.EOF
}

// DrainSSE reads all SSE chunks from stream and concatenates the content fields.
// Useful in tests. Closes stream when done.
func DrainSSE(stream io.ReadCloser) (string, error) {
	defer stream.Close()
	var sb strings.Builder
	scanner := bufio.NewScanner(stream)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := line[6:]
		if payload == "[DONE]" {
			break
		}
		var ev struct {
			Choices []struct {
				Delta struct{ Content string } `json:"delta"`
			} `json:"choices"`
			// Also handle non-streaming full response
			Content string `json:"content,omitempty"`
		}
		if err := json.Unmarshal([]byte(payload), &ev); err == nil {
			if len(ev.Choices) > 0 {
				sb.WriteString(ev.Choices[0].Delta.Content)
			} else if ev.Content != "" {
				sb.WriteString(ev.Content)
			}
		}
	}
	return sb.String(), scanner.Err()
}

// staticSSEStream wraps a pre-built SSE byte slice as an io.ReadCloser.
// Used in tests to return canned responses.
func staticSSEStream(s string) io.ReadCloser {
	return io.NopCloser(strings.NewReader(s))
}

