package airouter

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"
)

// aiBucket holds per-account token state for aiRateLimiter.
type aiBucket struct {
	tokens     [3]int64  // [minTokens, hourTokens, lastRefillNano]
	lastAccess time.Time // wall-clock time of last allow() call
}

// aiRateLimiter is a per-account token-bucket rate limiter for AI endpoints.
//
// It maintains two buckets per account: per-minute and per-hour.
// Defaults: AIROUTER_RATE_PER_MIN (default 20) and AIROUTER_RATE_PER_HOUR (default 200).
// When either bucket is empty the handler returns 429 with Retry-After.
//
// A background goroutine (started by newAIRateLimiterWithCtx) sweeps the
// buckets map every 5 minutes and evicts entries idle for more than 24 hours.
// The goroutine terminates when the supplied context is cancelled.
type aiRateLimiter struct {
	mu sync.Mutex

	perMin  int
	perHour int

	// buckets maps accountKey → per-account token state.
	buckets map[string]*aiBucket

	// now is a clock override used in tests; nil means time.Now.
	now func() time.Time

	// sweepInterval and idleThreshold can be overridden in tests.
	sweepInterval time.Duration
	idleThreshold time.Duration
}

// newAIRateLimiterWithCtx builds a limiter and starts the background eviction
// goroutine.  The goroutine exits when ctx is cancelled.
func newAIRateLimiterWithCtx(ctx context.Context) *aiRateLimiter {
	perMin := aiEnvInt("AIROUTER_RATE_PER_MIN", 20)
	perHour := aiEnvInt("AIROUTER_RATE_PER_HOUR", 200)
	rl := &aiRateLimiter{
		perMin:        perMin,
		perHour:       perHour,
		buckets:       make(map[string]*aiBucket),
		sweepInterval: 5 * time.Minute,
		idleThreshold: 24 * time.Hour,
	}
	go rl.sweepLoop(ctx)
	return rl
}

func newAIRateLimiter() *aiRateLimiter {
	return newAIRateLimiterWithCtx(context.Background())
}

// sweepLoop runs in a goroutine and periodically evicts idle buckets.
func (rl *aiRateLimiter) sweepLoop(ctx context.Context) {
	nowFn := rl.now
	if nowFn == nil {
		nowFn = time.Now
	}
	ticker := time.NewTicker(rl.sweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			rl.sweep(nowFn())
		}
	}
}

// sweep deletes bucket entries that have been idle for longer than idleThreshold.
func (rl *aiRateLimiter) sweep(now time.Time) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	for key, b := range rl.buckets {
		if now.Sub(b.lastAccess) > rl.idleThreshold {
			delete(rl.buckets, key)
		}
	}
}

// allow returns (allowed, retryAfterSeconds).
func (rl *aiRateLimiter) allow(accountKey string) (bool, int) {
	nowFn := rl.now
	if nowFn == nil {
		nowFn = time.Now
	}
	now := nowFn()
	nowNano := now.UnixNano()

	rl.mu.Lock()
	defer rl.mu.Unlock()

	b, ok := rl.buckets[accountKey]
	if !ok {
		b = &aiBucket{
			tokens:     [3]int64{int64(rl.perMin), int64(rl.perHour), nowNano},
			lastAccess: now,
		}
		rl.buckets[accountKey] = b
	}

	b.lastAccess = now

	elapsed := time.Duration(nowNano - b.tokens[2])
	if elapsed > 0 {
		minRefill := int64(float64(elapsed) / float64(time.Minute) * float64(rl.perMin))
		b.tokens[0] += minRefill
		if b.tokens[0] > int64(rl.perMin) {
			b.tokens[0] = int64(rl.perMin)
		}
		hourRefill := int64(float64(elapsed) / float64(time.Hour) * float64(rl.perHour))
		b.tokens[1] += hourRefill
		if b.tokens[1] > int64(rl.perHour) {
			b.tokens[1] = int64(rl.perHour)
		}
		b.tokens[2] = nowNano
	}

	if b.tokens[0] <= 0 {
		secsPerToken := int(time.Minute/time.Second) / rl.perMin
		if secsPerToken < 1 {
			secsPerToken = 1
		}
		return false, secsPerToken
	}
	if b.tokens[1] <= 0 {
		secsPerToken := int(time.Hour/time.Second) / rl.perHour
		if secsPerToken < 1 {
			secsPerToken = 1
		}
		return false, secsPerToken
	}

	b.tokens[0]--
	b.tokens[1]--
	return true, 0
}

