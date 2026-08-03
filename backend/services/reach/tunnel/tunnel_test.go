package tunnel

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"vulos/backend/services/reach"
)

// --- harness ---------------------------------------------------------------

// relayFixture is a relay served over plain HTTP on loopback, plus whatever
// agents a test attaches to it.
type relayFixture struct {
	srv    *Server
	http   *httptest.Server
	wsURL  string
	domain string
}

// safeLogf returns a logger that stops forwarding to t once the test ends.
// The relay and agent log from background goroutines that can outlive the test
// body (e.g. a box disconnect racing test teardown); calling t.Logf after the
// test completes is itself a data race the -race detector flags. The mutex makes
// the done-check and the t.Logf atomic w.r.t. the Cleanup that sets done.
func safeLogf(t *testing.T, prefix string) func(string, ...any) {
	var mu sync.Mutex
	done := false
	t.Cleanup(func() { mu.Lock(); done = true; mu.Unlock() })
	return func(f string, a ...any) {
		mu.Lock()
		defer mu.Unlock()
		if !done {
			t.Logf(prefix+f, a...)
		}
	}
}

func newRelayFixture(t *testing.T, grants []Grant, mutate func(*ServerConfig)) *relayFixture {
	t.Helper()
	store, err := NewGrantStore(grants, Revocations{})
	if err != nil {
		t.Fatalf("NewGrantStore: %v", err)
	}
	cfg := ServerConfig{
		Domain: "relay.test",
		Grants: store,
		Logf:   safeLogf(t, "[relay] "),
	}
	if mutate != nil {
		mutate(&cfg)
	}
	srv, err := NewServer(cfg)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() {
		ts.Close()
		_ = srv.Close()
	})
	return &relayFixture{
		srv:    srv,
		http:   ts,
		wsURL:  "ws://" + strings.TrimPrefix(ts.URL, "http://"),
		domain: cfg.Domain,
	}
}

// startAgent attaches an agent and blocks until its link is up (or fails).
func (f *relayFixture) startAgent(t *testing.T, name, token string, h http.Handler) *Agent {
	t.Helper()
	up := make(chan LinkStatus, 16)
	a, err := NewAgent(AgentConfig{
		Targets: []Target{{WSURL: f.wsURL, Name: name, Token: token, Label: "test-relay"}},
		Handler: h,
		Logf:    safeLogf(t, "[agent] "),
		OnState: func(s LinkStatus) {
			select {
			case up <- s:
			default:
			}
		},
	})
	if err != nil {
		t.Fatalf("NewAgent: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); _ = a.Close() })
	a.Start(ctx)

	// Generous: this waits on a real dial + handshake, and CI machines are
	// often busy compiling something else at the same time.
	deadline := time.After(30 * time.Second)
	for {
		select {
		case st := <-up:
			switch st.State {
			case LinkUp:
				return a
			case LinkRefused:
				t.Fatalf("agent refused: %s", st.LastError)
			}
		case <-deadline:
			t.Fatalf("agent never came up; last status: %+v", a.Status())
		}
	}
}

// get issues a request to the relay addressed to a tunnel name.
//
// A box becomes routable in two steps: the agent's LinkUp fires when the client
// side of the tunnel handshakes, but the relay only starts serving that box a
// moment later (server.go: "requests get 502 until activate()"). A request in
// that window gets a 502 "box unavailable". No test in this package expects a
// 502, so we retry through that brief registration race rather than flaking.
func (f *relayFixture) get(t *testing.T, name, path string, mutate func(*http.Request)) *http.Response {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		req, err := http.NewRequest(http.MethodGet, f.http.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Host = name + "." + f.domain
		if mutate != nil {
			mutate(req)
		}
		resp, err := f.http.Client().Do(req)
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		if resp.StatusCode == http.StatusBadGateway && time.Now().Before(deadline) {
			resp.Body.Close()
			time.Sleep(20 * time.Millisecond)
			continue
		}
		return resp
	}
}

func body(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// capture records what a box's handler saw. The handler runs on the agent's
// goroutine and the assertions run on the test's, so the fields need a mutex —
// completing an HTTP response is not a happens-before edge the race detector
// recognises.
type capture struct {
	mu      sync.Mutex
	path    string
	remote  string
	header  http.Header
	haveTLS bool
}

func (c *capture) record(r *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.path, c.remote, c.header, c.haveTLS = r.URL.Path, r.RemoteAddr, r.Header.Clone(), r.TLS != nil
}

func (c *capture) get() (path, remote string, hdr http.Header, haveTLS bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.path, c.remote, c.header, c.haveTLS
}

// --- end to end ------------------------------------------------------------

// TestEndToEndRequestReachesTheBox is the whole point of the package: a public
// request to the relay is served by the box's own handler, through a tunnel
// the box dialled outbound.
func TestEndToEndRequestReachesTheBox(t *testing.T) {
	f := newRelayFixture(t, []Grant{{Token: "tok-box1", Names: []string{"box1"}}}, nil)

	var cap capture
	f.startAgent(t, "box1", "tok-box1", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cap.record(r)
		fmt.Fprint(w, "hello from the box")
	}))

	resp := f.get(t, "box1", "/api/hello", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if got := body(t, resp); got != "hello from the box" {
		t.Errorf("body = %q", got)
	}
	gotPath, gotRemote, _, _ := cap.get()
	if gotPath != "/api/hello" {
		t.Errorf("box saw path %q, want /api/hello", gotPath)
	}
	// The box must see the REAL client, not the tunnel.
	if !strings.HasPrefix(gotRemote, "127.0.0.1:") {
		t.Errorf("box saw RemoteAddr %q, want the client's 127.0.0.1", gotRemote)
	}
}

// TestPostBodyAndStatusRoundTrip covers a non-GET with a body and a
// non-200 status, which exercises a different path through the proxy.
func TestPostBodyAndStatusRoundTrip(t *testing.T) {
	f := newRelayFixture(t, []Grant{{Token: "tok", Names: []string{"box1"}}}, nil)
	f.startAgent(t, "box1", "tok", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		w.Header().Set("X-Echo-Method", r.Method)
		w.WriteHeader(http.StatusTeapot)
		fmt.Fprintf(w, "got:%s", b)
	}))

	req, _ := http.NewRequest(http.MethodPost, f.http.URL+"/submit", strings.NewReader("payload"))
	req.Host = "box1." + f.domain
	resp, err := f.http.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusTeapot {
		t.Errorf("status = %d, want 418", resp.StatusCode)
	}
	if resp.Header.Get("X-Echo-Method") != "POST" {
		t.Errorf("method not preserved: %q", resp.Header.Get("X-Echo-Method"))
	}
	if got := body(t, resp); got != "got:payload" {
		t.Errorf("body = %q", got)
	}
}

