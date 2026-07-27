package tunnel

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	yamux "github.com/libp2p/go-yamux/v5"
)

// server.go — the relay side: `vulos relay serve`.
//
// A relay is an ordinary Vulos install that happens to have a public IP. It
// accepts agent tunnels, holds a name -> session table, and reverse-proxies
// public requests down the right tunnel. It terminates a TUNNEL, not a
// session: the TLS/auth handshake the box already speaks travels through it
// unmodified, so a relay is transport, not a trusted intermediary in the
// security sense.
//
// What a relay CAN do is what any transport can do: withhold service, observe
// traffic volume and timing, and see the plaintext of requests it forwards
// (it terminates the public TLS). That last point is stated plainly here and
// in the docs rather than glossed: run your own, or run one belonging to
// someone whose interests align with yours.
//
// # What the relay never does
//
//   - It never registers a name without a grant that authorises it (grants.go).
//   - It never forwards a client-supplied header from the vouched namespace.
//   - It never dials an address an agent asked it to, except through the
//     SSRF-guarded direct probe, which resolves and screens first.
//   - It never reveals its tunnel roster to an unauthenticated caller.

// ServerConfig configures a relay.
type ServerConfig struct {
	// Domain is the relay's base domain, e.g. "relay.example.com". Tunnels
	// are served at <name>.<Domain>. Required.
	Domain string

	// PathMode ALSO serves tunnels at /t/<name>/ for operators without
	// wildcard DNS or a wildcard certificate.
	//
	// HONEST LIMITATION: path mode rewrites only the URL path. An application
	// that emits absolute links or absolute asset paths — which the Vulos
	// shell does — will break under it. It is genuinely useful for simple
	// single-path services and for testing, and it is documented that way
	// rather than presented as an equal alternative to subdomain mode.
	PathMode bool

	// Grants authorises registrations. Required — a relay never runs open.
	Grants *GrantStore

	// MaxAgents caps concurrent tunnels. Zero uses DefaultMaxAgents.
	MaxAgents int

	// TrustProxyHeaders makes the relay believe X-Forwarded-For from its
	// caller.
	//
	// DEFAULT OFF, and that default is load-bearing. When the relay is
	// directly internet-facing, believing this header would let any client
	// choose its own apparent source IP and defeat every per-IP rate limit
	// and every access log. Turn it on ONLY when a trusted TLS-terminating
	// proxy (Caddy, nginx, a CDN) is the only thing that can reach the
	// relay's listener.
	TrustProxyHeaders bool

	// NodeID is a stable identifier for this relay, surfaced to agents and on
	// the admin endpoint. Optional.
	NodeID string

	// RendezvousHandler, when set, serves the discovery role at
	// RendezvousPrefix. It is an http.Handler rather than a concrete type so
	// the relay does not couple to the rendezvous package (and so a box,
	// which links this package for its agent, does not link the role at all).
	//
	// CONTAINMENT: it is served ONLY on the relay's APEX host. A tunnelled
	// box lives at <name>.<Domain> and keeps full control of every path under
	// its own host, including /rendezvous — otherwise adding a role to the
	// relay would silently steal a path from every box it serves.
	RendezvousHandler http.Handler
	// RendezvousPrefix is where that handler is mounted. Required when
	// RendezvousHandler is set.
	RendezvousPrefix string

	// MaxRequestBytes caps a single proxied request body. Zero uses
	// DefaultMaxRequestBytes.
	MaxRequestBytes int64

	// EnableDirectProbe allows agents to advertise a direct endpoint, which
	// the relay verifies before publishing. Off by default: it is the only
	// feature that makes the relay perform an outbound request influenced by
	// an agent, so it is opt-in even though the probe is SSRF-guarded.
	EnableDirectProbe bool

	// AllowPrivateDirect permits direct endpoints that resolve to private or
	// loopback addresses. For an operator whose boxes genuinely live on the
	// relay's own network. Off by default.
	AllowPrivateDirect bool

	// Rate limits. Zero values use the defaults below; a negative value
	// disables that limiter.
	UpgradeRatePerIP     float64
	RequestRatePerTunnel float64
	GlobalRequestRate    float64

	// Logf receives operator-facing log lines. Never called with a token.
	Logf func(format string, args ...any)

	// upgrader is a test seam.
	upgrader *websocket.Upgrader
	// probeClient is a test seam for the direct probe.
	probeClient *http.Client
	// nowFn is a test seam.
	nowFn func() time.Time
}

