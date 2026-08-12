package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"vulos/backend/services/reach/tunnel"
	"vulos/backend/services/relayconfig"
)

// reachwire_test.go covers summarizeIngress (reachwire.go), the wiring-layer
// function relayconfig/providers.go's doc comment makes a specific claim
// about: "the ingress status must reflect EVERY live link, not just the
// first relay's URL". That claim is otherwise only reachable through a real
// *tunnel.Agent's live network state (services/reach/tunnel/multirelay_test.go
// exercises the real agent end to end but never inspects reachwire.go's own
// translation), so a regression here — e.g. reverting to "detail := up[0]"
// unconditionally — would be silent. These tests are against literal
// LinkStatus values, no network involved.

func TestSummarizeIngress_TwoLiveLinksBothReported(t *testing.T) {
	links := []tunnel.LinkStatus{
		{Label: "relay1", State: tunnel.LinkUp, PublicURL: "https://box1.relay1.example.com"},
		{Label: "relay2", State: tunnel.LinkUp, PublicURL: "https://box1.relay2.example.net"},
	}
	got := summarizeIngress(links)
	if got.Mode != "relay-tunnel" {
		t.Fatalf("Mode = %q, want relay-tunnel", got.Mode)
	}
	if !strings.Contains(got.Detail, "relay1.example.com") || !strings.Contains(got.Detail, "relay2.example.net") {
		t.Fatalf("Detail = %q, want BOTH live links reported, not just the first", got.Detail)
	}
}

func TestSummarizeIngress_OneLinkUpOneDown_OnlyUpReported(t *testing.T) {
	links := []tunnel.LinkStatus{
		{Label: "relay1", State: tunnel.LinkUp, PublicURL: "https://box1.relay1.example.com"},
		{Label: "relay2", State: tunnel.LinkBackoff, PublicURL: ""},
	}
	got := summarizeIngress(links)
	if got.Detail != "https://box1.relay1.example.com" {
		t.Fatalf("Detail = %q, want exactly the one live link, no trace of the backoff one", got.Detail)
	}
}

// TestSummarizeIngress_StaleURLOnDownLinkIgnored pins the STATE filter on its
// own.
//
// Found by mutation testing: deleting "l.State == tunnel.LinkUp" from
// summarizeIngress passed the entire suite. Every other test happens to give
// its down links an empty PublicURL — which is what runLink does today — so
// they were all satisfied by the URL being blank and none of them actually
// required the state to be consulted. Two independent guards protect the
// honesty claim (runLink clears PublicURL on backoff; summarizeIngress ignores
// any link that is not up), and only one was pinned.
//
// A link carrying a URL it is no longer serving is exactly the state this
// filter exists for: the URL a relay assigned stays valid-looking long after
// the tunnel to it dropped.
func TestSummarizeIngress_StaleURLOnDownLinkIgnored(t *testing.T) {
	links := []tunnel.LinkStatus{
		{Label: "relay1", State: tunnel.LinkUp, PublicURL: "https://box1.relay1.example.com"},
		// Down, but still carrying the URL relay2 assigned before it died.
		{Label: "relay2", State: tunnel.LinkBackoff, PublicURL: "https://box1.relay2.example.net"},
		// Refused outright, likewise.
		{Label: "relay3", State: tunnel.LinkRefused, PublicURL: "https://box1.relay3.example.org"},
	}
	got := summarizeIngress(links)
	if strings.Contains(got.Detail, "relay2.example.net") {
		t.Errorf("Detail = %q advertises relay2, whose link is in BACKOFF", got.Detail)
	}
	if strings.Contains(got.Detail, "relay3.example.org") {
		t.Errorf("Detail = %q advertises relay3, whose link was REFUSED", got.Detail)
	}
	if got.Detail != "https://box1.relay1.example.com" {
		t.Errorf("Detail = %q, want exactly the one link that is actually up", got.Detail)
	}
}

// TestSummarizeIngress_AllDownWithStaleURLs_ReportsNothingUp is the same
// mutant-killing check for the boundary where NO link is up: with the state
// filter gone this returns a confident list of dead URLs instead of the
// honest "nothing is up" message.
func TestSummarizeIngress_AllDownWithStaleURLs_ReportsNothingUp(t *testing.T) {
	links := []tunnel.LinkStatus{
		{Label: "relay1", State: tunnel.LinkBackoff, PublicURL: "https://box1.relay1.example.com"},
		{Label: "relay2", State: tunnel.LinkConnecting, PublicURL: "https://box1.relay2.example.net"},
	}
	got := summarizeIngress(links)
	if strings.Contains(got.Detail, "https://") {
		t.Fatalf("Detail = %q advertises a URL while every link is down", got.Detail)
	}
	if !strings.Contains(got.Detail, "no relay tunnel is currently up") {
		t.Errorf("Detail = %q, want the honest no-tunnel message", got.Detail)
	}
}

