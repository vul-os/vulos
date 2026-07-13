// HTTP route for live per-app slice usage (PUBWEB-05 dashboard).
// Exposes GET /api/cgroups/status.
// RegisterStatusRoutes is called by the server package; it must NOT be called
// from main.go directly — wire it via a thin routes file that imports this
// package (the same contract as the Alerter's RegisterRoutes).
package cgroups

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"
)

// SliceUsage is the per-app usage row the dashboard renders as CPU/RAM bars.
// It is the LIVE view (memory.current + a CPU percentage); the Alerter's
// ThrottleStatus is the separate ALERT view (throttled, overuse timers) and
// carries no usage numbers — the two are not interchangeable.
type SliceUsage struct {
	AppID string `json:"app_id"`
	// CPUPct is the mean CPU utilisation over the interval BETWEEN the two most
	// recent polls, as a percentage of one core. It is 0 on the first poll for an
	// app (a rate needs two samples) and on non-Linux (cpu.stat is Linux-only).
	CPUPct     float64 `json:"cpu_pct"`
	MemCurrent int64   `json:"mem_current"`
	MemHigh    int64   `json:"mem_high"`
	MemMax     int64   `json:"mem_max"`
}

// cpuSample is the previous poll's cumulative CPU reading for one app, kept so
// the next poll can turn two cumulative counters into a rate.
type cpuSample struct {
	usageUs int64
	at      time.Time
}

// StatusHandler serves live slice usage. It holds the previous CPU sample per
// app because cgroup v2 reports CPU as a CUMULATIVE microsecond counter
// (cpu.stat usage_usec) — a percentage only exists between two readings.
type StatusHandler struct {
	gov *Governor
	now func() time.Time

	mu   sync.Mutex
	prev map[string]cpuSample // app_id → last cumulative reading
}

// NewStatusHandler builds the live-usage handler for gov.
func NewStatusHandler(gov *Governor) *StatusHandler {
	return &StatusHandler{
		gov:  gov,
		now:  time.Now,
		prev: make(map[string]cpuSample),
	}
}

// RegisterStatusRoutes wires the live-usage route onto mux.
//
// Routes:
//
//	GET /api/cgroups/status
func (h *StatusHandler) RegisterStatusRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/cgroups/status", h.handleStatus)
}

func (h *StatusHandler) handleStatus(w http.ResponseWriter, r *http.Request) {
	slices, err := h.gov.Status()
	if err != nil {
		http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
		return
	}

	out := h.usage(slices)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// usage converts raw slices into the dashboard's usage rows, folding each app's
// cumulative CPU counter into a rate against the previous poll. Only app slices
// are reported — the system slice is not an app the dashboard can show.
func (h *StatusHandler) usage(slices []SliceStatus) []SliceUsage {
	now := h.now()

	h.mu.Lock()
	defer h.mu.Unlock()

	out := make([]SliceUsage, 0, len(slices))
	seen := make(map[string]struct{}, len(slices))

	for _, s := range slices {
		if s.AppID == "" {
			continue // system slice: no app to attribute usage to
		}
		seen[s.AppID] = struct{}{}
		out = append(out, SliceUsage{
			AppID:      s.AppID,
			CPUPct:     h.cpuPct(s.AppID, s.CPUUsageUs, now),
			MemCurrent: s.MemCurrent,
			MemHigh:    s.Limits.MemHigh,
			MemMax:     s.Limits.MemMax,
		})
	}

	// Drop samples for slices that no longer exist, so an uninstalled app cannot
	// leave a stale baseline behind for a future app that reuses its id.
	for appID := range h.prev {
		if _, ok := seen[appID]; !ok {
			delete(h.prev, appID)
		}
	}
	return out
}

// cpuPct turns the cumulative counter into a percentage of one core over the
// interval since this app's previous sample, and stores the new baseline.
//
// It reports 0 rather than guessing whenever a rate cannot honestly be derived:
// no previous sample (first poll), a zero/negative elapsed interval, or a
// counter that went BACKWARDS (the slice was recreated, so the old baseline
// describes a different cgroup). Callers must hold h.mu.
func (h *StatusHandler) cpuPct(appID string, usageUs int64, now time.Time) float64 {
	prev, ok := h.prev[appID]
	h.prev[appID] = cpuSample{usageUs: usageUs, at: now}

	if !ok || usageUs < prev.usageUs {
		return 0
	}
	elapsed := now.Sub(prev.at)
	if elapsed <= 0 {
		return 0
	}
	pct := float64(usageUs-prev.usageUs) / float64(elapsed.Microseconds()) * 100
	if pct < 0 {
		return 0
	}
	return pct
}
