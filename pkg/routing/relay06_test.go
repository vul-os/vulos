// relay06_test.go — tests for RELAY-06: PoP heartbeat upsert, drain/undrain,
// unhealthy-PoP pool exclusion, and ListPoPs.
//
// These tests exercise both the Store-level methods and the routing semantics
// (NearestPoP excludes unhealthy PoPs).
//
// NOTE: SQLStore tests do NOT run t.Parallel() because openTestStore uses a
// shared in-memory DSN ("file::memory:?cache=shared&mode=memory"). MemStore
// and pure-logic tests may use t.Parallel() freely.
package routing

import (
	"context"
	"testing"
	"time"
)

// --------------------------------------------------------------------------
// Heartbeat upsert — verifies UpsertPoP sets healthy + lat/lon + LastHealth
// --------------------------------------------------------------------------

func TestHeartbeatUpsert_SetsHealthyAndLastHealth(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	before := time.Now().UTC().Add(-time.Second)
	p := PoP{
		ID:         "pop-hb-shlh",
		Region:     "eu-west",
		IP:         "10.0.0.1",
		Lat:        51.5,
		Lon:        -0.1,
		Healthy:    true,
		LastHealth: time.Now().UTC(),
	}
	if err := s.UpsertPoP(ctx, p); err != nil {
		t.Fatalf("UpsertPoP: %v", err)
	}
	after := time.Now().UTC().Add(time.Second)

	pops, err := s.HealthyPoPs(ctx)
	if err != nil {
		t.Fatalf("HealthyPoPs: %v", err)
	}
	// Find our specific PoP (other tests may have written to shared DB).
	var got *PoP
	for i := range pops {
		if pops[i].ID == "pop-hb-shlh" {
			got = &pops[i]
			break
		}
	}
	if got == nil {
		t.Fatalf("pop-hb-shlh not found in healthy pops: %v", pops)
	}
	if !got.Healthy {
		t.Error("expected Healthy=true")
	}
	if got.Lat != 51.5 || got.Lon != -0.1 {
		t.Errorf("lat/lon: got %.2f/%.2f want 51.5/-0.1", got.Lat, got.Lon)
	}
	if got.Region != "eu-west" {
		t.Errorf("Region: got %q want eu-west", got.Region)
	}
	if got.LastHealth.Before(before) || got.LastHealth.After(after) {
		t.Errorf("LastHealth %v out of expected range [%v, %v]", got.LastHealth, before, after)
	}
}

func TestHeartbeatUpsert_UpdatesLastHealth(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	t1 := time.Now().UTC().Add(-10 * time.Second)
	p := PoP{
		ID:         "pop-ts-ulh",
		Region:     "us-east",
		IP:         "1.2.3.4",
		Healthy:    true,
		LastHealth: t1,
	}
	if err := s.UpsertPoP(ctx, p); err != nil {
		t.Fatalf("first UpsertPoP: %v", err)
	}

	t2 := time.Now().UTC()
	p.LastHealth = t2
	p.Region = "us-west" // also updating region
	if err := s.UpsertPoP(ctx, p); err != nil {
		t.Fatalf("second UpsertPoP: %v", err)
	}

	pops, err := s.HealthyPoPs(ctx)
	if err != nil {
		t.Fatalf("HealthyPoPs: %v", err)
	}
	var got *PoP
	for i := range pops {
		if pops[i].ID == "pop-ts-ulh" {
			got = &pops[i]
			break
		}
	}
	if got == nil {
		t.Fatalf("pop-ts-ulh not found in healthy pops")
	}
	// last_health should reflect the second upsert.
	if got.LastHealth.Before(t2.Add(-time.Second)) {
		t.Errorf("LastHealth not updated: got %v, want >= %v", got.LastHealth, t2.Add(-time.Second))
	}
	if got.Region != "us-west" {
		t.Errorf("Region not updated: got %q want us-west", got.Region)
	}
}

// --------------------------------------------------------------------------
// Drain / undrain — SetPoPHealth toggles healthy flag
// --------------------------------------------------------------------------

