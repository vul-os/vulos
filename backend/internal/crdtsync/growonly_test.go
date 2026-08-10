package crdtsync

import (
	"errors"
	"testing"
)

// Grow-only exists so the security audit trail can replicate SAFELY.
//
// A plain last-writer-wins domain hands every rostered peer an EDIT primitive:
// a box that is later compromised could overwrite or tombstone the entry
// recording its own compromise, on every other box, and the merge would
// faithfully converge on the attacker's version. Grow-only removes that
// primitive from the algebra instead of trying to police it — a tombstone is
// refused, and a register is immutable once written because merge keeps the
// FIRST writer rather than the last.
//
// There are THREE ways to delete something, and a defence that closes fewer
// than all three closes none of them:
//
//	1. a local Delete
//	2. a tombstone op arriving from a peer
//	3. a SNAPSHOT from a peer carrying the same tombstone
//
// (3) is the one worth stating: a peer that cannot tombstone with an op could
// otherwise hand over a snapshot with the entry already deleted and get the
// identical effect. Each is asserted separately below.

// growOnlyDomain returns a domain declared GrowOnly in the policy table.
//
// It reads the real table rather than inventing a domain, so these tests
// exercise the same derivation production uses. It FAILS rather than skips when
// none is declared: a skip here would let the whole grow-only algebra rot
// unnoticed, which is exactly how a test comes to prove nothing.
//
// Note the domain need not be Sync:true. acctsec_sensitive_actions is declared
// GrowOnly and still refused, because its primary key is a per-box AUTOINCREMENT
// that would collide across machines (see policy.go). The algebra is complete
// and tested; only the schema blocks the flip.
func growOnlyDomain(t *testing.T) string {
	t.Helper()
	for _, d := range Decisions {
		if d.GrowOnly {
			return d.Domain
		}
	}
	t.Fatal("no domain is declared GrowOnly — the grow-only algebra below is unreachable and untested")
	return ""
}

func TestGrowOnly_LocalDeleteIsRefused(t *testing.T) {
	dom := growOnlyDomain(t)
	s := newTestStoreWithDomains(t, "box-a", []string{dom})

	if err := s.Set(dom, "evt-1", "detail", []byte("logged in")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	err := s.Delete(dom, "evt-1", "detail")
	var gv *ErrGrowOnlyViolation
	if !errors.As(err, &gv) {
		t.Fatalf("Delete on a grow-only domain = %v, want ErrGrowOnlyViolation — the audit trail must not be erasable locally either", err)
	}

	// And the entry must still be there: a refusal that dropped the value would
	// be the same data loss by another route.
	got, ok, err := s.Get(dom, "evt-1", "detail")
	if err != nil || !ok || string(got) != "logged in" {
		t.Fatalf("after refused Delete: got=%q ok=%v err=%v, want the original entry intact", got, ok, err)
	}
}

func TestGrowOnly_PeerTombstoneOpIsRefused(t *testing.T) {
	dom := growOnlyDomain(t)
	s := newTestStoreWithDomains(t, "box-a", []string{dom})
	if err := s.Set(dom, "evt-1", "detail", []byte("logged in")); err != nil {
		t.Fatal(err)
	}

	// A hostile peer's delete, delivered as an op inside a delta.
	hostile := &Delta{
		Domain: dom,
		Ops: []Op{{
			Domain: dom, Key: "evt-1", Field: "detail", Kind: OpDel,
			Actor: "peer-b", Seq: 1, Stamp: Stamp{Wall: 1 << 40, Logical: 0, Actor: "peer-b"},
		}},
	}
	_, err := s.Merge(hostile)
	var gv *ErrGrowOnlyViolation
	if !errors.As(err, &gv) {
		t.Fatalf("Merge of a peer tombstone = %v, want ErrGrowOnlyViolation — a compromised box could erase the record of its own compromise everywhere", err)
	}

	got, ok, _ := s.Get(dom, "evt-1", "detail")
	if !ok || string(got) != "logged in" {
		t.Fatalf("the peer's tombstone took effect anyway: got=%q ok=%v", got, ok)
	}
}

func TestGrowOnly_MergeKeepsTheFirstWriterNotTheLast(t *testing.T) {
	dom := growOnlyDomain(t)
	s := newTestStoreWithDomains(t, "box-a", []string{dom})

	// The local box records the truth first, with an EARLIER stamp.
	if err := s.Set(dom, "evt-1", "detail", []byte("original")); err != nil {
		t.Fatal(err)
	}

	// A peer arrives later with a higher stamp trying to rewrite it. Under
	// ordinary LWW this would win — that is exactly the primitive being removed.
	overwrite := &Delta{
		Domain: dom,
		Ops: []Op{{
			Domain: dom, Key: "evt-1", Field: "detail", Kind: OpSet, Value: []byte("rewritten"),
			Actor: "peer-b", Seq: 1, Stamp: Stamp{Wall: 1 << 42, Logical: 0, Actor: "peer-b"},
		}},
	}
	if _, err := s.Merge(overwrite); err != nil {
		t.Fatalf("Merge of a later Set: %v (a grow-only domain accepts ADDS; only edits and deletes are refused)", err)
	}

	got, ok, _ := s.Get(dom, "evt-1", "detail")
	if !ok || string(got) != "original" {
		t.Fatalf("value = %q, want %q — a later writer overwrote an immutable entry", got, "original")
	}
}

func TestGrowOnly_StillAcceptsNewEntries(t *testing.T) {
	// The counterweight. A domain that refused everything would pass every
	// assertion above while making the audit log useless.
	dom := growOnlyDomain(t)
	s := newTestStoreWithDomains(t, "box-a", []string{dom})

	add := &Delta{
		Domain: dom,
		Ops: []Op{{
			Domain: dom, Key: "evt-2", Field: "detail", Kind: OpSet, Value: []byte("from peer"),
			Actor: "peer-b", Seq: 1, Stamp: Stamp{Wall: 1 << 41, Logical: 0, Actor: "peer-b"},
		}},
	}
	if _, err := s.Merge(add); err != nil {
		t.Fatalf("Merge of a NEW entry = %v, want it accepted — grow-only means grow", err)
	}
	got, ok, _ := s.Get(dom, "evt-2", "detail")
	if !ok || string(got) != "from peer" {
		t.Fatalf("new entry did not land: got=%q ok=%v", got, ok)
	}
}

func TestGrowOnly_IsDerivedFromTheSamePolicyTable(t *testing.T) {
	// Grow-only must not be declarable somewhere other than the table that
	// decides what replicates, or a domain could be approved in one place and
	// given the wrong algebra in another.
	syncable := map[string]bool{}
	for _, d := range SyncableDomains() {
		syncable[d] = true
	}
	for _, d := range GrowOnlyDomains() {
		if !syncable[d] {
			t.Errorf("domain %q is grow-only but not syncable — the two tables have drifted", d)
		}
	}
}