// Defaults chosen for a small VPS: generous enough that a personal fleet
// never hits them, tight enough that one misbehaving peer cannot exhaust the
// machine.
const (
	DefaultMaxAgents       = 64
	DefaultMaxRequestBytes = 256 << 20 // 256 MiB
	// defaultUpgradeRatePerIP bounds tunnel-establishment attempts per source
	// IP. This is RESOURCE protection, not credential protection — an
	// unrecognised token is rejected before any allocation, and guessing a
	// 256-bit random grant is not a threat any rate limit meaningfully
	// changes. It is set well above one-per-second because several boxes
	// behind ONE home NAT share a source address, and after a relay restart
	// they all reconnect at once; too tight a limit would turn a relay
	// restart into a self-inflicted outage for exactly the multi-box
	// households this feature is for.
	defaultUpgradeRatePerIP = 5.0
	// defaultRequestRatePerTunnel and defaultGlobalRequestRate bound proxied
	// traffic. They are per-second with a burst of 2x.
	defaultRequestRatePerTunnel = 50.0
	defaultGlobalRequestRate    = 500.0
	// revocationSweep is how often live sessions are rechecked against the
	// grant store, so a revocation reaches ESTABLISHED tunnels and not only
	// future ones.
	revocationSweep = 20 * time.Second
	// directProbeTimeout bounds the ownership probe.
	directProbeTimeout = 10 * time.Second
)

// session is one live tunnel.
//
// A session is published into the routing table BEFORE its yamux session
// exists — see register's ordering note. Until then serving is nil and
// handleProxy answers 502, which is accurate: the tunnel is claimed but not
// yet carrying traffic. mu guards that one transition.
type session struct {
	name        string
	fingerprint string
	label       string
	connected   time.Time
	limiter     *limiter

	// direct is the verified direct endpoint, empty until the probe passes.
	direct string

	mu      sync.RWMutex
	sess    *yamux.Session
	proxy   *httputil.ReverseProxy
	serving bool

	closeOnce sync.Once
	closed    chan struct{}
}

// activate installs the multiplexed session once the control handshake is
// complete and the connection belongs to yamux.
func (s *session) activate(ys *yamux.Session, proxy *httputil.ReverseProxy) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sess, s.proxy, s.serving = ys, proxy, true
}

// handler returns the reverse proxy, or nil if this session is not yet (or no
// longer) carrying traffic.
func (s *session) handler() *httputil.ReverseProxy {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.serving {
		return nil
	}
	return s.proxy
}

func (s *session) close() {
	s.closeOnce.Do(func() {
		close(s.closed)
		s.mu.Lock()
		s.serving = false
		ys := s.sess
		s.mu.Unlock()
		if ys != nil {
			_ = ys.Close()
		}
	})
}

// Server is a relay.
type Server struct {
	cfg      ServerConfig
	upgrader *websocket.Upgrader
	probeCli *http.Client
	nowFn    func() time.Time

	mu       sync.RWMutex
	sessions map[string]*session // by tunnel name
	draining bool

	upgradeLim *limiter
	globalLim  *limiter

	stopOnce sync.Once
	stopped  chan struct{}
	wg       sync.WaitGroup
}

