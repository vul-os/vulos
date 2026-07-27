// Package tunnel is Vulos's OWN reverse tunnel: the built-in way a box with
// no public IP is reached from the internet, with no third-party tunnel
// service and no dependency on any external relay project.
//
// # The shape
//
// A box dials ONE outbound wss:// connection to a relay and holds it open. A
// public client hits the relay; the relay opens a multiplexed stream down
// that already-open connection and reverse-proxies the request. Nothing on
// the box ever accepts an inbound connection, so it works behind NAT, CGNAT,
// and hotel wifi identically.
//
//	public client ──HTTPS──► relay (vulos relay serve, public IP)
//	                            │  yamux stream, 1 per request
//	                            ▼
//	                         box (agent embedded IN the OS process)
//	                            │  in-process http.Handler call
//	                            ▼
//	                         the OS's own handler chain
//
// # Two deliberate design choices
//
//  1. THE AGENT IS EMBEDDED, NOT A SIDECAR. The agent serves the OS's own
//     http.Handler directly, in-process. There is no loopback hop, which
//     means there is no loopback listener to secure, no SSRF surface pointing
//     at 127.0.0.1, and no second binary to install, supervise, or version-
//     skew against the OS. A tunnelled request runs the EXACT same handler
//     chain — auth, session, CSRF, rate limits, security headers — as a
//     request arriving on the box's own listener. "Arrived via tunnel" grants
//     a request nothing.
//
//  2. THE RELAY IS A ROLE OF THE SAME BINARY, not a separate product. `vulos
//     relay serve` turns any Vulos install with a public IP into a relay for
//     other boxes. Standing one up means running software you already run and
//     already trust, which is the whole point: no new vendor, no new supply
//     chain, no new operator relationship.
//
// # Layering (and why each layer is off-the-shelf)
//
//	HTTP/1.1  — stdlib http.Server on the agent, httputil.ReverseProxy on the
//	            relay. Protocol upgrades (WebSocket) pass through natively.
//	yamux     — stream multiplexing over one connection. Already in this
//	            module's dependency graph via libp2p, so it adds no new
//	            supply-chain surface.
//	WebSocket — the outbound transport. Chosen over a raw TCP/TLS socket
//	            because wss:// traverses corporate proxies, captive portals,
//	            and CDNs that pass nothing else, which is precisely the
//	            population of networks a tunnel exists to serve.
//	TLS       — ordinary https to the relay's public name.
//
// Almost nothing here is novel: the novel code is the handshake, the routing
// table, and the header-trust boundary. That is intentional. A bespoke mux or
// a bespoke framing layer would be the most likely place for a
// memory-corruption or desync bug, and neither would buy anything.
//
// # The header-trust boundary (the security-critical part)
//
// A request arriving at the relay is ATTACKER-CONTROLLED, including its
// headers. A request arriving at the agent came over an authenticated tunnel
// and carries metadata the agent must be able to trust (who the real client
// was, whether they spoke https). Those two facts are reconciled in exactly
// one place:
//
//   - The relay STRIPS every HeaderPrefix header from the inbound client
//     request before forwarding — unconditionally, with no exceptions — and
//     then sets the ones it vouches for itself.
//   - The agent trusts HeaderPrefix headers, and ONLY because they cannot
//     have come from anywhere but the relay it authenticated to.
//
// If the strip step were ever conditional, a client could forge its own
// source IP and https-ness. It is therefore implemented as an unconditional
// loop over the canonical prefix (see server.go's stripUntrusted), not as a
// list of known header names that a future header could be forgotten from.
package tunnel

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Version is the wire-protocol version. It appears in the control-channel
// PATH rather than in a negotiated field: an incompatible future protocol
// gets a different path and an old relay answers 404, which is an
// unambiguous, un-downgradeable failure. There is no version negotiation and
// deliberately no way for either side to be talked down to an older one.
const Version = 1

// TunnelPath is the relay endpoint an agent dials to establish its tunnel.
// It lives under a reserved prefix that can never collide with a tunnelled
// box's own routes, because the relay resolves this path before it resolves
// any tunnel name.
const TunnelPath = "/_vulos-reach/v1/tunnel"

// HealthPath is the relay's unauthenticated liveness endpoint. It reports
// only that the process is up — never the tunnel roster, which would tell an
// unauthenticated scanner exactly which boxes exist.
const HealthPath = "/_vulos-reach/v1/health"

