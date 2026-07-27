package tunnel

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	yamux "github.com/libp2p/go-yamux/v5"
)

// agent.go — the box side of the tunnel, EMBEDDED in the OS process.
//
// # One link per relay, all held at once
//
// The agent does not pick a relay. It holds a live tunnel to EVERY configured
// relay simultaneously, because relay tunnels are affinity-bound: a client
// arriving at relay B cannot be served a box whose tunnel is only on relay A,
// since relays do not forward to one another. Holding all of them is what
// makes a second relay actually redundant rather than decorative — DNS or the
// client can move between them with no coordination and no failover delay,
// and a relay going down costs nothing because the other link was never idle.
//
// The cost is one held connection per relay. That is a socket and a keepalive
// ping every 30s, which is negligible next to the alternative (an outage
// lasting as long as it takes to notice and re-point a box).
//
// # Links fail independently
//
// Each link runs its own connect/serve/backoff loop. A relay that is down,
// draining, or refusing this box's grant affects only its own link; the
// others keep serving. There is deliberately no shared state between links
// beyond the handler they serve, so no single relay's behaviour can degrade
// another's.

// Target is one relay this agent maintains a tunnel to.
//
// It is a local struct rather than a reach.Endpoint on purpose: this package
// is compiled into the RELAY as well as the box, and the relay has no
// business linking the box-side endpoint-set machinery. The wiring layer
// converts (see TargetsFromEndpoints in the reachwire package).
type Target struct {
	// WSURL is the relay's WebSocket base, e.g. "wss://relay.example.com".
	WSURL string
	// Name is the tunnel name to claim.
	Name string
	// Token is the bearer grant. SECRET — never logged.
	Token string
	// Label identifies this target in logs and status. Conventionally the
	// relay's https base URL. Never contains the token.
	Label string
}

// LinkState is the coarse state of one link, for status surfaces.
type LinkState string

const (
	// LinkConnecting: dialling or handshaking.
	LinkConnecting LinkState = "connecting"
	// LinkUp: the tunnel is established and serving.
	LinkUp LinkState = "up"
	// LinkBackoff: the last attempt failed; waiting to retry.
	LinkBackoff LinkState = "backoff"
	// LinkRefused: the relay permanently refused this configuration. The
	// link keeps retrying, but slowly and loudly — see permanentRetry.
	LinkRefused LinkState = "refused"
	// LinkStopped: the agent is shutting down.
	LinkStopped LinkState = "stopped"
)

// LinkStatus is the operator-facing snapshot of one link. Token-free.
type LinkStatus struct {
	Label     string    `json:"label"`
	Name      string    `json:"name"`
	State     LinkState `json:"state"`
	PublicURL string    `json:"public_url,omitempty"`
	LastError string    `json:"last_error,omitempty"`
	// Since is when the link entered its current state.
	Since time.Time `json:"since"`
	// Attempts is the count of consecutive failed connect attempts.
	Attempts int `json:"attempts,omitempty"`
	// DirectAccepted reports whether the relay verified and is advertising
	// this box's optional direct endpoint.
	DirectAccepted bool `json:"direct_accepted,omitempty"`
}

// AgentConfig configures the embedded agent.
type AgentConfig struct {
	// Targets are the relays to hold tunnels to. An empty slice is valid and
	// means "no tunnels" — the expected posture for a box with a public IP.
	Targets []Target

	// Handler is the OS's own http.Handler — the SAME one the box's direct
	// listener serves, so a tunnelled request runs an identical auth,
	// session, CSRF, rate-limit, and security-header chain. Required when
	// Targets is non-empty.
	Handler http.Handler

	// Direct is an OPTIONAL public https base URL this box is also directly
	// reachable at. Relays verify reachability and ownership before
	// advertising it; failure is non-fatal and simply leaves the box
	// relay-only.
	Direct string

	// UserAgent is a diagnostic version string sent in the register frame.
	UserAgent string

	// MaxConcurrentStreams caps in-flight requests per tunnel. Zero uses
	// DefaultMaxConcurrentStreams. This is genuine backpressure: the box is
	// usually the smallest machine in the path.
	MaxConcurrentStreams uint32

	// Logf receives operator-facing log lines. Never called with a token.
	Logf func(format string, args ...any)

	// OnState is called whenever a link changes state, so the wiring layer
	// can feed the endpoint set's health tracker. Optional; must not block.
	OnState func(status LinkStatus)

	// dialer is a test seam. Nil uses a hardened default.
	dialer *websocket.Dialer
}

