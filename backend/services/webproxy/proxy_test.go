package webproxy

import (
	"fmt"
	"net"
	"testing"
)

// ---------------------------------------------------------------------------
// isDeniedIP tests
// ---------------------------------------------------------------------------

func TestIsDeniedIP_Loopback(t *testing.T) {
	cases := []string{"127.0.0.1", "127.255.255.255", "::1"}
	for _, c := range cases {
		ip := net.ParseIP(c)
		if ip == nil {
			t.Fatalf("bad test IP: %s", c)
		}
		if !isDeniedIP(ip) {
			t.Errorf("expected %s to be denied (loopback)", c)
		}
	}
}

func TestIsDeniedIP_Private(t *testing.T) {
	cases := []string{
		"10.0.0.1", "10.255.255.255",
		"172.16.0.1", "172.31.255.255",
		"192.168.0.1", "192.168.255.255",
	}
	for _, c := range cases {
		ip := net.ParseIP(c)
		if ip == nil {
			t.Fatalf("bad test IP: %s", c)
		}
		if !isDeniedIP(ip) {
			t.Errorf("expected %s to be denied (private)", c)
		}
	}
}

func TestIsDeniedIP_LinkLocal(t *testing.T) {
	cases := []string{"169.254.0.1", "169.254.255.255", "fe80::1"}
	for _, c := range cases {
		ip := net.ParseIP(c)
		if ip == nil {
			t.Fatalf("bad test IP: %s", c)
		}
		if !isDeniedIP(ip) {
			t.Errorf("expected %s to be denied (link-local)", c)
		}
	}
}

func TestIsDeniedIP_Multicast(t *testing.T) {
	cases := []string{"224.0.0.1", "239.255.255.255", "ff02::1"}
	for _, c := range cases {
		ip := net.ParseIP(c)
		if ip == nil {
			t.Fatalf("bad test IP: %s", c)
		}
		if !isDeniedIP(ip) {
			t.Errorf("expected %s to be denied (multicast)", c)
		}
	}
}

func TestIsDeniedIP_CGNAT(t *testing.T) {
	cases := []string{"100.64.0.1", "100.127.255.255"}
	for _, c := range cases {
		ip := net.ParseIP(c)
		if ip == nil {
			t.Fatalf("bad test IP: %s", c)
		}
		if !isDeniedIP(ip) {
			t.Errorf("expected %s to be denied (CGNAT)", c)
		}
	}
}

func TestIsDeniedIP_Reserved240(t *testing.T) {
	cases := []string{"240.0.0.1", "255.255.255.254", "255.255.255.255"}
	for _, c := range cases {
		ip := net.ParseIP(c)
		if ip == nil {
			t.Fatalf("bad test IP: %s", c)
		}
		if !isDeniedIP(ip) {
			t.Errorf("expected %s to be denied (reserved)", c)
		}
	}
}

func TestIsDeniedIP_IPv4MappedIPv6(t *testing.T) {
	// ::ffff:127.0.0.1 should be treated as 127.0.0.1 (loopback)
	ip := net.ParseIP("::ffff:127.0.0.1")
	if ip == nil {
		t.Fatal("could not parse ::ffff:127.0.0.1")
	}
	if !isDeniedIP(ip) {
		t.Error("expected ::ffff:127.0.0.1 to be denied (IPv4-mapped loopback)")
	}

	// ::ffff:10.0.0.1 should be treated as 10.0.0.1 (private)
	ip2 := net.ParseIP("::ffff:10.0.0.1")
	if ip2 == nil {
		t.Fatal("could not parse ::ffff:10.0.0.1")
	}
	if !isDeniedIP(ip2) {
		t.Error("expected ::ffff:10.0.0.1 to be denied (IPv4-mapped private)")
	}
}

func TestIsDeniedIP_ULAIPv6(t *testing.T) {
	cases := []string{"fc00::1", "fd00::1", "fdff:ffff:ffff:ffff::1"}
	for _, c := range cases {
		ip := net.ParseIP(c)
		if ip == nil {
			t.Fatalf("bad test IP: %s", c)
		}
		if !isDeniedIP(ip) {
			t.Errorf("expected %s to be denied (ULA)", c)
		}
	}
}

func TestIsDeniedIP_PublicAllowed(t *testing.T) {
	cases := []string{"8.8.8.8", "1.1.1.1", "2606:4700:4700::1111"}
	for _, c := range cases {
		ip := net.ParseIP(c)
		if ip == nil {
			t.Fatalf("bad test IP: %s", c)
		}
		if isDeniedIP(ip) {
			t.Errorf("expected %s to be allowed (public)", c)
		}
	}
}

