package crdt

import "sort"

// GridCRDT is a Last-Writer-Wins map keyed by (row, col), used for
// Sheets (OFFICE-23). Each cell stores the value plus the OpID of the
// write; merge keeps the value with the greater OpID (Lamport-stamp,
// tiebreak by replica). Tombstones (Deleted=true) preserve idempotency
// for deletions concurrent with writes.
//
// LWW is the right choice for cells because spreadsheet semantics are
// "the last edit wins" - no character-level merging needed. Concurrent
// edits resolve deterministically and identically on every replica.
type GridCRDT struct {
	cells map[CellKey]*gridCell
}

// CellKey is the (row, col) addressing used by sheets. row/col are
// zero-indexed.
type CellKey struct {
	Row int `json:"r"`
	Col int `json:"c"`
}

type gridCell struct {
	id      OpID
	value   string
	deleted bool
}

// NewGridCRDT returns an empty grid.
func NewGridCRDT() *GridCRDT {
	return &GridCRDT{cells: map[CellKey]*gridCell{}}
}

// GridOpKind is the union of mutating grid ops.
type GridOpKind uint8

const (
	GridOpSet   GridOpKind = 1
	GridOpClear GridOpKind = 2
)

// GridOp is a single cell mutation.
type GridOp struct {
	Kind  GridOpKind `json:"k"`
	ID    OpID       `json:"id"`
	Key   CellKey    `json:"key"`
	Value string     `json:"v,omitempty"`
}

// Apply applies op with LWW semantics, idempotently. The op with the
// greater OpID wins; equal OpIDs collapse (re-delivery is a no-op).
func (g *GridCRDT) Apply(op GridOp) {
	if existing, ok := g.cells[op.Key]; ok {
		// Drop if existing is newer or identical.
		if existing.id.Equal(op.ID) || op.ID.Less(existing.id) {
			return
		}
	}
	switch op.Kind {
	case GridOpSet:
		g.cells[op.Key] = &gridCell{id: op.ID, value: op.Value}
	case GridOpClear:
		g.cells[op.Key] = &gridCell{id: op.ID, deleted: true}
	}
}

// Get returns the value at key and whether the cell exists (and is
// not a tombstone).
func (g *GridCRDT) Get(key CellKey) (string, bool) {
	c, ok := g.cells[key]
	if !ok || c.deleted {
		return "", false
	}
	return c.value, true
}

// Set returns a GridOp that writes value to key.
func (g *GridCRDT) Set(key CellKey, value string, id OpID) GridOp {
	return GridOp{Kind: GridOpSet, ID: id, Key: key, Value: value}
}

// Clear returns a GridOp that tombstones key.
func (g *GridCRDT) Clear(key CellKey, id OpID) GridOp {
	return GridOp{Kind: GridOpClear, ID: id, Key: key}
}

// Keys returns the populated, non-deleted cell keys in row-major order.
func (g *GridCRDT) Keys() []CellKey {
	out := make([]CellKey, 0, len(g.cells))
	for k, c := range g.cells {
		if !c.deleted {
			out = append(out, k)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Row != out[j].Row {
			return out[i].Row < out[j].Row
		}
		return out[i].Col < out[j].Col
	})
	return out
}

type gridSnapshot struct {
	Cells []gridCellSnapshot `json:"cells"`
}

type gridCellSnapshot struct {
	Key     CellKey `json:"k"`
	ID      OpID    `json:"id"`
	Value   string  `json:"v,omitempty"`
	Deleted bool    `json:"d,omitempty"`
}

func (g *GridCRDT) snapshot() gridSnapshot {
	out := gridSnapshot{Cells: make([]gridCellSnapshot, 0, len(g.cells))}
	for k, c := range g.cells {
		out.Cells = append(out.Cells, gridCellSnapshot{
			Key: k, ID: c.id, Value: c.value, Deleted: c.deleted,
		})
	}
	sort.Slice(out.Cells, func(i, j int) bool {
		a, b := out.Cells[i].Key, out.Cells[j].Key
		if a.Row != b.Row {
			return a.Row < b.Row
		}
		return a.Col < b.Col
	})
	return out
}

func (g *GridCRDT) restore(s gridSnapshot) {
	g.cells = map[CellKey]*gridCell{}
	for _, cs := range s.Cells {
		g.cells[cs.Key] = &gridCell{id: cs.ID, value: cs.Value, deleted: cs.Deleted}
	}
}
