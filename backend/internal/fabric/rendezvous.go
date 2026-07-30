// no-broker-dep:allow-file: comments state the rendezvous protocol is OPEN -- 'point this at your
// own Ephor... at one somebody else hosts, or at nothing at all' -- and
// note a canonical message format kept byte-identical to Ephor's for wire
// compatibility. Describes an optional, operator-chosen peer and a
// wire-compat contract, not a dependency.

package fabric

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// RendezvousDiscoverer finds fabric peers through a relay's rendezvous role,
// which is what lets two boxes sync when they are not on the same LAN.
//
// MDNSDiscoverer can only see multicast, so the fabric has been LAN-only: two
// boxes belonging to one person, in two houses, could never find each other.
// This closes that. Each box announces its reachable endpoints under its own
// Ed25519 key and resolves the keys of its siblings; the changeset exchange
// afterwards is unchanged, so nothing about the merge path knows the difference.
//
// # Trust
//
// The relay is a lookup table, not an authority. It learns which keys are
// online and what endpoints they claim — that is the price of NAT traversal —
// and nothing else: announce and resolve carry no app data, and the changeset
// exchange that follows runs over TLS directly to the peer. A relay that lies
// can withhold a peer or point at an address the caller cannot authenticate to;
// it cannot forge a changeset, because the fabric's own signed-changeset check
// is downstream of discovery and unchanged by this file.
//
// # Any relay, or none
//
// The rendezvous protocol is open. Point this at your own Ephor
// (github.com/vul-os/ephor), at one somebody else hosts, or at nothing at all:
// with no RendezvousURL configured the fabric is mDNS-only.
// It composes with MDNSDiscoverer rather than replacing it — see MultiDiscoverer.
type RendezvousDiscoverer struct {
	// BaseURL is the relay's rendezvous prefix, e.g. https://relay.example/rendezvous.
	BaseURL string
	// Key is this instance's fabric identity. Its public half is the address
	// siblings resolve; the private half signs announcements.
	Key ed25519.PrivateKey
	// PeerKeys are the sibling public keys to resolve, base64url (unpadded).
	// Typically the other instances in the owner's multiinstance roster.
	PeerKeys []string
	// SelfEndpoints are the URLs at which this box is reachable, most-specific
	// first. Announced verbatim; the relay does not dial them.
	SelfEndpoints []string
	// TTL is how long an announcement stays live. Zero means DefaultAnnounceTTL.
	TTL time.Duration
	// HTTPClient reaches the relay. Required.
	HTTPClient *http.Client

	mu            sync.Mutex
	lastAnnounce  time.Time
	announceEvery time.Duration
}

// DefaultAnnounceTTL is the announcement lifetime when Config.TTL is unset. The
// discoverer re-announces at a third of this, so two refreshes may be lost
// before a peer is dropped from the relay's view.
const DefaultAnnounceTTL = 5 * time.Minute

// SelfKey returns the base64url public key other instances resolve to find this
// one. It is the value to put in a sibling's PeerKeys.
func (d *RendezvousDiscoverer) SelfKey() string {
	if len(d.Key) != ed25519.PrivateKeySize {
		return ""
	}
	return b64u(d.Key.Public().(ed25519.PublicKey))
}

// Peers implements Discoverer. It refreshes this box's own announcement when
// due, then resolves every configured sibling key.
//
// A resolve failure for one peer is not fatal: an offline sibling is the normal
// case, and one unreachable relay should not deny the caller the peers it did
// find. Errors are returned only when nothing at all could be learned, so the
// caller can distinguish "no peers online" from "discovery is broken".
func (d *RendezvousDiscoverer) Peers(ctx context.Context) ([]Peer, error) {
	if d.BaseURL == "" {
		return nil, errFabric("RendezvousDiscoverer.BaseURL is required")
	}
	if len(d.Key) != ed25519.PrivateKeySize {
		return nil, errFabric("RendezvousDiscoverer.Key is required")
	}
	if d.HTTPClient == nil {
		return nil, errFabric("RendezvousDiscoverer.HTTPClient is required")
	}

	if err := d.announceIfDue(ctx); err != nil {
		// Announcing is how *others* find us; failing it does not stop us
		// finding them, so carry on and let the resolves speak.
		_ = err
	}

	self := d.SelfKey()
	var (
		peers    []Peer
		failures int
	)
	for _, k := range d.PeerKeys {
		if k == "" || k == self {
			continue
		}
		p, ok, err := d.resolve(ctx, k)
		if err != nil {
			failures++
			continue
		}
		if ok {
			peers = append(peers, p)
		}
	}
	if len(peers) == 0 && failures > 0 && failures == countResolvable(d.PeerKeys, self) {
		return nil, fmt.Errorf("fabric: rendezvous: all %d resolves failed", failures)
	}
	return peers, nil
}

func countResolvable(keys []string, self string) int {
	n := 0
	for _, k := range keys {
		if k != "" && k != self {
			n++
		}
	}
	return n
}

func (d *RendezvousDiscoverer) ttl() time.Duration {
	if d.TTL > 0 {
		return d.TTL
	}
	return DefaultAnnounceTTL
}

func (d *RendezvousDiscoverer) announceIfDue(ctx context.Context) error {
	d.mu.Lock()
	if d.announceEvery == 0 {
		d.announceEvery = d.ttl() / 3
	}
	due := time.Since(d.lastAnnounce) >= d.announceEvery
	d.mu.Unlock()
	if !due {
		return nil
	}
	if err := d.Announce(ctx); err != nil {
		return err
	}
	d.mu.Lock()
	d.lastAnnounce = time.Now()
	d.mu.Unlock()
	return nil
}

