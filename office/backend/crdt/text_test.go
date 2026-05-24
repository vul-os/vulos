package crdt

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"testing"
)

// TestTextConvergence_RandomInterleaving applies a randomly-interleaved
// stream of ops produced by N replicas to every replica and asserts
// that every replica's String() is identical. This is the headline
// convergence proof for OFFICE-21's text CRDT.
func TestTextConvergence_RandomInterleaving(t *testing.T) {
	const numReplicas = 4
	const opsPerReplica = 80
	seeds := []int64{1, 42, 1337, 9001}

	for _, seed := range seeds {
		t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) {
			rng := rand.New(rand.NewSource(seed))
			docs := make([]*Doc, numReplicas)
			for i := range docs {
				docs[i] = NewDoc(DocKindText, "s", ReplicaID(fmt.Sprintf("r%d", i)))
			}
			// Each replica produces a list of local ops independently
			// (so they're concurrent w.r.t. each other replica).
			perReplicaOps := make([][]Op, numReplicas)
			for i, d := range docs {
				for k := 0; k < opsPerReplica; k++ {
					vis := d.Text().VisibleIDs()
					id := d.NextOpID()
					var op Op
					var err error
					// 70% inserts, 30% deletes (when there's something to delete)
					if rng.Intn(10) < 7 || len(vis) == 0 {
						pos := 0
						if len(vis) > 0 {
							pos = rng.Intn(len(vis) + 1)
						}
						r := rune('a' + rng.Intn(26))
						tb := d.Text().LocalInsert(pos, r, id)
						op, err = d.ApplyLocal(tb, id)
					} else {
						pos := rng.Intn(len(vis))
						tb, ok := d.Text().LocalDelete(pos, id)
						if !ok {
							continue
						}
						op, err = d.ApplyLocal(tb, id)
					}
					if err != nil {
						t.Fatal(err)
					}
					perReplicaOps[i] = append(perReplicaOps[i], op)
				}
			}
			// Build a globally shuffled merged stream and apply it to
			// every replica that didn't originate it.
			type tagged struct {
				origin int
				op     Op
			}
			var all []tagged
			for i, ops := range perReplicaOps {
				for _, op := range ops {
					all = append(all, tagged{origin: i, op: op})
				}
			}
			// Many random shuffles - each replica gets ops in a
			// different order, which is exactly the convergence test.
			for i, d := range docs {
				shuffled := make([]tagged, len(all))
				copy(shuffled, all)
				rng.Shuffle(len(shuffled), func(a, b int) {
					shuffled[a], shuffled[b] = shuffled[b], shuffled[a]
				})
				for _, t := range shuffled {
					if t.origin == i {
						continue
					}
					if err := d.ApplyRemote(t.op); err != nil {
						// non-fatal: ApplyRemote queues causally-blocked ops
					}
				}
			}
			// Now every doc must agree.
			want := docs[0].Text().String()
			for i := 1; i < numReplicas; i++ {
				got := docs[i].Text().String()
				if got != want {
					t.Fatalf("replica %d diverged.\n want=%q\n  got=%q", i, want, got)
				}
			}
			if want == "" {
				t.Fatalf("expected non-empty converged string")
			}
		})
	}
}

// TestTextIdempotent re-applies the same op twice and asserts state
// is unchanged.
func TestTextIdempotent(t *testing.T) {
	d := NewDoc(DocKindText, "s", "r1")
	id := d.NextOpID()
	tb := d.Text().LocalInsert(0, 'A', id)
	if _, err := d.ApplyLocal(tb, id); err != nil {
		t.Fatal(err)
	}
	// Wrap in an Op envelope and re-apply via ApplyRemote.
	body, _ := json.Marshal(tb)
	if err := d.ApplyRemote(Op{Kind: DocKindText, ID: id, Body: body}); err != nil {
		t.Fatal(err)
	}
	if got := d.Text().String(); got != "A" {
		t.Fatalf("idempotent insert produced %q, want %q", got, "A")
	}
}

// TestTextOfflineReconcile simulates two replicas editing offline and
// then exchanging ops; final state must match.
func TestTextOfflineReconcile(t *testing.T) {
	a := NewDoc(DocKindText, "s", "A")
	b := NewDoc(DocKindText, "s", "B")
	// Seed shared prefix "Hi" so both replicas agree on causal anchors.
	prefix := func(d *Doc) []Op {
		var ops []Op
		for _, r := range "Hi" {
			id := d.NextOpID()
			tb := d.Text().LocalInsert(len(d.Text().VisibleIDs()), r, id)
			op, _ := d.ApplyLocal(tb, id)
			ops = append(ops, op)
		}
		return ops
	}
	seed := prefix(a)
	for _, op := range seed {
		if err := b.ApplyRemote(op); err != nil {
			t.Fatal(err)
		}
	}
	// Diverge offline.
	var aops, bops []Op
	for _, r := range "!!!" {
		id := a.NextOpID()
		tb := a.Text().LocalInsert(len(a.Text().VisibleIDs()), r, id)
		op, _ := a.ApplyLocal(tb, id)
		aops = append(aops, op)
	}
	for _, r := range "???" {
		id := b.NextOpID()
		tb := b.Text().LocalInsert(0, r, id)
		op, _ := b.ApplyLocal(tb, id)
		bops = append(bops, op)
	}
	// Reconnect: cross-apply.
	for _, op := range aops {
		if err := b.ApplyRemote(op); err != nil {
			t.Fatal(err)
		}
	}
	for _, op := range bops {
		if err := a.ApplyRemote(op); err != nil {
			t.Fatal(err)
		}
	}
	if a.Text().String() != b.Text().String() {
		t.Fatalf("offline reconcile diverged: a=%q b=%q",
			a.Text().String(), b.Text().String())
	}
}

// TestTextLoopbackHubConvergence drives convergence through the
// Transport abstraction (LoopbackHub) end-to-end.
func TestTextLoopbackHubConvergence(t *testing.T) {
	hub := NewLoopbackHub("s")
	a := NewDoc(DocKindText, "s", "A")
	b := NewDoc(DocKindText, "s", "B")
	at := hub.Join("A")
	bt := hub.Join("B")
	defer at.Close()
	defer bt.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = a.Run(ctx, at, nil) }()
	go func() { _ = b.Run(ctx, bt, nil) }()

	// A types "hello".
	for _, r := range "hello" {
		id := a.NextOpID()
		tb := a.Text().LocalInsert(len(a.Text().VisibleIDs()), r, id)
		op, _ := a.ApplyLocal(tb, id)
		if err := a.Broadcast(ctx, op, at, nil); err != nil {
			t.Fatal(err)
		}
	}
	// Wait for delivery.
	waitFor(t, func() bool { return b.Text().String() == "hello" })

	if a.Text().String() != b.Text().String() {
		t.Fatalf("hub convergence failed: a=%q b=%q", a.Text().String(), b.Text().String())
	}
}
