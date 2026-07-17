package servingpool

import "testing"

func TestScalePolicyDecide(t *testing.T) {
	p := ScalePolicy{ScaleUpAt: 0.80, ScaleDownAt: 0.30}
	cases := []struct {
		name string
		st   PoolStats
		want string
	}{
		{"stranded leases, no nodes", PoolStats{HealthyNodes: 0, TotalLeases: 3}, ActionScaleUp},
		{"idle empty fleet", PoolStats{HealthyNodes: 0, TotalLeases: 0}, ActionHold},
		{"hot fleet", PoolStats{HealthyNodes: 2, FleetLoad: 0.9}, ActionScaleUp},
		{"cold fleet, room to shrink", PoolStats{HealthyNodes: 3, FleetLoad: 0.1}, ActionScaleDown},
		{"cold fleet, at floor", PoolStats{HealthyNodes: 1, FleetLoad: 0.1}, ActionHold},
		{"nominal", PoolStats{HealthyNodes: 2, FleetLoad: 0.5}, ActionHold},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := p.Decide(c.st); got.Action != c.want {
				t.Fatalf("Decide(%+v).Action = %q, want %q (reason: %s)", c.st, got.Action, c.want, got.Reason)
			}
		})
	}
}

// TestScalePolicyDefaults confirms a zero-value policy applies the package
// default watermarks rather than treating 0 as "scale up at any load".
func TestScalePolicyDefaults(t *testing.T) {
	var p ScalePolicy // zero value
	if got := p.Decide(PoolStats{HealthyNodes: 2, FleetLoad: 0.5}); got.Action != ActionHold {
		t.Fatalf("zero-value policy at load 0.5 = %q, want hold (defaults must apply)", got.Action)
	}
	if got := p.Decide(PoolStats{HealthyNodes: 2, FleetLoad: 0.85}); got.Action != ActionScaleUp {
		t.Fatalf("zero-value policy at load 0.85 = %q, want scale_up", got.Action)
	}
}
