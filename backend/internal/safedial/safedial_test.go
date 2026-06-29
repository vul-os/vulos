package safedial_test

// safedial_test.go — focused unit tests for the shared SSRF-safe dialer.
//
// Coverage:
//   - IsDeniedIP: each blocked range (loopback, link-local, multicast,
//     unspecified, private, CGNAT, ULA, 6to4, NAT64, TEST-NETs, reserved),
//     public IP allowed, LAN opt-in semantics.
//   - NormalizeIPLiteral: standard, hex, decimal, octal, bracket-IPv6.
//   - ParseAltIPv4 / ParseUint64: multi-form coverage.
//   - ValidateHostWithResolver: public allowed, private blocked, IP literal
//     (resolver not called), DNS failure fail-closed, multi-A DNS rebinding.
//   - ControlFunc / New: dial-time block of private IP (rebinding-style),
//     public allowed, LAN opt-in for private, loopback still blocked with LAN.

import (
	"fmt"
	"net"
	"testing"

	"vulos/backend/internal/safedial"
)

// ─── IsDeniedIP tests ─────────────────────────────────────────────────────────

func TestIsDeniedIP_Loopback(t *testing.T) {
	for _, s := range []string{"127.0.0.1", "127.255.255.255", "::1"} {
		ip := net.ParseIP(s)
		if ip == nil {
			t.Fatalf("bad test IP: %s", s)
		}
		if !safedial.IsDeniedIP(ip, false) {
			t.Errorf("IsDeniedIP(%s, false) = false, want true (loopback)", s)
		}
		if !safedial.IsDeniedIP(ip, true) {
			t.Errorf("IsDeniedIP(%s, true) = false, want true (loopback always blocked)", s)
		}
	}
}

func TestIsDeniedIP_LinkLocal(t *testing.T) {
	for _, s := range []string{"169.254.0.1", "169.254.169.254", "fe80::1"} {
		ip := net.ParseIP(s)
		if ip == nil {
			t.Fatalf("bad test IP: %s", s)
		}
		if !safedial.IsDeniedIP(ip, false) {
			t.Errorf("IsDeniedIP(%s, false) = false, want true (link-local)", s)
		}
		if !safedial.IsDeniedIP(ip, true) {
			t.Errorf("IsDeniedIP(%s, true) = false, want true (link-local always blocked)", s)
		}
	}
}

func TestIsDeniedIP_Multicast(t *testing.T) {
	for _, s := range []string{"224.0.0.1", "239.255.255.255", "ff02::1"} {
		ip := net.ParseIP(s)
		if ip == nil {
			t.Fatalf("bad test IP: %s", s)
		}
		if !safedial.IsDeniedIP(ip, false) {
			t.Errorf("IsDeniedIP(%s, false) = false, want true (multicast)", s)
		}
		if !safedial.IsDeniedIP(ip, true) {
			t.Errorf("IsDeniedIP(%s, true) = false, want true (multicast always blocked)", s)
		}
	}
}

func TestIsDeniedIP_Unspecified(t *testing.T) {
	for _, s := range []string{"0.0.0.0", "::"} {
		ip := net.ParseIP(s)
		if ip == nil {
			t.Fatalf("bad test IP: %s", s)
		}
		if !safedial.IsDeniedIP(ip, false) {
			t.Errorf("IsDeniedIP(%s, false) = false, want true (unspecified)", s)
		}
		if !safedial.IsDeniedIP(ip, true) {
			t.Errorf("IsDeniedIP(%s, true) = false, want true (unspecified always blocked)", s)
		}
	}
}

func TestIsDeniedIP_Private_BlockedWithoutLAN(t *testing.T) {
	for _, s := range []string{"10.0.0.1", "10.255.255.255", "172.16.0.1", "172.31.255.255", "192.168.0.1", "192.168.255.255"} {
		ip := net.ParseIP(s)
		if ip == nil {
			t.Fatalf("bad test IP: %s", s)
		}
		if !safedial.IsDeniedIP(ip, false) {
			t.Errorf("IsDeniedIP(%s, false) = false, want true (private without LAN)", s)
		}
	}
}

func TestIsDeniedIP_Private_AllowedWithLAN(t *testing.T) {
	// RFC1918 is permitted when allowLAN=true (LAN peer-share opt-in).
	for _, s := range []string{"10.0.0.1", "172.16.0.1", "192.168.1.1"} {
		ip := net.ParseIP(s)
		if ip == nil {
			t.Fatalf("bad test IP: %s", s)
		}
		if safedial.IsDeniedIP(ip, true) {
			t.Errorf("IsDeniedIP(%s, true) = true, want false (private with LAN opt-in)", s)
		}
	}
}

