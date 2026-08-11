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
// The peer identity a WAN transport needs is in peerauth.go: per-request
// Ed25519 signatures under the same per-instance fabric key the roster and the
// rendezvous relay already know a box by, authorised by a deny-by-default
// roster. What remains outside this package is NAT traversal itself — reaching
// the address the relay hands back.

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
	// PublicKey is the peer's Ed25519 identity (base64url, as
	// EncodePeerKey/rendezvous render it). For a WAN peer it is REQUIRED and
	// it is the key the relay was asked to resolve — so it is known before the
	// address is, which is what makes it usable to pin the responder.
	//
	// Empty for a LAN peer: mDNS learns an address, not a key, and the LAN path
	// authenticates with the shared secret inside a link-local tunnel.
	PublicKey string
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
// The syncer sends it on requests to LAN peers and the wiring's Authorizer
// checks it, so the CRDT endpoints are gated exactly like fabric's own.
//
// It is deliberately NOT sent to a WAN peer — see post. Those authenticate with
// PeerAuthHeader instead.
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
	// Secret is the shared fabric secret, used for LAN peers only. Required —
	// an unauthenticated exchange would let any host on the network rewrite
	// replicated state.
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
	// Identity is this box's Ed25519 peer identity — the SAME per-instance
	// fabric signing key the roster and the rendezvous relay know it by. It is
	// REQUIRED for WAN sync and unused on the LAN.
	//
	// When nil, WAN peers are SKIPPED. That is the fail-closed default and it
	// is the only correct one: without a key this box could not sign a request
	// a peer would accept, and could not check the signature on a response it
	// is about to merge.
	Identity *PeerIdentity
}

