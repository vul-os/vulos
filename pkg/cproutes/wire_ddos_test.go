package cproutes

import (
	"context"
	"testing"
	"time"

	"github.com/vul-os/vulos-management/pkg/cpdb"
	"github.com/vul-os/vulos-management/pkg/ddos"
	flycosting "github.com/vul-os/vulos-management/pkg/superadmin/costing/fly"
)

func openFlyTestStore(t *testing.T) *flycosting.Store {
	t.Helper()
	db, err := cpdb.OpenSQLiteDSN(":memory:")
	if err != nil {
		t.Fatalf("cpdb.OpenSQLiteDSN: %v", err)
	}
	st, err := flycosting.Open(db)
	if err != nil {
		db.Close()
		t.Fatalf("flycosting.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// TestBudgetBreakerTripsOnHighMachineCount verifies that the circuit breaker
// trips when the Fly instance count exceeds the configured threshold.
func TestBudgetBreakerTripsOnHighMachineCount(t *testing.T) {
	flySt := openFlyTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	// Insert enough app snapshots to exceed the default max (10 machines).
	// We simulate 15 distinct app names in the latest poll.
	snaps := make([]flycosting.Snapshot, 15)
	for i := range snaps {
		snaps[i] = flycosting.Snapshot{
			TakenAt:   now,
			OrgID:     "test-org",
			AppName:   "app-" + string(rune('a'+i)),
			Region:    "iad",
			AmountUSD: 0.10,
			Currency:  "USD",
		}
	}
	if err := flySt.Insert(ctx, snaps); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	// COORDINATOR: original wired a real internal/costing/tigris.Store here; the
	// egress reader is stubbed to 0 to avoid importing the cloud-internal package.
	// The assertion only exercises the fly-machine-count trip.
	readers := ddos.BudgetReaders{
		FlyInstanceCount: func(ctx context.Context) (int, error) {
			return flySt.LatestMachineCount(ctx)
		},
		ManagedEgressGB: func(_ context.Context) (float64, error) {
			return 0, nil
		},
	}

	cfg := ddos.DefaultBudgetConfig() // MaxFlyMachines = 10 by default
	breaker := ddos.NewBudgetCircuitBreaker(cfg, readers, nil, nil)
	breaker.Poll(ctx)

	_, _, halt, _ := breaker.Snapshot()
	if !halt {
		t.Error("want HaltAutoscale=true when machine count (15) exceeds threshold (10); got false")
	}
}

// TestBudgetBreakerNoTripOnZeroReaders verifies that the circuit breaker does
// NOT trip when both readers return 0 (nil store / env vars unset).
func TestBudgetBreakerNoTripOnZeroReaders(t *testing.T) {
	readers := ddos.BudgetReaders{
		FlyInstanceCount: func(_ context.Context) (int, error) { return 0, nil },
		ManagedEgressGB:  func(_ context.Context) (float64, error) { return 0, nil },
	}

	cfg := ddos.DefaultBudgetConfig()
	breaker := ddos.NewBudgetCircuitBreaker(cfg, readers, nil, nil)
	breaker.Poll(context.Background())

	_, _, halt, _ := breaker.Snapshot()
	if halt {
		t.Error("want HaltAutoscale=false when readers return 0; got true")
	}
}
