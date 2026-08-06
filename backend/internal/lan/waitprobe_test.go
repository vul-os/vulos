package lan

import (
	"net"
	"testing"
	"time"
)

// Proves detectLANIPWaiting actually waits rather than returning loopback
// immediately, and that it gives up rather than blocking forever.
func TestDetectLANIPWaiting_ReturnsRealAddressWhenPresent(t *testing.T) {
	start := time.Now()
	ip := detectLANIPWaiting()
	elapsed := time.Since(start)

	if ip == nil {
		t.Fatal("returned nil")
	}
	// This machine has a LAN address, so it must return promptly and non-loopback.
	if ip.IsLoopback() {
		t.Fatalf("got loopback %v on a host that has a LAN address — the wait "+
			"returned the fallback instead of the real address", ip)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("took %v to return an address that was already present; the "+
			"poll loop is not short-circuiting", elapsed)
	}
	if ip.To4() == nil {
		t.Fatalf("got non-IPv4 %v", ip)
	}
	var _ net.IP = ip
}

// The address arrives only on the 3rd poll. A wait that accepts loopback (the
// pre-fix behaviour) returns 127.0.0.1 here and fails.
func TestDetectLANIPWaiting_WaitsForALateAddress(t *testing.T) {
	orig := detectLANIPFn
	t.Cleanup(func() { detectLANIPFn = orig })

	calls := 0
	late := net.IPv4(192, 168, 42, 7)
	detectLANIPFn = func() net.IP {
		calls++
		if calls < 3 {
			return net.IPv4(127, 0, 0, 1)
		}
		return late
	}

	got := detectLANIPWaiting()
	if got.IsLoopback() {
		t.Fatalf("returned loopback %v instead of waiting for the real address — "+
			"a box whose DHCP lease lands late would bind loopback permanently", got)
	}
	if !got.Equal(late) {
		t.Fatalf("got %v, want %v", got, late)
	}
	if calls < 3 {
		t.Fatalf("polled only %d times; it did not actually retry", calls)
	}
}
