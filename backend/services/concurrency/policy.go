// Package concurrency defines per-data-type conflict resolution policies for
// the Vulos cluster sync/merge layer.
//
// Different kinds of data require different conflict strategies when the same
// profile is active on multiple nodes simultaneously (see CONCURRENCY.md):
//
//   - LWW (last-write-wins): the default for settings/preferences/most state;
//     matches cr-sqlite field-level merge semantics.
//   - Counter: additive CRDT counter for counters and quotas; commutative and
//     never loses increments across concurrent merges.
//   - Sequence: collaborative CRDT hook (Automerge/Yjs-style) for co-edited
//     documents; the actual CRDT implementation is wired by COLLAB-01.
//   - Lease: exclusive-resource policy; only one owner at a time, backed by the
//     fencing lease from COORDINATION.md.
//
// A Registry maps data-type / table names to their policy.  The sync/merge
// layer calls Resolve to pick the right strategy before applying a changeset.
//
// CONC-03: per-data-type conflict policy registry.
package concurrency

// Policy identifies the conflict resolution strategy for a data type.
type Policy string

const (
	// PolicyLWW is last-write-wins per field (cr-sqlite default).
	// Applied to settings, preferences, and most app state.
	PolicyLWW Policy = "lww"

	// PolicyCounter is an additive CRDT counter merge.
	// Applied to counters and quotas; increments from all nodes are summed.
	PolicyCounter Policy = "counter"

	// PolicySequence is the collaborative CRDT hook for co-edited documents
	// (Automerge/Yjs-style).  The concrete merge implementation is provided
	// by COLLAB-01 via a SequenceHook; this policy signals that the hook
	// should be consulted rather than falling back to LWW.
	PolicySequence Policy = "sequence"

	// PolicyLease is the exclusive-resource policy.
	// Only one node may hold the resource at a time; backed by the fencing
	// lease from COORDINATION.md.
	PolicyLease Policy = "lease"
)

// SequenceHook is the interface that a collaborative-CRDT implementation
// (e.g. Yjs, Automerge) must satisfy to participate in sequence-policy
// merges.  The actual implementation is out of scope for CONC-03 and will
// be wired by COLLAB-01; this placeholder lets the sync layer reference the
// interface without depending on the concrete CRDT library.
type SequenceHook interface {
	// Merge applies a remote changeset to the local document state.
	// doc is the current local document (opaque bytes); remote is the
	// incoming changeset; it returns the merged document bytes or an error.
	Merge(doc []byte, remote []byte) ([]byte, error)
}

// Registry maps data-type / table names to their conflict resolution policy.
// An explicit registration always wins; any unregistered name resolves to the
// default policy (LWW).
type Registry struct {
	policies map[string]Policy
}

// NewRegistry returns an empty Registry whose default policy is LWW.
func NewRegistry() *Registry {
	return &Registry{
		policies: make(map[string]Policy),
	}
}

// Register associates a data-type or table name with a policy.
// Calling Register with the same name twice overwrites the previous entry.
func (r *Registry) Register(dataType string, p Policy) {
	r.policies[dataType] = p
}

// Resolve returns the policy for the given data-type / table name.
// If no explicit mapping exists, PolicyLWW is returned (safe default).
func (r *Registry) Resolve(dataType string) Policy {
	if p, ok := r.policies[dataType]; ok {
		return p
	}
	return PolicyLWW
}

// MergeCounter performs an additive CRDT counter merge.
//
// In a leaderless cluster, each node may have independently incremented (or
// decremented) a counter relative to a shared base value.  The correct merged
// value is:
//
//	merged = base + (a - base) + (b - base)
//	       = a + b - base
//
// where a and b are the two concurrent values and base is the last value both
// nodes agreed on (the common ancestor).  This is commutative and associative,
// so it can be applied in any order and produces the same result.
//
// Example: base=10, node A increments twice (a=12), node B increments once
// (b=11) → merged = 12 + 11 - 10 = 13.  Neither increment is lost.
func MergeCounter(a, b, base int64) int64 {
	return a + b - base
}

// DefaultRegistry is a pre-populated Registry with the canonical data-type
// mappings described in CONCURRENCY.md.  Callers may add further entries with
// Register without affecting the package-level variable.
var DefaultRegistry = func() *Registry {
	r := NewRegistry()

	// Settings and preferences — LWW (explicit, matches the default, but
	// registered so callers can introspect the policy without relying on the
	// fallback path).
	r.Register("settings", PolicyLWW)
	r.Register("preferences", PolicyLWW)

	// Counters / quotas — additive CRDT counter.
	r.Register("counters", PolicyCounter)
	r.Register("quotas", PolicyCounter)

	// Co-edited documents — sequence / collaborative CRDT hook.
	r.Register("documents", PolicySequence)

	// Exclusive resources — lease-backed policy.
	r.Register("leases", PolicyLease)
	r.Register("exclusive_resources", PolicyLease)

	return r
}()
