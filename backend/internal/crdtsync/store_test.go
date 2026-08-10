package crdtsync

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

const dom = "test"

func TestOpenRejectsEmptyActor(t *testing.T) {
	// The actor is the LWW tie-break. A blank one would make two boxes
	// indistinguishable and the tie-break non-deterministic, so Open must refuse
	// rather than silently produce a replica that cannot converge.
	if _, err := Open(filepath.Join(t.TempDir(), "x.db"), "", testDomains); err == nil {
		t.Fatal("Open with empty actor must fail")
	}
}

func TestOpenRejectsEmptyAllowList(t *testing.T) {
	// Replication is opt-in per domain. An empty allow-list must fail closed
	// rather than quietly replicate everything.
	for _, domains := range [][]string{nil, {}, {""}, {"  "}} {
		if _, err := Open(filepath.Join(t.TempDir(), "x.db"), "A", domains); err == nil {
			t.Errorf("Open with allow-list %q must fail", domains)
		}
	}
}

func TestSetGetDelete(t *testing.T) {
	s := newTestStore(t, "A")
	if v, ok := mustGet(t, s, dom, "k1", "name"); ok {
		t.Fatalf("unwritten field should not exist, got %q", v)
	}
	if err := s.Set(dom, "k1", "name", []byte("ada")); err != nil {
		t.Fatal(err)
	}
	if v, ok := mustGet(t, s, dom, "k1", "name"); !ok || v != "ada" {
		t.Fatalf("got %q ok=%v, want ada true", v, ok)
	}
	if err := s.Delete(dom, "k1", "name"); err != nil {
		t.Fatal(err)
	}
	if v, ok := mustGet(t, s, dom, "k1", "name"); ok {
		t.Fatalf("deleted field must read as absent, got %q", v)
	}
}

func TestSetRequiresDomainKeyField(t *testing.T) {
	s := newTestStore(t, "A")
	for _, c := range []struct{ d, k, f string }{{"", "k", "f"}, {"d", "", "f"}, {"d", "k", ""}} {
		if err := s.Set(c.d, c.k, c.f, []byte("v")); err == nil {
			t.Errorf("Set(%q,%q,%q) must fail", c.d, c.k, c.f)
		}
		if err := s.Add(c.d, c.k, c.f, 1); err == nil {
			t.Errorf("Add(%q,%q,%q) must fail", c.d, c.k, c.f)
		}
	}
}

func TestFieldsAndKeys(t *testing.T) {
	s := newTestStore(t, "A")
	if err := s.Set(dom, "k1", "name", []byte("ada")); err != nil {
		t.Fatal(err)
	}
	if err := s.Set(dom, "k1", "email", []byte("a@x")); err != nil {
		t.Fatal(err)
	}
	if err := s.Set(dom, "k2", "name", []byte("bob")); err != nil {
		t.Fatal(err)
	}
	f, err := s.Fields(dom, "k1")
	if err != nil {
		t.Fatal(err)
	}
	if len(f) != 2 || string(f["name"]) != "ada" || string(f["email"]) != "a@x" {
		t.Fatalf("Fields = %v", f)
	}
	keys, err := s.Keys(dom)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 || keys[0] != "k1" || keys[1] != "k2" {
		t.Fatalf("Keys = %v", keys)
	}

	// A key whose every field is tombstoned drops out of Keys but must stay in
	// AllKeys: a materialiser has to actively REMOVE the row, which it can only
	// do if it is still told the key exists.
	if err := s.Delete(dom, "k2", "name"); err != nil {
		t.Fatal(err)
	}
	keys, _ = s.Keys(dom)
	if len(keys) != 1 || keys[0] != "k1" {
		t.Fatalf("Keys after delete = %v, want [k1]", keys)
	}
	all, err := s.AllKeys(dom)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("AllKeys after delete = %v, want both keys", all)
	}
}

