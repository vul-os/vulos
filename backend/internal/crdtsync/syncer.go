package crdtsync

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// ── the transport seam ───────────────────────────────────────────────────────
//
// The engine does not know what a peer is. It asks a PeerSource for a list of
// base URLs and talks the pull/push protocol to each over an injected HTTP
// client. internal/fabric's mDNS Discoverer is one PeerSource (adapted at the
// wiring site); the WAN/relay rendezvous discoverer is another and needs no
// change here — it is the SAME interface, which is the whole point of stating
// the seam rather than reaching for fabric's types directly.
//
// What a WAN transport still has to bring with it is covered in
// roadmap/SYNC.md: a peer identity check stronger than the shared LAN secret,
// and NAT traversal. Neither is a change to this file.

// SyncPeer is one reachable replica.
type SyncPeer struct {
	// InstanceID is the peer's ULID when known. It may be empty (a discovery
	// hit that did not carry one); it is used only for self-skip and logging,
	// never for merge decisions.
	InstanceID string
	// BaseURL is the scheme://host[:port] the endpoints hang off.
	BaseURL string
	// WAN marks a peer resolved through a relay rather than the local network.
	// A WAN peer is only ever dialled with WANHTTPClient — see clientFor.
	WAN bool
}

// PeerSource yields the currently reachable peers.
type PeerSource interface {
	Peers(ctx context.Context) ([]SyncPeer, error)
}

// PeerSourceFunc adapts a function to PeerSource. This is the adapter the
// wiring uses to turn a fabric.Discoverer into a PeerSource without this
// package importing internal/fabric (which would make the dependency point the
// wrong way and pin the engine to one transport).
type PeerSourceFunc func(ctx context.Context) ([]SyncPeer, error)

func (f PeerSourceFunc) Peers(ctx context.Context) ([]SyncPeer, error) { return f(ctx) }

// Doer is the HTTP seam, matching fabric.HTTPDoer.
type Doer interface {
	Do(req *http.Request) (*http.Response, error)
}

// AuthHeader is the shared-secret header the LAN fabric authenticates with.
// The syncer sends it on every request and the wiring's Authorizer checks it,
// so the CRDT endpoints are gated exactly like fabric's own.
const AuthHeader = "X-Fabric-Auth"

// DefaultSyncInterval matches internal/fabric's default sync cadence.
const DefaultSyncInterval = 30 * time.Second

// maxRoundsPerPeer bounds one reconciliation with one peer. A far-behind peer
// takes several rounds because deltas are capped; this stops a peer that keeps
// reporting itself behind from pinning the loop on one address forever.
const maxRoundsPerPeer = 8

// SyncerConfig configures a Syncer.
type SyncerConfig struct {
	// Store is the local replica. Required.
	Store *Store
	// Peers is the discovery seam. Required.
	Peers PeerSource
	// Domains are the domains to reconcile. Required and non-empty; every one
	// must be in the Store's allow-list.
	Domains []string
	// Secret is the shared fabric secret. Required — an unauthenticated
	// exchange would let any host on the network rewrite replicated state.
	Secret string
	// HTTPClient dials LAN peers. Required.
	HTTPClient Doer
	// WANHTTPClient dials peers discovered through a relay. When nil, WAN peers
	// are SKIPPED rather than dialled with the LAN client — the LAN client
	// trusts any certificate, which is safe pointed at a link-local address and
	// is not safe pointed at the internet.
	WANHTTPClient Doer
	// SelfBaseURLs are this box's own URLs, skipped during discovery.
	SelfBaseURLs []string
	// Interval is the sync cadence. Defaults to DefaultSyncInterval.
	Interval time.Duration
}

// Syncer drives pull-then-push reconciliation rounds against discovered peers.
type Syncer struct {
	cfg   SyncerConfig
	nudge chan struct{}

	mu       sync.Mutex
	lastErr  map[string]string
	lastSync time.Time
	rounds   int
}

