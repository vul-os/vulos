package superadmin

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestRemoteIP_IgnoresSpoofedFlyHeaderWithoutTrust proves the H1 fix: without an
// explicit VULOS_EDGE_TRUST_HEADER, a client-supplied Fly-Client-IP (or
// X-Forwarded-For) is NOT trusted, so an attacker cannot forge an allowlisted IP.
// The un-forgeable TCP peer address is used instead.
func TestRemoteIP_IgnoresSpoofedFlyHeaderWithoutTrust(t *testing.T) {
	t.Setenv("VULOS_EDGE_TRUST_HEADER", "")
	t.Setenv("VULOS_EDGE_TRUST_CIDR", "")

	r := httptest.NewRequest(http.MethodGet, "/superadmin/", nil)
	r.RemoteAddr = "203.0.113.7:4444"
	r.Header.Set("Fly-Client-IP", "10.0.0.1")       // spoofed "allowlisted" IP
	r.Header.Set("X-Forwarded-For", "192.168.1.250") // spoofed

	if got := remoteIP(r); got != "203.0.113.7" {
		t.Fatalf("remoteIP = %q, want the peer 203.0.113.7 (spoofed headers must be ignored)", got)
	}
}

// TestRemoteIP_HonoursTrustedHeaderWhenConfigured proves the header IS honoured
// once the deploy opts in via VULOS_EDGE_TRUST_HEADER (the Fly edge case).
func TestRemoteIP_HonoursTrustedHeaderWhenConfigured(t *testing.T) {
	t.Setenv("VULOS_EDGE_TRUST_HEADER", "Fly-Client-IP")
	t.Setenv("VULOS_EDGE_TRUST_CIDR", "")

	r := httptest.NewRequest(http.MethodGet, "/superadmin/", nil)
	r.RemoteAddr = "203.0.113.7:4444"
	r.Header.Set("Fly-Client-IP", "198.51.100.22")

	if got := remoteIP(r); got != "198.51.100.22" {
		t.Fatalf("remoteIP = %q, want the trusted-edge value 198.51.100.22", got)
	}
}
