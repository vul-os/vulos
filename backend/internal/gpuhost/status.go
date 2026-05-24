// status.go — HTTP status / health endpoint for the GPU streaming host.
//
// The cloud session orchestrator (STREAM-SIGNAL-01, separate repo) probes
// this endpoint before assigning a session to the box. The shape is also
// useful for the dashboard and for local sanity checks.
//
// Route: GET /api/gpuhost/status (mount via Service.StatusHandler()).
//
// Response (JSON, 200 always — the field "ready" is the load-bearing bit):
//
//	{
//	  "ready": true,
//	  "state": "ready",            // see gpuhost.State constants
//	  "host_id": "vula:ed25519:..",
//	  "domain": "abc123.vulos.org",
//	  "advertise": "1.2.3.4:47989",
//	  "codec": "h264",
//	  "supervisor": {
//	    "alive": true,
//	    "starts": 1,
//	    "restarts": 0,
//	    "exits": 0
//	  },
//	  "decisions": { "p2p": 12, "turn": 1 },
//	  "last_error": ""              // present only when non-empty
//	}

package gpuhost

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// statusHandler implements http.Handler for the gpuhost status endpoint.
type statusHandler struct {
	svc *Service
}

// statusResponse is the JSON shape served by GET /api/gpuhost/status.
type statusResponse struct {
	Ready      bool                  `json:"ready"`
	State      State                 `json:"state"`
	HostID     string                `json:"host_id"`
	Domain     string                `json:"domain"`
	Advertise  string                `json:"advertise"`
	Codec      string                `json:"codec"`
	Supervisor statusSupervisorBlock `json:"supervisor"`
	Decisions  statusDecisionsBlock  `json:"decisions"`
	LastError  string                `json:"last_error,omitempty"`
}

type statusSupervisorBlock struct {
	Alive    bool   `json:"alive"`
	Starts   uint64 `json:"starts"`
	Restarts uint64 `json:"restarts"`
	Exits    uint64 `json:"exits"`
}

type statusDecisionsBlock struct {
	P2P  uint64 `json:"p2p"`
	TURN uint64 `json:"turn"`
}

func (h statusHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	resp := h.svc.snapshot()
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(resp)
}

// snapshot builds a status response under the service mutex.
func (s *Service) snapshot() statusResponse {
	s.mu.Lock()
	state := s.state
	lastErrStr := ""
	if s.lastErr != nil {
		lastErrStr = s.lastErr.Error()
	}
	sup := s.sup
	s.mu.Unlock()

	supBlock := statusSupervisorBlock{}
	if sup != nil {
		supBlock = statusSupervisorBlock{
			Alive:    sup.Alive(),
			Starts:   sup.Starts(),
			Restarts: sup.Restarts(),
			Exits:    sup.Exits(),
		}
	}

	p2p, turn := s.chooser.Decisions()

	return statusResponse{
		Ready:     s.Ready(),
		State:     state,
		HostID:    s.cfg.Identity.HostID,
		Domain:    s.cfg.Identity.Domain,
		Advertise: fmt.Sprintf("%s:%d", s.cfg.AdvertiseHostname, s.cfg.AdvertisePort),
		Codec:     s.cfg.Codec,
		Supervisor: supBlock,
		Decisions: statusDecisionsBlock{P2P: p2p, TURN: turn},
		LastError: lastErrStr,
	}
}
