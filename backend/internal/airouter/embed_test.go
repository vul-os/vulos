package airouter

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// stubEmbedServer returns an httptest.Server that serves OpenAI-compatible
// embeddings responses for /v1/embeddings.
func stubEmbedServer(t *testing.T, vec []float32) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		type embData struct {
			Embedding []float32 `json:"embedding"`
			Index     int       `json:"index"`
		}
		type resp struct {
			Data []embData `json:"data"`
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp{Data: []embData{{Embedding: vec, Index: 0}}})
	}))
}

// newEmbedTestSetup wires store+router+embedder against a stub provider.
func newEmbedTestSetup(t *testing.T, stubURL string) (*Store, *Router, *Embedder) {
	t.Helper()
	s := newTestStore(t)
	if stubURL != "" {
		_ = s.AddProvider(Provider{
			ID:          "embed-prov",
			DisplayName: "EmbedStub",
			BaseURL:     stubURL,
			APIKeyEnc:   "test-key",
			Models:      []string{"text-embedding-test"},
		})
	}
	r := NewRouter(s)
	e := NewEmbedder(s, r)
	return s, r, e
}

// approxEqual returns true if |a-b| < eps.
func approxEqual(a, b, eps float64) bool {
	return math.Abs(a-b) < eps
}

// ---------------------------------------------------------------------------
// Unit: float32 blob encoding
// ---------------------------------------------------------------------------

func TestFloat32BlobRoundTrip(t *testing.T) {
	original := []float32{0.1, -0.5, 1.0, 3.14159, 0.0}
	blob := float32ToBlob(original)
	got, err := blobToFloat32(blob)
	if err != nil {
		t.Fatalf("blobToFloat32: %v", err)
	}
	if len(got) != len(original) {
		t.Fatalf("length mismatch: got %d, want %d", len(got), len(original))
	}
	for i := range original {
		if got[i] != original[i] {
			t.Errorf("[%d] got %f, want %f", i, got[i], original[i])
		}
	}
}

func TestBlobToFloat32_InvalidLength(t *testing.T) {
	_, err := blobToFloat32([]byte{0x01, 0x02, 0x03}) // 3 bytes — not multiple of 4
	if err == nil {
		t.Fatal("expected error for non-multiple-of-4 blob")
	}
}

// ---------------------------------------------------------------------------
// Unit: cosine similarity
// ---------------------------------------------------------------------------

func TestCosineSimilarity_Identical(t *testing.T) {
	v := []float32{1, 2, 3}
	sim, err := CosineSimilarity(v, v)
	if err != nil {
		t.Fatalf("CosineSimilarity: %v", err)
	}
	if !approxEqual(float64(sim), 1.0, 1e-6) {
		t.Errorf("identical vectors: got %f, want ~1.0", sim)
	}
}

func TestCosineSimilarity_Orthogonal(t *testing.T) {
	a := []float32{1, 0}
	b := []float32{0, 1}
	sim, err := CosineSimilarity(a, b)
	if err != nil {
		t.Fatalf("CosineSimilarity: %v", err)
	}
	if !approxEqual(float64(sim), 0.0, 1e-6) {
		t.Errorf("orthogonal vectors: got %f, want ~0.0", sim)
	}
}

func TestCosineSimilarity_Opposite(t *testing.T) {
	a := []float32{1, 0}
	b := []float32{-1, 0}
	sim, err := CosineSimilarity(a, b)
	if err != nil {
		t.Fatalf("CosineSimilarity: %v", err)
	}
	if !approxEqual(float64(sim), -1.0, 1e-6) {
		t.Errorf("opposite vectors: got %f, want ~-1.0", sim)
	}
}

func TestCosineSimilarity_LengthMismatch(t *testing.T) {
	_, err := CosineSimilarity([]float32{1, 2}, []float32{1, 2, 3})
	if err == nil {
		t.Fatal("expected error for length mismatch")
	}
}

// ---------------------------------------------------------------------------
// Unit: cache key determinism
// ---------------------------------------------------------------------------

func TestEmbedCacheKey_Deterministic(t *testing.T) {
	k1 := embedCacheKey("model-a", "hello world")
	k2 := embedCacheKey("model-a", "hello world")
	if k1 != k2 {
		t.Errorf("cache key not deterministic: %q vs %q", k1, k2)
	}
}