// NewServer validates cfg and returns a relay that is not yet serving.
func NewServer(cfg ServerConfig) (*Server, error) {
	cfg.Domain = strings.ToLower(strings.TrimSpace(cfg.Domain))
	if cfg.Domain == "" {
		return nil, errors.New("relay: Domain is required (e.g. relay.example.com)")
	}
	if strings.ContainsAny(cfg.Domain, "/:") {
		return nil, fmt.Errorf("relay: Domain %q must be a bare hostname, no scheme or port", cfg.Domain)
	}
	if cfg.Grants == nil {
		return nil, errors.New("relay: Grants is required — a relay never runs open")
	}
	if cfg.RendezvousHandler != nil && strings.TrimSpace(cfg.RendezvousPrefix) == "" {
		return nil, errors.New("relay: RendezvousPrefix is required when RendezvousHandler is set")
	}
	if cfg.MaxAgents <= 0 {
		cfg.MaxAgents = DefaultMaxAgents
	}
	if cfg.MaxRequestBytes <= 0 {
		cfg.MaxRequestBytes = DefaultMaxRequestBytes
	}
	if cfg.Logf == nil {
		cfg.Logf = func(string, ...any) {}
	}
	if cfg.nowFn == nil {
		cfg.nowFn = time.Now
	}

	up := cfg.upgrader
	if up == nil {
		up = &websocket.Upgrader{
			HandshakeTimeout: HandshakeTimeout,
			ReadBufferSize:   32 << 10,
			WriteBufferSize:  32 << 10,
			// CheckOrigin returns false for any browser-originated upgrade.
			// The tunnel endpoint is for agents, which are not browsers and
			// send no Origin. Refusing an Origin outright means a page in a
			// victim's browser can never be induced to open a tunnel session
			// with the victim's ambient credentials.
			CheckOrigin: func(r *http.Request) bool {
				return r.Header.Get("Origin") == ""
			},
			EnableCompression: false,
		}
	}
	probeCli := cfg.probeClient
	if probeCli == nil {
		probeCli = newProbeClient(cfg.AllowPrivateDirect)
	}

	s := &Server{
		cfg:      cfg,
		upgrader: up,
		probeCli: probeCli,
		nowFn:    cfg.nowFn,
		sessions: make(map[string]*session),
		stopped:  make(chan struct{}),
	}
	s.upgradeLim = newLimiter(rateOr(cfg.UpgradeRatePerIP, defaultUpgradeRatePerIP), s.nowFn)
	s.globalLim = newLimiter(rateOr(cfg.GlobalRequestRate, defaultGlobalRequestRate), s.nowFn)

	s.wg.Add(1)
	go s.sweepLoop()
	return s, nil
}

func rateOr(v, def float64) float64 {
	if v == 0 {
		return def
	}
	return v // negative disables (see limiter)
}

// Close tears down every tunnel and stops background work.
func (s *Server) Close() error {
	s.stopOnce.Do(func() {
		close(s.stopped)
		s.mu.Lock()
		s.draining = true
		for _, sess := range s.sessions {
			sess.close()
		}
		s.sessions = map[string]*session{}
		s.mu.Unlock()
	})
	s.wg.Wait()
	return nil
}

// Drain stops accepting new tunnels while leaving existing ones serving, so
// an operator can migrate boxes to another relay before shutting down. Agents
// receive CodeDraining, which tells them to try their NEXT endpoint at once
// rather than backing off on this one.
func (s *Server) Drain() {
	s.mu.Lock()
	s.draining = true
	s.mu.Unlock()
	s.cfg.Logf("[relay] draining: no new tunnels will be accepted")
}

// Handler returns the relay's public HTTP handler.
//
// Route resolution order matters and is fixed: the reserved /_vulos-reach/
// paths are resolved FIRST, before any tunnel-name routing, so no registered
// name can ever shadow the control plane. (Names are single DNS labels and
// the control paths live at the apex, so this cannot collide today either —
// but ordering it explicitly means a future routing change cannot silently
// create the collision.)
func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case TunnelPath:
			s.handleTunnel(w, r)
			return
		case HealthPath:
			s.handleHealth(w, r)
			return
		}
		// The discovery role, on the apex host only — see the containment
		// note on ServerConfig.RendezvousHandler.
		if s.cfg.RendezvousHandler != nil && s.isApex(r) &&
			strings.HasPrefix(r.URL.Path, s.cfg.RendezvousPrefix) {
			s.cfg.RendezvousHandler.ServeHTTP(w, r)
			return
		}
		s.handleProxy(w, r)
	})
}

// isApex reports whether a request is addressed to the relay's own hostname
// rather than to a tunnel under it.
func (s *Server) isApex(r *http.Request) bool {
	return strings.EqualFold(hostOnly(r.Host), s.cfg.Domain)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	// Deliberately says nothing about which tunnels exist: an
	// unauthenticated scanner learns only that a relay is running here.
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	s.mu.RLock()
	draining := s.draining
	s.mu.RUnlock()
	status := "ok"
	if draining {
		status = "draining"
	}
	fmt.Fprintf(w, `{"status":%q,"protocol":%d}`, status, Version)
}

