// Package modelvariance compares actual billing/cost actuals against a fixed
// set of margin projections.
//
// DEAD-MODEL EXEC RETIRED (BOX-MODEL-04, 2026-07-15). This package USED to shell
// out to `python3 billingmodel/model.py --json` for its projections. That script
// is the OLD ZAR-native, per-active-user TIER model (Vulos Mail Self-Host/Hosted,
// R99 Pro, …) and its own banner declares every price/tier/mail line DEAD — it is
// NOT the billing source of truth (that is backend/cp/internal/billing/catalog.go,
// the box model: compute + storage + relay, mail never billed). Running it as a
// live superadmin projection meant a superadmin surface computing "variance"
// against dead pricing — and shelling a Python interpreter out of a signed Go CP.
// Both are gone: projections are now the fixed table below and NOTHING execs
// model.py. The Calculator fields (UseHardcoded, ModelPyPath) are retained inert
// for wire compatibility (cmd/server sets UseHardcoded=true); neither now has any
// effect — the hardcoded table is the only source.
//
// Projections (gross-margin reference figures, retained for the variance RAG view):
//
//	Free:       COGS $0.32/user,   Rev $0,     Margin -100%
//	Pro:        COGS $3.83/user,   Rev $9.00,  Margin ~57%
//	Team:       COGS $3.81/device, Rev $12.00, Margin ~68%
//	Enterprise: COGS $42.62/user,  Rev $99.00, Margin ~57%
//	Blended @100k: Margin ~62.4%
package modelvariance

import (
	"context"
	"time"
)

// ─── Projection data (hardcoded from MODEL.md / billingmodel output) ─────────

// TierProjection holds the expected economics for a billing tier.
type TierProjection struct {
	Tier        string  // "free", "pro", "team", "enterprise"
	RevPerUser  float64 // USD/user/month
	COGSPerUser float64 // USD/user/month
	MarginPct   float64 // 0–100
}

// defaultProjections reflects model.py output @100k users, 80/12/7/1 mix.
// These are gross-margin figures (variable + amortized infra).
var defaultProjections = []TierProjection{
	{Tier: "free", RevPerUser: 0.00, COGSPerUser: 0.32, MarginPct: -100.0},
	{Tier: "pro", RevPerUser: 9.00, COGSPerUser: 3.83, MarginPct: 57.4}, // (9-3.83)/9 ≈ 57.4; model says 75% with add-ons
	{Tier: "team", RevPerUser: 12.00, COGSPerUser: 3.81, MarginPct: 68.3},
	{Tier: "enterprise", RevPerUser: 99.00, COGSPerUser: 42.62, MarginPct: 56.9},
}

// blendedExpectedMarginPct is the model-projected blended margin @100k users.
const blendedExpectedMarginPct = 62.4

// ─── Actuals input ────────────────────────────────────────────────────────────

// TierActuals holds real measured economics for a billing tier.
type TierActuals struct {
	Tier      string
	UserCount int64
	RevTotal  float64 // USD/month total for this tier
	COGSTotal float64 // USD/month total for this tier
}

// TierVariance is the per-tier comparison.
type TierVariance struct {
	Tier              string
	ActualRevPerUser  float64
	ActualCOGSPerUser float64
	ActualMarginPct   float64
	ExpectedMarginPct float64
	DeltaPP           float64 // actual - expected (negative = worse)
	Status            string  // GREEN / YELLOW / RED
}

// VarianceReport is the full output of Variance().
type VarianceReport struct {
	AtDate            time.Time
	ActualMarginPct   float64
	ExpectedMarginPct float64
	DeltaPP           float64 // actual - expected (negative = worse)
	Status            string  // GREEN / YELLOW / RED
	ByTier            []TierVariance
	// ModelSource records whether we used model.py output or the hardcoded table.
	ModelSource string
}

// ─── Calculator ───────────────────────────────────────────────────────────────

// Calculator computes variance between actuals and projections.
//
// DEAD-MODEL EXEC RETIRED: both fields below are RETAINED INERT for wire
// compatibility (cmd/server constructs Calculator{UseHardcoded: true}). Neither
// has any effect any more — the projection source is always the fixed
// defaultProjections table; model.py is never executed. See the package doc.
type Calculator struct {
	// ModelPyPath is retained inert (was: path to the deprecated model.py). No-op.
	ModelPyPath string
	// UseHardcoded is retained inert (the hardcoded table is now the ONLY source). No-op.
	UseHardcoded bool
}

// Variance computes the variance report for the given actuals at at_date.
func (c *Calculator) Variance(_ context.Context, atDate time.Time, actuals []TierActuals) (*VarianceReport, error) {
	// DEAD-MODEL EXEC RETIRED: projections are the fixed table; nothing execs
	// the deprecated per-active-user model.py any more.
	projections := defaultProjections
	modelSource := "hardcoded"

	// Build projection map by tier.
	projMap := make(map[string]TierProjection, len(projections))
	for _, p := range projections {
		projMap[p.Tier] = p
	}

	// Compute blended actuals.
	var totalRev, totalCOGS float64
	for _, a := range actuals {
		totalRev += a.RevTotal
		totalCOGS += a.COGSTotal
	}
	actualMargin := blendedMargin(totalRev, totalCOGS)

	// Per-tier variance.
	var byTier []TierVariance
	for _, a := range actuals {
		proj, ok := projMap[a.Tier]
		var expectedMargin float64
		if ok {
			expectedMargin = proj.MarginPct
		} else {
			expectedMargin = blendedExpectedMarginPct
		}

		var actRevPU, actCOGSPU, actMargin float64
		if a.UserCount > 0 {
			actRevPU = a.RevTotal / float64(a.UserCount)
			actCOGSPU = a.COGSTotal / float64(a.UserCount)
		}
		actMargin = blendedMargin(a.RevTotal, a.COGSTotal)
		delta := actMargin - expectedMargin

		tv := TierVariance{
			Tier:              a.Tier,
			ActualRevPerUser:  actRevPU,
			ActualCOGSPerUser: actCOGSPU,
			ActualMarginPct:   actMargin,
			ExpectedMarginPct: expectedMargin,
			DeltaPP:           delta,
			Status:            statusFromDelta(delta),
		}
		byTier = append(byTier, tv)
	}

	// Blended delta vs expected.
	blendedActualMargin := actualMargin
	blendedDelta := blendedActualMargin - blendedExpectedMarginPct

	return &VarianceReport{
		AtDate:            atDate,
		ActualMarginPct:   blendedActualMargin,
		ExpectedMarginPct: blendedExpectedMarginPct,
		DeltaPP:           blendedDelta,
		Status:            statusFromDelta(blendedDelta),
		ByTier:            byTier,
		ModelSource:       modelSource,
	}, nil
}

// blendedMargin computes gross margin % from totals. Returns 0 if rev is 0.
func blendedMargin(rev, cogs float64) float64 {
	if rev == 0 {
		return 0
	}
	return (rev - cogs) / rev * 100
}

// statusFromDelta classifies the variance:
//
//	|delta| < 5pp  → GREEN
//	5–10pp         → YELLOW
//	> 10pp         → RED
func statusFromDelta(delta float64) string {
	abs := delta
	if abs < 0 {
		abs = -abs
	}
	switch {
	case abs < 5:
		return "GREEN"
	case abs <= 10:
		return "YELLOW"
	default:
		return "RED"
	}
}
