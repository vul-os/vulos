// HTTP routes for the Alerter (PUBWEB-08).
// Exposes GET /api/cgroups/alerts/status.
// Register is called by the server package; it must NOT be called from main.go
// directly — wire it via a thin routes file that imports this package.
package cgroups

import (
	"encoding/json"
	"net/http"
	"time"
)

// ThrottleStatus describes the current alert/throttle state for one app.
type ThrottleStatus struct {
	AppID               string    `json:"app_id"`
	Throttled           bool      `json:"throttled"`
	NormalCPUWeight     int64     `json:"normal_cpu_weight,omitempty"`
	CPUOveruseStart     time.Time `json:"cpu_overuse_start,omitempty"`
	MemOveruseStart     time.Time `json:"mem_overuse_start,omitempty"`
	BelowThresholdStart time.Time `json:"below_threshold_start,omitempty"`
}

// RegisterRoutes wires the Alerter's HTTP routes onto mux.
// Callers that own the HTTP server call this in their route-registration phase.
//
// Routes:
//
//	GET /api/cgroups/alerts/status
func (a *Alerter) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/cgroups/alerts/status", a.handleAlertsStatus)
}

func (a *Alerter) handleAlertsStatus(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()

	out := make([]ThrottleStatus, 0, len(a.states))
	for appID, st := range a.states {
		out = append(out, ThrottleStatus{
			AppID:               appID,
			Throttled:           st.throttled,
			NormalCPUWeight:     st.normalCPUWeight,
			CPUOveruseStart:     st.cpuOveruseStart,
			MemOveruseStart:     st.memOveruseStart,
			BelowThresholdStart: st.belowThresholdStart,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}
