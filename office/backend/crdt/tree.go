package crdt

import "sort"

// TreeCRDT is an ordered-tree CRDT used for Slides (OFFICE-23). Each
// node has a stable id (OpID), a parent id, and an LWW "ordKey" used
// to position siblings. Move/reorder is an LWW write of the parent +
// ordKey. Deletion is a tombstone.
//
// Cycle prevention: a move that would create a cycle is dropped (the
// node keeps its previous parent). Cycles can only arise from
// concurrent moves; the loser is detected on Apply because we walk
// parent chains before accepting.
//
// Content of each slide is stored as a separate value string with its
// own LWW stamp. (For richer slide bodies the consumer can store a
// nested TextCRDT id; out of scope for this slice.)
type TreeCRDT struct {
	nodes map[OpID]*treeNode
	root  OpID
}

type treeNode struct {
	id      OpID
	parent  OpID
	ordKey  string // sortable position key, LWW
	ordID   OpID   // OpID of the last move/insert that set parent+ordKey
	value   string
	valueID OpID
	deleted bool
}

// NewTreeCRDT returns a tree with an implicit root (zero OpID).
func NewTreeCRDT() *TreeCRDT {
	return &TreeCRDT{nodes: map[OpID]*treeNode{}, root: OpID{}}
}

// TreeOpKind enumerates tree mutations.
type TreeOpKind uint8

const (
	TreeOpInsert  TreeOpKind = 1
	TreeOpMove    TreeOpKind = 2
	TreeOpSetText TreeOpKind = 3
	TreeOpDelete  TreeOpKind = 4
)

// TreeOp is a single tree mutation. OrdKey is a string so callers can
// use fractional-indexing schemes (e.g. "M", "MM", "MN") without this
// package prescribing one.
type TreeOp struct {
	Kind   TreeOpKind `json:"k"`
	ID     OpID       `json:"id"`
	Target OpID       `json:"t,omitempty"`
	Parent OpID       `json:"p,omitempty"`
	OrdKey string     `json:"o,omitempty"`
	Value  string     `json:"v,omitempty"`
}

// Apply applies op idempotently with LWW semantics on parent/ordKey
// and on value.
func (t *TreeCRDT) Apply(op TreeOp) {
	switch op.Kind {
	case TreeOpInsert:
		n, ok := t.nodes[op.ID]
		if ok {
			// A placeholder may have been created by an early-arriving
			// SetText/Move/Delete. Fill in parent/ordKey from this
			// Insert if the existing entry has no positioning op (or
			// has one that loses LWW to op.ID).
			if n.ordID.Equal(OpID{}) || op.ID.Less(n.ordID) == false {
				if !n.ordID.Equal(op.ID) {
					n.parent = op.Parent
					n.ordKey = op.OrdKey
					n.ordID = op.ID
				}
			}
			return
		}
		t.nodes[op.ID] = &treeNode{
			id:     op.ID,
			parent: op.Parent,
			ordKey: op.OrdKey,
			ordID:  op.ID,
		}
	case TreeOpMove:
		n, ok := t.nodes[op.Target]
		if !ok {
			// Buffer-via-tombstone so a late insert of Target merges
			// correctly. The insert won't overwrite because it bails
			// when id already exists; we'll just adopt parent/ordKey
			// from this move on insert as a small refinement:
			t.nodes[op.Target] = &treeNode{
				id: op.Target, parent: op.Parent, ordKey: op.OrdKey, ordID: op.ID,
			}
			return
		}
		// LWW: keep current if its ordID >= op.ID.
		if n.ordID.Equal(op.ID) || op.ID.Less(n.ordID) {
			return
		}
		if t.wouldCycle(op.Target, op.Parent) {
			return
		}
		n.parent = op.Parent
		n.ordKey = op.OrdKey
		n.ordID = op.ID
	case TreeOpSetText:
		n, ok := t.nodes[op.Target]
		if !ok {
			n = &treeNode{id: op.Target}
			t.nodes[op.Target] = n
		}
		if n.valueID.Equal(op.ID) || op.ID.Less(n.valueID) {
			return
		}
		n.value = op.Value
		n.valueID = op.ID
	case TreeOpDelete:
		n, ok := t.nodes[op.Target]
		if !ok {
			t.nodes[op.Target] = &treeNode{id: op.Target, deleted: true}
			return
		}
		n.deleted = true
	}
}

