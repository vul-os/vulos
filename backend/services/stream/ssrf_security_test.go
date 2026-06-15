package stream

// ssrf_security_test.go — SSRF regression tests for the VNC host validator.
//
// Covers VULN-08: LaunchVNC /api/stream/vnc host parameter must not resolve
// to loopback / private / metadata addresses.

import (
	"testing"
)

func TestIsBlockedVNCHost_Loopback(t *testing.T) {
	blocked := []string{
		"localhost",
		"127.0.0.1",
		"0.0.0.0",
		"::1",
		"127.0.0.2",
		"169.254.169.254", // AWS/GCP metadata
	}
	for _, h := range blocked {
		if !isBlockedVNCHost(h) {
			t.Errorf("isBlockedVNCHost(%q): expected true (blocked), got false", h)
		}
	}
}

func TestIsBlockedVNCHost_UnresolvableFails(t *testing.T) {
	// A non-existent hostname must fail closed (blocked) to prevent
	// speculative SSRF via DNS rebinding.
	unresolvable := "this-host-definitely-does-not-exist.invalid"
	if !isBlockedVNCHost(unresolvable) {
		t.Errorf("isBlockedVNCHost(%q): expected true (fail-closed), got false", unresolvable)
	}
}

func TestIsBlockedVNCHost_PrivateNetwork(t *testing.T) {
	// Private RFC-1918 ranges should be blocked.
	privateHosts := []string{
		"10.0.0.1",
		"172.16.0.1",
		"192.168.1.1",
	}
	for _, h := range privateHosts {
		if !isBlockedVNCHost(h) {
			t.Errorf("isBlockedVNCHost(%q): expected true (private), got false", h)
		}
	}
}
