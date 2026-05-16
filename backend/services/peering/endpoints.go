// Package peering implements Vula's server-to-server communication layer.
//
// PEER-40: Cluster Anycast – multi-endpoint registry + failover delivery.
//
// A Vula ID may advertise several server addresses (home node, cloud VPS,
// office machine, etc.).  When delivering a message the caller supplies the
// full endpoint list for the destination peer and this package races them by
// latency, returning after the first successful HTTP delivery.  Duplicate
// message-IDs (UUIDv7) are silently dropped so that racing two nodes at once
// is safe.
//
// Exported surface consumed by the orchestrator (wired in a later task):
//
//	RegisterEndpointHandlers(mux *http.ServeMux) — mounts the four REST routes.
//	EndpointFailoverDeliver(ctx, endpoints, payload) error — race-delivery helper.
package peering

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

// epEndpoint describes one server address for a Vula ID.
type epEndpoint struct {
	ID        string    `json:"id"`
	VulaID    string    `json:"vula_id"`
	Server    string    `json:"server"`     // "host:port"
	Priority  int       `json:"priority"`   // lower = higher priority
	LatencyMS int64     `json:"latency_ms"` // last measured round-trip, 0 = unknown
	Healthy   bool      `json:"healthy"`
	UpdatedAt time.Time `json:"updated_at"`
}

// epRegistryStore is the in-memory store for endpoint registrations.
// In a full cluster deployment the store would be backed by S3/MinIO;
// for this task it is an in-process map protected by a RWMutex, which
// the orchestrator can replace with a persistent implementation later.
type epRegistryStore struct {
	mu        sync.RWMutex
	endpoints map[string]*epEndpoint // id → endpoint
}

// epDedupeCache tracks recently seen message IDs to implement
// UUIDv7 inbound deduplication.
type epDedupeCache struct {
	mu   sync.Mutex
	seen map[string]time.Time
	ttl  time.Duration
}

// ---------------------------------------------------------------------------
// Package-level singletons (replaceable by the orchestrator via dependency
// injection if a persistent backend is introduced later).
// ---------------------------------------------------------------------------

var (
	epRegistry = &epRegistryStore{
		endpoints: make(map[string]*epEndpoint),
	}

	epDedupe = &epDedupeCache{
		seen: make(map[string]time.Time),
		ttl:  24 * time.Hour,
	}

	// epHTTPClient is the shared client used for both latency probes and
	// failover delivery attempts.
	epHTTPClient = &http.Client{
		Timeout: 5 * time.Second,
	}
)

// ---------------------------------------------------------------------------
// epRegistryStore – CRUD
// ---------------------------------------------------------------------------

// epRegister adds or updates an endpoint in the registry.
func (r *epRegistryStore) epRegister(ep *epEndpoint) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ep.UpdatedAt = time.Now().UTC()
	ep.Healthy = true
	r.endpoints[ep.ID] = ep
}

// epList returns all endpoints for a given Vula ID sorted by priority then
// latency.
func (r *epRegistryStore) epList(vulaID string) []*epEndpoint {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []*epEndpoint
	for _, ep := range r.endpoints {
		if ep.VulaID == vulaID {
			out = append(out, ep)
		}
	}
	// stable sort: primary key = Priority (asc), secondary = LatencyMS (asc, 0 last)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Priority != out[j].Priority {
			return out[i].Priority < out[j].Priority
		}
		li, lj := out[i].LatencyMS, out[j].LatencyMS
		if li == 0 {
			return false
		}
		if lj == 0 {
			return true
		}
		return li < lj
	})
	return out
}

// epListAll returns every registered endpoint (all Vula IDs).
func (r *epRegistryStore) epListAll() []*epEndpoint {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*epEndpoint, 0, len(r.endpoints))
	for _, ep := range r.endpoints {
		out = append(out, ep)
	}
	return out
}

// epRemove deletes an endpoint by ID.  Returns false if not found.
func (r *epRegistryStore) epRemove(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.endpoints[id]; !ok {
		return false
	}
	delete(r.endpoints, id)
	return true
}

// epSetPriority updates the priority of a single endpoint.  Returns false if
// the ID does not exist.
func (r *epRegistryStore) epSetPriority(id string, priority int) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	ep, ok := r.endpoints[id]
	if !ok {
		return false
	}
	ep.Priority = priority
	ep.UpdatedAt = time.Now().UTC()
	return true
}

// epRecordLatency updates the cached round-trip time for an endpoint.
func (r *epRegistryStore) epRecordLatency(id string, ms int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if ep, ok := r.endpoints[id]; ok {
		ep.LatencyMS = ms
		ep.UpdatedAt = time.Now().UTC()
	}
}

