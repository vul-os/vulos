package ddos

import (
	"context"
	"testing"
)

func TestBudgetCircuitBreaker_UnderThreshold(t *testing.T) {
	cfg := BudgetConfig{MaxFlyMachines: 10, HourlyBudgetUSD: 50}
	readers := BudgetReaders{
		FlyInstanceCount: func(_ context.Context) (int, error) { return 2, nil },
		TigrisEgressGB:   func(_ context.Context) (float64, error) { return 10.0, nil },
	}
	b := NewBudgetCircuitBreaker(cfg, readers, nil, nil)
	b.Poll(context.Background())

	machines, egress, halt, _ := b.Snapshot()
	if halt {
		t.Fatal("halt should be false when under threshold")
	}
	if machines != 2 {
		t.Errorf("want machines=2 got %d", machines)
	}
	if egress != 10.0 {
		t.Errorf("want egress=10.0 got %f", egress)
	}
}

func TestBudgetCircuitBreaker_ExceedsMachineCount(t *testing.T) {
	cfg := BudgetConfig{MaxFlyMachines: 3, HourlyBudgetUSD: 9999}
	readers := BudgetReaders{
		FlyInstanceCount: func(_ context.Context) (int, error) { return 5, nil },
		TigrisEgressGB:   func(_ context.Context) (float64, error) { return 0, nil },
	}
	b := NewBudgetCircuitBreaker(cfg, readers, nil, nil)
	b.Poll(context.Background())

	_, _, halt, trippedAt := b.Snapshot()
	if !halt {
		t.Fatal("halt should be true when machines > max")
	}
	if trippedAt == nil {
		t.Fatal("trippedAt should be set")
	}
}

func TestBudgetCircuitBreaker_SetHaltAutoscaleOverride(t *testing.T) {
	cfg := DefaultBudgetConfig()
	b := NewBudgetCircuitBreaker(cfg, BudgetReaders{}, nil, nil)
	b.SetHaltAutoscale(context.Background(), true)
	_, _, halt, _ := b.Snapshot()
	if !halt {
		t.Fatal("SetHaltAutoscale(true) should flip halt to true")
	}
	b.SetHaltAutoscale(context.Background(), false)
	_, _, halt, _ = b.Snapshot()
	if halt {
		t.Fatal("SetHaltAutoscale(false) should clear halt")
	}
}