// ---------------------------------------------------------------------------
// normalizeIPLiteral / parseAltIPv4 tests
// ---------------------------------------------------------------------------

func TestNormalizeIPLiteral_Standard(t *testing.T) {
	cases := []struct {
		input    string
		wantNil  bool
		wantDeny bool
	}{
		{"127.0.0.1", false, true},
		{"::1", false, true},
		{"[::1]", false, true},
		{"8.8.8.8", false, false},
		{"notanip", true, false},
	}
	for _, tc := range cases {
		ip := normalizeIPLiteral(tc.input)
		if tc.wantNil {
			if ip != nil {
				t.Errorf("normalizeIPLiteral(%q) = %v, want nil", tc.input, ip)
			}
			continue
		}
		if ip == nil {
			t.Errorf("normalizeIPLiteral(%q) = nil, want non-nil", tc.input)
			continue
		}
		if got := isDeniedIP(ip); got != tc.wantDeny {
			t.Errorf("normalizeIPLiteral(%q) denied=%v, want %v", tc.input, got, tc.wantDeny)
		}
	}
}

func TestParseAltIPv4_DecimalInteger(t *testing.T) {
	// 2130706433 == 0x7F000001 == 127.0.0.1
	ip := parseAltIPv4("2130706433")
	if ip == nil {
		t.Fatal("parseAltIPv4(2130706433) returned nil")
	}
	if !ip.Equal(net.ParseIP("127.0.0.1")) {
		t.Errorf("parseAltIPv4(2130706433) = %s, want 127.0.0.1", ip)
	}
	if !isDeniedIP(ip) {
		t.Error("decimal loopback should be denied")
	}
}

func TestParseAltIPv4_HexInteger(t *testing.T) {
	// 0x7f000001 == 127.0.0.1
	ip := parseAltIPv4("0x7f000001")
	if ip == nil {
		t.Fatal("parseAltIPv4(0x7f000001) returned nil")
	}
	if !ip.Equal(net.ParseIP("127.0.0.1")) {
		t.Errorf("parseAltIPv4(0x7f000001) = %s, want 127.0.0.1", ip)
	}
	if !isDeniedIP(ip) {
		t.Error("hex loopback should be denied")
	}
}

func TestParseAltIPv4_OctalOctets(t *testing.T) {
	// 0177.0.0.1 == 127.0.0.1
	ip := parseAltIPv4("0177.0.0.1")
	if ip == nil {
		t.Fatal("parseAltIPv4(0177.0.0.1) returned nil")
	}
	if !ip.Equal(net.ParseIP("127.0.0.1")) {
		t.Errorf("parseAltIPv4(0177.0.0.1) = %s, want 127.0.0.1", ip)
	}
	if !isDeniedIP(ip) {
		t.Error("octal loopback should be denied")
	}
}

func TestParseAltIPv4_TwoPart(t *testing.T) {
	// 0x0a.0x000001 — two-part form for 10.0.0.1
	ip := parseAltIPv4("0x0a.0x000001")
	if ip == nil {
		t.Fatal("parseAltIPv4(0x0a.0x000001) returned nil")
	}
	if !ip.Equal(net.ParseIP("10.0.0.1")) {
		t.Errorf("parseAltIPv4(0x0a.0x000001) = %s, want 10.0.0.1", ip)
	}
	if !isDeniedIP(ip) {
		t.Error("two-part private should be denied")
	}
}

func TestParseAltIPv4_ThreePart(t *testing.T) {
	// 192.168.0x0100 — three-part form where last part encodes two octets
	// 0x0100 = 256 which is > 255, so this should return nil (invalid)
	ip := parseAltIPv4("192.168.0x0100")
	// 0x0100 == 256, which fits in 16 bits; 192.168 + 0x0100 == 192.168.1.0
	if ip != nil {
		if !ip.Equal(net.ParseIP("192.168.1.0")) {
			t.Errorf("parseAltIPv4(192.168.0x0100) = %s, want 192.168.1.0", ip)
		}
		if !isDeniedIP(ip) {
			t.Error("three-part private should be denied")
		}
	}
}

func TestParseAltIPv4_NotAnIP(t *testing.T) {
	cases := []string{"example.com", "abc", "999.0.0.1", ""}
	for _, c := range cases {
		ip := parseAltIPv4(c)
		if ip != nil {
			t.Errorf("parseAltIPv4(%q) = %v, want nil", c, ip)
		}
	}
}

// ---------------------------------------------------------------------------
// resolveAndValidate tests (injectable resolver)
// ---------------------------------------------------------------------------

func makeResolver(ips []net.IP, err error) func(string) ([]net.IP, error) {
	return func(host string) ([]net.IP, error) {
		return ips, err
	}
}

