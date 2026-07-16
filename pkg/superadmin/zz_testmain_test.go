package superadmin

import (
	"os"
	"testing"
)

// TestMain defaults VULOS_ENV=local for this test binary. WAVE37: the single
// prod resolver (env.IsProdResolved) fails safe to prod when VULOS_ENV is unset.
// Several superadmin tests exercise dev behaviour (e.g. an empty IP allowlist is
// permitted outside prod); setting it here (only when unset) makes the whole
// binary a dev environment by default. Tests that need prod set VULOS_ENV=prod
// explicitly, which wins.
func TestMain(m *testing.M) {
	if os.Getenv("VULOS_ENV") == "" {
		os.Setenv("VULOS_ENV", "local")
	}
	os.Exit(m.Run())
}
