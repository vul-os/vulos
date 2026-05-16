package embeddings

import (
	"context"
	"math"
	"os"
	"testing"
)

// TestDefaultConfig verifies environment-variable overrides and fallback defaults.
func TestDefaultConfig(t *testing.T) {
	// Restore env after test
	restore := func(key, val string) { os.Setenv(key, val) }
	orig := map[string]string{
		"EMBED_BACKEND":  os.Getenv("EMBED_BACKEND"),
		"EMBED_MODEL":    os.Getenv("EMBED_MODEL"),
		"EMBED_ENDPOINT": os.Getenv("EMBED_ENDPOINT"),
		"EMBED_API_KEY":  os.Getenv("EMBED_API_KEY"),
	}
	defer func() {
		for k, v := range orig {
			restore(k, v)
		}
	}()

	t.Run("defaults when env unset", func(t *testing.T) {
		os.Unsetenv("EMBED_BACKEND")
		os.Unsetenv("EMBED_MODEL")
		os.Unsetenv("EMBED_ENDPOINT")
		os.Unsetenv("EMBED_API_KEY")

		cfg := DefaultConfig()
		if cfg.Backend != "ollama" {
			t.Errorf("Backend: got %q, want %q", cfg.Backend, "ollama")
		}
		if cfg.Model != "nomic-embed-text" {
			t.Errorf("Model: got %q, want %q", cfg.Model, "nomic-embed-text")
		}
		if cfg.Endpoint != "http://localhost:11434" {
			t.Errorf("Endpoint: got %q, want %q", cfg.Endpoint, "http://localhost:11434")
		}
		if cfg.APIKey != "" {
			t.Errorf("APIKey: expected empty, got %q", cfg.APIKey)
		}
	})

	t.Run("env overrides applied", func(t *testing.T) {
		os.Setenv("EMBED_BACKEND", "openai")
		os.Setenv("EMBED_MODEL", "text-embedding-3-small")
		os.Setenv("EMBED_ENDPOINT", "https://api.openai.com")
		os.Setenv("EMBED_API_KEY", "sk-test")

		cfg := DefaultConfig()
		if cfg.Backend != "openai" {
			t.Errorf("Backend: got %q, want %q", cfg.Backend, "openai")
		}
		if cfg.Model != "text-embedding-3-small" {
			t.Errorf("Model: got %q, want %q", cfg.Model, "text-embedding-3-small")
		}
		if cfg.Endpoint != "https://api.openai.com" {
			t.Errorf("Endpoint: got %q, want %q", cfg.Endpoint, "https://api.openai.com")
		}
		if cfg.APIKey != "sk-test" {
			t.Errorf("APIKey: got %q, want %q", cfg.APIKey, "sk-test")
		}
	})
}

// TestNew verifies that New constructs an Embedder without panicking and that
// Dimension() starts at zero before any embed call.
func TestNew(t *testing.T) {
	cfg := Config{
		Backend:  "ollama",
		Model:    "nomic-embed-text",
		Endpoint: "http://localhost:11434",
	}
	e := New(cfg)
	if e == nil {
		t.Fatal("New returned nil")
	}
	if d := e.Dimension(); d != 0 {
		t.Errorf("Dimension before first embed: got %d, want 0", d)
	}
}

// TestHealthCheck_UnknownBackend verifies error returned for bad backend name.
func TestHealthCheck_UnknownBackend(t *testing.T) {
	e := New(Config{Backend: "bogus", Model: "m", Endpoint: "http://x"})
	err := e.HealthCheck(context.Background())
	if err == nil {
		t.Fatal("expected error for unknown backend, got nil")
	}
}

// TestHealthCheck_OpenAINoKey verifies that openai backend without API key fails.
func TestHealthCheck_OpenAINoKey(t *testing.T) {
	e := New(Config{Backend: "openai", Model: "text-embedding-3-small", Endpoint: "https://api.openai.com"})
	err := e.HealthCheck(context.Background())
	if err == nil {
		t.Fatal("expected error for missing openai API key, got nil")
	}
}

