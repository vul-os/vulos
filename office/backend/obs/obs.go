// Package obs wires Prometheus metrics and OTel tracing for vulos-office.
// No-op when OTEL_EXPORTER_OTLP_ENDPOINT is unset.
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

const serviceName = "vulos-office"

var (
	RequestCount = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: "vulos_office",
		Name:      "request_count_total",
		Help:      "Total HTTP requests handled.",
	})
	RequestDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace: "vulos_office",
		Name:      "request_duration_seconds",
		Help:      "HTTP request latency.",
		Buckets:   prometheus.DefBuckets,
	})
	ErrorCount = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: "vulos_office",
		Name:      "error_count_total",
		Help:      "Total error responses.",
	})
	QueueDepth = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "vulos_office",
		Name:      "queue_depth",
		Help:      "Pending WebSocket signaling operations.",
	})
	CacheHitRatio = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "vulos_office",
		Name:      "cache_hit_ratio",
		Help:      "CRDT op cache hit ratio (0–1).",
	})

	tracer trace.Tracer = noop.NewTracerProvider().Tracer(serviceName)
)

func Init() {
	for _, c := range []prometheus.Collector{
		RequestCount, RequestDuration, ErrorCount, QueueDepth, CacheHitRatio,
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

// Middleware wraps an http.Handler to increment RequestCount and record
// RequestDuration for every request.
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

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}
