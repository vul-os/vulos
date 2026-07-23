// authclientip_test.go — H1 regression: the auth rate-limit IP resolver honours
// a forwarding header ONLY when the deploy opts in via VULOS_EDGE_TRUST_HEADER.
package cproutes

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func TestAuthClientIP_TrustDecision(t *testing.T) {
	newReq := func() *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.RemoteAddr = "203.0.113.9:5555"
		r.Header.Set("Fly-Client-IP", "10.9.9.9")
		r.Header.Set("X-Forwarded-For", "10.8.8.8")
		return r
	}

	// No trust header configured → a client-supplied header is ignored; the
	// un-forgeable peer address wins (so it cannot move the rate-limit bucket).
	os.Unsetenv("VULOS_EDGE_TRUST_HEADER")
	os.Unsetenv("VULOS_EDGE_TRUST_CIDR")
	if got := authClientIP(newReq()); got != "203.0.113.9" {
		t.Errorf("untrusted: authClientIP = %q, want the peer 203.0.113.9 (header ignored)", got)
	}

	// Trust header explicitly configured → the named header is honoured.
	t.Setenv("VULOS_EDGE_TRUST_HEADER", "Fly-Client-IP")
	if got := authClientIP(newReq()); got != "10.9.9.9" {
		t.Errorf("trusted: authClientIP = %q, want the trusted Fly-Client-IP 10.9.9.9", got)
	}
}