func TestEmbedCacheKey_DifferentModelDifferentKey(t *testing.T) {
	k1 := embedCacheKey("model-a", "hello")
	k2 := embedCacheKey("model-b", "hello")
	if k1 == k2 {
		t.Errorf("different models should produce different cache keys")
	}
}

// ---------------------------------------------------------------------------
// Integration: cache hit/miss
// ---------------------------------------------------------------------------

func TestEmbed_CacheHit_SkipsProvider(t *testing.T) {
	callCount := 0
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		type resp struct {
			Data []struct {
				Embedding []float32 `json:"embedding"`
			} `json:"data"`
		}
		json.NewEncoder(w).Encode(resp{Data: []struct {
			Embedding []float32 `json:"embedding"`
		}{{Embedding: []float32{0.1, 0.2, 0.3}}}})
	}))
	defer stub.Close()

	_, _, e := newEmbedTestSetup(t, stub.URL)

	// First call — provider hit.
	vec1, _, err := e.Embed("text-embedding-test", "hello cache")
	if err != nil {
		t.Fatalf("Embed (first): %v", err)
	}
	if callCount != 1 {
		t.Errorf("expected 1 provider call, got %d", callCount)
	}

	// Second call — should come from cache.
	vec2, _, err := e.Embed("text-embedding-test", "hello cache")
	if err != nil {
		t.Fatalf("Embed (second): %v", err)
	}
	if callCount != 1 {
		t.Errorf("expected still 1 provider call after cache hit, got %d", callCount)
	}

	// Vectors must match.
	if len(vec1) != len(vec2) {
		t.Fatalf("vec length mismatch: %d vs %d", len(vec1), len(vec2))
	}
	for i := range vec1 {
		if vec1[i] != vec2[i] {
			t.Errorf("[%d] vec mismatch: %f vs %f", i, vec1[i], vec2[i])
		}
	}

	hits, misses, _ := e.CacheStats()
	if hits != 1 || misses != 1 {
		t.Errorf("stats: hits=%d misses=%d, want 1/1", hits, misses)
	}
}

func TestEmbed_CacheEvictsExpired(t *testing.T) {
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		type resp struct {
			Data []struct {
				Embedding []float32 `json:"embedding"`
			} `json:"data"`
		}
		json.NewEncoder(w).Encode(resp{Data: []struct {
			Embedding []float32 `json:"embedding"`
		}{{Embedding: []float32{1.0, 2.0}}}})
	}))
	defer stub.Close()

	s, r, e := newEmbedTestSetup(t, stub.URL)

	// Manually insert an already-expired cache row.
	key := embedCacheKey("text-embedding-test", "expire-me")
	blob := float32ToBlob([]float32{9.9, 8.8})
	past := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	_, err := s.db.Exec(
		`INSERT INTO embed_cache(cache_key, model, input_hash, vector_blob, dim, created_at, expires_at)
		 VALUES(?,?,?,?,?,?,?)`,
		key, "text-embedding-test", "x", blob, 2, past, past,
	)
	if err != nil {
		t.Fatalf("insert expired row: %v", err)
	}

	// Embed should miss (expired) and call provider.
	_ = r // suppress unused warning
	vec, _, err := e.Embed("text-embedding-test", "expire-me")
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	// Should get the stub's vector (1.0, 2.0) not the stale (9.9, 8.8).
	if len(vec) < 2 || !approxEqual(float64(vec[0]), 1.0, 1e-5) {
		t.Errorf("expected fresh vector from provider, got %v", vec)
	}
}

func TestEmbed_NoProvidersError(t *testing.T) {
	s := newTestStore(t)
	r := NewRouter(s)
	e := NewEmbedder(s, r)

	_, _, err := e.Embed("some-model", "hello")
	if err == nil {
		t.Fatal("expected error with no providers configured")
	}
	if !strings.Contains(err.Error(), "no providers") {
		t.Errorf("unexpected error message: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Integration: cloud mode proxies to VULOS_AI_EMBED_URL
// ---------------------------------------------------------------------------

func TestEmbed_CloudMode_ProxiesWithDeviceCert(t *testing.T) {
	var gotAuthHeader string
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuthHeader = r.Header.Get("Authorization")
		type resp struct {
			Embedding []float32 `json:"embedding"`
		}
		json.NewEncoder(w).Encode(resp{Embedding: []float32{0.5, 0.6}})
	}))
	defer stub.Close()

	os.Setenv("VULOS_AI_EMBED_URL", stub.URL)
	defer os.Unsetenv("VULOS_AI_EMBED_URL")

	s := newTestStore(t)
	_ = s.SetConfig(Config{Mode: ModeCloud, ActiveModel: "cloud-embed-model"})
	r := NewRouter(s)
	r.DeviceCert = []byte("fake-cert")
	e := NewEmbedder(s, r)

	vec, model, err := e.Embed("cloud-embed-model", "cloud test input")
	if err != nil {
		t.Fatalf("Embed (cloud): %v", err)
	}
	if len(vec) != 2 {
		t.Errorf("expected 2-element vector, got %d", len(vec))
	}
	if model != "cloud-embed-model" {
		t.Errorf("model: got %q", model)
	}
	if !strings.HasPrefix(gotAuthHeader, "VulosDevice ") {
		t.Errorf("Authorization header: got %q, want VulosDevice prefix", gotAuthHeader)
	}
}

