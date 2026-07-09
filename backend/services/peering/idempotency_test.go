package peering

import (
	"testing"
	"time"
)

// TestReceiverIdempotency_FirstFalseThenTrue is the relocated PEER-40 dedup
// semantic (CONSOLIDATION B-3): the first Seen(id) records and returns false,
// a subsequent Seen(id) returns true.
func TestReceiverIdempotency_FirstFalseThenTrue(t *testing.T) {
	c := newReceiverIdempotency(24 * time.Hour)
	if c.Seen("env-uuid-001") {
		t.Fatal("first Seen should be false")
	}
	if !c.Seen("env-uuid-001") {
		t.Fatal("second Seen should be true (duplicate)")
	}
}

// TestReceiverIdempotency_DistinctIDsIndependent: different IDs never collide.
func TestReceiverIdempotency_DistinctIDsIndependent(t *testing.T) {
	c := newReceiverIdempotency(24 * time.Hour)
	c.Seen("id-A")
	if c.Seen("id-B") {
		t.Fatal("distinct ID must not be reported as seen")
	}
}

// TestReceiverIdempotency_EmptyNeverDeduped: an empty ID is never deduped, so
// callers lacking an ID do not all collide on "".
func TestReceiverIdempotency_EmptyNeverDeduped(t *testing.T) {
	c := newReceiverIdempotency(24 * time.Hour)
	if c.Seen("") {
		t.Fatal("empty id first Seen must be false")
	}
	if c.Seen("") {
		t.Fatal("empty id must never be treated as a duplicate")
	}
}

// TestReceiverIdempotency_Eviction: entries older than the TTL are evicted so a
// re-seen id after expiry is treated as new (memory stays bounded).
func TestReceiverIdempotency_Eviction(t *testing.T) {
	c := newReceiverIdempotency(10 * time.Millisecond)
	c.Seen("id-X")
	time.Sleep(20 * time.Millisecond)
	if c.Seen("id-X") {
		t.Fatal("entry should have been evicted after TTL; expected first-seen (false)")
	}
}

// TestEnvelopeDedupeSingleton: the package singleton is wired with the 24h TTL.
func TestEnvelopeDedupeSingleton(t *testing.T) {
	if envelopeDedupe == nil {
		t.Fatal("envelopeDedupe singleton must be initialized")
	}
	if envelopeDedupe.ttl != 24*time.Hour {
		t.Fatalf("envelopeDedupe ttl = %v, want 24h", envelopeDedupe.ttl)
	}
}
