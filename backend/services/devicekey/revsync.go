// revsync.go — fleet-wide device-key REVOCATION PULL: a self-contained
// periodic loop that asks every known peer for its
// GET /api/auth/device/revocations list (propagation.go / routes_devicekey_
// lifecycle.go) and merges what verifies into the local RevocationStore via
// MergeRevocationBatch.
//
// # Why a pull loop, and why it is safe
//
// A DeviceRevocationCert is SELF-VERIFYING (see revocation.go's Verify /
// propagation.go's package doc): correctness never depends on transport
// trust, only on this box's OWN roster for break-glass entries. That means
// the propagation problem reduces to "ask a peer what it knows, verify every
// item yourself, merge what verifies" — the exact idiom internal/fabric's
// Service.Run/SyncOnce and internal/multiinstance's CloudSyncer.Sync already
// use for CRDT rows and cloud-instance lists. RevSyncer applies that same
// shape here: ticker-driven Run(ctx), an immediate first round, per-peer
// failures logged and skipped (one bad peer must never stall convergence
// with the rest), and a Stop() to halt it.
//
// # Deliberately decoupled
//
// This file imports nothing from internal/fabric or internal/multiinstance
// and does not touch their wire formats — it takes a PeerSource function
// instead of any concrete discovery/registry type, so it stays a
// self-contained struct inside package devicekey that the orchestrator wires
// up from cmd/server (see the integration manifest) without this package
// ever depending on fabric/multiinstance internals.
package devicekey

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"vulos/backend/services/fleetid"
)

// DefaultRevSyncInterval is the default cadence of the background revocation
// pull loop — deliberately slower than internal/fabric's 30s CRDT sync
// (revocations are rare, security-critical events, not routine app state; a
// few minutes of propagation lag is an acceptable tradeoff against
// hammering every peer's HTTP endpoint on every tick).
const DefaultRevSyncInterval = 5 * time.Minute

// revSyncHTTPTimeout bounds a single peer GET so one slow/unreachable peer
// cannot stall an entire pull round.
const revSyncHTTPTimeout = 10 * time.Second

// PeerSource reports the current set of fleet peer base URLs (e.g.
// "https://10.0.0.4:7443") to pull revocations from. Implementations are
// expected to exclude this box itself (mirrors internal/fabric.Service's
// isSelf skip). A nil slice or an error degrades a round to a no-op — never
// a panic — so RevSyncer can be wired up before fleet peering is even
// configured.
type PeerSource func(ctx context.Context) ([]string, error)

// RosterSource resolves the fleetid.Roster + quorum threshold each pull
// round needs to independently re-verify any break-glass entries in a peer's
// batch (MergeRevocationBatch → DeviceRevocationCert.Verify). A nil
// RosterSource (or one returning a nil roster) is fine: RevocationMethodSelf
// entries verify against nothing but their own embedded signature regardless
// of roster, and RevocationMethodBreakGlass entries simply fail closed (are
// skipped, never merged) until a real roster is available — exactly
// MergeRevocationBatch's existing per-entry fail-closed behavior.
type RosterSource func() (roster fleetid.Roster, threshold int)

// revSyncPeerStatus is the last-round result tracked per peer, purely for
// observability (mirrors internal/fabric's peerStatus).
type revSyncPeerStatus struct {
	lastAttemptAt time.Time
	lastErr       string // "" on success
	lastMerged    int
}

// RevSyncer periodically pulls GET {peer}/api/auth/device/revocations from
// every peer reported by Peers, verifies and merges every entry into Store
// (MergeRevocationBatch already re-verifies each cert fail-closed — self
// certs against their own signature, break-glass certs against THIS box's
// own Roster — so a malicious or stale peer can never inject an unverifiable
// revocation), and logs per-peer failures without letting one bad peer stall
// the rest.
//
// Safe for concurrent use. Construct with NewRevSyncer, start the background
// loop with Run (blocks until ctx is cancelled or Stop is called).
type RevSyncer struct {
	// Store is the local revocation store entries are merged into. Required.
	Store *RevocationStore
	// Peers reports the current peer set to pull from each round. Required.
	Peers PeerSource
	// Roster resolves the roster/threshold used to verify break-glass
	// entries. Optional — nil means "no roster yet" every round.
	Roster RosterSource
	// Interval is the pull cadence. <= 0 uses DefaultRevSyncInterval.
	Interval time.Duration
	// Client is the HTTP client used to reach peers. Defaults to a client
	// with revSyncHTTPTimeout if left nil at NewRevSyncer time.
	Client *http.Client
	// Now returns the current time, used for cert Verify's `now` argument.
	// Overridable in tests; defaults to time.Now().UTC.
	Now func() time.Time

	mu       sync.Mutex
	status   map[string]revSyncPeerStatus
	stopOnce sync.Once
	stopCh   chan struct{}
}

// NewRevSyncer constructs a RevSyncer. store and peers must be non-nil (Run
// / PullOnce become safe no-ops otherwise, but a caller should not rely on
// that). roster may be nil. interval <= 0 uses DefaultRevSyncInterval.
func NewRevSyncer(store *RevocationStore, peers PeerSource, roster RosterSource, interval time.Duration) *RevSyncer {
	if interval <= 0 {
		interval = DefaultRevSyncInterval
	}
	return &RevSyncer{
		Store:    store,
		Peers:    peers,
		Roster:   roster,
		Interval: interval,
		Client:   &http.Client{Timeout: revSyncHTTPTimeout},
		Now:      func() time.Time { return time.Now().UTC() },
		status:   make(map[string]revSyncPeerStatus),
		stopCh:   make(chan struct{}),
	}
}

