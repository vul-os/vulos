package crdtsync

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// ── WAN harness ──────────────────────────────────────────────────────────────
//
// A wanNode is a real replica behind a real TLS server, reached over the real
// pull/push handlers, authenticated ONLY by per-peer Ed25519 signatures. No
// shared secret is configured on the client side of any exchange in this file —
// which is the point: these tests would all fail if the WAN path quietly fell
// back to it.

// mutableRoster is a PeerRoster an operator can change while the system runs,
// so a test can revoke a peer mid-flight and see it take effect on the very
// next request rather than at the next restart.
type mutableRoster struct {
	mu   sync.Mutex
	keys map[string]struct{}
}

func newMutableRoster(keys ...string) *mutableRoster {
	r := &mutableRoster{keys: map[string]struct{}{}}
	for _, k := range keys {
		r.keys[k] = struct{}{}
	}
	return r
}

func (r *mutableRoster) TrustedPeer(pub ed25519.PublicKey) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.keys[EncodePeerKey(pub)]
	return ok
}

func (r *mutableRoster) add(k string) {
	r.mu.Lock()
	r.keys[k] = struct{}{}
	r.mu.Unlock()
}

func (r *mutableRoster) remove(k string) {
	r.mu.Lock()
	delete(r.keys, k)
	r.mu.Unlock()
}

type wanNode struct {
	id     *PeerIdentity
	store  *Store
	srv    *httptest.Server
	roster *mutableRoster
	syncer *Syncer

	mu       sync.Mutex
	sawAuthN int // how many inbound requests carried the shared LAN secret
}

func newWANNode(t *testing.T, actor string, domains []string) *wanNode {
	t.Helper()
	n := &wanNode{
		id:     testIdentity(t),
		store:  newTestStoreWithDomains(t, actor, domains),
		roster: newMutableRoster(),
	}
	v, err := NewPeerVerifier(n.id.PublicKey(), n.roster)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	n.store.RegisterHandlers(mux,
		// Exactly the production composition: the LAN secret OR a rostered
		// peer signature. The secret is configured here on the SERVER side so
		// a client that wrongly fell back to it would be ACCEPTED — that is
		// what makes "no request carried it" a real observation rather than a
		// tautology.
		AnyOfAuthorizer(SecretAuthorizer(testSecret), PeerKeyAuthorizer(v)),
		WithResponseSigning(n.id))

	n.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(AuthHeader) != "" {
			n.mu.Lock()
			n.sawAuthN++
			n.mu.Unlock()
		}
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(n.srv.Close)
	return n
}

func (n *wanNode) sharedSecretSeen() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.sawAuthN
}

// wanClient returns an http.Client that verifies certificates properly against
// the given nodes' TLS certs. It is deliberately NOT an InsecureSkipVerify
// client: the WAN path in production uses fabric.NewWANClient, which does real
// certificate validation, and a test that skipped it would not be exercising
// the same shape.
func wanClient(t *testing.T, nodes ...*wanNode) *http.Client {
	t.Helper()
	pool := x509.NewCertPool()
	for _, n := range nodes {
		pool.AddCert(n.srv.Certificate())
	}
	return &http.Client{
		Timeout:   10 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}},
	}
}

// connectWAN gives n a syncer whose peers are all WAN peers, pinned by key.
func (n *wanNode) connectWAN(t *testing.T, domains []string, peers ...*wanNode) {
	t.Helper()
	sp := make([]SyncPeer, 0, len(peers))
	for _, p := range peers {
		sp = append(sp, SyncPeer{
			InstanceID: p.store.Actor(),
			BaseURL:    p.srv.URL,
			WAN:        true,
			PublicKey:  p.id.ID(),
		})
	}
	sy, err := NewSyncer(SyncerConfig{
		Store:   n.store,
		Peers:   PeerSourceFunc(func(context.Context) ([]SyncPeer, error) { return sp, nil }),
		Domains: domains,
		Secret:  testSecret,
		// A LAN client is present and unused: every peer above is WAN, so
		// clientFor must route through WANHTTPClient. If it ever downgraded, the
		// exchange would still work here and the shared-secret counter would
		// catch it.
		HTTPClient:    wanClient(t, peers...),
		WANHTTPClient: wanClient(t, peers...),
		Identity:      n.id,
		Interval:      time.Hour,
	})
	if err != nil {
		t.Fatalf("NewSyncer: %v", err)
	}
	n.syncer = sy
}

