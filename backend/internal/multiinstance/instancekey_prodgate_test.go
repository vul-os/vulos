package multiinstance

import (
	"os"
	"strings"
	"testing"

	vulenv "vulos/backend/services/env"
)

// SealedKeyFromEnv's production gate must fire on the RESOLVED environment, not
// on os.Getenv("VULOS_ENV").
//
// cmd/init starts the server with `--env prod` and leaves VULOS_ENV unset, so
// the raw getenv read this gate used to do took the DEV branch on a real
// production box: it sealed the fabric signing key — the identity that signs
// uninstall observations counted toward quorum — with a deterministic key
// derived from a hardcoded string that is in this repository. Anyone with the
// source could unseal it. The error path below exists precisely to stop that,
// and it was unreachable by the documented way of starting production.
//
// Nothing covered this gate before, which is why the fail-open survived: the
// only way to notice was to read main.go and the gate side by side.

func TestSealedKeyFromEnvFailsClosedWhenStartedWithEnvFlag(t *testing.T) {
	os.Unsetenv("VULOS_ENV") // the flag is set, the variable is NOT — the real prod shape
	t.Setenv(FabricKeyEnv, "")

	resolved, err := vulenv.Parse("prod")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	vulenv.SetActive(resolved)
	t.Cleanup(func() { vulenv.SetActive("") })

	got, err := SealedKeyFromEnv()
	if err == nil {
		t.Fatalf("SealedKeyFromEnv() = %v, nil — want a fail-closed error; the dev key was used in prod", got)
	}
	if !strings.Contains(err.Error(), FabricKeyEnv) {
		t.Fatalf("error %q does not name %s, so it will not tell an operator what to set", err, FabricKeyEnv)
	}
}

// The counterweight. Without it a gate that simply always errored would pass
// the test above while making every local checkout and test binary fail — the
// reason env.Active() deliberately does not default to prod.
func TestSealedKeyFromEnvUsesDevKeyOutsideProd(t *testing.T) {
	os.Unsetenv("VULOS_ENV")
	t.Setenv(FabricKeyEnv, "")
	vulenv.SetActive("")

	s, err := SealedKeyFromEnv()
	if err != nil {
		t.Fatalf("SealedKeyFromEnv() outside prod = %v, want the dev fallback", err)
	}
	if s == nil {
		t.Fatal("SealedKeyFromEnv() returned nil sealer with no error")
	}
}

// An explicitly supplied key is honoured in prod — the gate guards the UNSET
// case only, and must not reject a properly configured production box.
func TestSealedKeyFromEnvHonoursExplicitKeyInProd(t *testing.T) {
	os.Unsetenv("VULOS_ENV")
	t.Setenv(FabricKeyEnv, strings.Repeat("ab", 32)) // 64 hex chars = 32 bytes

	resolved, err := vulenv.Parse("prod")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	vulenv.SetActive(resolved)
	t.Cleanup(func() { vulenv.SetActive("") })

	if _, err := SealedKeyFromEnv(); err != nil {
		t.Fatalf("SealedKeyFromEnv() with a valid key in prod = %v, want success", err)
	}
}