// Run starts the background pull loop and blocks until ctx is cancelled or
// Stop is called. It runs an immediate first round so a freshly-booted or
// freshly-quorum-revoked fleet converges quickly, then repeats every
// Interval. Safe to call at most once per RevSyncer (mirrors
// internal/fabric.Service.Run: stop by cancelling ctx or calling Stop).
func (rs *RevSyncer) Run(ctx context.Context) {
	if rs == nil || rs.Store == nil || rs.Peers == nil {
		return // unwired — nothing to do, never panic
	}
	log.Printf("[devicekey] revocation pull loop starting (interval=%s)", rs.Interval)
	t := time.NewTicker(rs.Interval)
	defer t.Stop()

	rs.PullOnce(ctx)
	for {
		select {
		case <-ctx.Done():
			log.Printf("[devicekey] revocation pull loop stopped (context done)")
			return
		case <-rs.stopCh:
			log.Printf("[devicekey] revocation pull loop stopped")
			return
		case <-t.C:
			rs.PullOnce(ctx)
		}
	}
}

// Stop halts a running Run loop. Safe to call multiple times, concurrently,
// and even if Run was never started.
func (rs *RevSyncer) Stop() {
	if rs == nil {
		return
	}
	rs.stopOnce.Do(func() { close(rs.stopCh) })
}

// PullOnce runs a single pull-and-merge round against every peer currently
// reported by Peers. Per-peer network/decode/verify failures are logged and
// skipped — one unreachable or malformed peer must never stall convergence
// with the rest. Exported so callers can trigger an out-of-band round (e.g.
// right after learning of a fresh peer, or in tests) without waiting for the
// ticker.
func (rs *RevSyncer) PullOnce(ctx context.Context) {
	if rs == nil || rs.Store == nil || rs.Peers == nil {
		return
	}
	peers, err := rs.Peers(ctx)
	if err != nil {
		log.Printf("[devicekey] revocation pull: peer discovery failed: %v", err)
		return
	}

	var (
		roster    fleetid.Roster
		threshold int
	)
	if rs.Roster != nil {
		roster, threshold = rs.Roster()
	}

	now := time.Now().UTC()
	if rs.Now != nil {
		now = rs.Now()
	}

	client := rs.Client
	if client == nil {
		client = &http.Client{Timeout: revSyncHTTPTimeout}
	}

	for _, peer := range peers {
		peer = strings.TrimSpace(peer)
		if peer == "" {
			continue
		}
		merged, err := rs.pullPeer(ctx, client, peer, roster, threshold, now)
		rs.recordResult(peer, merged, err)
		if err != nil {
			log.Printf("[devicekey] revocation pull from %s failed: %v", peer, err)
			continue
		}
		if merged > 0 {
			log.Printf("[devicekey] revocation pull from %s merged %d new revocation(s)", peer, merged)
		}
	}
}

// pullPeer performs one GET {peer}/api/auth/device/revocations round-trip
// and merges the result. Returns the number of NEWLY recorded revocations.
func (rs *RevSyncer) pullPeer(ctx context.Context, client *http.Client, peerBaseURL string, roster fleetid.Roster, threshold int, now time.Time) (int, error) {
	url := strings.TrimRight(peerBaseURL, "/") + "/api/auth/device/revocations"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, fmt.Errorf("build request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("GET %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return 0, fmt.Errorf("GET %s: status %d: %s", url, resp.StatusCode, body)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20)) // 4MiB cap: revocation lists are small
	if err != nil {
		return 0, fmt.Errorf("read body: %w", err)
	}
	batch, err := UnmarshalRevocationBatch(data)
	if err != nil {
		return 0, err
	}

	merged, mergeErrs := MergeRevocationBatch(rs.Store, batch, roster, threshold, now)
	for _, e := range mergeErrs {
		// Skipped, not fatal to the round: one malformed/unverifiable entry
		// (possibly a break-glass cert this box's roster can't yet verify)
		// must not block the rest of the batch or the rest of the peers.
		log.Printf("[devicekey] revocation pull from %s: skipped entry: %v", peerBaseURL, e)
	}
	return merged, nil
}

// recordResult stamps the last-attempt time/error/merge-count for a peer
// (observability; mirrors internal/fabric.Service.recordStatus).
func (rs *RevSyncer) recordResult(peer string, merged int, err error) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	st := revSyncPeerStatus{lastAttemptAt: time.Now().UTC(), lastMerged: merged}
	if err != nil {
		st.lastErr = err.Error()
	}
	rs.status[peer] = st
}

// Status returns a snapshot of the most recent pull result for every peer
// this RevSyncer has attempted, keyed by peer base URL. Exposed for an
// operator-facing status endpoint the orchestrator may wire up (mirrors
// internal/fabric's /api/fabric/status); not required for correctness.
func (rs *RevSyncer) Status() map[string]RevSyncPeerStatus {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	out := make(map[string]RevSyncPeerStatus, len(rs.status))
	for peer, st := range rs.status {
		out[peer] = RevSyncPeerStatus{
			LastAttemptAt: st.lastAttemptAt,
			LastErr:       st.lastErr,
			LastMerged:    st.lastMerged,
		}
	}
	return out
}

// RevSyncPeerStatus is the exported, JSON-friendly shape of a peer's last
// pull result (see RevSyncer.Status).
type RevSyncPeerStatus struct {
	LastAttemptAt time.Time `json:"last_attempt_at"`
	LastErr       string    `json:"last_err,omitempty"`
	LastMerged    int       `json:"last_merged"`
}