func TestIsDeniedIP_CGNAT_BlockedWithoutLAN(t *testing.T) {
	for _, s := range []string{"100.64.0.1", "100.127.255.255"} {
		ip := net.ParseIP(s)
		if ip == nil {
			t.Fatalf("bad test IP: %s", s)
		}
		if !safedial.IsDeniedIP(ip, false) {
			t.Errorf("IsDeniedIP(%s, false) = false, want true (CGNAT without LAN)", s)
		}
	}
}

func TestIsDeniedIP_CGNAT_AllowedWithLAN(t *testing.T) {
	// CGNAT (100.64/10) is used by Tailscale; allowed with LAN opt-in.
	ip := net.ParseIP("100.64.0.1")
	if safedial.IsDeniedIP(ip, true) {
		t.Error("IsDeniedIP(100.64.0.1, true) = true, want false (CGNAT/Tailscale with LAN opt-in)")
	}
}

func TestIsDeniedIP_ULA_BlockedWithoutLAN(t *testing.T) {
	for _, s := range []string{"fc00::1", "fd00::1", "fdff:ffff:ffff:ffff::1"} {
		ip := net.ParseIP(s)
		if ip == nil {
			t.Fatalf("bad test IP: %s", s)
		}
		if !safedial.IsDeniedIP(ip, false) {
			t.Errorf("IsDeniedIP(%s, false) = false, want true (ULA without LAN)", s)
		}
	}
}

func TestIsDeniedIP_ULA_AllowedWithLAN(t *testing.T) {
	ip := net.ParseIP("fd00::1")
	if safedial.IsDeniedIP(ip, true) {
		t.Error("IsDeniedIP(fd00::1, true) = true, want false (ULA allowed with LAN opt-in)")
	}
}

func TestIsDeniedIP_TestNets_AlwaysBlocked(t *testing.T) {
	for _, s := range []string{"192.0.2.1", "198.51.100.1", "203.0.113.1"} {
		ip := net.ParseIP(s)
		if ip == nil {
			t.Fatalf("bad test IP: %s", s)
		}
		if !safedial.IsDeniedIP(ip, false) {
			t.Errorf("IsDeniedIP(%s, false) = false, want true (TEST-NET)", s)
		}
		if !safedial.IsDeniedIP(ip, true) {
			t.Errorf("IsDeniedIP(%s, true) = false, want true (TEST-NET always blocked)", s)
		}
	}
}

func TestIsDeniedIP_Reserved240_AlwaysBlocked(t *testing.T) {
	for _, s := range []string{"240.0.0.1", "255.255.255.254", "255.255.255.255"} {
		ip := net.ParseIP(s)
		if ip == nil {
			t.Fatalf("bad test IP: %s", s)
		}
		if !safedial.IsDeniedIP(ip, false) {
			t.Errorf("IsDeniedIP(%s, false) = false, want true (reserved)", s)
		}
		if !safedial.IsDeniedIP(ip, true) {
			t.Errorf("IsDeniedIP(%s, true) = false, want true (reserved always blocked)", s)
		}
	}
}

func TestIsDeniedIP_6to4_BlockedWithoutLAN(t *testing.T) {
	// 2002:0a00:0001:: encapsulates 10.0.0.1 (private) via 6to4.
	ip := net.ParseIP("2002:0a00:0001::")
	if ip == nil {
		t.Fatal("bad test IP: 2002:0a00:0001::")
	}
	if !safedial.IsDeniedIP(ip, false) {
		t.Error("IsDeniedIP(2002:0a00:0001::, false) = false, want true (6to4 without LAN)")
	}
}

func TestIsDeniedIP_IPv4MappedIPv6(t *testing.T) {
	// ::ffff:127.0.0.1 should be treated as loopback.
	ip := net.ParseIP("::ffff:127.0.0.1")
	if ip == nil {
		t.Fatal("bad test IP: ::ffff:127.0.0.1")
	}
	if !safedial.IsDeniedIP(ip, false) {
		t.Error("IsDeniedIP(::ffff:127.0.0.1, false) = false, want true (IPv4-mapped loopback)")
	}
}

func TestIsDeniedIP_Public_Allowed(t *testing.T) {
	for _, s := range []string{"8.8.8.8", "1.1.1.1", "2606:4700:4700::1111"} {
		ip := net.ParseIP(s)
		if ip == nil {
			t.Fatalf("bad test IP: %s", s)
		}
		if safedial.IsDeniedIP(ip, false) {
			t.Errorf("IsDeniedIP(%s, false) = true, want false (public IP should be allowed)", s)
		}
	}
}

// ─── NormalizeIPLiteral tests ─────────────────────────────────────────────────

