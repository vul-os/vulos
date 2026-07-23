package relayscale

import (
	"encoding/json"
	"net/http"
	"sort"
	"sync"
	"time"
)

// demand.go — the DEMAND API. In "external" mode (and usefully in any mode) the
// control plane PUBLISHES per-region current load + the policy's desired relay
// count, and an operator's OWN scaler reads it and actuates: a Kubernetes HPA on
// an external metric, a cloud autoscaling group, a cron reconcile, an IaC run.
//
// Two surfaces:
//   - POST /observe  (shared-secret gated) — relay PoPs / an aggregator push their
//     current per-region load. This is how the CP learns fleet load without
//     importing the relay binary.
//   - GET  /demand   — the published desired state: for each region, current
//     instances, mean saturation, the policy's desired count, and the reason. An
//     external scaler polls this and reconciles its own fleet.
//
// The store keeps only the LATEST observation per region (a small, bounded
// snapshot), so this never grows unbounded and needs no database.

// DemandStore holds the latest per-region load observations. Concurrency-safe.
type DemandStore struct {
	mu       sync.RWMutex
	byRegion map[string]RegionLoad
	seen     map[string]time.Time
	ttl      time.Duration
	now      func() time.Time
}

// NewDemandStore builds an empty store. staleAfter drops regions not observed
// within the window from the published demand (0 => 2m). now may be nil.
func NewDemandStore(staleAfter time.Duration, now func() time.Time) *DemandStore {
	if staleAfter <= 0 {
		staleAfter = 2 * time.Minute
	}
	if now == nil {
		now = time.Now
	}
	return &DemandStore{
		byRegion: make(map[string]RegionLoad),
		seen:     make(map[string]time.Time),
		ttl:      staleAfter,
		now:      now,
	}
}

// Observe records (or replaces) a region's latest load. An empty region is
// ignored.
func (s *DemandStore) Observe(l RegionLoad) {
	if l.Region == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.byRegion[l.Region] = l
	s.seen[l.Region] = s.now()
}

// Snapshot returns the fresh (non-stale) observations in stable region order.
func (s *DemandStore) Snapshot() []RegionLoad {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cut := s.now().Add(-s.ttl)
	out := make([]RegionLoad, 0, len(s.byRegion))
	for r, l := range s.byRegion {
		if s.seen[r].Before(cut) {
			continue
		}
		out = append(out, l)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Region < out[j].Region })
	return out
}

// DemandAPI serves the demand read + observe surfaces. Construct with NewDemandAPI
// and mount ServeHTTP (it routes GET /demand and POST /observe on the subtree it
// is mounted under, matching by trailing path segment).
type DemandAPI struct {
	store  *DemandStore
	policy Policy
	// provName names the active provisioner in the response (manual|external|…),
	// so an external scaler knows whether it is expected to act.
	provName string
	// secret gates POST /observe. Empty => the observe endpoint is disabled (503),
	// fail closed (never accept anonymous load pushes).
	secret func() string
}

// NewDemandAPI builds the demand API bound to a store + policy.
func NewDemandAPI(store *DemandStore, policy Policy, provName string, secret func() string) *DemandAPI {
	if secret == nil {
		secret = func() string { return "" }
	}
	return &DemandAPI{store: store, policy: policy, provName: provName, secret: secret}
}

// DemandResponse is the GET /demand wire shape.
type DemandResponse struct {
	GeneratedAt time.Time `json:"generated_at"`
	Provisioner string    `json:"provisioner"`
	// Actuated reports whether the control plane actuates scaling itself. When
	// false (manual/external), the desired counts are for YOUR scaler to act on.
	Actuated bool         `json:"actuated"`
	Regions  []RegionPlan `json:"regions"`
}

// Demand computes the current published desired state from the freshest
// observations. Exposed (not just via HTTP) so an in-process actuator and the
// wire response share one code path.
func (a *DemandAPI) Demand() DemandResponse {
	loads := a.store.Snapshot()
	actuated := a.provName != "" && a.provName != "manual" && a.provName != "external"
	return DemandResponse{
		GeneratedAt: time.Now().UTC(),
		Provisioner: a.provName,
		Actuated:    actuated,
		Regions:     a.policy.Plan(loads),
	}
}

// ServeDemand handles GET /demand.
func (a *DemandAPI) ServeDemand(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, a.Demand())
}

// observeRequest is the POST /observe body: a batch of per-region loads.
type observeRequest struct {
	Regions []RegionLoad `json:"regions"`
}

// ServeObserve handles POST /observe (shared-secret gated).
func (a *DemandAPI) ServeObserve(w http.ResponseWriter, r *http.Request) {
	secret := a.secret()
	if secret == "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "observe disabled (no shared secret configured)"})
		return
	}
	if !constEq(bearer(r), secret) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	var req observeRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad json"})
		return
	}
	for _, l := range req.Regions {
		a.store.Observe(l)
	}
	writeJSON(w, http.StatusOK, map[string]int{"accepted": len(req.Regions)})
}

// Register mounts the demand API routes on mux under the given prefix (e.g.
// "/api/relay/scale"). It wires:
//
//	GET  {prefix}/demand
//	POST {prefix}/observe
func (a *DemandAPI) Register(mux *http.ServeMux, prefix string) {
	mux.HandleFunc("GET "+prefix+"/demand", a.ServeDemand)
	mux.HandleFunc("POST "+prefix+"/observe", a.ServeObserve)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
