package main

// share_resolver_ssrf_test.go — regression tests for the capability-delivery
// SSRF hardening (same class as SSRF-FILES-01).
//
// httpCapabilityDeliverer POSTs a minted peer-share capability to a
// directory-resolved server. That server value is attacker-influenceable, so
// the delivery client must fail closed against:
//   - DNS rebinding: a name that looks public to the pre-dial check but
//     resolves to an internal/loopback/metadata IP at connect time. The
//     safedial dial-time Control hook re-validates the ACTUAL resolved IP on
//     every connect, so the connection is refused even when the pre-check is
//     bypassed.
//   - HTTP redirects: a malicious intake bouncing the POST to an internal
//     target. CheckRedirect refuses to follow.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"vulos/backend/services/files"
)

// resetDeliverAllowLAN forces the VULOS_PEER_ALLOW_LAN state for a test.
func resetDeliverAllowLAN(t *testing.T, allow bool) {
	t.Helper()
	deliverAllowLANOnce = sync.Once{}
	deliverAllowLAN = allow
	deliverAllowLANOnce.Do(func() { deliverAllowLAN = allow })
}

// TestDeliverCapability_DialTimeGuardBlocksInternalIP is the rebinding
// regression: even when the pre-dial addrValidator is BYPASSED (simulating a
// name that looked public but resolves to loopback at connect), the safedial
// dial-time Control hook on the production client must refuse the connection and
// the internal handler must never run.
//
// Pre-fix (bare &http.Client{}) this POSTed to loopback fine — a write-SSRF.
func TestDeliverCapability_DialTimeGuardBlocksInternalIP(t *testing.T) {
	resetDeliverAllowLAN(t, false) // loopback is denied regardless, but be explicit

	var hit int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.StoreInt32(&hit, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	d := newHTTPCapabilityDeliverer()
	// Simulate the TOCTOU/rebinding: pre-dial validator "passed" (as if it had
	// resolved a public IP a moment earlier). The dial-time guard must still
	// block the real connection to the loopback server.
	d.addrValidator = func(string) error { return nil }

	err := d.DeliverCapability(context.Background(), srv.URL, files.CapabilityDelivery{
		RecipientVulaID: "vula1",
		Link:            "tok",
	})
	if err == nil {
		t.Fatal("SSRF REGRESSION: DeliverCapability to an internal (loopback) IP succeeded — dial-time guard missing; the client must fail closed even when the pre-dial check is bypassed (DNS rebinding)")
	}
	if atomic.LoadInt32(&hit) != 0 {
		t.Fatal("SSRF REGRESSION: the internal intake handler was reached — the connection to a denied IP was not blocked at dial time")
	}
}

// TestDeliverCapability_PreDialBlocksMetadataAndLoopback verifies the fast-fail
// pre-dial layer rejects metadata and loopback targets before any network I/O.
func TestDeliverCapability_PreDialBlocksMetadataAndLoopback(t *testing.T) {
	resetDeliverAllowLAN(t, false)
	d := newHTTPCapabilityDeliverer()

	cases := []string{
		"http://169.254.169.254", // cloud IMDS
		"http://127.0.0.1:9999",  // loopback
		"http://localhost:9999",  // loopback hostname
		"http://10.0.0.5",        // RFC1918 (no LAN opt-in)
		"http://0x7f000001",      // hex-encoded loopback
	}
	for _, target := range cases {
		err := d.DeliverCapability(context.Background(), target, files.CapabilityDelivery{})
		if err == nil {
			t.Errorf("SSRF REGRESSION: DeliverCapability to %q was not blocked by the pre-dial guard", target)
		}
	}
}

// TestDeliverCapability_MetadataBlockedEvenWithLAN verifies the metadata IP is
// blocked even with VULOS_PEER_ALLOW_LAN=1 (permanently-denied range).
func TestDeliverCapability_MetadataBlockedEvenWithLAN(t *testing.T) {
	resetDeliverAllowLAN(t, true)
	d := newHTTPCapabilityDeliverer()
	if err := d.DeliverCapability(context.Background(), "http://169.254.169.254/latest/meta-data/", files.CapabilityDelivery{}); err == nil {
		t.Fatal("SSRF REGRESSION: metadata IP 169.254.169.254 delivered even with VULOS_PEER_ALLOW_LAN=1")
	}
}

// TestDeliverCapability_DoesNotFollowRedirects is the redirect-SSRF regression:
// the production client must refuse to follow 3xx so a malicious intake cannot
// bounce the POST to an unvalidated internal target.
func TestDeliverCapability_DoesNotFollowRedirects(t *testing.T) {
	d := newHTTPCapabilityDeliverer()
	if d.client.CheckRedirect == nil {
		t.Fatal("SSRF REGRESSION: capability-delivery client has no CheckRedirect — redirects to internal targets would be followed unvalidated")
	}
	if err := d.client.CheckRedirect(nil, nil); err != http.ErrUseLastResponse {
		t.Fatalf("SSRF REGRESSION: capability-delivery client follows redirects (CheckRedirect=%v); it must return ErrUseLastResponse", err)
	}
}
