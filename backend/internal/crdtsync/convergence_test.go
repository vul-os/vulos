package crdtsync

import (
	"fmt"
	"math/rand"
	"os"
	"sort"
	"strconv"
	"testing"
)

// ── the algebraic laws ───────────────────────────────────────────────────────
//
// A merge that is commutative, associative and idempotent converges under ANY
// delivery order, any duplication and any regrouping. Each law gets its own
// test against CONFLICTING ops — a law "proved" on ops that touch different
// keys proves nothing, because disjoint updates commute trivially.

// mkop builds an op with an explicit stamp. Tests that need exact orderings
// construct ops directly rather than racing wall clocks.
func mkop(actor string, seq uint64, key, field string, kind OpKind, val string, wall int64, logical uint32) Op {
	var v []byte
	if kind != OpDel {
		v = []byte(val)
	}
	return Op{
		Domain: dom, Actor: actor, Seq: seq, Key: key, Field: field, Kind: kind, Value: v,
		Stamp: Stamp{Wall: wall, Logical: logical, Actor: actor},
	}
}

func TestMergeIsCommutative(t *testing.T) {
	// Two CONFLICTING writes to the same register, delivered in both orders.
	d1 := &Delta{Domain: dom, Ops: []Op{mkop("B", 1, "k", "f", OpSet, "from-b", 100, 0)}}
	d2 := &Delta{Domain: dom, Ops: []Op{mkop("C", 1, "k", "f", OpSet, "from-c", 200, 0)}}

	x := newTestStore(t, "X")
	if _, err := x.Merge(wire(t, d1)); err != nil {
		t.Fatal(err)
	}
	if _, err := x.Merge(wire(t, d2)); err != nil {
		t.Fatal(err)
	}

	y := newTestStore(t, "Y")
	if _, err := y.Merge(wire(t, d2)); err != nil {
		t.Fatal(err)
	}
	if _, err := y.Merge(wire(t, d1)); err != nil {
		t.Fatal(err)
	}

	dx, dy := digest(t, x, dom), digest(t, y, dom)
	if dx != dy {
		t.Fatalf("merge is NOT commutative:\n--- d1 then d2 ---\n%s\n--- d2 then d1 ---\n%s", dx, dy)
	}
	// And the winner is the higher stamp, not the last arrival.
	if v, _ := mustGet(t, x, dom, "k", "f"); v != "from-c" {
		t.Fatalf("winner = %q, want from-c (the higher stamp)", v)
	}
}

func TestMergeIsAssociative(t *testing.T) {
	// Associativity is a property of the STATE JOIN, so the grouping is expressed
	// by merging a partially-merged state (carried as a snapshot, which is how
	// this engine ships state) rather than by re-ordering a sequence.
	d1 := &Delta{Domain: dom, Ops: []Op{mkop("B", 1, "k", "f", OpSet, "b1", 100, 0)}}
	d2 := &Delta{Domain: dom, Ops: []Op{mkop("C", 1, "k", "f", OpSet, "c1", 300, 0)}}
	d3 := &Delta{Domain: dom, Ops: []Op{mkop("D", 1, "k", "f", OpSet, "d1", 200, 0)}}

	mergeAll := func(name string, deltas ...*Delta) *Store {
		s := newTestStore(t, name)
		for _, d := range deltas {
			if _, err := s.Merge(wire(t, d)); err != nil {
				t.Fatal(err)
			}
		}
		return s
	}

	// (d1 ∘ d2) ∘ d3
	left := mergeAll("L1", d1, d2)
	snapL, err := left.Snapshot(dom)
	if err != nil {
		t.Fatal(err)
	}
	z1 := mergeAll("Z1", d3)
	if err := z1.ApplySnapshot(snapL); err != nil {
		t.Fatal(err)
	}

	// d1 ∘ (d2 ∘ d3)
	right := mergeAll("R1", d2, d3)
	snapR, err := right.Snapshot(dom)
	if err != nil {
		t.Fatal(err)
	}
	z2 := mergeAll("Z2", d1)
	if err := z2.ApplySnapshot(snapR); err != nil {
		t.Fatal(err)
	}

	if a, b := digest(t, z1, dom), digest(t, z2, dom); a != b {
		t.Fatalf("merge is NOT associative:\n--- (d1∘d2)∘d3 ---\n%s\n--- d1∘(d2∘d3) ---\n%s", a, b)
	}
	if v, _ := mustGet(t, z1, dom, "k", "f"); v != "c1" {
		t.Fatalf("winner = %q, want c1 (highest stamp)", v)
	}
}

