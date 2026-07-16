package ha

import (
	"encoding/json"
	"net/http"
	"time"
)

// Handler exposes the HA health endpoint. Routes:
//
//	GET /api/ha/health   — local replica's lease/role snapshot + peer list
//
// This endpoint is intentionally unauthenticated: it returns ONLY
// non-sensitive operational state (peer IDs, roles, generation, heartbeat
// timestamps). DNS-failover health checkers / load balancers / sibling
// peers poll it to determine whether to take over.
type Handler struct {
	mgr   *Manager
	store Store
}

// NewHandler builds an HTTP handler over the given manager + store.
func NewHandler(mgr *Manager, store Store) *Handler {
	return &Handler{mgr: mgr, store: store}
}

// Register installs the routes on mux.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("/api/ha/health", h.health)
}

// healthResponse is the wire schema for /api/ha/health.
type healthResponse struct {
	PeerID    string          `json:"peer_id"`
	LeaseKey  string          `json:"lease_key"`
	Self      State           `json:"self"`
	Lease     *Lease          `json:"lease,omitempty"`
	Peers     []PeerHeartbeat `json:"peers"`
	Timestamp time.Time       `json:"timestamp"`
}

func (h *Handler) health(w http.ResponseWriter, r *http.Request) {
	resp := healthResponse{
		PeerID:    h.mgr.PeerID(),
		LeaseKey:  h.mgr.LeaseKey(),
		Self:      h.mgr.Snapshot(),
		Timestamp: time.Now().UTC(),
	}
	ctx := r.Context()
	if l, err := h.store.GetLease(ctx, h.mgr.LeaseKey()); err == nil {
		resp.Lease = &l
	}
	if peers, err := h.store.ListPeers(ctx); err == nil {
		resp.Peers = peers
	}
	w.Header().Set("Content-Type", "application/json")
	// Status code reflects whether THIS replica is currently the leader.
	// LBs / failover health checks can probe and route based on the code:
	//   200 → leader (ready to take writes)
	//   503 → standby (do not route writes here)
	// Standbys still serve the body so observers can read the state.
	if resp.Self.IsLeader {
		w.WriteHeader(http.StatusOK)
	} else {
		w.WriteHeader(http.StatusServiceUnavailable)
	}
	_ = json.NewEncoder(w).Encode(resp)
}