// HeaderPrefix is the canonical prefix for every header the relay vouches
// for. The relay strips ALL headers with this prefix from inbound client
// requests before forwarding — see the package doc's trust-boundary note.
// Adding a new vouched header requires no change to the strip logic, which
// is exactly why the strip is prefix-based rather than a name list.
const HeaderPrefix = "X-Vulos-Reach-"

// Vouched headers the relay sets and the agent may trust.
const (
	// HeaderClientIP is the real client's IP as the relay observed it.
	HeaderClientIP = HeaderPrefix + "Client-Ip"
	// HeaderProto is the scheme the CLIENT used to reach the relay ("https"
	// or "http") — not the scheme inside the tunnel, which is always plain
	// HTTP over an already-encrypted transport.
	HeaderProto = HeaderPrefix + "Proto"
	// HeaderRelay is the relay's own public host, so a box serving several
	// relays can tell which one a request came through.
	HeaderRelay = HeaderPrefix + "Relay"
	// HeaderName is the tunnel name the request was routed to. The agent
	// checks it matches its own registration — a mismatch means the relay
	// misrouted, and the agent refuses rather than serving another box's
	// traffic.
	HeaderName = HeaderPrefix + "Name"
)

// AuthHeader carries the agent's bearer grant on the control-channel
// handshake. It is a standard Authorization header so the relay can reject an
// unauthorised agent BEFORE upgrading the connection — an unauthenticated
// peer never gets as far as allocating a WebSocket, a yamux session, or a
// registry slot.
const AuthHeader = "Authorization"

// Control-message size and timing bounds. The control channel carries two
// small JSON objects and then nothing but yamux frames, so these are tight
// on purpose: an agent that has not completed the handshake holds relay
// resources, and must not be able to hold them for long or fill them.
const (
	// MaxControlMessage bounds a single handshake frame.
	MaxControlMessage = 4 << 10 // 4 KiB
	// HandshakeTimeout bounds the whole register/ready exchange.
	HandshakeTimeout = 15 * time.Second
	// KeepAliveInterval is the yamux ping cadence. It also keeps the
	// WebSocket itself alive through idle-timeout middleboxes, which is why
	// there is no second, separate WebSocket ping loop.
	KeepAliveInterval = 30 * time.Second
	// WriteTimeout bounds a single write to the underlying transport before
	// the session is presumed wedged.
	WriteTimeout = 15 * time.Second
)

// MessageType identifies a control frame.
type MessageType string

const (
	// TypeRegister is agent -> relay, the first frame after upgrade.
	TypeRegister MessageType = "register"
	// TypeReady is relay -> agent, accepting the registration.
	TypeReady MessageType = "ready"
	// TypeError is relay -> agent, refusing it. The relay closes after.
	TypeError MessageType = "error"
)

// Register is the agent's opening frame.
//
// Note what is NOT here: the token. It rode the HTTP Authorization header on
// the upgrade request and was verified before the upgrade, so it never
// appears in a frame that might be logged by either side.
type Register struct {
	Type MessageType `json:"type"`
	V    int         `json:"v"`
	// Name is the tunnel name to claim. It must be one the presented grant
	// authorises; the relay does not take the agent's word for it.
	Name string `json:"name"`
	// Direct is an OPTIONAL public https base URL this box is also directly
	// reachable at. The relay verifies reachability and ownership before ever
	// advertising it — see the direct-probe path in server.go — so a hostile
	// agent cannot use this to point clients at a third party.
	Direct string `json:"direct,omitempty"`
	// Agent is a version string for operator-facing diagnostics only. It is
	// never used for any trust or compatibility decision.
	Agent string `json:"agent,omitempty"`
}

// Ready is the relay's acceptance frame.
type Ready struct {
	Type MessageType `json:"type"`
	V    int         `json:"v"`
	// PublicURL is where this box is now served, in whichever mode the relay
	// runs (subdomain or path). The agent logs it and reports it in status.
	PublicURL string `json:"public_url"`
	// RelayHost is the relay's own public host.
	RelayHost string `json:"relay_host"`
	// NodeID is the relay's stable identifier, for operator diagnostics.
	NodeID string `json:"node_id,omitempty"`
	// DirectAccepted reports whether the optional direct endpoint passed the
	// relay's ownership probe. False (with no error) simply means the box
	// stays relay-only, which is not a failure.
	DirectAccepted bool `json:"direct_accepted,omitempty"`
}