// TestOfflineBoxIsNotFoundNotAnError: an unknown name and an offline box must
// be indistinguishable, so a scanner cannot enumerate which boxes exist.
func TestOfflineBoxIsNotFoundNotAnError(t *testing.T) {
	f := newRelayFixture(t, []Grant{{Token: "tok", Names: []string{"box1"}}}, nil)

	offline := f.get(t, "box1", "/", nil) // granted, but never connected
	unknown := f.get(t, "nosuchbox", "/", nil)
	defer offline.Body.Close()
	defer unknown.Body.Close()

	if offline.StatusCode != http.StatusNotFound || unknown.StatusCode != http.StatusNotFound {
		t.Fatalf("offline=%d unknown=%d, both should be 404", offline.StatusCode, unknown.StatusCode)
	}
}

// --- the trust boundary ----------------------------------------------------

// TestClientCannotForgeVouchedHeaders is the security-critical test: a client
// that sets the relay's own vouched headers must not be able to influence
// what the box believes about it.
func TestClientCannotForgeVouchedHeaders(t *testing.T) {
	f := newRelayFixture(t, []Grant{{Token: "tok", Names: []string{"box1"}}}, nil)

	var cap capture
	f.startAgent(t, "box1", "tok", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cap.record(r)
		w.WriteHeader(http.StatusNoContent)
	}))

	resp := f.get(t, "box1", "/", func(req *http.Request) {
		req.Header.Set(HeaderClientIP, "203.0.113.66")
		req.Header.Set(HeaderProto, "https")
		req.Header.Set(HeaderName, "some-other-box")
		req.Header.Set(HeaderRelay, "evil.example.com")
		req.Header.Set("X-Forwarded-For", "198.51.100.1")
		req.Header.Set("X-Forwarded-Proto", "https")
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 (the forged HeaderName must not have caused a misroute)", resp.StatusCode)
	}

	_, remote, seen, tlsWasSet := cap.get()

	// No vouched header may survive to the box's handler.
	for k := range seen {
		if strings.HasPrefix(http.CanonicalHeaderKey(k), HeaderPrefix) {
			t.Errorf("vouched header %q reached the handler", k)
		}
	}
	// The forged client IP must not have become the box's view of the client.
	if strings.Contains(remote, "203.0.113.66") {
		t.Errorf("client forged its own RemoteAddr: %q", remote)
	}
	if got := seen.Get("X-Forwarded-For"); strings.Contains(got, "198.51.100.1") {
		t.Errorf("client-supplied X-Forwarded-For survived: %q", got)
	}
	// The relay here is plain HTTP, so the client did NOT speak https and the
	// synthetic TLS state must not have been fabricated from a forged header.
	if tlsWasSet {
		t.Error("r.TLS was set from a client-supplied proto header")
	}
}

