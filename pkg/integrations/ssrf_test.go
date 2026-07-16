package integrations

import "testing"

// TestValidateExternalHost_BlocksInternal verifies the SSRF deny-list rejects
// loopback, RFC1918, and the cloud metadata endpoint while allowing public IPs
// (audit M4).
func TestValidateExternalHost_BlocksInternal(t *testing.T) {
	blocked := []string{
		"127.0.0.1",
		"169.254.169.254", // cloud metadata
		"10.0.0.5",
		"192.168.1.1",
		"172.16.0.1",
		"::1",
		"0.0.0.0",
		"fc00::1", // IPv6 unique-local
	}
	for _, h := range blocked {
		if err := validateExternalHost(h); err == nil {
			t.Errorf("validateExternalHost(%q) = nil, want blocked", h)
		}
	}

	allowed := []string{
		"8.8.8.8",
		"1.1.1.1",
		"203.0.113.10", // TEST-NET-3 (public, routable)
	}
	for _, h := range allowed {
		if err := validateExternalHost(h); err != nil {
			t.Errorf("validateExternalHost(%q) = %v, want allowed", h, err)
		}
	}
}

// TestValidateExternalURL verifies URL host extraction + scheme checks.
func TestValidateExternalURL(t *testing.T) {
	if err := validateExternalURL("http://169.254.169.254/latest/meta-data/"); err == nil {
		t.Error("metadata URL must be blocked")
	}
	if err := validateExternalURL("https://10.1.2.3/dav"); err == nil {
		t.Error("private-IP URL must be blocked")
	}
	if err := validateExternalURL(""); err != nil {
		t.Errorf("empty URL should be allowed (omitted), got %v", err)
	}
	if err := validateExternalURL("ftp://example.com"); err == nil {
		t.Error("non-http(s) scheme must be rejected")
	}
	if err := validateExternalURL("https://8.8.8.8/dav"); err != nil {
		t.Errorf("public-IP https URL should be allowed, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Audit security regression tests
// ---------------------------------------------------------------------------

// TestSSRF_L1_CGNAT_Blocked verifies L1 (audit): the CGNAT block 100.64.0.0/10
// is in the SSRF deny-list and rejected by validateExternalHost.
func TestSSRF_L1_CGNAT_Blocked(t *testing.T) {
	cgnatHosts := []string{
		"100.64.0.1",
		"100.64.128.0",
		"100.127.255.255",
	}
	for _, h := range cgnatHosts {
		if err := validateExternalHost(h); err == nil {
			t.Errorf("SEC-L1: CGNAT address %q must be blocked, got nil", h)
		}
	}

	// 100.63.255.255 is JUST outside the CGNAT range — must be allowed.
	if err := validateExternalHost("100.63.255.255"); err != nil {
		t.Errorf("100.63.255.255 is outside CGNAT and must be allowed, got %v", err)
	}
}

// TestSSRF_H5_IPLiteralBlocked verifies H5 (audit): ssrfSafeTCPDial rejects
// IP literals in the SSRF deny-list without making a DNS lookup. This prevents
// the DNS-rebind TOCTOU window for direct-IP dial attempts.
func TestSSRF_H5_IPLiteralBlocked(t *testing.T) {
	t.Parallel()
	// These must fail at the IP-validation stage, not reach the network.
	blocked := []string{
		"127.0.0.1",       // loopback
		"10.0.0.1",        // RFC1918
		"192.168.0.1",     // RFC1918
		"172.16.0.1",      // RFC1918
		"169.254.169.254", // link-local / metadata
		"100.64.0.1",      // CGNAT (L1 audit)
		"fc00::1",         // ULA IPv6
	}
	for _, ip := range blocked {
		_, _, err := ssrfSafeTCPDial(nil, ip, 993, 0) //nolint:staticcheck
		if err == nil {
			t.Errorf("SEC-H5: ssrfSafeTCPDial(%q) must be blocked, got nil", ip)
		}
	}
}
