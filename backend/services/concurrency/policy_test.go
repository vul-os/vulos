package concurrency_test

import (
	"testing"

	"vulos/backend/services/concurrency"
)

// ---------------------------------------------------------------------------
// Registry resolution tests
// ---------------------------------------------------------------------------

func TestRegistry_Resolve_Registered(t *testing.T) {
	r := concurrency.NewRegistry()
	r.Register("my_counters", concurrency.PolicyCounter)
	r.Register("my_docs", concurrency.PolicySequence)
	r.Register("run_leases", concurrency.PolicyLease)

	cases := []struct {
		name     string
		dataType string
		want     concurrency.Policy
	}{
		{"counter table", "my_counters", concurrency.PolicyCounter},
		{"sequence table", "my_docs", concurrency.PolicySequence},
		{"lease table", "run_leases", concurrency.PolicyLease},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := r.Resolve(tc.dataType)
			if got != tc.want {
				t.Errorf("Resolve(%q) = %q, want %q", tc.dataType, got, tc.want)
			}
		})
	}
}

func TestRegistry_Resolve_DefaultLWW(t *testing.T) {
	r := concurrency.NewRegistry()
	// Nothing registered — every unknown name must fall back to LWW.
	for _, name := range []string{"", "anything", "settings", "unknown_table"} {
		got := r.Resolve(name)
		if got != concurrency.PolicyLWW {
			t.Errorf("Resolve(%q) = %q, want LWW (default)", name, got)
		}
	}
}

func TestRegistry_Register_Overwrite(t *testing.T) {
	r := concurrency.NewRegistry()
	r.Register("tbl", concurrency.PolicyLWW)
	r.Register("tbl", concurrency.PolicyCounter) // overwrite
	if got := r.Resolve("tbl"); got != concurrency.PolicyCounter {
		t.Errorf("Resolve after overwrite = %q, want %q", got, concurrency.PolicyCounter)
	}
}

func TestDefaultRegistry(t *testing.T) {
	dr := concurrency.DefaultRegistry
	cases := []struct {
		dataType string
		want     concurrency.Policy
	}{
		{"settings", concurrency.PolicyLWW},
		{"preferences", concurrency.PolicyLWW},
		{"counters", concurrency.PolicyCounter},
		{"quotas", concurrency.PolicyCounter},
		{"documents", concurrency.PolicySequence},
		{"leases", concurrency.PolicyLease},
		{"exclusive_resources", concurrency.PolicyLease},
		// unregistered name → LWW default
		{"unregistered_table", concurrency.PolicyLWW},
	}
	for _, tc := range cases {
		t.Run(tc.dataType, func(t *testing.T) {
			got := dr.Resolve(tc.dataType)
			if got != tc.want {
				t.Errorf("DefaultRegistry.Resolve(%q) = %q, want %q", tc.dataType, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// MergeCounter tests (additive CRDT counter)
// ---------------------------------------------------------------------------

func TestMergeCounter_Additive(t *testing.T) {
	cases := []struct {
		name string
		a    int64
		b    int64
		base int64
		want int64
	}{
		{
			name: "both nodes increment",
			a:    12, b: 11, base: 10,
			want: 13, // base=10; a+2, b+1 → merged = 10+2+1 = 13
		},
		{
			name: "no changes",
			a:    5, b: 5, base: 5,
			want: 5,
		},
		{
			name: "only a increments",
			a:    7, b: 3, base: 3,
			want: 7,
		},
		{
			name: "only b increments",
			a:    3, b: 8, base: 3,
			want: 8,
		},
		{
			name: "both decrement",
			a:    8, b: 9, base: 10,
			want: 7, // a-2, b-1 → merged = 10-2-1 = 7
		},
		{
			name: "zero base",
			a:    3, b: 5, base: 0,
			want: 8,
		},
		{
			name: "large values",
			a:    1_000_100, b: 1_000_050, base: 1_000_000,
			want: 1_000_150,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := concurrency.MergeCounter(tc.a, tc.b, tc.base)
			if got != tc.want {
				t.Errorf("MergeCounter(%d, %d, %d) = %d, want %d",
					tc.a, tc.b, tc.base, got, tc.want)
			}
			// Commutativity: MergeCounter(a,b,base) == MergeCounter(b,a,base)
			gotSwapped := concurrency.MergeCounter(tc.b, tc.a, tc.base)
			if got != gotSwapped {
				t.Errorf("MergeCounter not commutative: (%d,%d,%d)=%d but (%d,%d,%d)=%d",
					tc.a, tc.b, tc.base, got,
					tc.b, tc.a, tc.base, gotSwapped)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// SequenceHook interface presence test
// ---------------------------------------------------------------------------

// sequenceHookImpl is a minimal stub that satisfies the SequenceHook interface.
// Its existence confirms the interface is exported and has the expected shape;
// the actual Yjs/Automerge implementation is COLLAB-01's concern.
type sequenceHookImpl struct{}

func (s *sequenceHookImpl) Merge(doc []byte, remote []byte) ([]byte, error) {
	// Stub: return doc unchanged (no-op merge).
	return doc, nil
}

// TestSequenceHookInterface asserts that SequenceHook is an interface that can
// be satisfied by an external implementation.
func TestSequenceHookInterface(t *testing.T) {
	var _ concurrency.SequenceHook = (*sequenceHookImpl)(nil)
	// If this compiles the interface is correctly defined.

	hook := &sequenceHookImpl{}
	doc := []byte("hello")
	remote := []byte("world")
	merged, err := hook.Merge(doc, remote)
	if err != nil {
		t.Fatalf("SequenceHook.Merge returned unexpected error: %v", err)
	}
	if string(merged) != string(doc) {
		t.Errorf("stub Merge returned %q, want %q", merged, doc)
	}
}
