package obs

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// TestAssistantCollectorsExported guards the class of bug where a metric is
// declared + Inc()/Set() at call sites but never registered in Init(), so it is
// silently absent from /metrics. All of the assistant/Guard/RAG observability —
// the whole point of the metrics feature — depends on these being scraped.
func TestAssistantCollectorsExported(t *testing.T) {
	Init() // idempotent

	// Touch each metric so it materialises a sample line.
	AssistantGuardAllowedTotal.Inc()
	AssistantGuardBlockedTotal.Inc()
	AssistantProposalsPending.Set(1)
	SetRAGMode("degraded")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/metrics", nil)
	Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("/metrics status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()

	want := []string{
		"vulos_assistant_guard_allowed_total",
		"vulos_assistant_guard_blocked_total",
		"vulos_assistant_proposals_pending",
		`vulos_assistant_rag_mode{mode="degraded"} 1`,
		// Baseline collectors must remain exported too.
		"vulos_request_count_total",
	}
	for _, w := range want {
		if !strings.Contains(body, w) {
			t.Errorf("metric absent from /metrics export: %s", w)
		}
	}
}

// TestMetricsBodyHasNoSecrets is a lightweight guard that the metrics export
// carries only operational counts — no token/secret material ever leaks in a
// metric name or label. If a future collector adds a sensitive label, this
// alarms.
func TestMetricsBodyHasNoSecrets(t *testing.T) {
	Init()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/metrics", nil)
	Handler().ServeHTTP(rec, req)
	body := strings.ToLower(rec.Body.String())
	for _, bad := range []string{"token", "secret", "password", "apikey", "api_key", "bearer"} {
		if strings.Contains(body, bad) {
			t.Errorf("metrics export contains a sensitive-looking token %q — possible secret leak", bad)
		}
	}
}