// ---------------------------------------------------------------------------
// HTTP handler: POST /api/ai/embed
// ---------------------------------------------------------------------------

func TestHandleEmbed_ValidRequest(t *testing.T) {
	vec := []float32{0.1, 0.2, 0.3}
	stub := stubEmbedServer(t, vec)
	defer stub.Close()

	s := newTestStore(t)
	_ = s.AddProvider(Provider{
		ID:        "ep",
		BaseURL:   stub.URL,
		APIKeyEnc: "k",
		Models:    []string{"text-embedding-test"},
	})
	mux := http.NewServeMux()
	RegisterHandlers(mux, NewRouter(s))

	body := `{"input":"hello","model":"text-embedding-test"}`
	req := httptest.NewRequest("POST", "/api/ai/embed", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	var resp EmbedResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Embedding) != 3 {
		t.Errorf("embedding length: got %d, want 3", len(resp.Embedding))
	}
	if !approxEqual(float64(resp.Embedding[0]), 0.1, 1e-5) {
		t.Errorf("embedding[0]: got %f, want ~0.1", resp.Embedding[0])
	}
	if resp.Model != "text-embedding-test" {
		t.Errorf("model: got %q, want text-embedding-test", resp.Model)
	}
}

func TestHandleEmbed_EmptyInput_400(t *testing.T) {
	s := newTestStore(t)
	mux := http.NewServeMux()
	RegisterHandlers(mux, NewRouter(s))

	req := httptest.NewRequest("POST", "/api/ai/embed", strings.NewReader(`{"input":""}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d, want 400", rr.Code)
	}
}

func TestHandleEmbed_InvalidJSON_400(t *testing.T) {
	s := newTestStore(t)
	mux := http.NewServeMux()
	RegisterHandlers(mux, NewRouter(s))

	req := httptest.NewRequest("POST", "/api/ai/embed", strings.NewReader("{bad"))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d, want 400", rr.Code)
	}
}

// ---------------------------------------------------------------------------
// HTTP handler: GET /api/ai/embed/stats
// ---------------------------------------------------------------------------

func TestHandleEmbedStats(t *testing.T) {
	callN := 0
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callN++
		fmt.Fprintf(w, `{"data":[{"embedding":[0.1,0.2]}]}`)
	}))
	defer stub.Close()

	s := newTestStore(t)
	_ = s.AddProvider(Provider{
		ID:      "sp",
		BaseURL: stub.URL,
		Models:  []string{"m"},
	})
	router := NewRouter(s)
	h := NewHandler(router)

	// Trigger one embed (miss then cached) using the same handler's embedder.
	_, _, _ = h.embedder.Embed("m", "stats test")
	_, _, _ = h.embedder.Embed("m", "stats test")

	// Mount the handler directly (not via RegisterHandlers, to reuse the same h).
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/ai/embed/stats", h.handleEmbedStats)

	req := httptest.NewRequest("GET", "/api/ai/embed/stats", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	var resp map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode stats: %v", err)
	}
	if _, ok := resp["cache_hits"]; !ok {
		t.Errorf("expected cache_hits in response, got %v", resp)
	}
	if _, ok := resp["total_stored"]; !ok {
		t.Errorf("expected total_stored in response, got %v", resp)
	}
	hits := int64(resp["cache_hits"].(float64))
	misses := int64(resp["cache_misses"].(float64))
	if hits != 1 || misses != 1 {
		t.Errorf("stats: hits=%d misses=%d, want 1/1", hits, misses)
	}
}
