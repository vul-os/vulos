package storage

import (
	"strings"
	"testing"
)

// TestNewBYOProvider_RejectsSSRFEndpoints proves the H2 fix: a Bring-Your-Own
// bucket whose endpoint host is a loopback/link-local/private/metadata address is
// refused up front (the SSRF guard is wired into the single BYO provider factory
// that both connect-time validation and the nightly sampler build through). IP
// literals are used so no DNS is required.
func TestNewBYOProvider_RejectsSSRFEndpoints(t *testing.T) {
	blocked := []string{
		"https://169.254.169.254", // cloud metadata
		"https://127.0.0.1:9000",  // loopback
		"https://10.0.0.5",        // RFC1918
		"https://192.168.1.10",    // RFC1918
		"https://172.16.0.9",      // RFC1918
		"https://[::1]",           // IPv6 loopback
		"https://[fd00::1]",       // IPv6 ULA
		"https://[fe80::1]",       // IPv6 link-local
		"https://0.0.0.0",         // unspecified
		"https://100.64.0.1",      // carrier-grade NAT
	}
	for _, ep := range blocked {
		_, err := NewBYOProvider(Config{
			AccessKey: "AK",
			SecretKey: "SK",
			Endpoint:  ep,
			Bucket:    "b",
			Region:    "auto",
			BYO:       true,
		})
		if err == nil {
			t.Errorf("NewBYOProvider(%q) = nil error, want rejection (SSRF)", ep)
			continue
		}
		// The client-facing error must be generic (no resolved-IP leak).
		if strings.Contains(err.Error(), "169.254") || strings.Contains(err.Error(), "127.0.0.1") {
			t.Errorf("NewBYOProvider(%q) error leaks the target address: %v", ep, err)
		}
	}
}

// TestNewBYOProvider_AllowsPublicEndpoint proves a public endpoint still builds a
// provider (the guard blocks only private/metadata ranges).
func TestNewBYOProvider_AllowsPublicEndpoint(t *testing.T) {
	p, err := NewBYOProvider(Config{
		AccessKey: "AK",
		SecretKey: "SK",
		Endpoint:  "https://8.8.8.8", // public IP literal — no DNS, passes the guard
		Bucket:    "b",
		Region:    "auto",
		BYO:       true,
	})
	if err != nil {
		t.Fatalf("NewBYOProvider(public) = %v, want success", err)
	}
	if p == nil {
		t.Fatal("NewBYOProvider(public) returned nil provider")
	}
}
