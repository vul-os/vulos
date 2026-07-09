// idempotency.go — receiver-side envelope idempotency (CONSOLIDATION B-3).
//
// This is the UUIDv7 dedup that used to live inside PEER-40's endpoints.go
// (epDedupeCache / epSeen). PEER-40's transport (the in-memory endpoint race)
// has been deleted, but its dedup was a genuine *receiver-side* delivery
// semantic — it is independent of which transport carries the envelope — so it
// is RELOCATED here rather than removed.
//
// It exists so a receiver can drop a re-delivered envelope exactly once when the
// sender retries or races (the live durable retry path, outbox.go, retries a
// down peer and can therefore deliver the same envelope more than once). Note
// that several inbound handlers ALSO achieve idempotency at their storage layer
// (e.g. inbox.StoreMessage is a no-op when a file with the same message ID
// already exists), so this cache is a general, transport-agnostic guard that
// handlers may consult; it does not replace that per-handler idempotency.
package peering

import (
	"sync"
	"time"
)

// receiverIdempotency tracks recently-seen envelope IDs so a re-delivered
// envelope can be dropped exactly once within a TTL window. It is the relocated
// former epDedupeCache; the semantics (first Seen → false, subsequent Seen →
// true, entries evicted after ttl) are preserved byte-for-byte.
type receiverIdempotency struct {
	mu   sync.Mutex
	seen map[string]time.Time
	ttl  time.Duration
}

// newReceiverIdempotency returns an idempotency cache with the given TTL.
func newReceiverIdempotency(ttl time.Duration) *receiverIdempotency {
	return &receiverIdempotency{
		seen: make(map[string]time.Time),
		ttl:  ttl,
	}
}

// envelopeDedupe is the package-level receiver idempotency cache. The 24h TTL
// matches PEER-40's former epDedupe window.
var envelopeDedupe = newReceiverIdempotency(24 * time.Hour)

// Seen returns true if id was already processed within the TTL window, false on
// the first call for that id (recording it). It also evicts stale entries so
// memory stays bounded. An empty id is never deduped (returns false) so callers
// that lack an ID do not collide on "".
func (c *receiverIdempotency) Seen(id string) bool {
	if id == "" {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()

	// Periodic eviction: remove entries older than ttl.
	for k, ts := range c.seen {
		if now.Sub(ts) > c.ttl {
			delete(c.seen, k)
		}
	}

	if _, exists := c.seen[id]; exists {
		return true
	}
	c.seen[id] = now
	return false
}
