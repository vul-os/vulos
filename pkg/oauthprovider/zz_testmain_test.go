package oauthprovider

import (
	"os"
	"testing"
)

// TestMain defaults VULOS_ENV=local for this test binary. WAVE37: the single
// prod resolver (env.IsProdResolved) fails safe to prod when VULOS_ENV is unset,
// which would refuse the dev-KEK fallback these unit tests rely on. Setting it
// here (only when unset) makes the whole binary a dev environment by default;
// tests that need prod set VULOS_ENV=prod explicitly (via t.Setenv), which wins.
func TestMain(m *testing.M) {
	if os.Getenv("VULOS_ENV") == "" {
		os.Setenv("VULOS_ENV", "local")
	}
	// LAUNCH grantability gate: the mail.* scopes are UN-GRANTABLE by default while
	// hosted mail is dormant. The bulk of the existing suite registers clients for
	// (and drives authorize flows with) mail.read/mail.send, so default the whole
	// binary to hosted-mail-ENABLED. The dedicated gate tests (scope_grantable_test.go)
	// flip it OFF with t.Setenv, which wins over this process-level default.
	if os.Getenv("VULOS_HOSTED_MAIL") == "" {
		os.Setenv("VULOS_HOSTED_MAIL", "1")
	}
	os.Exit(m.Run())
}