// --- agent registration ----------------------------------------------------

func (s *Server) handleTunnel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ip := s.clientIP(r)
	if !s.upgradeLim.allow(ip) {
		w.Header().Set("Retry-After", "5")
		http.Error(w, "too many tunnel attempts", http.StatusTooManyRequests)
		return
	}

	// AUTHENTICATE BEFORE UPGRADING. An unauthorised peer must never cause a
	// WebSocket, a yamux session, or a registry slot to be allocated.
	tok, ok := bearerToken(r.Header.Get(AuthHeader))
	if !ok {
		unauthorized(w)
		return
	}
	auth, err := s.cfg.Grants.Lookup(tok)
	if err != nil {
		// The response is identical for every failure reason. Distinguishing
		// "unknown token" from "expired grant" would let an attacker
		// enumerate which of their guesses were once valid.
		s.cfg.Logf("[relay] tunnel upgrade refused from %s: %v", ip, err)
		unauthorized(w)
		return
	}

	s.mu.RLock()
	draining, live := s.draining, len(s.sessions)
	s.mu.RUnlock()
	if draining {
		http.Error(w, "relay is draining", http.StatusServiceUnavailable)
		return
	}
	if live >= s.cfg.MaxAgents {
		http.Error(w, "relay is at capacity", http.StatusServiceUnavailable)
		return
	}

	ws, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		// Upgrade already wrote a response.
		return
	}
	conn := newWSConn(ws)

	if err := s.register(conn, auth, ip); err != nil {
		s.cfg.Logf("[relay] registration from %s (%s) failed: %v", ip, auth.Fingerprint, err)
		conn.Close()
		return
	}
}

