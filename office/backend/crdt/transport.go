// Package crdt implements the CRDT document core + bucket sync layer
// for OFFICE-21. It defines three CRDT models (text/grid/tree), an
// op-log + snapshot persistence layer ("bucket"), and a transport
// abstraction so OFFICE-20's fabric adapter (WebRTC + relay fallback)
// or any other peer transport can plug in without this package
// depending on the concrete implementation.
//
// Design goals:
//   - Pure Go, no CGO, no external deps beyond the stdlib.
//   - Merge is commutative, associative, and idempotent.
//   - Offline edits converge on reconnect (op replay).
//   - Cold/late joiners reconstruct from snapshot + tail of op-log.
package crdt

import (
	"context"
	"errors"
)

// Transport is the abstract peer-to-peer message channel. OFFICE-20's
// fabric client adapter (WebRTC data channel + Vulos relay fallback)
// will satisfy this interface, but this package never imports it -
// the document core depends only on the abstract Transport so it can
// be exercised in-process (tests) or driven by any wire format.
type Transport interface {
	// SessionID returns the document/session this transport is bound to.
	SessionID() string
	// LocalReplicaID returns the replica id assigned to this peer.
	LocalReplicaID() ReplicaID
	// Send broadcasts a frame to all peers in the session.
	Send(ctx context.Context, frame Frame) error
	// Recv blocks until the next frame arrives or ctx is cancelled.
	Recv(ctx context.Context) (Frame, error)
	// Close releases transport resources.
	Close() error
}

// FrameKind identifies the wire-frame type.
type FrameKind uint8

const (
	// FrameOp carries a single CRDT op (hot path).
	FrameOp FrameKind = 1
	// FrameSnapshotRequest asks peers for the latest snapshot
	// (cold-joiner bootstrap).
	FrameSnapshotRequest FrameKind = 2
	// FrameSnapshot carries a serialized snapshot + op-log tail.
	FrameSnapshot FrameKind = 3
	// FrameAck carries a vector-clock ack so peers can prune
	// already-delivered ops.
	FrameAck FrameKind = 4
)

// Frame is the unit of transport payload. The transport layer is
// content-agnostic; framing format is JSON for the reference
// implementation but the abstraction allows any encoder.
type Frame struct {
	Kind    FrameKind `json:"kind"`
	Session string    `json:"session"`
	From    ReplicaID `json:"from"`
	// Payload is one of: encoded Op, encoded Snapshot, or VectorClock
	// depending on Kind. Encoded with the package's Codec.
	Payload []byte `json:"payload"`
}

// ErrTransportClosed is returned by Recv after Close.
var ErrTransportClosed = errors.New("crdt: transport closed")
