package crdt

import (
	"context"
	"sync"
)

// LoopbackHub is an in-process N-peer "fabric" used by tests and by
// the dev server. Each peer gets a Transport bound to the hub; Send
// fans frames out to every other peer's Recv channel. It satisfies
// the Transport contract OFFICE-20 will eventually realize over
// WebRTC + relay, without this package depending on the real
// implementation.
type LoopbackHub struct {
	mu      sync.Mutex
	session string
	peers   map[ReplicaID]*loopbackPeer
}

// NewLoopbackHub constructs a hub for the given session id.
func NewLoopbackHub(session string) *LoopbackHub {
	return &LoopbackHub{session: session, peers: map[ReplicaID]*loopbackPeer{}}
}

// Join registers replicaID with the hub and returns its Transport.
// Recv buffer is sized at 256 frames.
func (h *LoopbackHub) Join(replicaID ReplicaID) Transport {
	h.mu.Lock()
	defer h.mu.Unlock()
	p := &loopbackPeer{
		hub:     h,
		replica: replicaID,
		inbox:   make(chan Frame, 256),
		done:    make(chan struct{}),
	}
	h.peers[replicaID] = p
	return p
}

func (h *LoopbackHub) broadcast(from ReplicaID, frame Frame) {
	h.mu.Lock()
	dests := make([]*loopbackPeer, 0, len(h.peers))
	for r, p := range h.peers {
		if r == from {
			continue
		}
		dests = append(dests, p)
	}
	h.mu.Unlock()
	for _, p := range dests {
		select {
		case p.inbox <- frame:
		case <-p.done:
		}
	}
}

type loopbackPeer struct {
	hub     *LoopbackHub
	replica ReplicaID
	inbox   chan Frame
	done    chan struct{}
	closeMu sync.Mutex
	closed  bool
}

func (p *loopbackPeer) SessionID() string         { return p.hub.session }
func (p *loopbackPeer) LocalReplicaID() ReplicaID { return p.replica }

func (p *loopbackPeer) Send(ctx context.Context, frame Frame) error {
	frame.Session = p.hub.session
	frame.From = p.replica
	p.hub.broadcast(p.replica, frame)
	return nil
}

func (p *loopbackPeer) Recv(ctx context.Context) (Frame, error) {
	select {
	case f := <-p.inbox:
		return f, nil
	case <-ctx.Done():
		return Frame{}, ctx.Err()
	case <-p.done:
		return Frame{}, ErrTransportClosed
	}
}

func (p *loopbackPeer) Close() error {
	p.closeMu.Lock()
	defer p.closeMu.Unlock()
	if p.closed {
		return nil
	}
	p.closed = true
	close(p.done)
	return nil
}
