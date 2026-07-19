package obs

import "testing"

// TestDefaultMetricNamespace verifies the PROMETHEUS_NAMESPACE resolution
// logic in isolation: unset ⇒ the neutral OSS default ("vulos_management"),
// set ⇒ the override verbatim. This is the mechanism a deployment (e.g. the
// private vulos-cloud composition, which pins Grafana/Prometheus queries to
// "vulos_cloud_cp_*") uses to preserve its existing metric names — it sets
// PROMETHEUS_NAMESPACE=vulos_cloud_cp in its environment before the process
// starts.
//
// This tests the resolver function directly rather than the package-level
// MetricNamespace var: that var (like serviceName, its OTEL_SERVICE_NAME
// counterpart) is computed once at package load, before any test can set the
// env var, so it can't be re-exercised in-process. The resolver logic itself
// is what a real process boot depends on, and is fully covered here.
func TestDefaultMetricNamespace(t *testing.T) {
	t.Run("unset falls back to neutral OSS default", func(t *testing.T) {
		t.Setenv("PROMETHEUS_NAMESPACE", "")
		if got, want := defaultMetricNamespace(), "vulos_management"; got != want {
			t.Errorf("defaultMetricNamespace() = %q, want %q", got, want)
		}
	})

	t.Run("set overrides to the given value", func(t *testing.T) {
		t.Setenv("PROMETHEUS_NAMESPACE", "vulos_cloud_cp")
		if got, want := defaultMetricNamespace(), "vulos_cloud_cp"; got != want {
			t.Errorf("defaultMetricNamespace() = %q, want %q", got, want)
		}
	})

	t.Run("whitespace-only treated as unset", func(t *testing.T) {
		t.Setenv("PROMETHEUS_NAMESPACE", "   ")
		if got, want := defaultMetricNamespace(), "vulos_management"; got != want {
			t.Errorf("defaultMetricNamespace() = %q, want %q", got, want)
		}
	})
}
