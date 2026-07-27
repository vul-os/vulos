// Package reach is the SINGLE source of truth for the SET of relay endpoints
// this box reaches the outside world through — Vulos's OWN built-in
// reachability stack.
//
// # Why this exists
//
// Before this package, every relay-consuming seam in the OS read the same
// three environment variables directly — VULOS_RELAY_BASE_URL /
// VULOS_RELAY_NAME / VULOS_RELAY_TOKEN — and each of them held exactly ONE
// string. That meant:
//
//   - One relay. If it was down, or seized, or its operator went away, every
//     remote path the box had (peer resolve, cross-instance notify fan-out,
//     WAN peer discovery, HTTP ingress) went dark together. A single node was
//     load-bearing for the whole box, which is precisely the property the
//     substrate spec (KOTVA 4.2.1) says no component may have.
//   - No failover. A dead relay stayed selected until a human edited the
//     environment and restarted the process.
//   - No cross-check. A relay answering "where is peer X" was believed
//     unconditionally, because there was nothing to compare its answer to.
//
// This package replaces that single string with an ORDERED, HEALTH-TRACKED
// SET, and gives every consumer one resolver over it. Consumers ask for
// Ordered() and walk it; they never read the environment and never pin one
// endpoint.
//
// # The shape this implements (and why)
//
// KOTVA 4.2.1(3) calls for a home rendezvous set of >= 3 nodes under DISJOINT
// operators, cross-checked, so no single operator can censor or equivocate
// about where a peer is. This package is the box-side half of that: it holds
// N endpoints, prefers healthy ones, degrades rather than going dark, and
// exposes All() so a caller that cares about equivocation (peer resolve) can
// fan out and compare answers instead of trusting one.
//
// It is deliberately NOT a load balancer. Relay tunnels are affinity-bound —
// a client arriving at relay B cannot be served a box whose tunnel is held by
// relay A, because relays do not forward to each other. Ordered() therefore
// expresses PREFERENCE (try this one, then that one), never round-robin.
//
// # Fail-safe posture
//
//   - An endpoint that fails is marked down with exponential backoff + jitter,
//     but is NEVER removed. Ordered() returns healthy endpoints first and
//     unhealthy ones after, so a box whose entire set is currently failing
//     keeps retrying rather than reporting "no relays" and going dark.
//   - A malformed entry is rejected at load time and the whole set is refused,
//     rather than silently starting with a subset the operator did not intend.
//     Reachability config that half-applied would be worse than not starting.
//   - An empty set is a valid, expected state: a box with a public IP (or one
//     that only ever needs its LAN) runs with no relays at all. Callers must
//     treat Len()==0 as "no relay configured" and degrade, never as an error.
//
// # Security
//
//   - Tokens are bearer credentials. They are never logged, never returned by
//     Public(), and never placed in a URL. Redact() exists so callers can log
//     an endpoint safely.
//   - Endpoint URLs MUST be https (the token rides these requests). Plaintext
//     http is accepted ONLY for loopback targets and ONLY when
//     VULOS_RELAY_ALLOW_INSECURE=1 is set in the box's own process
//     environment — a developer affordance that cannot be reached from any
//     HTTP surface.
//   - URLs carrying userinfo (https://user:pass@host) are rejected outright:
//     credentials belong in the Token field, where the redaction rules apply.
//   - The set is size-capped (MaxEndpoints) so a hostile or fat-fingered
//     config cannot make every resolve fan out unboundedly.
package reach

