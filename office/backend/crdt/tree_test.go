package crdt

import (
	"fmt"
	"math/rand"
	"testing"
)

// TestTreeMoveDeterministic verifies that concurrent move ops with
// disjoint targets (the common slide-reorder case) converge.
func TestTreeMoveDeterministic(t *testing.T) {
	a := NewDoc(DocKindTree, "s", "A")
	b := NewDoc(DocKindTree, "s", "B")
	// Seed three slides on A and ship to B.
	var seeds []Op
	for k := 0; k < 3; k++ {
		id := a.NextOpID()
		op, _ := a.ApplyLocal(TreeOp{
			Kind: TreeOpInsert, ID: id, Parent: OpID{},
			OrdKey: fmt.Sprintf("M%d", k), Value: fmt.Sprintf("slide-%d", k),
		}, id)
		seeds = append(seeds, op)
	}
	for _, op := range seeds {
		_ = b.ApplyRemote(op)
	}
	order := a.Tree().Order()
	if len(order) != 3 {
		t.Fatalf("seed length: %d", len(order))
	}
	// A moves slide[0] after slide[2]; B concurrently moves slide[2] to OrdKey "Z" (after everything).
	idA := a.NextOpID()
	opA, _ := a.ApplyLocal(TreeOp{Kind: TreeOpMove, ID: idA, Target: order[0],
		Parent: OpID{}, OrdKey: "ZZ"}, idA)
	idB := b.NextOpID()
	opB, _ := b.ApplyLocal(TreeOp{Kind: TreeOpMove, ID: idB, Target: order[2],
		Parent: OpID{}, OrdKey: "Z"}, idB)
	_ = a.ApplyRemote(opB)
	_ = b.ApplyRemote(opA)
	oa := a.Tree().Order()
	ob := b.Tree().Order()
	if len(oa) != len(ob) {
		t.Fatalf("len diverged: a=%d b=%d", len(oa), len(ob))
	}
	for i := range oa {
		if !oa[i].Equal(ob[i]) {
			t.Fatalf("order[%d] diverged: a=%v b=%v", i, oa[i], ob[i])
		}
	}
}

// TestTreeConvergence_RandomInterleaving exercises insert/setText
// /delete ops on N replicas and asserts every replica's depth-first
// order + value map converges.
func TestTreeConvergence_RandomInterleaving(t *testing.T) {
	const numReplicas = 3
	const opsPerReplica = 50

	for _, seed := range []int64{2, 11, 999} {
		t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) {
			rng := rand.New(rand.NewSource(seed))
			docs := make([]*Doc, numReplicas)
			for i := range docs {
				docs[i] = NewDoc(DocKindTree, "s", ReplicaID(fmt.Sprintf("r%d", i)))
			}
			// Seed shared root children so move ops have targets.
			var seedOps []Op
			d0 := docs[0]
			for k := 0; k < 4; k++ {
				id := d0.NextOpID()
				op, _ := d0.ApplyLocal(TreeOp{
					Kind: TreeOpInsert, ID: id, Parent: OpID{},
					OrdKey: fmt.Sprintf("M%d", k), Value: fmt.Sprintf("slide-%d", k),
				}, id)
				seedOps = append(seedOps, op)
			}
			for i := 1; i < numReplicas; i++ {
				for _, op := range seedOps {
					if err := docs[i].ApplyRemote(op); err != nil {
						t.Fatal(err)
					}
				}
			}

			type tagged struct {
				origin int
				op     Op
			}
			// Exercise Insert / SetText / Delete on each replica. Move
			// is excluded from the random stream because concurrent
			// cyclic moves require a full Kleppmann-style undo log
			// (not part of this slice). Move is covered in
			// TestTreeMoveDeterministic below.
			all := make([]tagged, 0, numReplicas*opsPerReplica)
			for i, d := range docs {
				known := d.Tree().Order()
				for k := 0; k < opsPerReplica; k++ {
					if len(known) == 0 {
						break
					}
					target := known[rng.Intn(len(known))]
					id := d.NextOpID()
					var body TreeOp
					switch rng.Intn(3) {
					case 0:
						body = TreeOp{Kind: TreeOpInsert, ID: id, Parent: target,
							OrdKey: fmt.Sprintf("M%d-%d", i, k), Value: fmt.Sprintf("ins-%s", id)}
					case 1:
						body = TreeOp{Kind: TreeOpSetText, ID: id, Target: target,
							Value: fmt.Sprintf("v-%s", id)}
					case 2:
						if rng.Intn(5) == 0 {
							body = TreeOp{Kind: TreeOpDelete, ID: id, Target: target}
						} else {
							body = TreeOp{Kind: TreeOpSetText, ID: id, Target: target,
								Value: fmt.Sprintf("v2-%s", id)}
						}
					}
					op, err := d.ApplyLocal(body, id)
					if err != nil {
						t.Fatal(err)
					}
					all = append(all, tagged{origin: i, op: op})
					known = d.Tree().Order()
				}
			}
			// Cross-apply with shuffled ordering.
			for i, d := range docs {
				shuffled := make([]tagged, len(all))
				copy(shuffled, all)
				rng.Shuffle(len(shuffled), func(a, b int) {
					shuffled[a], shuffled[b] = shuffled[b], shuffled[a]
				})
				for _, tg := range shuffled {
					if tg.origin == i {
						continue
					}
					if err := d.ApplyRemote(tg.op); err != nil {
						t.Fatal(err)
					}
				}
			}
			// Compare ordered traversal of every replica.
			ref := docs[0].Tree().Order()
			for i := 1; i < numReplicas; i++ {
				got := docs[i].Tree().Order()
				if len(got) != len(ref) {
					t.Fatalf("replica %d order length diverged: %d vs %d", i, len(got), len(ref))
				}
				for j, id := range ref {
					if !got[j].Equal(id) {
						t.Fatalf("replica %d order[%d] diverged: %v vs %v", i, j, got[j], id)
					}
				}
				// Values must match too.
				for _, id := range ref {
					vr, _ := docs[0].Tree().Value(id)
					vg, _ := docs[i].Tree().Value(id)
					if vr != vg {
						t.Fatalf("replica %d value for %v diverged: %q vs %q", i, id, vg, vr)
					}
				}
			}
		})
	}
}
