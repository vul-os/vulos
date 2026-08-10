package main

import (
	"os"
	"strings"
	"testing"
)

// Break-glass fleet-identity recovery was half-built: this box could be ASKED
// to vouch, but `fleetid.GatherQuorum` had no production caller anywhere, so it
// could never ask. Its tests passed the whole time.
//
// That is the exact shape of `services/sync/hotpath.go`, which shipped with
// passing tests, zero callers and a route registered on no mux, and was
// eventually deleted as dead code. Its replacement guard then turned out to be
// a source-text grep that `if false { … }` would satisfy — so these use the AST
// helper (gateChain, in crdtsync_wiring_test.go) that pins the chain of
// conditions actually gating the call.

func TestFleetGatherCallSiteIsReachable(t *testing.T) {
	gates, found := gateChain(t, "main.go", "registerFleetGatherRoute")
	if !found {
		t.Fatal("main.go never calls registerFleetGatherRoute — this box cannot initiate a quorum, so break-glass recovery is unusable")
	}

	// A call that still greps but never runs is the failure this exists to
	// catch, so a constant-false gate is refused outright.
	for _, g := range gates {
		if g == "false" || strings.HasPrefix(g, "false &&") {
			t.Fatalf("the call to registerFleetGatherRoute is unreachable — gated by %q", g)
		}
	}
}

// The gather route is the INITIATOR half and is owner + step-up gated, unlike
// the peer-facing vouch REQUEST endpoint which is deliberately public and
// authenticates its own payload. Confusing the two would either expose quorum
// initiation to any caller or lock peers out of requesting a vouch, so the
// distinction is asserted rather than left to a reader of main.go.
func TestFleetGatherIsNotOnThePublicPath(t *testing.T) {
	raw, err := os.ReadFile("fleetid_wiring.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(raw)

	// The initiator route must not be added to the auth exemption list. Only
	// /api/fleetid/vouch/request belongs there.
	if strings.Contains(src, "publicPaths") {
		t.Error("fleetid_wiring.go touches publicPaths — the gather route is owner-gated and must not be exempted from the session gate")
	}
}