// DefaultMaxConcurrentStreams bounds concurrent in-flight requests per
// tunnel. Sized for a small box: enough that a browser opening its usual six
// connections plus background sync never queues, low enough that a burst
// cannot exhaust a Raspberry Pi's memory.
const DefaultMaxConcurrentStreams = 128

// Backoff bounds for a link's reconnect loop.
const (
	linkBaseBackoff = 500 * time.Millisecond
	linkMaxBackoff  = 30 * time.Second
	// permanentRetry is the (long) interval used after a PERMANENT refusal —
	// a grant that does not authorise this name cannot start working because
	// time passed, but the operator may fix the relay's grants file, so the
	// link must not give up entirely. Retrying every 30s for a
	// configuration that cannot work would be abusive to the relay; every 5
	// minutes is a compromise between "notices the fix" and "not a nuisance".
	permanentRetry = 5 * time.Minute
)

// Agent maintains one tunnel per configured relay.
type Agent struct {
	cfg    AgentConfig
	dialer *websocket.Dialer

	mu     sync.RWMutex
	status map[string]LinkStatus // keyed by Target.Label

	cancel context.CancelFunc
	wg     sync.WaitGroup
	closed bool
}

// NewAgent validates cfg and returns an agent that has not started yet.
//
// Validation is strict and up front: a malformed target is a startup error,
// not a link that quietly never connects. An operator who typo'd a relay URL
// must find out at boot, from a log line naming the target, rather than by
// noticing weeks later that their redundancy was imaginary.
func NewAgent(cfg AgentConfig) (*Agent, error) {
	if len(cfg.Targets) > 0 && cfg.Handler == nil {
		return nil, errors.New("tunnel: Handler is required when Targets is non-empty")
	}
	seen := make(map[string]struct{}, len(cfg.Targets))
	for i, t := range cfg.Targets {
		if err := ValidName(t.Name); err != nil {
			return nil, fmt.Errorf("tunnel: target %d: %w", i, err)
		}
		if strings.TrimSpace(t.Token) == "" {
			return nil, fmt.Errorf("tunnel: target %d (%s): token is required", i, t.Label)
		}
		u, err := url.Parse(t.WSURL)
		if err != nil || u.Host == "" {
			return nil, fmt.Errorf("tunnel: target %d (%s): bad WSURL %q", i, t.Label, t.WSURL)
		}
		if u.Scheme != "wss" && u.Scheme != "ws" {
			return nil, fmt.Errorf("tunnel: target %d (%s): WSURL scheme %q, want wss", i, t.Label, u.Scheme)
		}
		label := t.Label
		if label == "" {
			label = t.WSURL
		}
		if _, dup := seen[label]; dup {
			return nil, fmt.Errorf("tunnel: target %d duplicates label %q", i, label)
		}
		seen[label] = struct{}{}
	}
	if cfg.Direct != "" {
		u, err := url.Parse(cfg.Direct)
		if err != nil || u.Scheme != "https" || u.Host == "" {
			return nil, fmt.Errorf("tunnel: Direct must be an https URL with a host, got %q", cfg.Direct)
		}
	}
	if cfg.Logf == nil {
		cfg.Logf = func(string, ...any) {}
	}
	if cfg.MaxConcurrentStreams == 0 {
		cfg.MaxConcurrentStreams = DefaultMaxConcurrentStreams
	}

	d := cfg.dialer
	if d == nil {
		d = &websocket.Dialer{
			HandshakeTimeout: HandshakeTimeout,
			// The relay is reached over ordinary public TLS. No custom roots,
			// no InsecureSkipVerify, no way to configure either — a tunnel
			// that could be talked into trusting an arbitrary certificate
			// would hand every request on this box to whoever presented it.
			TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12},
			// Compression is disabled: the tunnel carries already-compressed
			// TLS records from the client's perspective and mixing attacker-
			// influenced plaintext with secrets under one compressor is the
			// CRIME/BREACH shape. Nothing to gain, a known hazard to avoid.
			EnableCompression: false,
		}
	}

	a := &Agent{cfg: cfg, dialer: d, status: make(map[string]LinkStatus, len(cfg.Targets))}
	for _, t := range cfg.Targets {
		a.status[a.labelOf(t)] = LinkStatus{
			Label: a.labelOf(t), Name: t.Name, State: LinkConnecting, Since: time.Now(),
		}
	}
	return a, nil
}

func (a *Agent) labelOf(t Target) string {
	if t.Label != "" {
		return t.Label
	}
	return t.WSURL
}

