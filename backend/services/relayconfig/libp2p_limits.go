package relayconfig

// libp2p_limits.go — the conservative, FIXED (never auto-scaling to the
// host's real memory/FD ulimits) resource caps applied to the one optional
// go-libp2p host this box may run (see libp2p_manager.go). This box embeds
// libp2p purely to reach a handful of operator-configured Circuit Relay v2
// peers (validateRelayPeers in validate.go already caps that list at
// maxRelayPeers=16) — it is NEVER a general-purpose p2p node serving
// arbitrary third parties, so there is no reason for it to approach
// go-libp2p's own, much larger, auto-scaled defaults. These constants and
// the two builders below are pure/hermetic — they allocate no sockets and
// make no network calls, so they are safe to exercise directly in unit
// tests without a real libp2p host.

import (
	"time"

	rcmgr "github.com/libp2p/go-libp2p/p2p/host/resource-manager"
	"github.com/libp2p/go-libp2p/p2p/net/connmgr"

	libp2pnetwork "github.com/libp2p/go-libp2p/core/network"
)

const (
	// libp2pConnsLowWater / libp2pConnsHighWater feed the connection
	// manager: once the live connection count reaches the high watermark it
	// proactively trims idle connections back down toward the low
	// watermark, well before the resource manager's hard ceiling
	// (libp2pMaxConns) below is ever reached. This is the FIRST line of
	// defence — graceful trimming, not resource denial.
	libp2pConnsLowWater  = 16
	libp2pConnsHighWater = 64
	// libp2pConnGracePeriod protects a just-opened connection from being
	// trimmed before it has had a chance to do anything useful (e.g.
	// negotiate a relay reservation).
	libp2pConnGracePeriod = 20 * time.Second

	// libp2pMaxConns is the resource manager's HARD ceiling — a bound the
	// connection manager's trimming should never let the box actually reach
	// in normal operation, but the resource manager enforces it
	// unconditionally regardless of what the connection manager does. Kept
	// above libp2pConnsHighWater so trimming (not a rejected dial) is the
	// normal path to staying under it.
	libp2pMaxConns = 96
	// libp2pMaxStreamsPerConn bounds how many logical streams a single
	// connection may multiplex (Circuit Relay v2 reservations/relays only
	// ever need a small handful of streams per peer).
	libp2pMaxStreamsPerConn = 32
	libp2pMaxStreams        = libp2pMaxConns * libp2pMaxStreamsPerConn
	// libp2pMaxMemoryBytes is a small, fixed memory ceiling for the whole
	// libp2p stack (buffers, stream state, etc.) — 64 MiB is generous for a
	// client dialing <=16 relay peers, tiny compared to what an unbounded
	// default host could claim on a box with lots of RAM.
	libp2pMaxMemoryBytes = 64 << 20 // 64 MiB
	// libp2pMaxFD bounds file descriptors (sockets) the libp2p stack may
	// hold open at once.
	libp2pMaxFD = 128
)

// libp2pResourceLimits returns the PartialLimitConfig applying the fixed caps
// above to BOTH the System (whole-process) and Transient (not-yet-fully-
// negotiated connections) scopes — Transient is capped identically so an
// attacker (or a misbehaving relay) can't exhaust resources purely via
// half-open/negotiating connections before they even count against System.
func libp2pResourceLimits() rcmgr.PartialLimitConfig {
	rl := rcmgr.ResourceLimits{
		Conns:           rcmgr.LimitVal(libp2pMaxConns),
		ConnsInbound:    rcmgr.LimitVal(libp2pMaxConns),
		ConnsOutbound:   rcmgr.LimitVal(libp2pMaxConns),
		Streams:         rcmgr.LimitVal(libp2pMaxStreams),
		StreamsInbound:  rcmgr.LimitVal(libp2pMaxStreams),
		StreamsOutbound: rcmgr.LimitVal(libp2pMaxStreams),
		Memory:          rcmgr.LimitVal64(libp2pMaxMemoryBytes),
		FD:              rcmgr.LimitVal(libp2pMaxFD),
	}
	return rcmgr.PartialLimitConfig{System: rl, Transient: rl}
}

// buildResourceManager constructs a FIXED libp2p resource manager (never
// go-libp2p's AutoScale()-to-real-ulimits default) capped at
// libp2pResourceLimits(). Fixed, small, and predictable regardless of the
// box's actual RAM/FD ulimits is the entire point — see the package doc.
// AutoScale() is only consulted here as the base to fill in any field this
// package does NOT explicitly override (there are none at the System/
// Transient scope above, but every other scope — services, protocols,
// per-peer — legitimately still wants go-libp2p's own sane defaults rather
// than this package reinventing all of them).
func buildResourceManager() (libp2pnetwork.ResourceManager, error) {
	concrete := libp2pResourceLimits().Build(rcmgr.DefaultLimits.AutoScale())
	limiter := rcmgr.NewFixedLimiter(concrete)
	return rcmgr.NewResourceManager(limiter)
}

// buildConnManager returns the low/high-watermark connection manager
// described above.
func buildConnManager() (*connmgr.BasicConnMgr, error) {
	return connmgr.NewConnManager(libp2pConnsLowWater, libp2pConnsHighWater, connmgr.WithGracePeriod(libp2pConnGracePeriod))
}