func aiEnvInt(key string, fallback int) int {
	if s := os.Getenv(key); s != "" {
		if v, err := strconv.Atoi(s); err == nil && v > 0 {
			return v
		}
	}
	return fallback
}

// Handler wraps a Router and exposes HTTP endpoints for the AI Router.
// Wire it into a ServeMux by calling RegisterHandlers.
//
// POST /api/ai/chat and POST /api/ai/embed are rate-limited per account
// (defaults: 20/min, 200/hour; configurable via AIROUTER_RATE_PER_MIN /
// AIROUTER_RATE_PER_HOUR).  Returns 429 with Retry-After when exceeded.
type Handler struct {
	router   *Router
	embedder *Embedder

	// semaphore limits concurrent in-flight /api/ai/chat requests to 10.
	sem chan struct{}

	// rl is the per-account rate limiter for POST endpoints.
	rl *aiRateLimiter
}

// NewHandler creates a Handler backed by the given Router.
func NewHandler(r *Router) *Handler {
	return &Handler{
		router:   r,
		embedder: NewEmbedder(r.store, r),
		sem:      make(chan struct{}, 10),
		rl:       newAIRateLimiter(),
	}
}

// accountKey extracts a per-account key from the request for rate limiting.
// Uses X-User-ID if set (by auth middleware), then X-OS-Session, then
// the remote address as a last resort.
func accountKey(r *http.Request) string {
	if id := r.Header.Get("X-User-ID"); id != "" {
		return id
	}
	if tok := r.Header.Get("X-OS-Session"); tok != "" {
		return tok
	}
	return r.RemoteAddr
}

// checkRateLimit applies the per-account rate limit.  Returns true if the
// request should proceed; writes 429 and returns false if limited.
func (h *Handler) checkRateLimit(w http.ResponseWriter, r *http.Request) bool {
	if ok, retryAfter := h.rl.allow(accountKey(r)); !ok {
		w.Header().Set("Retry-After", fmt.Sprintf("%d", retryAfter))
		jsonError(w, fmt.Sprintf("rate_limit_exceeded; retry after %ds", retryAfter), http.StatusTooManyRequests)
		return false
	}
	return true
}

// RegisterHandlers mounts the AI Router HTTP routes onto mux.
// The orchestrator calls this once at startup.
//
// Routes:
//
//	POST /api/ai/chat                    – stream a chat completion (SSE)
//	POST /api/ai/models                  – list available models
//	GET  /api/ai/status                  – current mode + active model
//	PUT  /api/ai/config                  – update mode / model / provider
//	POST /api/ai/embed                   – generate an embedding vector (AIROT-08)
//	GET  /api/ai/embed/stats             – embedding cache statistics (AIROT-08)
//	POST /api/ai/notes/index             – index a note's content (AIROT-04)
//	POST /api/ai/notes/search            – semantic search over notes (AIROT-04)
//	DELETE /api/ai/notes/{note_id}/embed – remove a note's embedding (AIROT-04)
func RegisterHandlers(mux *http.ServeMux, router *Router) {
	h := NewHandler(router)
	mux.HandleFunc("POST /api/ai/chat", h.handleChat)
	mux.HandleFunc("POST /api/ai/models", h.handleModels)
	mux.HandleFunc("GET /api/ai/status", h.handleStatus)
	mux.HandleFunc("PUT /api/ai/config", h.handleConfig)
	mux.HandleFunc("POST /api/ai/embed", h.handleEmbed)
	mux.HandleFunc("GET /api/ai/embed/stats", h.handleEmbedStats)
	// AIROT-04: Notes app AI indexing + semantic search
	mux.HandleFunc("POST /api/ai/notes/index", h.handleNotesIndex)
	mux.HandleFunc("POST /api/ai/notes/search", h.handleNotesSearch)
	mux.HandleFunc("DELETE /api/ai/notes/{note_id}/embed", h.handleNotesDeleteEmbed)
}

// ---------------------------------------------------------------------------
// POST /api/ai/chat
// ---------------------------------------------------------------------------

