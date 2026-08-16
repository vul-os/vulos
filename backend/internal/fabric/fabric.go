// Package fabric implements FABRIC-P2P-01: real same-LAN peer-to-peer CRDT
// sync between sibling Vulos instances, with NO dependency on the cloud
// control-plane, S3/Tigris, or the internet.
//
// This is the OFFLINE same-LAN path. Two (or more) Vulos boxes on the same
// LAN discover each other over mDNS, then exchange app-registry changesets
// directly over each other's LAN HTTPS listener. The merge itself reuses the
// hardened CRDT primitive in internal/multiinstance (AppSync.ApplyChangeset —
// LWW + OR-set + writer-node tie-break + uninstall quorum), so convergence is
// deterministic regardless of message order or which peer applies first.
//
// Architecture
//
//	┌──────────────┐   mDNS _vulos-fabric.local    ┌──────────────┐
//	│   box A      │ ◀───────── discover ────────▶ │   box B      │
//	│              │                               │              │
//	│  AppSync ────┼── GET /api/fabric/changeset ─▶ │── AppSync    │
//	│  (LWW merge) │◀─ POST /api/fabric/changeset ──┤  (LWW merge) │
//	└──────────────┘   (X-Fabric-Auth shared secret)└──────────────┘
//
// Three pieces:
//
//   - Discovery (discovery.go): a Discoverer reports the set of live peers
//     (LAN base URL + their instance id). The production implementation uses
//     mDNS; tests inject a StaticDiscoverer pointing at httptest listeners.
//   - Transport (handlers.go + client.go): an authenticated HTTPS exchange.
//     A peer serves GET /api/fabric/changeset?since=<cursor> (its changesets
//     after a cursor) and accepts POST /api/fabric/changeset (a peer's
//     changesets). Both require a shared fabric secret in X-Fabric-Auth so a
//     random LAN host cannot inject or exfiltrate registry state.
//   - Sync loop (this file): periodically (and on demand) pull-then-push with
//     every discovered peer, advancing a per-peer cursor.
//
// Pure-Go: no CGO, no cr-sqlite. The merge runs through modernc.org/sqlite via
// internal/multiinstance. The transport is net/http + crypto/tls only.
package fabric

import (
	"context"
	"log"
	"sort"
	"sync"
	"time"

	"vulos/backend/internal/multiinstance"
)

// AppSyncMerger is the subset of *multiinstance.AppSync the fabric service
// needs. Defining it as an interface keeps the sync loop testable with a fake
// and documents exactly which merge primitives the transport relies on.
type AppSyncMerger interface {
	// ChangesetSince returns all app_registry rows changed after the cursor.
	ChangesetSince(since time.Time) ([]multiinstance.AppRegistryEntry, error)
	// EmitChangeset wraps entries in a changeset stamped with the local
	// instance's known peer count (for uninstall quorum).
	EmitChangeset(originULID string, entries []multiinstance.AppRegistryEntry) (*multiinstance.AppChangeset, error)
	// ApplyChangeset merges a peer's changeset using the deterministic CRDT
	// rules (LWW + OR-set + writer-node tie-break + uninstall quorum).
	ApplyChangeset(cs *multiinstance.AppChangeset) error
}

