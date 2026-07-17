package relayscale

import (
	"fmt"
	"sort"
)

// policy.go — the pure relay-scaling POLICY: desired relay count PER REGION from
// observed load + region demand. It provisions nothing and names no provider; it
// is a pure function of its inputs so it is trivially testable and drives every
// actuation path (in-process actuator or an external scaler reading the demand
// API) identically.

// Default policy thresholds.
const (
	// DefaultScaleUpAt: a region whose mean saturation is at/above this wants
	// another relay.
	DefaultScaleUpAt = 0.75
	// DefaultScaleDownAt: a region whose mean saturation is at/below this can shed
	// a relay (never below MinPerRegion).
	DefaultScaleDownAt = 0.25
	// DefaultMinPerRegion: a region that has ANY observed demand keeps at least
	// this many relays (a floor, so a live region never drops to zero).
	DefaultMinPerRegion = 1
	// DefaultMaxPerRegion: the per-region ceiling (0 in Policy = unbounded).
	DefaultMaxPerRegion = 8
	// DefaultStep: relays added/removed per reconcile so the pool does not thrash.
	DefaultStep = 1
)

// Policy computes desired relay counts per region. Zero fields take the package
// defaults via withDefaults.
type Policy struct {
	// ScaleUpAt / ScaleDownAt are the region-mean saturation watermarks (0..1).
	ScaleUpAt   float64
	ScaleDownAt float64
	// MinPerRegion / MaxPerRegion bound each region's relay count. MaxPerRegion 0
	// = unbounded.
	MinPerRegion int
	MaxPerRegion int
	// Step is the max change (±) to a region's count per Plan call.
	Step int
}

func (p *Policy) withDefaults() {
	if p.ScaleUpAt <= 0 {
		p.ScaleUpAt = DefaultScaleUpAt
	}
	if p.ScaleDownAt <= 0 {
		p.ScaleDownAt = DefaultScaleDownAt
	}
	// Keep the band coherent: ScaleDownAt strictly below ScaleUpAt.
	if p.ScaleDownAt >= p.ScaleUpAt {
		p.ScaleDownAt = p.ScaleUpAt / 2
	}
	if p.MinPerRegion < 0 {
		p.MinPerRegion = 0
	}
	if p.MinPerRegion == 0 {
		p.MinPerRegion = DefaultMinPerRegion
	}
	if p.Step <= 0 {
		p.Step = DefaultStep
	}
	// MaxPerRegion 0 == unbounded (left as-is).
}

// RegionLoad is one region's observed demand snapshot the policy reads.
type RegionLoad struct {
	// Region is the coarse geo tag ("eu-central", "af-south", …).
	Region string
	// Instances is the count of relay nodes CURRENTLY healthy in the region.
	Instances int
	// Saturation is the region's mean load ratio (0..1+), the max-dimension
	// saturation averaged across its healthy nodes. > 1 means over soft capacity.
	Saturation float64
	// Demand, when > 0, is an explicit desired-instances hint from an upstream
	// signal (e.g. anticipated event load) that floors the region's desired count.
	Demand int
}

// RegionPlan is the policy's desired-state decision for one region.
type RegionPlan struct {
	Region  string  `json:"region"`
	Current int     `json:"current"`
	Desired int     `json:"desired"`
	Load    float64 `json:"saturation"`
	Reason  string  `json:"reason"`
}

// Delta is Desired - Current: positive = provision, negative = destroy.
func (r RegionPlan) Delta() int { return r.Desired - r.Current }

// Plan computes the desired relay count for every observed region. It is pure.
// For each region:
//
//   - saturation >= ScaleUpAt   → desired = current + Step
//   - saturation <= ScaleDownAt → desired = current - Step
//   - otherwise                  → desired = current (hold)
//
// then the result is clamped to [max(MinPerRegion, Demand), MaxPerRegion]. A
// region with zero current instances but observed demand (any load row) is
// floored at MinPerRegion so a cold region gets its first node. Regions are
// returned in stable id order.
func (p Policy) Plan(loads []RegionLoad) []RegionPlan {
	p.withDefaults()
	out := make([]RegionPlan, 0, len(loads))
	for _, l := range loads {
		desired := l.Instances
		reason := fmt.Sprintf("saturation %.2f within band [%.2f, %.2f]", l.Saturation, p.ScaleDownAt, p.ScaleUpAt)
		switch {
		case l.Instances == 0:
			desired = p.MinPerRegion
			reason = "cold region with demand → provision floor"
		case l.Saturation >= p.ScaleUpAt:
			desired = l.Instances + p.Step
			reason = fmt.Sprintf("saturation %.2f >= scale_up_at %.2f", l.Saturation, p.ScaleUpAt)
		case l.Saturation <= p.ScaleDownAt:
			desired = l.Instances - p.Step
			reason = fmt.Sprintf("saturation %.2f <= scale_down_at %.2f", l.Saturation, p.ScaleDownAt)
		}

		// Floor: never below MinPerRegion, and never below an explicit demand hint.
		floor := p.MinPerRegion
		if l.Demand > floor {
			floor = l.Demand
			if desired < floor {
				reason = fmt.Sprintf("demand hint floors region at %d", floor)
			}
		}
		if desired < floor {
			desired = floor
		}
		// Ceiling.
		if p.MaxPerRegion > 0 && desired > p.MaxPerRegion {
			desired = p.MaxPerRegion
			reason = fmt.Sprintf("clamped to max_per_region %d", p.MaxPerRegion)
		}

		out = append(out, RegionPlan{
			Region:  l.Region,
			Current: l.Instances,
			Desired: desired,
			Load:    l.Saturation,
			Reason:  reason,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Region < out[j].Region })
	return out
}