// Start launches one goroutine per target. It returns immediately; links
// connect in the background and retry forever until Close. A zero-target
// agent is a no-op, which is what lets the wiring layer construct one
// unconditionally.
func (a *Agent) Start(ctx context.Context) {
	a.mu.Lock()
	if a.closed || a.cancel != nil {
		a.mu.Unlock()
		return
	}
	ctx, a.cancel = context.WithCancel(ctx)
	a.mu.Unlock()

	for _, t := range a.cfg.Targets {
		a.wg.Add(1)
		go func(t Target) {
			defer a.wg.Done()
			a.runLink(ctx, t)
		}(t)
	}
}

// Close stops every link and waits for them to finish. Safe to call twice.
func (a *Agent) Close() error {
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return nil
	}
	a.closed = true
	cancel := a.cancel
	a.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	a.wg.Wait()
	return nil
}

// Status returns the current, token-free snapshot of every link.
func (a *Agent) Status() []LinkStatus {
	a.mu.RLock()
	defer a.mu.RUnlock()
	out := make([]LinkStatus, 0, len(a.status))
	for _, t := range a.cfg.Targets {
		if st, ok := a.status[a.labelOf(t)]; ok {
			out = append(out, st)
		}
	}
	return out
}

// setState records a link's state and notifies the wiring layer.
func (a *Agent) setState(label string, mutate func(*LinkStatus)) {
	a.mu.Lock()
	st := a.status[label]
	prev := st.State
	mutate(&st)
	if st.State != prev {
		st.Since = time.Now()
	}
	a.status[label] = st
	onState := a.cfg.OnState
	a.mu.Unlock()

	if onState != nil {
		onState(st)
	}
}

// runLink is one relay's connect/serve/backoff loop. It returns only when ctx
// is cancelled.
func (a *Agent) runLink(ctx context.Context, t Target) {
	label := a.labelOf(t)
	attempts := 0

	for {
		if ctx.Err() != nil {
			a.setState(label, func(s *LinkStatus) { s.State = LinkStopped })
			return
		}

		a.setState(label, func(s *LinkStatus) {
			s.State = LinkConnecting
			s.Attempts = attempts
		})

		err := a.serveOnce(ctx, t)
		if ctx.Err() != nil {
			a.setState(label, func(s *LinkStatus) { s.State = LinkStopped })
			return
		}

		attempts++
		var refused *RefusedError
		permanent := errors.As(err, &refused) && refused.IsPermanent()

		wait := linkBackoff(attempts)
		if permanent {
			wait = permanentRetry
			a.cfg.Logf("[reach] %s: relay REFUSED this configuration: %v — fix the relay's grant for name %q; retrying in %s",
				label, err, t.Name, wait)
		} else if err != nil && !errors.Is(err, io.EOF) {
			a.cfg.Logf("[reach] %s: tunnel down (%v); reconnecting in %s", label, err, wait)
		} else {
			a.cfg.Logf("[reach] %s: tunnel closed; reconnecting in %s", label, wait)
		}

		a.setState(label, func(s *LinkStatus) {
			if permanent {
				s.State = LinkRefused
			} else {
				s.State = LinkBackoff
			}
			s.Attempts = attempts
			s.PublicURL = ""
			s.DirectAccepted = false
			if err != nil {
				s.LastError = err.Error()
			}
		})

		select {
		case <-ctx.Done():
			a.setState(label, func(s *LinkStatus) { s.State = LinkStopped })
			return
		case <-time.After(wait):
		}
	}
}

// linkBackoff is exponential with full jitter, floored so a flapping relay
// cannot become a hot loop and capped so recovery is never slower than
// linkMaxBackoff.
func linkBackoff(attempts int) time.Duration {
	if attempts < 1 {
		attempts = 1
	}
	d := linkBaseBackoff
	for i := 1; i < attempts && d < linkMaxBackoff; i++ {
		d *= 2
	}
	if d > linkMaxBackoff {
		d = linkMaxBackoff
	}
	j := time.Duration(rand.Float64() * float64(d))
	if j < linkBaseBackoff {
		j = linkBaseBackoff
	}
	return j
}