// NewSyncer validates the configuration and returns a Syncer. It fails rather
// than degrading: a syncer with no secret, no peers source or no domains would
// look like it was working while doing nothing (or worse, doing it openly).
func NewSyncer(cfg SyncerConfig) (*Syncer, error) {
	if cfg.Store == nil {
		return nil, errors.New("crdtsync: NewSyncer: Store is required")
	}
	if cfg.Peers == nil {
		return nil, errors.New("crdtsync: NewSyncer: Peers is required")
	}
	if cfg.HTTPClient == nil {
		return nil, errors.New("crdtsync: NewSyncer: HTTPClient is required")
	}
	if cfg.Secret == "" {
		return nil, errors.New("crdtsync: NewSyncer: Secret is required — an unauthenticated exchange endpoint is never acceptable")
	}
	if len(cfg.Domains) == 0 {
		return nil, errors.New("crdtsync: NewSyncer: Domains is empty — there would be nothing to reconcile")
	}
	for _, d := range cfg.Domains {
		if err := cfg.Store.checkDomain(d); err != nil {
			return nil, fmt.Errorf("crdtsync: NewSyncer: %w", err)
		}
	}
	if cfg.Interval <= 0 {
		cfg.Interval = DefaultSyncInterval
	}
	return &Syncer{cfg: cfg, nudge: make(chan struct{}, 1), lastErr: map[string]string{}}, nil
}

// Run drives sync rounds until ctx is cancelled. It syncs immediately, then on
// every tick, and promptly whenever Nudge is called.
func (s *Syncer) Run(ctx context.Context) {
	t := time.NewTicker(s.cfg.Interval)
	defer t.Stop()
	s.SyncOnce(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.SyncOnce(ctx)
		case <-s.nudge:
			s.SyncOnce(ctx)
		}
	}
}

// Nudge asks for a prompt sync round. It never blocks and coalesces: a burst of
// local writes produces one extra round, not one per write.
func (s *Syncer) Nudge() {
	select {
	case s.nudge <- struct{}{}:
	default:
	}
}

// SyncOnce reconciles every domain with every currently discovered peer.
func (s *Syncer) SyncOnce(ctx context.Context) {
	peers, err := s.cfg.Peers.Peers(ctx)
	if err != nil {
		log.Printf("[crdtsync] peer discovery: %v", err)
		return
	}
	for _, p := range peers {
		if s.isSelf(p) {
			continue
		}
		if err := s.syncPeer(ctx, p); err != nil {
			s.recordErr(p.BaseURL, err)
			log.Printf("[crdtsync] sync %s: %v", p.BaseURL, err)
			continue
		}
		s.recordErr(p.BaseURL, nil)
	}
	s.mu.Lock()
	s.lastSync = time.Now()
	s.rounds++
	s.mu.Unlock()
}

func (s *Syncer) isSelf(p SyncPeer) bool {
	if p.InstanceID != "" && p.InstanceID == s.cfg.Store.Actor() {
		return true
	}
	for _, self := range s.cfg.SelfBaseURLs {
		if self != "" && strings.EqualFold(strings.TrimRight(self, "/"), strings.TrimRight(p.BaseURL, "/")) {
			return true
		}
	}
	return false
}

// clientFor picks the transport for a peer, failing CLOSED.
//
// This mirrors fabric's unexported httpClientFor (FABRIC-SSRF-01) and exists
// for the same reason: the LAN client skips certificate verification because it
// dials link-local addresses where trust comes from the shared secret inside
// the tunnel. Pointing that same client at a relay-supplied address would be
// trusting an arbitrary internet host's certificate, so a WAN peer with no WAN
// client is skipped, not downgraded.
func (s *Syncer) clientFor(p SyncPeer) (Doer, error) {
	if !p.WAN {
		return s.cfg.HTTPClient, nil
	}
	if s.cfg.WANHTTPClient == nil {
		return nil, errors.New("WAN peer skipped: no WAN HTTP client configured")
	}
	u, err := url.Parse(p.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("WAN peer has an unparseable base URL: %w", err)
	}
	if !strings.EqualFold(u.Scheme, "https") {
		return nil, fmt.Errorf("WAN peer must be https, got %q", u.Scheme)
	}
	return s.cfg.WANHTTPClient, nil
}