// trustEachOther puts every node in every other node's roster — the operator
// enrolment step, made explicit.
func trustEachOther(nodes ...*wanNode) {
	for _, a := range nodes {
		for _, b := range nodes {
			if a != b {
				a.roster.add(b.id.ID())
			}
		}
	}
}

// ── convergence over the WAN path ────────────────────────────────────────────

func TestWANSyncConvergesOverSignedTransport(t *testing.T) {
	// The headline claim: two replicas that share no secret on the wire, only
	// each other's public keys, converge through the real HTTP endpoints.
	domains := []string{dom}
	a := newWANNode(t, "AAA", domains)
	b := newWANNode(t, "BBB", domains)
	trustEachOther(a, b)
	a.connectWAN(t, domains, b)
	b.connectWAN(t, domains, a)

	// Concurrent writes: different fields of the same record (both must
	// survive) plus a conflicting write to the same field (one must win, and
	// both boxes must pick the SAME one).
	if err := a.store.Set(dom, "rec:1", "title", []byte("from-a")); err != nil {
		t.Fatal(err)
	}
	if err := b.store.Set(dom, "rec:1", "body", []byte("from-b")); err != nil {
		t.Fatal(err)
	}
	if err := a.store.Set(dom, "rec:1", "shared", []byte("a-wrote")); err != nil {
		t.Fatal(err)
	}
	if err := b.store.Set(dom, "rec:1", "shared", []byte("b-wrote")); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	a.syncer.SyncOnce(ctx)
	b.syncer.SyncOnce(ctx)
	a.syncer.SyncOnce(ctx)

	assertNoPeerErrors(t, a, b)
	assertConverged(t, dom, a.store, b.store)
	for _, n := range []*wanNode{a, b} {
		if v, ok := mustGet(t, n.store, dom, "rec:1", "title"); !ok || v != "from-a" {
			t.Errorf("%s: title = %q ok=%v", n.store.Actor(), v, ok)
		}
		if v, ok := mustGet(t, n.store, dom, "rec:1", "body"); !ok || v != "from-b" {
			t.Errorf("%s: body = %q ok=%v", n.store.Actor(), v, ok)
		}
	}

	// And the credential that must NOT have travelled.
	if got := a.sharedSecretSeen() + b.sharedSecretSeen(); got != 0 {
		t.Errorf("%d WAN requests carried the shared LAN secret — it must never leave the LAN", got)
	}
}

func TestWANSyncRefusesAnUnrosteredPeerAndNothingCrosses(t *testing.T) {
	// A holds a perfectly good key and reaches B. B has simply never been told
	// to trust it. Deny by default means NOTHING crosses — not the pull, not
	// the push.
	domains := []string{dom}
	a := newWANNode(t, "AAA", domains)
	b := newWANNode(t, "BBB", domains)
	a.roster.add(b.id.ID()) // A trusts B; B does NOT trust A.
	a.connectWAN(t, domains, b)

	if err := a.store.Set(dom, "rec:1", "title", []byte("from-a")); err != nil {
		t.Fatal(err)
	}
	if err := b.store.Set(dom, "rec:1", "body", []byte("from-b")); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	a.syncer.SyncOnce(ctx)

	if _, ok := mustGet(t, b.store, dom, "rec:1", "title"); ok {
		t.Fatal("an unrostered peer wrote to B")
	}
	if _, ok := mustGet(t, a.store, dom, "rec:1", "body"); ok {
		t.Fatal("A merged a delta it was never authorised to pull")
	}
	if errs := a.syncer.Status().PeerErrors; len(errs) == 0 {
		t.Fatal("the refusal was silent — a peer that cannot sync must be visible in Status")
	}

	// Enrolment is the operator's act, and it takes effect on the next request:
	// no restart, no cached decision.
	b.roster.add(a.id.ID())
	a.syncer.SyncOnce(ctx)
	a.syncer.SyncOnce(ctx)
	assertNoPeerErrors(t, a)
	assertConverged(t, dom, a.store, b.store)
}