// TestHealthCheck_OpenAIWithKey verifies that openai backend with key passes the key check.
func TestHealthCheck_OpenAIWithKey(t *testing.T) {
	e := New(Config{Backend: "openai", Model: "text-embedding-3-small", Endpoint: "https://api.openai.com", APIKey: "sk-fake"})
	// HealthCheck for openai with a key set only validates the key presence,
	// it does not make a network call — so it must return nil.
	err := e.HealthCheck(context.Background())
	if err != nil {
		t.Errorf("expected nil for openai with key, got: %v", err)
	}
}

// TestHealthCheck_OllamaUnreachable verifies that a health check against a
// non-listening endpoint returns an error (no network required — connection refused).
func TestHealthCheck_OllamaUnreachable(t *testing.T) {
	e := New(Config{Backend: "ollama", Model: "nomic-embed-text", Endpoint: "http://127.0.0.1:19999"})
	err := e.HealthCheck(context.Background())
	if err == nil {
		t.Fatal("expected error for unreachable ollama, got nil")
	}
}

// TestEmbed_OllamaSkipIfNoBackend skips when no real Ollama is running.
func TestEmbed_OllamaSkipIfNoBackend(t *testing.T) {
	if os.Getenv("EMBED_BACKEND") == "" {
		t.Skip("no EMBED_BACKEND set — skipping live embed test")
	}
	cfg := DefaultConfig()
	e := New(cfg)
	ctx := context.Background()
	vec, err := e.Embed(ctx, "hello world")
	if err != nil {
		t.Fatalf("Embed error: %v", err)
	}
	if len(vec) == 0 {
		t.Fatal("expected non-empty embedding")
	}
	if e.Dimension() != len(vec) {
		t.Errorf("Dimension() = %d, want %d", e.Dimension(), len(vec))
	}
}

// TestEmbed_UnknownBackend verifies Embed returns an error for unknown backends.
func TestEmbed_UnknownBackend(t *testing.T) {
	e := New(Config{Backend: "nonexistent", Model: "m", Endpoint: "http://x"})
	_, err := e.Embed(context.Background(), "hello")
	if err == nil {
		t.Fatal("expected error from unknown backend, got nil")
	}
}

// TestNormalize verifies the internal normalize helper produces a unit vector.
func TestNormalize(t *testing.T) {
	v := []float32{3, 4} // L2 norm = 5
	n := normalize(v)
	if len(n) != 2 {
		t.Fatalf("expected length 2, got %d", len(n))
	}
	got := math.Sqrt(float64(n[0])*float64(n[0]) + float64(n[1])*float64(n[1]))
	if math.Abs(got-1.0) > 1e-6 {
		t.Errorf("normalized vector L2 norm = %f, want ~1.0", got)
	}
	if math.Abs(float64(n[0])-0.6) > 1e-6 || math.Abs(float64(n[1])-0.8) > 1e-6 {
		t.Errorf("normalized values wrong: got [%f, %f], want [0.6, 0.8]", n[0], n[1])
	}
}

// TestNormalize_ZeroVector verifies normalize handles the zero vector gracefully.
func TestNormalize_ZeroVector(t *testing.T) {
	v := []float32{0, 0, 0}
	n := normalize(v)
	// Should not panic; all elements should remain 0.
	for i, x := range n {
		if x != 0 {
			t.Errorf("index %d: got %f, want 0 for zero vector", i, x)
		}
	}
}

// TestGetenv verifies the getenv helper returns the value when set and fallback otherwise.
func TestGetenv(t *testing.T) {
	const key = "VULOS_TEST_GETENV_XYZ"
	os.Unsetenv(key)
	if got := getenv(key, "default"); got != "default" {
		t.Errorf("got %q, want %q", got, "default")
	}
	os.Setenv(key, "override")
	defer os.Unsetenv(key)
	if got := getenv(key, "default"); got != "override" {
		t.Errorf("got %q, want %q", got, "override")
	}
}
