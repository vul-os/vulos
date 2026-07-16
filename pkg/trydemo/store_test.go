package trydemo

import (
	"context"
	"testing"
	"time"

	"github.com/vul-os/vulos-management/pkg/cpdb"
)

func openTestStore(t *testing.T) Store {
	t.Helper()
	db, err := cpdb.OpenSQLiteDSN(":memory:")
	if err != nil {
		t.Fatalf("cpdb.OpenSQLiteDSN: %v", err)
	}
	s, err := Open(db)
	if err != nil {
		db.Close()
		t.Fatalf("trydemo.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestStore_MigrationIdempotent(t *testing.T) {
	// Open twice on the same in-memory DB — migrations are idempotent.
	db, err := cpdb.OpenSQLiteDSN(":memory:")
	if err != nil {
		t.Fatalf("cpdb.OpenSQLiteDSN: %v", err)
	}
	defer db.Close()
	for i := 0; i < 2; i++ {
		if _, err2 := Open(db); err2 != nil {
			t.Fatalf("Open run %d: %v", i+1, err2)
		}
	}
}

func TestStore_DriverClaim_RoundTrip(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	ip := "192.0.2.1"

	// No record yet → zero time.
	got, err := s.LastDriverClaim(ctx, ip)
	if err != nil {
		t.Fatalf("LastDriverClaim (empty): %v", err)
	}
	if !got.IsZero() {
		t.Errorf("expected zero time, got %v", got)
	}

	// Record a claim.
	now := time.Now().UTC().Truncate(time.Second)
	if err := s.RecordDriverClaim(ctx, ip, now); err != nil {
		t.Fatalf("RecordDriverClaim: %v", err)
	}

	// Read back.
	got, err = s.LastDriverClaim(ctx, ip)
	if err != nil {
		t.Fatalf("LastDriverClaim (after record): %v", err)
	}
	if !got.Equal(now) {
		t.Errorf("got %v, want %v", got, now)
	}

	// Overwrite with a newer time.
	later := now.Add(5 * time.Minute)
	if err := s.RecordDriverClaim(ctx, ip, later); err != nil {
		t.Fatalf("RecordDriverClaim (update): %v", err)
	}
	got, err = s.LastDriverClaim(ctx, ip)
	if err != nil {
		t.Fatalf("LastDriverClaim (after update): %v", err)
	}
	if !got.Equal(later) {
		t.Errorf("got %v, want %v", got, later)
	}
}

func TestStore_AccumulateUsage(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	month := "202605"

	// No record yet → 0.
	cost, err := s.MonthlyCostUSD(ctx, month)
	if err != nil {
		t.Fatalf("MonthlyCostUSD (empty): %v", err)
	}
	if cost != 0 {
		t.Errorf("expected 0, got %v", cost)
	}

	// Accumulate in two calls.
	if err := s.AccumulateUsage(ctx, month, 10, 1024, 0.5); err != nil {
		t.Fatalf("AccumulateUsage 1: %v", err)
	}
	if err := s.AccumulateUsage(ctx, month, 5, 512, 0.25); err != nil {
		t.Fatalf("AccumulateUsage 2: %v", err)
	}

	cost, err = s.MonthlyCostUSD(ctx, month)
	if err != nil {
		t.Fatalf("MonthlyCostUSD (after accum): %v", err)
	}
	const want = 0.75
	if abs(cost-want) > 1e-9 {
		t.Errorf("cost = %v, want %v", cost, want)
	}
}

func TestStore_LogEvent(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	// Log several events — must not error.
	events := []struct{ typ, ip, sid, reason string }{
		{"driver_claim", "1.2.3.4", "tok-abc", "join_direct"},
		{"driver_release", "1.2.3.4", "tok-abc", "session_timeout"},
		{"machine_start", "", "", ""},
		{"cap_engaged", "", "", "promote_blocked"},
	}
	for _, e := range events {
		if err := s.LogEvent(ctx, e.typ, e.ip, e.sid, e.reason, now); err != nil {
			t.Errorf("LogEvent(%q): %v", e.typ, err)
		}
	}
}

func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}
