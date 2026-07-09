// status.go — HTTP status endpoint for the SFU host.
//
// Route: GET /api/meethost/status (mount via Service.StatusHandler()).
//
// Response (JSON, 200 always — "ready" is the load-bearing bit):
//
//	{
//	  "ready": true,
//	  "state": "ready",
//	  "host_id": "vula:ed25519:..",
//	  "endpoint": "https://box.example.org",
//	  "in_process": true,
//	  "max_participants": 50,
//	  "has_e2ee": false,
//	  "last_error": ""
//	}

package meethost

import (
	"encoding/json"
	"net/http"
)

// statusHandler implements http.Handler for the meethost status endpoint.
type statusHandler struct {
	svc *Service
}

// statusResponse is the JSON shape served by GET /api/meethost/status.
type statusResponse struct {
	Ready           bool   `json:"ready"`
	State           State  `json:"state"`
	HostID          string `json:"host_id"`
	Endpoint        string `json:"endpoint"`
	InProcess       bool   `json:"in_process"`
	MaxParticipants int    `json:"max_participants"`
	HasE2EE         bool   `json:"has_e2ee"`
	LastError       string `json:"last_error,omitempty"`
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
	inProc := s.inProc
	lastErrStr := ""
	if s.lastErr != nil {
		lastErrStr = s.lastErr.Error()
	}
	s.mu.Unlock()

	return statusResponse{
		Ready:           s.Ready(),
		State:           state,
		HostID:          s.cfg.Identity.HostID,
		Endpoint:        s.cfg.Endpoint,
		InProcess:       inProc,
		MaxParticipants: s.cfg.Capabilities.MaxParticipants,
		HasE2EE:         s.cfg.Capabilities.HasE2EE,
		LastError:       lastErrStr,
	}
}
