// no-broker-dep:allow-file: comments require this box's rendezvous agent to match Pier's wire
// contract byte-for-byte, and note a mixed set of built-in Vulos and
// Pier relays is fine -- wire-compatibility and operator choice, not a
// dependency.

// resolve.go — the single peer-reachability seam (CONSOLIDATION B-0, finished
// under the "unified reachability architecture" / two-class-app-model program).
//
// resolvePeerBaseURL is the one place that turns a peer's identity + last-known
// server address into the base URL that PeerClient.Post delivers to. B-0
// introduced it as a behavioral NO-OP ("https://<contact.Server>"); this
// revision (B-2) implements the documented direct → relay → server fallback
// ladder:
//
//  1. verified-direct: the peer's box proved (via internal/directlisten's
//     ownership-probe mechanism) that it controls a publicly reachable
//     endpoint. Fastest and most private — bypasses any relay entirely.
//  2. relay-tunnel: the peer's box has no public IP but a co-located relay
//     agent (Pier, self-hostable, run alongside the OS — NOT embedded
//     in this binary, see internal/directlisten's "the OS does not embed the
//     relay agent" note) maintains a reverse tunnel, so requests routed to
//     the relay's per-identity URL reach the box anyway. This is what makes
//     collab apps (Diwan) work for a NAT'd/CGNAT box with zero public-IP
//     configuration.
//  3. contact.Server: the last-known plain address (today's sole behavior,
//     unchanged as the final fallback).
//
// Both of the first two tiers come from an operator-configured relay's
// peer-reachability RESOLVE endpoint (GET {relay}/_vulos-direct/resolve,
// sharing the /_vulos-direct/ path family with directlisten's probe — see
// wellknown.go's CONSOLIDATION B-3 note: "discovered via the relay
// (/_vulos-direct/resolve)"). resolvePeerBaseURL itself stays a PURE,
// allocation-free, non-blocking function with the ORIGINAL signature (no
// ctx, no error) so no live delivery call site (outbox.go etc.) needs to
// change again: it only ever consults an in-process cache. The cache is kept
// warm by a background refresher (RefreshPeerReachability /
// StartReachabilityRefresh, mirroring the existing wellknown.go profile-cache
// pattern) — a cache miss or a relay that was never configured degrades to
// EXACTLY the B-0 pass-through, so self-host boxes that don't run a relay see
// byte-identical behavior to before.
package peering

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"vulos/backend/internal/safedial"
)

// reachabilityLogf logs operator-facing reachability notes — most importantly a
// SUSPECTED equivocation, where configured relays disagree about a peer's
// verified-direct endpoint. It is a package var so a test can capture the line;
// it defaults to log.Printf.
var reachabilityLogf = log.Printf

// relayResolvePath is the relay's peer-reachability resolve endpoint. It
// shares the "_vulos-direct" path family with internal/directlisten.ProbePath
// (the box-side ownership-proof probe) — both are the relay/box halves of the
// SAME direct-reachability mechanism. MUST match Pier's wire contract.
const relayResolvePath = "/_vulos-direct/resolve"

// reachabilityHTTPTimeout bounds a single resolve request to the relay.
const reachabilityHTTPTimeout = 5 * time.Second

// reachabilityCacheTTL bounds how long a resolved (or negative) result is
// trusted before the background refresher fetches it again.
const reachabilityCacheTTL = 5 * time.Minute

// reachabilityRefreshInterval is how often StartReachabilityRefresh re-polls
// the relay for every approved contact's current reachability.
const reachabilityRefreshInterval = 5 * time.Minute

// reachabilityResolveMaxBytes bounds the relay resolve response body read —
// the payload is just two optional URL strings, so this is generous headroom
// while still capping a misbehaving/compromised relay's memory impact.
const reachabilityResolveMaxBytes = 8 * 1024

// reachabilityEntry is the cached result of resolving one peer's reachability.
type reachabilityEntry struct {
	direct    string // verified-direct base URL, e.g. "https://box1.example.net"
	relay     string // relay-tunnel base URL, e.g. "https://<id>.relay.example.com"
	fetchedAt time.Time
}

var (
	reachMu    sync.RWMutex
	reachCache = map[string]reachabilityEntry{}
)

// relayResolveResponse is the wire shape returned by the relay's
// peer-reachability resolve endpoint for a given vulos_id. Both fields are
// optional; either, both, or neither may be present. MUST match Pier.
type relayResolveResponse struct {
	// Direct is the peer's verified-direct base URL (https), present only when
	// the relay has successfully probed and verified it (see directlisten.go).
	Direct string `json:"direct,omitempty"`
	// Relay is the peer's relay-tunnel base URL (https), present when the
	// peer's box maintains an active reverse tunnel with this relay.
	Relay string `json:"relay,omitempty"`
}

