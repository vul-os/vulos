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
//     agent (Ephor, self-hostable, run alongside the OS — NOT embedded
//     in this binary, see internal/directlisten's "the OS does not embed the
//     relay agent" note) maintains a reverse tunnel, so requests routed to
//     the relay's per-identity URL reach the box anyway. This is what makes
//     collab apps (Ofisi) work for a NAT'd/CGNAT box with zero public-IP
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
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"vulos/backend/internal/safedial"
)

// relayResolvePath is the relay's peer-reachability resolve endpoint. It
// shares the "_vulos-direct" path family with internal/directlisten.ProbePath
// (the box-side ownership-proof probe) — both are the relay/box halves of the
// SAME direct-reachability mechanism. MUST match Ephor's wire contract.
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
	relay     string // relay-tunnel base URL, e.g. "https://<id>.relay.vulos.org"
	fetchedAt time.Time
}

var (
	reachMu    sync.RWMutex
	reachCache = map[string]reachabilityEntry{}
)

// relayResolveResponse is the wire shape returned by the relay's
// peer-reachability resolve endpoint for a given vula_id. Both fields are
// optional; either, both, or neither may be present. MUST match Ephor.
type relayResolveResponse struct {
	// Direct is the peer's verified-direct base URL (https), present only when
	// the relay has successfully probed and verified it (see directlisten.go).
	Direct string `json:"direct,omitempty"`
	// Relay is the peer's relay-tunnel base URL (https), present when the
	// peer's box maintains an active reverse tunnel with this relay.
	Relay string `json:"relay,omitempty"`
}

// resolvePeerBaseURL returns the base URL to deliver an envelope to the peer
// identified by toVulaID, whose last-known server address is server (the value
// stored in contact.Server / outbox item.PeerServer).
//
// Ladder (highest to lowest preference):
//  1. a cached verified-direct endpoint for toVulaID (fastest, most private)
//  2. a cached relay-tunnel endpoint for toVulaID (works through NAT/CGNAT)
//  3. "https://" + server — the original B-0 pass-through fallback
//
// Contract (unchanged from B-0, still honored by the final fallback tier):
//   - server is a bare "host" or "host:port" (no scheme); the result carries
//     the https:// scheme, matching the pre-B-0 inline "https://" + contact.Server.
//   - a server that already includes a scheme is returned unchanged.
//   - an empty server AND no cached reachability yields an empty string (the
//     caller already guards this; e.g. outbox.go skips items with an empty
//     PeerServer).
func resolvePeerBaseURL(toVulaID, server string) string {
	if toVulaID != "" {
		if e, ok := reachabilityCacheGet(toVulaID); ok {
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

func reachabilityCacheGet(vulaID string) (reachabilityEntry, bool) {
	reachMu.RLock()
	defer reachMu.RUnlock()
	e, ok := reachCache[vulaID]
	if !ok || time.Since(e.fetchedAt) > reachabilityCacheTTL {
		return reachabilityEntry{}, false
	}
	return e, true
}

func reachabilityCachePut(vulaID string, direct, relay string) {
	reachMu.Lock()
	defer reachMu.Unlock()
	reachCache[vulaID] = reachabilityEntry{direct: direct, relay: relay, fetchedAt: time.Now()}
}

// RefreshPeerReachability queries the operator-configured relay's
// peer-reachability resolve endpoint for vulaID and updates the in-process
// cache resolvePeerBaseURL consults. A 404 (no reachability information
// published — the peer's box has no relay agent, or is offline) is cached as
// a negative result so the ladder degrades to the contact.Server fallback
// without hammering the relay every call. Errors are returned for the caller
// to log; they leave any existing cache entry untouched (stale-but-usable is
// better than evicting a good entry on a transient relay hiccup).
func RefreshPeerReachability(ctx context.Context, relayBaseURL, vulaID string) error {
	relayBaseURL = strings.TrimRight(strings.TrimSpace(relayBaseURL), "/")
	if relayBaseURL == "" || vulaID == "" {
		return fmt.Errorf("peering: reachability resolve requires a relay base URL and a vula id")
	}

	endpoint := relayBaseURL + relayResolvePath + "?vula_id=" + url.QueryEscape(vulaID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("peering: build reachability resolve request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: reachabilityHTTPTimeout}
	if !peeringSSRFBypass {
		// The relay base URL is OPERATOR-CONFIGURED (env), not per-request
		// attacker-supplied data, but SSRF-guard the dial anyway for defense in
		// depth and consistency with the rest of this package's outbound calls
		// (wellknown.go's wkFetchAndVerify does the same for peer profile fetch).
		client.Transport = &http.Transport{DialContext: safedial.New(false).DialContext}
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("peering: reachability resolve request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		reachabilityCachePut(vulaID, "", "")
		return nil
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("peering: reachability resolve: relay returned %d", resp.StatusCode)
	}

	// Bound the response body read: the relay is operator-configured, not
	// per-request attacker data, but a compromised/misbehaving relay must
	// never be able to force unbounded memory use here (defense in depth,
	// consistent with the SSRF-guarded dial above and the rest of this
	// package's outbound fetches, e.g. bandwidth.go/discovery.go).
	var out relayResolveResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, reachabilityResolveMaxBytes)).Decode(&out); err != nil {
		return fmt.Errorf("peering: decode reachability resolve response: %w", err)
	}
	reachabilityCachePut(vulaID, out.Direct, out.Relay)
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
func StartReachabilityRefresh(ctx context.Context, relayBaseURL string, listApproved func() []WKApprovedPeer) {
	relayBaseURL = strings.TrimSpace(relayBaseURL)
	if relayBaseURL == "" {
		return
	}
	refresh := func() {
		for _, p := range listApproved() {
			if p.VulaID == "" {
				continue
			}
			tctx, cancel := context.WithTimeout(ctx, reachabilityHTTPTimeout)
			_ = RefreshPeerReachability(tctx, relayBaseURL, p.VulaID)
			cancel()
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
