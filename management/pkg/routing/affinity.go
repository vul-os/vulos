// affinity.go — in-memory geo-routing session affinity store (DNS-05).
//
// When a client resolves a ULID, we return the same PoP consistently for the
// (account, ULID) pair while the preferred PoP is healthy. On PoP failure we
// fall back to the next-nearest healthy PoP by Haversine distance. Entries are
// evicted after 5 minutes of inactivity.
//
// The clock is injectable (AffinityStore.Now) so tests can fast-forward time
// without sleeping.
package routing

import (
	"context"
	"sync"
	"time"
)

const defaultAffinityTTL = 5 * time.Minute

// affinityKey is the composite key for an affinity entry.
type affinityKey struct {
	accountID string
	ulid      string
}

// affinityEntry records the preferred PoP for an (account, ULID) pair.
type affinityEntry struct {
	popID    string
	lastUsed time.Time
}

// AffinityStore is a concurrency-safe in-memory affinity cache.
// Create one with NewAffinityStore; the zero value is not usable.
type AffinityStore struct {
	mu  sync.Mutex
	m   map[affinityKey]affinityEntry
	ttl time.Duration
	// Now is the clock function used to obtain the current time. Override in
	// tests to control time without sleeping. Defaults to time.Now.
	Now func() time.Time
}

// NewAffinityStore returns a ready-to-use AffinityStore with the standard 5 m TTL.
func NewAffinityStore() *AffinityStore {
	return &AffinityStore{
		m:   make(map[affinityKey]affinityEntry),
		ttl: defaultAffinityTTL,
		Now: time.Now,
	}
}

// get returns the stored affinity entry and a found flag. It does NOT evict.
func (a *AffinityStore) get(key affinityKey) (affinityEntry, bool) {
	e, ok := a.m[key]
	return e, ok
}

// set writes or updates an affinity entry with the current timestamp.
func (a *AffinityStore) set(key affinityKey, popID string) {
	a.m[key] = affinityEntry{popID: popID, lastUsed: a.Now()}
}

// evict removes all entries whose lastUsed is older than the TTL.
func (a *AffinityStore) evict() {
	cutoff := a.Now().Add(-a.ttl)
	for k, e := range a.m {
		if e.lastUsed.Before(cutoff) {
			delete(a.m, k)
		}
	}
}

// popHealthy returns true when the PoP exists in the provided slice and is
// both healthy (Healthy==true) and recently seen (LastHealth within 90 s).
// A zero LastHealth is treated as "always fresh" for backward compatibility
// with stores that have not yet recorded a heartbeat timestamp.
func popHealthy(pops []PoP, popID string, now time.Time) bool {
	const healthWindow = 90 * time.Second
	for _, p := range pops {
		if p.ID == popID {
			if !p.Healthy {
				return false
			}
			// Zero LastHealth means no timestamp recorded yet; trust Healthy flag.
			if p.LastHealth.IsZero() {
				return true
			}
			return now.Sub(p.LastHealth) <= healthWindow
		}
	}
	return false
}

// ResolveWithAffinity selects the best PoP for the (accountID, ulid) pair
// using sticky-until-failure semantics:
//
//  1. If an affinity entry exists AND the stored PoP is still healthy (healthy=true
//     AND last_seen_at ≤ 90 s), return it unchanged (sticky).
//  2. Otherwise, pick the nearest healthy PoP by Haversine distance from
//     (clientLat, clientLon), update the affinity entry, and return it.
//
// The pops slice must contain ALL PoPs (both healthy and unhealthy) so that
// affinity staleness can be detected. Returns ErrNoHealthyPoP when no healthy
// PoP is available.
func (a *AffinityStore) ResolveWithAffinity(
	ctx context.Context,
	st Store,
	accountID, ulid string,
	clientLat, clientLon float64,
) (PoP, error) {
	// Collect all PoPs; we need the full list to check the stored PoP's health.
	type lister interface {
		ListPoPs(ctx context.Context) ([]PoP, error)
	}
	var allPops []PoP
	if ls, ok := st.(lister); ok {
		var err error
		allPops, err = ls.ListPoPs(ctx)
		if err != nil {
			return PoP{}, err
		}
	} else {
		// Fallback: only healthy PoPs available; cannot detect staleness by
		// LastHealth, so treat any stored entry whose PoP is absent as invalid.
		var err error
		allPops, err = st.HealthyPoPs(ctx)
		if err != nil {
			return PoP{}, err
		}
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	// Evict stale entries on every call (amortised O(n) across the whole map).
	a.evict()

	key := affinityKey{accountID: accountID, ulid: ulid}
	now := a.Now()

	if e, ok := a.get(key); ok {
		if popHealthy(allPops, e.popID, now) {
			// Preferred PoP is still healthy — touch the TTL and return sticky.
			a.set(key, e.popID)
			for _, p := range allPops {
				if p.ID == e.popID {
					return p, nil
				}
			}
		}
	}

	// No valid affinity entry or preferred PoP is unhealthy — pick nearest.
	// Build a healthy-eligible slice. A PoP is eligible when Healthy==true AND
	// (LastHealth is zero OR within the 90 s window). Zero LastHealth means
	// no heartbeat has been recorded yet and we trust the Healthy flag directly,
	// which preserves backward compatibility with test helpers that create PoPs
	// without an explicit timestamp.
	const healthWindow = 90 * time.Second
	healthy := make([]PoP, 0, len(allPops))
	for _, p := range allPops {
		if !p.Healthy {
			continue
		}
		if !p.LastHealth.IsZero() && now.Sub(p.LastHealth) > healthWindow {
			continue // stale: last heartbeat too old
		}
		healthy = append(healthy, p)
	}
	best, err := NearestPoP(healthy, clientLat, clientLon)
	if err != nil {
		return PoP{}, err // ErrNoHealthyPoP
	}
	a.set(key, best.ID)
	return best, nil
}