// TestTunnelHandlerTranslation checks the translation itself directly: the
// vouched values (which, in production, only the relay can set) must become
// RemoteAddr and a synthetic TLS state.
func TestTunnelHandlerTranslation(t *testing.T) {
	var got *http.Request
	a, err := NewAgent(AgentConfig{
		Targets: []Target{{WSURL: "wss://relay.test", Name: "box1", Token: "t", Label: "l"}},
		Handler: http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { got = r }),
	})
	if err != nil {
		t.Fatal(err)
	}
	h := a.tunnelHandler(Target{Name: "box1"}, "relay.test")

	t.Run("https client becomes a secure request", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "https://box1.relay.test/x", nil)
		r.Header.Set(HeaderClientIP, "203.0.113.9")
		r.Header.Set(HeaderProto, "https")
		r.Header.Set(HeaderName, "box1")
		h.ServeHTTP(httptest.NewRecorder(), r)

		if got.RemoteAddr != "203.0.113.9:0" {
			t.Errorf("RemoteAddr = %q, want the vouched client IP", got.RemoteAddr)
		}
		if got.TLS == nil {
			t.Fatal("r.TLS is nil for an https client — session cookies would lose Secure")
		}
		if !got.TLS.HandshakeComplete {
			t.Error("synthetic TLS state is not marked complete")
		}
		if got.Header.Get("X-Forwarded-Proto") != "https" {
			t.Errorf("X-Forwarded-Proto = %q", got.Header.Get("X-Forwarded-Proto"))
		}
	})

	t.Run("plain http client stays insecure", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "http://box1.relay.test/x", nil)
		r.Header.Set(HeaderClientIP, "203.0.113.9")
		r.Header.Set(HeaderProto, "http")
		h.ServeHTTP(httptest.NewRecorder(), r)
		if got.TLS != nil {
			t.Error("r.TLS was set for a plaintext client")
		}
	})

	t.Run("unparseable client IP fails closed", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "http://box1.relay.test/x", nil)
		r.RemoteAddr = "tunnel:relay.test"
		r.Header.Set(HeaderClientIP, "not-an-ip")
		h.ServeHTTP(httptest.NewRecorder(), r)
		if got.RemoteAddr != "tunnel:relay.test" {
			t.Errorf("RemoteAddr = %q; a bad vouched IP must leave the tunnel address in place", got.RemoteAddr)
		}
	})

	t.Run("misrouted request is refused", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "http://other.relay.test/x", nil)
		r.Header.Set(HeaderName, "a-different-box")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusMisdirectedRequest {
			t.Errorf("status = %d, want 421 — the agent must not answer as another box", w.Code)
		}
	})
}

// TestRelayHonoursForwardedForOnlyWhenTrusted.
func TestRelayHonoursForwardedForOnlyWhenTrusted(t *testing.T) {
	check := func(t *testing.T, trust bool, want string) {
		t.Helper()
		f := newRelayFixture(t, []Grant{{Token: "tok", Names: []string{"box1"}}}, func(c *ServerConfig) {
			c.TrustProxyHeaders = trust
		})
		var cap capture
		f.startAgent(t, "box1", "tok", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			cap.record(r)
			w.WriteHeader(http.StatusNoContent)
		}))
		resp := f.get(t, "box1", "/", func(req *http.Request) {
			// Two entries: a trusting relay must take the RIGHTMOST, which is
			// what its own trusted proxy observed — never the leftmost, which
			// the client chose.
			req.Header.Set("X-Forwarded-For", "1.2.3.4, 203.0.113.7")
		})
		resp.Body.Close()
		_, seenIP, _, _ := cap.get()
		if !strings.HasPrefix(seenIP, want) {
			t.Errorf("trust=%v: box saw %q, want prefix %q", trust, seenIP, want)
		}
	}
	t.Run("untrusted ignores the header", func(t *testing.T) { check(t, false, "127.0.0.1") })
	t.Run("trusted takes the rightmost entry", func(t *testing.T) { check(t, true, "203.0.113.7") })
}

