package crdt

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
)

// DocKind enumerates the three document types this package supports.
type DocKind string

const (
	DocKindText DocKind = "text"
	DocKindGrid DocKind = "grid"
	DocKindTree DocKind = "tree"
)

// Op is the over-the-wire envelope around the kind-specific op body.
// Doc serializes Op to bytes and ships it via the Transport.
type Op struct {
	Kind DocKind         `json:"kind"`
	ID   OpID            `json:"id"`
	Body json.RawMessage `json:"body"`
}

// Doc is the document-level facade that ties one CRDT (text, grid, or
// tree) to a Transport and a Bucket. It is what call-sites for
// OFFICE-22 (Docs), OFFICE-23 (Sheets/Slides), and OFFICE-29 (Spaces)
// will instantiate.
//
// Thread-safety: all public methods take an internal mutex. Op apply
// is O(log n) for grid, O(n) for text/tree walk on read.
type Doc struct {
	mu      sync.Mutex
	kind    DocKind
	session string
	replica ReplicaID
	clock   *LamportClock
	vc      VectorClock      // high-water-mark per replica (for snapshots/acks)
	seen    map[OpID]struct{} // exact set of observed OpIDs (de-dup)

	text *TextCRDT
	grid *GridCRDT
	tree *TreeCRDT

	log []Op // op-log retained for the bucket / late joiners

	// pending holds ops whose causal parent (text-only) hasn't arrived
	// yet. Grid + tree are LWW and always safe to apply on arrival.
	pending []Op
}

// NewDoc constructs an empty document of the given kind.
func NewDoc(kind DocKind, session string, replica ReplicaID) *Doc {
	d := &Doc{
		kind:    kind,
		session: session,
		replica: replica,
		clock:   NewLamportClock(replica),
		vc:      VectorClock{},
		seen:    map[OpID]struct{}{},
	}
	switch kind {
	case DocKindText:
		d.text = NewTextCRDT()
	case DocKindGrid:
		d.grid = NewGridCRDT()
	case DocKindTree:
		d.tree = NewTreeCRDT()
	}
	return d
}

// Kind returns the document's CRDT kind.
func (d *Doc) Kind() DocKind { return d.kind }

// SessionID returns the session id.
func (d *Doc) SessionID() string { return d.session }

// Replica returns the local replica id.
func (d *Doc) Replica() ReplicaID { return d.replica }

// Text returns the underlying text CRDT (nil if Kind != DocKindText).
func (d *Doc) Text() *TextCRDT { return d.text }

// Grid returns the underlying grid CRDT (nil if Kind != DocKindGrid).
func (d *Doc) Grid() *GridCRDT { return d.grid }

// Tree returns the underlying tree CRDT (nil if Kind != DocKindTree).
func (d *Doc) Tree() *TreeCRDT { return d.tree }

// NextOpID stamps and returns a fresh OpID for a local op.
func (d *Doc) NextOpID() OpID {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.clock.Tick()
}

// ApplyLocal applies a locally-produced op + appends it to the log.
// Returns the encoded Op envelope for broadcast.
func (d *Doc) ApplyLocal(body any, id OpID) (Op, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	bs, err := json.Marshal(body)
	if err != nil {
		return Op{}, err
	}
	op := Op{Kind: d.kind, ID: id, Body: bs}
	d.applyLocked(op)
	d.log = append(d.log, op)
	d.seen[id] = struct{}{}
	d.vc.Observe(id)
	return op, nil
}

// ApplyRemote applies a received op (idempotent). Causally-blocked ops
// are queued and re-tried on every subsequent ApplyRemote call.
func (d *Doc) ApplyRemote(op Op) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.seen[op.ID]; ok {
		return nil
	}
	if d.kind == DocKindText && !d.textCauseReady(op) {
		// Don't double-queue if a copy of this op is already pending.
		for _, p := range d.pending {
			if p.ID.Equal(op.ID) {
				return nil
			}
		}
		d.pending = append(d.pending, op)
		return nil
	}
	d.applyLocked(op)
	d.log = append(d.log, op)
	d.seen[op.ID] = struct{}{}
	d.clock.Observe(op.ID.Counter)
	d.vc.Observe(op.ID)
	d.drainPending()
	return nil
}

// textCauseReady reports whether op's causal prerequisites are
// already present in the text CRDT. For inserts that's the parent
// anchor; for deletes it's the target node. The zero OpID (root) is
// always ready.
func (d *Doc) textCauseReady(op Op) bool {
	var tb TextOp
	if err := json.Unmarshal(op.Body, &tb); err != nil {
		return false
	}
	switch tb.Kind {
	case TextOpInsert:
		if tb.Parent.Equal(OpID{}) {
			return true
		}
		_, ok := d.text.nodes[tb.Parent]
		return ok
	case TextOpDelete:
		_, ok := d.text.nodes[tb.Target]
		return ok
	}
	return true
}

func (d *Doc) drainPending() {
	for {
		moved := false
		remaining := make([]Op, 0, len(d.pending))
		for _, op := range d.pending {
			if d.textCauseReady(op) {
				d.applyLocked(op)
				d.log = append(d.log, op)
				d.seen[op.ID] = struct{}{}
				d.vc.Observe(op.ID)
				moved = true
			} else {
				remaining = append(remaining, op)
			}
		}
		d.pending = remaining
		if !moved {
			return
		}
	}
}