// resolvePeerBaseURL returns the base URL to deliver an envelope to the peer
// identified by toVulosID, whose last-known server address is server (the value
// stored in contact.Server / outbox item.PeerServer).
//
// Ladder (highest to lowest preference):
//  1. a cached verified-direct endpoint for toVulosID (fastest, most private)
//  2. a cached relay-tunnel endpoint for toVulosID (works through NAT/CGNAT)
//  3. "https://" + server — the original B-0 pass-through fallback
//
// Contract (unchanged from B-0, still honored by the final fallback tier):
//   - server is a bare "host" or "host:port" (no scheme); the result carries
//     the https:// scheme, matching the pre-B-0 inline "https://" + contact.Server.
//   - a server that already includes a scheme is returned unchanged.
//   - an empty server AND no cached reachability yields an empty string (the
//     caller already guards this; e.g. outbox.go skips items with an empty
//     PeerServer).
func resolvePeerBaseURL(toVulosID, server string) string {
	if toVulosID != "" {
		if e, ok := reachabilityCacheGet(toVulosID); ok {
			if e.direct != "" {
				return e.direct
			}
			if e.relay != "" {
				return e.relay
			}
		}
	}
	if server == "" {
		return ""
	}
	if strings.Contains(server, "://") {
		return server
	}
	return "https://" + server
}

func reachabilityCacheGet(vulosID string) (reachabilityEntry, bool) {
	reachMu.RLock()
	defer reachMu.RUnlock()
	e, ok := reachCache[vulosID]
	if !ok || time.Since(e.fetchedAt) > reachabilityCacheTTL {
		return reachabilityEntry{}, false
	}
	return e, true
}

func reachabilityCachePut(vulosID string, direct, relay string) {
	reachMu.Lock()
	defer reachMu.Unlock()
	reachCache[vulosID] = reachabilityEntry{direct: direct, relay: relay, fetchedAt: time.Now()}
}