// --- authorization ---------------------------------------------------------

// TestUnauthorizedTokenRejectedBeforeUpgrade: an unrecognised credential must
// never cause a WebSocket or a session to be allocated.
func TestUnauthorizedTokenRejectedBeforeUpgrade(t *testing.T) {
	f := newRelayFixture(t, []Grant{{Token: "good", Names: []string{"box1"}}}, nil)

	req, _ := http.NewRequest(http.MethodGet, f.http.URL+TunnelPath, nil)
	req.Header.Set(AuthHeader, "Bearer wrong")
	resp, err := f.http.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
	if resp.Header.Get("WWW-Authenticate") != "" {
		t.Error("a WWW-Authenticate challenge would prompt a stray browser for credentials")
	}
	if len(f.srv.Tunnels()) != 0 {
		t.Error("a rejected upgrade allocated a session")
	}
}

// TestNameNotGrantedIsRefused: a valid token may not claim a name outside its
// grant.
func TestNameNotGrantedIsRefused(t *testing.T) {
	f := newRelayFixture(t, []Grant{{Token: "tok", Names: []string{"box1"}}}, nil)

	refused := make(chan LinkStatus, 8)
	a, err := NewAgent(AgentConfig{
		Targets: []Target{{WSURL: f.wsURL, Name: "box2", Token: "tok", Label: "l"}},
		Handler: http.NotFoundHandler(),
		Logf:    safeLogf(t, "[agent] "),
		OnState: func(s LinkStatus) {
			if s.State == LinkRefused {
				select {
				case refused <- s:
				default:
				}
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a.Start(ctx)

	select {
	case st := <-refused:
		if !strings.Contains(st.LastError, CodeUnauthorized) {
			t.Errorf("refusal did not carry the unauthorized code: %q", st.LastError)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("agent was not refused; status: %+v", a.Status())
	}
	_ = a.Close()
	if len(f.srv.Tunnels()) != 0 {
		t.Error("a refused registration left a session behind")
	}
}

// TestSameCredentialEvictsDifferentCredentialCannot is the reconnect-vs-
// hijack rule.
func TestSameCredentialEvictsDifferentCredentialCannot(t *testing.T) {
	// Two grants that BOTH list "box1" — the situation the rule exists for.
	f := newRelayFixture(t, []Grant{
		{Token: "tok-a", Names: []string{"box1"}},
		{Token: "tok-b", Names: []string{"box1"}},
	}, nil)

	first := f.startAgent(t, "box1", "tok-a", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "first")
	}))
	if n := len(f.srv.Tunnels()); n != 1 {
		t.Fatalf("expected 1 tunnel, got %d", n)
	}

	// A DIFFERENT credential must not be able to take the name.
	stolen := make(chan LinkStatus, 8)
	b, err := NewAgent(AgentConfig{
		Targets: []Target{{WSURL: f.wsURL, Name: "box1", Token: "tok-b", Label: "b"}},
		Handler: http.NotFoundHandler(),
		Logf:    func(fm string, ar ...any) { t.Logf("[agent-b] "+fm, ar...) },
		OnState: func(s LinkStatus) {
			select {
			case stolen <- s:
			default:
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b.Start(ctx)
	defer b.Close()

	deadline := time.After(8 * time.Second)
	for done := false; !done; {
		select {
		case st := <-stolen:
			if st.State == LinkUp {
				t.Fatal("a different credential hijacked a live name")
			}
			if st.State == LinkBackoff && strings.Contains(st.LastError, CodeNameInUse) {
				done = true
			}
		case <-deadline:
			done = true // no hijack observed, which is the pass condition
		}
	}

	// The original session still serves.
	resp := f.get(t, "box1", "/", nil)
	if got := body(t, resp); got != "first" {
		t.Errorf("original session was displaced: body = %q", got)
	}

	// The SAME credential reconnecting evicts and takes over — the
	// box-rebooted case.
	//
	// The first agent is stopped before the replacement starts, because two
	// LIVE agents sharing one name and credential would evict each other in a
	// loop (each eviction closes the other's tunnel, which it then retries).
	// That flapping is a real hazard for an operator who copies a config file
	// between two boxes without changing the name — it is documented in
	// docs/REACH.md — but it is not what this test is about.
	if err := first.Close(); err != nil {
		t.Fatalf("closing the first agent: %v", err)
	}
	f.startAgent(t, "box1", "tok-a", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "second")
	}))
	deadline2 := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline2) {
		resp := f.get(t, "box1", "/", nil)
		if body(t, resp) == "second" {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Error("the same credential did not take over the name on reconnect")
}

// --- routing ---------------------------------------------------------------

func TestRouteName(t *testing.T) {
	f := newRelayFixture(t, []Grant{{Token: "t", Names: []string{"box1"}}}, func(c *ServerConfig) {
		c.PathMode = true
	})
	cases := []struct {
		host, path string
		want       string
		ok         bool
	}{
		{"box1.relay.test", "/", "box1", true},
		{"box1.relay.test:443", "/", "box1", true},
		{"BOX1.RELAY.TEST", "/", "box1", true},
		// Multi-label must NOT resolve: a wildcard cert covers one level.
		{"a.box1.relay.test", "/", "", false},
		// The apex is not a tunnel.
		{"relay.test", "/", "", false},
		// A foreign host is not a tunnel.
		{"evil.example.com", "/", "", false},
		// Path mode.
		{"relay.test", "/t/box1/api", "box1", true},
		{"relay.test", "/t/BOX1/api", "", false},
		{"relay.test", "/t//api", "", false},
	}
	for _, tc := range cases {
		r := httptest.NewRequest(http.MethodGet, "http://"+f.domain+tc.path, nil)
		r.Host = tc.host
		got, ok := f.srv.routeName(r)
		if ok != tc.ok || got != tc.want {
			t.Errorf("routeName(host=%q path=%q) = (%q,%v), want (%q,%v)", tc.host, tc.path, got, ok, tc.want, tc.ok)
		}
	}
}

// TestReservedPathsCannotBeShadowed: the control plane resolves before any
// tunnel routing, whatever Host is presented.
func TestReservedPathsCannotBeShadowed(t *testing.T) {
	f := newRelayFixture(t, []Grant{{Token: "t", Names: []string{"box1"}}}, nil)
	f.startAgent(t, "box1", "t", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "BOX ANSWERED")
	}))

	for _, p := range []string{HealthPath, TunnelPath} {
		resp := f.get(t, "box1", p, nil)
		got := body(t, resp)
		if strings.Contains(got, "BOX ANSWERED") {
			t.Errorf("%s was routed into a tunnel", p)
		}
	}
}

// --- grants ----------------------------------------------------------------

func TestGrantStoreRules(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	mk := func(t *testing.T, gs []Grant, rev Revocations) *GrantStore {
		t.Helper()
		s, err := NewGrantStore(gs, rev)
		if err != nil {
			t.Fatalf("NewGrantStore: %v", err)
		}
		s.nowFn = func() time.Time { return now }
		return s
	}

	t.Run("relay never runs open", func(t *testing.T) {
		if _, err := NewGrantStore(nil, Revocations{}); err == nil {
			t.Fatal("an empty grant set was accepted")
		}
	})

	t.Run("grant with no names is refused", func(t *testing.T) {
		if _, err := NewGrantStore([]Grant{{Token: "t"}}, Revocations{}); err == nil {
			t.Fatal("a grant authorising nothing was accepted")
		}
	})

	t.Run("name must be in the grant", func(t *testing.T) {
		s := mk(t, []Grant{{Token: "t", Names: []string{"box1"}}}, Revocations{})
		if _, err := s.Authorize("t", "box1"); err != nil {
			t.Errorf("granted name refused: %v", err)
		}
		if _, err := s.Authorize("t", "box2"); err != ErrNameNotGranted {
			t.Errorf("err = %v, want ErrNameNotGranted", err)
		}
	})

	t.Run("rotation accepts both tokens", func(t *testing.T) {
		s := mk(t, []Grant{{Token: "new", PreviousToken: "old", Names: []string{"box1"}}}, Revocations{})
		for _, tok := range []string{"new", "old"} {
			if _, err := s.Authorize(tok, "box1"); err != nil {
				t.Errorf("token %q refused during rotation: %v", tok, err)
			}
		}
	})

	t.Run("expiry is enforced", func(t *testing.T) {
		s := mk(t, []Grant{{
			Token: "t", Names: []string{"box1"},
			ExpiresAt: now.Add(-time.Hour).UTC().Format(time.RFC3339),
		}}, Revocations{})
		if _, err := s.Authorize("t", "box1"); err != ErrGrantExpired {
			t.Errorf("err = %v, want ErrGrantExpired", err)
		}
	})

	t.Run("revoked token and name", func(t *testing.T) {
		s := mk(t, []Grant{
			{Token: "t1", Names: []string{"box1"}},
			{Token: "t2", Names: []string{"box2"}},
		}, Revocations{Tokens: []string{"t1"}, Names: []string{"box2"}})
		if _, err := s.Authorize("t1", "box1"); err != ErrTokenRevoked {
			t.Errorf("err = %v, want ErrTokenRevoked", err)
		}
		if _, err := s.Authorize("t2", "box2"); err != ErrNameRevoked {
			t.Errorf("err = %v, want ErrNameRevoked", err)
		}
	})

	t.Run("fingerprint does not disclose the token", func(t *testing.T) {
		fp := Fingerprint("super-secret")
		if strings.Contains(fp, "super-secret") || len(fp) != 12 {
			t.Errorf("fingerprint %q", fp)
		}
	})
}

// TestRevocationReachesEstablishedTunnels: a revocation that only applied to
// NEW connections would leave a compromised box connected indefinitely,
// because a working tunnel never reconnects.
func TestRevocationReachesEstablishedTunnels(t *testing.T) {
	f := newRelayFixture(t, []Grant{{Token: "tok", Names: []string{"box1"}}}, nil)
	f.startAgent(t, "box1", "tok", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "alive")
	}))
	if len(f.srv.Tunnels()) != 1 {
		t.Fatal("setup: tunnel not established")
	}

	if err := f.srv.cfg.Grants.Reload(
		[]Grant{{Token: "tok", Names: []string{"box1"}}},
		Revocations{Names: []string{"box1"}},
	); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	// Call the sweep directly rather than waiting out the ticker.
	f.srv.sweepRevoked()

	if n := len(f.srv.Tunnels()); n != 0 {
		t.Errorf("%d tunnel(s) survived revocation", n)
	}
}

