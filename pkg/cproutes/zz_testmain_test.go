package cproutes

import (
	"os"
	"testing"
)

// TestMain defaults VULOS_ENV=local for this test binary. The single prod
// resolver (env.IsProdResolved) fails safe to prod when VULOS_ENV is unset,
// which would refuse the dev-KEK / dev-secret fallbacks and enforce prod-only
// gates (Secure cookies, https origins, cookie-domain requirement) that these
// route tests do not configure. Setting it here (only when unset) makes the
// whole binary a dev environment by default; tests that need prod set
// VULOS_ENV=prod explicitly, which wins.
func TestMain(m *testing.M) {
	if os.Getenv("VULOS_ENV") == "" {
		os.Setenv("VULOS_ENV", "local")
	}
	os.Exit(m.Run())
}
