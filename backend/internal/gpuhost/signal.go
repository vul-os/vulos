// signal.go — P2P-preferred / relay-fallback decision logic.
//
// The fabric is used for *signaling*; media MUST go peer-to-peer whenever
// possible. The VULOS-STREAM/1 sub-protocol exposes a TURN slot
// only after the requester emits an explicit msgP2PFailed signal, served by
// the host's peering backend.
//
// PathChooser owns the box-side decision: it runs a configurable P2P probe
// (typically a STUN/ICE handshake attempt against the candidate addresses);
// if the probe succeeds within P2PProbeTimeout, the chosen path is
// PathP2P; otherwise it is PathTURN. The chosen path is also reported via
// Decisions() so tests and metrics can assert the ratio.

package gpuhost

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

// Path is the media-transport decision the chooser returned for a session.
type Path string

const (
	// PathP2P means the box and client successfully negotiated direct P2P
	// media (no relay bytes).
	PathP2P Path = "p2p"
	// PathTURN means P2P failed and the session falls back to relay TURN.
	PathTURN Path = "turn"
)

// P2PProbe is the seam tests use to inject a P2P-availability check. In
// production it is a STUN/ICE handshake attempt; in tests it is a stub.
// A nil error from Probe means P2P is viable for this session.
type P2PProbe interface {
	Probe(ctx context.Context, session string) error
}

// ProbeFunc adapts a function to P2PProbe.
type ProbeFunc func(ctx context.Context, session string) error

// Probe satisfies P2PProbe.
func (f ProbeFunc) Probe(ctx context.Context, session string) error { return f(ctx, session) }

// PathChooser decides P2P vs TURN for each incoming session. Safe for
// concurrent use.
type PathChooser struct {
	timeout time.Duration

	mu      sync.Mutex
	probe   P2PProbe
	p2pCnt  atomic.Uint64
	turnCnt atomic.Uint64
}

// NewPathChooser builds a chooser whose probes time out after timeout
// (defaults to 5s if zero).
func NewPathChooser(timeout time.Duration) *PathChooser {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	return &PathChooser{timeout: timeout}
}

// SetProbe installs the P2P-probe seam. Must be called before Choose, or
// every Choose will return PathTURN (the safe default — never claim P2P
// you haven't proven).
func (c *PathChooser) SetProbe(p P2PProbe) {
	c.mu.Lock()
	c.probe = p
	c.mu.Unlock()
}

// Choose decides the path for session. It runs the probe with a deadline
// of timeout; success → PathP2P, any error or timeout → PathTURN. Both
// counters are bumped accordingly.
//
// Choose never returns an error: the chooser's whole job is to give the
// streamer a fall-back path when P2P fails. The caller passes the chosen
// path back to the relay via SendP2PFailed when PathTURN.
func (c *PathChooser) Choose(ctx context.Context, session string) Path {
	c.mu.Lock()
	probe := c.probe
	timeout := c.timeout
	c.mu.Unlock()

	if probe == nil {
		c.turnCnt.Add(1)
		return PathTURN
	}

	pctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := probe.Probe(pctx, session); err != nil {
		c.turnCnt.Add(1)
		return PathTURN
	}
	c.p2pCnt.Add(1)
	return PathP2P
}

// Decisions returns the running counts of (p2p, turn) decisions.
func (c *PathChooser) Decisions() (p2p uint64, turn uint64) {
	return c.p2pCnt.Load(), c.turnCnt.Load()
}

// ErrP2PFailed is the canonical error returned by P2P probes to mean "P2P
// not viable for this session — fall back". Probes MAY return any error
// (Choose only cares about nil-vs-not), but using ErrP2PFailed makes the
// intent explicit at the call site.
var ErrP2PFailed = errors.New("gpuhost: P2P negotiation failed")
