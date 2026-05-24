package crdt

import (
	"sort"
	"strings"
)

// TextCRDT is an RGA-flavoured sequence CRDT used for Docs (OFFICE-22).
//
// Each visible character is a node addressed by an OpID. An insert op
// places a new node "after" an existing parent OpID; concurrent inserts
// with the same parent are ordered deterministically by their OpID
// (Counter desc, then Replica desc) so every replica converges to the
// identical visible string.
//
// Deletes are tombstones: the node stays in the structure (so other
// inserts that referenced it still anchor correctly) but is hidden from
// the rendered text. This guarantees the structure is monotone, hence
// merge is commutative + idempotent.
type TextCRDT struct {
	nodes  map[OpID]*textNode
	root   OpID // sentinel ("zero" OpID) is the start anchor
	// childIndex maps parent OpID -> sorted child OpIDs (descending OpID).
	childIndex map[OpID][]OpID
}

type textNode struct {
	id      OpID
	parent  OpID // OpID this node was inserted *after*
	value   rune
	deleted bool
}

// NewTextCRDT returns an empty text document.
func NewTextCRDT() *TextCRDT {
	return &TextCRDT{
		nodes:      map[OpID]*textNode{},
		root:       OpID{},
		childIndex: map[OpID][]OpID{},
	}
}

// TextOpKind is the union of mutating ops on a text CRDT.
type TextOpKind uint8

const (
	TextOpInsert TextOpKind = 1
	TextOpDelete TextOpKind = 2
)

// TextOp is a single text mutation. Serialized into Op.Body when sent
// over a Transport.
type TextOp struct {
	Kind   TextOpKind `json:"k"`
	ID     OpID       `json:"id"`
	Parent OpID       `json:"p,omitempty"` // for Insert: anchor; for Delete: target id is in Target
	Value  rune       `json:"v,omitempty"`
	Target OpID       `json:"t,omitempty"` // for Delete
}

// Apply applies op idempotently. It is safe to apply the same op twice
// or in any order with respect to other ops. The only requirement is
// that an insert's Parent has been observed before the insert is
// applied (causal delivery); the Document layer enforces this by
// buffering ops with missing causes.
func (t *TextCRDT) Apply(op TextOp) {
	switch op.Kind {
	case TextOpInsert:
		if _, ok := t.nodes[op.ID]; ok {
			return // idempotent
		}
		t.nodes[op.ID] = &textNode{
			id:     op.ID,
			parent: op.Parent,
			value:  op.Value,
		}
		t.insertChild(op.Parent, op.ID)
	case TextOpDelete:
		// Doc layer (Doc.textCauseReady) buffers deletes whose target
		// isn't observed yet, so by the time we get here Target should
		// exist. Tolerate the rare case where it doesn't.
		n, ok := t.nodes[op.Target]
		if !ok {
			return
		}
		n.deleted = true
	}
}

// insertChild inserts cid into parent's child list, kept sorted in
// descending OpID order (so a left-to-right depth-first walk produces
// the correct RGA visible order).
func (t *TextCRDT) insertChild(parent, cid OpID) {
	children := t.childIndex[parent]
	idx := sort.Search(len(children), func(i int) bool {
		// descending: we want the position where children[i] < cid
		return children[i].Less(cid)
	})
	children = append(children, OpID{})
	copy(children[idx+1:], children[idx:])
	children[idx] = cid
	t.childIndex[parent] = children
}

// String renders the visible string. Stable across replicas.
func (t *TextCRDT) String() string {
	var b strings.Builder
	t.walk(t.root, &b)
	return b.String()
}

func (t *TextCRDT) walk(parent OpID, b *strings.Builder) {
	for _, cid := range t.childIndex[parent] {
		n := t.nodes[cid]
		if n != nil && !n.deleted && n.value != 0 {
			b.WriteRune(n.value)
		}
		t.walk(cid, b)
	}
}

// VisibleIDs returns the visible character OpIDs in document order.
// Useful for resolving a UI cursor index to a stable parent anchor.
func (t *TextCRDT) VisibleIDs() []OpID {
	var out []OpID
	t.collect(t.root, &out)
	return out
}

func (t *TextCRDT) collect(parent OpID, out *[]OpID) {
	for _, cid := range t.childIndex[parent] {
		n := t.nodes[cid]
		if n != nil && !n.deleted && n.value != 0 {
			*out = append(*out, cid)
		}
		t.collect(cid, out)
	}
}

// LocalInsert is a helper that returns the TextOp for inserting `r`
// after visible index `i` (0 = beginning). Caller is responsible for
// stamping the OpID via the document's Lamport clock.
func (t *TextCRDT) LocalInsert(i int, r rune, id OpID) TextOp {
	parent := t.root
	if i > 0 {
		vis := t.VisibleIDs()
		if i-1 < len(vis) {
			parent = vis[i-1]
		} else if len(vis) > 0 {
			parent = vis[len(vis)-1]
		}
	}
	return TextOp{Kind: TextOpInsert, ID: id, Parent: parent, Value: r}
}

// LocalDelete returns the TextOp for deleting the character at visible
// index `i`.
func (t *TextCRDT) LocalDelete(i int, id OpID) (TextOp, bool) {
	vis := t.VisibleIDs()
	if i < 0 || i >= len(vis) {
		return TextOp{}, false
	}
	return TextOp{Kind: TextOpDelete, ID: id, Target: vis[i]}, true
}

// snapshot exports the CRDT state for bucket persistence.
type textSnapshot struct {
	Nodes []textNodeSnapshot `json:"nodes"`
}

type textNodeSnapshot struct {
	ID      OpID `json:"id"`
	Parent  OpID `json:"p"`
	Value   rune `json:"v"`
	Deleted bool `json:"d,omitempty"`
}

func (t *TextCRDT) snapshot() textSnapshot {
	out := textSnapshot{Nodes: make([]textNodeSnapshot, 0, len(t.nodes))}
	for _, n := range t.nodes {
		out.Nodes = append(out.Nodes, textNodeSnapshot{
			ID: n.id, Parent: n.parent, Value: n.value, Deleted: n.deleted,
		})
	}
	// Deterministic ordering for stable hashing of snapshots.
	sort.Slice(out.Nodes, func(i, j int) bool {
		return out.Nodes[i].ID.Less(out.Nodes[j].ID)
	})
	return out
}

func (t *TextCRDT) restore(s textSnapshot) {
	t.nodes = map[OpID]*textNode{}
	t.childIndex = map[OpID][]OpID{}
	// Apply parent-first so child lists are built consistently. We
	// approximate by sorting by OpID ascending; since Counter only
	// grows, parents arrive before children.
	sort.Slice(s.Nodes, func(i, j int) bool { return s.Nodes[i].ID.Less(s.Nodes[j].ID) })
	for _, n := range s.Nodes {
		t.nodes[n.ID] = &textNode{
			id: n.ID, parent: n.Parent, value: n.Value, deleted: n.Deleted,
		}
		t.insertChild(n.Parent, n.ID)
	}
}
