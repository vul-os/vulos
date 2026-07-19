package ddos

import (
	"context"
	"log"
	"os"
	"strconv"
	"sync"
	"time"
)

// BudgetConfig holds thresholds for the cost circuit breaker.
type BudgetConfig struct {
	MaxFlyMachines  int
	HourlyBudgetUSD float64
}

// DefaultBudgetConfig reads from env with safe defaults.
func DefaultBudgetConfig() BudgetConfig {
	cfg := BudgetConfig{
		MaxFlyMachines:  10,
		HourlyBudgetUSD: 50.0,
	}
	if s := os.Getenv("VULOS_MAX_FLY_MACHINES"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			cfg.MaxFlyMachines = n
		}
	}
	if s := os.Getenv("VULOS_HOURLY_BUDGET_USD"); s != "" {
		if f, err := strconv.ParseFloat(s, 64); err == nil && f > 0 {
			cfg.HourlyBudgetUSD = f
		}
	}
	return cfg
}

// BudgetReaders provides the raw metrics the circuit breaker checks.
// Either field may be nil (stub mode — treated as 0).
type BudgetReaders struct {
	// FlyInstanceCount returns the current number of running Fly instances.
	FlyInstanceCount func(ctx context.Context) (int, error)
	// ManagedEgressGB returns the current hour's the managed store egress in GB.
	ManagedEgressGB func(ctx context.Context) (float64, error)
}

// BudgetState tracks the current watermark and halt flag.
type BudgetState struct {
	mu              sync.RWMutex
	FlyMachines     int
	ManagedEgressGB float64
	HaltAutoscale   bool
	TrippedAt       *time.Time
}

// BudgetCircuitBreaker polls resource usage and flips HaltAutoscale when
// thresholds are exceeded.
type BudgetCircuitBreaker struct {
	cfg     BudgetConfig
	readers BudgetReaders
	bl      *BlocklistStore
	al      HoneypotAuditLogger // reuse interface for budget audit log
	State   *BudgetState
}

// NewBudgetCircuitBreaker creates a circuit breaker with the given config and readers.
func NewBudgetCircuitBreaker(cfg BudgetConfig, readers BudgetReaders, bl *BlocklistStore, al HoneypotAuditLogger) *BudgetCircuitBreaker {
	return &BudgetCircuitBreaker{
		cfg:     cfg,
		readers: readers,
		bl:      bl,
		al:      al,
		State:   &BudgetState{},
	}
}

// Poll performs one check cycle. Call periodically (e.g., hourly).
func (b *BudgetCircuitBreaker) Poll(ctx context.Context) {
	machines := 0
	egressGB := 0.0

	if b.readers.FlyInstanceCount != nil {
		n, err := b.readers.FlyInstanceCount(ctx)
		if err != nil {
			log.Printf("[ddos/budget] FlyInstanceCount: %v", err)
		} else {
			machines = n
		}
	}
	if b.readers.ManagedEgressGB != nil {
		g, err := b.readers.ManagedEgressGB(ctx)
		if err != nil {
			log.Printf("[ddos/budget] ManagedEgressGB: %v", err)
		} else {
			egressGB = g
		}
	}

	b.State.mu.Lock()
	b.State.FlyMachines = machines
	b.State.ManagedEgressGB = egressGB
	b.State.mu.Unlock()

	// Estimate hourly cost: $0.02/machine-hour + $0.02/GB egress (rough).
	estimatedUSD := float64(machines)*0.02 + egressGB*0.02

	exceeded := machines > b.cfg.MaxFlyMachines || estimatedUSD > b.cfg.HourlyBudgetUSD
	if exceeded {
		now := time.Now().UTC()
		log.Printf("[ddos/budget] THRESHOLD EXCEEDED machines=%d (max=%d) estimatedUSD=%.2f (budget=%.2f)",
			machines, b.cfg.MaxFlyMachines, estimatedUSD, b.cfg.HourlyBudgetUSD)

		b.State.mu.Lock()
		if !b.State.HaltAutoscale {
			b.State.HaltAutoscale = true
			b.State.TrippedAt = &now
		}
		b.State.mu.Unlock()

		// Persist halt flag to ddos_settings so it survives restarts.
		if b.bl != nil {
			_ = b.bl.SetSetting(ctx, "halt_autoscale", "1")
		}
		// Audit log.
		if b.al != nil {
			_ = b.al.Record(ctx, "system", "ddos.budget.exceeded", "autoscale",
				map[string]string{
					"machines":      strconv.Itoa(machines),
					"egress_gb":     strconv.FormatFloat(egressGB, 'f', 2, 64),
					"estimated_usd": strconv.FormatFloat(estimatedUSD, 'f', 2, 64),
				})
		}
		// Emit metric (log-based — picked up by Prometheus log scraper).
		log.Printf("[metric] ddos_budget_exceeded{machines=%d,egress_gb=%.2f,estimated_usd=%.2f}",
			machines, egressGB, estimatedUSD)
	} else {
		// Auto-reset if back under threshold.
		b.State.mu.Lock()
		if b.State.HaltAutoscale {
			log.Printf("[ddos/budget] budget back under threshold — clearing halt flag")
			b.State.HaltAutoscale = false
			b.State.TrippedAt = nil
			if b.bl != nil {
				ctx2, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = b.bl.SetSetting(ctx2, "halt_autoscale", "0")
			}
		}
		b.State.mu.Unlock()
	}
}

// RunHourly runs Poll on startup and then every hour until ctx is cancelled.
func (b *BudgetCircuitBreaker) RunHourly(ctx context.Context) {
	b.Poll(ctx)
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			b.Poll(ctx)
		}
	}
}

// Snapshot returns current watermark values for the dashboard.
func (b *BudgetCircuitBreaker) Snapshot() (machines int, egressGB float64, halt bool, trippedAt *time.Time) {
	b.State.mu.RLock()
	defer b.State.mu.RUnlock()
	return b.State.FlyMachines, b.State.ManagedEgressGB, b.State.HaltAutoscale, b.State.TrippedAt
}

// SetHaltAutoscale is the super-admin override for the halt flag.
func (b *BudgetCircuitBreaker) SetHaltAutoscale(ctx context.Context, halt bool) {
	b.State.mu.Lock()
	b.State.HaltAutoscale = halt
	if !halt {
		b.State.TrippedAt = nil
	}
	b.State.mu.Unlock()
	if b.bl != nil {
		val := "0"
		if halt {
			val = "1"
		}
		_ = b.bl.SetSetting(ctx, "halt_autoscale", val)
	}
}
