package trydemo

import (
	"context"
	"testing"
)

func TestCostGuard_NotEngagedWhenEmpty(t *testing.T) {
	s := openTestStore(t)
	cg := newCostGuard(s, 100)
	engaged, err := cg.Engaged(context.Background())
	if err != nil {
		t.Fatalf("Engaged: %v", err)
	}
	if engaged {
		t.Error("expected not engaged with empty accumulator")
	}
}

func TestCostGuard_EngagesAtCap(t *testing.T) {
	s := openTestStore(t)
	cap := 10.0
	cg := newCostGuard(s, cap)
	ctx := context.Background()
	month := currentMonth()

	// Accumulate cost just below the cap.
	if err := s.AccumulateUsage(ctx, month, 0, 0, 9.99); err != nil {
		t.Fatalf("AccumulateUsage: %v", err)
	}
	engaged, err := cg.Engaged(ctx)
	if err != nil {
		t.Fatalf("Engaged (below cap): %v", err)
	}
	if engaged {
		t.Error("expected not engaged below cap")
	}

	// Accumulate cost to exactly the cap.
	if err := s.AccumulateUsage(ctx, month, 0, 0, 0.01); err != nil {
		t.Fatalf("AccumulateUsage (to cap): %v", err)
	}
	engaged, err = cg.Engaged(ctx)
	if err != nil {
		t.Fatalf("Engaged (at cap): %v", err)
	}
	if !engaged {
		t.Error("expected engaged at cap")
	}
}

func TestCostGuard_Tick_AccumulatesMath(t *testing.T) {
	s := openTestStore(t)
	cg := newCostGuard(s, 1000)
	ctx := context.Background()

	// 1 compute-minute + 1 GB egress.
	const computeMin = 1.0
	const egressBytes = 1024 * 1024 * 1024 // 1 GB
	if err := cg.Tick(ctx, computeMin, egressBytes); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	// Check cost: 0.00025 + 0.02 = 0.02025 (approximately).
	cost, err := s.MonthlyCostUSD(ctx, currentMonth())
	if err != nil {
		t.Fatalf("MonthlyCostUSD: %v", err)
	}
	const want = 0.00025 + 0.02
	if abs(cost-want) > 1e-6 {
		t.Errorf("cost = %v, want ~%v", cost, want)
	}
}

func TestCostGuard_Promote_BlockedWhenEngaged(t *testing.T) {
	s := openTestStore(t)
	// Cap at zero — always engaged.
	cg := newCostGuard(s, 0)
	ctx := context.Background()

	cfg := Config{
		MaxViewers:              8,
		DriverSessionSecs:       600,
		DriverWarnSecs:          120,
		DriverIdleSecs:          60,
		PerIPConcurrent:         1,
		PerIPDriverCooldownSecs: 1800,
	}
	q := newQueue(cfg, s, cg)

	// Join as spectator (driver seat blocked by cap).
	token, role, _, err := q.Join(ctx, "1.2.3.4")
	if err != nil {
		t.Fatalf("Join: %v", err)
	}
	if role == RoleDriver {
		t.Errorf("expected non-driver role when cap engaged, got %v", role)
	}

	// Promote should be a no-op (no queue, driver seat also blocked).
	promoted, err := q.Promote(ctx)
	if err != nil {
		t.Fatalf("Promote: %v", err)
	}
	if promoted != "" {
		t.Errorf("expected no promotion when cap engaged, got token %q", promoted)
	}

	// Spectators still served — can heartbeat fine.
	if role == RoleSpectator {
		if err := q.Heartbeat(ctx, token); err != nil {
			t.Errorf("Heartbeat for spectator: %v", err)
		}
	}
}
