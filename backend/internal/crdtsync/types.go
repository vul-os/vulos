package crdtsync

import (
	"fmt"
	"sync"
	"time"
)

// OpKind is the CRDT operation algebra. See doc.go.
type OpKind string

const (
	// OpSet writes a LWW-register: (domain, key, field) := value.
	OpSet OpKind = "set"
	// OpDel tombstones a LWW-register. It carries a stamp and competes with a
	// concurrent OpSet by stamp, not by arrival order.
	OpDel OpKind = "del"
	// OpInc applies a signed delta to a PN-counter. The op's Value holds the
	// signed decimal delta; the merged state keeps per-actor (pos, neg) totals.
	OpInc OpKind = "inc"
)

// Valid reports whether k is a known op kind. Unknown kinds are rejected at the
// merge boundary rather than silently ignored: an op this build cannot
// interpret must not be counted as applied, or the version vector would advance
// past data this box does not actually hold.
func (k OpKind) Valid() bool {
	return k == OpSet || k == OpDel || k == OpInc
}

// Stamp is the hybrid-logical-clock timestamp that decides LWW conflicts.
//
// Ordering is (Wall, Logical, Actor). Actor is the tie-break, and because every
// box compares the same triple, two boxes handed the same pair of concurrent
// writes pick the SAME winner regardless of which arrives first. That is the
// property appsync gets from its installed_by tie-break, generalised.
type Stamp struct {
	// Wall is the HLC wall component, unix milliseconds.
	Wall int64 `json:"wall"`
	// Logical is the HLC logical counter, disambiguating same-millisecond events.
	Logical uint32 `json:"logical"`
	// Actor is the instance ULID that produced the stamp.
	Actor string `json:"actor"`
}

// Compare returns -1, 0 or +1 ordering s before/equal/after other.
func (s Stamp) Compare(other Stamp) int {
	switch {
	case s.Wall < other.Wall:
		return -1
	case s.Wall > other.Wall:
		return 1
	}
	switch {
	case s.Logical < other.Logical:
		return -1
	case s.Logical > other.Logical:
		return 1
	}
	switch {
	case s.Actor < other.Actor:
		return -1
	case s.Actor > other.Actor:
		return 1
	}
	return 0
}

func (s Stamp) String() string {
	return fmt.Sprintf("%d.%d@%s", s.Wall, s.Logical, s.Actor)
}

// Op is one CRDT operation. It is the wire unit AND the log unit.
//
// (Actor, Seq) identifies the op globally: Actor is the ORIGINATING instance
// (never the relaying one) and Seq is that instance's monotonic counter. A box
// that receives an op from a peer stores it under the origin's (Actor, Seq), so
// it can relay the op onward to a third box that never talks to the origin.
type Op struct {
	Domain string `json:"domain"`
	Actor  string `json:"actor"`
	Seq    uint64 `json:"seq"`
	Key    string `json:"key"`
	Field  string `json:"field"`
	Kind   OpKind `json:"kind"`
	Value  []byte `json:"value,omitempty"`
	Stamp  Stamp  `json:"stamp"`
}

// VersionVector maps actor → highest seq observed from that actor.
//
// It decides only what to TRANSMIT, never who wins a merge (see doc.go).
type VersionVector map[string]uint64

// Clone returns an independent copy.
func (vv VersionVector) Clone() VersionVector {
	out := make(VersionVector, len(vv))
	for k, v := range vv {
		out[k] = v
	}
	return out
}

// Observe raises the entry for actor to seq when seq is higher.
func (vv VersionVector) Observe(actor string, seq uint64) {
	if seq > vv[actor] {
		vv[actor] = seq
	}
}

// Covers reports whether vv has seen at least seq from actor.
func (vv VersionVector) Covers(actor string, seq uint64) bool {
	return vv[actor] >= seq
}

