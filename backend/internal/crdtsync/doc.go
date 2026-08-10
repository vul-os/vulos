// Package crdtsync is the general, pure-Go, leaderless CRDT sync engine for
// Vulos — the mechanism internal/multiinstance/appsync.go proves for one narrow
// case (the app registry), lifted into something any domain can use.
//
// No CGO, no cr-sqlite. Storage is modernc.org/sqlite exactly like every other
// store in this codebase (docs/decisions.md D23, reaffirmed D94 item J). The
// merge is ordinary Go over ordinary SQL rows.
//
// # Why this shape
//
// The requirement is: every instance holds a full mergeable copy, concurrent
// writes converge with no leader, and a joining node does not replay unbounded
// history. That rules out a pure op-log (unbounded) and a pure state-blob
// (loses concurrent field writes). What satisfies both is a delta-state CRDT
// with a *bounded* op log in front of it:
//
//   - The materialised STATE (crdt_reg / crdt_ctr) is the authority for reads
//     and the thing a snapshot serialises. It is bounded by the number of live
//     key/field pairs, never by history length.
//   - The OP LOG (crdt_op) exists only as the reconciliation index: "give me
//     everything I have not seen". It is pruned by Compact, and a peer that has
//     fallen behind the pruned floor is served a SNAPSHOT instead of a delta.
//
// So history is bounded by construction and bootstrap cost is bounded by state
// size, not cluster age.
//
// # The op algebra
//
// Three op kinds, each a well-known CRDT, all merged by an operation that is
// commutative, associative and idempotent:
//
//	OpSet    LWW-register write   — (domain, key, field) := value
//	OpDel    LWW-register delete  — a tombstone that participates in the same
//	                                LWW ordering, so a delete and a concurrent
//	                                write resolve by stamp rather than by
//	                                arrival order
//	OpInc    PN-counter delta     — per-actor (pos, neg) pair; the value is
//	                                Σpos − Σneg, and merge takes the per-actor
//	                                MAX of each, so a replayed or duplicated
//	                                increment is absorbed rather than counted
//	                                twice
//
// These are exactly the policies services/concurrency/policy.go declared and
// nothing implemented: PolicyLWW → OpSet/OpDel, PolicyCounter → OpInc.
//
// # Stamps: why (wall, logical, actor) and not a bare timestamp
//
// appsync orders by wall clock with the writer node id as tie-break. That is
// sound but it inherits every clock-skew pathology, and two writes in the same
// millisecond on one box are indistinguishable. crdtsync keeps the same
// deterministic tie-break and adds a hybrid logical clock: the wall component
// never goes backwards for a given actor, and it jumps forward on receipt of a
// remote stamp, so a box whose clock is behind still produces stamps that sort
// after everything it has already seen. The full ordering key is
//
//	(Wall, Logical, Actor, liveness, value)
//
// which is a TOTAL order over register states. Merge is then literally "take
// the max", which is trivially commutative, associative and idempotent — the
// property the convergence tests exercise. The two trailing components only
// ever decide an exact-stamp collision (which a well-behaved actor cannot
// produce, but a byzantine one can): live beats deleted, matching appsync's
// "install wins over uninstall on a tie", and larger value bytes break the last
// tie so two boxes handed the same pair of forged ops still agree.
//
// # Version vectors
//
// Each actor stamps its own ops with a monotonic Seq. A VersionVector is
// actor → highest seq observed. It decides ONLY what to transmit, never who
// wins: the merge outcome is a pure function of the stamps, so a delta computed
// from a stale, wrong, or empty version vector costs bandwidth and nothing else.
// That separation is deliberate — it means a peer cannot influence the merge by
// lying about what it has seen.
//
// # Transport seam
//
// The engine is transport-agnostic. Delta / Merge / Snapshot / ApplySnapshot
// are ordinary Go calls over JSON-serialisable values; handlers.go exposes them
// over HTTP and syncer.go drives a pull-then-push round against whatever
// PeerSource it is given. internal/fabric's mDNS Discoverer is one PeerSource
// (adapted in cmd/server/crdtsync_wiring.go); a WAN/relay rendezvous
// discoverer is another and slots in at the same seam with no engine change —
// what it must still bring is a peer identity check stronger than the shared
// LAN secret. See roadmap/SYNC.md.
//
// # Which data syncs
//
// The engine will faithfully replicate anything it is handed, so what it is
// handed is the security boundary. That decision is an ALLOW-LIST in policy.go,
// recorded per domain with its reasoning (refusals included), and it is
// ENFORCED rather than documented: Open requires a non-empty allow-list and
// every path that can introduce state checks it. internal/sqlcrdt binds each
// approved domain to the table and columns that hold it.
package crdtsync
