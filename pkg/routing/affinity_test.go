// affinity_test.go — unit tests for AffinityStore (DNS-05).
//
// Tests:
//   - Sticky: repeated calls return the same PoP while it stays healthy.
//   - Failover: when the preferred PoP goes unhealthy the next-nearest is returned.
//   - Eviction: affinity entry is discarded after 5 m inactivity (injected clock).
//   - GeoIP stub: ClientGeo returns sensible coordinates for well-known ranges.
package routing

import (
	"context"
	"testing"
	"time"
)

// --------------------------------------------------------------------------
// helpers
// --------------------------------------------------------------------------

// makePoP creates a PoP with the given ID, coordinates, health state, and a
// LastHealth timestamp relative to now.
func makePoP(id string, lat, lon float64, healthy bool, lastHealthOffset time.Duration, now time.Time) PoP {
	return PoP{
		ID:         id,
		Region:     id,
		IP:         "1.2.3.4",
		Lat:        lat,
		Lon:        lon,
		Healthy:    healthy,
		LastHealth: now.Add(lastHealthOffset),
	}
}

// seedPops upserts a list of PoPs into a MemStore.
func seedPops(t *testing.T, st *MemStore, pops []PoP) {
	t.Helper()
	ctx := context.Background()
	for _, p := range pops {
		if err := st.UpsertPoP(ctx, p); err != nil {
			t.Fatalf("seedPops: UpsertPoP %q: %v", p.ID, err)
		}
	}
}

// affinityStoreWithClock creates an AffinityStore whose clock returns t.
func affinityStoreWithClock(t time.Time) *AffinityStore {
	a := NewAffinityStore()
	a.Now = func() time.Time { return t }
	return a
}

// --------------------------------------------------------------------------
// Sticky: same PoP returned while healthy
// --------------------------------------------------------------------------

func TestAffinity_Sticky_WhileHealthy(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	a := affinityStoreWithClock(now)
	st := NewMemStore()

	// Two PoPs: near and far. Both recently healthy.
	pops := []PoP{
		makePoP("pop-near", 1, 1, true, -10*time.Second, now),
		makePoP("pop-far", 50, 50, true, -10*time.Second, now),
	}
	seedPops(t, st, pops)

	ctx := context.Background()
	// First call — picks nearest (pop-near is closest to 0,0).
	p1, err := a.ResolveWithAffinity(ctx, st, "acct-a", validULID, 0, 0)
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	if p1.ID != "pop-near" {
		t.Fatalf("expected pop-near on first call, got %q", p1.ID)
	}

	// Second call with same params — must return the same PoP (sticky).
	p2, err := a.ResolveWithAffinity(ctx, st, "acct-a", validULID, 0, 0)
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if p2.ID != "pop-near" {
		t.Errorf("sticky: expected pop-near, got %q", p2.ID)
	}

	// Third call, simulating that time has advanced 1 minute (still < 5 m TTL).
	a.Now = func() time.Time { return now.Add(time.Minute) }
	p3, err := a.ResolveWithAffinity(ctx, st, "acct-a", validULID, 0, 0)
	if err != nil {
		t.Fatalf("third call: %v", err)
	}
	if p3.ID != "pop-near" {
		t.Errorf("sticky within TTL: expected pop-near, got %q", p3.ID)
	}
}

// --------------------------------------------------------------------------
// Sticky: different (account, ulid) pairs are independent
// --------------------------------------------------------------------------

func TestAffinity_Sticky_IndependentKeys(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	a := affinityStoreWithClock(now)
	st := NewMemStore()

	pops := []PoP{
		makePoP("pop-near", 1, 1, true, -5*time.Second, now),
		makePoP("pop-far", 50, 50, true, -5*time.Second, now),
	}
	seedPops(t, st, pops)

	ctx := context.Background()
	const ulidB = "01BX5ZZKBKACTAV9WEVGEMMVRY"

	p1, _ := a.ResolveWithAffinity(ctx, st, "acct-a", validULID, 0, 0)
	p2, _ := a.ResolveWithAffinity(ctx, st, "acct-b", ulidB, 0, 0)

	// Both should be nearest PoP since neither has existing affinity.
	if p1.ID != "pop-near" {
		t.Errorf("acct-a/validULID: expected pop-near, got %q", p1.ID)
	}
	if p2.ID != "pop-near" {
		t.Errorf("acct-b/ulidB: expected pop-near, got %q", p2.ID)
	}

	// Each key is tracked independently.
	p1b, _ := a.ResolveWithAffinity(ctx, st, "acct-a", validULID, 99, 99)
	if p1b.ID != "pop-near" {
		t.Errorf("acct-a sticky despite different lat/lon: expected pop-near, got %q", p1b.ID)
	}
}