// epMarkHealth toggles the healthy flag.
func (r *epRegistryStore) epMarkHealth(id string, healthy bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if ep, ok := r.endpoints[id]; ok {
		ep.Healthy = healthy
		ep.UpdatedAt = time.Now().UTC()
	}
}

// ---------------------------------------------------------------------------
// epDedupeCache
// ---------------------------------------------------------------------------

// epSeen returns true if msgID was already processed, false on first call.
// It also evicts stale entries to keep memory bounded.
func (c *epDedupeCache) epSeen(msgID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()

	// Periodic eviction: remove entries older than ttl.
	for id, ts := range c.seen {
		if now.Sub(ts) > c.ttl {
			delete(c.seen, id)
		}
	}

	if _, exists := c.seen[msgID]; exists {
		return true
	}
	c.seen[msgID] = now
	return false
}

// ---------------------------------------------------------------------------
// HTTP handler registration
// ---------------------------------------------------------------------------

// RegisterEndpointHandlers mounts the cluster-anycast endpoint management
// routes onto mux.  The orchestrator calls this once at startup.
//
// Routes:
//
//	POST   /api/peering/endpoints/register       – register / update an endpoint
//	DELETE /api/peering/endpoints/{id}           – remove an endpoint
//	GET    /api/peering/endpoints                – list endpoints (all or ?vula_id=…)
//	PUT    /api/peering/endpoints/{id}/priority  – change failover priority
func RegisterEndpointHandlers(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/peering/endpoints/register", epHandleRegister)
	mux.HandleFunc("DELETE /api/peering/endpoints/{id}", epHandleRemove)
	mux.HandleFunc("GET /api/peering/endpoints", epHandleList)
	mux.HandleFunc("PUT /api/peering/endpoints/{id}/priority", epHandlePriority)
}