func (d *Doc) applyLocked(op Op) {
	switch op.Kind {
	case DocKindText:
		var tb TextOp
		if err := json.Unmarshal(op.Body, &tb); err == nil {
			d.text.Apply(tb)
		}
	case DocKindGrid:
		var gb GridOp
		if err := json.Unmarshal(op.Body, &gb); err == nil {
			d.grid.Apply(gb)
		}
	case DocKindTree:
		var tb TreeOp
		if err := json.Unmarshal(op.Body, &tb); err == nil {
			d.tree.Apply(tb)
		}
	}
}

// Log returns a copy of the op-log.
func (d *Doc) Log() []Op {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]Op, len(d.log))
	copy(out, d.log)
	return out
}

// Snapshot exports a serialized cold-path bootstrap blob (snapshot of
// the CRDT state + retained op-log).
func (d *Doc) Snapshot() (Snapshot, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	s := Snapshot{Kind: d.kind, Session: d.session, Clock: d.vc.Clone(), Log: append([]Op(nil), d.log...)}
	switch d.kind {
	case DocKindText:
		s.Text = d.text.snapshot()
	case DocKindGrid:
		s.Grid = d.grid.snapshot()
	case DocKindTree:
		s.Tree = d.tree.snapshot()
	}
	return s, nil
}

// Restore replaces this Doc's state from a snapshot blob.
func (d *Doc) Restore(s Snapshot) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if s.Kind != d.kind {
		return fmt.Errorf("snapshot kind %q != doc kind %q", s.Kind, d.kind)
	}
	d.vc = s.Clock.Clone()
	d.log = append([]Op(nil), s.Log...)
	d.seen = make(map[OpID]struct{}, len(d.log))
	for _, op := range d.log {
		d.seen[op.ID] = struct{}{}
	}
	switch d.kind {
	case DocKindText:
		d.text = NewTextCRDT()
		d.text.restore(s.Text)
	case DocKindGrid:
		d.grid = NewGridCRDT()
		d.grid.restore(s.Grid)
	case DocKindTree:
		d.tree = NewTreeCRDT()
		d.tree.restore(s.Tree)
	}
	// Re-seed clock to the max counter we have observed locally so
	// future local ops stay causally greater.
	var maxC uint64
	for _, c := range d.vc {
		if c > maxC {
			maxC = c
		}
	}
	d.clock = &LamportClock{Replica: d.replica, C: maxC}
	return nil
}

// Run starts hot-path send/recv loops bound to the given Transport
// and Bucket. It returns when ctx is cancelled or the transport
// closes. Run is the integration point OFFICE-20/22/23 wire to:
// hand it a Transport (fabric adapter) and a Bucket (cold store) and
// the doc converges automatically.
func (d *Doc) Run(ctx context.Context, tr Transport, b Bucket) error {
	if tr == nil {
		return errors.New("crdt: nil transport")
	}
	// Cold-join bootstrap: if the bucket has a snapshot for this
	// session, load it first so we don't replay history from scratch.
	if b != nil {
		if snap, ok, err := b.LoadSnapshot(ctx, d.session); err == nil && ok {
			_ = d.Restore(snap)
		}
	}
	// Recv loop.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			frame, err := tr.Recv(ctx)
			if err != nil {
				return
			}
			d.handleFrame(ctx, frame, tr, b)
		}
	}()
	<-done
	return nil
}

func (d *Doc) handleFrame(ctx context.Context, frame Frame, tr Transport, b Bucket) {
	switch frame.Kind {
	case FrameOp:
		var op Op
		if err := json.Unmarshal(frame.Payload, &op); err == nil {
			_ = d.ApplyRemote(op)
		}
	case FrameSnapshotRequest:
		snap, err := d.Snapshot()
		if err != nil {
			return
		}
		bs, err := json.Marshal(snap)
		if err != nil {
			return
		}
		_ = tr.Send(ctx, Frame{
			Kind:    FrameSnapshot,
			Session: d.session,
			From:    d.replica,
			Payload: bs,
		})
	case FrameSnapshot:
		var snap Snapshot
		if err := json.Unmarshal(frame.Payload, &snap); err == nil {
			_ = d.Restore(snap)
		}
	}
	// Persist to the cold-store bucket periodically (every received op).
	if b != nil {
		snap, err := d.Snapshot()
		if err == nil {
			_ = b.SaveSnapshot(ctx, d.session, snap)
		}
	}
}

// Broadcast sends op over tr and persists to b.
func (d *Doc) Broadcast(ctx context.Context, op Op, tr Transport, b Bucket) error {
	if tr != nil {
		bs, err := json.Marshal(op)
		if err != nil {
			return err
		}
		if err := tr.Send(ctx, Frame{
			Kind:    FrameOp,
			Session: d.session,
			From:    d.replica,
			Payload: bs,
		}); err != nil {
			return err
		}
	}
	if b != nil {
		if err := b.AppendOp(ctx, d.session, op); err != nil {
			return err
		}
	}
	return nil
}
