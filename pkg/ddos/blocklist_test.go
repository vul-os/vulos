package ddos

import (
	"context"
	"testing"
	"time"

	"github.com/vul-os/vulos-management/pkg/cpdb"
)

func openTestBlocklist(t *testing.T) *BlocklistStore {
	t.Helper()
	db, err := cpdb.OpenSQLiteDSN(":memory:")
	if err != nil {
		t.Fatalf("cpdb open: %v", err)
	}
	s, err := OpenBlocklistStore(db)
	if err != nil {
		db.Close()
		t.Fatalf("OpenBlocklistStore: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestBlocklist_AddAndIsBlocked(t *testing.T) {
	s := openTestBlocklist(t)
	ctx := context.Background()

	if err := s.Add(ctx, "1.2.3.4", "test", "admin", nil); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if !s.IsBlocked("1.2.3.4") {
		t.Fatal("expected IsBlocked=true for added IP")
	}
}

// TestBlocklist_CacheInvalidatesOnMutation (BLOCKLIST-CACHE-01): IsBlocked serves
// from an in-memory cache, but a mutation must take effect immediately (no wait
// for the TTL). Add → blocked at once; Remove → unblocked at once.
func TestBlocklist_CacheInvalidatesOnMutation(t *testing.T) {
	s := openTestBlocklist(t)
	ctx := context.Background()

	// Prime the cache with a miss (populates a non-nil snapshot).
	if s.IsBlocked("9.9.9.9") {
		t.Fatal("expected 9.9.9.9 not blocked initially")
	}
	// Add invalidates → immediately blocked without waiting for the TTL.
	if err := s.Add(ctx, "9.9.9.9", "test", "admin", nil); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if !s.IsBlocked("9.9.9.9") {
		t.Fatal("expected Add to take effect immediately (cache invalidated)")
	}
	// Remove invalidates → immediately unblocked.
	if err := s.Remove(ctx, "9.9.9.9"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if s.IsBlocked("9.9.9.9") {
		t.Fatal("expected Remove to take effect immediately (cache invalidated)")
	}
}

// TestBlocklist_CacheServesWithoutDBReadInSteadyState verifies IsBlocked answers
// from cache after the first load: closing the DB, a cached hit still resolves.
func TestBlocklist_CacheServesWithoutDBReadInSteadyState(t *testing.T) {
	s := openTestBlocklist(t)
	ctx := context.Background()
	if err := s.Add(ctx, "5.5.5.5", "test", "admin", nil); err != nil {
		t.Fatalf("Add: %v", err)
	}
	// Populate the cache.
	if !s.IsBlocked("5.5.5.5") {
		t.Fatal("expected blocked")
	}
	// Freeze the snapshot as fresh so no refresh is attempted, then close the DB.
	snap := s.cache.Load()
	if snap == nil {
		t.Fatal("expected a populated cache snapshot")
	}
	snap.loadedAt = time.Now() // ensure within TTL
	_ = s.db.Close()
	// Still resolves from cache without touching the (now-closed) DB.
	if !s.IsBlocked("5.5.5.5") {
		t.Fatal("expected cached hit to resolve without a DB read")
	}
}

func TestBlocklist_CIDRMatch(t *testing.T) {
	s := openTestBlocklist(t)
	ctx := context.Background()

	if err := s.Add(ctx, "10.0.0.0/8", "bad range", "system", nil); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if !s.IsBlocked("10.5.6.7") {
		t.Fatal("expected CIDR match for 10.5.6.7")
	}
	if s.IsBlocked("192.168.1.1") {
		t.Fatal("192.168.1.1 should NOT be blocked")
	}
}

func TestBlocklist_Remove(t *testing.T) {
	s := openTestBlocklist(t)
	ctx := context.Background()

	_ = s.Add(ctx, "9.9.9.9", "test", "admin", nil)
	if !s.IsBlocked("9.9.9.9") {
		t.Fatal("should be blocked before remove")
	}
	if err := s.Remove(ctx, "9.9.9.9"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if s.IsBlocked("9.9.9.9") {
		t.Fatal("should not be blocked after remove")
	}
}

func TestBlocklist_TTLExpiry(t *testing.T) {
	s := openTestBlocklist(t)
	ctx := context.Background()

	// Add with a TTL that is already in the past.
	ttl := -time.Second // already expired
	if err := s.Add(ctx, "7.7.7.7", "short-lived", "system", &ttl); err != nil {
		t.Fatalf("Add: %v", err)
	}
	// Should not be blocked (TTL expired).
	if s.IsBlocked("7.7.7.7") {
		t.Fatal("expired TTL entry should not block")
	}
}

func TestBlocklist_SweepExpired(t *testing.T) {
	s := openTestBlocklist(t)
	ctx := context.Background()

	ttl := -time.Second
	_ = s.Add(ctx, "8.8.8.8", "expired", "system", &ttl)
	if err := s.SweepExpired(ctx); err != nil {
		t.Fatalf("SweepExpired: %v", err)
	}
	entries, err := s.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, e := range entries {
		if e.CIDR == "8.8.8.8" {
			t.Fatal("expired entry should have been swept")
		}
	}
}
