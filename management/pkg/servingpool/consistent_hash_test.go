package servingpool

import (
	"fmt"
	"testing"
)

func TestHashRingDeterministicLookup(t *testing.T) {
	r := NewHashRing(64)
	for _, id := range []string{"node-a", "node-b", "node-c"} {
		r.AddNode(id)
	}
	first := r.Lookup("tenant-1")
	for i := 0; i < 100; i++ {
		if got := r.Lookup("tenant-1"); got != first {
			t.Fatalf("non-deterministic lookup: got %q, want %q on iter %d", got, first, i)
		}
	}
}

func TestHashRingEmpty(t *testing.T) {
	r := NewHashRing(8)
	if got := r.Lookup("anything"); got != "" {
		t.Fatalf("empty ring should return empty string, got %q", got)
	}
}

func TestHashRingAddRemoveStable(t *testing.T) {
	r := NewHashRing(64)
	for _, id := range []string{"node-a", "node-b", "node-c", "node-d"} {
		r.AddNode(id)
	}
	// Record where 1000 tenants map.
	before := make(map[string]string)
	for i := 0; i < 1000; i++ {
		k := fmt.Sprintf("tenant-%04d", i)
		before[k] = r.Lookup(k)
	}
	// Remove one node — at most ~1/N of tenants should move.
	r.RemoveNode("node-c")
	moved := 0
	for k, oldOwner := range before {
		newOwner := r.Lookup(k)
		if newOwner == "" {
			t.Fatalf("ring went empty for %s", k)
		}
		if newOwner == "node-c" {
			t.Fatalf("removed node still owns %s", k)
		}
		if newOwner != oldOwner {
			moved++
		}
	}
	// With 64 vnodes × 4 nodes = ~256 vnodes; removing 1 node should move
	// roughly 25% of keys. Allow generous bounds (10-50%).
	if moved < 100 || moved > 500 {
		t.Fatalf("unexpected churn on remove: %d/1000 moved (want 100-500)", moved)
	}
}

func TestHashRingDistribution(t *testing.T) {
	r := NewHashRing(128)
	nodes := []string{"n1", "n2", "n3", "n4", "n5"}
	for _, id := range nodes {
		r.AddNode(id)
	}
	counts := map[string]int{}
	const N = 5000
	for i := 0; i < N; i++ {
		owner := r.Lookup(fmt.Sprintf("tenant-%d", i))
		counts[owner]++
	}
	// Each node should get >5% and <40% of keys (rough fairness).
	for _, n := range nodes {
		c := counts[n]
		if c < N/20 || c > N*2/5 {
			t.Fatalf("unfair distribution: node %s got %d/%d", n, c, N)
		}
	}
}

func TestHashRingLookupNDistinct(t *testing.T) {
	r := NewHashRing(32)
	for _, id := range []string{"a", "b", "c"} {
		r.AddNode(id)
	}
	got := r.LookupN("tenant-xyz", 3)
	if len(got) != 3 {
		t.Fatalf("LookupN(_, 3) = %d nodes, want 3", len(got))
	}
	seen := map[string]bool{}
	for _, id := range got {
		if seen[id] {
			t.Fatalf("LookupN returned duplicate %q", id)
		}
		seen[id] = true
	}
}

func TestHashRingIdempotentAdd(t *testing.T) {
	r := NewHashRing(8)
	r.AddNode("x")
	r.AddNode("x")
	r.AddNode("x")
	if r.Len() != 1 {
		t.Fatalf("idempotent add expected Len=1, got %d", r.Len())
	}
}
