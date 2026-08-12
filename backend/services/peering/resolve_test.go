package peering

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// resetReachabilityCache clears the package-level reachability cache so tests
// don't leak state into each other via shared vulos IDs.
func resetReachabilityCache(t *testing.T) {
	t.Helper()
	reachMu.Lock()
	reachCache = map[string]reachabilityEntry{}
	reachMu.Unlock()
	t.Cleanup(func() {
		reachMu.Lock()
		reachCache = map[string]reachabilityEntry{}
		reachMu.Unlock()
	})
}

// TestResolvePeerBaseURL_PassThrough locks in the CONSOLIDATION B-0 contract:
// with an empty reachability cache (no relay configured / no cache entry yet),
// the seam is a byte-compatible pass-through that produces exactly what the
// live call sites used to compute inline ("https://" + contact.Server).
func TestResolvePeerBaseURL_PassThrough(t *testing.T) {
	resetReachabilityCache(t)
	cases := []struct {
		name, vulosID, server, want string
	}{
		{"host_port", "vulos:ed25519:peer", "bob.example.org:8080", "https://bob.example.org:8080"},
		{"bare_host", "vulos:ed25519:peer", "bob.example.org", "https://bob.example.org"},
		{"empty_server", "vulos:ed25519:peer", "", ""},
		{"already_https", "vulos:ed25519:peer", "https://bob.example.org:8080", "https://bob.example.org:8080"},
		{"already_http", "vulos:ed25519:peer", "http://bob.example.org", "http://bob.example.org"},
		{"empty_vulaid_still_resolves", "", "bob.example.org", "https://bob.example.org"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := resolvePeerBaseURL(tc.vulosID, tc.server)
			if got != tc.want {
				t.Fatalf("resolvePeerBaseURL(%q, %q) = %q, want %q", tc.vulosID, tc.server, got, tc.want)
			}
		})
	}
}

// TestResolvePeerBaseURL_PrefersVerifiedDirect verifies tier 1 of the ladder:
// a cached verified-direct endpoint wins over both the relay tier and the
// contact.Server fallback.
func TestResolvePeerBaseURL_PrefersVerifiedDirect(t *testing.T) {
	resetReachabilityCache(t)
	reachabilityCachePut("vulos:ed25519:alice", "https://alice-direct.example.net", "https://alice.relay.example.com")
	got := resolvePeerBaseURL("vulos:ed25519:alice", "stale.example.org:8080")
	if got != "https://alice-direct.example.net" {
		t.Fatalf("got %q, want the cached verified-direct endpoint", got)
	}
}

// TestResolvePeerBaseURL_FallsBackToRelayTunnel verifies tier 2: with no
// verified-direct endpoint but a relay-tunnel endpoint cached, the relay wins
// over contact.Server.
func TestResolvePeerBaseURL_FallsBackToRelayTunnel(t *testing.T) {
	resetReachabilityCache(t)
	reachabilityCachePut("vulos:ed25519:bob", "", "https://bob.relay.example.com")
	got := resolvePeerBaseURL("vulos:ed25519:bob", "stale.example.org:8080")
	if got != "https://bob.relay.example.com" {
		t.Fatalf("got %q, want the cached relay-tunnel endpoint", got)
	}
}

// TestResolvePeerBaseURL_NegativeCacheFallsThrough verifies a negative cache
// entry (relay resolved but published nothing — 404 case) degrades cleanly to
// the contact.Server fallback rather than an empty/broken URL.
func TestResolvePeerBaseURL_NegativeCacheFallsThrough(t *testing.T) {
	resetReachabilityCache(t)
	reachabilityCachePut("vulos:ed25519:carol", "", "")
	got := resolvePeerBaseURL("vulos:ed25519:carol", "carol.example.org")
	if got != "https://carol.example.org" {
		t.Fatalf("got %q, want the contact.Server fallback", got)
	}
}