func TestDrainUndrain_ToggleHealthy(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	popID := "pop-drain-tg"
	p := PoP{ID: popID, Region: "af-south", IP: "5.6.7.8", Healthy: true, LastHealth: time.Now().UTC()}
	if err := s.UpsertPoP(ctx, p); err != nil {
		t.Fatalf("UpsertPoP: %v", err)
	}

	findPoP := func(pops []PoP) *PoP {
		for i := range pops {
			if pops[i].ID == popID {
				return &pops[i]
			}
		}
		return nil
	}

	// Initially healthy.
	h, err := s.HealthyPoPs(ctx)
	if err != nil {
		t.Fatalf("HealthyPoPs: %v", err)
	}
	if findPoP(h) == nil {
		t.Fatalf("%s should be healthy before drain", popID)
	}

	// Drain — set healthy=false.
	if err := s.SetPoPHealth(ctx, popID, false); err != nil {
		t.Fatalf("SetPoPHealth(drain): %v", err)
	}
	h, err = s.HealthyPoPs(ctx)
	if err != nil {
		t.Fatalf("HealthyPoPs after drain: %v", err)
	}
	if findPoP(h) != nil {
		t.Errorf("%s should not be healthy after drain", popID)
	}

	// Undrain — set healthy=true.
	if err := s.SetPoPHealth(ctx, popID, true); err != nil {
		t.Fatalf("SetPoPHealth(undrain): %v", err)
	}
	h, err = s.HealthyPoPs(ctx)
	if err != nil {
		t.Fatalf("HealthyPoPs after undrain: %v", err)
	}
	if findPoP(h) == nil {
		t.Errorf("%s should be healthy again after undrain", popID)
	}
}

func TestDrain_UpdatesLastHealth(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	popID := "pop-drain-ulh"
	p := PoP{ID: popID, Region: "ap-east", IP: "7.8.9.10", Healthy: true, LastHealth: time.Now().UTC().Add(-5 * time.Second)}
	if err := s.UpsertPoP(ctx, p); err != nil {
		t.Fatalf("UpsertPoP: %v", err)
	}

	drainTime := time.Now().UTC()
	if err := s.SetPoPHealth(ctx, popID, false); err != nil {
		t.Fatalf("SetPoPHealth: %v", err)
	}

	// last_health is updated by SetPoPHealth.
	all, err := s.ListPoPs(ctx)
	if err != nil {
		t.Fatalf("ListPoPs: %v", err)
	}
	var got *PoP
	for i := range all {
		if all[i].ID == popID {
			got = &all[i]
			break
		}
	}
	if got == nil {
		t.Fatalf("%s not found in ListPoPs", popID)
	}
	if got.LastHealth.Before(drainTime.Add(-time.Second)) {
		t.Errorf("LastHealth not updated on drain: got %v, want >= %v", got.LastHealth, drainTime.Add(-time.Second))
	}
}

