package env

import (
	"os"
	"testing"
)

// TestActiveFromFlagWithoutEnvVar is the regression test for the fail-open this
// seam exists to close.
//
// BEFORE: production gates read os.Getenv("VULOS_ENV") == "prod" directly. An
// operator who starts the box the documented way — `vulos-server --env=prod`,
// which is also exactly how cmd/init launches it — sets NO environment
// variable, so every one of those gates evaluated false and took its DEV
// branch on a production box.
//
// The test therefore mimics that invocation precisely: the flag value is
// present, VULOS_ENV is unset, and the production gate must still arm.
func TestActiveFromFlagWithoutEnvVar(t *testing.T) {
	os.Unsetenv("VULOS_ENV")
	t.Cleanup(func() { SetActive("") })

	// What main() does: Parse the flag, then publish the resolved value.
	e, err := Parse("prod")
	if err != nil {
		t.Fatalf("Parse(\"prod\"): %v", err)
	}
	SetActive(e)

	if got := Active(); got != EnvProd {
		t.Errorf("Active() = %q, want %q (--env=prod with VULOS_ENV unset)", got, EnvProd)
	}
	if !IsProdActive() {
		t.Error("IsProdActive() = false with --env=prod and VULOS_ENV unset — production gates would take their DEV branch (this is the fail-open)")
	}
	if v := os.Getenv("VULOS_ENV"); v != "" {
		t.Fatalf("test precondition broken: VULOS_ENV=%q — the point is that it is UNSET", v)
	}
}

// TestActiveNonProdFlagDoesNotArmGates is the other half: --env=local must not
// arm the production gates, or a developer laptop stops booting.
func TestActiveNonProdFlagDoesNotArmGates(t *testing.T) {
	os.Unsetenv("VULOS_ENV")
	t.Cleanup(func() { SetActive("") })

	for _, e := range []Env{EnvLocal, EnvDev} {
		SetActive(e)
		if IsProdActive() {
			t.Errorf("IsProdActive() = true with --env=%s", e)
		}
		if got := Active(); got != e {
			t.Errorf("Active() = %q, want %q", got, e)
		}
	}
}

// TestActiveFallsBackToEnvVarBeforeResolution pins the pre-SetActive behaviour:
// package unit tests and helper binaries that never parse the flag keep reading
// VULOS_ENV exactly as they did before this seam existed.
func TestActiveFallsBackToEnvVarBeforeResolution(t *testing.T) {
	SetActive("") // unresolved, as in a package test binary
	t.Cleanup(func() { SetActive("") })

	t.Setenv("VULOS_ENV", "prod")
	if !IsProdActive() {
		t.Error("IsProdActive() = false with VULOS_ENV=prod and no SetActive — the env var must still work")
	}

	t.Setenv("VULOS_ENV", "local")
	if IsProdActive() {
		t.Error("IsProdActive() = true with VULOS_ENV=local")
	}
}

// TestActiveUnresolvedAndUnsetIsNotProd guards the one thing Active() must NOT
// inherit from Parse: Parse's empty-input default is prod, but applying that
// default before main() has resolved anything would arm every fail-closed gate
// inside every package's test binary.
func TestActiveUnresolvedAndUnsetIsNotProd(t *testing.T) {
	SetActive("")
	os.Unsetenv("VULOS_ENV")
	t.Cleanup(func() { SetActive("") })

	if got := Active(); got != "" {
		t.Errorf("Active() = %q, want \"\" when unresolved and VULOS_ENV unset", got)
	}
	if IsProdActive() {
		t.Error("IsProdActive() = true when unresolved and VULOS_ENV unset")
	}
}

// TestSetActiveOverridesEnvVar pins the documented precedence: --env overrides
// VULOS_ENV (see Parse), so the resolved value wins once main() publishes it.
// Without this, the flag and the variable could still disagree at a gate.
func TestSetActiveOverridesEnvVar(t *testing.T) {
	t.Setenv("VULOS_ENV", "local")
	t.Cleanup(func() { SetActive("") })

	e, err := Parse("prod") // `--env=prod` on the command line
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	SetActive(e)

	if !IsProdActive() {
		t.Error("IsProdActive() = false with --env=prod overriding VULOS_ENV=local")
	}
}