func TestSummarizeIngress_NoneUp_HonestNotBlank(t *testing.T) {
	links := []tunnel.LinkStatus{
		{Label: "relay1", State: tunnel.LinkBackoff},
		{Label: "relay2", State: tunnel.LinkConnecting},
	}
	got := summarizeIngress(links)
	if got.Mode != "relay-tunnel" {
		t.Fatalf("Mode = %q, want relay-tunnel even while down (configured, just not up)", got.Mode)
	}
	if strings.Contains(got.Detail, "https://") {
		t.Fatalf("Detail invented a URL while nothing is up: %q", got.Detail)
	}
}

func TestSummarizeIngress_ThreeLiveLinks_AllThreeReported(t *testing.T) {
	// Guards specifically against a regression to "first two only" or any
	// other fixed-arity join — a real box could hold more than two relays.
	links := []tunnel.LinkStatus{
		{Label: "a", State: tunnel.LinkUp, PublicURL: "https://a.example.com"},
		{Label: "b", State: tunnel.LinkUp, PublicURL: "https://b.example.com"},
		{Label: "c", State: tunnel.LinkUp, PublicURL: "https://c.example.com"},
	}
	got := summarizeIngress(links)
	for _, want := range []string{"a.example.com", "b.example.com", "c.example.com"} {
		if !strings.Contains(got.Detail, want) {
			t.Errorf("Detail = %q, missing %q", got.Detail, want)
		}
	}
}

// --- the same claim, against a REAL agent over REAL relays --------------------
//
// Everything above feeds summarizeIngress literal LinkStatus values. That
// proves the translation, and nothing else: the PublicURLs are typed in by the
// test, so a regression anywhere between "a relay accepted this box" and "the
// agent's Status() reports the URL that relay assigned" would leave all four
// tests green. The whole chain — grant accepted, tunnel registered, relay's
// own publicURLFor recorded into LinkStatus, ingressDescriptor reading
// Agent.Status() — is only exercised here.
//
// These use tunnel.NewServer/NewAgent directly (both exported) rather than the
// tunnel package's own unexported test fixtures, which are not importable from
// package main.

// twoRelayAgent brings up two real relay servers on loopback and one real
// agent holding a link to BOTH, blocking until both links report Up. It
// returns the runtime plus the public URL each relay assigned this box.
func twoRelayAgent(t *testing.T, boxName string) (*reachRuntime, string, string, func()) {
	t.Helper()

	newRelay := func(domain, token string) (*tunnel.Server, *httptest.Server, string) {
		t.Helper()
		store, err := tunnel.NewGrantStore([]tunnel.Grant{
			{Token: token, Names: []string{boxName}},
		}, tunnel.Revocations{})
		if err != nil {
			t.Fatalf("NewGrantStore(%s): %v", domain, err)
		}
		srv, err := tunnel.NewServer(tunnel.ServerConfig{Domain: domain, Grants: store})
		if err != nil {
			t.Fatalf("NewServer(%s): %v", domain, err)
		}
		ts := httptest.NewServer(srv.Handler())
		return srv, ts, "ws://" + strings.TrimPrefix(ts.URL, "http://")
	}

	srv1, http1, ws1 := newRelay("r1.test", "tok-r1")
	srv2, http2, ws2 := newRelay("r2.test", "tok-r2")

	upCh := make(chan tunnel.LinkStatus, 64)
	agent, err := tunnel.NewAgent(tunnel.AgentConfig{
		Targets: []tunnel.Target{
			{WSURL: ws1, Name: boxName, Token: "tok-r1", Label: "relay1"},
			{WSURL: ws2, Name: boxName, Token: "tok-r2", Label: "relay2"},
		},
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprint(w, "alive")
		}),
		OnState: func(s tunnel.LinkStatus) {
			select {
			case upCh <- s:
			default:
			}
		},
	})
	if err != nil {
		t.Fatalf("NewAgent: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		_ = agent.Close()
		http2.Close()
		_ = srv2.Close()
	})
	agent.Start(ctx)

	seen := map[string]bool{}
	deadline := time.After(30 * time.Second)
	for len(seen) < 2 {
		select {
		case st := <-upCh:
			switch st.State {
			case tunnel.LinkUp:
				seen[st.Label] = true
			case tunnel.LinkRefused:
				t.Fatalf("link %s refused: %s", st.Label, st.LastError)
			}
		case <-deadline:
			t.Fatalf("both links never came up; status: %+v", agent.Status())
		}
	}

	// The URLs come from the RELAYS' own live rosters, not from this test —
	// that is the point. Asserting exactly one tunnel per relay here also
	// pins that each relay really registered this box once.
	relayURL := func(srv *tunnel.Server, which string) string {
		t.Helper()
		tuns := srv.Tunnels()
		if len(tuns) != 1 {
			t.Fatalf("%s roster has %d tunnels, want exactly 1", which, len(tuns))
		}
		return tuns[0].PublicURL
	}

	var killOnce sync.Once
	killRelay1 := func() {
		killOnce.Do(func() {
			// Order matters and mirrors multirelay_test.go: the WebSocket has
			// been hijacked out of net/http's bookkeeping, so the tunnel
			// Server — not the httptest one — is what tears down the live
			// session the box is holding.
			_ = srv1.Close()
			http1.Close()
		})
	}
	t.Cleanup(killRelay1)

	return &reachRuntime{Agent: agent}, relayURL(srv1, "relay1"), relayURL(srv2, "relay2"), killRelay1
}

