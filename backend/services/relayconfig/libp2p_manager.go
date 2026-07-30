// no-broker-dep:allow-file: comment states 'the box's out-of-the-box default (DefaultConfig() is
// ephor)' -- THIS IS STALE relative to the current code: relayconfig.go's
// DefaultConfig() literally returns Provider: vulos. Reported separately
// as a stale-comment finding, not fixed here; no actual dependency or
// behavioural default on Ephor exists (C-DEP go clean, 899 entries).

package relayconfig

// libp2p_manager.go owns the ONE optional, real go-libp2p host this box may
// run: a Circuit Relay v2 CLIENT that reaches the operator-configured relay
// peers (ProviderLibp2p's Libp2pProviderConfig.RelayPeers, see
// relayconfig.go). It is the sole place that decides whether that host
// exists at all — providers.go's libp2pProvider never talks to
// newLibp2pReachabilityHost directly, only through ensureLibp2pManager here.
//
// # OFF BY DEFAULT — TWO independent gates, BOTH required
//
//  1. relayconfig's Provider must be ProviderLibp2p. Reaching this state
//     already requires an owner/admin authenticated + step-up-gated Set()
//     call through the HTTP layer (see backend/cmd/server/routes_relayconfig.go
//     and relayconfig.go's package doc) — Settings' "libp2p" choice is never
//     the box's out-of-the-box default (DefaultConfig() is ephor).
//  2. The env var VULOS_LIBP2P_HOST_ENABLE=1 must ALSO be set in the box's
//     OWN process environment (libp2p_env.go).
//
// Gate #2 exists so that a relayconfig.json saying "libp2p" — restored from
// a backup, hand-edited, or written before this feature existed to embed a
// real host — can NEVER by itself start a live libp2p stack. Flipping an env
// var is something only whoever controls the box's process/systemd unit/
// container can do; neither the persisted config file nor the HTTP Settings
// API can reach it. Combined, this means: a fresh box, or a box whose owner
// only ever used Settings (never touched the box's env), has ZERO libp2p
// goroutines, sockets, or allocated resource-manager/connmgr state — this
// file's ensureLibp2pManager returns immediately on the `!want` branch below
// without ever calling buildResourceManager/buildConnManager/libp2p.New.
//
// # Fail-closed on misconfig
//
// If both gates are satisfied but the host fails to construct safely (e.g. a
// resource-manager/connmgr build error), ensureLibp2pManager logs and leaves
// the manager stopped/disabled — it NEVER falls back to a host without the
// fixed limits, and it never panics or crashes the box. A short backoff
// (libp2pRetryBackoff) avoids retry-storming a misconfigured environment on
// every single status read.
import (
	"context"
	"log"
	"sync"
	"time"
)

// libp2pRetryBackoff bounds how often ensureLibp2pManager will retry
// constructing a host after a failure, so a permanently-broken environment
// (e.g. no usable transport) doesn't retry on every single Ingress()/
// ResolvePeer() read.
const libp2pRetryBackoff = 30 * time.Second

var (
	lp2pMgrMu          sync.Mutex
	lp2pMgrHost        *libp2pReachabilityHost // nil unless a real host is running
	lp2pMgrPeers       []string                // relay peer set the running host was built for
	lp2pMgrLastErr     error                   // last construction failure, if any
	lp2pMgrLastAttempt time.Time               // when that failure happened
)

// Libp2pHostStatus is a safe-to-display (no secrets) snapshot of the
// optional embedded libp2p host's live state.
type Libp2pHostStatus struct {
	// Running is true iff a real go-libp2p host is currently constructed and
	// active (both gates satisfied and construction succeeded).
	Running bool
	// PeerID is this box's own libp2p peer ID when Running.
	PeerID string
	// NumPeers is how many relay peer multiaddrs are configured (regardless
	// of Running — mirrors the existing report-only Ingress() detail).
	NumPeers int
	// LastError carries the most recent construction failure, if the host
	// is not Running because of one (fail-closed — see package doc).
	LastError string
	// HostEnvEnabled reports whether VULOS_LIBP2P_HOST_ENABLE is set,
	// independent of whether the provider is even selected — lets a status
	// surface explain WHY a configured libp2p provider isn't live yet.
	HostEnvEnabled bool
}