// --- limiter ---------------------------------------------------------------

func TestLimiter(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	clock := func() time.Time { return now }

	t.Run("burst then refill", func(t *testing.T) {
		l := newLimiter(10, clock) // burst 20
		allowed := 0
		for i := 0; i < 100; i++ {
			if l.allow("k") {
				allowed++
			}
		}
		if allowed != 20 {
			t.Errorf("allowed %d in one instant, want the burst of 20", allowed)
		}
		now = now.Add(time.Second)
		refilled := 0
		for i := 0; i < 100; i++ {
			if l.allow("k") {
				refilled++
			}
		}
		if refilled != 10 {
			t.Errorf("refilled %d after 1s at rate 10, want 10", refilled)
		}
		now = now.Add(-time.Second)
	})

	t.Run("keys are independent", func(t *testing.T) {
		l := newLimiter(1, clock)
		for i := 0; i < 5; i++ {
			l.allow("a")
		}
		if !l.allow("b") {
			t.Error("exhausting one key throttled another")
		}
	})

	t.Run("negative rate disables", func(t *testing.T) {
		l := newLimiter(-1, clock)
		for i := 0; i < 1000; i++ {
			if !l.allow("k") {
				t.Fatal("a disabled limiter refused a request")
			}
		}
	})

	t.Run("idle buckets are swept", func(t *testing.T) {
		l := newLimiter(10, clock)
		for i := 0; i < 100; i++ {
			l.allow(fmt.Sprintf("ip-%d", i))
		}
		if len(l.buckets) != 100 {
			t.Fatalf("setup: %d buckets", len(l.buckets))
		}
		now = now.Add(bucketGCInterval + bucketIdleTTL + time.Second)
		l.allow("fresh")
		if len(l.buckets) > 2 {
			t.Errorf("%d buckets survived the sweep; the map must not grow without bound", len(l.buckets))
		}
		now = time.Unix(1_700_000_000, 0)
	})
}

