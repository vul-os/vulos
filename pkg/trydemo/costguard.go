package trydemo

import (
	"context"
	"fmt"
	"time"
)

// costGuard implements CostGuard using the Store accumulator.
// Compute cost estimate: $0.00025/min for a small Fly demo machine
// (~$10.71/mo ≈ $0.015/hr), plus egress at $0.02/GB (per COSTS.md §5c). GPU
// SKUs (not used by the demo VM) are L40S $1.20/hr, A100-40GB $1.60/hr,
// H100 $2.50/hr.
type costGuard struct {
	store  Store
	capUSD float64
}

// newCostGuard creates a CostGuard backed by store with the given $ ceiling.
func newCostGuard(s Store, capUSD float64) CostGuard {
	return &costGuard{store: s, capUSD: capUSD}
}

// Engaged returns true when the current month's estimated cost equals or
// exceeds the configured cap. When engaged, no new driver promotions occur
// (spectators are unaffected — NEVER outage).
func (c *costGuard) Engaged(ctx context.Context) (bool, error) {
	yyyymm := currentMonth()
	cost, err := c.store.MonthlyCostUSD(ctx, yyyymm)
	if err != nil {
		return false, fmt.Errorf("costguard: engaged: %w", err)
	}
	return cost >= c.capUSD, nil
}

// Tick adds compute minutes and egress bytes to the current month's
// accumulator and recomputes the estimated cost.
func (c *costGuard) Tick(ctx context.Context, computeMin float64, egressBytes int64) error {
	// Cost model (conservative; matches billingmodel/COSTS.md §5c):
	//   compute: $0.00025 per machine-minute (small Fly demo VM,
	//            ~$10.71/mo ≈ $0.015/hr = $0.00025/min)
	//   egress:  $0.02 per GB = $0.00000002 per byte
	const computeRatePerMin = 0.00025
	const egressRatePerByte = 0.02 / (1024 * 1024 * 1024)

	estCost := computeMin*computeRatePerMin + float64(egressBytes)*egressRatePerByte
	return c.store.AccumulateUsage(ctx, currentMonth(), computeMin, egressBytes, estCost)
}

// currentMonth returns the current UTC month as "YYYYMM" (e.g. "202605").
func currentMonth() string {
	return time.Now().UTC().Format("200601")
}