func TestWANSyncStopsAtRevocation(t *testing.T) {
	// Revocation must bite mid-flight. A peer that was trusted a moment ago and
	// is removed now must not land its NEXT write.
	domains := []string{dom}
	a := newWANNode(t, "AAA", domains)
	b := newWANNode(t, "BBB", domains)
	trustEachOther(a, b)
	a.connectWAN(t, domains, b)

	if err := a.store.Set(dom, "rec:1", "before", []byte("landed")); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	a.syncer.SyncOnce(ctx)
	if v, ok := mustGet(t, b.store, dom, "rec:1", "before"); !ok || v != "landed" {
		t.Fatalf("setup: B never received the first write (%q ok=%v)", v, ok)
	}

	b.roster.remove(a.id.ID())
	if err := a.store.Set(dom, "rec:1", "after", []byte("must-not-land")); err != nil {
		t.Fatal(err)
	}
	a.syncer.SyncOnce(ctx)
	if _, ok := mustGet(t, b.store, dom, "rec:1", "after"); ok {
		t.Fatal("a REVOKED peer's write still landed")
	}
}

// ── the lying-relay cases ────────────────────────────────────────────────────
//
// A rendezvous relay's /resolve answer is unsigned (fabric/rendezvous.go
// documents this). So the address a WAN peer is dialled at is attacker-choosable
// in the worst case. These are the tests that say what happens then.