// Config configures a fabric sync Service.
type Config struct {
	// InstanceID is this box's stable ULID — its identity on the fabric and
	// the OriginULID stamped on changesets it emits. Required.
	InstanceID string

	// Secret is the shared fabric secret presented in X-Fabric-Auth on every
	// peer request and required by this box's own handlers. Peers that cannot
	// present it are rejected (401). Required — an empty secret disables the
	// handlers (fail-closed: no auth means no exchange).
	//
	// This is the CURRENT secret: the one this box sends. What it ACCEPTS is
	// Secrets, below, which during a rotation overlap is a superset.
	Secret string

	// Secrets is the rotation ring: the set of secrets this box accepts inbound
	// (Secret, plus an overlap value while one is configured and unexpired) and
	// the single value it sends (always Secret).
	//
	// Optional. When nil, New builds one with LoadSecretRingFromEnv, so a box
	// gets rotation from VULOS_FABRIC_SECRET_ALSO / _ALSO_UNTIL with no change
	// at the call site. When supplied, its Current MUST equal Secret — a ring
	// that sends one value and reports another is the kind of divergence that
	// would show up as an unexplained 401 halfway through a fleet roll, so it is
	// a construction error rather than something to reconcile silently.
	Secrets *SecretRing

	// AppSync is the CRDT merge primitive (real *multiinstance.AppSync in prod).
	// Required.
	AppSync AppSyncMerger

	// Discoverer reports the live peer set. Required. In production this is an
	// MDNSDiscoverer; tests inject a StaticDiscoverer.
	Discoverer Discoverer

	// SyncInterval is the cadence of the background pull/push loop. Defaults to
	// 30s. A local change can also trigger an out-of-band sync via Nudge.
	SyncInterval time.Duration

	// HTTPClient is the client used to reach same-LAN peers (Peer.WAN == false,
	// the default). In production it must trust the LAN cert (or skip
	// verification against a known-pinned LAN peer — see NewLANClient). Tests
	// inject the httptest server's client. Required.
	HTTPClient HTTPDoer

	// WANHTTPClient is the client used to reach peers discovered off-LAN via a
	// rendezvous relay (Peer.WAN == true — see RendezvousDiscoverer). It MUST
	// be built with NewWANClient: safedial-guarded dialing plus real TLS
	// certificate verification, because a WAN peer's address came from an
	// unauthenticated relay (FABRIC-SSRF-01 — see rendezvous.go's resolve
	// doc). Optional when the configured Discoverer never yields a WAN peer
	// (e.g. mDNS/StaticDiscoverer only); a WAN peer arriving with this unset
	// fails that one peer's sync closed rather than falling back to
	// HTTPClient — see Service.httpClientFor.
	WANHTTPClient *WANClient

	// SelfBaseURLs are this box's own LAN base URL(s); a discovered peer whose
	// base URL matches one of these is skipped so the box never syncs with
	// itself. Optional but recommended.
	SelfBaseURLs []string
}

// Service is the running fabric P2P sync service. Construct with New, start the
// background loop with Run, stop by cancelling Run's context.
type Service struct {
	cfg Config

	mu      sync.Mutex
	cursors map[string]time.Time  // peer base URL -> last-synced updated_at cursor
	status  map[string]peerStatus // peer base URL -> last-sync result (observability)
	lastRun time.Time             // wall-clock of the most recent SyncOnce round

	nudge chan struct{} // out-of-band sync trigger (on local change)
}

// peerStatus is the per-peer last-sync result tracked for the status endpoint.
type peerStatus struct {
	instanceID string
	lastSyncAt time.Time
	lastErr    string // "" on success
}

// New validates cfg and returns a fabric Service. It does no network I/O.
func New(cfg Config) (*Service, error) {
	if cfg.InstanceID == "" {
		return nil, errFabric("Config.InstanceID is required")
	}
	if cfg.Secret == "" {
		return nil, errFabric("Config.Secret is required (empty secret would disable peer auth)")
	}
	// FABRIC-SECRET-ROT-01, checked HERE and not further down: a ring that
	// disagrees with the secret we send is a credential misconfiguration, and it
	// must be reported as one rather than being masked by whichever unrelated
	// required field the caller also happened to omit.
	if cfg.Secrets != nil && cfg.Secrets.Current() != cfg.Secret {
		return nil, errFabric("Config.Secrets.Current() does not match Config.Secret — " +
			"a box that sends one secret and accepts another as 'current' would fail halfway through a fleet roll with an unexplained 401")
	}
	if cfg.AppSync == nil {
		return nil, errFabric("Config.AppSync is required")
	}
	if cfg.Discoverer == nil {
		return nil, errFabric("Config.Discoverer is required")
	}
	if cfg.HTTPClient == nil {
		return nil, errFabric("Config.HTTPClient is required")
	}
	if cfg.SyncInterval <= 0 {
		cfg.SyncInterval = 30 * time.Second
	}
	// The ring is what authOK consults; cfg.Secret is only what we send. Building
	// it here rather than at the call site means every existing constructor gets
	// rotation without a main.go edit.
	if cfg.Secrets == nil {
		cfg.Secrets = LoadSecretRingFromEnv(cfg.Secret)
		cfg.Secrets.LogSummary("[fabric]")
	}
	return &Service{
		cfg:     cfg,
		cursors: make(map[string]time.Time),
		status:  make(map[string]peerStatus),
		nudge:   make(chan struct{}, 1),
	}, nil
}

// authOK reports whether presented is a secret this box accepts, in constant
// time. An empty configured secret never matches (fail-closed).
//
// It goes through the rotation ring, not a single string: during a rotation
// overlap the ring accepts the current secret AND the overlap value, and the
// window is re-evaluated against the clock on every call — so it closes on a box
// that is never restarted. See secretring.go.
func (s *Service) authOK(presented string) bool {
	if s.cfg.Secret == "" {
		return false
	}
	return s.cfg.Secrets.Accepts(presented)
}

