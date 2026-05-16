package webproxy

// proxy_security_test.go — SECAUDIT2 / SEC-E adversarial regressions.
//
// The SEC-E fix is "resolve once, validate ALL returned IPs, pin the dial to
// the validated IP" — closing the DNS-rebinding TOCTOU. These tests drive the
// real HTTP Handler (not just resolveAndValidate) so a regression in the
// wiring (e.g. someone reverting to client.Do with a fresh DNS lookup) is
// caught, not only a regression in the resolver helper.

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestHandler_BlocksLoopbackResolvedHost: a hostname that resolves to a
// loopback IP must be refused by the Handler with 403, never proxied.
func TestHandler_BlocksLoopbackResolvedHost(t *testing.T) {
	svc := &Service{resolveHost: makeResolver([]net.IP{net.ParseIP("127.0.0.1")}, nil)}
	h := svc.Handler()

	req := httptest.NewRequest(http.MethodGet, "/api/proxy/http://attacker-controlled.example/secret", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("SEC-E REGRESSION: host resolving to 127.0.0.1 was not "+
			"blocked at the Handler (status %d, want 403). SSRF to loopback "+
			"is possible.", rr.Code)
	}
}

// TestHandler_BlocksPrivateResolvedHost: rebinding-style host that resolves
// to RFC1918 space must be refused.
func TestHandler_BlocksPrivateResolvedHost(t *testing.T) {
	svc := &Service{resolveHost: makeResolver([]net.IP{net.ParseIP("169.254.169.254")}, nil)}
	h := svc.Handler()

	// 169.254.169.254 = cloud metadata endpoint (link-local) — classic SSRF.
	req := httptest.NewRequest(http.MethodGet, "/api/proxy/?url=http://metadata.example/latest/meta-data/", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("SEC-E REGRESSION: host resolving to the link-local cloud "+
			"metadata IP 169.254.169.254 was not blocked (status %d, want "+
			"403).", rr.Code)
	}
}

// TestHandler_BlocksMixedPublicPrivateResolution: if ANY resolved IP is
// private the request must be refused even though a public IP is also
// present (the core SEC-E "validate all IPs" property at Handler level).
func TestHandler_BlocksMixedPublicPrivateResolution(t *testing.T) {
	svc := &Service{resolveHost: makeResolver(
		[]net.IP{net.ParseIP("8.8.8.8"), net.ParseIP("10.1.2.3")}, nil)}
	h := svc.Handler()

	req := httptest.NewRequest(http.MethodGet, "/api/proxy/http://dual.example/", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("SEC-E REGRESSION: a host whose DNS answer mixed a public "+
			"and a private IP was proxied (status %d, want 403). DNS "+
			"rebinding is exploitable.", rr.Code)
	}
}

// TestHandler_BlocksDecimalIPLiteralLoopback: obfuscated IP literal for
// loopback (2130706433 == 127.0.0.1) must be blocked before any dial.
func TestHandler_BlocksDecimalIPLiteralLoopback(t *testing.T) {
	svc := &Service{resolveHost: func(string) ([]net.IP, error) {
		t.Fatalf("resolver must not be consulted for an IP literal")
		return nil, nil
	}}
	h := svc.Handler()

	// Use the ?url= form so httptest path normalisation cannot mangle the
	// double slash; this exercises the same resolveAndValidate path.
	req := httptest.NewRequest(http.MethodGet, "/api/proxy/?url=http://2130706433/", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("SEC-E REGRESSION: decimal IP literal 2130706433 "+
			"(=127.0.0.1) was not blocked (status %d, want 403).", rr.Code)
	}
}
