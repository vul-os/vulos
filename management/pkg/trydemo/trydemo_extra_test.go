// trydemo_extra_test.go — additional tests to raise trydemo coverage above 60%.
// Covers: ConfigFromEnv, queue SecsLeft, store Open + LogEvent + Close.
package trydemo

import (
	"context"
	"testing"
	"time"

	"github.com/vul-os/vulos-management/pkg/cpdb"
)

// ────────────────────────────────────────────────────────────────────────────
// ConfigFromEnv
// ────────────────────────────────────────────────────────────────────────────

func TestConfigFromEnv_Defaults(t *testing.T) {
	// Unset all env vars so we get pure defaults.
	t.Setenv("TRY_DEMO_MAX_VIEWERS", "")
	t.Setenv("TRY_DEMO_DRIVER_SESSION_SECS", "")
	t.Setenv("TRY_DEMO_DRIVER_WARN_SECS", "")
	t.Setenv("TRY_DEMO_DRIVER_IDLE_SECS", "")
	t.Setenv("TRY_DEMO_PER_IP_CONCURRENT", "")
	t.Setenv("TRY_DEMO_PER_IP_DRIVER_COOLDOWN_SECS", "")
	t.Setenv("TRY_DEMO_IDLE_STOP_SECS", "")
	t.Setenv("TRY_DEMO_MONTHLY_COGS_CAP_USD", "")
	t.Setenv("TRY_DEMO_FLY_REGION", "")
	t.Setenv("TRY_DEMO_FLY_APP", "")
	t.Setenv("TRY_DEMO_FLY_MACHINE_ID", "")

	cfg := ConfigFromEnv()

	if cfg.MaxViewers != 8 {
		t.Errorf("MaxViewers default: want 8, got %d", cfg.MaxViewers)
	}
	if cfg.DriverSessionSecs != 600 {
		t.Errorf("DriverSessionSecs default: want 600, got %d", cfg.DriverSessionSecs)
	}
	if cfg.DriverWarnSecs != 120 {
		t.Errorf("DriverWarnSecs default: want 120, got %d", cfg.DriverWarnSecs)
	}
	if cfg.DriverIdleSecs != 60 {
		t.Errorf("DriverIdleSecs default: want 60, got %d", cfg.DriverIdleSecs)
	}
	if cfg.PerIPConcurrent != 1 {
		t.Errorf("PerIPConcurrent default: want 1, got %d", cfg.PerIPConcurrent)
	}
	if cfg.PerIPDriverCooldownSecs != 1800 {
		t.Errorf("PerIPDriverCooldownSecs default: want 1800, got %d", cfg.PerIPDriverCooldownSecs)
	}
	if cfg.IdleStopSecs != 300 {
		t.Errorf("IdleStopSecs default: want 300, got %d", cfg.IdleStopSecs)
	}
	if cfg.MonthlyCostCapUSD != 100 {
		t.Errorf("MonthlyCostCapUSD default: want 100, got %.2f", cfg.MonthlyCostCapUSD)
	}
	if cfg.Region != "iad" {
		t.Errorf("Region default: want iad, got %q", cfg.Region)
	}
	if cfg.FlyApp != "vulos-try" {
		t.Errorf("FlyApp default: want vulos-try, got %q", cfg.FlyApp)
	}
}