// ErrorFrame is the relay's refusal. Code is machine-readable; Message is for
// the operator's log.
type ErrorFrame struct {
	Type    MessageType `json:"type"`
	V       int         `json:"v"`
	Code    string      `json:"code"`
	Message string      `json:"message"`
}

// Refusal codes. These are stable strings: an agent keys its retry behaviour
// off them, and an operator greps for them.
const (
	// CodeUnauthorized: the grant does not authorise the requested name.
	// PERMANENT — retrying with the same config cannot succeed.
	CodeUnauthorized = "unauthorized"
	// CodeNameInUse: another live session already holds this name.
	// TRANSIENT — the other session may end.
	CodeNameInUse = "name_in_use"
	// CodeBadRequest: malformed or wrong-version frame. PERMANENT.
	CodeBadRequest = "bad_request"
	// CodeCapacity: the relay is at its agent limit. TRANSIENT.
	CodeCapacity = "capacity"
	// CodeDraining: the relay is shutting down and is not accepting new
	// sessions. TRANSIENT, and the signal to try the NEXT endpoint in the
	// set immediately rather than backing off on this one.
	CodeDraining = "draining"
)

// Permanent reports whether a refusal code means "this configuration can
// never work". The agent uses it to decide between a fast move to the next
// endpoint (transient) and a slow, loud retry (permanent) — a box that
// hammers a relay it is not authorised for is both useless and abusive.
func Permanent(code string) bool {
	switch code {
	case CodeUnauthorized, CodeBadRequest:
		return true
	}
	return false
}

// RefusedError is what the agent surfaces when a relay sends TypeError.
type RefusedError struct {
	Code    string
	Message string
}

func (e *RefusedError) Error() string {
	return fmt.Sprintf("relay refused the tunnel: %s (%s)", e.Message, e.Code)
}

// IsPermanent reports whether retrying this endpoint unchanged is pointless.
func (e *RefusedError) IsPermanent() bool { return Permanent(e.Code) }

// ErrHandshake is returned for a handshake that failed structurally (bad
// JSON, wrong version, unexpected frame) rather than by explicit refusal.
var ErrHandshake = errors.New("tunnel handshake failed")

// decodeControl parses one control frame, enforcing the size cap and the
// protocol version. Both checks happen before any field is used, so a
// mismatched or oversized peer is rejected without this side allocating on
// its behalf.
func decodeControl(raw []byte, into any) error {
	if len(raw) > MaxControlMessage {
		return fmt.Errorf("%w: control frame is %d bytes, max %d", ErrHandshake, len(raw), MaxControlMessage)
	}
	// Probe the envelope first so a version mismatch produces a clear error
	// rather than a confusing field-level one.
	var env struct {
		Type MessageType `json:"type"`
		V    int         `json:"v"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("%w: %v", ErrHandshake, err)
	}
	if env.V != Version {
		return fmt.Errorf("%w: peer speaks protocol v%d, this build speaks v%d", ErrHandshake, env.V, Version)
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		return fmt.Errorf("%w: %v", ErrHandshake, err)
	}
	return nil
}

// encodeControl marshals a control frame and enforces the same size cap on
// the way out, so this side can never emit a frame its peer must reject.
func encodeControl(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	if len(raw) > MaxControlMessage {
		return nil, fmt.Errorf("control frame is %d bytes, max %d", len(raw), MaxControlMessage)
	}
	return raw, nil
}

// ValidName enforces the single-DNS-label rule for a tunnel name. The name
// becomes both a hostname component (<name>.relay.example.com) and a path
// segment (/t/<name>/), so anything outside this alphabet is either
// unrepresentable in one position or an injection risk in the other.
//
// It is deliberately a copy of the same rule in the reach package rather than
// a shared import: this package is compiled into the RELAY, which must not
// depend on the box-side endpoint-set machinery, and a validation rule this
// short is safer duplicated than coupled. The two are locked together by
// TestNameRuleMatchesReach.
func ValidName(name string) error {
	if name == "" {
		return errors.New("name is required")
	}
	if len(name) > 63 {
		return fmt.Errorf("name %q is longer than 63 bytes", name)
	}
	if name[0] == '-' || name[len(name)-1] == '-' {
		return fmt.Errorf("name %q must not start or end with a hyphen", name)
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-':
		default:
			return fmt.Errorf("name %q contains %q — names are lowercase a-z, 0-9 and hyphen", name, string(c))
		}
	}
	return nil
}