// register performs the handshake and, on success, serves the session until
// it ends. It runs on the upgrade request's goroutine.
func (s *Server) register(conn *wsConn, auth *AuthResult, ip string) error {
	deadline := s.nowFn().Add(HandshakeTimeout)
	if err := conn.SetDeadline(deadline); err != nil {
		return err
	}

	typ, raw, err := conn.ws.ReadMessage()
	if err != nil {
		return fmt.Errorf("%w: reading register: %v", ErrHandshake, err)
	}
	if typ != websocket.TextMessage {
		s.refuse(conn, CodeBadRequest, "expected a text register frame")
		return fmt.Errorf("%w: register frame was type %d", ErrHandshake, typ)
	}
	var reg Register
	if err := decodeControl(raw, &reg); err != nil {
		s.refuse(conn, CodeBadRequest, "malformed register frame")
		return err
	}
	if reg.Type != TypeRegister {
		s.refuse(conn, CodeBadRequest, "expected a register frame")
		return fmt.Errorf("%w: got %q", ErrHandshake, reg.Type)
	}

	if err := ValidName(reg.Name); err != nil {
		s.refuse(conn, CodeBadRequest, err.Error())
		return err
	}
	// AUTHORISE THE NAME. The token was recognised at upgrade time and its
	// authorised name list came back with it; this is where we check it
	// covers THIS name. An agent does not get to pick a name its grant does
	// not cover.
	//
	// The check is against auth.Names rather than a second Authorize call
	// because the relay deliberately does not retain the raw token past the
	// upgrade — and auth.Names carries exactly the same authority, having
	// come from the same grant lookup.
	if !contains(auth.Names, reg.Name) {
		s.refuse(conn, CodeUnauthorized, "this grant does not authorise that name")
		return fmt.Errorf("grant %s may not claim %q", auth.Fingerprint, reg.Name)
	}

	// Optional direct endpoint: verified before it is ever advertised.
	var direct string
	if reg.Direct != "" && s.cfg.EnableDirectProbe {
		if err := s.probeDirect(reg.Direct); err != nil {
			// Non-fatal. The box stays relay-only, which always works.
			s.cfg.Logf("[relay] %s: direct endpoint %q not accepted: %v", reg.Name, reg.Direct, err)
		} else {
			direct = reg.Direct
			s.cfg.Logf("[relay] %s: direct endpoint %q verified", reg.Name, direct)
		}
	}

	// ORDERING — this sequence is load-bearing, not stylistic.
	//
	// The control channel and the multiplexed session share ONE WebSocket. A
	// yamux session begins writing (window updates, keepalive pings) the
	// instant it is constructed, from its own goroutine. So yamux must not
	// exist until this side has finished every control-channel write, or two
	// goroutines write the same connection concurrently — a data race, and a
	// peer that reads a binary yamux frame where it expects the text Ready
	// frame and fails the handshake.
	//
	// Therefore: reserve the name, send Ready, hand the connection to yamux.
	// In that order, with no overlap.
	sess := &session{
		name:        reg.Name,
		fingerprint: auth.Fingerprint,
		label:       auth.Label,
		connected:   s.nowFn(),
		direct:      direct,
		closed:      make(chan struct{}),
		limiter:     newLimiter(rateOr(s.cfg.RequestRatePerTunnel, defaultRequestRatePerTunnel), s.nowFn),
	}

	// (1) Reserve the name. The session is now in the routing table but not
	// serving; requests for it get 502 until activate() below.
	if err := s.attach(sess); err != nil {
		var ref *RefusedError
		if errors.As(err, &ref) {
			s.refuse(conn, ref.Code, ref.Message)
		} else {
			s.refuse(conn, CodeCapacity, err.Error())
		}
		return err
	}
	defer s.detach(sess)

	// (2) Send Ready — the LAST control-channel write on this connection.
	ready, err := encodeControl(Ready{
		Type: TypeReady, V: Version,
		PublicURL:      s.publicURLFor(reg.Name),
		RelayHost:      s.cfg.Domain,
		NodeID:         s.cfg.NodeID,
		DirectAccepted: direct != "",
	})
	if err != nil {
		return err
	}
	if err := conn.ws.WriteMessage(websocket.TextMessage, ready); err != nil {
		return err
	}

	// (3) Clear the handshake deadline and hand the connection to yamux. From
	// here nothing else writes it. The relay OPENS streams; the agent accepts.
	if err := conn.SetDeadline(time.Time{}); err != nil {
		return err
	}
	ysess, err := yamux.Client(conn, yamuxConfig(0, io.Discard), nil)
	if err != nil {
		return err
	}
	sess.activate(ysess, s.newProxy(sess))

	s.cfg.Logf("[relay] %s connected from %s (grant %s) — serving %s",
		reg.Name, ip, auth.Fingerprint, s.publicURLFor(reg.Name))

	// Block until the session dies. yamux surfaces that through its close
	// channel; nothing else needs to be read from the control connection.
	select {
	case <-ysess.CloseChan():
	case <-sess.closed:
	case <-s.stopped:
	}
	s.cfg.Logf("[relay] %s disconnected", reg.Name)
	return nil
}

func contains(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}

// attach installs a session in the routing table.
//
// # Same-credential registrations EVICT; different-credential ones are REFUSED
//
// A box that restarts leaves a session the relay has not yet noticed is dead
// (a half-open TCP connection can survive for minutes). If a re-registration
// were refused, the box would be unreachable for that whole window — the
// worst possible moment to be unreachable, right after a reboot. So a
// registration presenting the SAME credential fingerprint evicts the old
// session immediately.
//
// A registration presenting a DIFFERENT credential is refused. Two grants can
// legitimately both list a name; without this rule, a compromised secondary
// grant could kick a healthy box off its own name at will. Eviction is a
// convenience for the credential that already holds the name, never a lever
// for a different one.
func (s *Server) attach(sess *session) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.draining {
		return &RefusedError{Code: CodeDraining, Message: "relay is draining"}
	}
	if prev, ok := s.sessions[sess.name]; ok {
		if prev.fingerprint != sess.fingerprint {
			return &RefusedError{
				Code:    CodeNameInUse,
				Message: "that name is held by a different credential",
			}
		}
		s.cfg.Logf("[relay] %s: replacing the previous session (same grant %s)", sess.name, sess.fingerprint)
		go prev.close()
	} else if len(s.sessions) >= s.cfg.MaxAgents {
		return &RefusedError{Code: CodeCapacity, Message: "relay is at capacity"}
	}
	s.sessions[sess.name] = sess
	return nil
}