func TestConfigFromEnv_Overrides(t *testing.T) {
	t.Setenv("TRY_DEMO_MAX_VIEWERS", "12")
	t.Setenv("TRY_DEMO_DRIVER_SESSION_SECS", "300")
	t.Setenv("TRY_DEMO_DRIVER_WARN_SECS", "60")
	t.Setenv("TRY_DEMO_DRIVER_IDLE_SECS", "30")
	t.Setenv("TRY_DEMO_PER_IP_CONCURRENT", "2")
	t.Setenv("TRY_DEMO_PER_IP_DRIVER_COOLDOWN_SECS", "900")
	t.Setenv("TRY_DEMO_IDLE_STOP_SECS", "120")
	t.Setenv("TRY_DEMO_MONTHLY_COGS_CAP_USD", "50.5")
	t.Setenv("TRY_DEMO_FLY_REGION", "fra")
	t.Setenv("TRY_DEMO_FLY_APP", "my-vulos-try")
	t.Setenv("TRY_DEMO_FLY_MACHINE_ID", "mach-abc123")

	cfg := ConfigFromEnv()

	if cfg.MaxViewers != 12 {
		t.Errorf("MaxViewers override: want 12, got %d", cfg.MaxViewers)
	}
	if cfg.DriverSessionSecs != 300 {
		t.Errorf("DriverSessionSecs override: want 300, got %d", cfg.DriverSessionSecs)
	}
	if cfg.MonthlyCostCapUSD != 50.5 {
		t.Errorf("MonthlyCostCapUSD override: want 50.5, got %.2f", cfg.MonthlyCostCapUSD)
	}
	if cfg.Region != "fra" {
		t.Errorf("Region override: want fra, got %q", cfg.Region)
	}
	if cfg.FlyApp != "my-vulos-try" {
		t.Errorf("FlyApp override: want my-vulos-try, got %q", cfg.FlyApp)
	}
	if cfg.FlyMachineID != "mach-abc123" {
		t.Errorf("FlyMachineID override: want mach-abc123, got %q", cfg.FlyMachineID)
	}
}

func TestConfigFromEnv_InvalidInt_FallsToDefault(t *testing.T) {
	t.Setenv("TRY_DEMO_MAX_VIEWERS", "not-a-number")
	cfg := ConfigFromEnv()
	if cfg.MaxViewers != 8 {
		t.Errorf("invalid int should fall to default 8, got %d", cfg.MaxViewers)
	}
}

func TestConfigFromEnv_InvalidFloat_FallsToDefault(t *testing.T) {
	t.Setenv("TRY_DEMO_MONTHLY_COGS_CAP_USD", "$$$$")
	cfg := ConfigFromEnv()
	if cfg.MonthlyCostCapUSD != 100 {
		t.Errorf("invalid float should fall to default 100, got %.2f", cfg.MonthlyCostCapUSD)
	}
}

// ────────────────────────────────────────────────────────────────────────────
// Queue.SecsLeft
// ────────────────────────────────────────────────────────────────────────────

func TestQueue_SecsLeft_NoDriver(t *testing.T) {
	cfg := Config{
		MaxViewers:        4,
		DriverSessionSecs: 600,
		DriverIdleSecs:    60,
		DriverWarnSecs:    30,
	}
	st := newTestTrydemoStore(t)
	cg := newCostGuard(st, cfg.MonthlyCostCapUSD)
	q := newQueue(cfg, st, cg)

	// No driver → should return 0.
	if s := q.SecsLeft(); s != 0 {
		t.Errorf("SecsLeft with no driver: want 0, got %d", s)
	}
}

func TestQueue_SecsLeft_WithDriver(t *testing.T) {
	cfg := Config{
		MaxViewers:              4,
		DriverSessionSecs:       600,
		DriverIdleSecs:          60,
		DriverWarnSecs:          30,
		PerIPConcurrent:         5, // allow multiple joins from same IP in tests
		PerIPDriverCooldownSecs: 0, // no cooldown
	}
	st := newTestTrydemoStore(t)
	cg := newCostGuard(st, 100)
	q := newQueue(cfg, st, cg)

	// Join as driver.
	ctx := context.Background()
	tok, role, _, err := q.Join(ctx, "127.99.99.1")
	if err != nil {
		t.Fatalf("Join: %v", err)
	}
	if role != RoleDriver {
		t.Skipf("first join was not driver (role=%v) — queue has pre-existing state", role)
	}
	_ = tok

	secs := q.SecsLeft()
	if secs <= 0 || secs > cfg.DriverSessionSecs {
		t.Errorf("SecsLeft with driver: want 0 < secs ≤ %d, got %d", cfg.DriverSessionSecs, secs)
	}
}

// ────────────────────────────────────────────────────────────────────────────
// Store.OpenStore + LogEvent + Close
// ────────────────────────────────────────────────────────────────────────────

