package tunnel

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// multirelay_test.go — the scenarios that were previously exercised BY HAND
// and nowhere else: two boxes sharing one relay, four simultaneous tunnels
// across two boxes and two relays, and a relay dying out from under a box
// that also holds a tunnel to a second one. See also reach_test.go
// (TestOneBadEntryRefusesTheWholeSet, TestEndpointsFileMustNotBeWorldAccessible)
// for the box-side halves of the config-hygiene scenarios; this file adds
// their RELAY-side counterparts (the grants file), which had no test at all.

// startMultiAgent attaches an agent with several targets and blocks until
// EVERY target's link reports Up (or any of them is permanently refused).
func startMultiAgent(t *testing.T, targets []Target, h http.Handler) *Agent {
	t.Helper()
	up := make(chan LinkStatus, 64)
	a, err := NewAgent(AgentConfig{
		Targets: targets,
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

	seenUp := make(map[string]bool, len(targets))
	deadline := time.After(30 * time.Second)
	for len(seenUp) < len(targets) {
		select {
		case st := <-up:
			switch st.State {
			case LinkUp:
				seenUp[st.Label] = true
			case LinkRefused:
				t.Fatalf("agent %s refused: %s", st.Label, st.LastError)
			}
		case <-deadline:
			t.Fatalf("not every link came up; status: %+v", a.Status())
		}
	}
	return a
}

// --- two boxes, one relay ----------------------------------------------------

// TestTwoBoxesOneRelaySimultaneously covers the first hand-tested scenario:
// two DIFFERENT boxes, holding independent tunnels to the SAME relay at the
// same time, each reachable at its own name without leaking into the other's
// traffic.
func TestTwoBoxesOneRelaySimultaneously(t *testing.T) {
	f := newRelayFixture(t, []Grant{
		{Token: "tok-box1", Names: []string{"box1"}},
		{Token: "tok-box2", Names: []string{"box2"}},
	}, nil)

	f.startAgent(t, "box1", "tok-box1", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "i am box1")
	}))
	f.startAgent(t, "box2", "tok-box2", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "i am box2")
	}))

	if n := len(f.srv.Tunnels()); n != 2 {
		t.Fatalf("relay reports %d live tunnels, want 2", n)
	}

	// Interleave requests to both names; each must only ever see its own box.
	var wg sync.WaitGroup
	errs := make(chan error, 40)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			name, want := "box1", "i am box1"
			if i%2 == 1 {
				name, want = "box2", "i am box2"
			}
			resp := f.get(t, name, "/", nil)
			got := body(t, resp)
			if got != want {
				errs <- fmt.Errorf("request %d: name=%s got %q want %q", i, name, got, want)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

// --- two boxes, two relays, four tunnels -------------------------------------

// TestFourTunnelsAcrossTwoRelays covers the second hand-tested scenario: two
// boxes, each holding a tunnel to BOTH of two relays at once (the documented
// "affinity-bound" design — a box holds a link to every configured relay, not
// just one), for four live tunnels total. Every one of the four combinations
// must be independently reachable.
func TestFourTunnelsAcrossTwoRelays(t *testing.T) {
	relay1 := newRelayFixture(t, []Grant{
		{Token: "r1-box1", Names: []string{"box1"}},
		{Token: "r1-box2", Names: []string{"box2"}},
	}, nil)
	relay2 := newRelayFixture(t, []Grant{
		{Token: "r2-box1", Names: []string{"box1"}},
		{Token: "r2-box2", Names: []string{"box2"}},
	}, nil)

	// One handler per box, shared across BOTH of that box's links — exactly as
	// in production, where the OS's single http.Handler serves every relay a
	// box is tunnelled to. Distinguishing "which physical relay" from inside
	// the handler is neither possible (the vouched HeaderRelay is stripped
	// before the handler sees the request — see tunnelHandler) nor the point:
	// the point is that all four (box, relay) combinations independently
	// route to the right box.
	mkHandler := func(box string) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprintf(w, "hello from %s", box)
		})
	}

	startMultiAgent(t, []Target{
		{WSURL: relay1.wsURL, Name: "box1", Token: "r1-box1", Label: "relay1"},
		{WSURL: relay2.wsURL, Name: "box1", Token: "r2-box1", Label: "relay2"},
	}, mkHandler("box1"))
	startMultiAgent(t, []Target{
		{WSURL: relay1.wsURL, Name: "box2", Token: "r1-box2", Label: "relay1"},
		{WSURL: relay2.wsURL, Name: "box2", Token: "r2-box2", Label: "relay2"},
	}, mkHandler("box2"))

	if n := len(relay1.srv.Tunnels()); n != 2 {
		t.Fatalf("relay1 reports %d live tunnels, want 2", n)
	}
	if n := len(relay2.srv.Tunnels()); n != 2 {
		t.Fatalf("relay2 reports %d live tunnels, want 2", n)
	}

	cases := []struct {
		relay *relayFixture
		name  string
		want  string
	}{
		{relay1, "box1", "hello from box1"},
		{relay1, "box2", "hello from box2"},
		{relay2, "box1", "hello from box1"},
		{relay2, "box2", "hello from box2"},
	}
	for _, tc := range cases {
		resp := tc.relay.get(t, tc.name, "/", nil)
		if got := body(t, resp); got != tc.want {
			t.Errorf("%s@%s: got %q want %q", tc.name, tc.relay.domain, got, tc.want)
		}
	}
}