// serveOnce establishes one tunnel and serves it until it fails. The returned
// error explains why it ended; nil or io.EOF means an orderly close.
func (a *Agent) serveOnce(ctx context.Context, t Target) error {
	label := a.labelOf(t)

	dialCtx, cancelDial := context.WithTimeout(ctx, HandshakeTimeout)
	defer cancelDial()

	hdr := http.Header{}
	hdr.Set(AuthHeader, "Bearer "+t.Token)
	// A diagnostic only. The relay authorises from the token, never from this.
	if a.cfg.UserAgent != "" {
		hdr.Set("User-Agent", a.cfg.UserAgent)
	}

	ws, resp, err := a.dialer.DialContext(dialCtx, strings.TrimRight(t.WSURL, "/")+TunnelPath, hdr)
	if err != nil {
		// A 401/403 on the upgrade is the relay rejecting the grant BEFORE
		// allocating anything. Surface it as the permanent refusal it is, so
		// the loop slows down instead of hammering.
		if resp != nil {
			defer resp.Body.Close()
			switch resp.StatusCode {
			case http.StatusUnauthorized, http.StatusForbidden:
				return &RefusedError{
					Code:    CodeUnauthorized,
					Message: fmt.Sprintf("relay returned HTTP %d to the tunnel upgrade", resp.StatusCode),
				}
			case http.StatusNotFound:
				return &RefusedError{
					Code:    CodeBadRequest,
					Message: "relay has no v1 tunnel endpoint (not a Vulos relay, or an older protocol version)",
				}
			}
			return fmt.Errorf("dial %s: %w (HTTP %d)", label, err, resp.StatusCode)
		}
		return fmt.Errorf("dial %s: %w", label, err)
	}

	conn := newWSConn(ws)
	defer conn.Close()

	ready, err := a.handshake(conn, t)
	if err != nil {
		return err
	}

	a.cfg.Logf("[reach] %s: tunnel up — this box is served at %s%s",
		label, ready.PublicURL, directNote(ready.DirectAccepted))
	a.setState(label, func(s *LinkStatus) {
		s.State = LinkUp
		s.PublicURL = ready.PublicURL
		s.LastError = ""
		s.Attempts = 0
		s.DirectAccepted = ready.DirectAccepted
	})

	// The agent ACCEPTS streams; the relay opens one per request.
	sess, err := yamux.Server(conn, yamuxConfig(a.cfg.MaxConcurrentStreams, io.Discard), nil)
	if err != nil {
		return fmt.Errorf("%s: yamux: %w", label, err)
	}
	defer sess.Close()

	srv := &http.Server{
		Handler: a.tunnelHandler(t, ready.RelayHost),
		// The relay has already read the client's headers once; these bound
		// a MISBEHAVING RELAY, not a slow client, so they can be tight.
		ReadHeaderTimeout: 20 * time.Second,
		IdleTimeout:       2 * time.Minute,
		ErrorLog:          nil,
	}
	// Shut the HTTP server down when the agent is cancelled, so Close()
	// does not wait on in-flight requests forever.
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = srv.Shutdown(shutCtx)
			_ = sess.Close()
		case <-done:
		}
	}()
	defer close(done)

	err = srv.Serve(&sessionListener{sess: sess, addr: tunnelAddr{relay: ready.RelayHost}})
	if errors.Is(err, http.ErrServerClosed) || errors.Is(err, io.EOF) {
		return nil
	}
	return err
}

func directNote(accepted bool) string {
	if accepted {
		return " (direct endpoint verified — clients will prefer it)"
	}
	return ""
}

// handshake performs the register/ready exchange under a bounded deadline.
func (a *Agent) handshake(conn *wsConn, t Target) (*Ready, error) {
	deadline := time.Now().Add(HandshakeTimeout)
	if err := conn.SetDeadline(deadline); err != nil {
		return nil, err
	}
	// Clear the handshake deadline afterwards: yamux manages its own
	// per-write timeouts and a lingering read deadline would kill an idle
	// but perfectly healthy tunnel.
	defer func() { _ = conn.SetDeadline(time.Time{}) }()

	reg, err := encodeControl(Register{
		Type: TypeRegister, V: Version,
		Name: t.Name, Direct: a.cfg.Direct, Agent: a.cfg.UserAgent,
	})
	if err != nil {
		return nil, err
	}
	if err := conn.ws.WriteMessage(websocket.TextMessage, reg); err != nil {
		return nil, fmt.Errorf("%w: sending register: %v", ErrHandshake, err)
	}

	typ, raw, err := conn.ws.ReadMessage()
	if err != nil {
		return nil, fmt.Errorf("%w: awaiting ready: %v", ErrHandshake, err)
	}
	if typ != websocket.TextMessage {
		return nil, fmt.Errorf("%w: expected a text control frame, got type %d", ErrHandshake, typ)
	}

	var env struct {
		Type MessageType `json:"type"`
	}
	// A malformed envelope is caught by decodeControl below; this probe only
	// chooses which concrete type to decode into.
	_ = decodeControl(raw, &env)

	if env.Type == TypeError {
		var ef ErrorFrame
		if err := decodeControl(raw, &ef); err != nil {
			return nil, err
		}
		return nil, &RefusedError{Code: ef.Code, Message: ef.Message}
	}

	var ready Ready
	if err := decodeControl(raw, &ready); err != nil {
		return nil, err
	}
	if ready.Type != TypeReady {
		return nil, fmt.Errorf("%w: expected %q, got %q", ErrHandshake, TypeReady, ready.Type)
	}
	return &ready, nil
}