func newTestTrydemoStore(t *testing.T) Store {
	t.Helper()
	db, err := cpdb.OpenSQLiteDSN(":memory:")
	if err != nil {
		t.Fatalf("cpdb.OpenSQLiteDSN: %v", err)
	}
	st, err := Open(db)
	if err != nil {
		db.Close()
		t.Fatalf("trydemo.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestOpenStore_OK(t *testing.T) {
	st := newTestTrydemoStore(t)
	if st == nil {
		t.Error("OpenStore returned nil store")
	}
}

func TestStore_LogEvent_Extra(t *testing.T) {
	st := newTestTrydemoStore(t)
	ctx := context.Background()

	err := st.LogEvent(ctx, "join", "192.168.1.1", "tok-abc", "", time.Now().UTC())
	if err != nil {
		t.Fatalf("LogEvent: %v", err)
	}

	// Log a second event with a reason.
	err = st.LogEvent(ctx, "leave", "192.168.1.1", "tok-abc", "idle_timeout", time.Now().UTC())
	if err != nil {
		t.Fatalf("LogEvent with reason: %v", err)
	}
}

func TestStore_RecordDriverClaim_And_LastDriverClaim(t *testing.T) {
	st := newTestTrydemoStore(t)
	ctx := context.Background()

	// Initially no claim.
	ts, err := st.LastDriverClaim(ctx, "10.0.0.1")
	if err != nil {
		t.Fatalf("LastDriverClaim initial: %v", err)
	}
	if !ts.IsZero() {
		t.Errorf("LastDriverClaim initial: want zero time, got %v", ts)
	}

	// Record a claim.
	now := time.Now().UTC().Truncate(time.Second)
	if err := st.RecordDriverClaim(ctx, "10.0.0.1", now); err != nil {
		t.Fatalf("RecordDriverClaim: %v", err)
	}

	// Should now be set.
	ts2, err := st.LastDriverClaim(ctx, "10.0.0.1")
	if err != nil {
		t.Fatalf("LastDriverClaim after record: %v", err)
	}
	if !ts2.Equal(now) {
		t.Errorf("LastDriverClaim: want %v, got %v", now, ts2)
	}
}

// ────────────────────────────────────────────────────────────────────────────
// Manager — NewManager + Close + MachineWarm + QueueRef + CostGuardRef
// ────────────────────────────────────────────────────────────────────────────

func TestManager_NewAndClose(t *testing.T) {
	st := newTestTrydemoStore(t)
	dm := NewNoopDemoMachines()
	cfg := ConfigFromEnv()
	cfg.PerIPConcurrent = 5
	cfg.PerIPDriverCooldownSecs = 0

	m := NewManager(st, dm, cfg)
	defer m.Close()

	// MachineWarm defaults to false until the sync goroutine fires.
	// Just verify the type is correct.
	_ = m.MachineWarm()

	// QueueRef and CostGuardRef must return non-nil.
	if m.QueueRef() == nil {
		t.Error("QueueRef() should not be nil")
	}
	if m.CostGuardRef() == nil {
		t.Error("CostGuardRef() should not be nil")
	}
}

func TestStore_AccumulateUsage_And_MonthlyCostUSD(t *testing.T) {
	st := newTestTrydemoStore(t)
	ctx := context.Background()

	const yyyymm = "202605"

	// Start from zero.
	cost, _ := st.MonthlyCostUSD(ctx, yyyymm)
	if cost != 0 {
		t.Errorf("MonthlyCostUSD initial: want 0, got %.4f", cost)
	}

	// Accumulate.
	if err := st.AccumulateUsage(ctx, yyyymm, 10.0, 1024, 0.05); err != nil {
		t.Fatalf("AccumulateUsage: %v", err)
	}
	if err := st.AccumulateUsage(ctx, yyyymm, 5.0, 512, 0.02); err != nil {
		t.Fatalf("AccumulateUsage 2: %v", err)
	}

	cost2, err := st.MonthlyCostUSD(ctx, yyyymm)
	if err != nil {
		t.Fatalf("MonthlyCostUSD: %v", err)
	}
	// 0.05 + 0.02 = 0.07
	if cost2 < 0.06 || cost2 > 0.08 {
		t.Errorf("MonthlyCostUSD: want ~0.07, got %.4f", cost2)
	}
}