import (
	"fmt"
	"math/rand/v2"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

// MaxEndpoints bounds how many relays one box may be configured with. Fan-out
// paths (peer resolve cross-check) iterate the whole set, so this is also the
// bound on that work. Eight is far past the >= 3 the substrate spec asks for
// and well short of anything that would make a fan-out expensive.
const MaxEndpoints = 8

// Name/token/URL length caps. Generous for any real deployment, tight enough
// that validation never walks attacker-sized input.
const (
	MaxNameLen   = 63  // one DNS label
	MaxURLLen    = 512 //
	MaxTokenLen  = 512 //
	MaxRegionLen = 64  //
)

// Backoff schedule for an endpoint that fails. Doubling from baseBackoff,
// capped at maxBackoff, with full jitter so a box with several endpoints that
// all fail at once (upstream outage) does not retry them in lockstep.
const (
	baseBackoff = 5 * time.Second
	maxBackoff  = 5 * time.Minute
)

// nowFn is the clock seam (tests override it). Mirrors relayconfig's pattern.
var nowFn = time.Now

// jitterFn returns a fraction in [0,1) used for full-jitter backoff. Seam for
// deterministic tests.
var jitterFn = rand.Float64

// Endpoint is one relay this box can reach the world through.
//
// A relay is identified by BaseURL. Name and Token are this box's IDENTITY ON
// THAT RELAY: the same box legitimately holds a different grant on each relay
// it registers with, so these are per-endpoint rather than global.
type Endpoint struct {
	// BaseURL is the relay's https base URL, e.g. "https://relay.example.com".
	// No trailing slash (Load normalises). A path component is allowed, for a
	// relay served under a prefix by someone's reverse proxy.
	BaseURL string `json:"url"`
	// Name is the DNS-label-safe name this box claims on that relay. It
	// becomes the public host, e.g. "box1" -> https://box1.relay.example.com.
	Name string `json:"name"`
	// Token is the bearer grant the relay authorised this box's Name with.
	// SECRET: never logged, never serialised back out by Public().
	Token string `json:"token"`
	// Region is an optional operator label ("eu-central") used only for
	// display and for preferring a nearby relay. Never interpreted.
	Region string `json:"region,omitempty"`
	// Priority orders preference; lower is tried first. Equal priorities keep
	// configuration order. Defaults to configuration order when unset.
	Priority int `json:"priority,omitempty"`
}

// WSURL returns the WebSocket control-channel URL for this endpoint:
// https -> wss, http -> ws. The tunnel agent dials this.
func (e Endpoint) WSURL() string {
	switch {
	case strings.HasPrefix(e.BaseURL, "https://"):
		return "wss://" + strings.TrimPrefix(e.BaseURL, "https://")
	case strings.HasPrefix(e.BaseURL, "http://"):
		return "ws://" + strings.TrimPrefix(e.BaseURL, "http://")
	}
	return e.BaseURL
}

// PublicURL is where this box is served from on that relay, in subdomain
// mode: https://<name>.<relay-host>. Path-mode relays serve /t/<name>/
// instead; PathModeURL builds that form.
func (e Endpoint) PublicURL() string {
	u, err := url.Parse(e.BaseURL)
	if err != nil || u.Host == "" {
		return ""
	}
	u.Host = e.Name + "." + u.Host
	return strings.TrimRight(u.String(), "/")
}

// PathModeURL is the no-wildcard-DNS form: https://<relay>/t/<name>/.
func (e Endpoint) PathModeURL() string {
	return strings.TrimRight(e.BaseURL, "/") + "/t/" + e.Name + "/"
}

// Redact returns a copy with Token cleared — the ONLY form safe to log or to
// hand to any surface a user can read.
func (e Endpoint) Redact() Endpoint {
	e.Token = ""
	return e
}

// String is deliberately token-free so an Endpoint cannot leak a credential
// through a stray %v in a log line.
func (e Endpoint) String() string {
	if e.Name == "" {
		return e.BaseURL
	}
	return e.Name + "@" + e.BaseURL
}

// health is the per-endpoint failure state Set tracks.
type health struct {
	failures  int
	downUntil time.Time
	lastErr   string
	lastOK    time.Time
}

// Set is an ordered, health-tracked collection of relay endpoints. The zero
// value is an empty set and is safe to use — every method tolerates it, which
// is what lets a box with no relays configured run every code path without
// nil checks at the call sites.
type Set struct {
	mu  sync.RWMutex
	eps []Endpoint
	hp  map[string]*health // keyed by BaseURL
}

// NewSet builds a Set from already-validated endpoints. Callers should
// normally use Load/FromEnv, which validate first; this exists for tests and
// for callers assembling a set programmatically.
func NewSet(eps []Endpoint) *Set {
	s := &Set{hp: make(map[string]*health, len(eps))}
	s.eps = append(s.eps, eps...)
	for i := range s.eps {
		s.hp[s.eps[i].BaseURL] = &health{}
	}
	return s
}

// Len returns how many endpoints are configured. Zero is a valid, expected
// state (a box with a public IP, or a LAN-only box) and callers MUST treat it
// as "no relay configured" rather than as an error.
func (s *Set) Len() int {
	if s == nil {
		return 0
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.eps)
}

// All returns every configured endpoint in priority order, regardless of
// health. This is the fan-out view: peer resolve uses it to query several
// relays and compare their answers, because an endpoint that is currently
// marked down for tunnel purposes may still answer a read correctly, and
// because equivocation detection needs every source, not just the healthy
// ones.
func (s *Set) All() []Endpoint {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.sortedLocked(s.eps)
}

// Ordered returns endpoints in the order a caller should TRY them: healthy
// ones first (by priority), then endpoints currently in backoff (soonest to
// recover first).
//
// It never returns fewer endpoints than are configured. An endpoint in
// backoff is DEPRIORITISED, never dropped — a box whose entire set is failing
// must keep trying, because "all relays look down" is far more often a local
// network blip than a genuine global outage, and a box that stopped trying
// would need a human to bring it back.
func (s *Set) Ordered() []Endpoint {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	now := nowFn()
	var up, down []Endpoint
	for _, e := range s.eps {
		h := s.hp[e.BaseURL]
		if h == nil || now.After(h.downUntil) {
			up = append(up, e)
			continue
		}
		down = append(down, e)
	}
	out := s.sortedLocked(up)
	// Endpoints in backoff sort by how soon they recover, so the one closest
	// to being usable again is tried first if every healthy endpoint fails.
	sort.SliceStable(down, func(i, j int) bool {
		hi, hj := s.hp[down[i].BaseURL], s.hp[down[j].BaseURL]
		return hi.downUntil.Before(hj.downUntil)
	})
	return append(out, down...)
}

// Primary returns the endpoint a caller with room for only one should use,
// and false when the set is empty.
func (s *Set) Primary() (Endpoint, bool) {
	ord := s.Ordered()
	if len(ord) == 0 {
		return Endpoint{}, false
	}
	return ord[0], true
}

// sortedLocked orders by Priority then configuration order. Caller holds mu.
func (s *Set) sortedLocked(in []Endpoint) []Endpoint {
	out := append([]Endpoint(nil), in...)
	idx := make(map[string]int, len(s.eps))
	for i, e := range s.eps {
		idx[e.BaseURL] = i
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Priority != out[j].Priority {
			return out[i].Priority < out[j].Priority
		}
		return idx[out[i].BaseURL] < idx[out[j].BaseURL]
	})
	return out
}

