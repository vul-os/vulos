package obs_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	dto "github.com/prometheus/client_model/go"
	"github.com/vul-os/vulos-management/pkg/obs"
)

func TestMetricIncrement(t *testing.T) {
	obs.Init()
	before := counterVal(t, obs.RequestCount)
	obs.RequestCount.Inc()
	after := counterVal(t, obs.RequestCount)
	if after <= before {
		t.Fatalf("RequestCount did not increment: before=%v after=%v", before, after)
	}
}

func TestTracerNoOpWhenEndpointUnset(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	obs.Init()
	ctx, span := obs.Start(context.Background(), "test-op")
	if ctx == nil {
		t.Fatal("Start returned nil context")
	}
	span.End()
}

// TestMetricsEndpointExposesCoreCounters verifies the scrapeable /metrics
// endpoint emits the core counters after they are incremented: HTTP requests by
// route-class, auth failures, rate-limit 429s, relay-usage reports, GPU rentals,
// and webhook events.
func TestMetricsEndpointExposesCoreCounters(t *testing.T) {
	obs.Init()

	// Exercise each counter once.
	obs.HTTPRequestsByClass.WithLabelValues("api_org", "2xx").Inc()
	obs.AuthFailures.Inc()
	obs.RateLimited.Inc()
	obs.RelayUsageReports.WithLabelValues("applied").Inc()
	obs.RelayQuotaRejections.Inc()
	obs.GPURentals.WithLabelValues("start").Inc()
	obs.WebhookEvents.WithLabelValues("charge.failed", "processed").Inc()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	obs.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("/metrics status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()

	for _, name := range []string{
		"http_requests_by_class_total",
		"auth_failures_total",
		"rate_limited_total",
		"relay_usage_reports_total",
		"relay_quota_rejections_total",
		"gpu_rentals_total",
		"webhook_events_total",
	} {
		want := obs.MetricNamespace + "_" + name
		if !strings.Contains(body, want) {
			t.Errorf("/metrics output missing counter %q", want)
		}
	}
}

// TestMiddlewareRecordsRouteClassAnd429 verifies the obs.Middleware records the
// route-class request count and bumps RateLimited on a downstream 429.
func TestMiddlewareRecordsRouteClassAnd429(t *testing.T) {
	obs.Init()
	before429 := counterVal(t, obs.RateLimited)
	beforeAuth := counterVal(t, obs.AuthFailures)

	h429 := obs.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	h429.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/org/members", nil))
	if got := counterVal(t, obs.RateLimited); got <= before429 {
		t.Errorf("RateLimited not incremented on 429: before=%v after=%v", before429, got)
	}

	h401 := obs.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	h401.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/quota", nil))
	if got := counterVal(t, obs.AuthFailures); got <= beforeAuth {
		t.Errorf("AuthFailures not incremented on 401: before=%v after=%v", beforeAuth, got)
	}
}

// TestRouteClass verifies the path→class bucketing keeps cardinality bounded.
func TestRouteClass(t *testing.T) {
	cases := map[string]string{
		"/metrics":             "metrics",
		"/api/relay/usage":     "api_relay",
		"/api/quota":           "api_quota",
		"/api/billing/webhook": "api_billing",
		"/api/org/members":     "api_org",
		"/api/fleet/heartbeat": "api_fleet",
		"/foo":                 "other",
	}
	for path, want := range cases {
		if got := obs.RouteClass(path); got != want {
			t.Errorf("RouteClass(%q) = %q, want %q", path, got, want)
		}
	}
}

func counterVal(t *testing.T, c interface {
	Write(*dto.Metric) error
}) float64 {
	t.Helper()
	var m dto.Metric
	if err := c.Write(&m); err != nil {
		t.Fatalf("counter.Write: %v", err)
	}
	return m.GetCounter().GetValue()
}