// Announce publishes this instance's endpoints to the relay under its key.
// Peers calls it on a schedule; it is exported so a caller can announce eagerly
// at boot rather than waiting for the first sync tick.
func (d *RendezvousDiscoverer) Announce(ctx context.Context) error {
	ttlSec := int64(d.ttl() / time.Second)
	ts := time.Now().Unix()
	nonce, err := randB64(16)
	if err != nil {
		return err
	}
	key := d.SelfKey()

	// Canonical field order is fixed by the relay's announce contract:
	// key, ts, ttl, nonce, meta, endpoints...  (vulos-rdv/announce/1)
	fields := []string{key, strconv.FormatInt(ts, 10), strconv.FormatInt(ttlSec, 10), nonce, fabricMeta}
	fields = append(fields, d.SelfEndpoints...)
	sig := ed25519.Sign(d.Key, canonicalMessage(domainRdvAnnounce, fields...))

	body, err := json.Marshal(map[string]any{
		"key": key, "endpoints": d.SelfEndpoints, "meta": fabricMeta,
		"ttl": ttlSec, "nonce": nonce, "ts": ts, "sig": b64u(sig),
	})
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(d.BaseURL, "/")+"/announce", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := d.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _, _ = io.Copy(io.Discard, resp.Body); _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("fabric: rendezvous announce: HTTP %d", resp.StatusCode)
	}
	return nil
}

func (d *RendezvousDiscoverer) resolve(ctx context.Context, peerKey string) (Peer, bool, error) {
	u := strings.TrimRight(d.BaseURL, "/") + "/resolve/" + peerKey
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return Peer{}, false, err
	}
	resp, err := d.HTTPClient.Do(req)
	if err != nil {
		return Peer{}, false, err
	}
	defer func() { _, _ = io.Copy(io.Discard, resp.Body); _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return Peer{}, false, nil // offline: normal, not an error
	default:
		return Peer{}, false, fmt.Errorf("fabric: rendezvous resolve: HTTP %d", resp.StatusCode)
	}

	var out struct {
		Key       string   `json:"key"`
		Online    bool     `json:"online"`
		Endpoints []string `json:"endpoints"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&out); err != nil {
		return Peer{}, false, err
	}
	if !out.Online || len(out.Endpoints) == 0 {
		return Peer{}, false, nil
	}
	// Most-specific endpoint first, matching announce order.
	return Peer{InstanceID: "", BaseURL: strings.TrimRight(out.Endpoints[0], "/")}, true, nil
}

// MultiDiscoverer merges several discoverers, de-duplicating by BaseURL.
//
// The production arrangement is mDNS plus rendezvous: a peer in the same house
// is found by multicast without a round trip to anyone, and the same peer moved
// behind a different NAT is still found through the relay. Neither is required.
// A discoverer that errors is skipped rather than failing the set — losing
// multicast should not cost you your remote siblings, or vice versa.
type MultiDiscoverer struct {
	Discoverers []Discoverer
}

// NewMultiDiscoverer returns a MultiDiscoverer over ds, ignoring nils.
func NewMultiDiscoverer(ds ...Discoverer) *MultiDiscoverer {
	out := make([]Discoverer, 0, len(ds))
	for _, d := range ds {
		if d != nil {
			out = append(out, d)
		}
	}
	return &MultiDiscoverer{Discoverers: out}
}

// Peers implements Discoverer.
func (m *MultiDiscoverer) Peers(ctx context.Context) ([]Peer, error) {
	seen := map[string]bool{}
	var (
		peers  []Peer
		errN   int
		lastEr error
	)
	for _, d := range m.Discoverers {
		got, err := d.Peers(ctx)
		if err != nil {
			errN++
			lastEr = err
			continue
		}
		for _, p := range got {
			if p.BaseURL == "" || seen[p.BaseURL] {
				continue
			}
			seen[p.BaseURL] = true
			peers = append(peers, p)
		}
	}
	if len(peers) == 0 && errN > 0 && errN == len(m.Discoverers) {
		return nil, lastEr
	}
	return peers, nil
}

const (
	// domainRdvAnnounce must match the relay's announce domain tag exactly;
	// a mismatch is a signature failure, not a subtle bug.
	domainRdvAnnounce = "vulos-rdv/announce/1"
	// fabricMeta marks these announcements as fabric peers, so a relay shared
	// with other apps can be filtered by an operator reading its presence view.
	fabricMeta = "caps=vulos-fabric"
)

// canonicalMessage builds the relay's domain-separated, length-prefixed signing
// preimage: each segment prefixed by its big-endian uint32 length, domain first.
// Kept byte-identical to keyauth.CanonicalMessage in Ephor
// (github.com/vul-os/ephor, tunnel/internal/keyauth) — the two are a wire
// contract, and rendezvous_test.go pins the shared vector.
func canonicalMessage(domain string, fields ...string) []byte {
	total := 4 + len(domain)
	for _, f := range fields {
		total += 4 + len(f)
	}
	buf := make([]byte, 0, total)
	var lp [4]byte
	appendSeg := func(s string) {
		binary.BigEndian.PutUint32(lp[:], uint32(len(s)))
		buf = append(buf, lp[:]...)
		buf = append(buf, s...)
	}
	appendSeg(domain)
	for _, f := range fields {
		appendSeg(f)
	}
	return buf
}

func b64u(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func randB64(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return b64u(b), nil
}