// MarkUp records a successful interaction: the endpoint leaves backoff
// immediately and its failure count resets. Call this on any successful
// round trip, not just on connect — a relay that is answering is healthy.
func (s *Set) MarkUp(baseURL string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	h := s.hp[baseURL]
	if h == nil {
		return
	}
	h.failures = 0
	h.downUntil = time.Time{}
	h.lastErr = ""
	h.lastOK = nowFn()
}

// MarkDown records a failure and puts the endpoint into exponential backoff
// with full jitter. err is stored for status display only — it is assumed to
// be operator-facing and is never used for control flow.
func (s *Set) MarkDown(baseURL string, err error) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	h := s.hp[baseURL]
	if h == nil {
		return
	}
	h.failures++
	if err != nil {
		h.lastErr = err.Error()
	}
	h.downUntil = nowFn().Add(backoffFor(h.failures))
}

// backoffFor is baseBackoff doubled per consecutive failure, capped at
// maxBackoff, then FULL-jittered (a uniform sample in (0, d]) so several
// endpoints failing at the same instant do not retry in lockstep.
func backoffFor(failures int) time.Duration {
	if failures < 1 {
		failures = 1
	}
	d := baseBackoff
	for i := 1; i < failures && d < maxBackoff; i++ {
		d *= 2
	}
	if d > maxBackoff {
		d = maxBackoff
	}
	// Full jitter, with a floor of 1s so a hot loop can never result.
	j := time.Duration(jitterFn() * float64(d))
	if j < time.Second {
		j = time.Second
	}
	return j
}

// Status is the operator-facing health view of one endpoint. Token-free by
// construction — it embeds the REDACTED endpoint.
type Status struct {
	Endpoint  Endpoint `json:"endpoint"`
	Healthy   bool     `json:"healthy"`
	Failures  int      `json:"failures,omitempty"`
	LastError string   `json:"last_error,omitempty"`
	// DownForSeconds is how much longer this endpoint stays in backoff.
	DownForSeconds int `json:"down_for_seconds,omitempty"`
	// LastOKUnix is when this endpoint last succeeded (0 = never).
	LastOKUnix int64 `json:"last_ok_unix,omitempty"`
}

// Statuses returns the redacted health view of the whole set, in Ordered()
// order, for Settings and status routes.
func (s *Set) Statuses() []Status {
	if s == nil {
		return nil
	}
	ord := s.Ordered()
	s.mu.RLock()
	defer s.mu.RUnlock()
	now := nowFn()
	out := make([]Status, 0, len(ord))
	for _, e := range ord {
		h := s.hp[e.BaseURL]
		st := Status{Endpoint: e.Redact(), Healthy: true}
		if h != nil {
			st.Failures = h.failures
			st.LastError = h.lastErr
			if !h.lastOK.IsZero() {
				st.LastOKUnix = h.lastOK.Unix()
			}
			if now.Before(h.downUntil) {
				st.Healthy = false
				st.DownForSeconds = int(h.downUntil.Sub(now).Seconds()) + 1
			}
		}
		out = append(out, st)
	}
	return out
}

// Describe is a short, TOKEN-FREE summary for a startup log line.
func (s *Set) Describe() string {
	if s.Len() == 0 {
		return "no relay endpoints configured"
	}
	all := s.All()
	names := make([]string, 0, len(all))
	for _, e := range all {
		names = append(names, e.String())
	}
	return fmt.Sprintf("%d relay endpoint(s): %s", len(all), strings.Join(names, ", "))
}