// Syncer drives pull-then-push reconciliation rounds against discovered peers.
type Syncer struct {
	cfg   SyncerConfig
	nudge chan struct{}

	mu       sync.Mutex
	lastErr  map[string]string
	lastSync time.Time
	rounds   int
	// lastPeers is what discovery returned on the most recent round, AFTER the
	// self-filter. It is recorded even when it is empty, because empty is the
	// case worth seeing: a round with no peers logs nothing, does nothing, and
	// is indistinguishable from a healthy round in every other signal this
	// type reports. Rounds climbing while this stays empty is the difference
	// between "the loop is broken" and "the loop has nobody to talk to".
	lastPeers []string
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
	dialled := make([]string, 0, len(peers))
	for _, p := range peers {
		if s.isSelf(p) {
			continue
		}
		dialled = append(dialled, p.BaseURL)
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
	s.lastPeers = dialled
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
// It also fails closed on IDENTITY, which is the harder half. Over the LAN the
// shared X-Fabric-Auth secret is defensible; over the WAN it is not an identity
// at all — every box holds the same value, so it says "a member of this fleet"
// and never which one, and sending it to a relay-supplied address discloses a
// fleet-wide credential to whoever that relay named. So a WAN peer is dialled
// only when BOTH halves of a real per-peer authentication are available:
//
//	Identity   — this box's Ed25519 key, to SIGN the request, and
//	PublicKey  — the peer's key, pinned from the relay lookup, to VERIFY the
//	             response before a single op of it is merged.
//
// Missing either, the peer is skipped. It is never downgraded to the shared
// secret, and never to the LAN client (which skips certificate verification —
// safe at a link-local address, not safe at an internet one).
func (s *Syncer) clientFor(p SyncPeer) (Doer, error) {
	if !p.WAN {
		return s.cfg.HTTPClient, nil
	}
	if s.cfg.WANHTTPClient == nil {
		return nil, errors.New("WAN peer skipped: no WAN HTTP client configured")
	}
	if s.cfg.Identity == nil {
		return nil, errors.New("WAN peer skipped: no peer signing identity configured — the shared LAN secret is not a peer identity and will not be sent over the WAN")
	}
	if p.PublicKey == "" {
		return nil, errors.New("WAN peer skipped: no peer public key — its response could not be attributed to the box we meant to reach")
	}
	if _, err := DecodePeerKey(p.PublicKey); err != nil {
		return nil, fmt.Errorf("WAN peer skipped: %w", err)
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
		if err := s.syncDomain(ctx, client, p, base, domain); err != nil {
			return fmt.Errorf("domain %s: %w", domain, err)
		}
	}
	return nil
}

func (s *Syncer) syncDomain(ctx context.Context, client Doer, p SyncPeer, base, domain string) error {
	for round := 0; round < maxRoundsPerPeer; round++ {
		// ── pull ──
		vv, err := s.cfg.Store.VersionVector(domain)
		if err != nil {
			return err
		}
		var in Delta
		if err := s.post(ctx, client, p, base+"/api/crdt/pull", PullRequest{Domain: domain, VV: vv}, &in); err != nil {
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
			if err := s.post(ctx, client, p, base+"/api/crdt/push", out, &resp); err != nil {
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

// post sends a JSON body to a URL and decodes the JSON response.
//
// Which credential travels depends on the peer, and the difference matters:
//
//   - LAN peer — the shared X-Fabric-Auth secret, unchanged. It is carried
//     inside a TLS tunnel to a link-local address multicast resolved, which is
//     the setting it was designed for.
//
//   - WAN peer — a per-request Ed25519 signature, and NOT the shared secret.
//     Withholding it is deliberate: the address came from a relay whose
//     /resolve answer is unsigned, so sending a fleet-wide bearer credential
//     there would hand it to whoever the relay named, before this box has any
//     way to tell. The signature discloses nothing and is bound to this box as
//     recipient, so a relay that lies learns nothing it can reuse.
//
// The response is then verified against the key that was RESOLVED, before any
// of it reaches Merge. That is the half a request-only scheme would miss: a
// pull response is data this box is about to write into its own database.
func (s *Syncer) post(ctx context.Context, client Doer, p SyncPeer, url string, body, out any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	var reqNonce string
	if p.WAN {
		if s.cfg.Identity == nil || p.PublicKey == "" {
			// clientFor already refuses this combination; repeated here so the
			// invariant holds for any future caller of post, not only that one.
			return errors.New("WAN peer requires both a local signing identity and the peer's public key")
		}
		header, nonce, serr := s.cfg.Identity.SignRequest(req.Method, req.URL.Path, raw, p.PublicKey)
		if serr != nil {
			return serr
		}
		req.Header.Set(PeerAuthHeader, header)
		reqNonce = nonce
	} else {
		req.Header.Set(AuthHeader, s.cfg.Secret)
	}

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
	if p.WAN {
		// Checked BEFORE the body is decoded, let alone merged. An unsigned or
		// wrongly-signed 200 from a WAN peer is an error, never a body that is
		// merged anyway with a warning.
		if verr := VerifyResponse(resp.Header.Get(PeerAuthResponseHeader), p.PublicKey,
			s.cfg.Identity.ID(), reqNonce, resp.StatusCode, respBody); verr != nil {
			return verr
		}
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
	Actor    string   `json:"actor"`
	Domains  []string `json:"domains"`
	Interval string   `json:"interval"`
	Rounds   int      `json:"rounds"`
	// Peers is what the last round actually dialled. An empty list next to a
	// climbing Rounds is a healthy loop with nothing to sync against.
	Peers      []string          `json:"peers"`
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
		Peers:      append([]string{}, s.lastPeers...),
		PeerErrors: errs,
	}
	if !s.lastSync.IsZero() {
		st.LastSyncMS = s.lastSync.UTC().UnixMilli()
	}
	return st
}

// RegisterSyncStatusHandler exposes the LOOP's health at
// GET /api/crdt/sync-status, behind the same authorizer as every other CRDT
// endpoint.
//
// The engine's own /api/crdt/status answers "what state do I hold" — version
// vectors, log sizes, register counts. It cannot answer "is replication
// happening", and those are different questions with the same failure mode: a
// box that has never synced with anybody reports a perfectly healthy engine
// holding perfectly local data.
//
// This was not an abstract omission. Status() existed, was documented as
// "observability for the loop itself", and had no caller outside this package's
// tests — so when two boxes silently failed to converge, the loop's round
// count, dialled peers and per-peer errors were all being computed and all
// being thrown away. The diagnosis had to be reconstructed from which log lines
// were ABSENT.
func RegisterSyncStatusHandler(mux *http.ServeMux, authz Authorizer, s *Syncer) {
	mux.HandleFunc("GET /api/crdt/sync-status", func(w http.ResponseWriter, r *http.Request) {
		if !authz(r) {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(s.Status()); err != nil {
			log.Printf("[crdtsync] sync-status: %v", err)
		}
	})
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
