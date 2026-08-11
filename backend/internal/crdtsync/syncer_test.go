package crdtsync

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

const testSecret = "shared-fabric-secret"

// node is a replica plus the HTTP server its peers reach it on — the real
// handlers, over a real socket. Nothing in these tests hands one store's
// in-memory pointer to another.
type node struct {
	store  *Store
	srv    *httptest.Server
	syncer *Syncer
}

func newNode(t *testing.T, actor string, domains []string) *node {
	t.Helper()
	st := newTestStoreWithDomains(t, actor, domains)
	mux := http.NewServeMux()
	st.RegisterHandlers(mux, SecretAuthorizer(testSecret))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &node{store: st, srv: srv}
}

// connect gives n a syncer pointing at the given peers.
func (n *node) connect(t *testing.T, domains []string, peers ...*node) {
	t.Helper()
	var sp []SyncPeer
	for _, p := range peers {
		sp = append(sp, SyncPeer{InstanceID: p.store.Actor(), BaseURL: p.srv.URL})
	}
	sy, err := NewSyncer(SyncerConfig{
		Store:      n.store,
		Peers:      PeerSourceFunc(func(context.Context) ([]SyncPeer, error) { return sp, nil }),
		Domains:    domains,
		Secret:     testSecret,
		HTTPClient: n.srv.Client(),
		Interval:   50 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewSyncer: %v", err)
	}
	n.syncer = sy
}

func TestSyncerConvergesOverHTTP(t *testing.T) {
	// The end-to-end claim: two replicas, concurrent writes to DIFFERENT fields
	// of the same record and a conflicting write to the same field, reconciled
	// only through POST /api/crdt/pull and /push over a real HTTP server.
	domains := []string{dom}
	a := newNode(t, "AAA", domains)
	b := newNode(t, "BBB", domains)
	a.connect(t, domains, b)
	b.connect(t, domains, a)

	if err := a.store.Set(dom, "rec:1", "title", []byte("from-a")); err != nil {
		t.Fatal(err)
	}
	if err := b.store.Set(dom, "rec:1", "body", []byte("from-b")); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	a.syncer.SyncOnce(ctx)
	b.syncer.SyncOnce(ctx)

	assertConverged(t, dom, a.store, b.store)
	for _, n := range []*node{a, b} {
		if v, ok := mustGet(t, n.store, dom, "rec:1", "title"); !ok || v != "from-a" {
			t.Errorf("%s: title = %q ok=%v", n.store.Actor(), v, ok)
		}
		if v, ok := mustGet(t, n.store, dom, "rec:1", "body"); !ok || v != "from-b" {
			t.Errorf("%s: body = %q ok=%v", n.store.Actor(), v, ok)
		}
	}
}

func TestSyncerPushesAsWellAsPulls(t *testing.T) {
	// One side runs the loop. The other must still receive — the round is
	// pull-THEN-push, so a passive peer converges without running a syncer.
	domains := []string{dom}
	a := newNode(t, "AAA", domains)
	b := newNode(t, "BBB", domains)
	a.connect(t, domains, b)

	if err := a.store.Set(dom, "k", "only-a-knows", []byte("v")); err != nil {
		t.Fatal(err)
	}
	a.syncer.SyncOnce(context.Background())

	if v, ok := mustGet(t, b.store, dom, "k", "only-a-knows"); !ok || v != "v" {
		t.Fatalf("passive peer did not receive the push: %q ok=%v", v, ok)
	}
	assertConverged(t, dom, a.store, b.store)
}

func TestSyncerThreeNodeConvergenceOverHTTP(t *testing.T) {
	domains := []string{dom}
	a := newNode(t, "AAA", domains)
	b := newNode(t, "BBB", domains)
	c := newNode(t, "CCC", domains)
	// A and C never talk to each other directly.
	a.connect(t, domains, b)
	b.connect(t, domains, a, c)
	c.connect(t, domains, b)

	if err := a.store.Set(dom, "k", "a", []byte("1")); err != nil {
		t.Fatal(err)
	}
	if err := c.store.Set(dom, "k", "c", []byte("3")); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	for i := 0; i < 4; i++ {
		a.syncer.SyncOnce(ctx)
		b.syncer.SyncOnce(ctx)
		c.syncer.SyncOnce(ctx)
	}
	assertConverged(t, dom, a.store, b.store, c.store)
	if v, ok := mustGet(t, c.store, dom, "k", "a"); !ok || v != "1" {
		t.Fatalf("A's write did not relay through B to C: %q ok=%v", v, ok)
	}
}

func TestSyncerConvergesLargeBacklogAcrossRounds(t *testing.T) {
	// More ops than one delta carries, so the loop has to take several rounds.
	domains := []string{dom}
	a := newNode(t, "AAA", domains)
	b := newNode(t, "BBB", domains)
	a.connect(t, domains, b)

	for i := 0; i < 300; i++ {
		if err := a.store.Set(dom, "k", "f"+strconv.Itoa(i), []byte("v")); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 300; i++ {
		if err := b.store.Set(dom, "k", "g"+strconv.Itoa(i), []byte("v")); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	for i := 0; i < 6; i++ {
		a.syncer.SyncOnce(ctx)
	}
	assertConverged(t, dom, a.store, b.store)
}

func TestSyncerRunLoopAndNudge(t *testing.T) {
	domains := []string{dom}
	a := newNode(t, "AAA", domains)
	b := newNode(t, "BBB", domains)
	a.connect(t, domains, b)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.syncer.Run(ctx)

	if err := a.store.Set(dom, "k", "f", []byte("nudged")); err != nil {
		t.Fatal(err)
	}
	a.syncer.Nudge()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if v, ok, _ := b.store.Get(dom, "k", "f"); ok && string(v) == "nudged" {
			cancel()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the run loop never propagated the write")
}

func TestSyncerNudgeDoesNotBlock(t *testing.T) {
	domains := []string{dom}
	a := newNode(t, "AAA", domains)
	b := newNode(t, "BBB", domains)
	a.connect(t, domains, b)
	// Nobody is draining the channel; Nudge must coalesce rather than block.
	done := make(chan struct{})
	go func() {
		for i := 0; i < 1000; i++ {
			a.syncer.Nudge()
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Nudge blocked")
	}
}

// ── authorisation ────────────────────────────────────────────────────────────

func TestHandlersRefuseToRegisterWithoutAuthorizer(t *testing.T) {
	// Fail-closed: an unauthenticated exchange endpoint must not exist at all,
	// rather than exist and serve openly.
	s := newTestStore(t, "A")
	mux := http.NewServeMux()
	s.RegisterHandlers(mux, nil)
	for _, path := range []string{"/api/crdt/pull", "/api/crdt/push", "/api/crdt/status"} {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader("{}"))
		_, pattern := mux.Handler(req)
		if pattern != "" {
			t.Errorf("%s was registered despite a nil Authorizer (pattern %q)", path, pattern)
		}
	}
}

func TestEndpointsRejectWrongSecret(t *testing.T) {
	domains := []string{dom}
	a := newNode(t, "AAA", domains)
	if err := a.store.Set(dom, "k", "f", []byte("secret-data")); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct{ name, secret string }{
		{"no secret", ""},
		{"wrong secret", "not-the-secret"},
		{"prefix of the secret", testSecret[:5]},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, path := range []string{"/api/crdt/pull", "/api/crdt/push"} {
				req, err := http.NewRequest(http.MethodPost, a.srv.URL+path, strings.NewReader(`{"domain":"`+dom+`"}`))
				if err != nil {
					t.Fatal(err)
				}
				if tc.secret != "" {
					req.Header.Set(AuthHeader, tc.secret)
				}
				resp, err := a.srv.Client().Do(req)
				if err != nil {
					t.Fatal(err)
				}
				resp.Body.Close()
				if resp.StatusCode != http.StatusUnauthorized {
					t.Errorf("%s with %s: status %d, want 401", path, tc.name, resp.StatusCode)
				}
			}
		})
	}
}

func TestSecretAuthorizerRejectsEmptySecret(t *testing.T) {
	// A box with no secret configured must authorise nothing — including a
	// request that also presents an empty header, which a naive equality check
	// would wave through.
	authz := SecretAuthorizer("")
	req := httptest.NewRequest(http.MethodPost, "/api/crdt/pull", nil)
	if authz(req) {
		t.Fatal("empty secret authorised a request with no header")
	}
	req.Header.Set(AuthHeader, "")
	if authz(req) {
		t.Fatal("empty secret authorised a request with an empty header")
	}
}

func TestStatusEndpoint(t *testing.T) {
	domains := []string{dom}
	a := newNode(t, "AAA", domains)
	if err := a.store.Set(dom, "k", "f", []byte("v")); err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodGet, a.srv.URL+"/api/crdt/status", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(AuthHeader, testSecret)
	resp, err := a.srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

// ── configuration is fail-closed ─────────────────────────────────────────────

func TestNewSyncerValidation(t *testing.T) {
	s := newTestStore(t, "A")
	ps := PeerSourceFunc(func(context.Context) ([]SyncPeer, error) { return nil, nil })
	ok := SyncerConfig{Store: s, Peers: ps, Domains: []string{dom}, Secret: "x", HTTPClient: http.DefaultClient}

	if _, err := NewSyncer(ok); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	bad := map[string]func(SyncerConfig) SyncerConfig{
		"no store":   func(c SyncerConfig) SyncerConfig { c.Store = nil; return c },
		"no peers":   func(c SyncerConfig) SyncerConfig { c.Peers = nil; return c },
		"no client":  func(c SyncerConfig) SyncerConfig { c.HTTPClient = nil; return c },
		"no secret":  func(c SyncerConfig) SyncerConfig { c.Secret = ""; return c },
		"no domains": func(c SyncerConfig) SyncerConfig { c.Domains = nil; return c },
		"refused domain": func(c SyncerConfig) SyncerConfig {
			c.Domains = []string{"sql:sessions"}
			return c
		},
	}
	for name, mut := range bad {
		if _, err := NewSyncer(mut(ok)); err == nil {
			t.Errorf("%s: NewSyncer must fail", name)
		}
	}
}

func TestWANPeerSkippedWithoutWANClient(t *testing.T) {
	// The LAN client skips certificate verification, which is safe pointed at a
	// link-local address and is not safe pointed at the internet. A relay-
	// supplied peer with no WAN client must be skipped, never downgraded.
	s := newTestStore(t, "A")
	sy, err := NewSyncer(SyncerConfig{
		Store:      s,
		Peers:      PeerSourceFunc(func(context.Context) ([]SyncPeer, error) { return nil, nil }),
		Domains:    []string{dom},
		Secret:     "x",
		HTTPClient: http.DefaultClient,
	})
	if err != nil {
		t.Fatal(err)
	}
	id := testIdentity(t)
	peerKey := EncodePeerKey(testIdentity(t).PublicKey())

	if _, err := sy.clientFor(SyncPeer{BaseURL: "https://relayed.example", WAN: true, PublicKey: peerKey}); err == nil {
		t.Fatal("a WAN peer must be skipped when no WAN client is configured")
	}
	// With one configured, https is required.
	sy.cfg.WANHTTPClient = http.DefaultClient
	sy.cfg.Identity = id
	if _, err := sy.clientFor(SyncPeer{BaseURL: "http://relayed.example", WAN: true, PublicKey: peerKey}); err == nil {
		t.Fatal("a plaintext WAN peer must be refused")
	}
	// IDENTITY is the other half of failing closed, and it is the one the
	// shared secret cannot supply. Without a local signing key this box could
	// not sign a request a peer would accept nor check the signature on a
	// response it is about to merge, so the peer is skipped — not dialled with
	// the fleet-wide secret.
	sy.cfg.Identity = nil
	if _, err := sy.clientFor(SyncPeer{BaseURL: "https://relayed.example", WAN: true, PublicKey: peerKey}); err == nil {
		t.Fatal("a WAN peer must be skipped when this box has no signing identity")
	}
	sy.cfg.Identity = id
	// And without the PEER's key there is nothing to pin the responder to: the
	// address came from an unsigned relay answer, so "whoever answers" is not
	// an identity.
	if _, err := sy.clientFor(SyncPeer{BaseURL: "https://relayed.example", WAN: true}); err == nil {
		t.Fatal("a WAN peer with no public key must be skipped")
	}
	if _, err := sy.clientFor(SyncPeer{BaseURL: "https://relayed.example", WAN: true, PublicKey: "not-a-key"}); err == nil {
		t.Fatal("a WAN peer with an unparseable public key must be skipped")
	}
	if _, err := sy.clientFor(SyncPeer{BaseURL: "https://relayed.example", WAN: true, PublicKey: peerKey}); err != nil {
		t.Fatalf("a valid https WAN peer with both keys must be accepted: %v", err)
	}
	// A LAN peer still uses the LAN client with no WAN client present.
	sy.cfg.WANHTTPClient = nil
	if _, err := sy.clientFor(SyncPeer{BaseURL: "https://192.168.1.5"}); err != nil {
		t.Fatalf("LAN peer rejected: %v", err)
	}
}

func TestSyncerSkipsSelf(t *testing.T) {
	domains := []string{dom}
	a := newNode(t, "AAA", domains)
	sy, err := NewSyncer(SyncerConfig{
		Store: a.store,
		Peers: PeerSourceFunc(func(context.Context) ([]SyncPeer, error) {
			return []SyncPeer{
				{InstanceID: "AAA", BaseURL: "http://elsewhere.invalid"}, // self by id
				{BaseURL: a.srv.URL}, // self by URL
			}, nil
		}),
		Domains:      domains,
		Secret:       testSecret,
		HTTPClient:   a.srv.Client(),
		SelfBaseURLs: []string{a.srv.URL},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Both entries are self; nothing is dialled, so no peer error is recorded.
	sy.SyncOnce(context.Background())
	if st := sy.Status(); len(st.PeerErrors) != 0 {
		t.Fatalf("self was dialled: %v", st.PeerErrors)
	}
}

func TestSyncerRecordsPeerErrors(t *testing.T) {
	domains := []string{dom}
	a := newNode(t, "AAA", domains)
	sy, err := NewSyncer(SyncerConfig{
		Store: a.store,
		Peers: PeerSourceFunc(func(context.Context) ([]SyncPeer, error) {
			return []SyncPeer{{InstanceID: "ZZZ", BaseURL: "http://127.0.0.1:1"}}, nil
		}),
		Domains:    domains,
		Secret:     testSecret,
		HTTPClient: a.srv.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	sy.SyncOnce(context.Background())
	st := sy.Status()
	if len(st.PeerErrors) != 1 {
		t.Fatalf("unreachable peer not recorded: %+v", st)
	}
	if st.Rounds != 1 {
		t.Fatalf("rounds = %d, want 1", st.Rounds)
	}
	// An unreachable peer must not take the loop down.
	sy.SyncOnce(context.Background())
	if sy.Status().Rounds != 2 {
		t.Fatal("loop stopped after a peer error")
	}
}

func TestSyncerRefusesToServeRefusedDomain(t *testing.T) {
	// End to end over HTTP: a peer that asks for a refused domain by name is
	// turned away by the handler, not quietly served an empty delta.
	a := newNode(t, "AAA", []string{dom})
	req, err := http.NewRequest(http.MethodPost, a.srv.URL+"/api/crdt/pull",
		strings.NewReader(`{"domain":"sql:sessions"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(AuthHeader, testSecret)
	resp, err := a.srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Fatal("a pull for a refused domain must not succeed")
	}
}