func TestMergeIsIdempotent(t *testing.T) {
	s := newTestStore(t, "X")
	d := &Delta{Domain: dom, Ops: []Op{
		mkop("B", 1, "k", "f", OpSet, "b1", 100, 0),
		mkop("B", 2, "k", "g", OpSet, "b2", 110, 0),
		mkop("C", 1, "k", "f", OpSet, "c1", 105, 0),
	}}
	n1, err := s.Merge(wire(t, d))
	if err != nil {
		t.Fatal(err)
	}
	if n1 != 3 {
		t.Fatalf("first merge applied %d, want 3", n1)
	}
	first := digest(t, s, dom)

	for i := 0; i < 5; i++ {
		n, err := s.Merge(wire(t, d))
		if err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Fatalf("replay %d applied %d ops, want 0 (duplicates must be absorbed)", i, n)
		}
	}
	if got := digest(t, s, dom); got != first {
		t.Fatalf("merge is NOT idempotent:\n--- after 1 ---\n%s\n--- after 6 ---\n%s", first, got)
	}
	if n, _ := s.LogSize(dom); n != 3 {
		t.Fatalf("log holds %d ops after replays, want 3", n)
	}
}

func TestCounterMergeIsIdempotent(t *testing.T) {
	// The reason OpInc carries CUMULATIVE totals rather than a delta: a replayed
	// increment must be absorbed, not counted twice.
	s := newTestStore(t, "X")
	d := &Delta{Domain: dom, Ops: []Op{
		{Domain: dom, Actor: "B", Seq: 1, Key: "c", Field: "hits", Kind: OpInc,
			Value: encodeCounter(10, 4), Stamp: Stamp{Wall: 100, Actor: "B"}},
	}}
	for i := 0; i < 4; i++ {
		if _, err := s.Merge(wire(t, d)); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.Counter(dom, "c", "hits")
	if err != nil {
		t.Fatal(err)
	}
	if got != 6 {
		t.Fatalf("counter = %d after 4 deliveries of the same op, want 6", got)
	}
}

func TestCounterConvergesAcrossActors(t *testing.T) {
	a := newTestStore(t, "A")
	b := newTestStore(t, "B")
	c := newTestStore(t, "C")
	if err := a.Add(dom, "c", "hits", 5); err != nil {
		t.Fatal(err)
	}
	if err := b.Add(dom, "c", "hits", 3); err != nil {
		t.Fatal(err)
	}
	if err := c.Add(dom, "c", "hits", -2); err != nil {
		t.Fatal(err)
	}
	syncAll(t, dom, a, b, c)
	assertConverged(t, dom, a, b, c)
	for _, s := range []*Store{a, b, c} {
		got, err := s.Counter(dom, "c", "hits")
		if err != nil {
			t.Fatal(err)
		}
		if got != 6 {
			t.Fatalf("%s counter = %d, want 6", s.actor, got)
		}
	}
}

// ── per-COLUMN granularity ───────────────────────────────────────────────────

func TestPerColumnWritesBothSurvive(t *testing.T) {
	// The whole point of column granularity: two nodes editing DIFFERENT fields
	// of the SAME record must both survive. A per-row LWW would lose one.
	a := newTestStore(t, "A")
	b := newTestStore(t, "B")

	if err := a.Set(dom, "user:1", "name", []byte("ada")); err != nil {
		t.Fatal(err)
	}
	if err := b.Set(dom, "user:1", "email", []byte("ada@example.org")); err != nil {
		t.Fatal(err)
	}
	syncAll(t, dom, a, b)
	assertConverged(t, dom, a, b)

	for _, s := range []*Store{a, b} {
		if v, ok := mustGet(t, s, dom, "user:1", "name"); !ok || v != "ada" {
			t.Errorf("%s lost the concurrent name write: %q ok=%v", s.actor, v, ok)
		}
		if v, ok := mustGet(t, s, dom, "user:1", "email"); !ok || v != "ada@example.org" {
			t.Errorf("%s lost the concurrent email write: %q ok=%v", s.actor, v, ok)
		}
	}
}

func TestSameColumnResolvesByStampNotArrivalOrder(t *testing.T) {
	// Deliver the LOWER-stamped write LAST on one node and FIRST on the other.
	// If arrival order decided, the two nodes would disagree.
	early := &Delta{Domain: dom, Ops: []Op{mkop("B", 1, "k", "f", OpSet, "early", 100, 0)}}
	late := &Delta{Domain: dom, Ops: []Op{mkop("C", 1, "k", "f", OpSet, "late", 900, 0)}}

	x := newTestStore(t, "X")
	if _, err := x.Merge(wire(t, late)); err != nil {
		t.Fatal(err)
	}
	if _, err := x.Merge(wire(t, early)); err != nil {
		t.Fatal(err)
	}

	y := newTestStore(t, "Y")
	if _, err := y.Merge(wire(t, early)); err != nil {
		t.Fatal(err)
	}
	if _, err := y.Merge(wire(t, late)); err != nil {
		t.Fatal(err)
	}

	for _, s := range []*Store{x, y} {
		if v, _ := mustGet(t, s, dom, "k", "f"); v != "late" {
			t.Errorf("%s resolved to %q, want late (higher stamp wins regardless of arrival)", s.actor, v)
		}
	}
	if digest(t, x, dom) != digest(t, y, dom) {
		t.Fatal("nodes disagree after opposite delivery orders")
	}
}

func TestNodeIDBreaksExactStampTie(t *testing.T) {
	// Two boxes producing the SAME (wall, logical) is what a tie-break exists
	// for. Both must pick the same winner, deterministically, in both orders.
	opB := &Delta{Domain: dom, Ops: []Op{mkop("B", 1, "k", "f", OpSet, "from-b", 500, 7)}}
	opC := &Delta{Domain: dom, Ops: []Op{mkop("C", 1, "k", "f", OpSet, "from-c", 500, 7)}}

	x := newTestStore(t, "X")
	if _, err := x.Merge(wire(t, opB)); err != nil {
		t.Fatal(err)
	}
	if _, err := x.Merge(wire(t, opC)); err != nil {
		t.Fatal(err)
	}
	y := newTestStore(t, "Y")
	if _, err := y.Merge(wire(t, opC)); err != nil {
		t.Fatal(err)
	}
	if _, err := y.Merge(wire(t, opB)); err != nil {
		t.Fatal(err)
	}

	vx, _ := mustGet(t, x, dom, "k", "f")
	vy, _ := mustGet(t, y, dom, "k", "f")
	if vx != vy {
		t.Fatalf("exact-stamp tie resolved differently: x=%q y=%q", vx, vy)
	}
	if vx != "from-c" {
		t.Fatalf("tie winner = %q, want from-c (actor C > actor B)", vx)
	}
}

func TestDeleteVsConcurrentUpdate(t *testing.T) {
	// A tombstone competes by STAMP. A delete that loses does not erase a newer
	// write, and a delete that wins is not undone by a late-arriving older one.
	t.Run("newer delete wins", func(t *testing.T) {
		set := &Delta{Domain: dom, Ops: []Op{mkop("B", 1, "k", "f", OpSet, "value", 100, 0)}}
		del := &Delta{Domain: dom, Ops: []Op{mkop("C", 1, "k", "f", OpDel, "", 200, 0)}}
		x, y := newTestStore(t, "X"), newTestStore(t, "Y")
		if _, err := x.Merge(wire(t, set)); err != nil {
			t.Fatal(err)
		}
		if _, err := x.Merge(wire(t, del)); err != nil {
			t.Fatal(err)
		}
		if _, err := y.Merge(wire(t, del)); err != nil {
			t.Fatal(err)
		}
		if _, err := y.Merge(wire(t, set)); err != nil {
			t.Fatal(err)
		}
		for _, s := range []*Store{x, y} {
			if v, ok := mustGet(t, s, dom, "k", "f"); ok {
				t.Errorf("%s: newer delete lost, value %q resurrected", s.actor, v)
			}
		}
		if digest(t, x, dom) != digest(t, y, dom) {
			t.Fatal("delete/update disagreement across delivery orders")
		}
	})

	t.Run("newer write beats older delete", func(t *testing.T) {
		del := &Delta{Domain: dom, Ops: []Op{mkop("B", 1, "k", "f", OpDel, "", 100, 0)}}
		set := &Delta{Domain: dom, Ops: []Op{mkop("C", 1, "k", "f", OpSet, "revived", 200, 0)}}
		x, y := newTestStore(t, "X"), newTestStore(t, "Y")
		if _, err := x.Merge(wire(t, del)); err != nil {
			t.Fatal(err)
		}
		if _, err := x.Merge(wire(t, set)); err != nil {
			t.Fatal(err)
		}
		if _, err := y.Merge(wire(t, set)); err != nil {
			t.Fatal(err)
		}
		if _, err := y.Merge(wire(t, del)); err != nil {
			t.Fatal(err)
		}
		for _, s := range []*Store{x, y} {
			if v, ok := mustGet(t, s, dom, "k", "f"); !ok || v != "revived" {
				t.Errorf("%s: newer write lost to an older delete: %q ok=%v", s.actor, v, ok)
			}
		}
	})

	t.Run("live beats deleted on an exact tie", func(t *testing.T) {
		del := &Delta{Domain: dom, Ops: []Op{mkop("B", 1, "k", "f", OpDel, "", 100, 3)}}
		set := &Delta{Domain: dom, Ops: []Op{mkop("B", 2, "k", "f", OpSet, "kept", 100, 3)}}
		// Same stamp entirely (same actor, forced) — only the liveness component
		// can decide, and it must decide the same way in both orders.
		x, y := newTestStore(t, "X"), newTestStore(t, "Y")
		if _, err := x.Merge(wire(t, del)); err != nil {
			t.Fatal(err)
		}
		if _, err := x.Merge(wire(t, set)); err != nil {
			t.Fatal(err)
		}
		if _, err := y.Merge(wire(t, set)); err != nil {
			t.Fatal(err)
		}
		if _, err := y.Merge(wire(t, del)); err != nil {
			t.Fatal(err)
		}
		if digest(t, x, dom) != digest(t, y, dom) {
			t.Fatalf("exact-tie liveness resolved by arrival order:\n%s\n---\n%s", digest(t, x, dom), digest(t, y, dom))
		}
		if _, ok := mustGet(t, x, dom, "k", "f"); !ok {
			t.Error("live must beat deleted on an exact stamp tie")
		}
	})
}

func TestRegisterWinsIsATotalOrder(t *testing.T) {
	// registerWins must be antisymmetric: for any two distinct states exactly one
	// direction wins. If both (or neither) could win, arrival order would leak
	// into the result.
	type st struct {
		stamp   Stamp
		deleted bool
		val     []byte
	}
	states := []st{
		{Stamp{100, 0, "A"}, false, []byte("a")},
		{Stamp{100, 0, "A"}, true, nil},
		{Stamp{100, 0, "B"}, false, []byte("b")},
		{Stamp{100, 1, "A"}, false, []byte("c")},
		{Stamp{200, 0, "A"}, false, []byte("d")},
		{Stamp{100, 0, "A"}, false, []byte("zz")},
	}
	for i, x := range states {
		if registerWins(x.stamp, x.deleted, x.val, x.stamp, x.deleted, x.val) {
			t.Errorf("state %d beats itself — merge would not be idempotent", i)
		}
		for j, y := range states {
			if i == j {
				continue
			}
			xy := registerWins(x.stamp, x.deleted, x.val, y.stamp, y.deleted, y.val)
			yx := registerWins(y.stamp, y.deleted, y.val, x.stamp, x.deleted, x.val)
			if xy == yx {
				t.Errorf("states %d and %d are not strictly ordered (x>y=%v, y>x=%v)", i, j, xy, yx)
			}
		}
	}
}

// ── multi-node convergence ───────────────────────────────────────────────────

func TestThreeNodeRelayConvergence(t *testing.T) {
	// C never talks to A. Ops must reach it THROUGH B, stamped with their origin
	// actor, or the tie-break would differ on C and the cluster would diverge.
	a := newTestStore(t, "A")
	b := newTestStore(t, "B")
	c := newTestStore(t, "C")

	if err := a.Set(dom, "k", "f", []byte("from-a")); err != nil {
		t.Fatal(err)
	}
	if err := c.Set(dom, "k", "g", []byte("from-c")); err != nil {
		t.Fatal(err)
	}

	// Only A↔B and B↔C links exist.
	for round := 0; round < 4; round++ {
		pull(t, b, a, dom)
		pull(t, a, b, dom)
		pull(t, c, b, dom)
		pull(t, b, c, dom)
	}
	assertConverged(t, dom, a, b, c)
	if v, ok := mustGet(t, c, dom, "k", "f"); !ok || v != "from-a" {
		t.Fatalf("relayed op did not reach C: %q ok=%v", v, ok)
	}
	if v, ok := mustGet(t, a, dom, "k", "g"); !ok || v != "from-c" {
		t.Fatalf("relayed op did not reach A: %q ok=%v", v, ok)
	}
	// The relayed op must still carry its ORIGIN actor, not the relay's.
	st, _, err := c.stampOf(dom, "k", "f")
	if err != nil {
		t.Fatal(err)
	}
	if st.Actor != "A" {
		t.Fatalf("relayed stamp actor = %q, want A (the origin, not the relay)", st.Actor)
	}
}

func TestOfflineNodeCatchesUp(t *testing.T) {
	a := newTestStore(t, "A")
	b := newTestStore(t, "B")
	offline := newTestStore(t, "C")

	// C makes one write, then goes dark.
	if err := offline.Set(dom, "k", "owner", []byte("c-was-here")); err != nil {
		t.Fatal(err)
	}
	syncAll(t, dom, a, b, offline)

	// The cluster moves on without C.
	for i := 0; i < 30; i++ {
		if err := a.Set(dom, "k", "f"+strconv.Itoa(i), []byte("a"+strconv.Itoa(i))); err != nil {
			t.Fatal(err)
		}
		if err := b.Set(dom, "k", "g"+strconv.Itoa(i), []byte("b"+strconv.Itoa(i))); err != nil {
			t.Fatal(err)
		}
	}
	syncAll(t, dom, a, b)
	assertConverged(t, dom, a, b)

	// C comes back and catches up in one go.
	syncAll(t, dom, a, b, offline)
	assertConverged(t, dom, a, b, offline)
	if v, ok := mustGet(t, a, dom, "k", "owner"); !ok || v != "c-was-here" {
		t.Fatalf("the offline node's own write was lost: %q ok=%v", v, ok)
	}
}

func TestOfflineNodeCatchesUpViaSnapshotAfterCompaction(t *testing.T) {
	// The straggler is now BELOW the compaction floor: the ops it missed no
	// longer exist. It must still converge, via the bounded snapshot path.
	a := newTestStore(t, "A")
	offline := newTestStore(t, "C")

	if err := offline.Set(dom, "k", "owner", []byte("c-was-here")); err != nil {
		t.Fatal(err)
	}
	syncAll(t, dom, a, offline)

	for i := 0; i < 40; i++ {
		if err := a.Set(dom, "k", "f", []byte("v"+strconv.Itoa(i))); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := a.Compact(dom, 3); err != nil {
		t.Fatal(err)
	}

	d := deltaFor(t, a, offline, dom)
	if !d.SnapshotRequired {
		t.Fatal("a straggler below the floor must be served a snapshot")
	}
	if _, err := offline.Merge(d); err != nil {
		t.Fatal(err)
	}
	assertConverged(t, dom, a, offline)
	// Its own pre-compaction write must survive the snapshot merge.
	if v, ok := mustGet(t, offline, dom, "k", "owner"); !ok || v != "c-was-here" {
		t.Fatalf("snapshot bootstrap lost the straggler's own write: %q ok=%v", v, ok)
	}
}

func TestDuplicateAndOutOfOrderDelivery(t *testing.T) {
	// Build a realistic op set on three nodes, then deliver every op to a fresh
	// node REVERSED and each op twice.
	src := buildOps(t)
	forward := newTestStore(t, "F")
	for _, op := range src {
		deliver(t, forward, dom, []Op{op})
	}

	reverse := newTestStore(t, "R")
	for i := len(src) - 1; i >= 0; i-- {
		deliver(t, reverse, dom, []Op{src[i]})
		deliver(t, reverse, dom, []Op{src[i]}) // duplicate delivery
	}

	if a, b := digest(t, forward, dom), digest(t, reverse, dom); a != b {
		t.Fatalf("forward and reversed+duplicated delivery diverged:\n--- forward ---\n%s\n--- reverse ---\n%s", a, b)
	}
}

// buildOps produces a set of conflicting ops originating from three actors.
func buildOps(t *testing.T) []Op {
	t.Helper()
	a := newTestStore(t, "A")
	b := newTestStore(t, "B")
	c := newTestStore(t, "C")
	ftA, ftB, ftC := newFakeTime(1000), newFakeTime(1000), newFakeTime(1000)
	withFakeClock(a, ftA)
	withFakeClock(b, ftB)
	withFakeClock(c, ftC)

	// Interleaved wall clocks so the ops genuinely conflict rather than being
	// trivially ordered by origin.
	for i := 0; i < 6; i++ {
		ftA.set(1000 + int64(i)*10)
		ftB.set(1005 + int64(i)*10)
		ftC.set(1003 + int64(i)*10)
		if err := a.Set(dom, "row:1", "title", []byte(fmt.Sprintf("a%d", i))); err != nil {
			t.Fatal(err)
		}
		if err := b.Set(dom, "row:1", "title", []byte(fmt.Sprintf("b%d", i))); err != nil {
			t.Fatal(err)
		}
		if err := c.Set(dom, "row:1", "body", []byte(fmt.Sprintf("c%d", i))); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.Delete(dom, "row:1", "body"); err != nil {
		t.Fatal(err)
	}
	if err := a.Add(dom, "row:1", "views", 3); err != nil {
		t.Fatal(err)
	}
	if err := c.Add(dom, "row:1", "views", 4); err != nil {
		t.Fatal(err)
	}

	var out []Op
	out = append(out, allOps(t, a, dom)...)
	out = append(out, allOps(t, b, dom)...)
	out = append(out, allOps(t, c, dom)...)
	return out
}

func TestEveryPermutationConverges(t *testing.T) {
	// Exhaustive: EVERY ordering of a conflicting op set must produce identical
	// state. 6 ops = 720 orderings, each into a fresh replica.
	ops := []Op{
		mkop("A", 1, "k", "f", OpSet, "a1", 100, 0),
		mkop("A", 2, "k", "g", OpSet, "a2", 400, 0),
		mkop("B", 1, "k", "f", OpSet, "b1", 300, 0),
		mkop("B", 2, "k", "f", OpDel, "", 250, 0),
		mkop("C", 1, "k", "g", OpSet, "c1", 400, 0), // exact wall tie with A/2
		mkop("C", 2, "k", "f", OpSet, "c2", 300, 0), // exact wall tie with B/1
	}

	var want string
	count := 0
	permute(ops, func(order []Op) {
		s := newTestStore(t, "P"+strconv.Itoa(count))
		for _, op := range order {
			deliver(t, s, dom, []Op{op})
		}
		got := digest(t, s, dom)
		_ = s.Close()
		count++
		if want == "" {
			want = got
			return
		}
		if got != want {
			var names []string
			for _, o := range order {
				names = append(names, fmt.Sprintf("%s/%d", o.Actor, o.Seq))
			}
			t.Fatalf("ordering %v diverged:\n--- want ---\n%s\n--- got ---\n%s", names, want, got)
		}
	})
	if count != 720 {
		t.Fatalf("ran %d permutations, want 720", count)
	}
}

// permute calls fn once per permutation of ops (Heap's algorithm).
func permute(ops []Op, fn func([]Op)) {
	work := make([]Op, len(ops))
	copy(work, ops)
	var rec func(int)
	rec = func(k int) {
		if k == 1 {
			out := make([]Op, len(work))
			copy(out, work)
			fn(out)
			return
		}
		for i := 0; i < k; i++ {
			rec(k - 1)
			if k%2 == 0 {
				work[i], work[k-1] = work[k-1], work[i]
			} else {
				work[0], work[k-1] = work[k-1], work[0]
			}
		}
	}
	rec(len(work))
}

func TestRandomisedConvergence(t *testing.T) {
	// Property test: random op sets, random delivery orders, random duplication,
	// random partial deliveries. Every replica that has seen every op must hold
	// identical state.
	//
	// The seed is printed on failure (and reproducible via VULOS_CRDT_SEED) so a
	// counterexample is never lost to randomness.
	seed := int64(20260810)
	if env := os.Getenv("VULOS_CRDT_SEED"); env != "" {
		if v, err := strconv.ParseInt(env, 10, 64); err == nil {
			seed = v
		}
	}

	for trial := 0; trial < 40; trial++ {
		trialSeed := seed + int64(trial)
		t.Run(fmt.Sprintf("trial%02d", trial), func(t *testing.T) {
			rng := rand.New(rand.NewSource(trialSeed))
			ops := randomOps(t, rng)

			// Three replicas, each fed the SAME ops in an independently shuffled
			// order, some delivered more than once, in randomly sized batches.
			var replicas []*Store
			for i := 0; i < 3; i++ {
				s := newTestStore(t, fmt.Sprintf("N%d", i))
				replicas = append(replicas, s)

				order := make([]Op, len(ops))
				copy(order, ops)
				rng.Shuffle(len(order), func(a, b int) { order[a], order[b] = order[b], order[a] })
				// Duplicate a random subset.
				for j := 0; j < len(ops)/3; j++ {
					order = append(order, ops[rng.Intn(len(ops))])
				}
				rng.Shuffle(len(order), func(a, b int) { order[a], order[b] = order[b], order[a] })

				for pos := 0; pos < len(order); {
					batch := 1 + rng.Intn(4)
					if pos+batch > len(order) {
						batch = len(order) - pos
					}
					deliver(t, s, dom, order[pos:pos+batch])
					pos += batch
				}
			}

			base := digest(t, replicas[0], dom)
			for i, r := range replicas[1:] {
				if got := digest(t, r, dom); got != base {
					t.Fatalf("SEED=%d: replica 0 and replica %d diverged\n--- r0 ---\n%s\n--- r%d ---\n%s\n(reproduce with VULOS_CRDT_SEED=%d)",
						trialSeed, i+1, base, i+1, got, trialSeed)
				}
			}
		})
	}
}

// randomOps generates a random set of conflicting ops from several actors.
// Sequence numbers per actor are contiguous from 1, as a real replica's are.
func randomOps(t *testing.T, rng *rand.Rand) []Op {
	t.Helper()
	actors := []string{"A", "B", "C", "D"}
	keys := []string{"row:1", "row:2"}
	fields := []string{"title", "body", "tag"}

	seqs := map[string]uint64{}
	var ops []Op
	n := 12 + rng.Intn(20)
	for i := 0; i < n; i++ {
		actor := actors[rng.Intn(len(actors))]
		seqs[actor]++
		key := keys[rng.Intn(len(keys))]
		field := fields[rng.Intn(len(fields))]
		// A deliberately SMALL wall range so exact ties (and therefore the
		// tie-break path) actually occur.
		wall := int64(100 + rng.Intn(5))
		logical := uint32(rng.Intn(3))

		switch rng.Intn(10) {
		case 0, 1:
			ops = append(ops, mkop(actor, seqs[actor], key, field, OpDel, "", wall, logical))
		case 2:
			ops = append(ops, Op{
				Domain: dom, Actor: actor, Seq: seqs[actor], Key: key, Field: "count", Kind: OpInc,
				Value: encodeCounter(int64(rng.Intn(50)), int64(rng.Intn(20))),
				Stamp: Stamp{Wall: wall, Logical: logical, Actor: actor},
			})
		default:
			ops = append(ops, mkop(actor, seqs[actor], key, field, OpSet,
				fmt.Sprintf("v%d", rng.Intn(6)), wall, logical))
		}
	}
	// Deterministic canonical order in, so shuffling is the only source of order.
	sort.SliceStable(ops, func(i, j int) bool {
		if ops[i].Actor != ops[j].Actor {
			return ops[i].Actor < ops[j].Actor
		}
		return ops[i].Seq < ops[j].Seq
	})
	return ops
}

func TestConvergenceViaSnapshotAndOpsMixed(t *testing.T) {
	// A four-node cluster where one node bootstraps from a snapshot while the
	// others exchange ops — the two paths must land on the same state.
	a := newTestStore(t, "A")
	b := newTestStore(t, "B")
	c := newTestStore(t, "C")
	for i := 0; i < 8; i++ {
		if err := a.Set(dom, "k", "f"+strconv.Itoa(i), []byte("a")); err != nil {
			t.Fatal(err)
		}
		if err := b.Set(dom, "k", "f"+strconv.Itoa(i), []byte("b")); err != nil {
			t.Fatal(err)
		}
	}
	syncAll(t, dom, a, b)

	// C bootstraps from A's snapshot, then meets B.
	snap, err := a.Snapshot(dom)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.ApplySnapshot(snap); err != nil {
		t.Fatal(err)
	}
	if err := c.Set(dom, "k", "own", []byte("c")); err != nil {
		t.Fatal(err)
	}
	syncAll(t, dom, a, b, c)
	assertConverged(t, dom, a, b, c)
	if v, ok := mustGet(t, a, dom, "k", "own"); !ok || v != "c" {
		t.Fatalf("the snapshot-bootstrapped node's own write did not propagate: %q ok=%v", v, ok)
	}
}
