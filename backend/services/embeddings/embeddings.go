package embeddings

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

// Embedder generates vector embeddings for text.
// Supports multiple backends: Ollama (local), OpenAI-compatible APIs.
type Embedder struct {
	backend  string
	model    string
	endpoint string
	apiKey   string
	client   *http.Client
	dim      int
}

type Config struct {
	Backend  string // "ollama" or "openai"
	Model    string // e.g., "nomic-embed-text", "all-minilm", "text-embedding-3-small"
	Endpoint string // e.g., "http://localhost:11434" for Ollama
	APIKey   string // for OpenAI-compatible APIs
}

func DefaultConfig() Config {
	return Config{
		Backend:  getenv("EMBED_BACKEND", "ollama"),
		Model:    getenv("EMBED_MODEL", "nomic-embed-text"),
		Endpoint: getenv("EMBED_ENDPOINT", "http://localhost:11434"),
		APIKey:   os.Getenv("EMBED_API_KEY"),
	}
}

func New(cfg Config) *Embedder {
	return &Embedder{
		backend:  cfg.Backend,
		model:    cfg.Model,
		endpoint: cfg.Endpoint,
		apiKey:   cfg.APIKey,
		client:   &http.Client{Timeout: 30 * time.Second},
	}
}

// Embed returns the embedding vector for the given text.
func (e *Embedder) Embed(ctx context.Context, text string) ([]float32, error) {
	switch e.backend {
	case "ollama":
		return e.embedOllama(ctx, text)
	case "openai":
		return e.embedOpenAI(ctx, text)
	default:
		return nil, fmt.Errorf("unknown embedding backend: %s", e.backend)
	}
}

// Dimension returns the embedding dimension (detected on first call).
func (e *Embedder) Dimension() int {
	return e.dim
}

// HealthCheck verifies the embedding backend is reachable.
func (e *Embedder) HealthCheck(ctx context.Context) error {
	switch e.backend {
	case "ollama":
		req, _ := http.NewRequestWithContext(ctx, "GET", e.endpoint+"/api/tags", nil)
		resp, err := e.client.Do(req)
		if err != nil {
			return fmt.Errorf("ollama unreachable at %s: %w", e.endpoint, err)
		}
		resp.Body.Close()
		return nil
	case "openai":
		if e.apiKey == "" {
			return fmt.Errorf("openai API key not configured")
		}
		return nil
	default:
		return fmt.Errorf("unknown backend: %s", e.backend)
	}
}

// --- Ollama backend ---

type ollamaEmbedReq struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
}

type ollamaEmbedResp struct {
	Embedding []float32 `json:"embedding"`
}

func (e *Embedder) embedOllama(ctx context.Context, text string) ([]float32, error) {
	body, _ := json.Marshal(ollamaEmbedReq{Model: e.model, Prompt: text})
	req, err := http.NewRequestWithContext(ctx, "POST", e.endpoint+"/api/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := e.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ollama request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("ollama error %d: %s", resp.StatusCode, b)
	}

	var result ollamaEmbedResp
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode ollama response: %w", err)
	}

	if e.dim == 0 && len(result.Embedding) > 0 {
		e.dim = len(result.Embedding)
	}
	return result.Embedding, nil
}

// --- OpenAI-compatible backend ---

type openaiEmbedReq struct {
	Model string `json:"model"`
	Input string `json:"input"`
}

type openaiEmbedResp struct {
	Data []struct {
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
}

func (e *Embedder) embedOpenAI(ctx context.Context, text string) ([]float32, error) {
	body, _ := json.Marshal(openaiEmbedReq{Model: e.model, Input: text})
	req, err := http.NewRequestWithContext(ctx, "POST", e.endpoint+"/v1/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if e.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+e.apiKey)
	}

	resp, err := e.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openai request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("openai error %d: %s", resp.StatusCode, b)
	}

	var result openaiEmbedResp
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode openai response: %w", err)
	}
	if len(result.Data) == 0 {
		return nil, fmt.Errorf("no embedding returned")
	}

	emb := result.Data[0].Embedding
	if e.dim == 0 && len(emb) > 0 {
		e.dim = len(emb)
	}
	return emb, nil
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
