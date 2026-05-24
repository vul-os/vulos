package crdt

import (
	"fmt"
	"sort"
)

// ReplicaID is a stable identifier for a replica (peer) within a
// session. Lexicographic ordering on ReplicaID is used as a tiebreaker
// for concurrent ops with equal Lamport stamps, giving a total order.
type ReplicaID string

// OpID is a per-replica monotonically increasing op identifier paired
// with its origin replica. (ReplicaID, Counter) is globally unique.
type OpID struct {
	Replica ReplicaID `json:"r"`
	Counter uint64    `json:"c"`
}

// Less defines a total order on OpIDs: first by Counter (Lamport-like),
// then by Replica as a deterministic tiebreaker. This ordering is the
// foundation of every merge function in this package.
func (a OpID) Less(b OpID) bool {
	if a.Counter != b.Counter {
		return a.Counter < b.Counter
	}
	return a.Replica < b.Replica
}

// Equal reports whether two OpIDs refer to the same op.
func (a OpID) Equal(b OpID) bool {
	return a.Counter == b.Counter && a.Replica == b.Replica
}

// String returns "replica@counter" - useful for debug + map keys when
// you need a flat string.
func (a OpID) String() string {
	return fmt.Sprintf("%s@%d", a.Replica, a.Counter)
}

// VectorClock maps each known replica to the highest counter we have
// observed from it. Used for delivery acks and to detect missing ops.
type VectorClock map[ReplicaID]uint64

// Clone returns a deep copy of the clock.
func (vc VectorClock) Clone() VectorClock {
	out := make(VectorClock, len(vc))
	for k, v := range vc {
		out[k] = v
	}
	return out
}

// Observe records that we have seen op id from its replica.
func (vc VectorClock) Observe(id OpID) {
	if vc[id.Replica] < id.Counter {
		vc[id.Replica] = id.Counter
	}
}

// Has reports whether op id has been observed.
func (vc VectorClock) Has(id OpID) bool {
	return vc[id.Replica] >= id.Counter
}

// Replicas returns the sorted list of replicas known to the clock,
// for deterministic iteration in tests/snapshots.
func (vc VectorClock) Replicas() []ReplicaID {
	out := make([]ReplicaID, 0, len(vc))
	for r := range vc {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// LamportClock is a simple monotonic counter used by a replica to
// stamp outgoing ops. On Observe(remote), it is advanced past remote
// so subsequent local ops are causally greater.
type LamportClock struct {
	Replica ReplicaID
	C       uint64
}

// NewLamportClock returns a fresh clock for the given replica.
func NewLamportClock(r ReplicaID) *LamportClock {
	return &LamportClock{Replica: r}
}

// Tick increments the clock and returns a fresh OpID.
func (lc *LamportClock) Tick() OpID {
	lc.C++
	return OpID{Replica: lc.Replica, Counter: lc.C}
}

// Observe advances the clock past `remote` (Lamport rule).
func (lc *LamportClock) Observe(remote uint64) {
	if remote > lc.C {
		lc.C = remote
	}
}
