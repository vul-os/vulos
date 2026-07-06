// Package llmuxclient routes all OS LLM/AI calls through the llmux gateway.
//
// Architecture: OS → llmux (LLMUX_URL, alias VULOS_LLMUX_URL) → providers
//
// The llmux gateway is OpenAI-compatible and handles provider management,
// BYO routing, and billing/metering on behalf of the OS. The OS is
// transparent: it proxies requests and streams responses unchanged.
//
// Transcription (Whisper) is NOT routed through llmux — see whisper.go.
// llmux does not expose the multipart /v1/audio/transcriptions shape.
package llmuxclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"vulos/backend/services/env"
)

// Config is the resolved llmux gateway target.
type Config struct {
	// BaseURL is the gateway base URL WITHOUT a trailing "/v1" or "/" — callers
	// append the OpenAI sub-path (e.g. "/v1/chat/completions") themselves.
	// A configured value of "http://llmux:4000/v1" is normalised to
	// "http://llmux:4000".
	BaseURL string
	// Key is the bearer token sent as "Authorization: Bearer <key>".
	// May be empty for an unauthenticated local gateway.
	Key string
}

// FromEnv reads the llmux gateway URL and key from the environment.
//
// The canonical variable is LLMUX_URL; VULOS_LLMUX_URL is accepted as an alias
// (whichever is set non-empty wins, LLMUX_URL first) so that an operator who
// sets either name gets the gateway wired up. The key follows the same rule:
// LLMUX_KEY canonical, VULOS_LLMUX_KEY alias.
//
// It normalises the URL by stripping a trailing slash then a trailing "/v1"
// segment so callers that append "/v1/..." build the right URL regardless of
// whether the operator supplied ".../v1" or just the host root.
//
// Returns ok=false when neither URL variable is set or non-empty. In that case
// the returned Config is zero and Client methods will return errors.
func FromEnv() (Config, bool) {
	raw := env.FirstNonEmptyEnv("LLMUX_URL", "VULOS_LLMUX_URL")
	if raw == "" {
		return Config{}, false
	}
	base := strings.TrimRight(raw, "/")
	base = strings.TrimSuffix(base, "/v1")
	base = strings.TrimRight(base, "/")
	return Config{
		BaseURL: base,
		Key:     env.FirstNonEmptyEnv("LLMUX_KEY", "VULOS_LLMUX_KEY"),
	}, true
}

// Client is an HTTP client for the llmux gateway.
type Client struct {
	cfg        Config
	httpClient *http.Client
}

// New creates a Client using the given Config.
// When cfg.BaseURL is empty (LLMUX_URL unset) every method returns an error.
func New(cfg Config) *Client {
	return &Client{
		cfg:        cfg,
		httpClient: &http.Client{Timeout: 120 * time.Second},
	}
}

// Chat posts body (a raw JSON chat/completions request) to the llmux gateway
// and returns the SSE stream body. The caller must close the returned
// io.ReadCloser when done.
func (c *Client) Chat(ctx context.Context, body []byte) (io.ReadCloser, error) {
	if c.cfg.BaseURL == "" {
		return nil, fmt.Errorf("llmuxclient: LLMUX_URL not configured")
	}
	endpoint := c.cfg.BaseURL + "/v1/chat/completions"
	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.cfg.Key != "" {
		req.Header.Set("Authorization", "Bearer "+c.cfg.Key)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("llmuxclient: chat request failed: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("llmuxclient: gateway returned %d: %s", resp.StatusCode, b)
	}
	return resp.Body, nil
}

// Embed posts an OpenAI-compatible embeddings request to the llmux gateway.
// Returns the embedding vector and the resolved model name.
func (c *Client) Embed(ctx context.Context, model, input string) ([]float32, string, error) {
	if c.cfg.BaseURL == "" {
		return nil, model, fmt.Errorf("llmuxclient: LLMUX_URL not configured")
	}
	if model == "" {
		model = "text-embedding-3-small"
	}
	endpoint := c.cfg.BaseURL + "/v1/embeddings"
	reqBody := map[string]any{
		"model": model,
		"input": input,
	}
	data, _ := json.Marshal(reqBody)
	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(data))
	if err != nil {
		return nil, model, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.cfg.Key != "" {
		req.Header.Set("Authorization", "Bearer "+c.cfg.Key)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, model, fmt.Errorf("llmuxclient: embed request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return nil, model, fmt.Errorf("llmuxclient: gateway returned %d: %s", resp.StatusCode, b)
	}
	var result struct {
		Data []struct {
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
		Embedding []float32 `json:"embedding"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, model, fmt.Errorf("llmuxclient: decode embed response: %w", err)
	}
	if len(result.Data) > 0 {
		return result.Data[0].Embedding, model, nil
	}
	if len(result.Embedding) > 0 {
		return result.Embedding, model, nil
	}
	return nil, model, fmt.Errorf("llmuxclient: empty embedding in response")
}

// Models fetches the model list from the llmux gateway and returns raw JSON
// suitable for proxying to callers.
func (c *Client) Models(ctx context.Context) (json.RawMessage, error) {
	if c.cfg.BaseURL == "" {
		return nil, fmt.Errorf("llmuxclient: LLMUX_URL not configured")
	}
	endpoint := c.cfg.BaseURL + "/v1/models"
	req, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	if err != nil {
		return nil, err
	}
	if c.cfg.Key != "" {
		req.Header.Set("Authorization", "Bearer "+c.cfg.Key)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("llmuxclient: models request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("llmuxclient: gateway returned %d: %s", resp.StatusCode, b)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("llmuxclient: read models response: %w", err)
	}
	return json.RawMessage(raw), nil
}
