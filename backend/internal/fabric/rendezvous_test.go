// no-broker-dep:allow-file: test comment cites the same canonical-message vector Ephor's own test
// asserts, to prove wire-format compatibility -- a cross-project test
// vector reference, not a dependency.

package fabric

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

// canonicalVectorHex is the same vector Ephor asserts in
// tunnel/rendezvous/canonical_test.go (github.com/vul-os/ephor). It is a
// cross-implementation wire contract: this
// side signs the preimage and the relay verifies it, so a one-byte divergence
// is an authentication failure at runtime that no amount of local testing here
// would otherwise catch. Both sides assert the same bytes; if either drifts,
// one of them goes red.
const canonicalVectorHex = "0000001476756c6f732d7264762f616e6e6f756e63652f31" +
	"00000004414141410000000a3137303030303030303000000003333030000000086e6f6e" +
	"6365313233000000066d6574612d78000000077773733a2f2f610000000968747470733a" +
	"2f2f62"

func TestCanonicalMessageMatchesRelayVector(t *testing.T) {
	m := canonicalMessage("vulos-rdv/announce/1", "AAAA", "1700000000", "300", "nonce123", "meta-x", "wss://a", "https://b")
	if got := hex.EncodeToString(m); got != canonicalVectorHex {
		t.Fatalf("canonical preimage diverged from the pinned vector:\n got=%s\nwant=%s", got, canonicalVectorHex)
	}
}

// TestAnnounceIsVerifiableByRelayRules reproduces the relay's verification:
// rebuild the canonical preimage from the JSON body's own fields and check the
// signature against the announced key. If field order or encoding is wrong here,
// this fails exactly as a real relay would reject it.
func TestAnnounceIsVerifiableByRelayRules(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}

	var got struct {
		Key       string   `json:"key"`
		Endpoints []string `json:"endpoints"`
		Meta      string   `json:"meta"`
		TTL       int64    `json:"ttl"`
		Nonce     string   `json:"nonce"`
		TS        int64    `json:"ts"`
		Sig       string   `json:"sig"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/announce" {
			t.Errorf("announce hit wrong path: %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode: %v", err)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	d := &RendezvousDiscoverer{
		BaseURL:       srv.URL,
		Key:           priv,
		SelfEndpoints: []string{"https://box.example:443", "wss://relay/t/abc"},
		HTTPClient:    srv.Client(),
	}
	if err := d.Announce(context.Background()); err != nil {
		t.Fatalf("announce: %v", err)
	}

	if got.Key != base64.RawURLEncoding.EncodeToString(pub) {
		t.Fatalf("announced key is not our public key")
	}
	fields := []string{got.Key, strconv.FormatInt(got.TS, 10), strconv.FormatInt(got.TTL, 10), got.Nonce, got.Meta}
	fields = append(fields, got.Endpoints...)
	msg := canonicalMessage("vulos-rdv/announce/1", fields...)
	sig, err := base64.RawURLEncoding.DecodeString(got.Sig)
	if err != nil {
		t.Fatalf("sig not b64url: %v", err)
	}
	if !ed25519.Verify(pub, msg, sig) {
		t.Fatal("relay would reject this announcement: signature does not verify over the canonical preimage")
	}
}

// TestResolveOfflinePeerIsNotAnError: a sibling that is switched off is the
// ordinary case, not a discovery fault. Treating 404 as an error would make a
// single powered-down box look like a broken relay.
func TestResolveOfflinePeerIsNotAnError(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(nil)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/announce" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"online":false}`))
	}))
	defer srv.Close()

	d := &RendezvousDiscoverer{BaseURL: srv.URL, Key: priv, PeerKeys: []string{"peer-a"}, HTTPClient: srv.Client()}
	peers, err := d.Peers(context.Background())
	if err != nil {
		t.Fatalf("offline peer reported as error: %v", err)
	}
	if len(peers) != 0 {
		t.Fatalf("want no peers, got %d", len(peers))
	}
}