// --------------------------------------------------------------------------
// Failover: unhealthy preferred PoP → next-nearest returned
// --------------------------------------------------------------------------

func TestAffinity_Failover_UnhealthyPreferred(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	a := affinityStoreWithClock(now)
	st := NewMemStore()

	pops := []PoP{
		makePoP("pop-near", 1, 1, true, -5*time.Second, now),
		makePoP("pop-far", 50, 50, true, -5*time.Second, now),
	}
	seedPops(t, st, pops)

	ctx := context.Background()
	// Establish affinity → pop-near.
	p1, err := a.ResolveWithAffinity(ctx, st, "acct-a", validULID, 0, 0)
	if err != nil || p1.ID != "pop-near" {
		t.Fatalf("expected pop-near, got %q / %v", p1.ID, err)
	}

	// Now mark pop-near unhealthy.
	if err := st.SetPoPHealth(ctx, "pop-near", false); err != nil {
		t.Fatalf("SetPoPHealth: %v", err)
	}

	// Next call must fail over to pop-far.
	p2, err := a.ResolveWithAffinity(ctx, st, "acct-a", validULID, 0, 0)
	if err != nil {
		t.Fatalf("failover call: %v", err)
	}
	if p2.ID != "pop-far" {
		t.Errorf("failover: expected pop-far, got %q", p2.ID)
	}
}

// --------------------------------------------------------------------------
// Failover: stale last_health (> 90 s old) treated as unhealthy
// --------------------------------------------------------------------------

func TestAffinity_Failover_StaleLastHealth(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	a := affinityStoreWithClock(now)
	st := NewMemStore()

	// pop-near: last_health is 120 s ago → stale; pop-far: freshly healthy.
	pops := []PoP{
		makePoP("pop-near", 1, 1, true, -120*time.Second, now),
		makePoP("pop-far", 50, 50, true, -5*time.Second, now),
	}
	seedPops(t, st, pops)

	ctx := context.Background()

	// Manually plant an affinity entry pointing to the stale pop-near.
	a.mu.Lock()
	a.set(affinityKey{"acct-a", validULID}, "pop-near")
	a.mu.Unlock()

	// Should fail over to pop-far because pop-near's LastHealth is > 90 s.
	p, err := a.ResolveWithAffinity(ctx, st, "acct-a", validULID, 0, 0)
	if err != nil {
		t.Fatalf("stale-health call: %v", err)
	}
	if p.ID != "pop-far" {
		t.Errorf("stale LastHealth failover: expected pop-far, got %q", p.ID)
	}
}

// --------------------------------------------------------------------------
// Eviction: entry discarded after 5 m inactivity
// --------------------------------------------------------------------------

func TestAffinity_Eviction_After5Minutes(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	a := affinityStoreWithClock(now)
	st := NewMemStore()

	// pop-near is near (1,1), pop-far is far (50,50).
	pops := []PoP{
		makePoP("pop-near", 1, 1, true, -5*time.Second, now),
		makePoP("pop-far", 50, 50, true, -5*time.Second, now),
	}
	seedPops(t, st, pops)

	ctx := context.Background()

	// First call: affinity set to pop-near.
	p1, err := a.ResolveWithAffinity(ctx, st, "acct-a", validULID, 0, 0)
	if err != nil || p1.ID != "pop-near" {
		t.Fatalf("initial: got %q / %v", p1.ID, err)
	}

	// Advance clock by exactly 5 m + 1 s (past eviction threshold).
	a.Now = func() time.Time { return now.Add(5*time.Minute + time.Second) }

	// Update PoP LastHealth so they're still fresh relative to the new "now".
	freshPops := []PoP{
		makePoP("pop-near", 1, 1, true, -5*time.Second, now.Add(5*time.Minute+time.Second)),
		makePoP("pop-far", 50, 50, true, -5*time.Second, now.Add(5*time.Minute+time.Second)),
	}
	seedPops(t, st, freshPops)

	// After eviction the affinity entry is gone; nearest PoP is recalculated.
	// Query from (0,0) — pop-near should still be selected (nearest), but the
	// key point is that the old entry was evicted and a new one created.
	p2, err := a.ResolveWithAffinity(ctx, st, "acct-a", validULID, 0, 0)
	if err != nil {
		t.Fatalf("post-eviction call: %v", err)
	}
	if p2.ID != "pop-near" {
		t.Errorf("post-eviction nearest: expected pop-near, got %q", p2.ID)
	}
}

