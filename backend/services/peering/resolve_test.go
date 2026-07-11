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
// don't leak state into each other via shared vula IDs.
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
		name, vulaID, server, want string
	}{
		{"host_port", "vula:ed25519:peer", "bob.example.org:8080", "https://bob.example.org:8080"},
		{"bare_host", "vula:ed25519:peer", "bob.example.org", "https://bob.example.org"},
		{"empty_server", "vula:ed25519:peer", "", ""},
		{"already_https", "vula:ed25519:peer", "https://bob.example.org:8080", "https://bob.example.org:8080"},
		{"already_http", "vula:ed25519:peer", "http://bob.example.org", "http://bob.example.org"},
		{"empty_vulaid_still_resolves", "", "bob.example.org", "https://bob.example.org"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := resolvePeerBaseURL(tc.vulaID, tc.server)
			if got != tc.want {
				t.Fatalf("resolvePeerBaseURL(%q, %q) = %q, want %q", tc.vulaID, tc.server, got, tc.want)
			}
		})
	}
}

// TestResolvePeerBaseURL_PrefersVerifiedDirect verifies tier 1 of the ladder:
// a cached verified-direct endpoint wins over both the relay tier and the
// contact.Server fallback.
func TestResolvePeerBaseURL_PrefersVerifiedDirect(t *testing.T) {
	resetReachabilityCache(t)
	reachabilityCachePut("vula:ed25519:alice", "https://alice-direct.example.net", "https://alice.relay.vulos.org")
	got := resolvePeerBaseURL("vula:ed25519:alice", "stale.example.org:8080")
	if got != "https://alice-direct.example.net" {
		t.Fatalf("got %q, want the cached verified-direct endpoint", got)
	}
}

// TestResolvePeerBaseURL_FallsBackToRelayTunnel verifies tier 2: with no
// verified-direct endpoint but a relay-tunnel endpoint cached, the relay wins
// over contact.Server.
func TestResolvePeerBaseURL_FallsBackToRelayTunnel(t *testing.T) {
	resetReachabilityCache(t)
	reachabilityCachePut("vula:ed25519:bob", "", "https://bob.relay.vulos.org")
	got := resolvePeerBaseURL("vula:ed25519:bob", "stale.example.org:8080")
	if got != "https://bob.relay.vulos.org" {
		t.Fatalf("got %q, want the cached relay-tunnel endpoint", got)
	}
}

// TestResolvePeerBaseURL_NegativeCacheFallsThrough verifies a negative cache
// entry (relay resolved but published nothing — 404 case) degrades cleanly to
// the contact.Server fallback rather than an empty/broken URL.
func TestResolvePeerBaseURL_NegativeCacheFallsThrough(t *testing.T) {
	resetReachabilityCache(t)
	reachabilityCachePut("vula:ed25519:carol", "", "")
	got := resolvePeerBaseURL("vula:ed25519:carol", "carol.example.org")
	if got != "https://carol.example.org" {
		t.Fatalf("got %q, want the contact.Server fallback", got)
	}
}

// TestResolvePeerBaseURL_ExpiredCacheFallsThrough verifies a stale (expired
// TTL) cache entry is treated as a miss, not trusted forever.
func TestResolvePeerBaseURL_ExpiredCacheFallsThrough(t *testing.T) {
	resetReachabilityCache(t)
	reachMu.Lock()
	reachCache["vula:ed25519:dave"] = reachabilityEntry{
		direct:    "https://dave-direct.example.net",
		fetchedAt: time.Now().Add(-2 * reachabilityCacheTTL),
	}
	reachMu.Unlock()
	got := resolvePeerBaseURL("vula:ed25519:dave", "dave.example.org")
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
		if got := r.URL.Query().Get("vula_id"); got != "vula:ed25519:erin" {
			t.Fatalf("vula_id query = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(relayResolveResponse{Relay: "https://erin.relay.vulos.org"}) //nolint:errcheck
	}))
	defer srv.Close()

	if err := RefreshPeerReachability(context.Background(), srv.URL, "vula:ed25519:erin"); err != nil {
		t.Fatalf("RefreshPeerReachability: %v", err)
	}
	got := resolvePeerBaseURL("vula:ed25519:erin", "erin.example.org")
	if got != "https://erin.relay.vulos.org" {
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

	if err := RefreshPeerReachability(context.Background(), srv.URL, "vula:ed25519:frank"); err != nil {
		t.Fatalf("RefreshPeerReachability on 404 should not error: %v", err)
	}
	got := resolvePeerBaseURL("vula:ed25519:frank", "frank.example.org")
	if got != "https://frank.example.org" {
		t.Fatalf("got %q, want the contact.Server fallback after a 404 resolve", got)
	}
}

// TestRefreshPeerReachability_RequiresRelayBaseURLAndVulaID verifies input
// validation.
func TestRefreshPeerReachability_RequiresRelayBaseURLAndVulaID(t *testing.T) {
	if err := RefreshPeerReachability(context.Background(), "", "vula:ed25519:x"); err == nil {
		t.Fatal("expected error with empty relay base URL")
	}
	if err := RefreshPeerReachability(context.Background(), "https://relay.example.org", ""); err == nil {
		t.Fatal("expected error with empty vula id")
	}
}

// TestStartReachabilityRefresh_EmptyRelayIsNoop verifies that leaving
// VULOS_RELAY_BASE_URL unset (relayBaseURL == "") never touches the network
// or the cache — self-host boxes without a relay see zero behavior change.
func TestStartReachabilityRefresh_EmptyRelayIsNoop(t *testing.T) {
	resetReachabilityCache(t)
	called := false
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	StartReachabilityRefresh(ctx, "", func() []WKApprovedPeer {
		called = true
		return nil
	})
	// Give any (incorrectly) spawned goroutine a moment to misbehave.
	time.Sleep(50 * time.Millisecond)
	if called {
		t.Fatal("StartReachabilityRefresh must not call listApproved when relayBaseURL is empty")
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
	StartReachabilityRefresh(ctx, srv.URL, func() []WKApprovedPeer {
		return []WKApprovedPeer{{VulaID: "vula:ed25519:grace", ServerAddr: "grace.example.org"}}
	})

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if got := resolvePeerBaseURL("vula:ed25519:grace", "grace.example.org"); got == "https://grace-direct.example.net" {
			return // success
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("cache was not warmed by StartReachabilityRefresh within the deadline")
}