// syncPeer runs a bounded pull-then-push reconciliation for every domain.
func (s *Syncer) syncPeer(ctx context.Context, p SyncPeer) error {
	client, err := s.clientFor(p)
	if err != nil {
		return err
	}
	base := strings.TrimRight(p.BaseURL, "/")
	for _, domain := range s.cfg.Domains {
		if err := s.syncDomain(ctx, client, base, domain); err != nil {
			return fmt.Errorf("domain %s: %w", domain, err)
		}
	}
	return nil
}

func (s *Syncer) syncDomain(ctx context.Context, client Doer, base, domain string) error {
	for round := 0; round < maxRoundsPerPeer; round++ {
		// ── pull ──
		vv, err := s.cfg.Store.VersionVector(domain)
		if err != nil {
			return err
		}
		var in Delta
		if err := s.post(ctx, client, base+"/api/crdt/pull", PullRequest{Domain: domain, VV: vv}, &in); err != nil {
			return fmt.Errorf("pull: %w", err)
		}
		if in.Domain == "" {
			in.Domain = domain
		}
		if _, err := s.cfg.Store.Merge(&in); err != nil {
			return fmt.Errorf("merge pulled delta: %w", err)
		}

		// ── push ──
		//
		// The peer's own version vector came back on the pull response, so the
		// reverse direction costs no extra round trip. A stale or hostile VV
		// only costs bandwidth: the merge outcome is a pure function of stamps.
		out, err := s.cfg.Store.Delta(domain, in.SenderVV, 0)
		if err != nil {
			return err
		}
		if len(out.Ops) > 0 || out.SnapshotRequired {
			var resp PushResponse
			if err := s.post(ctx, client, base+"/api/crdt/push", out, &resp); err != nil {
				return fmt.Errorf("push: %w", err)
			}
		}

		// Another round only if either side had more to say than fitted.
		if !in.Truncated && !out.Truncated {
			return nil
		}
	}
	return nil
}

// post sends a JSON body to a URL with the shared-secret header and decodes the
// JSON response.
func (s *Syncer) post(ctx context.Context, client Doer, url string, body, out any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(AuthHeader, s.cfg.Secret)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, MaxBodyBytes+1))
	if err != nil {
		return err
	}
	if len(respBody) > MaxBodyBytes {
		return fmt.Errorf("response exceeds %d bytes", MaxBodyBytes)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(respBody, out)
}

func (s *Syncer) recordErr(peer string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err == nil {
		delete(s.lastErr, peer)
		return
	}
	s.lastErr[peer] = err.Error()
}

// SyncerStatus is observability for the loop itself, distinct from the engine's
// per-domain EngineStatus.
type SyncerStatus struct {
	Actor      string            `json:"actor"`
	Domains    []string          `json:"domains"`
	Interval   string            `json:"interval"`
	Rounds     int               `json:"rounds"`
	LastSyncMS int64             `json:"last_sync_ms,omitempty"`
	PeerErrors map[string]string `json:"peer_errors,omitempty"`
}

// Status reports the loop's health.
func (s *Syncer) Status() SyncerStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	errs := make(map[string]string, len(s.lastErr))
	for k, v := range s.lastErr {
		errs[k] = v
	}
	st := SyncerStatus{
		Actor:      s.cfg.Store.Actor(),
		Domains:    append([]string(nil), s.cfg.Domains...),
		Interval:   s.cfg.Interval.String(),
		Rounds:     s.rounds,
		PeerErrors: errs,
	}
	if !s.lastSync.IsZero() {
		st.LastSyncMS = s.lastSync.UTC().UnixMilli()
	}
	return st
}

// SecretAuthorizer returns the Authorizer for the CRDT endpoints: a constant-
// time comparison against the shared fabric secret, matching internal/fabric's
// own authOK. An empty secret authorises NOTHING, so a misconfigured box serves
// no CRDT endpoints rather than open ones.
func SecretAuthorizer(secret string) Authorizer {
	return func(r *http.Request) bool {
		if secret == "" {
			return false
		}
		presented := r.Header.Get(AuthHeader)
		return subtle.ConstantTimeCompare([]byte(presented), []byte(secret)) == 1
	}
}