// detach removes a session, but only if it is still the current holder — a
// session that was evicted must not remove its replacement on the way out.
func (s *Server) detach(sess *session) {
	s.mu.Lock()
	if cur, ok := s.sessions[sess.name]; ok && cur == sess {
		delete(s.sessions, sess.name)
	}
	s.mu.Unlock()
	sess.close()
}

func (s *Server) refuse(conn *wsConn, code, msg string) {
	raw, err := encodeControl(ErrorFrame{Type: TypeError, V: Version, Code: code, Message: msg})
	if err != nil {
		return
	}
	_ = conn.ws.SetWriteDeadline(s.nowFn().Add(5 * time.Second))
	_ = conn.ws.WriteMessage(websocket.TextMessage, raw)
}

func unauthorized(w http.ResponseWriter) {
	// No WWW-Authenticate challenge: this endpoint is for agents, and a
	// challenge would prompt a browser that stumbled onto it for credentials.
	http.Error(w, "unauthorized", http.StatusUnauthorized)
}

func bearerToken(header string) (string, bool) {
	const p = "Bearer "
	if len(header) <= len(p) || !strings.EqualFold(header[:len(p)], p) {
		return "", false
	}
	tok := strings.TrimSpace(header[len(p):])
	return tok, tok != ""
}

// --- proxying --------------------------------------------------------------

func (s *Server) handleProxy(w http.ResponseWriter, r *http.Request) {
	name, ok := s.routeName(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	s.mu.RLock()
	sess := s.sessions[name]
	s.mu.RUnlock()
	if sess == nil {
		// Same response as an unknown name: a probe cannot distinguish
		// "no such box" from "that box is offline right now".
		http.NotFound(w, r)
		return
	}

	if !s.globalLim.allow("") || !sess.limiter.allow(name) {
		w.Header().Set("Retry-After", "1")
		http.Error(w, "rate limited", http.StatusTooManyRequests)
		return
	}

	proxy := sess.handler()
	if proxy == nil {
		// The name is reserved but the tunnel is not carrying traffic yet
		// (mid-handshake) or is shutting down. Accurate, and a sub-millisecond
		// window in practice.
		http.Error(w, "box unavailable", http.StatusBadGateway)
		return
	}

	if s.cfg.MaxRequestBytes > 0 && r.Body != nil {
		r.Body = http.MaxBytesReader(w, r.Body, s.cfg.MaxRequestBytes)
	}
	proxy.ServeHTTP(w, r)
}

// routeName resolves the tunnel name a request is addressed to.
func (s *Server) routeName(r *http.Request) (string, bool) {
	host := strings.ToLower(hostOnly(r.Host))
	if suffix := "." + s.cfg.Domain; strings.HasSuffix(host, suffix) {
		label := strings.TrimSuffix(host, suffix)
		// Exactly ONE label. "a.b.relay.example.com" must not resolve to a
		// tunnel: allowing it would mean a wildcard certificate covering
		// *.relay.example.com does not actually cover every name the relay
		// serves, and it would make name collisions possible.
		if label != "" && !strings.Contains(label, ".") && ValidName(label) == nil {
			return label, true
		}
		return "", false
	}
	if s.cfg.PathMode && strings.HasPrefix(r.URL.Path, "/t/") {
		rest := strings.TrimPrefix(r.URL.Path, "/t/")
		label, _, _ := strings.Cut(rest, "/")
		if ValidName(label) == nil {
			return label, true
		}
	}
	return "", false
}

// newProxy builds the reverse proxy for one session.
func (s *Server) newProxy(sess *session) *httputil.ReverseProxy {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return sess.sess.Open(ctx)
		},
		// One yamux stream per request; pooling them across requests would
		// mean a stream outliving the request that opened it, which
		// complicates accounting for no benefit (stream setup is a single
		// frame, not a handshake).
		DisableKeepAlives:     true,
		ResponseHeaderTimeout: 60 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}

	pathPrefix := ""
	if s.cfg.PathMode {
		pathPrefix = "/t/" + sess.name
	}

	return &httputil.ReverseProxy{
		Transport: transport,
		Rewrite: func(pr *httputil.ProxyRequest) {
			// The scheme/host are placeholders: the transport dials the
			// yamux session and ignores both. They must still be set, or the
			// stdlib refuses to build the request.
			pr.Out.URL.Scheme = "http"
			pr.Out.URL.Host = sess.name + ".tunnel.invalid"

			// THE TRUST BOUNDARY. Every vouched header is removed from the
			// client's request before any is set. Unconditional and
			// prefix-based, so a header added later is covered automatically.
			stripPrefixHeaders(pr.Out.Header)

			// Client-supplied forwarded headers are destroyed too. The agent
			// rewrites these from the vouched values as well, so a client has
			// to defeat two independent barriers, not one.
			pr.Out.Header.Del("X-Forwarded-For")
			pr.Out.Header.Del("X-Forwarded-Proto")
			pr.Out.Header.Del("X-Forwarded-Host")
			pr.Out.Header.Del("Forwarded")

			clientIP := s.clientIP(pr.In)
			pr.Out.Header.Set(HeaderClientIP, clientIP)
			pr.Out.Header.Set(HeaderProto, schemeOf(pr.In))
			pr.Out.Header.Set(HeaderRelay, s.cfg.Domain)
			pr.Out.Header.Set(HeaderName, sess.name)

			if pathPrefix != "" {
				pr.Out.URL.Path = strings.TrimPrefix(pr.Out.URL.Path, pathPrefix)
				if !strings.HasPrefix(pr.Out.URL.Path, "/") {
					pr.Out.URL.Path = "/" + pr.Out.URL.Path
				}
				pr.Out.URL.RawPath = ""
			}
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			// A dead tunnel is the box being offline, not a relay fault.
			// 502 with no detail: the client learns the box is unavailable
			// and nothing about the relay's internals.
			s.cfg.Logf("[relay] %s: proxy error: %v", sess.name, err)
			http.Error(w, "box unavailable", http.StatusBadGateway)
		},
	}
}

