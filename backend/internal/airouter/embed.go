package airouter

// AIROT-08: embed endpoint + vector store.
//
// Exposes:
//   POST /api/ai/embed       body: {input:string, model?:string}
//                            returns: {embedding:[float32...], model:string}
//   GET  /api/ai/embed/stats returns: {cache_hits, cache_misses, total_stored}
//
// Embeddings are cached in SQLite keyed by SHA256(model + "\x00" + input)
// with a 30-day TTL. In byo mode the request is forwarded to the provider's
// OpenAI-compatible /v1/embeddings endpoint. In cloud mode it is proxied to
// POST https://api.vulos.org/ai/embed (VULOS_AI_EMBED_URL env override).

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

// embedTTL is the default cache TTL for stored embeddings.
const embedTTL = 30 * 24 * time.Hour

// EmbedRequest is the HTTP request body for POST /api/ai/embed.
type EmbedRequest struct {
	Input string `json:"input"`
	Model string `json:"model,omitempty"`
}

// EmbedResponse is returned by POST /api/ai/embed.
type EmbedResponse struct {
	Embedding []float32 `json:"embedding"`
	Model     string    `json:"model"`
}

// embedStats tracks in-memory hit/miss counters (best-effort; not persisted).
type embedStats struct {
	hits   atomic.Int64
	misses atomic.Int64
}

// Embedder provides embedding generation + vector caching.
type Embedder struct {
	store  *Store
	router *Router
	stats  embedStats
}

// NewEmbedder creates an Embedder backed by the given store and router.
func NewEmbedder(store *Store, router *Router) *Embedder {
	return &Embedder{store: store, router: router}
}

// Embed returns a float32 embedding vector for the given input/model.
// It checks the cache first; on miss it calls the provider and stores the result.
func (e *Embedder) Embed(model, input string) ([]float32, string, error) {
	// Resolve model if empty.
	if model == "" {
		cfg, err := e.store.GetConfig()
		if err == nil && cfg.ActiveModel != "" {
			model = cfg.ActiveModel
		}
		if model == "" {
			model = "text-embedding-3-small" // sensible default
		}
	}

	cacheKey := embedCacheKey(model, input)

	// --- cache lookup ---
	if vec, err := e.cacheGet(cacheKey); err == nil {
		e.stats.hits.Add(1)
		return vec, model, nil
	}
	e.stats.misses.Add(1)

	// --- provider call ---
	vec, err := e.callEmbedProvider(model, input)
	if err != nil {
		return nil, model, err
	}

	// --- cache store (best-effort) ---
	_ = e.cacheSet(cacheKey, model, input, vec)

	return vec, model, nil
}

// cacheGet fetches a non-expired embedding from SQLite.
func (e *Embedder) cacheGet(key string) ([]float32, error) {
	var blob []byte
	var expiresAt string
	err := e.store.db.QueryRow(
		`SELECT vector_blob, expires_at FROM embed_cache WHERE cache_key=?`, key,
	).Scan(&blob, &expiresAt)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("cache miss")
	}
	if err != nil {
		return nil, err
	}
	// Check TTL.
	exp, parseErr := time.Parse(time.RFC3339, expiresAt)
	if parseErr != nil || time.Now().UTC().After(exp) {
		// Evict stale row.
		_, _ = e.store.db.Exec(`DELETE FROM embed_cache WHERE cache_key=?`, key)
		return nil, fmt.Errorf("cache expired")
	}
	return blobToFloat32(blob)
}

// cacheSet stores an embedding in SQLite with a 30-day TTL.
func (e *Embedder) cacheSet(key, model, input string, vec []float32) error {
	blob := float32ToBlob(vec)
	inputHash := hex.EncodeToString(func() []byte {
		h := sha256.Sum256([]byte(input))
		return h[:]
	}())
	now := time.Now().UTC()
	exp := now.Add(embedTTL).Format(time.RFC3339)
	nowStr := now.Format(time.RFC3339)
	_, err := e.store.db.Exec(
		`INSERT OR REPLACE INTO embed_cache
		 (cache_key, model, input_hash, vector_blob, dim, created_at, expires_at)
		 VALUES(?,?,?,?,?,?,?)`,
		key, model, inputHash, blob, len(vec), nowStr, exp,
	)
	return err
}

