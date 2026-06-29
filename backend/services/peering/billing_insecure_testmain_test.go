package peering

import (
	"os"
	"testing"
)

// TestMain opts this package's tests into the plaintext-http escape hatch: the
// cp billing tests here point a cpbilling.Client at an http httptest stub, which
// cpbilling.New otherwise disables (fail-closed) to avoid leaking
// CP_SHARED_SECRET over plaintext. Dev/test-only, mirroring the lan tests'
// VULOS_LANCERT_ALLOW_INSECURE usage.
//
// wkAllowLAN=true lets FetchPeerProfile / wellknown tests connect to httptest
// servers on 127.0.0.1 without being rejected by the H1 SSRF guard. This flag
// is only present in package peering and has no effect outside tests.
func TestMain(m *testing.M) {
	os.Setenv("VULOS_CP_ALLOW_INSECURE", "1")
	// H1 SSRF guard test hooks: skip SSRF validation in tests so httptest.Server
	// on 127.0.0.1 and fake hostnames like "home.alice" are accepted.
	// These vars are unexported and can only be set within package peering.
	peeringSSRFBypass = true
	os.Exit(m.Run())
}