// clientIP resolves the caller's address, honouring X-Forwarded-For ONLY when
// the operator has declared a trusted proxy in front.
//
// When trusted, the RIGHTMOST entry is used. XFF is appended left to right, so
// the rightmost entry is the one the trusted proxy itself observed; every
// entry to its left was supplied by someone further out and is attacker-
// controlled. Taking the leftmost — the common mistake — hands the attacker
// their own choice of identity.
func (s *Server) clientIP(r *http.Request) string {
	if s.cfg.TrustProxyHeaders {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			cand := strings.TrimSpace(parts[len(parts)-1])
			if ip := net.ParseIP(cand); ip != nil {
				return ip.String()
			}
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func schemeOf(r *http.Request) string {
	if r.TLS != nil {
		return "https"
	}
	// Behind a trusted TLS-terminating proxy the client spoke https even
	// though this hop did not. Outside that case the header is not believed.
	return "http"
}

func (s *Server) publicURLFor(name string) string {
	if s.cfg.PathMode {
		return "https://" + s.cfg.Domain + "/t/" + name + "/"
	}
	return "https://" + name + "." + s.cfg.Domain
}

// --- status ----------------------------------------------------------------

// TunnelInfo is the admin view of one live tunnel. Credential-free.
type TunnelInfo struct {
	Name        string    `json:"name"`
	Label       string    `json:"label,omitempty"`
	Fingerprint string    `json:"grant_fingerprint"`
	PublicURL   string    `json:"public_url"`
	Direct      string    `json:"direct,omitempty"`
	ConnectedAt time.Time `json:"connected_at"`
}

// Tunnels returns the live roster. This is ADMIN-ONLY information — it names
// every box registered here — and the caller is responsible for serving it
// only on the loopback-bound admin listener.
func (s *Server) Tunnels() []TunnelInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]TunnelInfo, 0, len(s.sessions))
	for _, sess := range s.sessions {
		out = append(out, TunnelInfo{
			Name:        sess.name,
			Label:       sess.label,
			Fingerprint: sess.fingerprint,
			PublicURL:   s.publicURLFor(sess.name),
			Direct:      sess.direct,
			ConnectedAt: sess.connected,
		})
	}
	return out
}

