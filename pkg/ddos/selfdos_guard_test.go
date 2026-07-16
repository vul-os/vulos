package ddos

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

// These tests guard against the self-DoS outage where, with the edge trust
// header unset, RealClientIP fell back to the shared Fly proxy IP for every
// request; honeypot scanners then auto-blocked that shared internal IP, 403'ing
// the entire control plane.

// (item 1a) AutoBlock must refuse to add a private/shared/unroutable IP.
func TestAutoBlock_RefusesPrivateAndUnroutableIPs(t *testing.T) {
	s := openTestBlocklist(t)
	ctx := context.Background()

	unblockable := []string{
		"172.16.40.10", // the actual Fly shared proxy IP from the outage (RFC1918)
		"10.0.0.1",     // RFC1918
		"192.168.1.1",  // RFC1918
		"127.0.0.1",    // loopback
		"::1",          // loopback v6
		"169.254.1.1",  // link-local
		"100.64.0.1",   // CGNAT / RFC6598
		"0.0.0.0",      // unspecified
		"",             // empty / unresolvable
		"not-an-ip",    // garbage
		"10.0.0.0/8",   // private CIDR form
	}
	for _, ip := range unblockable {
		if !IsUnblockableIP(ip) {
			t.Errorf("IsUnblockableIP(%q) = false, want true", ip)
		}
		s.AutoBlock(ctx, ip, "test")
		if ip != "" && s.IsBlocked(ip) {
			t.Errorf("AutoBlock(%q) blocked a shared/private IP — self-DoS", ip)
		}
	}

	// A genuine public client IP must still be blockable.
	s.AutoBlock(ctx, "203.0.113.7", "test")
	if IsUnblockableIP("203.0.113.7") {
		t.Fatal("public IP wrongly classified unblockable")
	}
	if !s.IsBlocked("203.0.113.7") {
		t.Fatal("AutoBlock failed to block a real public IP")
	}
}

// (item 1b) The honeypot must not auto-block a private/shared upstream even when
// its handler fires (scanner hitting /.env behind a proxy with no trust header).
func TestHoneypot_DoesNotBlockPrivateUpstream(t *testing.T) {
	os.Unsetenv(envTrustHeader)
	os.Unsetenv(envTrustCIDR)

	bl := openTestBlocklist(t)

	mux := http.NewServeMux()
	RegisterHoneypotRoutes(mux, bl, nil) // nil audit logger is tolerated

	// Request arrives from the shared internal proxy IP with no trust header.
	// Use a cancelled context so the handler's 30s tarpit returns immediately;
	// the block decision runs before the tarpit, so this does not affect it.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := httptest.NewRequest("GET", "/.env", nil).WithContext(ctx)
	r.RemoteAddr = "172.16.40.10:41000"
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)

	if bl.IsBlocked("172.16.40.10") {
		t.Fatal("honeypot blocked the shared proxy IP — this is the outage")
	}
}

// (item 2) RealClientIP returns "" for a private/loopback RemoteAddr when no
// trust header is configured — degrading to "don't block" not "block everyone".
func TestRealClientIP_PrivateRemoteAddrNoHeaderIsNonMatchable(t *testing.T) {
	os.Unsetenv(envTrustHeader)
	os.Unsetenv(envTrustCIDR)

	for _, addr := range []string{"172.16.40.10:5000", "10.1.2.3:80", "127.0.0.1:9000", "[::1]:443", "100.64.0.5:80"} {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = addr
		if got := RealClientIP(r); got != "" {
			t.Errorf("RealClientIP(RemoteAddr=%s) = %q, want \"\" (non-matchable)", addr, got)
		}
	}

	// A public RemoteAddr must still resolve normally.
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "203.0.113.9:5000"
	if got := RealClientIP(r); got != "203.0.113.9" {
		t.Fatalf("public RemoteAddr: got %q want 203.0.113.9", got)
	}
}

// (item 4c) A request from a private RemoteAddr must not be 403'd by the
// blocklist middleware even if that shared IP is (stale) in the blocklist.
func TestBlocklistMiddleware_DoesNotBlockPrivateUpstream(t *testing.T) {
	os.Unsetenv(envTrustHeader)
	os.Unsetenv(envTrustCIDR)

	bl := openTestBlocklist(t)
	ctx := context.Background()
	// Simulate a stale block on the shared proxy IP (should never have happened,
	// but must not take down real traffic behind it).
	if err := bl.Add(ctx, "172.16.40.10", "stale", "system", nil); err != nil {
		t.Fatalf("Add: %v", err)
	}

	// Mirror the wire_ddos blocklist middleware decision.
	blocked := func(r *http.Request) bool { return bl.IsBlocked(RealClientIP(r)) }

	r := httptest.NewRequest("GET", "/api/health", nil)
	r.RemoteAddr = "172.16.40.10:33000"
	if blocked(r) {
		t.Fatal("request from shared proxy IP was 403'd — universal outage")
	}
}
