package main

import (
	"strings"
	"testing"

	"vulos/backend/services/reach/tunnel"
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
