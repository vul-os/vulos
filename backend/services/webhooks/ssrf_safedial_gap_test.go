// ssrf_safedial_gap_test.go — regression coverage for the gap closed by
// deleting this package's parallel SSRF deny-list in favour of delegating to
// backend/internal/safedial (see ssrf.go's package comment).
//
// Before that change, this package carried its OWN copy of isBlockedIP that
// checked only: loopback, link-local, RFC1918, ULA fc00::/7, CGNAT, and
// multicast/unspecified. It did NOT block:
//
//   - The IPv4 limited-broadcast address 255.255.255.255.
//   - 6to4 (2002::/16) and NAT64 (64:ff9b::/96) IPv6 prefixes, which can
//     encapsulate an RFC1918 or link-local/metadata IPv4 address and, on a
//     network with 6to4/NAT64 translation configured, actually reach it —
//     a documented technique for bypassing IPv4-only SSRF filters.
//
// safedial's deny-list already covered all three (see
// internal/safedial/safedial.go's alwaysDeniedCIDRs/strictDeniedCIDRs). These
// tests fail on the old duplicated deny-list and pass now that ssrf.go
// delegates to safedial; they exist so nobody can silently re-fork the
// deny-list here without breaking a test.
package webhooks_test

import (
	"context"
	"testing"

	"vulos/backend/services/webhooks"
)

func TestCreateSubscriptionRejectsBroadcastAndTunneledPrivateRanges(t *testing.T) {
	st, err := webhooks.OpenStore("") // production store: SSRF guard enabled
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	cases := []struct {
		name string
		url  string
	}{
		{"IPv4 limited broadcast", "http://255.255.255.255/hook"},
		{"NAT64-encapsulated metadata IP (64:ff9b::/96 + 169.254.169.254)", "http://[64:ff9b::a9fe:a9fe]/hook"},
		{"6to4-encapsulated RFC1918 IP (2002::/16 + 192.168.1.1)", "http://[2002:c0a8:0101::]/hook"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			_, err := st.CreateSubscription(context.Background(), "admin-gap-test", tc.url, []string{"device.enrolled"})
			if err == nil {
				t.Fatalf("URL %q should have been blocked (this is the gap the old duplicate SSRF deny-list left open) but CreateSubscription accepted it", tc.url)
			}
			t.Logf("correctly blocked %q: %v", tc.url, err)
		})
	}
}