// tunnelHandler wraps the OS handler with the ONE translation between the
// relay's vouched headers and the request fields the rest of the OS already
// trusts.
//
// # Why translate rather than pass through
//
// Every existing security decision in the OS keys off r.RemoteAddr and
// r.TLS — rate limiting, step-up, secure-cookie selection, origin checks.
// If the tunnel left those reflecting the tunnel itself, then:
//
//   - every tunnelled client would share ONE rate-limit bucket, so one
//     attacker would lock out every legitimate remote user; and
//   - r.TLS would be nil, so session cookies would lose the Secure attribute
//     on exactly the requests that traversed the public internet.
//
// Translating here, once, at the trust boundary, means no downstream handler
// needs to know tunnels exist — and, critically, no downstream handler needs
// to be taught to trust a header, which is the mistake that turns a proxy
// into a spoofing oracle.
func (a *Agent) tunnelHandler(t Target, relayHost string) http.Handler {
	next := a.cfg.Handler
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// MISROUTING CHECK. The relay tells us which tunnel name it routed
		// to. If it is not ours, the relay is misconfigured or compromised;
		// serving the request anyway would mean answering as another box.
		if got := r.Header.Get(HeaderName); got != "" && got != t.Name {
			http.Error(w, "misdirected request", http.StatusMisdirectedRequest)
			return
		}

		clientIP := r.Header.Get(HeaderClientIP)
		proto := r.Header.Get(HeaderProto)
		via := r.Header.Get(HeaderRelay)
		if via == "" {
			via = relayHost
		}

		// Consume the vouched headers: nothing downstream should ever see
		// them, so no downstream handler can grow a dependency on trusting
		// them, and a future non-tunnel code path cannot be fooled by a
		// client that sets them.
		stripPrefixHeaders(r.Header)

		// r.RemoteAddr becomes the REAL client, so every existing per-client
		// decision in the OS is correct without changes. An unparseable or
		// absent value leaves the tunnel address in place, which fails
		// CLOSED: such a request shares a bucket rather than getting a
		// forged, attacker-chosen one.
		if ip := net.ParseIP(clientIP); ip != nil {
			r.RemoteAddr = net.JoinHostPort(ip.String(), "0")
		}

		// Rewrite the forwarded headers from the vouched values, destroying
		// anything the client sent. The relay already strips these, so this
		// is the second of two independent barriers.
		if clientIP != "" {
			r.Header.Set("X-Forwarded-For", clientIP)
		} else {
			r.Header.Del("X-Forwarded-For")
		}
		if proto != "" {
			r.Header.Set("X-Forwarded-Proto", proto)
		} else {
			r.Header.Del("X-Forwarded-Proto")
		}

		// SYNTHETIC TLS STATE. The client's connection to the relay was TLS
		// and the relay's connection to this box is TLS; only the relay's own
		// memory ever holds plaintext. From the client's perspective — which
		// is the perspective every r.TLS check in this codebase is asking
		// about — the request arrived over a secure transport, so r.TLS must
		// be non-nil or session cookies silently lose Secure and step-up
		// treats a public request as local.
		//
		// This is a deliberate, load-bearing assertion, and it is exactly as
		// trustworthy as the relay: a relay that lies here is a relay that
		// could already read and rewrite the request. It is set ONLY from the
		// vouched header, never from anything a client can influence.
		if proto == "https" {
			r.TLS = &tls.ConnectionState{
				Version:           tls.VersionTLS13,
				HandshakeComplete: true,
				ServerName:        hostOnly(r.Host),
			}
		}

		next.ServeHTTP(w, r)
	})
}

// stripPrefixHeaders removes every HeaderPrefix header. Prefix-based by
// design: adding a vouched header later must not require remembering to
// extend a list here.
func stripPrefixHeaders(h http.Header) {
	for k := range h {
		if strings.HasPrefix(http.CanonicalHeaderKey(k), HeaderPrefix) {
			h.Del(k)
		}
	}
}

// hostOnly strips any port from a Host header value.
func hostOnly(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		return h
	}
	return host
}