func TestResolveAndValidate_PrivateResolution(t *testing.T) {
	svc := &Service{resolveHost: makeResolver([]net.IP{net.ParseIP("10.0.0.1")}, nil)}
	_, err := svc.resolveAndValidate("internal.example.com")
	if err == nil {
		t.Error("expected error for private IP resolution, got nil")
	}
}

func TestResolveAndValidate_PublicResolution(t *testing.T) {
	svc := &Service{resolveHost: makeResolver([]net.IP{net.ParseIP("8.8.8.8")}, nil)}
	ip, err := svc.resolveAndValidate("dns.google.com")
	if err != nil {
		t.Errorf("expected no error for public IP, got: %v", err)
	}
	if !ip.Equal(net.ParseIP("8.8.8.8")) {
		t.Errorf("expected 8.8.8.8, got %s", ip)
	}
}

func TestResolveAndValidate_FailClosed_ResolutionError(t *testing.T) {
	svc := &Service{resolveHost: makeResolver(nil, fmt.Errorf("dns failure"))}
	_, err := svc.resolveAndValidate("unresolvable.example.com")
	if err == nil {
		t.Error("expected error on DNS failure (fail-closed), got nil")
	}
}

func TestResolveAndValidate_FailClosed_EmptyIPs(t *testing.T) {
	svc := &Service{resolveHost: makeResolver([]net.IP{}, nil)}
	_, err := svc.resolveAndValidate("empty.example.com")
	if err == nil {
		t.Error("expected error for empty IP list (fail-closed), got nil")
	}
}

func TestResolveAndValidate_AnyPrivateIPBlocked(t *testing.T) {
	// Even if there is one public IP, if any resolved IP is private, refuse
	ips := []net.IP{net.ParseIP("8.8.8.8"), net.ParseIP("192.168.1.1")}
	svc := &Service{resolveHost: makeResolver(ips, nil)}
	_, err := svc.resolveAndValidate("mixed.example.com")
	if err == nil {
		t.Error("expected error when any resolved IP is private, got nil")
	}
}

func TestResolveAndValidate_LocalhostBlocked(t *testing.T) {
	// Localhost should be blocked by quick-check before DNS
	svc := &Service{resolveHost: func(host string) ([]net.IP, error) {
		t.Error("resolver should not be called for localhost")
		return nil, nil
	}}
	_, err := svc.resolveAndValidate("localhost")
	if err == nil {
		t.Error("expected localhost to be blocked")
	}
}

func TestResolveAndValidate_IPLiteral_Loopback(t *testing.T) {
	// IP literals should be blocked without DNS
	svc := &Service{resolveHost: func(host string) ([]net.IP, error) {
		t.Error("resolver should not be called for IP literals")
		return nil, nil
	}}
	cases := []string{"127.0.0.1", "::1", "[::1]"}
	for _, c := range cases {
		_, err := svc.resolveAndValidate(c)
		if err == nil {
			t.Errorf("expected %q to be blocked as IP literal", c)
		}
	}
}

func TestResolveAndValidate_DecimalIPLiteral(t *testing.T) {
	// Decimal-encoded 127.0.0.1 = 2130706433
	svc := &Service{resolveHost: func(host string) ([]net.IP, error) {
		t.Error("resolver should not be called for IP literals")
		return nil, nil
	}}
	_, err := svc.resolveAndValidate("2130706433")
	if err == nil {
		t.Error("expected decimal loopback (2130706433) to be blocked")
	}
}

func TestResolveAndValidate_HexIPLiteral(t *testing.T) {
	svc := &Service{resolveHost: func(host string) ([]net.IP, error) {
		t.Error("resolver should not be called for IP literals")
		return nil, nil
	}}
	_, err := svc.resolveAndValidate("0x7f000001")
	if err == nil {
		t.Error("expected hex loopback (0x7f000001) to be blocked")
	}
}

func TestResolveAndValidate_OctalIPLiteral(t *testing.T) {
	svc := &Service{resolveHost: func(host string) ([]net.IP, error) {
		t.Error("resolver should not be called for IP literals")
		return nil, nil
	}}
	_, err := svc.resolveAndValidate("0177.0.0.1")
	if err == nil {
		t.Error("expected octal loopback (0177.0.0.1) to be blocked")
	}
}

// ---------------------------------------------------------------------------
// parseUint64 tests
// ---------------------------------------------------------------------------

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
		n, ok := parseUint64(tc.input)
		if ok != tc.wantOK || (ok && n != tc.wantN) {
			t.Errorf("parseUint64(%q) = (%d, %v), want (%d, %v)", tc.input, n, ok, tc.wantN, tc.wantOK)
		}
	}
}
