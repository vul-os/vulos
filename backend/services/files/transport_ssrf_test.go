package files

// transport_ssrf_test.go — SSRF-FILES-01 regression tests for validateOwnerAddr.
//
// These tests verify that the SSRF deny-list applied to peer-share capability
// OwnerAddr values correctly blocks metadata/private/loopback addresses while
// allowing public addresses and (when VULOS_PEER_ALLOW_LAN=1) LAN addresses.

import (
	"sync"
	"testing"
)

// resetPeerAllowLAN resets the once-flag so individual tests can control the
// VULOS_PEER_ALLOW_LAN state cleanly.
func resetPeerAllowLAN(t *testing.T, allow bool) {
	t.Helper()
	peerAllowLANOnce = sync.Once{}
	peerAllowLAN = allow
	peerAllowLANOnce.Do(func() { peerAllowLAN = allow })
}

// ── Blocked without LAN flag ──────────────────────────────────────────────────

// TestValidateOwnerAddr_BlocksLoopback verifies that 127.0.0.1 is always blocked.
// A peer-share target on 127.x.x.x is the recipient box itself — never valid.
func TestValidateOwnerAddr_BlocksLoopback(t *testing.T) {
	resetPeerAllowLAN(t, false)
	if err := validateOwnerAddr("http://127.0.0.1:8080"); err == nil {
		t.Fatal("SSRF-FILES-01 REGRESSION: loopback 127.0.0.1 was not blocked")
	}
}

// TestValidateOwnerAddr_BlocksLocalhostHostname verifies "localhost" is blocked.
func TestValidateOwnerAddr_BlocksLocalhostHostname(t *testing.T) {
	resetPeerAllowLAN(t, false)
	if err := validateOwnerAddr("http://localhost:3000"); err == nil {
		t.Fatal("SSRF-FILES-01 REGRESSION: localhost hostname was not blocked")
	}
}

// TestValidateOwnerAddr_BlocksMetadataIP verifies 169.254.169.254 is always blocked.
// This is the cloud IMDSv1/v2 endpoint — never a legitimate peer-share target.
func TestValidateOwnerAddr_BlocksMetadataIP(t *testing.T) {
	resetPeerAllowLAN(t, false)
	if err := validateOwnerAddr("http://169.254.169.254/"); err == nil {
		t.Fatal("SSRF-FILES-01 REGRESSION: metadata IP 169.254.169.254 was not blocked")
	}
}

// TestValidateOwnerAddr_BlocksMetadataIP_WithLANFlag verifies 169.254.169.254
// is STILL blocked even when VULOS_PEER_ALLOW_LAN=1.  Metadata addresses are
// permanently denied because no peer-share box lives there.
func TestValidateOwnerAddr_BlocksMetadataIP_WithLANFlag(t *testing.T) {
	resetPeerAllowLAN(t, true)
	if err := validateOwnerAddr("http://169.254.169.254/latest/meta-data/"); err == nil {
		t.Fatal("SSRF-FILES-01 REGRESSION: metadata IP 169.254.169.254 was not blocked even with VULOS_PEER_ALLOW_LAN=1 — permanently denied address bypassed")
	}
}

// TestValidateOwnerAddr_BlocksPrivateRFC1918_NoLAN verifies RFC1918 private
// addresses are blocked without the LAN opt-in flag.
func TestValidateOwnerAddr_BlocksPrivateRFC1918_NoLAN(t *testing.T) {
	resetPeerAllowLAN(t, false)
	cases := []string{
		"http://10.0.0.1:8080",
		"http://192.168.1.100/",
		"http://172.16.0.1:9000",
	}
	for _, addr := range cases {
		if err := validateOwnerAddr(addr); err == nil {
			t.Errorf("SSRF-FILES-01 REGRESSION: private addr %s was not blocked (VULOS_PEER_ALLOW_LAN=0)", addr)
		}
	}
}

