package ddos

import (
	"net/http/httptest"
	"os"
	"testing"
)

func TestRealClientIP_RemoteAddr(t *testing.T) {
	os.Unsetenv(envTrustHeader)
	os.Unsetenv(envTrustCIDR)

	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "1.2.3.4:55123"
	got := RealClientIP(r)
	if got != "1.2.3.4" {
		t.Fatalf("want 1.2.3.4 got %q", got)
	}
}

func TestRealClientIP_CFConnectingIP(t *testing.T) {
	os.Setenv(envTrustHeader, "CF-Connecting-IP")
	os.Unsetenv(envTrustCIDR)
	defer os.Unsetenv(envTrustHeader)

	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "10.0.0.1:443"
	r.Header.Set("CF-Connecting-IP", "5.6.7.8")
	got := RealClientIP(r)
	if got != "5.6.7.8" {
		t.Fatalf("want 5.6.7.8 got %q", got)
	}
}

func TestRealClientIP_CIDRGuard_UntrustedUpstream(t *testing.T) {
	os.Setenv(envTrustHeader, "CF-Connecting-IP")
	os.Setenv(envTrustCIDR, "192.168.0.0/16")
	defer os.Unsetenv(envTrustHeader)
	defer os.Unsetenv(envTrustCIDR)

	r := httptest.NewRequest("GET", "/", nil)
	// Upstream is NOT in 192.168.0.0/16 — header must be ignored.
	r.RemoteAddr = "1.2.3.4:443"
	r.Header.Set("CF-Connecting-IP", "9.9.9.9")
	got := RealClientIP(r)
	if got != "1.2.3.4" {
		t.Fatalf("untrusted upstream: want RemoteAddr IP 1.2.3.4 got %q", got)
	}
}
