package main

import "testing"

// A VULOS_FABRIC_PEERS entry is dialled with the LAN client — InsecureSkipVerify,
// no SSRF guard, fabric secret in a header — so it has to name the operator's own
// network. See staticPeerIsLocal for the full reasoning.
func TestStaticPeerAddressClassification(t *testing.T) {
	local := []string{
		"https://192.168.1.42:443", "192.168.1.42:443", "192.168.1.42",
		"https://10.0.0.5", "https://172.16.3.9:8443", "http://172.31.255.254",
		"https://127.0.0.1:443", "https://[::1]:443",
		// Link-local, including the IPv6 form a box on a flat network may use.
		"https://169.254.10.3", "https://[fe80::1]:443",
		// Unique-local IPv6 is private.
		"https://[fd00::1]:443",
	}
	for _, e := range local {
		if !staticPeerIsLocal(e) {
			t.Errorf("staticPeerIsLocal(%q) = false, want true — this is a normal LAN peer and "+
				"refusing it reproduces the silent no-sync this setting exists to fix", e)
		}
	}

	remote := []string{
		"https://203.0.113.7", "https://8.8.8.8:443", "https://[2001:db8::1]:443",
		// A NAME cannot be classified without resolving it, and resolution can
		// change under us. The safe answer for an address we cannot vouch for is
		// the one that does not hand out the fabric secret.
		"https://box.example.com", "https://my-other-box", "box.example.com:443",
		// 172.32/12 is OUTSIDE the private range that ends at 172.31 — the
		// off-by-one an eyeballed prefix check gets wrong.
		"https://172.32.0.1",
		"", "   ", "https://",
	}
	for _, e := range remote {
		if staticPeerIsLocal(e) {
			t.Errorf("staticPeerIsLocal(%q) = true, want false — this would be dialled with an "+
				"unverified-TLS client carrying the fabric secret", e)
		}
	}
}
