// Package obs wires Prometheus metrics and OTel tracing for the Vulos Cloud
// control-plane server. No-op when OTEL_EXPORTER_OTLP_ENDPOINT is unset.
package obs

import (
	"context"
	"net/http"
	"os"
	"strings"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

const serviceName = "vulos-cloud-cp"

var (
	RequestCount = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: "vulos_cloud_cp",
		Name:      "request_count_total",
		Help:      "Total HTTP requests handled by the control plane.",
	})
	RequestDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace: "vulos_cloud_cp",
		Name:      "request_duration_seconds",
		Help:      "HTTP request latency.",
		Buckets:   prometheus.DefBuckets,
	})
	ErrorCount = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: "vulos_cloud_cp",
		Name:      "error_count_total",
		Help:      "Total error responses.",
	})
	QueueDepth = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "vulos_cloud_cp",
		Name:      "queue_depth",
		Help:      "Pending background work items.",
	})
	CacheHitRatio = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "vulos_cloud_cp",
		Name:      "cache_hit_ratio",
		Help:      "Session / tenant cache hit ratio (0–1).",
	})

	// HTTPRequestsByClass counts HTTP requests labelled by a coarse route-class
	// (e.g. "api_org", "api_relay", "api_billing", "webhook", "metrics") and the
	// response status-class ("2xx".."5xx"). The route-class keeps cardinality
	// bounded — we never label on the raw path. See RouteClass.
	HTTPRequestsByClass = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "vulos_cloud_cp",
		Name:      "http_requests_by_class_total",
		Help:      "HTTP requests by route-class and status-class.",
	}, []string{"route_class", "status_class"})

	// HTTPLatencyByClass is request latency bucketed by route-class.
	HTTPLatencyByClass = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "vulos_cloud_cp",
		Name:      "http_request_duration_by_class_seconds",
		Help:      "HTTP request latency by route-class.",
		Buckets:   prometheus.DefBuckets,
	}, []string{"route_class"})

	// AuthFailures counts authentication/authorization rejections (401/403 on
	// session, HMAC, or shared-secret gates). Incremented by call-sites that
	// reject a request for an auth reason.
	AuthFailures = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: "vulos_cloud_cp",
		Name:      "auth_failures_total",
		Help:      "Authentication / authorization failures (401/403).",
	})

	// RateLimited counts requests rejected with 429 by the rate-limit / DDoS
	// throttling layers.
	RateLimited = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: "vulos_cloud_cp",
		Name:      "rate_limited_total",
		Help:      "Requests rejected with HTTP 429 Too Many Requests.",
	})

	// RelayUsageReports counts accepted POST /api/relay/usage reports, labelled
	// by outcome: "applied" (first-seen, items accumulated), "duplicate" (replay
	// no-op), or "rejected" (over-quota / auth / malformed).
	RelayUsageReports = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "vulos_cloud_cp",
		Name:      "relay_usage_reports_total",
		Help:      "Relay-usage reports received, by outcome.",
	}, []string{"outcome"})

	// RelayQuotaRejections counts relay/TURN quota-gate rejections (over-cap
	// tenants denied at /api/quota or the relay-usage path).
	RelayQuotaRejections = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: "vulos_cloud_cp",
		Name:      "relay_quota_rejections_total",
		Help:      "Relay/TURN requests rejected because the tenant is over its tier cap.",
	})

	// GPURentals counts GPU-rental lifecycle events, labelled by action
	// (e.g. "start", "stop", "reap").
	GPURentals = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "vulos_cloud_cp",
		Name:      "gpu_rentals_total",
		Help:      "GPU-rental lifecycle events, by action.",
	}, []string{"action"})

	// WebhookEvents counts inbound billing webhook events, labelled by the
	// Paystack event type ("charge.success", "charge.failed",
	// "invoice.payment_failed", …) and outcome ("processed"|"rejected").
	WebhookEvents = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "vulos_cloud_cp",
		Name:      "webhook_events_total",
		Help:      "Billing webhook events received, by event type and outcome.",
	}, []string{"event", "outcome"})

	tracer trace.Tracer = noop.NewTracerProvider().Tracer(serviceName)
)