// TestIngressDescriptor_RealAgentTwoRelays_NamesBoth is the end-to-end form of
// the "EVERY live link, not just the first" claim in relayconfig/providers.go.
func TestIngressDescriptor_RealAgentTwoRelays_NamesBoth(t *testing.T) {
	rt, url1, url2, _ := twoRelayAgent(t, "box1")

	// VACUITY GUARD: the assertion below is only meaningful if the agent is
	// really holding two links AND the two relays really assigned different
	// URLs. Counted, not guessed: two targets configured above, two relays.
	links := rt.Agent.Status()
	if len(links) != 2 {
		t.Fatalf("agent reports %d links, want exactly 2 — the fixture, not the code, is broken", len(links))
	}
	nUp := 0
	for _, l := range links {
		if l.State == tunnel.LinkUp {
			nUp++
		}
	}
	if nUp != 2 {
		t.Fatalf("%d/2 links up: %+v", nUp, links)
	}
	if url1 == url2 || url1 == "" || url2 == "" {
		t.Fatalf("relays assigned useless URLs %q / %q", url1, url2)
	}

	got := rt.ingressDescriptor()
	if got.Mode != "relay-tunnel" {
		t.Fatalf("Mode = %q, want relay-tunnel", got.Mode)
	}
	if !strings.Contains(got.Detail, url1) {
		t.Errorf("Detail = %q, missing relay1's assigned URL %q", got.Detail, url1)
	}
	if !strings.Contains(got.Detail, url2) {
		t.Errorf("Detail = %q, missing relay2's assigned URL %q — ingress reported only the first relay", got.Detail, url2)
	}
}

// TestIngressDescriptor_RealAgentFailover_DropsTheDeadRelay covers the OTHER
// half of the doc claim, which no test reached at all: ingress "reports what
// is TRUE, not what was configured". After one relay dies, its URL must
// DISAPPEAR from the descriptor while the survivor's remains — a box that kept
// advertising a URL nobody answers sends its operator hunting in the wrong
// place.
func TestIngressDescriptor_RealAgentFailover_DropsTheDeadRelay(t *testing.T) {
	rt, url1, url2, killRelay1 := twoRelayAgent(t, "box1")

	before := rt.ingressDescriptor()
	if !strings.Contains(before.Detail, url1) || !strings.Contains(before.Detail, url2) {
		t.Fatalf("setup: Detail = %q, want both %q and %q", before.Detail, url1, url2)
	}

	killRelay1()

	// Poll until the dead relay's link notices — asynchronous by nature.
	//
	// The survivor is then asserted over a SUSTAINED WINDOW rather than once.
	// Checking it a single time at the moment url1 disappears is a race that
	// hides a real regression: when a link's death cancels the whole agent
	// (links no longer failing independently), relay1's URL drops a beat
	// BEFORE relay2's does, so a one-shot check catches the descriptor
	// mid-teardown and passes. Verified by mutation — a one-shot version of
	// this test stayed green against exactly that bug.
	deadline := time.Now().Add(20 * time.Second)
	var got relayconfig.IngressDescriptor
	sawDrop := false
	for time.Now().Before(deadline) {
		got = rt.ingressDescriptor()
		if !strings.Contains(got.Detail, url1) {
			sawDrop = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !sawDrop {
		t.Fatalf("Detail = %q still advertises the DEAD relay %q after 20s", got.Detail, url1)
	}

	// The survivor must be reported CONTINUOUSLY from here on. One relay's
	// death must not disturb the other's link at all.
	stableUntil := time.Now().Add(3 * time.Second)
	checks := 0
	for time.Now().Before(stableUntil) {
		got = rt.ingressDescriptor()
		checks++
		if strings.Contains(got.Detail, url1) {
			t.Fatalf("check %d: Detail = %q re-advertised the DEAD relay %q", checks, got.Detail, url1)
		}
		if !strings.Contains(got.Detail, url2) {
			t.Fatalf("check %d: Detail = %q dropped the SURVIVING relay %q — one relay's death took the other's link with it", checks, got.Detail, url2)
		}
		if got.Mode != "relay-tunnel" {
			t.Fatalf("check %d: Mode = %q, want relay-tunnel", checks, got.Mode)
		}
		time.Sleep(50 * time.Millisecond)
	}
	// VACUITY GUARD: a 3s window at 50ms must yield tens of samples. A floor
	// of 10 is far below that and still proves the loop ran.
	if checks < 10 {
		t.Fatalf("only %d stability samples taken, expected dozens — the loop did not run", checks)
	}
}