// fetchPeerReachability performs ONE peer-reachability resolve against a
// single relay and returns the decoded answer WITHOUT touching the cache. It
// is the per-relay primitive the single-relay (RefreshPeerReachability) and
// cross-checked (refreshPeerReachabilityCrossChecked) paths both build on, so
// the SSRF guard, the bounded read, and the 404-is-negative semantics live in
// exactly one place.
//
// found is false for a 404 — the relay published nothing for this peer (its
// box has no relay agent, or is offline). That is a definitive answer, not an
// error, so the caller records a negative result rather than retrying.
func fetchPeerReachability(ctx context.Context, relayBaseURL, vulosID string) (resp relayResolveResponse, found bool, err error) {
	relayBaseURL = strings.TrimRight(strings.TrimSpace(relayBaseURL), "/")
	if relayBaseURL == "" || vulosID == "" {
		return relayResolveResponse{}, false, fmt.Errorf("peering: reachability resolve requires a relay base URL and a vulos id")
	}

	endpoint := relayBaseURL + relayResolvePath + "?vulos_id=" + url.QueryEscape(vulosID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return relayResolveResponse{}, false, fmt.Errorf("peering: build reachability resolve request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: reachabilityHTTPTimeout}
	if !peeringSSRFBypass {
		// The relay base URL is OPERATOR-CONFIGURED (env), not per-request
		// attacker-supplied data, but SSRF-guard the dial anyway for defense in
		// depth and consistency with the rest of this package's outbound calls
		// (wellknown.go's wkFetchAndVerify does the same for peer profile fetch).
		//
		// safedial.NewPeer, not safedial.New(false): a relay may legitimately
		// live on the operator's own tailnet or private mesh, which is an
		// address range only the operator can know. NewPeer honours the grant
		// they wrote (VULOS_PEER_ALLOW_CIDR, or the older coarse
		// VULOS_PEER_ALLOW_LAN) and nothing wider — a relay on 100.64.0.0/10 is
		// reachable when that range is granted, while loopback, link-local
		// (169.254.169.254) and bogons stay refused under every grant, and an
		// UNCONFIGURED box is byte-identical to safedial.New(false).
		client.Transport = &http.Transport{DialContext: safedial.NewPeer().DialContext}
	}

	httpResp, err := client.Do(req)
	if err != nil {
		return relayResolveResponse{}, false, fmt.Errorf("peering: reachability resolve request: %w", err)
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode == http.StatusNotFound {
		return relayResolveResponse{}, false, nil
	}
	if httpResp.StatusCode != http.StatusOK {
		return relayResolveResponse{}, false, fmt.Errorf("peering: reachability resolve: relay returned %d", httpResp.StatusCode)
	}

	// Bound the response body read: the relay is operator-configured, not
	// per-request attacker data, but a compromised/misbehaving relay must
	// never be able to force unbounded memory use here (defense in depth,
	// consistent with the SSRF-guarded dial above and the rest of this
	// package's outbound fetches, e.g. bandwidth.go/discovery.go).
	var out relayResolveResponse
	if err := json.NewDecoder(io.LimitReader(httpResp.Body, reachabilityResolveMaxBytes)).Decode(&out); err != nil {
		return relayResolveResponse{}, false, fmt.Errorf("peering: decode reachability resolve response: %w", err)
	}
	return out, true, nil
}

// RefreshPeerReachability queries a SINGLE operator-configured relay's
// peer-reachability resolve endpoint for vulosID and updates the in-process
// cache resolvePeerBaseURL consults. A 404 (no reachability information
// published — the peer's box has no relay agent, or is offline) is cached as
// a negative result so the ladder degrades to the contact.Server fallback
// without hammering the relay every call. Errors are returned for the caller
// to log; they leave any existing cache entry untouched (stale-but-usable is
// better than evicting a good entry on a transient relay hiccup).
//
// With a single relay there is nothing to cross-check against; the multi-relay
// path (refreshPeerReachabilityCrossChecked, used by StartReachabilityRefresh)
// is the one that guards against an equivocating relay. This entry point is
// kept for callers that genuinely have one relay and for direct testing.
func RefreshPeerReachability(ctx context.Context, relayBaseURL, vulosID string) error {
	out, found, err := fetchPeerReachability(ctx, relayBaseURL, vulosID)
	if err != nil {
		return err
	}
	if !found {
		reachabilityCachePut(vulosID, "", "")
		return nil
	}
	reachabilityCachePut(vulosID, out.Direct, out.Relay)
	return nil
}

// refreshPeerReachabilityCrossChecked resolves ONE peer across SEVERAL relays
// and CROSS-CHECKS their answers before trusting any, rather than believing
// whichever relay answers first.
//
// # What it defends, and why "first answer wins" was not enough
//
// The verified-direct endpoint (tier 1 of resolvePeerBaseURL's ladder) is the
// dangerous field: it is where this box sends the peer's traffic at HIGHEST
// preference, BYPASSING every relay and the tunnel's header-trust boundary
// entirely. A single compromised or hostile relay that answers with a direct
// endpoint IT controls — it can pass its own ownership probe on an
// attacker-run box — would silently redirect the peer's traffic there, and a
// first-answer-wins resolver would follow it without a second opinion.
//
// The substrate spec's answer (KOTVA §4.2.1(3): a rendezvous set of >= 3 nodes
// under DISJOINT operators, cross-checked) is that no single operator may
// equivocate about where a peer is. Applied here: ask EVERY configured relay
// (a mixed set of built-in Vulos and Pier relays is fine — same wire
// contract) and compare their `direct` answers. Honest relays agree — a peer
// has one real verified-direct endpoint, or none — so a DISAGREEMENT means at
// least one relay is equivocating. We cannot tell which, so we treat the
// direct field as SUSPECT and DROP it, failing closed to the relay-tunnel tier
// or the last-known address. Never follow a contested direct endpoint.
//
// The relay-tunnel field is deliberately NOT cross-checked the same way: each
// relay legitimately answers with the peer's tunnel URL ON ITSELF (a different
// host per relay by construction), so there is no cross-relay agreement to
// test, and that URL points back at a relay the OWNER configured. The first
// relay that offers one wins, exactly as before.
//
// With one relay this is a no-op cross-check (nothing to compare) and behaves
// exactly like RefreshPeerReachability — which is precisely why the docs push
// for two relays under different operators: a box with one relay never had
// this protection to begin with.
func refreshPeerReachabilityCrossChecked(ctx context.Context, relays []string, vulosID string) error {
	if strings.TrimSpace(vulosID) == "" {
		return fmt.Errorf("peering: reachability resolve requires a vulos id")
	}

	var (
		directVals []string // DISTINCT non-empty direct answers, in first-seen order
		directSeen = map[string]bool{}
		relayURL   string // first non-empty relay-tunnel answer
		answered   bool   // at least one relay gave a definitive answer (200 or 404)
		lastErr    error
	)

	for _, relay := range relays {
		if strings.TrimSpace(relay) == "" {
			continue
		}
		tctx, cancel := context.WithTimeout(ctx, reachabilityHTTPTimeout)
		resp, found, err := fetchPeerReachability(tctx, relay, vulosID)
		cancel()
		if err != nil {
			lastErr = err
			continue
		}
		answered = true
		if !found {
			continue
		}
		if d := strings.TrimSpace(resp.Direct); d != "" && !directSeen[d] {
			directSeen[d] = true
			directVals = append(directVals, d)
		}
		if relayURL == "" {
			relayURL = strings.TrimSpace(resp.Relay)
		}
	}

	if !answered {
		// Every relay errored — a transient outage, not a resolution. Leave any
		// existing cache entry in place (stale-but-usable beats evicting a good
		// answer on a blip) and surface the last error for the caller to log.
		if lastErr != nil {
			return lastErr
		}
		return nil
	}

	direct := ""
	switch len(directVals) {
	case 0:
		// No relay knows a verified-direct endpoint — fine, use the relay tier.
	case 1:
		direct = directVals[0]
	default:
		// EQUIVOCATION. Relays disagree about the peer's direct endpoint, so at
		// least one is lying or misinformed. We cannot tell which, so we trust
		// NONE of them and fall back to the relay tier / last-known address.
		// Fail closed: a contested direct endpoint is never followed.
		reachabilityLogf("peering: SUSPECT reachability for %s — %d relays gave conflicting verified-direct endpoints (%s); ignoring the direct tier for this peer",
			vulosID, len(directVals), strings.Join(directVals, ", "))
	}

	reachabilityCachePut(vulosID, direct, relayURL)
	return nil
}

// StartReachabilityRefresh launches a background goroutine that periodically
// refreshes the reachability cache for every approved contact, so
// resolvePeerBaseURL's direct/relay tiers stay warm without adding a
// synchronous network call to the hot delivery path. A no-op (returns
// immediately, cache stays empty forever, resolvePeerBaseURL stays a pure
// pass-through) when relayBaseURL is empty — i.e. a box that hasn't
// configured VULOS_RELAY_BASE_URL sees EXACTLY the original B-0 behavior.
// The goroutine exits when ctx is cancelled.
// StartReachabilityRefresh accepts SEVERAL relay base URLs and, for each peer,
// queries ALL of them and CROSS-CHECKS their answers (see
// refreshPeerReachabilityCrossChecked) rather than stopping at the first that
// answers.
//
// # Why several, and why cross-check rather than first-answer-wins
//
// Peer reachability is the one lookup an established relationship still
// depends on, so a single relay holding the only answer makes that relay a
// single point of failure for every cross-NAT peer this box has. Querying a
// list means one relay being down, seized, or simply not knowing about a
// peer costs nothing as long as another does.
//
// But a list also enables the stronger guarantee the substrate spec actually
// asks for (KOTVA §4.2.1(3): a set of nodes under DISJOINT operators,
// cross-checked): a relay must not be able to EQUIVOCATE about where a peer
// is. So instead of trusting the first relay to answer, this fans out to every
// relay and treats a disagreement about the peer's verified-direct endpoint as
// suspect, dropping it. A hostile relay that alone claims a false direct
// endpoint is outvoted rather than obeyed.
//
// A single URL is just a one-element list (no cross-check possible, behaves as
// before); an empty list disables the refresher, exactly as an empty list did.
func StartReachabilityRefresh(ctx context.Context, relayBaseURLs []string, listApproved func() []WKApprovedPeer) {
	var relays []string
	for _, u := range relayBaseURLs {
		if u = strings.TrimSpace(u); u != "" {
			relays = append(relays, u)
		}
	}
	if len(relays) == 0 {
		return
	}
	refresh := func() {
		for _, p := range listApproved() {
			if p.VulosID == "" {
				continue
			}
			// CROSS-CHECK across every relay rather than trusting whichever
			// answers first — see refreshPeerReachabilityCrossChecked. A relay
			// that does not know this peer is not an error worth escalating; a
			// relay that DISAGREES about the peer's verified-direct endpoint is
			// caught and its answer dropped. If every relay errors, the ladder
			// degrades to the peer's last-known address exactly as it does with
			// no relay configured at all, so a transient failure is silent.
			_ = refreshPeerReachabilityCrossChecked(ctx, relays, p.VulosID)
		}
	}
	go func() {
		// Warm the cache once at startup rather than waiting a full interval.
		refresh()
		ticker := time.NewTicker(reachabilityRefreshInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				refresh()
			}
		}
	}()
}
