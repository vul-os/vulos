package crdt

import (
	"fmt"
	"math/rand"
	"testing"
)

// TestGridConvergence_RandomInterleaving applies a random op stream
// from N replicas to every replica in a random order and asserts all
// replicas observe the same final cell map. LWW means the op with the
// greatest (Counter, Replica) for a given cell wins.
func TestGridConvergence_RandomInterleaving(t *testing.T) {
	const numReplicas = 3
	const opsPerReplica = 60
	const gridSize = 8

	for _, seed := range []int64{1, 7, 1234} {
		t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) {
			rng := rand.New(rand.NewSource(seed))
			docs := make([]*Doc, numReplicas)
			for i := range docs {
				docs[i] = NewDoc(DocKindGrid, "s", ReplicaID(fmt.Sprintf("r%d", i)))
			}
			type tagged struct {
				origin int
				op     Op
			}
			var all []tagged
			for i, d := range docs {
				for k := 0; k < opsPerReplica; k++ {
					id := d.NextOpID()
					key := CellKey{Row: rng.Intn(gridSize), Col: rng.Intn(gridSize)}
					var gb GridOp
					if rng.Intn(5) == 0 {
						gb = d.Grid().Clear(key, id)
					} else {
						gb = d.Grid().Set(key, fmt.Sprintf("r%d-%d", i, k), id)
					}
					op, err := d.ApplyLocal(gb, id)
					if err != nil {
						t.Fatal(err)
					}
					all = append(all, tagged{origin: i, op: op})
				}
			}
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
			// Compare every replica's full cell state.
			ref := snapshotMap(docs[0].Grid())
			for i := 1; i < numReplicas; i++ {
				got := snapshotMap(docs[i].Grid())
				if len(got) != len(ref) {
					t.Fatalf("replica %d size diverged: got %d ref %d", i, len(got), len(ref))
				}
				for k, v := range ref {
					if got[k] != v {
						t.Fatalf("replica %d cell %v diverged: got %q want %q", i, k, got[k], v)
					}
				}
			}
		})
	}
}

func snapshotMap(g *GridCRDT) map[CellKey]string {
	out := map[CellKey]string{}
	for _, k := range g.Keys() {
		v, ok := g.Get(k)
		if !ok {
			continue
		}
		out[k] = v
	}
	return out
}

// TestGridLWWTiebreaker confirms that concurrent writes resolve via
// (Counter, Replica) tiebreak deterministically.
func TestGridLWWTiebreaker(t *testing.T) {
	a := NewDoc(DocKindGrid, "s", "A")
	b := NewDoc(DocKindGrid, "s", "B")
	key := CellKey{Row: 0, Col: 0}

	// Both pick counter=1 -> tiebreak by replica id. "B" > "A" so B wins.
	idA := a.NextOpID()
	idB := b.NextOpID()
	opA, _ := a.ApplyLocal(a.Grid().Set(key, "from-A", idA), idA)
	opB, _ := b.ApplyLocal(b.Grid().Set(key, "from-B", idB), idB)

	if err := a.ApplyRemote(opB); err != nil {
		t.Fatal(err)
	}
	if err := b.ApplyRemote(opA); err != nil {
		t.Fatal(err)
	}
	va, _ := a.Grid().Get(key)
	vb, _ := b.Grid().Get(key)
	if va != vb {
		t.Fatalf("LWW diverged: a=%q b=%q", va, vb)
	}
	if va != "from-B" {
		t.Fatalf("expected B (higher replica id) to win, got %q", va)
	}
}