// wouldCycle reports whether re-parenting node under newParent would
// produce a cycle. The walk is bounded by the current node count to
// stay finite even if the tree is temporarily inconsistent (e.g. mid
// concurrent move arriving at the same node from two sides).
func (t *TreeCRDT) wouldCycle(node, newParent OpID) bool {
	cur := newParent
	limit := len(t.nodes) + 1
	for !cur.Equal(OpID{}) && limit > 0 {
		if cur.Equal(node) {
			return true
		}
		n, ok := t.nodes[cur]
		if !ok {
			return false
		}
		cur = n.parent
		limit--
	}
	// If we ran out of iterations we are in a transient cycle - treat
	// as "would cycle" so this move is dropped.
	return limit <= 0
}

// Children returns the non-deleted children of parent, sorted by
// (ordKey, OpID) for a stable order.
func (t *TreeCRDT) Children(parent OpID) []OpID {
	var out []OpID
	for id, n := range t.nodes {
		if !n.deleted && n.parent.Equal(parent) {
			out = append(out, id)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		ni, nj := t.nodes[out[i]], t.nodes[out[j]]
		if ni.ordKey != nj.ordKey {
			return ni.ordKey < nj.ordKey
		}
		return out[i].Less(out[j])
	})
	return out
}

// Value returns the text value of node id (LWW).
func (t *TreeCRDT) Value(id OpID) (string, bool) {
	n, ok := t.nodes[id]
	if !ok || n.deleted {
		return "", false
	}
	return n.value, true
}

// Order returns a depth-first traversal of visible nodes starting at
// root, which for slides yields the rendered deck order.
func (t *TreeCRDT) Order() []OpID {
	var out []OpID
	t.walkOrder(t.root, &out)
	return out
}

func (t *TreeCRDT) walkOrder(parent OpID, out *[]OpID) {
	for _, c := range t.Children(parent) {
		*out = append(*out, c)
		t.walkOrder(c, out)
	}
}

type treeSnapshot struct {
	Nodes []treeNodeSnapshot `json:"nodes"`
}

type treeNodeSnapshot struct {
	ID      OpID   `json:"id"`
	Parent  OpID   `json:"p"`
	OrdKey  string `json:"o,omitempty"`
	OrdID   OpID   `json:"oi"`
	Value   string `json:"v,omitempty"`
	ValueID OpID   `json:"vi"`
	Deleted bool   `json:"d,omitempty"`
}

func (t *TreeCRDT) snapshot() treeSnapshot {
	out := treeSnapshot{Nodes: make([]treeNodeSnapshot, 0, len(t.nodes))}
	for _, n := range t.nodes {
		out.Nodes = append(out.Nodes, treeNodeSnapshot{
			ID: n.id, Parent: n.parent, OrdKey: n.ordKey, OrdID: n.ordID,
			Value: n.value, ValueID: n.valueID, Deleted: n.deleted,
		})
	}
	sort.Slice(out.Nodes, func(i, j int) bool {
		return out.Nodes[i].ID.Less(out.Nodes[j].ID)
	})
	return out
}

func (t *TreeCRDT) restore(s treeSnapshot) {
	t.nodes = map[OpID]*treeNode{}
	for _, n := range s.Nodes {
		t.nodes[n.ID] = &treeNode{
			id: n.ID, parent: n.Parent, ordKey: n.OrdKey, ordID: n.OrdID,
			value: n.Value, valueID: n.ValueID, deleted: n.Deleted,
		}
	}
}