// --- cross-package invariants ----------------------------------------------

// TestNameRuleMatchesReach locks the duplicated name rule to the one in the
// reach package. The duplication is deliberate (the relay must not link the
// box-side endpoint machinery); this test is what keeps it honest.
func TestNameRuleMatchesReach(t *testing.T) {
	cases := []string{
		"box1", "b", "a-b-c", "0", strings.Repeat("a", 63),
		"", "-box", "box-", "Box", "box.1", "box_1", strings.Repeat("a", 64), "böx",
	}
	for _, name := range cases {
		mine := ValidName(name) == nil
		theirs := reach.ValidName(name) == nil
		if mine != theirs {
			t.Errorf("name %q: tunnel.ValidName=%v, reach=%v — the two rules have drifted", name, mine, theirs)
		}
	}
}

// TestDirectProbeConstantsMatchDirectListen locks the wire strings this
// package redeclares to the box-side listener's own copies.
func TestDirectProbeConstantsMatchDirectListen(t *testing.T) {
	// Redeclared rather than imported so the relay role does not link the
	// box's public-listener package. Values from
	// backend/internal/directlisten/directlisten.go.
	const (
		wantPath   = "/_vulos-direct/probe"
		wantHeader = "X-Vulos-Direct-Probe"
	)
	if DirectProbePath != wantPath {
		t.Errorf("DirectProbePath = %q, want %q", DirectProbePath, wantPath)
	}
	if DirectProbeHeader != wantHeader {
		t.Errorf("DirectProbeHeader = %q, want %q", DirectProbeHeader, wantHeader)
	}
}