// hostileServer is an endpoint that answers 200 with a well-formed delta — the
// thing a relay would point this box at. What it does NOT have is the key.
func hostileServer(t *testing.T, sign func(reqNonce string, body []byte) string) *httptest.Server {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A delta that would, if merged, plant a register the caller never wrote.
		d := Delta{
			Domain:   dom,
			SenderVV: VersionVector{"EVIL": 1},
			Ops: []Op{{
				Domain: dom, Actor: "EVIL", Seq: 1, Key: "rec:1", Field: "title",
				Kind: OpSet, Value: []byte("planted"),
				Stamp: Stamp{Wall: time.Now().UnixMilli() + 1_000_000, Logical: 0, Actor: "EVIL"},
			}},
		}
		body, _ := json.Marshal(d)
		if sign != nil {
			var env peerAuthEnvelope
			_ = decodeEnvelope(r.Header.Get(PeerAuthHeader), &env)
			if h := sign(env.Nonce, body); h != "" {
				w.Header().Set(PeerAuthResponseHeader, h)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestWANClientRefusesAResponseItCannotAttribute(t *testing.T) {
	expected := testIdentity(t) // the key we asked the relay to resolve
	imposter := testIdentity(t) // whoever actually answered

	cases := []struct {
		name string
		sign func(t *testing.T, aud string) func(string, []byte) string
	}{
		{
			// The plainest case, and the one a request-only scheme would miss
			// entirely: the endpoint just does not sign.
			name: "unsigned 200",
			sign: func(t *testing.T, aud string) func(string, []byte) string { return nil },
		},
		{
			name: "signed by a key we never asked for",
			sign: func(t *testing.T, aud string) func(string, []byte) string {
				return func(nonce string, body []byte) string {
					h, err := imposter.SignResponse(aud, nonce, http.StatusOK, body)
					if err != nil {
						t.Fatal(err)
					}
					return h
				}
			},
		},
		{
			name: "correct key, but replaying an older nonce",
			sign: func(t *testing.T, aud string) func(string, []byte) string {
				return func(_ string, body []byte) string {
					h, err := expected.SignResponse(aud, "a-nonce-from-some-earlier-exchange", http.StatusOK, body)
					if err != nil {
						t.Fatal(err)
					}
					return h
				}
			},
		},
		{
			name: "correct key and nonce, but the body was swapped after signing",
			sign: func(t *testing.T, aud string) func(string, []byte) string {
				return func(nonce string, _ []byte) string {
					h, err := expected.SignResponse(aud, nonce, http.StatusOK, []byte(`{"domain":"`+dom+`"}`))
					if err != nil {
						t.Fatal(err)
					}
					return h
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			me := testIdentity(t)
			st := newTestStore(t, "ME")
			srv := hostileServer(t, tc.sign(t, me.ID()))
			pool := x509.NewCertPool()
			pool.AddCert(srv.Certificate())
			client := &http.Client{Transport: &http.Transport{
				TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}}

			peer := SyncPeer{BaseURL: srv.URL, WAN: true, PublicKey: expected.ID()}
			sy, err := NewSyncer(SyncerConfig{
				Store:         st,
				Peers:         PeerSourceFunc(func(context.Context) ([]SyncPeer, error) { return []SyncPeer{peer}, nil }),
				Domains:       []string{dom},
				Secret:        testSecret,
				HTTPClient:    client,
				WANHTTPClient: client,
				Identity:      me,
			})
			if err != nil {
				t.Fatal(err)
			}
			sy.SyncOnce(context.Background())

			// TLS succeeded, the status was 200, the body parsed. The ONLY
			// thing standing between that and a write into this database is the
			// response signature.
			if _, ok := mustGet(t, st, dom, "rec:1", "title"); ok {
				t.Fatal("merged ops from an endpoint that could not prove it was the peer we resolved")
			}
			if len(sy.Status().PeerErrors) == 0 {
				t.Fatal("the refusal was silent")
			}
		})
	}
}

func TestWANClientAcceptsTheSameResponseWhenProperlySigned(t *testing.T) {
	// The control for the table above: identical hostile-server shape, correct
	// signature by the expected key — and now it IS merged. Without this, every
	// case above could be passing because the transport was broken.
	expected := testIdentity(t)
	me := testIdentity(t)
	st := newTestStore(t, "ME")
	srv := hostileServer(t, func(nonce string, body []byte) string {
		h, err := expected.SignResponse(me.ID(), nonce, http.StatusOK, body)
		if err != nil {
			t.Fatal(err)
		}
		return h
	})
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	client := &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}}

	sy, err := NewSyncer(SyncerConfig{
		Store: st,
		Peers: PeerSourceFunc(func(context.Context) ([]SyncPeer, error) {
			return []SyncPeer{{BaseURL: srv.URL, WAN: true, PublicKey: expected.ID()}}, nil
		}),
		Domains:       []string{dom},
		Secret:        testSecret,
		HTTPClient:    client,
		WANHTTPClient: client,
		Identity:      me,
	})
	if err != nil {
		t.Fatal(err)
	}
	sy.SyncOnce(context.Background())
	if v, ok := mustGet(t, st, dom, "rec:1", "title"); !ok || v != "planted" {
		t.Fatalf("a properly signed delta was NOT merged (%q ok=%v) — the refusals above may be proving nothing", v, ok)
	}
}

// ── reordered, duplicated, and offline delivery over the signed path ─────────

// wanPost sends one signed request over the real transport and returns the
// status. It is how a test drives delivery order by hand.
func wanPost(t *testing.T, from *PeerIdentity, client *http.Client, to *wanNode, path string, body any, out any) int {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	header, nonce, err := from.SignRequest(http.MethodPost, path, raw, to.id.ID())
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, to.srv.URL+path, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(PeerAuthHeader, header)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	respBody := readAll(t, resp)
	if resp.StatusCode == http.StatusOK {
		if err := VerifyResponse(resp.Header.Get(PeerAuthResponseHeader), to.id.ID(), from.ID(), nonce, resp.StatusCode, respBody); err != nil {
			t.Fatalf("response from %s did not verify: %v", to.store.Actor(), err)
		}
		if out != nil {
			if err := json.Unmarshal(respBody, out); err != nil {
				t.Fatal(err)
			}
		}
	}
	return resp.StatusCode
}

func TestWANConvergesUnderReorderedAndDuplicatedDelivery(t *testing.T) {
	// Convergence is a property of the merge, and the merge does not change for
	// the WAN. But "does not change" is a claim, so it is checked THROUGH the
	// signed transport rather than asserted: every op below crosses a TLS
	// socket, a signature check and a roster check on the way in.
	domains := []string{dom}
	a := newWANNode(t, "AAA", domains)
	b := newWANNode(t, "BBB", domains)
	trustEachOther(a, b)
	client := wanClient(t, a, b)

	// Six conflicting ops: two boxes, same record, overlapping fields.
	for i := 0; i < 3; i++ {
		if err := a.store.Set(dom, "rec:1", fmt.Sprintf("f%d", i), []byte(fmt.Sprintf("a%d", i))); err != nil {
			t.Fatal(err)
		}
		if err := a.store.Set(dom, "rec:1", "shared", []byte(fmt.Sprintf("a-shared-%d", i))); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.store.Delete(dom, "rec:1", "f1"); err != nil {
		t.Fatal(err)
	}

	ops := allOps(t, a.store, dom)
	if len(ops) < 6 {
		t.Fatalf("setup produced only %d ops", len(ops))
	}

	// REVERSED, one op per request, then the WHOLE set again as one batch —
	// i.e. out-of-order delivery followed by wholesale duplication. Each is a
	// separate signed request with its own nonce, which is what an honest peer
	// retrying actually looks like.
	for i := len(ops) - 1; i >= 0; i-- {
		if code := wanPost(t, a.id, client, b, "/api/crdt/push",
			Delta{Domain: dom, Ops: []Op{ops[i]}}, nil); code != http.StatusOK {
			t.Fatalf("push of op %d: status %d", i, code)
		}
	}
	for round := 0; round < 2; round++ {
		if code := wanPost(t, a.id, client, b, "/api/crdt/push",
			Delta{Domain: dom, Ops: ops}, nil); code != http.StatusOK {
			t.Fatalf("duplicate batch push: status %d", code)
		}
	}
	// And a shuffled interleaving for good measure.
	shuffled := append([]Op(nil), ops...)
	for i := range shuffled {
		j := (i * 7) % len(shuffled)
		shuffled[i], shuffled[j] = shuffled[j], shuffled[i]
	}
	if code := wanPost(t, a.id, client, b, "/api/crdt/push",
		Delta{Domain: dom, Ops: shuffled}, nil); code != http.StatusOK {
		t.Fatalf("shuffled push: status %d", code)
	}

	assertConverged(t, dom, a.store, b.store)
	if v, ok := mustGet(t, b.store, dom, "rec:1", "f1"); ok {
		t.Errorf("a tombstone did not survive reordering: f1 = %q", v)
	}
}

func TestWANOfflinePeerCatchesUp(t *testing.T) {
	// C is offline while A and B talk, then joins. It must end up identical to
	// them — first by ops, then, past the compaction floor, by snapshot.
	domains := []string{dom}
	a := newWANNode(t, "AAA", domains)
	b := newWANNode(t, "BBB", domains)
	c := newWANNode(t, "CCC", domains)
	trustEachOther(a, b, c)
	a.connectWAN(t, domains, b)
	b.connectWAN(t, domains, a)

	ctx := context.Background()
	for i := 0; i < 5; i++ {
		if err := a.store.Set(dom, "rec:1", fmt.Sprintf("a%d", i), []byte("x")); err != nil {
			t.Fatal(err)
		}
		if err := b.store.Set(dom, "rec:1", fmt.Sprintf("b%d", i), []byte("y")); err != nil {
			t.Fatal(err)
		}
		a.syncer.SyncOnce(ctx)
		b.syncer.SyncOnce(ctx)
	}
	assertNoPeerErrors(t, a, b)
	assertConverged(t, dom, a.store, b.store)
	if _, ok := mustGet(t, c.store, dom, "rec:1", "a0"); ok {
		t.Fatal("C was supposed to be offline")
	}

	// C comes online: catch-up by ops.
	c.connectWAN(t, domains, a, b)
	c.syncer.SyncOnce(ctx)
	c.syncer.SyncOnce(ctx)
	assertNoPeerErrors(t, c)
	assertConverged(t, dom, a.store, b.store, c.store)

	// Now push A past the compaction floor while a FOURTH node stays offline,
	// so its catch-up has to come from a snapshot rather than a delta.
	d := newWANNode(t, "DDD", domains)
	trustEachOther(a, b, c, d)
	for i := 0; i < 8; i++ {
		if err := a.store.Set(dom, "rec:2", fmt.Sprintf("k%d", i), []byte("v")); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := a.store.Compact(dom, 1); err != nil {
		t.Fatal(err)
	}
	d.connectWAN(t, domains, a)
	d.syncer.SyncOnce(ctx)
	d.syncer.SyncOnce(ctx)
	assertNoPeerErrors(t, d)
	if v, ok := mustGet(t, d.store, dom, "rec:2", "k7"); !ok || v != "v" {
		t.Fatalf("the snapshot bootstrap did not reach D (%q ok=%v)", v, ok)
	}
	// A local write D made before it ever heard of A must survive the snapshot,
	// because applying a snapshot is a merge and not a replace.
	if err := d.store.Set(dom, "rec:3", "local", []byte("mine")); err != nil {
		t.Fatal(err)
	}
	d.syncer.SyncOnce(ctx)
	if v, ok := mustGet(t, d.store, dom, "rec:3", "local"); !ok || v != "mine" {
		t.Fatalf("a snapshot clobbered a local write (%q ok=%v)", v, ok)
	}
}

func TestWANReplayedRequestIsRefusedAndStateIsUnharmed(t *testing.T) {
	// A captured push, re-sent verbatim. The second delivery is refused as a
	// replay; the state is nevertheless correct, because the first delivery
	// already landed and the merge is idempotent. Both halves matter: a scheme
	// that let the replay through would be weaker, and one that corrupted state
	// on the retry would be broken.
	domains := []string{dom}
	a := newWANNode(t, "AAA", domains)
	b := newWANNode(t, "BBB", domains)
	trustEachOther(a, b)
	client := wanClient(t, a, b)

	if err := a.store.Set(dom, "rec:1", "title", []byte("once")); err != nil {
		t.Fatal(err)
	}
	ops := allOps(t, a.store, dom)
	raw, err := json.Marshal(Delta{Domain: dom, Ops: ops})
	if err != nil {
		t.Fatal(err)
	}
	header, _, err := a.id.SignRequest(http.MethodPost, "/api/crdt/push", raw, b.id.ID())
	if err != nil {
		t.Fatal(err)
	}
	send := func() int {
		req, _ := http.NewRequest(http.MethodPost, b.srv.URL+"/api/crdt/push", bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(PeerAuthHeader, header)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		readAll(t, resp)
		return resp.StatusCode
	}
	if code := send(); code != http.StatusOK {
		t.Fatalf("first delivery: status %d", code)
	}
	if code := send(); code != http.StatusUnauthorized {
		t.Fatalf("a byte-identical replay got %d, want 401", code)
	}
	if v, ok := mustGet(t, b.store, dom, "rec:1", "title"); !ok || v != "once" {
		t.Fatalf("state after replay: %q ok=%v", v, ok)
	}
	assertConverged(t, dom, a.store, b.store)
}

func TestWANPeerIsSkippedWhenIdentityIsMissing(t *testing.T) {
	// The end-to-end fail-closed: a reachable, rostered WAN peer, but this box
	// has no signing identity. It must be SKIPPED — not dialled with the LAN
	// secret, which the peer's server would happily accept.
	domains := []string{dom}
	a := newWANNode(t, "AAA", domains)
	b := newWANNode(t, "BBB", domains)
	trustEachOther(a, b)

	client := wanClient(t, b)
	st := a.store
	if err := st.Set(dom, "rec:1", "title", []byte("from-a")); err != nil {
		t.Fatal(err)
	}
	sy, err := NewSyncer(SyncerConfig{
		Store: st,
		Peers: PeerSourceFunc(func(context.Context) ([]SyncPeer, error) {
			return []SyncPeer{{BaseURL: b.srv.URL, WAN: true, PublicKey: b.id.ID()}}, nil
		}),
		Domains:       domains,
		Secret:        testSecret,
		HTTPClient:    client,
		WANHTTPClient: client,
		// Identity deliberately nil.
	})
	if err != nil {
		t.Fatal(err)
	}
	sy.SyncOnce(context.Background())
	if _, ok := mustGet(t, b.store, dom, "rec:1", "title"); ok {
		t.Fatal("a box with no peer identity still wrote to a WAN peer")
	}
	if b.sharedSecretSeen() != 0 {
		t.Fatal("the shared LAN secret was sent to a WAN peer")
	}
	if len(sy.Status().PeerErrors) == 0 {
		t.Fatal("the skip was silent")
	}
}

func assertNoPeerErrors(t *testing.T, nodes ...*wanNode) {
	t.Helper()
	for _, n := range nodes {
		if errs := n.syncer.Status().PeerErrors; len(errs) > 0 {
			var b strings.Builder
			for k, v := range errs {
				fmt.Fprintf(&b, "\n  %s: %s", k, v)
			}
			t.Fatalf("%s reported peer errors:%s", n.store.Actor(), b.String())
		}
	}
}