// SecretRing returns this service's rotation ring (never nil after New). It is
// exposed so the wiring can share ONE ring between the fabric handlers and the
// crdtsync door rather than parsing the environment twice and risking two
// different answers to "is the window open".
func (s *Service) SecretRing() *SecretRing { return s.cfg.Secrets }

// Run starts the background sync loop and blocks until ctx is cancelled. It
// runs an immediate first sync, then syncs every SyncInterval, and also reacts
// to Nudge (a local change wants to push promptly).
func (s *Service) Run(ctx context.Context) {
	log.Printf("[fabric] sync loop starting (instance=%s interval=%s)", s.cfg.InstanceID, s.cfg.SyncInterval)
	t := time.NewTicker(s.cfg.SyncInterval)
	defer t.Stop()

	// Immediate first round so a freshly-booted box converges quickly rather
	// than waiting a full interval.
	s.SyncOnce(ctx)

	for {
		select {
		case <-ctx.Done():
			log.Printf("[fabric] sync loop stopped")
			return
		case <-t.C:
			s.SyncOnce(ctx)
		case <-s.nudge:
			s.SyncOnce(ctx)
		}
	}
}

// Nudge requests an out-of-band sync round (e.g. right after a local install/
// uninstall). It is non-blocking and coalescing: many nudges between rounds
// collapse into one. Safe to call before Run starts.
func (s *Service) Nudge() {
	select {
	case s.nudge <- struct{}{}:
	default:
	}
}

// SyncOnce runs a single pull-then-push round against every currently-discovered
// peer. Per-peer failures are logged and skipped — one unreachable peer must
// never stall convergence with the rest. It is safe to call concurrently with
// the background loop, though typically only the loop calls it.
func (s *Service) SyncOnce(ctx context.Context) {
	s.mu.Lock()
	s.lastRun = time.Now()
	s.mu.Unlock()

	peers, err := s.cfg.Discoverer.Peers(ctx)
	if err != nil {
		log.Printf("[fabric] discovery failed: %v", err)
		return
	}
	for _, p := range peers {
		if s.isSelf(p) {
			continue
		}
		err := s.syncPeer(ctx, p)
		s.recordStatus(p, err)
		if err != nil {
			log.Printf("[fabric] sync with peer %s (%s) failed: %v", p.InstanceID, p.BaseURL, err)
		}
	}
}

// recordStatus stamps the last-sync time + error for a peer (observability for
// the /api/fabric/status endpoint).
func (s *Service) recordStatus(p Peer, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.status[p.BaseURL]
	if p.InstanceID != "" {
		st.instanceID = p.InstanceID
	}
	st.lastSyncAt = time.Now()
	if err != nil {
		st.lastErr = err.Error()
	} else {
		st.lastErr = ""
	}
	s.status[p.BaseURL] = st
}

// isSelf reports whether a discovered peer is actually this box (matching
// instance id or one of our own base URLs), so we never sync with ourselves.
func (s *Service) isSelf(p Peer) bool {
	if p.InstanceID != "" && p.InstanceID == s.cfg.InstanceID {
		return true
	}
	for _, self := range s.cfg.SelfBaseURLs {
		if self != "" && self == p.BaseURL {
			return true
		}
	}
	return false
}

// syncPeer performs the pull-then-push exchange with a single peer and advances
// that peer's cursor on success.
//
// Pull: ask the peer for everything it changed since our last cursor for it,
// then ApplyChangeset (deterministic merge). Push: send the peer everything WE
// have changed since the same cursor so the exchange is symmetric — after one
// round each side has merged the other's recent changes.
//
// The cursor is advanced to the max updated_at observed across the merged
// rows. Because both sides run the same monotonic LWW merge, advancing the
// cursor never loses an update (a row older than the cursor cannot win LWW
// anyway), and re-pulling from a slightly stale cursor is harmless (idempotent
// merge).
func (s *Service) syncPeer(ctx context.Context, p Peer) error {
	cursor := s.cursorFor(p.BaseURL)

	// ── PULL ────────────────────────────────────────────────────────────────
	remoteCS, err := s.pull(ctx, p, cursor)
	if err != nil {
		return err
	}
	maxSeen := cursor
	if remoteCS != nil && len(remoteCS.Entries) > 0 {
		if err := s.cfg.AppSync.ApplyChangeset(remoteCS); err != nil {
			return errFabricWrap("apply pulled changeset", err)
		}
		for _, e := range remoteCS.Entries {
			if e.UpdatedAt.After(maxSeen) {
				maxSeen = e.UpdatedAt
			}
		}
	}

	// ── PUSH ────────────────────────────────────────────────────────────────
	localEntries, err := s.cfg.AppSync.ChangesetSince(cursor)
	if err != nil {
		return errFabricWrap("read local changeset", err)
	}
	if len(localEntries) > 0 {
		localCS, err := s.cfg.AppSync.EmitChangeset(s.cfg.InstanceID, localEntries)
		if err != nil {
			return errFabricWrap("emit local changeset", err)
		}
		if err := s.push(ctx, p, localCS); err != nil {
			return err
		}
		for _, e := range localEntries {
			if e.UpdatedAt.After(maxSeen) {
				maxSeen = e.UpdatedAt
			}
		}
	}

	s.setCursor(p.BaseURL, maxSeen)
	return nil
}

