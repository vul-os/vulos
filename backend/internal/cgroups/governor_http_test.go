package cgroups

// Regression tests for GET /api/cgroups/status — the LIVE per-app usage the
// dashboard renders as CPU/RAM bars.
//
// The dashboard has always fetched /api/cgroups/status and read
// {app_id, cpu_pct, mem_current, mem_high, mem_max} off it, but no such route
// was ever registered: the only cgroups route was the Alerter's
// /api/cgroups/alerts/status, which reports throttle STATE and carries no usage
// numbers at all. The call fell through to the SPA catch-all (200 text/html),
// `if (!r.ok) return []` never fired, and the bars sat permanently empty.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// newTestStatusHandler builds a StatusHandler over a temp Governor with a clock
// the test drives, so CPU rates are computed against known intervals.
func newTestStatusHandler(t *testing.T) (*Governor, *StatusHandler, *time.Time) {
	t.Helper()
	g := newTestGovernor(t)
	h := NewStatusHandler(g)
	now := time.Date(2026, 7, 13, 0, 0, 0, 0, time.UTC)
	h.now = func() time.Time { return now }
	return g, h, &now
}

// TestStatusRoute_ServesUsageJSON pins the contract the dashboard already
// codes against: the route exists, and each app slice comes back with the
// memory limits the Governor holds.
func TestStatusRoute_ServesUsageJSON(t *testing.T) {
	g, h, _ := newTestStatusHandler(t)
	if err := g.EnsureAppSlice("mail", VisibilityPrivate); err != nil {
		t.Fatalf("EnsureAppSlice: %v", err)
	}

	mux := http.NewServeMux()
	h.RegisterStatusRoutes(mux)

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/cgroups/status", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}

	var got []SliceUsage
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v (body %q)", err, w.Body.String())
	}
	if len(got) != 1 {
		t.Fatalf("got %d usage rows, want 1: %+v", len(got), got)
	}
	if got[0].AppID != "mail" {
		t.Errorf("app_id = %q, want mail", got[0].AppID)
	}
	if got[0].MemHigh <= 0 || got[0].MemMax <= 0 {
		t.Errorf("mem limits not reported: %+v", got[0])
	}
}

// TestStatusUsage_CPUPctIsARateBetweenPolls pins the CPU maths: cgroup v2
// reports CPU as a CUMULATIVE microsecond counter, so a percentage only exists
// between two readings. The first poll must report 0 (no baseline to rate
// against) rather than publishing the raw counter as if it were a percent.
func TestStatusUsage_CPUPctIsARateBetweenPolls(t *testing.T) {
	_, h, now := newTestStatusHandler(t)

	slices := []SliceStatus{{AppID: "mail", CPUUsageUs: 1_000_000}}

	first := h.usage(slices)
	if len(first) != 1 || first[0].CPUPct != 0 {
		t.Fatalf("first poll cpu_pct = %v, want 0 (no baseline yet)", first)
	}

	// 2s later the app burned 1s of CPU → 50% of one core.
	*now = now.Add(2 * time.Second)
	slices[0].CPUUsageUs = 2_000_000

	second := h.usage(slices)
	if len(second) != 1 {
		t.Fatalf("got %d rows, want 1", len(second))
	}
	if got := second[0].CPUPct; got < 49.9 || got > 50.1 {
		t.Errorf("cpu_pct = %v, want ~50", got)
	}
}

// TestStatusUsage_CounterResetReportsZero — a recreated slice restarts its
// counter, so the stored baseline describes a different cgroup. Reporting the
// difference would underflow into a nonsense (or negative) rate; report 0.
func TestStatusUsage_CounterResetReportsZero(t *testing.T) {
	_, h, now := newTestStatusHandler(t)

	h.usage([]SliceStatus{{AppID: "mail", CPUUsageUs: 5_000_000}})

	*now = now.Add(time.Second)
	got := h.usage([]SliceStatus{{AppID: "mail", CPUUsageUs: 10_000}}) // counter went backwards

	if got[0].CPUPct != 0 {
		t.Errorf("cpu_pct = %v after counter reset, want 0", got[0].CPUPct)
	}
}

// TestStatusUsage_SkipsSystemSliceAndForgetsRemovedApps — the system slice is
// not an app the dashboard can attribute usage to, and an uninstalled app must
// not leave a stale CPU baseline behind for a future app that reuses its id.
func TestStatusUsage_SkipsSystemSliceAndForgetsRemovedApps(t *testing.T) {
	_, h, now := newTestStatusHandler(t)

	got := h.usage([]SliceStatus{
		{AppID: "", CPUUsageUs: 9_000_000}, // system slice
		{AppID: "mail", CPUUsageUs: 1_000_000},
	})
	if len(got) != 1 || got[0].AppID != "mail" {
		t.Fatalf("system slice not skipped: %+v", got)
	}

	// "mail" disappears (uninstalled) → its baseline must be dropped.
	*now = now.Add(time.Second)
	h.usage([]SliceStatus{})
	if _, ok := h.prev["mail"]; ok {
		t.Error("baseline for removed app was retained")
	}

	// A new app reusing the id starts fresh: first poll rates against nothing.
	*now = now.Add(time.Second)
	again := h.usage([]SliceStatus{{AppID: "mail", CPUUsageUs: 50_000_000}})
	if again[0].CPUPct != 0 {
		t.Errorf("cpu_pct = %v for re-created app, want 0 (fresh baseline)", again[0].CPUPct)
	}
}
