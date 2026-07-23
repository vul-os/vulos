package cpserver_test

import (
	"os"
	"testing"
)

// TestMain defaults VULOS_ENV=local so the prod resolver's fail-safe (which
// refuses to start without SESSION_SECRET, Secure cookies, https origins) does
// not trip in these self-host assembly tests. Set VULOS_ENV=prod explicitly in a
// test that wants prod behaviour.
func TestMain(m *testing.M) {
	if os.Getenv("VULOS_ENV") == "" {
		os.Setenv("VULOS_ENV", "local")
	}
	os.Exit(m.Run())
}