// ── Status (observability) ─────────────────────────────────────────────────

// PeerSyncStatus is the per-peer view returned by the status endpoint: who the
// peer is, where the LWW cursor sits, and the result of the last sync attempt.
type PeerSyncStatus struct {
	InstanceID string     `json:"instance_id,omitempty"`
	BaseURL    string     `json:"base_url"`
	Cursor     *time.Time `json:"cursor,omitempty"`       // last-synced updated_at watermark
	LastSyncAt *time.Time `json:"last_sync_at,omitempty"` // wall-clock of last attempt
	LastError  string     `json:"last_error,omitempty"`   // "" when the last sync succeeded
}

// FabricStatus is the JSON shape served by GET /api/fabric/status. It reports
// this box's identity, when the sync loop last ran, and the per-peer state.
type FabricStatus struct {
	InstanceID     string           `json:"instance_id"`
	LastRunAt      *time.Time       `json:"last_run_at,omitempty"`
	SyncIntervalMS int64            `json:"sync_interval_ms"`
	PeerCount      int              `json:"peer_count"`
	Peers          []PeerSyncStatus `json:"peers"`

	// AuthenticatedWith names the secret slot THE CALLER'S OWN request matched:
	// "current" or "overlap" (see SlotCurrent/SlotOverlap). Empty when the
	// snapshot was not produced for a request.
	//
	// This is how an operator answers "which secret is this box on?" during a
	// roll — present the new value to each box in turn; "current" means that box
	// has committed to it, "overlap" means it has only prepared. It discloses
	// nothing, because it describes a value the caller just sent.
	AuthenticatedWith string `json:"authenticated_with,omitempty"`

	// SecretRotation is the fleet-wide half of the same question: is a window
	// open, when does it close, and is anybody still arriving on the old value.
	SecretRotation SecretRotationStatus `json:"secret_rotation"`
}

// Status returns a snapshot of the fabric sync state for observability. It is
// safe to call concurrently with the sync loop. Peers are ordered by base URL
// so the output is stable across calls.
func (s *Service) Status() FabricStatus {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := FabricStatus{
		InstanceID:     s.cfg.InstanceID,
		SyncIntervalMS: s.cfg.SyncInterval.Milliseconds(),
		Peers:          make([]PeerSyncStatus, 0, len(s.status)),
		SecretRotation: s.cfg.Secrets.RotationStatus(),
	}
	if !s.lastRun.IsZero() {
		lr := s.lastRun.UTC()
		out.LastRunAt = &lr
	}

	// Union of every base URL we have either a cursor or a status for.
	keys := make(map[string]struct{})
	for k := range s.cursors {
		keys[k] = struct{}{}
	}
	for k := range s.status {
		keys[k] = struct{}{}
	}
	ordered := make([]string, 0, len(keys))
	for k := range keys {
		ordered = append(ordered, k)
	}
	sort.Strings(ordered)

	for _, base := range ordered {
		ps := PeerSyncStatus{BaseURL: base}
		if st, ok := s.status[base]; ok {
			ps.InstanceID = st.instanceID
			if !st.lastSyncAt.IsZero() {
				ls := st.lastSyncAt.UTC()
				ps.LastSyncAt = &ls
			}
			ps.LastError = st.lastErr
		}
		if c, ok := s.cursors[base]; ok && !c.IsZero() {
			cu := c.UTC()
			ps.Cursor = &cu
		}
		out.Peers = append(out.Peers, ps)
	}
	out.PeerCount = len(out.Peers)
	return out
}

func (s *Service) cursorFor(baseURL string) time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cursors[baseURL]
}

func (s *Service) setCursor(baseURL string, t time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t.After(s.cursors[baseURL]) {
		s.cursors[baseURL] = t
	}
}
