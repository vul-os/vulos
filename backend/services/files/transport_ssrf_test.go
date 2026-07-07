package files

// transport_ssrf_test.go — SSRF-FILES-01 regression tests for validateOwnerAddr.
//
// These tests verify that the SSRF deny-list applied to peer-share capability
// OwnerAddr values correctly blocks metadata/private/loopback addresses while
// allowing public addresses and (when VULOS_PEER_ALLOW_LAN=1) LAN addresses.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
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

// ── Dial-time guard: the client itself must fail closed ───────────────────────
//
// The tests above only exercise validateOwnerAddr as a STRING parser. The real
// SSRF hole (sec/os-deep) was that the pre-dial check resolves the host once and
// throws the result away, while the actual fetch client re-resolves at dial time
// and follows redirects — so a DNS name that is public on the pre-check but
// resolves to an internal IP at dial time (rebinding), or an owner that answers
// with a 3xx to an internal target, bypassed the guard entirely. These tests
// drive an ACTUAL Fetch through the production client to prove both vectors are
// now closed on the client (dial-time IP re-validation + no redirect following).

// TestPeerFetch_DialTimeGuardBlocksInternalIP is the rebinding regression: even
// when the pre-dial validator is BYPASSED (simulating a name that looked public
// to validateOwnerAddr but resolves to an internal/loopback IP at connect time),
// the safedial dial-time Control hook on the production client must refuse the
// connection. We point Fetch at a real loopback httptest server and assert it
// (a) errors and (b) NEVER reaches the server's handler.
//
// Pre-fix (bare &http.Client{}) this dialled loopback fine, the handler ran, and
// Fetch returned the internal response — a read-SSRF. Post-fix it fails closed.
func TestPeerFetch_DialTimeGuardBlocksInternalIP(t *testing.T) {
	resetPeerAllowLAN(t, false) // loopback is denied regardless, but be explicit

	var hit int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.StoreInt32(&hit, 1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("INTERNAL SECRET"))
	}))
	defer srv.Close()

	tr := NewHTTPPeerTransport()
	// Simulate the TOCTOU/rebinding: the pre-dial address validator "passed"
	// (as if it had resolved a public IP a moment earlier). The dial-time guard
	// must still block the real connection to the loopback server.
	tr.addrValidator = func(string) error { return nil }

	rc, _, err := tr.Fetch(context.Background(), srv.URL, PeerFetchRequest{})
	if rc != nil {
		rc.Close()
	}
	if err == nil {
		t.Fatal("SSRF-FILES-01 REGRESSION: Fetch to an internal (loopback) IP succeeded — dial-time guard missing; the client must fail closed even when the pre-dial check is bypassed (DNS rebinding)")
	}
	if atomic.LoadInt32(&hit) != 0 {
		t.Fatal("SSRF-FILES-01 REGRESSION: the internal server handler was reached — the connection to a denied IP was not blocked at dial time")
	}
}

// TestPeerFetch_DoesNotFollowRedirects is the redirect-SSRF regression: a
// malicious owner box must not be able to bounce the fetch to another address
// via an HTTP 3xx. The production client's CheckRedirect must refuse to follow,
// so a redirect surfaces as a non-200 owner response (Fetch errors) rather than
// being chased to an unvalidated target.
func TestPeerFetch_DoesNotFollowRedirects(t *testing.T) {
	tr := NewHTTPPeerTransport()
	if tr.http.CheckRedirect == nil {
		t.Fatal("SSRF-FILES-01 REGRESSION: peer fetch client has no CheckRedirect — redirects to internal targets would be followed unvalidated")
	}
	if err := tr.http.CheckRedirect(nil, nil); err != http.ErrUseLastResponse {
		t.Fatalf("SSRF-FILES-01 REGRESSION: peer fetch client follows redirects (CheckRedirect=%v); it must return ErrUseLastResponse so a 3xx from a malicious owner is not chased to an unvalidated address", err)
	}
}