// TestValidateOwnerAddr_BlocksUnspecified verifies 0.0.0.0 is always blocked.
func TestValidateOwnerAddr_BlocksUnspecified(t *testing.T) {
	resetPeerAllowLAN(t, false)
	if err := validateOwnerAddr("http://0.0.0.0:8080"); err == nil {
		t.Fatal("SSRF-FILES-01 REGRESSION: unspecified address 0.0.0.0 was not blocked")
	}
}

// TestValidateOwnerAddr_BlocksHexEncodedLoopback verifies hex-encoded loopback
// IP (0x7f000001 = 127.0.0.1) is blocked by the alt-IPv4 parser.
func TestValidateOwnerAddr_BlocksHexEncodedLoopback(t *testing.T) {
	resetPeerAllowLAN(t, false)
	// 0x7f000001 = 127.0.0.1
	if err := validateOwnerAddr("http://0x7f000001:8080"); err == nil {
		t.Fatal("SSRF-FILES-01 REGRESSION: hex-encoded loopback 0x7f000001 was not blocked")
	}
}

// TestValidateOwnerAddr_BlocksDecimalEncodedLoopback verifies decimal-encoded
// loopback (2130706433 = 127.0.0.1) is blocked.
func TestValidateOwnerAddr_BlocksDecimalEncodedLoopback(t *testing.T) {
	resetPeerAllowLAN(t, false)
	// 2130706433 = 0x7f000001 = 127.0.0.1
	if err := validateOwnerAddr("http://2130706433:8080"); err == nil {
		t.Fatal("SSRF-FILES-01 REGRESSION: decimal-encoded loopback 2130706433 was not blocked")
	}
}

// TestValidateOwnerAddr_BlocksBadScheme verifies that non-http/https schemes
// are rejected (e.g. file://, gopher://).
func TestValidateOwnerAddr_BlocksBadScheme(t *testing.T) {
	resetPeerAllowLAN(t, false)
	cases := []string{
		"file:///etc/shadow",
		"gopher://evil.example/",
		"ftp://10.0.0.1/secret",
		"//10.0.0.1/share",
		"not-a-url",
	}
	for _, addr := range cases {
		if err := validateOwnerAddr(addr); err == nil {
			t.Errorf("SSRF-FILES-01 REGRESSION: bad-scheme addr %q was not blocked", addr)
		}
	}
}

// ── Allowed with LAN flag ─────────────────────────────────────────────────────

// TestValidateOwnerAddr_AllowsPrivateWithLANFlag verifies that RFC1918 private
// addresses ARE allowed when VULOS_PEER_ALLOW_LAN=1 (LAN peer-share opt-in).
func TestValidateOwnerAddr_AllowsPrivateWithLANFlag(t *testing.T) {
	resetPeerAllowLAN(t, true)
	cases := []string{
		"http://192.168.1.5:8080",
		"http://10.0.0.2:8080",
		"https://172.16.50.1:9000",
	}
	for _, addr := range cases {
		if err := validateOwnerAddr(addr); err != nil {
			t.Errorf("SSRF-FILES-01: private addr %s should be allowed with VULOS_PEER_ALLOW_LAN=1, got err: %v", addr, err)
		}
	}
}

// ── Structural: always-blocked regardless of env ──────────────────────────────

// TestValidateOwnerAddr_AlwaysBlocksLoopback verifies loopback is blocked
// even with the LAN flag set.
func TestValidateOwnerAddr_AlwaysBlocksLoopback_WithLAN(t *testing.T) {
	resetPeerAllowLAN(t, true)
	if err := validateOwnerAddr("http://127.0.0.1/"); err == nil {
		t.Fatal("SSRF-FILES-01 REGRESSION: loopback 127.0.0.1 not blocked even with VULOS_PEER_ALLOW_LAN=1")
	}
}

// TestValidateOwnerAddr_EmptyAddr verifies empty/blank addresses are rejected.
func TestValidateOwnerAddr_EmptyAddr(t *testing.T) {
	resetPeerAllowLAN(t, false)
	cases := []string{"", "  ", "/"}
	for _, addr := range cases {
		if err := validateOwnerAddr(addr); err == nil {
			t.Errorf("SSRF-FILES-01: empty/blank addr %q should be rejected", addr)
		}
	}
}
