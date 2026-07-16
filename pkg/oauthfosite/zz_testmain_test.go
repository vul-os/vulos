package oauthfosite_test

import (
	"os"
	"testing"
)

// TestMain defaults VULOS_ENV=local for this test binary. The single prod
// resolver (env.IsProdResolved) fails safe to prod when VULOS_ENV is unset,
// which would refuse the dev-KEK fallback these tests rely on to load the
// KEK-wrapped id_token signing key. Mirrors internal/oauthprovider's TestMain.
func TestMain(m *testing.M) {
	if os.Getenv("VULOS_ENV") == "" {
		os.Setenv("VULOS_ENV", "local")
	}
	os.Exit(m.Run())
}
