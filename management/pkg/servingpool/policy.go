package servingpool

import "fmt"

// policy.go — the pure autoscaling POLICY, split out from the Autoscaler that
// performs the store/metric side effects.
//
// SEPARATION OF POLICY FROM ACTUATION. The control plane deliberately does not
// scale the fleet itself. This file computes a DESIRED-STATE decision from pool
// stats and NOTHING ELSE — no store writes, no provider APIs, no goroutines. The
// Autoscaler (autoscale.go) reads stats, calls Decide, then emits the decision as
// a signal an out-of-band actuator consumes. For the RELAY pool the actuator is a
// pluggable pkg/relayscale.RelayProvisioner (manual / external / kubernetes /
// firecracker / proxmox / a commercial managed multi-provider); for the compute
// pool an external scaler reads the same signal. Either way this policy PROVISIONS
// NOTHING — it only decides what the fleet should look like.
//
// Being a pure function of its input, the policy is trivially unit-testable and
// reusable independently of the I/O-bound Autoscaler.

// ScalePolicy decides an autoscale action from a fleet snapshot. Thresholds are
// the fleet-load watermarks (0..1). A zero-value ScalePolicy is usable via
// withDefaults, but callers normally construct it from a Config.
type ScalePolicy struct {
	// ScaleUpAt: recommend scale_up when fleet load >= this (0..1).
	ScaleUpAt float64
	// ScaleDownAt: recommend scale_down when fleet load <= this (0..1) AND more
	// than one healthy node exists (never scale a live fleet to zero).
	ScaleDownAt float64
}

// policyFromConfig builds a ScalePolicy from a (defaulted) Config.
func policyFromConfig(cfg Config) ScalePolicy {
	return ScalePolicy{ScaleUpAt: cfg.ScaleUpAt, ScaleDownAt: cfg.ScaleDownAt}
}

func (p *ScalePolicy) withDefaults() {
	if p.ScaleUpAt <= 0 {
		p.ScaleUpAt = DefaultScaleUpLoad
	}
	if p.ScaleDownAt <= 0 {
		p.ScaleDownAt = DefaultScaleDownLoad
	}
}

// Decision is the pure result of the policy: the recommended action plus a
// human-readable reason. It carries no side effects and names no provider.
type Decision struct {
	Action string // ActionScaleUp | ActionScaleDown | ActionHold
	Reason string
}

// Decide computes the desired-state action from a fleet snapshot. It is a pure
// function — same input, same output, no I/O. Rules (first match wins):
//
//   - no healthy nodes but leases exist  → scale_up  (work stranded, need a node)
//   - no healthy nodes and no leases      → hold      (nothing to serve)
//   - fleet load >= ScaleUpAt             → scale_up
//   - fleet load <= ScaleDownAt (n>1)     → scale_down (keep at least one node)
//   - otherwise                            → hold
func (p ScalePolicy) Decide(st PoolStats) Decision {
	p.withDefaults()
	switch {
	case st.HealthyNodes == 0 && st.TotalLeases > 0:
		return Decision{ActionScaleUp, "no healthy nodes but leases exist"}
	case st.HealthyNodes == 0:
		return Decision{ActionHold, "no healthy nodes and no leases"}
	case st.FleetLoad >= p.ScaleUpAt:
		return Decision{ActionScaleUp, fmt.Sprintf("fleet_load %.2f >= scale_up_at %.2f", st.FleetLoad, p.ScaleUpAt)}
	case st.FleetLoad <= p.ScaleDownAt && st.HealthyNodes > 1:
		return Decision{ActionScaleDown, fmt.Sprintf("fleet_load %.2f <= scale_down_at %.2f", st.FleetLoad, p.ScaleDownAt)}
	default:
		return Decision{ActionHold, "load within bounds"}
	}
}
