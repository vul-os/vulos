package servingpool

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/vul-os/vulos-management/pkg/obs"
)

// Autoscaler observes the pool and emits autoscale-by-load signals an
// external actuator (a pkg/relayscale.RelayProvisioner, a Fly scaler, a k8s HPA,
// …) reads from RecentSignals + the exposed Prometheus gauges.
//
// POLICY vs ACTUATION. The Autoscaler is pure I/O: it reads Stats, asks the
// injected ScalePolicy for a desired-state Decision, updates gauges, and persists
// the decision as a signal. The DECISION logic lives in ScalePolicy (policy.go),
// a pure function with no side effects. The Autoscaler itself PROVISIONS NOTHING —
// the cloud control plane does not scale the fleet directly; an out-of-band
// component reconciles the emitted desired state. That keeps the CP free of
// provider-specific scaling APIs and lets the same policy drive any actuator.
type Autoscaler struct {
	cfg       Config
	policy    ScalePolicy
	scheduler *Scheduler
	store     Store
}

// Prometheus gauges exposed for the autoscaler / dashboards. Registered once
// on first NewAutoscaler call.
var (
	fleetLoad = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: obs.MetricNamespace,
		Subsystem: "servingpool",
		Name:      "fleet_load",
		Help:      "Mean load_score across healthy bundle nodes (0..1).",
	})
	healthyNodes = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: obs.MetricNamespace,
		Subsystem: "servingpool",
		Name:      "healthy_nodes",
		Help:      "Count of bundle nodes currently in 'healthy' state.",
	})
	totalNodes = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: obs.MetricNamespace,
		Subsystem: "servingpool",
		Name:      "total_nodes",
		Help:      "Total bundle nodes registered in the pool (any health).",
	})
	totalLeases = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: obs.MetricNamespace,
		Subsystem: "servingpool",
		Name:      "total_leases",
		Help:      "Total active tenant→node leases.",
	})
	autoscaleSignals = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: obs.MetricNamespace,
		Subsystem: "servingpool",
		Name:      "autoscale_signals_total",
		Help:      "Autoscale signals emitted, by action.",
	}, []string{"action"})

	metricsOnce sync.Once
)

func registerMetrics() {
	metricsOnce.Do(func() {
		_ = prometheus.DefaultRegisterer.Register(fleetLoad)
		_ = prometheus.DefaultRegisterer.Register(healthyNodes)
		_ = prometheus.DefaultRegisterer.Register(totalNodes)
		_ = prometheus.DefaultRegisterer.Register(totalLeases)
		_ = prometheus.DefaultRegisterer.Register(autoscaleSignals)
	})
}

// NewAutoscaler returns an Autoscaler bound to scheduler + store.
func NewAutoscaler(scheduler *Scheduler, store Store, cfg Config) *Autoscaler {
	cfg.withDefaults()
	registerMetrics()
	return &Autoscaler{cfg: cfg, policy: policyFromConfig(cfg), scheduler: scheduler, store: store}
}

// Tick computes pool stats, updates gauges, and emits one autoscale signal
// (scale_up / scale_down / hold) based on fleet load + tenant pressure.
func (a *Autoscaler) Tick(ctx context.Context) (AutoscaleSignal, error) {
	st, err := a.scheduler.Stats(ctx)
	if err != nil {
		return AutoscaleSignal{}, fmt.Errorf("servingpool: autoscale stats: %w", err)
	}
	fleetLoad.Set(st.FleetLoad)
	healthyNodes.Set(float64(st.HealthyNodes))
	totalNodes.Set(float64(st.TotalNodes))
	totalLeases.Set(float64(st.TotalLeases))

	// Ask the pure policy for the desired-state decision. The Autoscaler makes no
	// scaling decision of its own — it only persists what the policy decides.
	d := a.policy.Decide(st)

	sig := AutoscaleSignal{
		EmittedAt: nowUTC(),
		Scope:     "global",
		Action:    d.Action,
		Reason:    d.Reason,
		LoadScore: st.FleetLoad,
	}
	if err := a.store.EmitSignal(ctx, sig); err != nil {
		return sig, err
	}
	autoscaleSignals.WithLabelValues(d.Action).Inc()
	return sig, nil
}

// Run executes Tick on an interval until ctx is cancelled. interval <= 0 uses
// 30s.
func (a *Autoscaler) Run(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	// One immediate tick so the first signal lands quickly.
	_, _ = a.Tick(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			_, _ = a.Tick(ctx)
		}
	}
}