// epHandleRegister handles POST /api/peering/endpoints/register.
func epHandleRegister(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID       string `json:"id"`
		VulaID   string `json:"vula_id"`
		Server   string `json:"server"`
		Priority int    `json:"priority"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.ID == "" || req.VulaID == "" || req.Server == "" {
		http.Error(w, "id, vula_id and server are required", http.StatusBadRequest)
		return
	}
	ep := &epEndpoint{
		ID:       req.ID,
		VulaID:   req.VulaID,
		Server:   req.Server,
		Priority: req.Priority,
	}
	epRegistry.epRegister(ep)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(ep)
}

// epHandleRemove handles DELETE /api/peering/endpoints/{id}.
func epHandleRemove(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "id is required", http.StatusBadRequest)
		return
	}
	if !epRegistry.epRemove(id) {
		http.Error(w, "endpoint not found", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// epHandleList handles GET /api/peering/endpoints[?vula_id=…].
func epHandleList(w http.ResponseWriter, r *http.Request) {
	vulaID := r.URL.Query().Get("vula_id")
	var eps []*epEndpoint
	if vulaID != "" {
		eps = epRegistry.epList(vulaID)
	} else {
		eps = epRegistry.epListAll()
	}
	if eps == nil {
		eps = []*epEndpoint{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(eps)
}

// epHandlePriority handles PUT /api/peering/endpoints/{id}/priority.
func epHandlePriority(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "id is required", http.StatusBadRequest)
		return
	}
	var req struct {
		Priority int `json:"priority"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if !epRegistry.epSetPriority(id, req.Priority) {
		http.Error(w, "endpoint not found", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// EndpointFailoverDeliver – race delivery across a peer's endpoint list
// ---------------------------------------------------------------------------

// EndpointDeliveryRequest carries the metadata needed to perform failover
// delivery to a remote peer.
type EndpointDeliveryRequest struct {
	// MsgID is the UUIDv7 message identifier.  A non-empty MsgID triggers
	// inbound deduplication: if this node has already delivered or forwarded
	// the same ID the call returns immediately with no error.
	MsgID string

	// Endpoints is the ordered list of server addresses to try.  They are
	// attempted concurrently; the first one that responds with 2xx wins and
	// the rest are cancelled.  The list is sorted by priority/latency before
	// the race begins if SortByLatency is true.
	Endpoints []*epEndpoint

	// Path is the URL path to POST to on each endpoint, e.g.
	// "/api/peering/inbound/message".
	Path string

	// Payload is the JSON body forwarded verbatim to the winning endpoint.
	Payload []byte

	// SortByLatency re-sorts Endpoints by (Priority, LatencyMS) before racing.
	// Set to false when the caller has already ordered the list.
	SortByLatency bool
}

// epDeliveryResult is the outcome of a single endpoint attempt.
type epDeliveryResult struct {
	endpointID string
	latencyMS  int64
	err        error
}

// EndpointFailoverDeliver posts payload to the first reachable endpoint in the
// list.  All live endpoints are tried concurrently; the function returns as
// soon as one succeeds (or all have failed).
//
// Deduplication: if req.MsgID is non-empty and has been seen before within
// the dedup TTL window, the call is a no-op and returns nil.
//
// On a successful delivery the winning endpoint's latency is recorded in the
// registry (if the endpoint has a matching ID).
func EndpointFailoverDeliver(ctx context.Context, req EndpointDeliveryRequest) error {
	// --- deduplication ---
	if req.MsgID != "" && epDedupe.epSeen(req.MsgID) {
		return nil
	}

	if len(req.Endpoints) == 0 {
		return errors.New("peering: no endpoints to deliver to")
	}

	endpoints := req.Endpoints
	if req.SortByLatency {
		sorted := make([]*epEndpoint, len(endpoints))
		copy(sorted, endpoints)
		sort.SliceStable(sorted, func(i, j int) bool {
			if sorted[i].Priority != sorted[j].Priority {
				return sorted[i].Priority < sorted[j].Priority
			}
			li, lj := sorted[i].LatencyMS, sorted[j].LatencyMS
			if li == 0 {
				return false
			}
			if lj == 0 {
				return true
			}
			return li < lj
		})
		endpoints = sorted
	}

	// Filter to healthy endpoints only; if all are marked unhealthy fall back
	// to the full list so we don't give up entirely.
	healthy := make([]*epEndpoint, 0, len(endpoints))
	for _, ep := range endpoints {
		if ep.Healthy {
			healthy = append(healthy, ep)
		}
	}
	if len(healthy) == 0 {
		healthy = endpoints
	}

	// Race all healthy endpoints concurrently.
	raceCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	results := make(chan epDeliveryResult, len(healthy))

	for _, ep := range healthy {
		go func(ep *epEndpoint) {
			start := time.Now()
			err := epDoDeliver(raceCtx, ep.Server, req.Path, req.Payload)
			ms := time.Since(start).Milliseconds()
			results <- epDeliveryResult{endpointID: ep.ID, latencyMS: ms, err: err}
		}(ep)
	}

	var lastErr error
	succeeded := 0
	failed := 0

	for range healthy {
		res := <-results
		if res.err == nil {
			succeeded++
			cancel() // stop remaining goroutines
			// Record latency in the registry for future sorting.
			if res.endpointID != "" {
				epRegistry.epRecordLatency(res.endpointID, res.latencyMS)
				epRegistry.epMarkHealth(res.endpointID, true)
			}
			// Drain remaining results without blocking.
			go func() {
				for i := 0; i < len(healthy)-1-failed; i++ {
					r := <-results
					if r.err != nil {
						epRegistry.epMarkHealth(r.endpointID, false)
					}
				}
			}()
			return nil
		}
		failed++
		lastErr = res.err
		if res.endpointID != "" {
			epRegistry.epMarkHealth(res.endpointID, false)
		}
	}

	if succeeded == 0 {
		return fmt.Errorf("peering: all %d endpoint(s) failed; last error: %w", len(healthy), lastErr)
	}
	return nil
}

// epDoDeliver performs a single HTTP POST to server+path with payload.
func epDoDeliver(ctx context.Context, server, path string, payload []byte) error {
	url := "https://" + server + path
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := epHTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d from %s", resp.StatusCode, server)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Well-known .vula-id endpoint list serialisation helpers
// ---------------------------------------------------------------------------

// epWellKnownEndpoint is the shape advertised in /.well-known/vula-id for
// each node (see PEERING.md §Extensions/Cluster Anycast).
// The orchestrator embeds a []epWellKnownEndpoint into the well-known JSON
// under the "endpoints" key; this file only defines the type.
type epWellKnownEndpoint struct {
	Server   string `json:"server"`
	Priority int    `json:"priority"`
}

// EPFromRegistry converts stored registry entries for a Vula ID into the
// well-known representation, sorted by priority.
func EPFromRegistry(vulaID string) []epWellKnownEndpoint {
	eps := epRegistry.epList(vulaID)
	out := make([]epWellKnownEndpoint, 0, len(eps))
	for _, ep := range eps {
		out = append(out, epWellKnownEndpoint{
			Server:   ep.Server,
			Priority: ep.Priority,
		})
	}
	return out
}