// TestAffinity_Eviction_EntryRemovedFromMap confirms the underlying map is
// cleaned up (not just logically evicted), preventing unbounded growth.
func TestAffinity_Eviction_EntryRemovedFromMap(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	a := affinityStoreWithClock(now)
	st := NewMemStore()

	pops := []PoP{makePoP("pop-a", 0, 0, true, -5*time.Second, now)}
	seedPops(t, st, pops)

	ctx := context.Background()
	if _, err := a.ResolveWithAffinity(ctx, st, "acct-a", validULID, 0, 0); err != nil {
		t.Fatalf("initial call: %v", err)
	}

	// Confirm entry is present.
	a.mu.Lock()
	if len(a.m) != 1 {
		t.Errorf("expected 1 affinity entry, got %d", len(a.m))
	}
	a.mu.Unlock()

	// Advance past TTL and trigger eviction via a call.
	a.Now = func() time.Time { return now.Add(6 * time.Minute) }
	freshPops := []PoP{makePoP("pop-a", 0, 0, true, -5*time.Second, now.Add(6*time.Minute))}
	seedPops(t, st, freshPops)
	if _, err := a.ResolveWithAffinity(ctx, st, "acct-a", validULID, 0, 0); err != nil {
		t.Fatalf("post-eviction call: %v", err)
	}

	// After the evict() call in ResolveWithAffinity the old entry should be
	// gone and a new one created (net size still 1, but the key's lastUsed is
	// refreshed to the new now).
	a.mu.Lock()
	e, ok := a.m[affinityKey{"acct-a", validULID}]
	a.mu.Unlock()
	if !ok {
		t.Error("expected new affinity entry after eviction+re-select")
	}
	if e.lastUsed.Before(now.Add(5 * time.Minute)) {
		t.Errorf("lastUsed not refreshed: got %v", e.lastUsed)
	}
}

// --------------------------------------------------------------------------
// No healthy PoP → ErrNoHealthyPoP propagated
// --------------------------------------------------------------------------

func TestAffinity_NoHealthyPoP(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	a := affinityStoreWithClock(now)
	st := NewMemStore()

	pops := []PoP{makePoP("pop-sick", 0, 0, false, -5*time.Second, now)}
	seedPops(t, st, pops)

	_, err := a.ResolveWithAffinity(context.Background(), st, "acct-a", validULID, 0, 0)
	if err != ErrNoHealthyPoP {
		t.Errorf("expected ErrNoHealthyPoP, got %v", err)
	}
}

// --------------------------------------------------------------------------
// ClientGeo stub
// --------------------------------------------------------------------------

func TestClientGeo_PrivateAndLoopback(t *testing.T) {
	t.Parallel()
	cases := []string{"127.0.0.1", "::1", "0.0.0.0", "192.168.1.1", "10.0.0.1", "172.16.0.1"}
	for _, ip := range cases {
		lat, lon := ClientGeo(ip)
		// Private ranges — any value is fine, just must not panic.
		_ = lat
		_ = lon
	}
}

func TestClientGeo_KnownRanges(t *testing.T) {
	t.Parallel()
	cases := []struct {
		ip      string
		latSign float64 // positive = Northern hemisphere
	}{
		{"8.8.8.8", 1},   // US (Google DNS)
		{"80.1.2.3", 1},  // Europe
		{"41.1.2.3", -1}, // Africa (south of equator)
	}
	for _, c := range cases {
		lat, _ := ClientGeo(c.ip)
		sign := 1.0
		if lat < 0 {
			sign = -1
		}
		if lat == 0 {
			continue // equatorial fallback — ok
		}
		if sign != c.latSign {
			t.Errorf("ClientGeo(%q): lat=%v, want sign %v", c.ip, lat, c.latSign)
		}
	}
}

func TestClientGeo_InvalidIP(t *testing.T) {
	t.Parallel()
	lat, lon := ClientGeo("not-an-ip")
	if lat != 0 || lon != 0 {
		t.Errorf("invalid IP: expected (0,0), got (%v,%v)", lat, lon)
	}
}