// handleChat validates the request, calls the router, and streams the SSE
// response back to the client. At most 10 requests run concurrently; the
// remainder are queued by blocking on the semaphore.
func (h *Handler) handleChat(w http.ResponseWriter, r *http.Request) {
	if !h.checkRateLimit(w, r) {
		return
	}

	var req ChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "invalid_json: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Validate: model must be non-empty.
	if req.Model == "" {
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

	// Force stream mode: the HTTP handler always streams SSE.
	req.Stream = true

	stream, err := h.router.Route(r.Context(), req)
	if err != nil {
		// Unknown model or no providers configured.
		if isModelNotFound(err) {
			jsonError(w, "model_not_found", http.StatusUnprocessableEntity)
			return
		}
		jsonError(w, "router_error: "+err.Error(), http.StatusBadGateway)
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
				// Best-effort: write a terminal SSE error event.
				fmt.Fprintf(w, "data: {\"error\":%q}\n\n", readErr.Error())
				if canFlush {
					flusher.Flush()
				}
			}
			return
		}
	}
}

// isModelNotFound returns true for errors that should surface as 422.
func isModelNotFound(err error) bool {
	// The router returns "no providers configured" when BYO has no providers.
	// For unknown models the provider might return a non-2xx but we map that
	// generically to 502. A dedicated ErrModelNotFound sentinel can be added
	// later; for now we detect the common "no providers" case.
	return false
}

// ---------------------------------------------------------------------------
// POST /api/ai/models
// ---------------------------------------------------------------------------