// CacheStats returns (hits, misses, totalStored).
func (e *Embedder) CacheStats() (hits, misses, totalStored int64) {
	hits = e.stats.hits.Load()
	misses = e.stats.misses.Load()
	_ = e.store.db.QueryRow(`SELECT COUNT(*) FROM embed_cache`).Scan(&totalStored)
	return
}

// EvictExpired removes all expired cache rows. Safe to call periodically.
func (e *Embedder) EvictExpired() error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := e.store.db.Exec(`DELETE FROM embed_cache WHERE expires_at < ?`, now)
	return err
}

// ---------------------------------------------------------------------------
// Provider call (byo / cloud)
// ---------------------------------------------------------------------------

func (e *Embedder) callEmbedProvider(model, input string) ([]float32, error) {
	cfg, err := e.store.GetConfig()
	if err != nil {
		return nil, fmt.Errorf("airouter embed: load config: %w", err)
	}
	if cfg.Mode == ModeCloud {
		return e.callEmbedCloud(model, input)
	}
	return e.callEmbedBYO(model, input)
}

func (e *Embedder) callEmbedBYO(model, input string) ([]float32, error) {
	providers, err := e.store.ListProviders()
	if err != nil || len(providers) == 0 {
		return nil, fmt.Errorf("airouter embed byo: no providers configured")
	}
	// Select provider that claims the model, else use first provider.
	provider := providers[0]
	for _, p := range providers {
		for _, m := range p.Models {
			if m == model {
				provider = p
				break
			}
		}
	}

	endpoint := strings.TrimRight(provider.BaseURL, "/") + "/v1/embeddings"
	reqBody := map[string]any{
		"model": model,
		"input": input,
	}
	data, _ := json.Marshal(reqBody)

	httpReq, err := http.NewRequest("POST", endpoint, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if provider.APIKeyEnc != "" {
		httpReq.Header.Set("Authorization", "Bearer "+provider.APIKeyEnc)
	}

	resp, err := e.router.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("airouter embed byo: request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("airouter embed byo: provider returned %d: %s", resp.StatusCode, b)
	}

	return parseEmbedResponse(resp.Body)
}

func (e *Embedder) callEmbedCloud(model, input string) ([]float32, error) {
	rc, err := e.router.cloudProxy().ProxyEmbed(context.Background(), model, input)
	if err != nil {
		return nil, fmt.Errorf("airouter embed cloud: %w", err)
	}
	defer rc.Close()
	return parseEmbedResponse(rc)
}

// parseEmbedResponse parses an OpenAI-compatible embeddings response.
func parseEmbedResponse(body io.Reader) ([]float32, error) {
	var result struct {
		Data []struct {
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
		// Some providers return a flat "embedding" array at root level.
		Embedding []float32 `json:"embedding"`
	}
	if err := json.NewDecoder(body).Decode(&result); err != nil {
		return nil, fmt.Errorf("airouter embed: decode response: %w", err)
	}
	if len(result.Data) > 0 {
		return result.Data[0].Embedding, nil
	}
	if len(result.Embedding) > 0 {
		return result.Embedding, nil
	}
	return nil, fmt.Errorf("airouter embed: empty embedding in response")
}

// ---------------------------------------------------------------------------
// Vector similarity
// ---------------------------------------------------------------------------

// CosineSimilarity computes cosine similarity between two equal-length vectors.
// Returns a value in [-1, 1]; higher means more similar.
func CosineSimilarity(a, b []float32) (float32, error) {
	if len(a) != len(b) {
		return 0, fmt.Errorf("vector length mismatch: %d vs %d", len(a), len(b))
	}
	if len(a) == 0 {
		return 0, fmt.Errorf("zero-length vectors")
	}
	var dot, normA, normB float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		normA += float64(a[i]) * float64(a[i])
		normB += float64(b[i]) * float64(b[i])
	}
	denom := math.Sqrt(normA) * math.Sqrt(normB)
	if denom == 0 {
		return 0, nil
	}
	return float32(dot / denom), nil
}

// ---------------------------------------------------------------------------
// Binary encoding helpers (little-endian float32)
// ---------------------------------------------------------------------------

func float32ToBlob(v []float32) []byte {
	buf := make([]byte, len(v)*4)
	for i, f := range v {
		bits := math.Float32bits(f)
		binary.LittleEndian.PutUint32(buf[i*4:], bits)
	}
	return buf
}

func blobToFloat32(b []byte) ([]float32, error) {
	if len(b)%4 != 0 {
		return nil, fmt.Errorf("blob length %d not a multiple of 4", len(b))
	}
	out := make([]float32, len(b)/4)
	for i := range out {
		bits := binary.LittleEndian.Uint32(b[i*4:])
		out[i] = math.Float32frombits(bits)
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Cache key
// ---------------------------------------------------------------------------

func embedCacheKey(model, input string) string {
	h := sha256.Sum256([]byte(model + "\x00" + input))
	return hex.EncodeToString(h[:])
}

// ---------------------------------------------------------------------------
// AIROT-04: note_embeddings store helpers
// ---------------------------------------------------------------------------

// NoteEmbedding holds a single note's stored embedding.
type NoteEmbedding struct {
	NoteID    string
	ModelSlug string
	Vector    []float32
	UpdatedAt time.Time
}

// UpsertNoteEmbedding stores or replaces the embedding for (note_id, model_slug).
func (s *Store) UpsertNoteEmbedding(ne NoteEmbedding) error {
	blob := float32ToBlob(ne.Vector)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.Exec(
		`INSERT INTO note_embeddings(note_id, model_slug, vector_blob, dim, updated_at)
		 VALUES(?, ?, ?, ?, ?)
		 ON CONFLICT(note_id, model_slug) DO UPDATE SET
		   vector_blob=excluded.vector_blob,
		   dim=excluded.dim,
		   updated_at=excluded.updated_at`,
		ne.NoteID, ne.ModelSlug, blob, len(ne.Vector), now,
	)
	return err
}

// ListNoteEmbeddings returns all stored note embeddings.
func (s *Store) ListNoteEmbeddings() ([]NoteEmbedding, error) {
	rows, err := s.db.Query(
		`SELECT note_id, model_slug, vector_blob, dim, updated_at FROM note_embeddings`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []NoteEmbedding
	for rows.Next() {
		var ne NoteEmbedding
		var blob []byte
		var dim int
		var updatedAt string
		if err := rows.Scan(&ne.NoteID, &ne.ModelSlug, &blob, &dim, &updatedAt); err != nil {
			return nil, err
		}
		ne.Vector, _ = blobToFloat32(blob)
		ne.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
		out = append(out, ne)
	}
	return out, rows.Err()
}

// DeleteNoteEmbedding removes all embeddings for note_id.
func (s *Store) DeleteNoteEmbedding(noteID string) error {
	_, err := s.db.Exec(`DELETE FROM note_embeddings WHERE note_id=?`, noteID)
	return err
}

// SemanticSearchNotes returns the top-k note IDs (and scores) whose embedding
// has cosine similarity >= threshold with queryVec.
func (s *Store) SemanticSearchNotes(queryVec []float32, topK int, threshold float64) ([]string, []float64, error) {
	embeddings, err := s.ListNoteEmbeddings()
	if err != nil {
		return nil, nil, err
	}

	type scored struct {
		noteID string
		score  float64
	}

	bestByNote := map[string]float64{}
	for _, ne := range embeddings {
		sim, simErr := CosineSimilarity(queryVec, ne.Vector)
		if simErr != nil {
			continue
		}
		score := float64(sim)
		if score < threshold {
			continue
		}
		if prev, ok := bestByNote[ne.NoteID]; !ok || score > prev {
			bestByNote[ne.NoteID] = score
		}
	}

	results := make([]scored, 0, len(bestByNote))
	for id, sc := range bestByNote {
		results = append(results, scored{noteID: id, score: sc})
	}
	// Insertion-sort (small N in practice).
	for i := 1; i < len(results); i++ {
		for j := i; j > 0 && results[j].score > results[j-1].score; j-- {
			results[j], results[j-1] = results[j-1], results[j]
		}
	}
	if topK > 0 && topK < len(results) {
		results = results[:topK]
	}

	noteIDs := make([]string, len(results))
	scores := make([]float64, len(results))
	for i, r := range results {
		noteIDs[i] = r.noteID
		scores[i] = r.score
	}
	return noteIDs, scores, nil
}