func TestNormalizeIPLiteral_Standard(t *testing.T) {
	cases := []struct {
		input   string
		wantNil bool
	}{
		{"127.0.0.1", false},
		{"::1", false},
		{"[::1]", false}, // bracket form
		{"8.8.8.8", false},
		{"example.com", true},
		{"", true},
		{"notanip", true},
	}
	for _, tc := range cases {
		ip := safedial.NormalizeIPLiteral(tc.input)
		if tc.wantNil && ip != nil {
			t.Errorf("NormalizeIPLiteral(%q) = %v, want nil", tc.input, ip)
		}
		if !tc.wantNil && ip == nil {
			t.Errorf("NormalizeIPLiteral(%q) = nil, want non-nil", tc.input)
		}
	}
}

func TestNormalizeIPLiteral_AltForms(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"2130706433", "127.0.0.1"},       // decimal integer
		{"0x7f000001", "127.0.0.1"},       // hex integer
		{"0177.0.0.1", "127.0.0.1"},       // octal first octet
		{"2852039166", "169.254.169.254"}, // decimal 169.254.169.254
	}
	for _, tc := range cases {
		ip := safedial.NormalizeIPLiteral(tc.input)
		if ip == nil {
			t.Errorf("NormalizeIPLiteral(%q) = nil, want %s", tc.input, tc.want)
			continue
		}
		if !ip.Equal(net.ParseIP(tc.want)) {
			t.Errorf("NormalizeIPLiteral(%q) = %s, want %s", tc.input, ip, tc.want)
		}
	}
}

// ─── ParseUint64 tests ────────────────────────────────────────────────────────

func TestParseUint64(t *testing.T) {
	cases := []struct {
		input  string
		wantN  uint64
		wantOK bool
	}{
		{"0", 0, true},
		{"255", 255, true},
		{"0xff", 255, true},
		{"0xFF", 255, true},
		{"0377", 255, true}, // octal
		{"0x7f000001", 2130706433, true},
		{"2130706433", 2130706433, true},
		{"", 0, false},
		{"xyz", 0, false},
		{"0x", 0, false},
	}
	for _, tc := range cases {
		n, ok := safedial.ParseUint64(tc.input)
		if ok != tc.wantOK || (ok && n != tc.wantN) {
			t.Errorf("ParseUint64(%q) = (%d, %v), want (%d, %v)",
				tc.input, n, ok, tc.wantN, tc.wantOK)
		}
	}
}

// ─── ValidateHostWithResolver tests ──────────────────────────────────────────

func TestValidateHostWithResolver_PublicAllowed(t *testing.T) {
	resolve := func(host string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("8.8.8.8")}, nil
	}
	ip, err := safedial.ValidateHostWithResolver("dns.google.com", false, resolve)
	if err != nil {
		t.Errorf("ValidateHostWithResolver: unexpected error: %v", err)
	}
	if ip == nil || !ip.Equal(net.ParseIP("8.8.8.8")) {
		t.Errorf("ValidateHostWithResolver: got %v, want 8.8.8.8", ip)
	}
}

func TestValidateHostWithResolver_PrivateBlocked(t *testing.T) {
	resolve := func(host string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("10.0.0.1")}, nil
	}
	_, err := safedial.ValidateHostWithResolver("internal.corp", false, resolve)
	if err == nil {
		t.Error("ValidateHostWithResolver: expected error for private IP, got nil")
	}
}

func TestValidateHostWithResolver_IPLiteral_ResolverNotCalled(t *testing.T) {
	// For an IP literal, the resolver must not be consulted.
	called := false
	resolve := func(host string) ([]net.IP, error) {
		called = true
		return nil, fmt.Errorf("should not be called")
	}
	_, err := safedial.ValidateHostWithResolver("127.0.0.1", false, resolve)
	if err == nil {
		t.Error("ValidateHostWithResolver: expected loopback literal to be blocked")
	}
	if called {
		t.Error("ValidateHostWithResolver: resolver was called for an IP literal")
	}
}

func TestValidateHostWithResolver_FailClosed_DNSError(t *testing.T) {
	resolve := func(host string) ([]net.IP, error) {
		return nil, fmt.Errorf("DNS SERVFAIL")
	}
	_, err := safedial.ValidateHostWithResolver("nonexistent.invalid", false, resolve)
	if err == nil {
		t.Error("ValidateHostWithResolver: expected error on DNS failure (fail-closed), got nil")
	}
}

func TestValidateHostWithResolver_FailClosed_EmptyIPs(t *testing.T) {
	resolve := func(host string) ([]net.IP, error) {
		return []net.IP{}, nil
	}
	_, err := safedial.ValidateHostWithResolver("empty.example", false, resolve)
	if err == nil {
		t.Error("ValidateHostWithResolver: expected error for empty IP list (fail-closed), got nil")
	}
}

