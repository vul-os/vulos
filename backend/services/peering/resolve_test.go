package peering

import "testing"

// TestResolvePeerBaseURL_PassThrough locks in the CONSOLIDATION B-0 contract:
// the reachability seam is a byte-compatible pass-through that produces exactly
// what the live call sites used to compute inline ("https://" + contact.Server).
func TestResolvePeerBaseURL_PassThrough(t *testing.T) {
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
