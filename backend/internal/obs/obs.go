// Package obs wires Prometheus metrics and OpenTelemetry tracing for the
// Vulos backend. Both are no-op when their respective env vars are unset,
// so the binary never requires external infra at dev time.
//
// Usage:
//
//	obs.Init()                          // call once at startup
//	mux.Handle("/metrics", obs.Handler())
//	ctx, span := obs.Start(ctx, "op")
//	defer span.End()
//	obs.RequestCount.Inc()
package obs

import (
	"context"
	"net/http"
	"os"

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

const serviceName = "vulos"

var (
	// RequestCount counts all HTTP requests handled.
	RequestCount = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: "vulos",
		Name:      "request_count_total",
		Help:      "Total HTTP requests handled.",
	})

	// RequestDuration tracks HTTP request latency in seconds.
	RequestDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace: "vulos",
		Name:      "request_duration_seconds",
		Help:      "HTTP request latency distribution.",
		Buckets:   prometheus.DefBuckets,
	})

	// ErrorCount counts requests that resulted in a 5xx or internal error.
	ErrorCount = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: "vulos",
		Name:      "error_count_total",
		Help:      "Total requests resulting in errors.",
	})

	// QueueDepth gauges the current number of queued background tasks.
	QueueDepth = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "vulos",
		Name:      "queue_depth",
		Help:      "Current background task queue depth.",
	})

	// CacheHitRatio tracks the running fraction of cache hits (0.0–1.0).
	CacheHitRatio = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "vulos",
		Name:      "cache_hit_ratio",
		Help:      "Running cache hit ratio (0=miss, 1=hit).",
	})

	// --- Assistant / sovereignty Guard counters ---
	// These are non-sensitive operational counts (no mail content, no user IDs):
	// they let an operator see that the private-AI Guard is actually enforcing the
	// egress guarantee and that proposals are flowing through the approval ledger.

	// AssistantGuardAllowedTotal counts assistant model calls the sovereignty
	// Guard permitted (target is on-instance, or external egress was authorized).
	AssistantGuardAllowedTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: "vulos",
		Subsystem: "assistant",
		Name:      "guard_allowed_total",
		Help:      "Assistant model calls the sovereignty Guard allowed.",
	})

	// AssistantGuardBlockedTotal counts assistant model calls the Guard BLOCKED
	// because the target endpoint was not on-instance and external egress was not
	// authorized (the sovereign guarantee firing).
	AssistantGuardBlockedTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: "vulos",
		Subsystem: "assistant",
		Name:      "guard_blocked_total",
		Help:      "Assistant model calls the sovereignty Guard blocked (egress denied).",
	})

	// AssistantProposalsPending gauges the number of mutating proposals awaiting
	// operator approval in the ledger. A durable non-zero value flags proposals
	// the user has not yet approved/rejected.
	AssistantProposalsPending = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "vulos",
		Subsystem: "assistant",
		Name:      "proposals_pending",
		Help:      "Mutating assistant proposals awaiting operator approval.",
	})

	// AssistantRAGMode gauges the assistant's retrieval quality mode as a set of
	// mutually-exclusive labelled gauges (value 1 = active). Labels: semantic,
	// degraded, lexical. This is the machine-readable twin of the shell's RAG
	// readiness badge — an operator can alert on rag_mode{mode="degraded"}==1.
	AssistantRAGMode = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "vulos",
		Subsystem: "assistant",
		Name:      "rag_mode",
		Help:      "Assistant retrieval mode (1=active): semantic|degraded|lexical.",
	}, []string{"mode"})

	tracer trace.Tracer = noop.NewTracerProvider().Tracer(serviceName)
)

// SetRAGMode records the assistant's current retrieval mode as one-hot gauges.
// mode is one of "semantic", "degraded", "lexical". Unknown values set all to 0.
func SetRAGMode(mode string) {
	for _, m := range []string{"semantic", "degraded", "lexical"} {
		v := 0.0
		if m == mode {
			v = 1.0
		}
		AssistantRAGMode.WithLabelValues(m).Set(v)
	}
}

// Init registers metrics with the default registry and, when
// OTEL_EXPORTER_OTLP_ENDPOINT is set, wires an OTLP HTTP trace exporter.
// It is safe to call multiple times (subsequent calls are no-ops).
func Init() {
	// Register Prometheus metrics (ignore already-registered errors in tests).
	for _, c := range []prometheus.Collector{
		RequestCount, RequestDuration, ErrorCount, QueueDepth, CacheHitRatio,
	} {
		_ = prometheus.DefaultRegisterer.Register(c)
	}

	endpoint := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	if endpoint == "" {
		return // no-op tracer stays in place
	}

	exp, err := otlptracehttp.New(context.Background(),
		otlptracehttp.WithEndpoint(endpoint),
		otlptracehttp.WithInsecure(),
	)
	if err != nil {
		return // fall back to no-op
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

// Start begins a new span for the given operation name.
func Start(ctx context.Context, op string) (context.Context, trace.Span) {
	return tracer.Start(ctx, op)
}

// Handler returns the Prometheus HTTP handler for the /metrics endpoint.
func Handler() http.Handler {
	return promhttp.Handler()
}

// Middleware wraps an http.Handler to increment RequestCount and record
// RequestDuration for every request. Wire it around the top-level mux to
// cover all routes with a single call.
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		timer := prometheus.NewTimer(RequestDuration)
		rw := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rw, r)
		timer.ObserveDuration()
		RequestCount.Inc()
		if rw.status >= 500 {
			ErrorCount.Inc()
		}
	})
}

// statusRecorder captures the HTTP status code written by a handler.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}