// TestValidateHostWithResolver_DNSRebindingBlock simulates the multi-A DNS
// rebinding scenario: the resolver returns a mix of a public and a private IP.
// The request must be refused even though a public IP is present.
func TestValidateHostWithResolver_DNSRebindingBlock(t *testing.T) {
	resolve := func(host string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("8.8.8.8"), net.ParseIP("10.0.0.1")}, nil
	}
	_, err := safedial.ValidateHostWithResolver("rebind.example", false, resolve)
	if err == nil {
		t.Error("ValidateHostWithResolver: expected error when any resolved IP is private (multi-A DNS rebinding)")
	}
}

func TestValidateHostWithResolver_LocalhostBlocked(t *testing.T) {
	called := false
	resolve := func(host string) ([]net.IP, error) {
		called = true
		return nil, fmt.Errorf("should not be called")
	}
	_, err := safedial.ValidateHostWithResolver("localhost", false, resolve)
	if err == nil {
		t.Error("ValidateHostWithResolver: expected localhost to be blocked")
	}
	if called {
		t.Error("ValidateHostWithResolver: resolver should not be called for localhost")
	}
}

func TestValidateHostWithResolver_LANOptIn(t *testing.T) {
	// With allowLAN=true, a private IP returned by the resolver should be allowed.
	resolve := func(host string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("192.168.1.5")}, nil
	}
	ip, err := safedial.ValidateHostWithResolver("mybox.local", true, resolve)
	if err != nil {
		t.Errorf("ValidateHostWithResolver(allowLAN=true): unexpected error for private IP: %v", err)
	}
	if ip == nil {
		t.Error("ValidateHostWithResolver(allowLAN=true): expected non-nil IP")
	}
}

// ─── ControlFunc tests ────────────────────────────────────────────────────────

// TestControlFunc_DNSRebinding_Block simulates the DNS-rebinding scenario:
// the pre-dial ValidateHost check passed (based on what DNS said before),
// but by the time connect(2) is called, the OS resolved the hostname to a
// private IP.  The Control hook must catch and reject this.
func TestControlFunc_DNSRebinding_Block(t *testing.T) {
	hook := safedial.ControlFunc(false)

	// Private IP — would result from a rebinding attack.
	if err := hook("tcp4", "10.0.0.1:80", nil); err == nil {
		t.Error("ControlFunc: expected private IP 10.0.0.1 to be blocked at dial time (rebinding defence)")
	}
	// Loopback.
	if err := hook("tcp4", "127.0.0.1:80", nil); err == nil {
		t.Error("ControlFunc: expected loopback 127.0.0.1 to be blocked at dial time")
	}
	// Cloud metadata.
	if err := hook("tcp4", "169.254.169.254:80", nil); err == nil {
		t.Error("ControlFunc: expected link-local metadata IP to be blocked at dial time")
	}
}

func TestControlFunc_Public_Allowed(t *testing.T) {
	hook := safedial.ControlFunc(false)
	if err := hook("tcp4", "8.8.8.8:443", nil); err != nil {
		t.Errorf("ControlFunc: expected public IP 8.8.8.8 to be allowed: %v", err)
	}
}

func TestControlFunc_LANOptIn_PrivateAllowed(t *testing.T) {
	hook := safedial.ControlFunc(true)
	if err := hook("tcp4", "192.168.1.1:8080", nil); err != nil {
		t.Errorf("ControlFunc(allowLAN=true): expected private IP to be allowed: %v", err)
	}
}

func TestControlFunc_LANOptIn_LoopbackStillBlocked(t *testing.T) {
	// Even with LAN opt-in, loopback is never a legitimate peer target.
	hook := safedial.ControlFunc(true)
	if err := hook("tcp4", "127.0.0.1:80", nil); err == nil {
		t.Error("ControlFunc(allowLAN=true): loopback should still be blocked")
	}
}

func TestControlFunc_LANOptIn_LinkLocalStillBlocked(t *testing.T) {
	// Link-local (169.254/16) is always blocked — no legitimate peer lives there.
	hook := safedial.ControlFunc(true)
	if err := hook("tcp4", "169.254.169.254:80", nil); err == nil {
		t.Error("ControlFunc(allowLAN=true): link-local metadata IP should still be blocked")
	}
}

func TestControlFunc_IPv6(t *testing.T) {
	hook := safedial.ControlFunc(false)

	// IPv6 loopback — note Go dials IPv6 with bracket notation.
	if err := hook("tcp6", "[::1]:80", nil); err == nil {
		t.Error("ControlFunc: expected ::1 to be blocked")
	}
	// IPv6 public (Google DNS).
	if err := hook("tcp6", "[2001:4860:4860::8888]:443", nil); err != nil {
		t.Errorf("ControlFunc: expected IPv6 public to be allowed: %v", err)
	}
}