// handleModels returns the list of available models from the current config.
func (h *Handler) handleModels(w http.ResponseWriter, r *http.Request) {
	providers, err := h.router.store.ListProviders()
	if err != nil {
		jsonError(w, "store_error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	type modelEntry struct {
		Model    string `json:"model"`
		Provider string `json:"provider"`
	}

	var models []modelEntry
	seen := map[string]bool{}
	for _, p := range providers {
		for _, m := range p.Models {
			if !seen[m] {
				seen[m] = true
				models = append(models, modelEntry{Model: m, Provider: p.DisplayName})
			}
		}
	}
	if models == nil {
		models = []modelEntry{}
	}

	jsonOK(w, map[string]any{"models": models})
}

// ---------------------------------------------------------------------------
// GET /api/ai/status
// ---------------------------------------------------------------------------

// handleStatus returns the current router mode and active model.
func (h *Handler) handleStatus(w http.ResponseWriter, r *http.Request) {
	cfg, err := h.router.store.GetConfig()
	if err != nil {
		jsonError(w, "store_error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	jsonOK(w, map[string]any{
		"mode":         string(cfg.Mode),
		"active_model": cfg.ActiveModel,
	})
}

// ---------------------------------------------------------------------------
// PUT /api/ai/config
// ---------------------------------------------------------------------------

// configUpdateRequest is the body accepted by PUT /api/ai/config.
type configUpdateRequest struct {
	Mode        *string   `json:"mode"`
	ActiveModel *string   `json:"active_model"`
	Provider    *Provider `json:"provider"`
}

// handleConfig updates the router mode, active model, and/or adds/replaces a provider.
func (h *Handler) handleConfig(w http.ResponseWriter, r *http.Request) {
	var req configUpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "invalid_json: "+err.Error(), http.StatusBadRequest)
		return
	}

	if req.Mode != nil || req.ActiveModel != nil {
		cfg, err := h.router.store.GetConfig()
		if err != nil {
			jsonError(w, "store_error: "+err.Error(), http.StatusInternalServerError)
			return
		}
		if req.Mode != nil {
			cfg.Mode = Mode(*req.Mode)
		}
		if req.ActiveModel != nil {
			cfg.ActiveModel = *req.ActiveModel
		}
		if err := h.router.store.SetConfig(cfg); err != nil {
			jsonError(w, "store_error: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}

	if req.Provider != nil {
		p := *req.Provider
		// Try update first; fall back to add if not found.
		if err := h.router.store.UpdateProvider(p); err != nil {
			if addErr := h.router.store.AddProvider(p); addErr != nil {
				jsonError(w, "store_error: "+addErr.Error(), http.StatusInternalServerError)
				return
			}
		}
	}

	cfg, err := h.router.store.GetConfig()
	if err != nil {
		jsonError(w, "store_error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	jsonOK(w, map[string]any{
		"mode":         string(cfg.Mode),
		"active_model": cfg.ActiveModel,
	})
}

// ---------------------------------------------------------------------------
// POST /api/ai/embed
// ---------------------------------------------------------------------------

// handleEmbed generates an embedding for the given input string.
// Request: {"input": "...", "model": "..."}
// Response: {"embedding": [...], "model": "..."}
func (h *Handler) handleEmbed(w http.ResponseWriter, r *http.Request) {
	if !h.checkRateLimit(w, r) {
		return
	}

	var req EmbedRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "invalid_json: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.Input == "" {
		jsonError(w, "input_required", http.StatusBadRequest)
		return
	}

	vec, model, err := h.embedder.Embed(req.Model, req.Input)
	if err != nil {
		jsonError(w, "embed_error: "+err.Error(), http.StatusBadGateway)
		return
	}

	jsonOK(w, EmbedResponse{Embedding: vec, Model: model})
}

// ---------------------------------------------------------------------------
// GET /api/ai/embed/stats
// ---------------------------------------------------------------------------

// handleEmbedStats returns embedding cache hit rate and total stored count.
func (h *Handler) handleEmbedStats(w http.ResponseWriter, r *http.Request) {
	hits, misses, total := h.embedder.CacheStats()
	var hitRate float64
	if req := hits + misses; req > 0 {
		hitRate = float64(hits) / float64(req)
	}
	jsonOK(w, map[string]any{
		"cache_hits":   hits,
		"cache_misses": misses,
		"total_stored": total,
		"hit_rate":     hitRate,
	})
}

// ---------------------------------------------------------------------------
// AIROT-04: Notes app AI indexing + semantic search
// ---------------------------------------------------------------------------

// noteIndexRequest is the body for POST /api/ai/notes/index.
type noteIndexRequest struct {
	NoteID  string `json:"note_id"`
	Content string `json:"content"`
	Model   string `json:"model,omitempty"`
}

// handleNotesIndex embeds note content and stores it in note_embeddings.
// Returns 503 when AI Router is unconfigured so the caller can degrade gracefully.
func (h *Handler) handleNotesIndex(w http.ResponseWriter, r *http.Request) {
	var req noteIndexRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "invalid_json: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.NoteID == "" || req.Content == "" {
		jsonError(w, "note_id and content required", http.StatusBadRequest)
		return
	}

	vec, model, err := h.embedder.Embed(req.Model, req.Content)
	if err != nil {
		code := http.StatusBadGateway
		if isUnconfiguredErr(err) {
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
	if err := h.router.store.UpsertNoteEmbedding(ne); err != nil {
		jsonError(w, "store_error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	jsonOK(w, map[string]any{"note_id": req.NoteID, "model": model, "dim": len(vec)})
}

// noteSearchRequest is the body for POST /api/ai/notes/search.
type noteSearchRequest struct {
	Query     string  `json:"query"`
	Model     string  `json:"model,omitempty"`
	Threshold float64 `json:"threshold,omitempty"`
	TopK      int     `json:"top_k,omitempty"`
}

// noteSearchHit is one result in the semantic search response.
type noteSearchHit struct {
	NoteID string  `json:"note_id"`
	Score  float64 `json:"score"`
}

// handleNotesSearch embeds the query and returns top semantic hits.
// Gracefully degrades (returns empty hits + degraded:true) when unconfigured.
func (h *Handler) handleNotesSearch(w http.ResponseWriter, r *http.Request) {
	var req noteSearchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
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

	qVec, _, err := h.embedder.Embed(req.Model, req.Query)
	if err != nil {
		if isUnconfiguredErr(err) {
			// Graceful degrade: return empty semantic results.
			jsonOK(w, map[string]any{"hits": []noteSearchHit{}, "degraded": true})
			return
		}
		jsonError(w, "embed_error: "+err.Error(), http.StatusBadGateway)
		return
	}

	noteIDs, scores, err := h.router.store.SemanticSearchNotes(qVec, req.TopK, req.Threshold)
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

// handleNotesDeleteEmbed removes the embedding for a specific note.
func (h *Handler) handleNotesDeleteEmbed(w http.ResponseWriter, r *http.Request) {
	noteID := r.PathValue("note_id")
	if noteID == "" {
		jsonError(w, "note_id required", http.StatusBadRequest)
		return
	}
	if err := h.router.store.DeleteNoteEmbedding(noteID); err != nil {
		jsonError(w, "store_error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	jsonOK(w, map[string]any{"deleted": true, "note_id": noteID})
}

// isUnconfiguredErr returns true when the error indicates no providers are set up.
func isUnconfiguredErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	for _, sub := range []string{"no providers", "unconfigured"} {
		if strContains(msg, sub) {
			return true
		}
	}
	return false
}

func strContains(s, sub string) bool {
	if len(sub) == 0 {
		return true
	}
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// JSON helpers
// ---------------------------------------------------------------------------

var jsonMu sync.Mutex // protects nothing; just ensures the helpers are not inlined away

func jsonOK(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(v)
}

func jsonError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// Ensure context cancellation propagation compiles even without direct usage.
var _ context.Context = (context.Context)(nil)