func Init() {
	for _, c := range []prometheus.Collector{
		RequestCount, RequestDuration, ErrorCount, QueueDepth, CacheHitRatio,
		HTTPRequestsByClass, HTTPLatencyByClass, AuthFailures, RateLimited,
		RelayUsageReports, RelayQuotaRejections, GPURentals, WebhookEvents,
	} {
		_ = prometheus.DefaultRegisterer.Register(c)
	}
	endpoint := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	if endpoint == "" {
		return
	}
	exp, err := otlptracehttp.New(context.Background(),
		otlptracehttp.WithEndpoint(endpoint),
		otlptracehttp.WithInsecure(),
	)
	if err != nil {
		return
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp),
		sdktrace.WithResource(resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceNameKey.String(serviceName),
		)),
	)
	otel.SetTracerProvider(tp)
	tracer = tp.Tracer(serviceName)
}

func Start(ctx context.Context, op string) (context.Context, trace.Span) {
	return tracer.Start(ctx, op)
}

func Handler() http.Handler {
	return promhttp.Handler()
}

// Middleware counts and times every request. It records the legacy aggregate
// counters AND the route-class-labelled counters (HTTPRequestsByClass /
// HTTPLatencyByClass) so a scrape exposes both the global rate and a per-class
// breakdown without unbounded path cardinality. It also auto-increments the
// RateLimited counter on a 429 so any throttle layer downstream is observed.
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		class := RouteClass(r.URL.Path)
		timer := prometheus.NewTimer(RequestDuration)
		classTimer := prometheus.NewTimer(HTTPLatencyByClass.WithLabelValues(class))
		rw := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rw, r)
		timer.ObserveDuration()
		classTimer.ObserveDuration()
		RequestCount.Inc()
		HTTPRequestsByClass.WithLabelValues(class, statusClass(rw.status)).Inc()
		if rw.status >= 500 {
			ErrorCount.Inc()
		}
		if rw.status == http.StatusTooManyRequests {
			RateLimited.Inc()
		}
		if rw.status == http.StatusUnauthorized || rw.status == http.StatusForbidden {
			AuthFailures.Inc()
		}
	})
}

// RouteClass maps a request path to a low-cardinality bucket for metric labels.
// We deliberately collapse IDs/ULIDs into a fixed set of classes so the
// HTTPRequestsByClass time-series count stays bounded regardless of traffic.
func RouteClass(path string) string {
	switch {
	case path == "/metrics":
		return "metrics"
	case path == "/healthz" || path == "/readyz":
		return "health"
	case strings.HasPrefix(path, "/api/relay/"):
		return "api_relay"
	case strings.HasPrefix(path, "/api/quota"):
		return "api_quota"
	case strings.HasPrefix(path, "/api/billing/") || strings.HasPrefix(path, "/api/paystack"):
		return "api_billing"
	case strings.Contains(path, "/webhook"):
		return "webhook"
	case strings.HasPrefix(path, "/api/org/"):
		return "api_org"
	case strings.HasPrefix(path, "/api/fleet/"):
		return "api_fleet"
	case strings.HasPrefix(path, "/api/auth/") || strings.HasPrefix(path, "/api/login") || strings.HasPrefix(path, "/api/signup"):
		return "api_auth"
	case strings.HasPrefix(path, "/api/"):
		return "api_other"
	case strings.HasPrefix(path, "/superadmin"):
		return "superadmin"
	default:
		return "other"
	}
}

// statusClass collapses an HTTP status code into its class ("2xx".."5xx").
func statusClass(code int) string {
	switch {
	case code >= 500:
		return "5xx"
	case code >= 400:
		return "4xx"
	case code >= 300:
		return "3xx"
	case code >= 200:
		return "2xx"
	default:
		return "1xx"
	}
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}
