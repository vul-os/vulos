package vault

import (
	"context"
	"errors"
	"os"
	"testing"

	vulenv "vulos/backend/services/env"
)

// TestInitRefusesDevPasswordWhenStartedWithEnvFlag is the regression test for
// the --env=prod fail-open.
//
// BEFORE: this gate read os.Getenv("VULOS_ENV") == "prod". A box started the
// documented way — `vulos-server --env=prod`, which is also how cmd/init
// launches it — sets no environment variable, so the gate was false and the
// vault happily encrypted every production backup with the compiled-in
// "vulos-default-key" that anyone can read out of the source tree.
//
// So: VULOS_ENV is explicitly UNSET here, and the environment is supplied the
// way main() supplies it (Parse the flag, publish the result).
func TestInitRefusesDevPasswordWhenStartedWithEnvFlag(t *testing.T) {
	os.Unsetenv("VULOS_ENV")
	os.Unsetenv("VULOS_RESTIC_PASSWORD")
	os.Unsetenv("RESTIC_PASSWORD")

	resolved, err := vulenv.Parse("prod") // exactly `--env=prod`
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	vulenv.SetActive(resolved)
	t.Cleanup(func() { vulenv.SetActive("") })

	if v := os.Getenv("VULOS_ENV"); v != "" {
		t.Fatalf("test precondition broken: VULOS_ENV=%q — this test only proves anything while it is UNSET", v)
	}

	v := New(configuredS3(), t.TempDir())
	if !v.usingDefaultPassword() {
		t.Fatalf("test precondition broken: vault is not on the dev default password (got %q)", v.password)
	}

	if err := v.Init(context.Background()); !errors.Is(err, errDefaultPasswordInProd) {
		t.Fatalf("Init() = %v, want errDefaultPasswordInProd — with --env=prod the vault must refuse the dev key, not encrypt production backups with it", err)
	}
}

// TestBackupRefusesDevPasswordWhenStartedWithEnvFlag covers the second gate
// (defence in depth: Backup is reached without Init when the repo already
// exists), same --env=prod / VULOS_ENV-unset invocation.
func TestBackupRefusesDevPasswordWhenStartedWithEnvFlag(t *testing.T) {
	os.Unsetenv("VULOS_ENV")
	os.Unsetenv("VULOS_RESTIC_PASSWORD")
	os.Unsetenv("RESTIC_PASSWORD")

	resolved, err := vulenv.Parse("prod")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	vulenv.SetActive(resolved)
	t.Cleanup(func() { vulenv.SetActive("") })

	v := New(configuredS3(), t.TempDir())
	v.status.Initialized = true // repo already exists; Init is never called

	if err := v.Backup(context.Background()); !errors.Is(err, errDefaultPasswordInProd) {
		t.Fatalf("Backup() = %v, want errDefaultPasswordInProd", err)
	}
}

// TestInitAllowsDevPasswordOutsideProd is the counterweight: a local/dev box
// with no RESTIC_PASSWORD must still boot. Without this, a gate that simply
// returned an error unconditionally would pass the two tests above.
func TestInitAllowsDevPasswordOutsideProd(t *testing.T) {
	os.Unsetenv("VULOS_ENV")
	os.Unsetenv("VULOS_RESTIC_PASSWORD")
	os.Unsetenv("RESTIC_PASSWORD")

	vulenv.SetActive(vulenv.EnvLocal) // `--env=local`
	t.Cleanup(func() { vulenv.SetActive("") })

	v := New(configuredS3(), t.TempDir())
	err := v.Init(context.Background())
	if errors.Is(err, errDefaultPasswordInProd) {
		t.Fatalf("Init() refused the dev key with --env=local: %v", err)
	}
	// Any other error (restic missing, S3 unreachable) is fine and expected —
	// this test only asserts the passphrase gate did not fire.
}