func TestResolveOnlinePeerYieldsBaseURL(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(nil)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/announce" {
			w.WriteHeader(http.StatusOK)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"key": "peer-a", "online": true,
			"endpoints": []string{"https://peer.example:443/", "wss://relay/t/x"},
		})
	}))
	defer srv.Close()

	d := &RendezvousDiscoverer{BaseURL: srv.URL, Key: priv, PeerKeys: []string{"peer-a"}, HTTPClient: srv.Client()}
	peers, err := d.Peers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(peers) != 1 || peers[0].BaseURL != "https://peer.example:443" {
		t.Fatalf("want the first endpoint with trailing slash trimmed, got %+v", peers)
	}
}

// TestSelfIsNeverResolved guards the obvious self-sync loop: a box whose own key
// appears in the roster must skip it rather than announce to and then sync with
// itself.
func TestSelfIsNeverResolved(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(nil)
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/announce" {
			w.WriteHeader(http.StatusOK)
			return
		}
		hits++
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	d := &RendezvousDiscoverer{BaseURL: srv.URL, Key: priv, HTTPClient: srv.Client()}
	d.PeerKeys = []string{d.SelfKey()}
	if _, err := d.Peers(context.Background()); err != nil {
		t.Fatal(err)
	}
	if hits != 0 {
		t.Fatalf("resolved our own key %d times", hits)
	}
}

// TestAnnounceIsRateLimited: Peers runs on the sync tick, which is far more
// frequent than the announcement TTL. Re-announcing every tick would be a
// pointless write amplification against the relay.
func TestAnnounceIsRateLimited(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(nil)
	announces := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/announce" {
			announces++
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	d := &RendezvousDiscoverer{BaseURL: srv.URL, Key: priv, TTL: time.Hour, HTTPClient: srv.Client()}
	for i := 0; i < 5; i++ {
		if _, err := d.Peers(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if announces != 1 {
		t.Fatalf("want 1 announce across 5 ticks, got %d", announces)
	}
}

// TestMultiDiscovererSurvivesOneFailure is the property that matters for the
// mDNS+rendezvous pairing: losing multicast must not cost you your remote
// siblings, and losing the relay must not cost you the box in the next room.
func TestMultiDiscovererSurvivesOneFailure(t *testing.T) {
	good := NewStaticDiscoverer(Peer{BaseURL: "https://a"})
	bad := failingDiscoverer{}
	m := NewMultiDiscoverer(bad, good)
	peers, err := m.Peers(context.Background())
	if err != nil {
		t.Fatalf("one failing discoverer sank the whole set: %v", err)
	}
	if len(peers) != 1 || peers[0].BaseURL != "https://a" {
		t.Fatalf("want the working discoverer's peer, got %+v", peers)
	}
}

func TestMultiDiscovererDeduplicates(t *testing.T) {
	m := NewMultiDiscoverer(
		NewStaticDiscoverer(Peer{BaseURL: "https://a"}, Peer{BaseURL: "https://b"}),
		NewStaticDiscoverer(Peer{BaseURL: "https://a"}),
	)
	peers, err := m.Peers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(peers) != 2 {
		t.Fatalf("want 2 deduplicated peers, got %d: %+v", len(peers), peers)
	}
}

// TestMultiDiscovererFailsOnlyWhenEverythingFails preserves the caller's ability
// to tell "nobody is online" from "discovery is broken".
func TestMultiDiscovererFailsOnlyWhenEverythingFails(t *testing.T) {
	m := NewMultiDiscoverer(failingDiscoverer{}, failingDiscoverer{})
	if _, err := m.Peers(context.Background()); err == nil {
		t.Fatal("want an error when every discoverer failed")
	}
}

type failingDiscoverer struct{}

func (failingDiscoverer) Peers(context.Context) ([]Peer, error) {
	return nil, errFabric("discovery unavailable")
}
