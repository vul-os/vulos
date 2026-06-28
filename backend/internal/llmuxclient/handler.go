package llmuxclient

// handler.go — HTTP handlers for the llmux gateway client.
//
// Routes:
//   POST /api/ai/chat                    — SSE chat completion via llmux gateway
//   POST /api/ai/models                  — list models (proxied from llmux)
//   GET  /api/ai/status                  — llmux gateway status
//   PUT  /api/ai/config                  — 410 Gone (config now lives in llmux)
//   POST /api/ai/embed                   — embedding via llmux
//   GET  /api/ai/embed/stats             — embedding cache stats
//   POST /api/ai/notes/index             — index a note embedding
//   POST /api/ai/notes/search            — semantic search over notes
//   DELETE /api/ai/notes/{note_id}/embed — remove note embedding
//
// Billing and metering are handled by llmux → CP; the OS is transparent.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
)

// Handler wraps a Client and Store and exposes HTTP endpoints for the llmux
// gateway. Wire it into a ServeMux by calling RegisterHandlers.
type Handler struct {
	client *Client
	store  *Store

	// semaphore limits concurrent in-flight /api/ai/chat requests to 10.
	sem chan struct{}

	// rl is the per-account rate limiter for POST endpoints.
	rl *aiRateLimiter

	// embedStats tracks cache hits/misses in memory.
	embedStats embedStats
}

// NewHandler creates a Handler backed by the given Client and Store.
// store may be nil — in that case notes routes return 503 and embeddings
// are called without caching.
func NewHandler(client *Client, store *Store) *Handler {
	return &Handler{
		client: client,
		store:  store,
		sem:    make(chan struct{}, 10),
		rl:     newAIRateLimiterWithCtx(context.Background()),
	}
}

// RegisterHandlers mounts the AI gateway HTTP routes onto mux.
// Shortcut: creates a Handler internally; use NewHandler for more control.
func RegisterHandlers(mux *http.ServeMux, client *Client, store *Store) {
	h := NewHandler(client, store)
	mux.HandleFunc("POST /api/ai/chat", h.handleChat)
	mux.HandleFunc("POST /api/ai/models", h.handleModels)
	mux.HandleFunc("GET /api/ai/status", h.handleStatus)
	mux.HandleFunc("PUT /api/ai/config", h.handleConfig)
	mux.HandleFunc("POST /api/ai/embed", h.handleEmbed)
	mux.HandleFunc("GET /api/ai/embed/stats", h.handleEmbedStats)
	mux.HandleFunc("POST /api/ai/notes/index", h.handleNotesIndex)
	mux.HandleFunc("POST /api/ai/notes/search", h.handleNotesSearch)
	mux.HandleFunc("DELETE /api/ai/notes/{note_id}/embed", h.handleNotesDeleteEmbed)
}

// checkRateLimit applies the per-account rate limit. Returns true if the
// request should proceed; writes 429 and returns false if limited.
func (h *Handler) checkRateLimit(w http.ResponseWriter, r *http.Request) bool {
	if ok, retryAfter := h.rl.allow(accountKey(r)); !ok {
		w.Header().Set("Retry-After", fmt.Sprintf("%d", retryAfter))
		jsonError(w, fmt.Sprintf("rate_limit_exceeded; retry after %ds", retryAfter), http.StatusTooManyRequests)
		return false
	}
	return true
}

// ---------------------------------------------------------------------------
// POST /api/ai/chat
// ---------------------------------------------------------------------------