// Delta is the reconciliation payload: the ops a peer is missing, plus the
// sender's own version vector so the exchange can be symmetric in one round
// trip (the receiver immediately knows what to send back).
//
// When the requester has fallen behind the sender's compaction floor, the
// sender cannot express the difference as ops any more. It then sets
// SnapshotRequired and attaches a full Snapshot instead — this is the bounded
// bootstrap path.
type Delta struct {
	Domain string `json:"domain"`
	// SenderVV is the sender's version vector AFTER the ops in this delta.
	SenderVV VersionVector `json:"sender_vv"`
	// Ops are the operations the requester had not seen, oldest first.
	Ops []Op `json:"ops,omitempty"`
	// SnapshotRequired is true when the requester is behind the sender's
	// compaction floor and Snapshot carries the bounded bootstrap payload.
	SnapshotRequired bool `json:"snapshot_required,omitempty"`
	// Snapshot is set iff SnapshotRequired.
	Snapshot *Snapshot `json:"snapshot,omitempty"`
	// Truncated is true when a per-response op limit cut the delta short. The
	// requester's next round picks up where this one stopped; convergence is
	// unaffected because merge is idempotent and order-independent.
	Truncated bool `json:"truncated,omitempty"`
}

// RegisterState is one materialised LWW-register row in a Snapshot.
type RegisterState struct {
	Key     string `json:"key"`
	Field   string `json:"field"`
	Value   []byte `json:"value,omitempty"`
	Deleted bool   `json:"deleted,omitempty"`
	Stamp   Stamp  `json:"stamp"`
}

// CounterState is one materialised PN-counter contribution in a Snapshot.
type CounterState struct {
	Key   string `json:"key"`
	Field string `json:"field"`
	Actor string `json:"actor"`
	Pos   int64  `json:"pos"`
	Neg   int64  `json:"neg"`
}

// Snapshot is the first-class bounded bootstrap payload: the whole materialised
// state of one domain plus the version vector it covers.
//
// Applying a snapshot is a MERGE, not a replace — every register goes through
// the same max-by-stamp rule and every counter through the same per-actor max —
// so a node that has local writes the snapshot's author never saw does not lose
// them. That is what makes it safe to hand a snapshot to an already-running
// node, not just a blank one.
type Snapshot struct {
	Domain     string          `json:"domain"`
	VV         VersionVector   `json:"vv"`
	Registers  []RegisterState `json:"registers,omitempty"`
	Counters   []CounterState  `json:"counters,omitempty"`
	TakenAtMS  int64           `json:"taken_at_ms"`
	SchemaVers int             `json:"schema_version"`
}

// SnapshotSchemaVersion is the on-wire schema version of Snapshot. A snapshot
// from a future schema is refused rather than half-applied.
const SnapshotSchemaVersion = 1

// clock is a hybrid logical clock. It guarantees that every stamp this actor
// emits sorts strictly after every stamp it has previously emitted OR observed,
// even when the host wall clock is behind or jumps backwards.
type clock struct {
	mu      sync.Mutex
	wall    int64
	logical uint32
	now     func() time.Time // injectable for tests
}

func newClock() *clock { return &clock{now: time.Now} }

// tick returns the next stamp for a local event.
func (c *clock) tick(actor string) Stamp {
	c.mu.Lock()
	defer c.mu.Unlock()
	nowMS := c.now().UTC().UnixMilli()
	if nowMS > c.wall {
		c.wall = nowMS
		c.logical = 0
	} else {
		c.logical++
	}
	return Stamp{Wall: c.wall, Logical: c.logical, Actor: actor}
}

// observe advances the clock past a remote stamp so any stamp this actor emits
// afterwards sorts after it. This is what stops a box with a slow clock from
// emitting writes that lose to everything it just received.
func (c *clock) observe(s Stamp) {
	c.mu.Lock()
	defer c.mu.Unlock()
	nowMS := c.now().UTC().UnixMilli()
	switch {
	case s.Wall > c.wall && s.Wall >= nowMS:
		c.wall = s.Wall
		c.logical = s.Logical
	case s.Wall == c.wall && s.Logical > c.logical:
		c.logical = s.Logical
	case nowMS > c.wall:
		c.wall = nowMS
		c.logical = 0
	}
}
