package servingpool

import (
	"crypto/sha1"
	"encoding/binary"
	"sort"
	"strconv"
	"sync"
)

// HashRing is a thread-safe consistent-hash ring of node IDs with virtual
// nodes ("vnodes") for better key distribution. The ring is rebuilt on each
// AddNode / RemoveNode; Lookup is a hot path so we keep it O(log N) with a
// pre-sorted slice + binary search.
//
// We define our own ring rather than importing a third-party library, and we
// deliberately do NOT import any vulos-mail / vulos-relay primitives. The
// implementation is small, deterministic, and well-suited to the cloud CP.
type HashRing struct {
	mu       sync.RWMutex
	replicas int
	keys     []uint32          // sorted hashes of vnodes
	hashMap  map[uint32]string // hash → node_id
	nodes    map[string]bool   // node_id → present
}

// NewHashRing constructs a ring with the given replication factor (virtual
// nodes per real node). Sensible value: 64–256.
func NewHashRing(replicas int) *HashRing {
	if replicas <= 0 {
		replicas = DefaultVirtualNodeWeight
	}
	return &HashRing{
		replicas: replicas,
		hashMap:  make(map[uint32]string),
		nodes:    make(map[string]bool),
	}
}

// AddNode inserts node id into the ring (idempotent).
func (h *HashRing) AddNode(id string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.nodes[id] {
		return
	}
	h.nodes[id] = true
	for i := 0; i < h.replicas; i++ {
		k := hash32(strconv.Itoa(i) + "#" + id)
		h.keys = append(h.keys, k)
		h.hashMap[k] = id
	}
	sort.Slice(h.keys, func(i, j int) bool { return h.keys[i] < h.keys[j] })
}

// RemoveNode removes a node from the ring (idempotent).
func (h *HashRing) RemoveNode(id string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.nodes[id] {
		return
	}
	delete(h.nodes, id)
	filtered := h.keys[:0]
	for _, k := range h.keys {
		if h.hashMap[k] == id {
			delete(h.hashMap, k)
			continue
		}
		filtered = append(filtered, k)
	}
	h.keys = filtered
}

// Has reports whether a node id is present on the ring.
func (h *HashRing) Has(id string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.nodes[id]
}

// Len returns the number of distinct nodes on the ring.
func (h *HashRing) Len() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.nodes)
}

// Lookup returns the node id responsible for key. Returns "" if the ring is
// empty.
func (h *HashRing) Lookup(key string) string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if len(h.keys) == 0 {
		return ""
	}
	target := hash32(key)
	idx := sort.Search(len(h.keys), func(i int) bool {
		return h.keys[i] >= target
	})
	if idx == len(h.keys) {
		idx = 0 // wrap around
	}
	return h.hashMap[h.keys[idx]]
}

// LookupN returns up to n distinct nodes responsible for key, in ring order.
// Useful for failover: try preferred, then the next.
func (h *HashRing) LookupN(key string, n int) []string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if n <= 0 || len(h.keys) == 0 {
		return nil
	}
	target := hash32(key)
	start := sort.Search(len(h.keys), func(i int) bool {
		return h.keys[i] >= target
	})
	if start == len(h.keys) {
		start = 0
	}
	seen := make(map[string]bool, n)
	out := make([]string, 0, n)
	for i := 0; i < len(h.keys) && len(out) < n; i++ {
		idx := (start + i) % len(h.keys)
		id := h.hashMap[h.keys[idx]]
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

// hash32 returns a 32-bit hash of key. We use the first 4 bytes of SHA-1 —
// deterministic, fast, and good distribution for short string keys.
func hash32(key string) uint32 {
	sum := sha1.Sum([]byte(key))
	return binary.BigEndian.Uint32(sum[:4])
}