// handleChat rate-limits, forwards the raw JSON body to the llmux gateway,
// and streams the SSE response back. No billing or metering at the OS level —
// llmux handles that.
func (h *Handler) handleChat(w http.ResponseWriter, r *http.Request) {
	if !h.checkRateLimit(w, r) {
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 256<<10))
	if err != nil {
		jsonError(w, "read_error: "+err.Error(), http.StatusBadRequest)
		return
	}
	// Validate JSON minimally.
	var peek struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &peek); err != nil {
		jsonError(w, "invalid_json: "+err.Error(), http.StatusBadRequest)
		return
	}
	if peek.Model == "" {
		jsonError(w, "model_not_found", http.StatusUnprocessableEntity)
		return
	}

	// Acquire concurrency slot (queue if all 10 are busy).
	select {
	case h.sem <- struct{}{}:
	case <-r.Context().Done():
		jsonError(w, "request_cancelled", http.StatusServiceUnavailable)
		return
	}
	defer func() { <-h.sem }()

	stream, err := h.client.Chat(r.Context(), body)
	if err != nil {
		if h.client.cfg.BaseURL == "" {
			jsonError(w, "gateway_unconfigured: set LLMUX_URL", http.StatusServiceUnavailable)
			return
		}
		jsonError(w, "gateway_error: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer stream.Close()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	flusher, canFlush := w.(http.Flusher)
	buf := make([]byte, 4096)
	for {
		n, readErr := stream.Read(buf)
		if n > 0 {
			if _, writeErr := w.Write(buf[:n]); writeErr != nil {
				return
			}
			if canFlush {
				flusher.Flush()
			}
		}
		if readErr != nil {
			if readErr != io.EOF {
				fmt.Fprintf(w, "data: {\"error\":%q}\n\n", readErr.Error())
				if canFlush {
					flusher.Flush()
				}
			}
			break
		}
	}
}

// ---------------------------------------------------------------------------
// POST /api/ai/models
// ---------------------------------------------------------------------------

func (h *Handler) handleModels(w http.ResponseWriter, r *http.Request) {
	raw, err := h.client.Models(r.Context())
	if err != nil {
		jsonError(w, "gateway_error: "+err.Error(), http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(raw) //nolint:errcheck
}

// ---------------------------------------------------------------------------
// GET /api/ai/status
// ---------------------------------------------------------------------------

func (h *Handler) handleStatus(w http.ResponseWriter, r *http.Request) {
	if h.client.cfg.BaseURL == "" {
		jsonOK(w, map[string]any{"status": "unconfigured"})
		return
	}
	jsonOK(w, map[string]any{
		"status":  "ok",
		"gateway": h.client.cfg.BaseURL,
	})
}

// ---------------------------------------------------------------------------
// PUT /api/ai/config — removed; config now lives in llmux
// ---------------------------------------------------------------------------

func (h *Handler) handleConfig(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusGone)
	json.NewEncoder(w).Encode(map[string]string{ //nolint:errcheck
		"error": "airouter_removed: configure providers in llmux",
	})
}

// ---------------------------------------------------------------------------
// POST /api/ai/embed
// ---------------------------------------------------------------------------

func (h *Handler) handleEmbed(w http.ResponseWriter, r *http.Request) {
	if !h.checkRateLimit(w, r) {
		return
	}

	var req EmbedRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		jsonError(w, "invalid_json: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.Input == "" {
		jsonError(w, "input_required", http.StatusBadRequest)
		return
	}

	model := req.Model
	if model == "" {
		model = "text-embedding-3-small"
	}

	// Cache lookup.
	if h.store != nil {
		key := EmbedCacheKey(model, req.Input)
		if vec, err := h.store.CacheGet(key); err == nil {
			h.embedStats.hits.Add(1)
			jsonOK(w, EmbedResponse{Embedding: vec, Model: model})
			return
		}
		h.embedStats.misses.Add(1)
	}

	vec, resolvedModel, err := h.client.Embed(r.Context(), model, req.Input)
	if err != nil {
		jsonError(w, "embed_error: "+err.Error(), http.StatusBadGateway)
		return
	}

	// Cache store (best-effort).
	if h.store != nil {
		key := EmbedCacheKey(resolvedModel, req.Input)
		_ = h.store.CacheSet(key, resolvedModel, req.Input, vec)
	}

	jsonOK(w, EmbedResponse{Embedding: vec, Model: resolvedModel})
}

// ---------------------------------------------------------------------------
// GET /api/ai/embed/stats
// ---------------------------------------------------------------------------

func (h *Handler) handleEmbedStats(w http.ResponseWriter, r *http.Request) {
	hits := h.embedStats.hits.Load()
	misses := h.embedStats.misses.Load()
	var totalStored int64
	if h.store != nil {
		totalStored = h.store.CacheCount()
	}
	var hitRate float64
	if req := hits + misses; req > 0 {
		hitRate = float64(hits) / float64(req)
	}
	jsonOK(w, map[string]any{
		"cache_hits":   hits,
		"cache_misses": misses,
		"total_stored": totalStored,
		"hit_rate":     hitRate,
	})
}

// ---------------------------------------------------------------------------
// POST /api/ai/notes/index
// ---------------------------------------------------------------------------

type noteIndexRequest struct {
	NoteID  string `json:"note_id"`
	Content string `json:"content"`
	Model   string `json:"model,omitempty"`
}

func (h *Handler) handleNotesIndex(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		jsonError(w, "note_embeddings_unavailable: store not initialised", http.StatusServiceUnavailable)
		return
	}
	if !h.checkRateLimit(w, r) {
		return
	}
	var req noteIndexRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256<<10)).Decode(&req); err != nil {
		jsonError(w, "invalid_json: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.NoteID == "" || req.Content == "" {
		jsonError(w, "note_id and content required", http.StatusBadRequest)
		return
	}

	vec, model, err := h.client.Embed(r.Context(), req.Model, req.Content)
	if err != nil {
		code := http.StatusBadGateway
		if h.client.cfg.BaseURL == "" {
			code = http.StatusServiceUnavailable
		}
		jsonError(w, "embed_error: "+err.Error(), code)
		return
	}

	ne := NoteEmbedding{
		NoteID:    req.NoteID,
		ModelSlug: model,
		Vector:    vec,
	}
	if err := h.store.UpsertNoteEmbedding(ne); err != nil {
		jsonError(w, "store_error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	jsonOK(w, map[string]any{"note_id": req.NoteID, "model": model, "dim": len(vec)})
}

// ---------------------------------------------------------------------------
// POST /api/ai/notes/search
// ---------------------------------------------------------------------------

type noteSearchRequest struct {
	Query     string  `json:"query"`
	Model     string  `json:"model,omitempty"`
	Threshold float64 `json:"threshold,omitempty"`
	TopK      int     `json:"top_k,omitempty"`
}

type noteSearchHit struct {
	NoteID string  `json:"note_id"`
	Score  float64 `json:"score"`
}

func (h *Handler) handleNotesSearch(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		jsonOK(w, map[string]any{"hits": []noteSearchHit{}, "degraded": true})
		return
	}
	if !h.checkRateLimit(w, r) {
		return
	}
	var req noteSearchRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10)).Decode(&req); err != nil {
		jsonError(w, "invalid_json: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.Query == "" {
		jsonOK(w, map[string]any{"hits": []noteSearchHit{}, "degraded": false})
		return
	}
	if req.Threshold == 0 {
		req.Threshold = 0.75
	}
	if req.TopK == 0 {
		req.TopK = 5
	}

	qVec, _, err := h.client.Embed(r.Context(), req.Model, req.Query)
	if err != nil {
		if h.client.cfg.BaseURL == "" {
			jsonOK(w, map[string]any{"hits": []noteSearchHit{}, "degraded": true})
			return
		}
		jsonError(w, "embed_error: "+err.Error(), http.StatusBadGateway)
		return
	}

	noteIDs, scores, err := h.store.SemanticSearchNotes(qVec, req.TopK, req.Threshold)
	if err != nil {
		jsonError(w, "search_error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	hits := make([]noteSearchHit, len(noteIDs))
	for i, id := range noteIDs {
		hits[i] = noteSearchHit{NoteID: id, Score: scores[i]}
	}
	jsonOK(w, map[string]any{"hits": hits, "degraded": false})
}

// ---------------------------------------------------------------------------
// DELETE /api/ai/notes/{note_id}/embed
// ---------------------------------------------------------------------------

func (h *Handler) handleNotesDeleteEmbed(w http.ResponseWriter, r *http.Request) {
	noteID := r.PathValue("note_id")
	if noteID == "" {
		jsonError(w, "note_id required", http.StatusBadRequest)
		return
	}
	if h.store == nil {
		jsonError(w, "note_embeddings_unavailable: store not initialised", http.StatusServiceUnavailable)
		return
	}
	if err := h.store.DeleteNoteEmbedding(noteID); err != nil {
		jsonError(w, "store_error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	jsonOK(w, map[string]any{"deleted": true, "note_id": noteID})
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// accountKey extracts a per-account key from the request for rate limiting.
func accountKey(r *http.Request) string {
	if id := r.Header.Get("X-User-ID"); id != "" {
		return id
	}
	if tok := r.Header.Get("X-OS-Session"); tok != "" {
		return tok
	}
	return r.RemoteAddr
}

var jsonMu sync.Mutex // prevents the helpers being inlined away

func jsonOK(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(v) //nolint:errcheck
}

func jsonError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg}) //nolint:errcheck
}
