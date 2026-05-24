package obs_test

import (
	"context"
	"testing"

	dto "github.com/prometheus/client_model/go"
	"vulos-office/backend/obs"
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