// ServesHostname reports whether this relay would serve host — i.e. whether
// host is <name>.<domain> for a name some live grant authorises.
//
// It exists for automatic-TLS terminators (Caddy's on-demand TLS "ask"
// endpoint, and equivalents): without a gate, an on-demand issuer will attempt
// a certificate for every hostname anyone points at it, which is both an ACME
// rate-limit hazard and a way for a stranger to make your relay do work.
//
// It answers from GRANTS, not from live sessions, so a certificate is ready
// before a box first connects rather than after its first request fails.
func (s *Server) ServesHostname(host string) bool {
	host = strings.ToLower(hostOnly(strings.TrimSpace(host)))
	// The apex serves the health endpoint and the discovery role, so it needs
	// a certificate too — and it needs one before any box has registered.
	if host == s.cfg.Domain {
		return true
	}
	suffix := "." + s.cfg.Domain
	if !strings.HasSuffix(host, suffix) {
		return false
	}
	label := strings.TrimSuffix(host, suffix)
	if label == "" || strings.Contains(label, ".") {
		return false
	}
	return s.cfg.Grants.GrantsName(label)
}

// DirectFor returns the verified direct endpoint for a name, if any. This is
// what a peer-reachability resolve answers with.
func (s *Server) DirectFor(name string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sess := s.sessions[name]
	if sess == nil || sess.direct == "" {
		return "", false
	}
	return sess.direct, true
}

// sweepLoop rechecks live sessions against the grant store, so revocation
// reaches established tunnels. Without it, a revoked box stays connected
// indefinitely: a working tunnel never reconnects, so it would never re-present
// its credential for checking.
func (s *Server) sweepLoop() {
	defer s.wg.Done()
	t := time.NewTicker(revocationSweep)
	defer t.Stop()
	for {
		select {
		case <-s.stopped:
			return
		case <-t.C:
			s.sweepRevoked()
		}
	}
}

func (s *Server) sweepRevoked() {
	s.mu.RLock()
	var doomed []*session
	for _, sess := range s.sessions {
		if s.cfg.Grants.Revoked(sess.fingerprint, sess.name) {
			doomed = append(doomed, sess)
		}
	}
	s.mu.RUnlock()

	for _, sess := range doomed {
		s.cfg.Logf("[relay] %s: credential revoked or expired — closing tunnel", sess.name)
		s.detach(sess)
	}
}

// --- direct-endpoint ownership probe ---------------------------------------

// DirectProbePath and DirectProbeHeader MUST match the box-side constants in
// backend/internal/directlisten. They are redeclared here rather than
// imported so the relay role does not link the box's public-listener package —
// the two share only the wire strings, and a test asserts they stay equal.
const (
	DirectProbePath   = "/_vulos-direct/probe"
	DirectProbeHeader = "X-Vulos-Direct-Probe"
)

// probeDirect verifies that the advertised endpoint is reachable AND that
// whoever answers it controls it, by requiring a fresh nonce to be echoed.
//
// Without the nonce, an agent could advertise ANY https URL — a bank, a
// competitor, a service it wants traffic pointed at — and the relay would
// publish it to clients as "this box, but faster". The echo makes the claim
// self-proving: only something serving that endpoint can answer.
func (s *Server) probeDirect(endpoint string) error {
	u, err := url.Parse(endpoint)
	if err != nil {
		return fmt.Errorf("not a URL: %w", err)
	}
	if u.Scheme != "https" {
		return errors.New("direct endpoints must be https")
	}
	if u.Host == "" {
		return errors.New("no host")
	}
	if u.User != nil {
		return errors.New("must not embed credentials")
	}
	if u.Path != "" && u.Path != "/" {
		return errors.New("must be a bare origin, no path")
	}

	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	want := hex.EncodeToString(nonce)

	ctx, cancel := context.WithTimeout(context.Background(), directProbeTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		strings.TrimRight(endpoint, "/")+DirectProbePath, nil)
	if err != nil {
		return err
	}
	req.Header.Set(DirectProbeHeader, want)

	resp, err := s.probeCli.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("probe returned HTTP %d", resp.StatusCode)
	}
	// Bounded read: the response is a 32-character hex string, so anything
	// larger is a misbehaving or hostile endpoint.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 512))
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(body)) != want {
		return errors.New("nonce not echoed — the endpoint did not prove ownership")
	}
	return nil
}