// Reconcile re-evaluates the live host against the CURRENT persisted
// relayconfig state and starts/stops/rebuilds it as needed. It is idempotent
// and safe to call at any time (from any goroutine). providers.go's
// libp2pProvider calls this on every Ingress()/ResolvePeer() read, so the
// host self-heals within moments of any Set() change even without an
// explicit boot-time call. It is ALSO safe (and a purely optional latency
// optimization, never required for correctness) for cmd/server's startup
// sequence to call this once right after relayconfig.Init() for an
// immediate-at-boot start instead of waiting on the first status read — see
// the integration manifest delivered alongside this package.
func Reconcile() Libp2pHostStatus {
	return ensureLibp2pManager(currentConfig())
}

// ensureLibp2pManager is Reconcile's actual implementation, taking an
// explicit cfg so callers that already hold a Config value (providers.go)
// don't need a second currentConfig() lock round-trip.
func ensureLibp2pManager(cfg Config) Libp2pHostStatus {
	envEnabled := libp2pHostEnabledByEnv()

	lp2pMgrMu.Lock()
	defer lp2pMgrMu.Unlock()

	want := cfg.Provider == ProviderLibp2p && envEnabled && len(cfg.Libp2p.RelayPeers) > 0

	if !want {
		stopLocked()
		lp2pMgrLastErr = nil
		return statusLocked(cfg, envEnabled)
	}

	if lp2pMgrHost != nil {
		if samePeerSet(lp2pMgrPeers, cfg.Libp2p.RelayPeers) {
			// Already running for exactly this peer set — nothing to do.
			return statusLocked(cfg, envEnabled)
		}
		// Peer set changed under us (operator edited the relay list): tear
		// down the stale host before rebuilding for the new set.
		stopLocked()
	}

	if lp2pMgrLastErr != nil && time.Since(lp2pMgrLastAttempt) < libp2pRetryBackoff {
		// Still backing off from a recent failed start attempt.
		return statusLocked(cfg, envEnabled)
	}

	lp2pMgrLastAttempt = time.Now()
	h, err := newLibp2pReachabilityHost(context.Background(), cfg.Libp2p.RelayPeers)
	if err != nil {
		// FAIL CLOSED: log and stay disabled. Never fall back to an
		// unlimited/default host, never crash the box.
		log.Printf("[relayconfig] libp2p host failed to start safely, staying disabled: %v", err)
		lp2pMgrLastErr = err
		return statusLocked(cfg, envEnabled)
	}
	lp2pMgrLastErr = nil
	lp2pMgrHost = h
	lp2pMgrPeers = append([]string(nil), cfg.Libp2p.RelayPeers...)
	log.Printf("[relayconfig] libp2p Circuit Relay v2 client host started (peer_id=%s, %d relay peer(s))", h.PeerID(), len(cfg.Libp2p.RelayPeers))
	return statusLocked(cfg, envEnabled)
}

// stopLocked closes any running host. Caller must hold lp2pMgrMu.
func stopLocked() {
	if lp2pMgrHost != nil {
		if err := lp2pMgrHost.Close(); err != nil {
			log.Printf("[relayconfig] libp2p host close error: %v", err)
		}
		lp2pMgrHost = nil
		lp2pMgrPeers = nil
	}
}

// statusLocked builds the status snapshot. Caller must hold lp2pMgrMu.
func statusLocked(cfg Config, envEnabled bool) Libp2pHostStatus {
	st := Libp2pHostStatus{
		NumPeers:       len(cfg.Libp2p.RelayPeers),
		HostEnvEnabled: envEnabled,
	}
	if lp2pMgrHost != nil {
		st.Running = true
		st.PeerID = lp2pMgrHost.PeerID()
	}
	if lp2pMgrLastErr != nil {
		st.LastError = lp2pMgrLastErr.Error()
	}
	return st
}

// samePeerSet reports whether a and b list the same relay peers in the same
// order (order changes are treated as a change worth rebuilding for — cheap
// and correct; relay peer lists are capped at maxRelayPeers=16 so this never
// costs anything meaningful).
func samePeerSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// resetLibp2pManagerForTest tears down any running host and clears all
// manager state. TEST-ONLY (unexported, package-internal) — mirrors
// relayconfig_test.go's resetState pattern so libp2p tests never leak state
// into each other or into unrelated relayconfig tests.
func resetLibp2pManagerForTest() {
	lp2pMgrMu.Lock()
	defer lp2pMgrMu.Unlock()
	stopLocked()
	lp2pMgrLastErr = nil
	lp2pMgrLastAttempt = time.Time{}
}
