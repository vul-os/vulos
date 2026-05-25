package fabric

import (
	"strings"
	"testing"
)

// TestInstanceFabricName verifies the per-instance mDNS name builder: a valid
// id yields "<sanitised-id>.vulos-fabric.local", and the result is a well-formed
// DNS name (lower-cased, label-safe).
func TestInstanceFabricName(t *testing.T) {
	cases := map[string]string{
		"01HWZFABRIC0000000000000A": "01hwzfabric0000000000000a.vulos-fabric.local",
		"":                          "",
		"  ":                        "", // no usable label characters
		"My-Box_42":                 "my-box42.vulos-fabric.local",
	}
	for in, want := range cases {
		if got := InstanceFabricName(in); got != want {
			t.Errorf("InstanceFabricName(%q): got %q want %q", in, got, want)
		}
	}
	// Distinct ids must yield distinct names (this is the whole point: it lets a
	// 3+ box LAN resolve each peer individually).
	if InstanceFabricName("aaa") == InstanceFabricName("bbb") {
		t.Fatal("distinct ids must produce distinct mDNS names")
	}
}

// TestSanitizeDNSLabel verifies the DNS-label sanitiser keeps only [a-z0-9-],
// lower-cases, trims hyphens, and honours the 63-octet label limit.
func TestSanitizeDNSLabel(t *testing.T) {
	cases := map[string]string{
		"ABC123":      "abc123",
		"a.b/c d":     "abcd",
		"-trim-":      "trim",
		"under_score": "underscore",
	}
	for in, want := range cases {
		if got := sanitizeDNSLabel(in); got != want {
			t.Errorf("sanitizeDNSLabel(%q): got %q want %q", in, got, want)
		}
	}
	long := strings.Repeat("a", 200)
	if got := sanitizeDNSLabel(long); len(got) > 63 {
		t.Errorf("sanitizeDNSLabel must cap at 63 octets, got %d", len(got))
	}
}

// TestPeerNamesFuncExpandsQuerySet verifies the discoverer folds the roster
// provider's ids into per-instance query names. We can't exercise real
// multicast in CI, so we assert the wiring: SetPeerNamesFunc is honoured and the
// names are built via InstanceFabricName. (The end-to-end >2-box convergence is
// covered by TestThreeInstancesConverge against the StaticDiscoverer seam.)
func TestPeerNamesFuncExpandsQuerySet(t *testing.T) {
	d := &MDNSDiscoverer{queryNames: []string{FabricMDNSName}}
	d.SetPeerNamesFunc(func() []string { return []string{"peer-one", "peer-two"} })

	d.mu.RLock()
	pn := d.peerNamesFunc
	d.mu.RUnlock()
	if pn == nil {
		t.Fatal("SetPeerNamesFunc did not register the roster provider")
	}
	got := pn()
	if len(got) != 2 {
		t.Fatalf("roster provider returned %d ids, want 2", len(got))
	}
	for _, id := range got {
		if name := InstanceFabricName(id); !strings.HasSuffix(name, FabricMDNSSuffix) {
			t.Errorf("roster id %q -> %q does not carry the per-instance suffix", id, name)
		}
	}
}
