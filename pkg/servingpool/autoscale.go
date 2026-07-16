package servingpool

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// Autoscaler observes the pool and emits autoscale-by-load signals an
// external autoscaler (Fly scalings, k8s HPA, etc.)
// reads from RecentSignals + the exposed Prometheus gauges.
//
// Cloud control plane does NOT scale the fleet itself — the responsibility
// is split: this package SIGNALS what is needed; an out-of-band component
// reconciles. That keeps the CP free of provider-specific scaling APIs.
type Autoscaler struct {
	cfg       Config
	scheduler *Scheduler
	store     Store
}

// Prometheus gauges exposed for the autoscaler / dashboards. Registered once
// on first NewAutoscaler call.
var (
	fleetLoad = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "vulos_cloud_cp",
		Subsystem: "servingpool",
		Name:      "fleet_load",
		Help:      "Mean load_score across healthy bundle nodes (0..1).",
	})
	healthyNodes = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "vulos_cloud_cp",
		Subsystem: "servingpool",
		Name:      "healthy_nodes",
		Help:      "Count of bundle nodes currently in 'healthy' state.",
	})
	totalNodes = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "vulos_cloud_cp",
		Subsystem: "servingpool",
		Name:      "total_nodes",
		Help:      "Total bundle nodes registered in the pool (any health).",
	})
	totalLeases = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "vulos_cloud_cp",
		Subsystem: "servingpool",
		Name:      "total_leases",
		Help:      "Total active tenant→node leases.",
	})
	autoscaleSignals = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "vulos_cloud_cp",
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
	return &Autoscaler{cfg: cfg, scheduler: scheduler, store: store}
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

	action := ActionHold
	reason := "load within bounds"
	switch {
	case st.HealthyNodes == 0 && st.TotalLeases > 0:
		action = ActionScaleUp
		reason = "no healthy nodes but leases exist"
	case st.HealthyNodes == 0:
		// no nodes, no work — hold.
		action = ActionHold
		reason = "no healthy nodes and no leases"
	case st.FleetLoad >= a.cfg.ScaleUpAt:
		action = ActionScaleUp
		reason = fmt.Sprintf("fleet_load %.2f >= scale_up_at %.2f", st.FleetLoad, a.cfg.ScaleUpAt)
	case st.FleetLoad <= a.cfg.ScaleDownAt && st.HealthyNodes > 1:
		// Don't scale down to zero — keep at least one node.
		action = ActionScaleDown
		reason = fmt.Sprintf("fleet_load %.2f <= scale_down_at %.2f", st.FleetLoad, a.cfg.ScaleDownAt)
	}

	sig := AutoscaleSignal{
		EmittedAt: nowUTC(),
		Scope:     "global",
		Action:    action,
		Reason:    reason,
		LoadScore: st.FleetLoad,
	}
	if err := a.store.EmitSignal(ctx, sig); err != nil {
		return sig, err
	}
	autoscaleSignals.WithLabelValues(action).Inc()
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
