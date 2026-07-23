package security

import (
	"context"
	"testing"
)

// TestEgressBaseline verifies baseline calculation with fewer than 3 samples.
func TestEgressBaseline(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	// With no samples, baseline should return zero mean/stddev without error.
	mean, stddev, n, err := store.EgressBaseline(ctx, "user@vulos.org")
	if err != nil {
		t.Fatalf("EgressBaseline: %v", err)
	}
	if n != 0 || mean != 0 {
		t.Errorf("want zero baseline with no samples, got n=%d mean=%.0f", n, mean)
	}
	_ = stddev
}

// TestEgressAnomalyRecorded verifies that a spike is recorded in the DB.
func TestEgressAnomalyRecorded(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	// Seed 7 days of baseline samples: 1 MB/hr each.
	for i := 0; i < 10; i++ {
		_ = store.RecordEgressSample(ctx, "biguser@vulos.org", 1_000_000)
	}

	// Record an anomaly directly (simulating a spike > 3σ above baseline).
	_ = store.RecordEgressAnomaly(ctx, "biguser@vulos.org", 50_000_000, 1_000_000, 50_000)

	anomalies, err := store.RecentEgressAnomalies(ctx, 10)
	if err != nil {
		t.Fatalf("RecentEgressAnomalies: %v", err)
	}
	found := false
	for _, a := range anomalies {
		if a.UserID == "biguser@vulos.org" {
			found = true
		}
	}
	if !found {
		t.Error("want egress anomaly recorded for biguser")
	}
}

// TestEgressNoFlagBelowThreshold verifies low-egress users are not flagged.
func TestEgressNoFlagBelowThreshold(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	// 10 samples at 1MB each, then "spike" of 1.5MB — well within 3σ.
	for i := 0; i < 10; i++ {
		_ = store.RecordEgressSample(ctx, "normal@vulos.org", 1_000_000)
	}

	// checkEgressAnomaly with 1.5MB bytes (far below 3σ).
	checkEgressAnomaly(ctx, store, "normal@vulos.org", 1_500_000)

	anomalies, _ := store.RecentEgressAnomalies(ctx, 10)
	for _, a := range anomalies {
		if a.UserID == "normal@vulos.org" {
			t.Error("want no anomaly for normal egress user")
		}
	}
}