func TestDrain_UnknownPoP_ErrNotFound(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	err := s.SetPoPHealth(ctx, "definitely-nonexistent-pop-relay06", false)
	if err != ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

// --------------------------------------------------------------------------
// ListPoPs — returns all PoPs sorted by ID (healthy + unhealthy)
// --------------------------------------------------------------------------

func TestListPoPs_IncludesUnhealthy(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	popsIn := []PoP{
		{ID: "pop-relay06-a", Region: "us-east", IP: "1.1.1.1", Healthy: true, LastHealth: now},
		{ID: "pop-relay06-b", Region: "eu-west", IP: "2.2.2.2", Healthy: false, LastHealth: now},
		{ID: "pop-relay06-c", Region: "ap-se", IP: "3.3.3.3", Healthy: true, LastHealth: now},
	}
	for _, p := range popsIn {
		if err := s.UpsertPoP(ctx, p); err != nil {
			t.Fatalf("UpsertPoP %q: %v", p.ID, err)
		}
	}

	all, err := s.ListPoPs(ctx)
	if err != nil {
		t.Fatalf("ListPoPs: %v", err)
	}
	// Filter to our test pops (shared DB may have others).
	var ourPops []PoP
	for _, p := range all {
		if p.ID == "pop-relay06-a" || p.ID == "pop-relay06-b" || p.ID == "pop-relay06-c" {
			ourPops = append(ourPops, p)
		}
	}
	if len(ourPops) != 3 {
		t.Fatalf("expected 3 relay06 pops, got %d (all: %v)", len(ourPops), all)
	}
	// Sorted by ID: a < b < c.
	if ourPops[0].ID != "pop-relay06-a" || ourPops[1].ID != "pop-relay06-b" || ourPops[2].ID != "pop-relay06-c" {
		t.Errorf("unexpected order: %v %v %v", ourPops[0].ID, ourPops[1].ID, ourPops[2].ID)
	}
}

// --------------------------------------------------------------------------
// Unhealthy PoP excluded from pool (NearestPoP semantics) — pure logic, parallel OK
// --------------------------------------------------------------------------

func TestUnhealthyExcludedFromPool(t *testing.T) {
	t.Parallel()
	// Verify NearestPoP skips unhealthy PoPs even when they are closest.
	pops := []PoP{
		{ID: "sick", Healthy: false, Lat: 0, Lon: 0, IP: "1.1.1.1"},
		{ID: "healthy-far", Healthy: true, Lat: 10, Lon: 10, IP: "2.2.2.2"},
	}
	got, err := NearestPoP(pops, 0, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.ID != "healthy-far" {
		t.Errorf("expected healthy-far, got %q", got.ID)
	}
}

func TestUnhealthyExcludedFromPool_AllUnhealthy(t *testing.T) {
	t.Parallel()
	pops := []PoP{
		{ID: "a", Healthy: false, Lat: 0, Lon: 0},
		{ID: "b", Healthy: false, Lat: 5, Lon: 5},
	}
	_, err := NearestPoP(pops, 0, 0)
	if err != ErrNoHealthyPoP {
		t.Errorf("expected ErrNoHealthyPoP, got %v", err)
	}
}

// --------------------------------------------------------------------------
// MemStore parity — same semantics as SQLStore, parallel OK
// --------------------------------------------------------------------------

func TestMemStore_ListPoPs_IncludesUnhealthy(t *testing.T) {
	t.Parallel()
	m := NewMemStore()
	ctx := context.Background()
	now := time.Now().UTC()

	popsIn := []PoP{
		{ID: "pop-x", Region: "r1", IP: "1.1.1.1", Healthy: true, LastHealth: now},
		{ID: "pop-y", Region: "r2", IP: "2.2.2.2", Healthy: false, LastHealth: now},
	}
	for _, p := range popsIn {
		if err := m.UpsertPoP(ctx, p); err != nil {
			t.Fatalf("UpsertPoP: %v", err)
		}
	}
	all, err := m.ListPoPs(ctx)
	if err != nil {
		t.Fatalf("ListPoPs: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("expected 2 pops, got %d", len(all))
	}
}

func TestMemStore_DrainUndrain_ToggleHealthy(t *testing.T) {
	t.Parallel()
	m := NewMemStore()
	ctx := context.Background()

	p := PoP{ID: "pop-mem-drain", Region: "eu", IP: "1.2.3.4", Healthy: true, LastHealth: time.Now().UTC()}
	if err := m.UpsertPoP(ctx, p); err != nil {
		t.Fatalf("UpsertPoP: %v", err)
	}

	h, err := m.HealthyPoPs(ctx)
	if err != nil {
		t.Fatalf("HealthyPoPs: %v", err)
	}
	if len(h) != 1 {
		t.Fatalf("expected 1 healthy pop, got %d", len(h))
	}

	if err := m.SetPoPHealth(ctx, "pop-mem-drain", false); err != nil {
		t.Fatalf("SetPoPHealth(drain): %v", err)
	}
	h, _ = m.HealthyPoPs(ctx)
	if len(h) != 0 {
		t.Errorf("expected 0 healthy after drain, got %d", len(h))
	}

	if err := m.SetPoPHealth(ctx, "pop-mem-drain", true); err != nil {
		t.Fatalf("SetPoPHealth(undrain): %v", err)
	}
	h, _ = m.HealthyPoPs(ctx)
	if len(h) != 1 {
		t.Errorf("expected 1 healthy after undrain, got %d", len(h))
	}
}