// TestConcurrentRequestsThroughOneTunnel exercises the mux under -race.
func TestConcurrentRequestsThroughOneTunnel(t *testing.T) {
	f := newRelayFixture(t, []Grant{{Token: "t", Names: []string{"box1"}}}, func(c *ServerConfig) {
		c.RequestRatePerTunnel = -1 // not the subject of this test
		c.GlobalRequestRate = -1
	})
	f.startAgent(t, "box1", "t", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, r.URL.Path)
	}))

	var wg sync.WaitGroup
	errs := make(chan error, 64)
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			want := fmt.Sprintf("/p/%d", i)
			resp := f.get(t, "box1", want, nil)
			defer resp.Body.Close()
			b, _ := io.ReadAll(resp.Body)
			if string(b) != want {
				errs <- fmt.Errorf("got %q want %q", b, want)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

// TestServesHostnameGatesCertificateIssuance covers the automatic-TLS gate. An
// ungated on-demand issuer would attempt a certificate for any hostname a
// stranger points at the relay, which is both an ACME rate-limit hazard and
// free work for an attacker.
func TestServesHostnameGatesCertificateIssuance(t *testing.T) {
	f := newRelayFixture(t, []Grant{
		{Token: "t1", Names: []string{"box1", "box2"}},
		{Token: "t2", Names: []string{"expired"}, ExpiresAt: "2000-01-01T00:00:00Z"},
	}, nil)

	cases := []struct {
		host string
		want bool
		why  string
	}{
		{"box1.relay.test", true, "granted name"},
		{"box2.relay.test", true, "granted name, same grant"},
		{"box1.relay.test:443", true, "port is stripped"},
		{"BOX1.RELAY.TEST", true, "case-insensitive"},
		{"relay.test", true, "the apex serves health and discovery"},
		{"nosuchbox.relay.test", false, "no grant authorises it"},
		{"expired.relay.test", false, "the grant has expired"},
		{"a.box1.relay.test", false, "multi-label is not a tunnel name"},
		{"evil.example.com", false, "a foreign host"},
		{"", false, "empty"},
	}
	for _, tc := range cases {
		if got := f.srv.ServesHostname(tc.host); got != tc.want {
			t.Errorf("ServesHostname(%q) = %v, want %v (%s)", tc.host, got, tc.want, tc.why)
		}
	}

	// A revoked name must stop being certifiable.
	if err := f.srv.cfg.Grants.Reload(
		[]Grant{{Token: "t1", Names: []string{"box1", "box2"}}},
		Revocations{Names: []string{"box1"}},
	); err != nil {
		t.Fatal(err)
	}
	if f.srv.ServesHostname("box1.relay.test") {
		t.Error("a revoked name is still certifiable")
	}
}
