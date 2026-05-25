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
	"crypto/subtle"
	"log"
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
	Secret string

	// AppSync is the CRDT merge primitive (real *multiinstance.AppSync in prod).
	// Required.
	AppSync AppSyncMerger

	// Discoverer reports the live peer set. Required. In production this is an
	// MDNSDiscoverer; tests inject a StaticDiscoverer.
	Discoverer Discoverer

	// SyncInterval is the cadence of the background pull/push loop. Defaults to
	// 30s. A local change can also trigger an out-of-band sync via Nudge.
	SyncInterval time.Duration

	// HTTPClient is the client used to reach peers. In production it must trust
	// the LAN cert (or skip verification against a known-pinned LAN peer — see
	// NewLANClient). Tests inject the httptest server's client. Required.
	HTTPClient HTTPDoer

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
	cursors map[string]time.Time // peer base URL -> last-synced updated_at cursor

	nudge chan struct{} // out-of-band sync trigger (on local change)
}

// New validates cfg and returns a fabric Service. It does no network I/O.
func New(cfg Config) (*Service, error) {
	if cfg.InstanceID == "" {
		return nil, errFabric("Config.InstanceID is required")
	}
	if cfg.Secret == "" {
		return nil, errFabric("Config.Secret is required (empty secret would disable peer auth)")
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
	return &Service{
		cfg:     cfg,
		cursors: make(map[string]time.Time),
		nudge:   make(chan struct{}, 1),
	}, nil
}

// authOK reports whether presented matches the configured secret in constant
// time. An empty configured secret never matches (fail-closed).
func (s *Service) authOK(presented string) bool {
	if s.cfg.Secret == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(presented), []byte(s.cfg.Secret)) == 1
}

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
	peers, err := s.cfg.Discoverer.Peers(ctx)
	if err != nil {
		log.Printf("[fabric] discovery failed: %v", err)
		return
	}
	for _, p := range peers {
		if s.isSelf(p) {
			continue
		}
		if err := s.syncPeer(ctx, p); err != nil {
			log.Printf("[fabric] sync with peer %s (%s) failed: %v", p.InstanceID, p.BaseURL, err)
		}
	}
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