// TestResolvePeerBaseURL_ExpiredCacheFallsThrough verifies a stale (expired
// TTL) cache entry is treated as a miss, not trusted forever.
func TestResolvePeerBaseURL_ExpiredCacheFallsThrough(t *testing.T) {
	resetReachabilityCache(t)
	reachMu.Lock()
	reachCache["vulos:ed25519:dave"] = reachabilityEntry{
		direct:    "https://dave-direct.example.net",
		fetchedAt: time.Now().Add(-2 * reachabilityCacheTTL),
	}
	reachMu.Unlock()
	got := resolvePeerBaseURL("vulos:ed25519:dave", "dave.example.org")
	if got != "https://dave.example.org" {
		t.Fatalf("got %q, want the contact.Server fallback (expired cache must be ignored)", got)
	}
}

// TestRefreshPeerReachability_PopulatesCache verifies the network half: a
// relay resolve response is decoded and lands in the cache that
// resolvePeerBaseURL consults.
func TestRefreshPeerReachability_PopulatesCache(t *testing.T) {
	resetReachabilityCache(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != relayResolvePath {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		if got := r.URL.Query().Get("vulos_id"); got != "vulos:ed25519:erin" {
			t.Fatalf("vulos_id query = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(relayResolveResponse{Relay: "https://erin.relay.example.com"}) //nolint:errcheck
	}))
	defer srv.Close()

	if err := RefreshPeerReachability(context.Background(), srv.URL, "vulos:ed25519:erin"); err != nil {
		t.Fatalf("RefreshPeerReachability: %v", err)
	}
	got := resolvePeerBaseURL("vulos:ed25519:erin", "erin.example.org")
	if got != "https://erin.relay.example.com" {
		t.Fatalf("got %q, want the relay-resolved endpoint", got)
	}
}

// TestRefreshPeerReachability_404IsNegativeCache verifies a 404 (no
// reachability published) is cached as a negative result rather than an error.
func TestRefreshPeerReachability_404IsNegativeCache(t *testing.T) {
	resetReachabilityCache(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	if err := RefreshPeerReachability(context.Background(), srv.URL, "vulos:ed25519:frank"); err != nil {
		t.Fatalf("RefreshPeerReachability on 404 should not error: %v", err)
	}
	got := resolvePeerBaseURL("vulos:ed25519:frank", "frank.example.org")
	if got != "https://frank.example.org" {
		t.Fatalf("got %q, want the contact.Server fallback after a 404 resolve", got)
	}
}

// TestRefreshPeerReachability_RequiresRelayBaseURLAndVulosID verifies input
// validation.
func TestRefreshPeerReachability_RequiresRelayBaseURLAndVulosID(t *testing.T) {
	if err := RefreshPeerReachability(context.Background(), "", "vulos:ed25519:x"); err == nil {
		t.Fatal("expected error with empty relay base URL")
	}
	if err := RefreshPeerReachability(context.Background(), "https://relay.example.org", ""); err == nil {
		t.Fatal("expected error with empty vulos id")
	}
}

// TestStartReachabilityRefresh_EmptyRelayIsNoop verifies that configuring no
// relays (an empty list) never touches the network or the cache — self-host
// boxes without a relay see zero behavior change.
//
// NOTE: this package's test binary currently fails to build because of a
// PRE-EXISTING import cycle unrelated to reachability (peering -> auth -> ... ->
// fleetid -> peering, via vouch.go / e2e_session_stack_test.go), so these tests
// cannot be executed with `go test ./services/peering/` today. They are kept
// correct against the live signatures so coverage exists the moment that cycle
// is broken.
func TestStartReachabilityRefresh_EmptyRelayIsNoop(t *testing.T) {
	resetReachabilityCache(t)
	called := false
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	StartReachabilityRefresh(ctx, nil, func() []WKApprovedPeer {
		called = true
		return nil
	})
	// Give any (incorrectly) spawned goroutine a moment to misbehave.
	time.Sleep(50 * time.Millisecond)
	if called {
		t.Fatal("StartReachabilityRefresh must not call listApproved when no relays are configured")
	}
}

// TestStartReachabilityRefresh_WarmsCacheImmediately verifies the background
// refresher populates the cache on startup, not only after the first tick.
func TestStartReachabilityRefresh_WarmsCacheImmediately(t *testing.T) {
	resetReachabilityCache(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(relayResolveResponse{Direct: "https://grace-direct.example.net"}) //nolint:errcheck
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	StartReachabilityRefresh(ctx, []string{srv.URL}, func() []WKApprovedPeer {
		return []WKApprovedPeer{{VulosID: "vulos:ed25519:grace", ServerAddr: "grace.example.org"}}
	})

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if got := resolvePeerBaseURL("vulos:ed25519:grace", "grace.example.org"); got == "https://grace-direct.example.net" {
			return // success
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("cache was not warmed by StartReachabilityRefresh within the deadline")
}

// TestCrossCheck_AgreeingRelaysTrustDirect verifies that when every relay
// reports the SAME verified-direct endpoint, it is trusted.
func TestCrossCheck_AgreeingRelaysTrustDirect(t *testing.T) {
	resetReachabilityCache(t)
	mk := func(direct string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(relayResolveResponse{Direct: direct}) //nolint:errcheck
		}))
	}
	a := mk("https://hank-direct.example.net")
	defer a.Close()
	b := mk("https://hank-direct.example.net")
	defer b.Close()

	if err := refreshPeerReachabilityCrossChecked(context.Background(), []string{a.URL, b.URL}, "vulos:ed25519:hank"); err != nil {
		t.Fatalf("cross-check: %v", err)
	}
	if got := resolvePeerBaseURL("vulos:ed25519:hank", "hank.example.org"); got != "https://hank-direct.example.net" {
		t.Fatalf("got %q, want the agreed verified-direct endpoint", got)
	}
}

// TestCrossCheck_EquivocatingRelaysDropDirect is the security case: two relays
// disagree about the peer's verified-direct endpoint (one is lying). The
// direct tier must be DROPPED (fail closed) and the ladder must degrade to the
// contact.Server fallback rather than follow either contested endpoint.
func TestCrossCheck_EquivocatingRelaysDropDirect(t *testing.T) {
	resetReachabilityCache(t)
	honest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(relayResolveResponse{Direct: "https://ivy-real.example.net"}) //nolint:errcheck
	}))
	defer honest.Close()
	liar := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(relayResolveResponse{Direct: "https://ivy-attacker.evil.example"}) //nolint:errcheck
	}))
	defer liar.Close()

	if err := refreshPeerReachabilityCrossChecked(context.Background(), []string{honest.URL, liar.URL}, "vulos:ed25519:ivy"); err != nil {
		t.Fatalf("cross-check: %v", err)
	}
	got := resolvePeerBaseURL("vulos:ed25519:ivy", "ivy.example.org")
	if got == "https://ivy-attacker.evil.example" || got == "https://ivy-real.example.net" {
		t.Fatalf("cross-check followed a CONTESTED direct endpoint: %q", got)
	}
	if got != "https://ivy.example.org" {
		t.Fatalf("got %q, want the contact.Server fallback after equivocation drops the direct tier", got)
	}
}

// TestCrossCheck_AllRelaysDownLeavesCacheUntouched verifies that when every
// relay errors, an existing good cache entry is NOT evicted (stale-but-usable
// beats a transient blip), and the error is surfaced.
func TestCrossCheck_AllRelaysDownLeavesCacheUntouched(t *testing.T) {
	resetReachabilityCache(t)
	reachabilityCachePut("vulos:ed25519:jack", "https://jack-direct.example.net", "")

	// Two relays that are refused/unreachable: point at a closed server.
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	deadURL := dead.URL
	dead.Close() // now nothing is listening

	err := refreshPeerReachabilityCrossChecked(context.Background(), []string{deadURL}, "vulos:ed25519:jack")
	if err == nil {
		t.Fatal("expected an error when every relay is unreachable")
	}
	if got := resolvePeerBaseURL("vulos:ed25519:jack", "jack.example.org"); got != "https://jack-direct.example.net" {
		t.Fatalf("got %q, want the pre-existing cached direct endpoint left untouched", got)
	}
}
