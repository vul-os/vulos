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
func TestMain(m *testing.M) {
	os.Setenv("VULOS_CP_ALLOW_INSECURE", "1")
	os.Exit(m.Run())
}
