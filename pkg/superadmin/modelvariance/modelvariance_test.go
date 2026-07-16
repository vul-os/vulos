package modelvariance

import (
	"context"
	"testing"
	"time"
)

var testDate = time.Date(2026, 5, 23, 0, 0, 0, 0, time.UTC)

func calc() *Calculator {
	return &Calculator{UseHardcoded: true}
}

// Test 1: clean margin path — actuals match expected → GREEN.
func TestVariance_CleanMargin(t *testing.T) {
	// Actuals that closely match the Pro projection (57.4% margin on Pro-like numbers).
	actuals := []TierActuals{
		{Tier: "pro", UserCount: 1000, RevTotal: 9000.00, COGSTotal: 3830.00},
	}
	report, err := calc().Variance(context.Background(), testDate, actuals)
	if err != nil {
		t.Fatalf("Variance: %v", err)
	}
	if report.Status == "RED" {
		t.Errorf("expected GREEN or YELLOW for close match, got RED (delta=%.2f)", report.DeltaPP)
	}
	if report.ModelSource != "hardcoded" {
		t.Errorf("ModelSource: want hardcoded, got %s", report.ModelSource)
	}
}

// Test 2: tier mix shift — heavy free-tier skew increases COGS, expect YELLOW/RED.
func TestVariance_TierMixShift(t *testing.T) {
	// Simulate 95% free users (high COGS relative to $0 revenue).
	actuals := []TierActuals{
		{Tier: "free", UserCount: 9500, RevTotal: 0, COGSTotal: 3040}, // $0.32/user
		{Tier: "pro", UserCount: 500, RevTotal: 4500, COGSTotal: 1915},
	}
	report, err := calc().Variance(context.Background(), testDate, actuals)
	if err != nil {
		t.Fatalf("Variance: %v", err)
	}
	// Blended: rev = 4500, cogs = 4955 → negative margin → big negative delta.
	if report.Status == "GREEN" {
		t.Errorf("expected YELLOW or RED for heavy free-tier mix, got GREEN (delta=%.2f)", report.DeltaPP)
	}
	if len(report.ByTier) != 2 {
		t.Errorf("expected 2 tier entries, got %d", len(report.ByTier))
	}
}

// Test 3: cost spike — COGS 3x expected → RED.
func TestVariance_CostSpike(t *testing.T) {
	actuals := []TierActuals{
		{Tier: "enterprise", UserCount: 100, RevTotal: 9900, COGSTotal: 9900 * 0.90}, // 90% COGS = 10% margin
	}
	report, err := calc().Variance(context.Background(), testDate, actuals)
	if err != nil {
		t.Fatalf("Variance: %v", err)
	}
	// Expected ~57%, actual ~10% → delta ~47pp → RED.
	if report.Status != "RED" {
		t.Errorf("expected RED for cost spike, got %s (delta=%.2f)", report.Status, report.DeltaPP)
	}
}

// Test 4: hardcoded json mode returns correct structure and model source.
func TestVariance_JSONMode(t *testing.T) {
	c := &Calculator{UseHardcoded: true}
	actuals := []TierActuals{
		{Tier: "pro", UserCount: 100, RevTotal: 900, COGSTotal: 383},
	}
	report, err := c.Variance(context.Background(), testDate, actuals)
	if err != nil {
		t.Fatalf("Variance: %v", err)
	}
	if report.ModelSource != "hardcoded" {
		t.Errorf("expected hardcoded model source, got %s", report.ModelSource)
	}
	if report.ExpectedMarginPct != blendedExpectedMarginPct {
		t.Errorf("ExpectedMarginPct: want %.1f, got %.1f", blendedExpectedMarginPct, report.ExpectedMarginPct)
	}
	// Verify the status classification logic.
	greenReport, _ := c.Variance(context.Background(), testDate, []TierActuals{
		{Tier: "pro", UserCount: 100, RevTotal: 1000, COGSTotal: 380}, // ~62% margin → GREEN
	})
	if greenReport.Status != "GREEN" {
		t.Errorf("expected GREEN for near-expected margin, got %s (delta=%.2f)", greenReport.Status, greenReport.DeltaPP)
	}
}

// TestVariance_NeverExecsDeadModelPy pins the DEAD-MODEL EXEC RETIREMENT: even a
// Calculator constructed WITHOUT UseHardcoded (the old code path that shelled out
// to `python3 billingmodel/model.py --json`) must NOT exec anything — it resolves
// the fixed projection table and reports ModelSource "hardcoded". A default (zero-
// value) Calculator is the strongest probe: previously it would have tried to run
// the deprecated per-active-user model.py; now there is no exec path at all.
func TestVariance_NeverExecsDeadModelPy(t *testing.T) {
	// Zero-value Calculator: UseHardcoded is false, ModelPyPath is empty.
	c := &Calculator{}
	report, err := c.Variance(context.Background(), testDate, []TierActuals{
		{Tier: "pro", UserCount: 100, RevTotal: 900, COGSTotal: 383},
	})
	if err != nil {
		t.Fatalf("Variance on a zero-value Calculator must not error (no exec, no python3): %v", err)
	}
	if report.ModelSource != "hardcoded" {
		t.Fatalf("ModelSource = %q, want hardcoded — the deprecated model.py exec must be retired", report.ModelSource)
	}
	if report.ExpectedMarginPct != blendedExpectedMarginPct {
		t.Errorf("ExpectedMarginPct = %.1f, want %.1f (fixed table)", report.ExpectedMarginPct, blendedExpectedMarginPct)
	}
}

// Additional: verify statusFromDelta thresholds.
func TestStatusFromDelta(t *testing.T) {
	cases := []struct {
		delta  float64
		expect string
	}{
		{0, "GREEN"},
		{4.9, "GREEN"},
		{5.0, "YELLOW"},
		{10.0, "YELLOW"},
		{10.1, "RED"},
		{-4.9, "GREEN"},
		{-5.0, "YELLOW"},
		{-10.1, "RED"},
	}
	for _, tc := range cases {
		got := statusFromDelta(tc.delta)
		if got != tc.expect {
			t.Errorf("statusFromDelta(%.1f) = %s, want %s", tc.delta, got, tc.expect)
		}
	}
}