func TestCounterAccumulates(t *testing.T) {
	s := newTestStore(t, "A")
	for _, d := range []int64{5, 3, -2} {
		if err := s.Add(dom, "c", "hits", d); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.Counter(dom, "c", "hits")
	if err != nil {
		t.Fatal(err)
	}
	if got != 6 {
		t.Fatalf("Counter = %d, want 6", got)
	}
}

func TestVersionVectorAdvancesPerOp(t *testing.T) {
	s := newTestStore(t, "A")
	for i := 0; i < 3; i++ {
		if err := s.Set(dom, "k", "f", []byte("v")); err != nil {
			t.Fatal(err)
		}
	}
	vv, err := s.VersionVector(dom)
	if err != nil {
		t.Fatal(err)
	}
	if vv["A"] != 3 {
		t.Fatalf("vv[A] = %d, want 3", vv["A"])
	}
}

func TestStampOrdering(t *testing.T) {
	cases := []struct {
		a, b Stamp
		want int
	}{
		{Stamp{1, 0, "A"}, Stamp{2, 0, "A"}, -1},
		{Stamp{2, 0, "A"}, Stamp{1, 0, "A"}, 1},
		{Stamp{1, 0, "A"}, Stamp{1, 1, "A"}, -1},
		{Stamp{1, 1, "A"}, Stamp{1, 0, "A"}, 1},
		{Stamp{1, 1, "A"}, Stamp{1, 1, "B"}, -1},
		{Stamp{1, 1, "B"}, Stamp{1, 1, "A"}, 1},
		{Stamp{1, 1, "A"}, Stamp{1, 1, "A"}, 0},
	}
	for _, c := range cases {
		if got := c.a.Compare(c.b); got != c.want {
			t.Errorf("%s.Compare(%s) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestClockNeverGoesBackwards(t *testing.T) {
	// A box whose wall clock jumps BACKWARDS must still emit strictly increasing
	// stamps, or its own later writes would lose to its own earlier ones.
	ft := newFakeTime(1000)
	c := &clock{now: ft.now}
	first := c.tick("A")
	ft.set(500) // clock jumps back half a second
	second := c.tick("A")
	if second.Compare(first) <= 0 {
		t.Fatalf("stamp went backwards across a clock jump: %s then %s", first, second)
	}
}

func TestClockObserveJumpsPastRemote(t *testing.T) {
	// Receiving a stamp from a box with a faster clock must drag ours forward, so
	// the next thing we write sorts AFTER what we just received rather than
	// silently losing to it.
	ft := newFakeTime(1000)
	c := &clock{now: ft.now}
	remote := Stamp{Wall: 9_000, Logical: 4, Actor: "B"}
	c.observe(remote)
	next := c.tick("A")
	if next.Compare(remote) <= 0 {
		t.Fatalf("local stamp %s does not sort after observed remote %s", next, remote)
	}
}

func TestClockReseededOnReopen(t *testing.T) {
	// Restarting must not reset the clock below what this box already wrote,
	// even if the host wall clock is behind.
	path := filepath.Join(t.TempDir(), "c.db")
	s := newTestStoreAt(t, path, "A")
	ft := newFakeTime(5_000_000)
	withFakeClock(s, ft)
	if err := s.Set(dom, "k", "f", []byte("v1")); err != nil {
		t.Fatal(err)
	}
	before, _, err := s.stampOf(dom, "k", "f")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s2 := newTestStoreAt(t, path, "A")
	defer s2.Close()
	slow := newFakeTime(1000) // host clock is now way behind
	withFakeClock(s2, slow)
	if err := s2.Set(dom, "k", "f", []byte("v2")); err != nil {
		t.Fatal(err)
	}
	after, _, err := s2.stampOf(dom, "k", "f")
	if err != nil {
		t.Fatal(err)
	}
	if after.Compare(before) <= 0 {
		t.Fatalf("post-restart stamp %s does not beat pre-restart %s", after, before)
	}
	if v, _ := mustGet(t, s2, dom, "k", "f"); v != "v2" {
		t.Fatalf("post-restart write lost: %q", v)
	}
}

func TestOpKindValid(t *testing.T) {
	for _, k := range []OpKind{OpSet, OpDel, OpInc} {
		if !k.Valid() {
			t.Errorf("%s must be valid", k)
		}
	}
	for _, k := range []OpKind{"", "nope", "SET"} {
		if k.Valid() {
			t.Errorf("%q must be invalid", k)
		}
	}
}

func TestMergeRejectsMalformedOps(t *testing.T) {
	s := newTestStore(t, "A")
	base := Op{Domain: dom, Actor: "B", Seq: 1, Key: "k", Field: "f", Kind: OpSet, Value: []byte("v"), Stamp: Stamp{1, 0, "B"}}

	bad := map[string]func(Op) Op{
		"unknown kind": func(o Op) Op { o.Kind = "frobnicate"; return o },
		"no actor":     func(o Op) Op { o.Actor = ""; return o },
		"zero seq":     func(o Op) Op { o.Seq = 0; return o },
		"no key":       func(o Op) Op { o.Key = ""; return o },
		"no field":     func(o Op) Op { o.Field = ""; return o },
	}
	for name, mut := range bad {
		d := &Delta{Domain: dom, Ops: []Op{mut(base)}}
		if _, err := s.Merge(d); err == nil {
			t.Errorf("%s: Merge must reject", name)
		}
	}

	// An op this build cannot interpret must not advance the version vector:
	// promising a peer we hold data we could not apply is how a gap becomes
	// permanent.
	vv, _ := s.VersionVector(dom)
	if len(vv) != 0 {
		t.Fatalf("rejected ops advanced the version vector: %v", vv)
	}
}

func TestMergeRejectsForgedStampActor(t *testing.T) {
	// An op's stamp actor MUST equal its origin actor. The log stores the stamp
	// actor implicitly (it is the origin), so a forged mismatch would be applied
	// with one tie-break here and RELAYED with a different one to a third box —
	// a real divergence. Reject at the boundary instead.
	s := newTestStore(t, "A")
	op := Op{Domain: dom, Actor: "B", Seq: 1, Key: "k", Field: "f", Kind: OpSet,
		Value: []byte("v"), Stamp: Stamp{Wall: 1, Logical: 0, Actor: "Z"}}
	_, err := s.Merge(&Delta{Domain: dom, Ops: []Op{op}})
	if err == nil {
		t.Fatal("Merge must reject an op whose stamp actor differs from its origin actor")
	}
	if !strings.Contains(err.Error(), "stamp actor") {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := mustGet(t, s, dom, "k", "f"); ok {
		t.Fatal("rejected op must not reach state")
	}
}

func TestMergeNilDelta(t *testing.T) {
	s := newTestStore(t, "A")
	if _, err := s.Merge(nil); err == nil {
		t.Fatal("Merge(nil) must fail")
	}
}

func TestDeltaTruncationIsContiguous(t *testing.T) {
	s := newTestStore(t, "A")
	for i := 0; i < 10; i++ {
		if err := s.Set(dom, "k", "f", []byte{byte('a' + i)}); err != nil {
			t.Fatal(err)
		}
	}
	d, err := s.Delta(dom, VersionVector{}, 4)
	if err != nil {
		t.Fatal(err)
	}
	if !d.Truncated {
		t.Fatal("delta of 10 ops with limit 4 must report Truncated")
	}
	if len(d.Ops) != 4 {
		t.Fatalf("got %d ops, want 4", len(d.Ops))
	}
	// A truncated delta must still be a contiguous PREFIX — otherwise the
	// receiver could not advance its version vector at all.
	for i, op := range d.Ops {
		if op.Seq != uint64(i+1) {
			t.Fatalf("op %d has seq %d, want %d (delta is not a contiguous prefix)", i, op.Seq, i+1)
		}
	}
}

func TestTruncatedDeltaStillConverges(t *testing.T) {
	a := newTestStore(t, "A")
	b := newTestStore(t, "B")
	for i := 0; i < 25; i++ {
		if err := a.Set(dom, "k", "f", []byte{byte('a' + i%26)}); err != nil {
			t.Fatal(err)
		}
	}
	// Pull in deliberately tiny bites.
	for round := 0; round < 50; round++ {
		vv, _ := b.VersionVector(dom)
		d, err := a.Delta(dom, vv, 3)
		if err != nil {
			t.Fatal(err)
		}
		if len(d.Ops) == 0 {
			break
		}
		if _, err := b.Merge(wire(t, d)); err != nil {
			t.Fatal(err)
		}
	}
	assertConverged(t, dom, a, b)
}

func TestGapDoesNotAdvanceVersionVector(t *testing.T) {
	// A peer that hands us seq 3 while we still lack seq 2 must not be able to
	// make us claim we hold seq 3 — we would never ask for 2 again.
	s := newTestStore(t, "A")
	mk := func(seq uint64, val string) Op {
		return Op{Domain: dom, Actor: "B", Seq: seq, Key: "k", Field: "f", Kind: OpSet,
			Value: []byte(val), Stamp: Stamp{Wall: int64(seq), Actor: "B"}}
	}
	if _, err := s.Merge(&Delta{Domain: dom, Ops: []Op{mk(1, "one"), mk(3, "three")}}); err != nil {
		t.Fatal(err)
	}
	vv, _ := s.VersionVector(dom)
	if vv["B"] != 1 {
		t.Fatalf("vv[B] = %d, want 1 (must stop at the gap)", vv["B"])
	}
	// State still reflects the out-of-order op, because state is stamp-ordered
	// and therefore correct regardless of arrival order.
	if v, ok := mustGet(t, s, dom, "k", "f"); !ok || v != "three" {
		t.Fatalf("state = %q ok=%v, want three", v, ok)
	}
	// Filling the gap releases the whole contiguous run.
	if _, err := s.Merge(&Delta{Domain: dom, Ops: []Op{mk(2, "two")}}); err != nil {
		t.Fatal(err)
	}
	vv, _ = s.VersionVector(dom)
	if vv["B"] != 3 {
		t.Fatalf("vv[B] = %d after filling the gap, want 3", vv["B"])
	}
}

func TestSnapshotRoundTrip(t *testing.T) {
	a := newTestStore(t, "A")
	if err := a.Set(dom, "k1", "name", []byte("ada")); err != nil {
		t.Fatal(err)
	}
	if err := a.Delete(dom, "k1", "old"); err != nil {
		t.Fatal(err)
	}
	if err := a.Add(dom, "c", "hits", 7); err != nil {
		t.Fatal(err)
	}
	snap, err := a.Snapshot(dom)
	if err != nil {
		t.Fatal(err)
	}
	if snap.SchemaVers != SnapshotSchemaVersion {
		t.Fatalf("schema version = %d", snap.SchemaVers)
	}

	b := newTestStore(t, "B")
	if err := b.ApplySnapshot(snap); err != nil {
		t.Fatal(err)
	}
	if da, db := digest(t, a, dom), digest(t, b, dom); da != db {
		t.Fatalf("snapshot did not reproduce state:\n--- a ---\n%s\n--- b ---\n%s", da, db)
	}
	if got, _ := b.Counter(dom, "c", "hits"); got != 7 {
		t.Fatalf("counter after snapshot = %d, want 7", got)
	}
}

func TestApplySnapshotRefusesFutureSchema(t *testing.T) {
	s := newTestStore(t, "A")
	snap := &Snapshot{Domain: dom, SchemaVers: SnapshotSchemaVersion + 1, VV: VersionVector{"B": 3}}
	err := s.ApplySnapshot(snap)
	if !errors.Is(err, ErrSnapshotSchema) {
		t.Fatalf("err = %v, want ErrSnapshotSchema", err)
	}
	// Refusing must be total: a half-applied snapshot would advance the version
	// vector past state this box does not hold.
	vv, _ := s.VersionVector(dom)
	if len(vv) != 0 {
		t.Fatalf("refused snapshot still advanced vv: %v", vv)
	}
}

func TestApplySnapshotPreservesNewerLocalWrite(t *testing.T) {
	// A snapshot is a MERGE, not a replace. A node with a write the snapshot's
	// author never saw must keep it.
	a := newTestStore(t, "A")
	ftA := newFakeTime(1_000)
	withFakeClock(a, ftA)
	if err := a.Set(dom, "k", "f", []byte("old")); err != nil {
		t.Fatal(err)
	}
	snap, err := a.Snapshot(dom)
	if err != nil {
		t.Fatal(err)
	}

	b := newTestStore(t, "B")
	ftB := newFakeTime(9_000) // b's write is strictly newer
	withFakeClock(b, ftB)
	if err := b.Set(dom, "k", "f", []byte("newer")); err != nil {
		t.Fatal(err)
	}
	if err := b.ApplySnapshot(snap); err != nil {
		t.Fatal(err)
	}
	if v, ok := mustGet(t, b, dom, "k", "f"); !ok || v != "newer" {
		t.Fatalf("snapshot clobbered a newer local write: %q", v)
	}
}

func TestCompactionServesSnapshotBelowFloor(t *testing.T) {
	a := newTestStore(t, "A")
	for i := 0; i < 20; i++ {
		if err := a.Set(dom, "k", "f", []byte{byte('a' + i)}); err != nil {
			t.Fatal(err)
		}
	}
	before, err := a.LogSize(dom)
	if err != nil {
		t.Fatal(err)
	}
	if before != 20 {
		t.Fatalf("log size = %d, want 20", before)
	}
	pruned, err := a.Compact(dom, 5)
	if err != nil {
		t.Fatal(err)
	}
	if pruned != 15 {
		t.Fatalf("pruned = %d, want 15", pruned)
	}
	after, _ := a.LogSize(dom)
	if after != 5 {
		t.Fatalf("log size after compaction = %d, want 5", after)
	}

	// A brand-new peer is below the floor and must be handed a snapshot.
	d, err := a.Delta(dom, VersionVector{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !d.SnapshotRequired || d.Snapshot == nil {
		t.Fatalf("peer below the compaction floor must get a snapshot, got %+v", d)
	}

	b := newTestStore(t, "B")
	if _, err := b.Merge(wire(t, d)); err != nil {
		t.Fatal(err)
	}
	assertConverged(t, dom, a, b)

	// A peer that is CAUGHT UP past the floor still gets ordinary ops.
	vv, _ := b.VersionVector(dom)
	d2, err := a.Delta(dom, vv, 0)
	if err != nil {
		t.Fatal(err)
	}
	if d2.SnapshotRequired {
		t.Fatal("a caught-up peer must not be forced through a snapshot")
	}
}

func TestCompactRejectsNegativeKeep(t *testing.T) {
	s := newTestStore(t, "A")
	if _, err := s.Compact(dom, -1); err == nil {
		t.Fatal("Compact with negative keep must fail")
	}
}

func TestOnLocalChangeFires(t *testing.T) {
	s := newTestStore(t, "A")
	fired := 0
	s.SetOnLocalChange(func() { fired++ })
	if err := s.Set(dom, "k", "f", []byte("v")); err != nil {
		t.Fatal(err)
	}
	if err := s.Add(dom, "c", "n", 1); err != nil {
		t.Fatal(err)
	}
	if fired != 2 {
		t.Fatalf("hook fired %d times, want 2", fired)
	}
	// A REMOTE merge must not fire the local-change hook, or two boxes would
	// ping-pong pushes at each other forever.
	op := Op{Domain: dom, Actor: "B", Seq: 1, Key: "k", Field: "g", Kind: OpSet,
		Value: []byte("v"), Stamp: Stamp{Wall: 1, Actor: "B"}}
	if _, err := s.Merge(&Delta{Domain: dom, Ops: []Op{op}}); err != nil {
		t.Fatal(err)
	}
	if fired != 2 {
		t.Fatalf("remote merge fired the local-change hook (%d)", fired)
	}
}

func TestCounterPayloadValidation(t *testing.T) {
	for _, bad := range [][]byte{nil, []byte("x"), []byte("1"), []byte("a:1"), []byte("1:b"), []byte("-1:0"), []byte("0:-1")} {
		if _, _, err := decodeCounter(bad); err == nil {
			t.Errorf("decodeCounter(%q) must fail", string(bad))
		}
	}
	pos, neg, err := decodeCounter(encodeCounter(7, 3))
	if err != nil || pos != 7 || neg != 3 {
		t.Fatalf("round trip = %d/%d err=%v", pos, neg, err)
	}
}

func TestDomainsAndStatus(t *testing.T) {
	s := newTestStore(t, "A")
	if err := s.Set("alpha", "k", "f", []byte("v")); err != nil {
		t.Fatal(err)
	}
	if err := s.Set("beta", "k", "f", []byte("v")); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete("beta", "k", "f"); err != nil {
		t.Fatal(err)
	}
	ds, err := s.Domains()
	if err != nil {
		t.Fatal(err)
	}
	if len(ds) != 2 || ds[0] != "alpha" || ds[1] != "beta" {
		t.Fatalf("Domains = %v", ds)
	}
	st, err := s.Status()
	if err != nil {
		t.Fatal(err)
	}
	if st.Actor != "A" || len(st.Domains) != 2 {
		t.Fatalf("Status = %+v", st)
	}
	if st.Domains[0].Registers != 1 {
		t.Fatalf("alpha registers = %d, want 1", st.Domains[0].Registers)
	}
	if st.Domains[1].Registers != 0 {
		t.Fatalf("beta must report 0 LIVE registers, got %d", st.Domains[1].Registers)
	}
}

// stampOf is a test-only accessor for a register's winning stamp.
func (s *Store) stampOf(domain, key, field string) (Stamp, bool, error) {
	var st Stamp
	var logical int64
	err := s.db.QueryRow(`SELECT wall, logical, actor FROM crdt_reg WHERE domain=? AND key=? AND field=?`,
		domain, key, field).Scan(&st.Wall, &logical, &st.Actor)
	if err != nil {
		return Stamp{}, false, nil
	}
	st.Logical = uint32(logical)
	return st, true, nil
}