// --- failover -----------------------------------------------------------------

// TestFailoverToSecondRelayWhenFirstDies covers the third hand-tested
// scenario: a box holding tunnels to two relays keeps serving through the
// SECOND when the FIRST disappears out from under it — the whole reason
// "hold every relay at once" exists (docs/REACH.md, "Multiple relays"). The
// dead relay's link must back off, not the agent as a whole.
func TestFailoverToSecondRelayWhenFirstDies(t *testing.T) {
	relay1 := newRelayFixture(t, []Grant{{Token: "tok1", Names: []string{"box1"}}}, nil)
	relay2 := newRelayFixture(t, []Grant{{Token: "tok2", Names: []string{"box1"}}}, nil)

	agent := startMultiAgent(t, []Target{
		{WSURL: relay1.wsURL, Name: "box1", Token: "tok1", Label: "relay1"},
		{WSURL: relay2.wsURL, Name: "box1", Token: "tok2", Label: "relay2"},
	}, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "alive")
	}))

	// Sanity: both serve before the kill.
	if got := body(t, relay1.get(t, "box1", "/", nil)); got != "alive" {
		t.Fatalf("setup: relay1 not serving: %q", got)
	}
	if got := body(t, relay2.get(t, "box1", "/", nil)); got != "alive" {
		t.Fatalf("setup: relay2 not serving: %q", got)
	}

	// Kill relay1 out from under the agent — the process going away, not a
	// graceful drain. Server.Close() alone is not enough: the tunnel's
	// WebSocket connection has been hijacked out of net/http's own bookkeeping
	// (that is what an upgrade IS), so the stdlib server no longer knows the
	// connection exists and will not touch it on Close(). srv.Close() is what
	// actually tears down the live yamux session (and therefore the
	// underlying socket) that the box's link is holding — the same thing that
	// happens when a relay process is kill -9'd.
	relay1.srv.Close()
	relay1.http.Close()

	// relay2 must keep serving throughout, with no interruption caused by
	// relay1's death — links fail independently.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		resp := relay2.get(t, "box1", "/", nil)
		if got := body(t, resp); got != "alive" {
			t.Fatalf("relay2 stopped serving after relay1 died: %q", got)
		}
		time.Sleep(50 * time.Millisecond)
	}

	// And the agent's OWN view must show relay1 backing off rather than the
	// whole agent going dark — this is the property, not just its symptom.
	sawBackoff := false
	for _, st := range agent.Status() {
		if st.Label == "relay1" && (st.State == LinkBackoff || st.State == LinkConnecting) {
			sawBackoff = true
		}
		if st.Label == "relay2" && st.State != LinkUp {
			t.Errorf("relay2's link degraded too: %+v", st)
		}
	}
	if !sawBackoff {
		t.Errorf("relay1's link never left LinkUp after the relay died: %+v", agent.Status())
	}
}

// --- relay-side config hygiene: grants file --------------------------------
//
// reach_test.go covers the BOX-side halves of these two properties (the
// endpoints file). Nothing previously exercised the RELAY side, even though
// grants.go's readSecretFile implements the identical rule for the grants
// file — and the grants file is the one holding every box's bearer token.

// TestGrantsFileWorldAccessibleRefused: a relay's grants file holds bearer
// tokens for every box it serves, so a permissive mode must be a startup
// error, exactly like the box-side endpoints file.
func TestGrantsFileWorldAccessibleRefused(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "grants.json")
	body := []byte(`[{"token":"t","names":["box1"]}]`)
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := LoadGrantsFile(path); err == nil {
		t.Fatal("a world-readable grants file was accepted")
	} else if !strings.Contains(err.Error(), "chmod 600") {
		t.Errorf("error should tell the operator how to fix it, got: %v", err)
	}

	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	grants, err := LoadGrantsFile(path)
	if err != nil {
		t.Fatalf("0600 grants file refused: %v", err)
	}
	if len(grants) != 1 {
		t.Fatalf("got %d grants, want 1", len(grants))
	}
}

// TestOneBadGrantRefusesTheWholeStore is the relay-side twin of
// reach_test.go's TestOneBadEntryRefusesTheWholeSet: an operator who typo'd
// one grant among several must not silently end up with a relay serving the
// others while believing all were accepted.
func TestOneBadGrantRefusesTheWholeStore(t *testing.T) {
	store, err := NewGrantStore([]Grant{
		{Token: "good-a", Names: []string{"box1"}},
		{Token: "good-b", Names: []string{"Box-Two"}}, // uppercase: invalid name
		{Token: "good-c", Names: []string{"box3"}},
	}, Revocations{})
	if err == nil {
		t.Fatal("a grant list with one bad entry was accepted")
	}
	if !strings.Contains(err.Error(), "grant 1") {
		t.Errorf("error should name the offending index, got: %v", err)
	}
	if store != nil {
		t.Error("a refused grant list must yield a nil store, not a partial one")
	}
}
