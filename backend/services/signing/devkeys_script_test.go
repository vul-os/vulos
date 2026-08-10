package signing

// devkeys_script_test.go — the guard on scripts/signing/dev-keys.sh.
//
// dev-keys.sh regenerates the repo's DEVELOPMENT signing keys, overwriting the
// SHIPPED public trust material (keys/trust-anchor.pub, keys/release-cert.json,
// keys/*.pub.json).  `make sign-registry` runs it AUTOMATICALLY whenever the dev
// release private key is missing — which is always true on a fresh clone, since
// *.priv.json is gitignored.  On a tree carrying real ceremony output that meant
// a routine command silently replaced the production root of trust with a
// keypair derived from a published seed, and then re-signed every registry entry
// with it.
//
// The script now refuses when keys/ holds material it did not produce.  These
// tests exercise that refusal (and the deliberate opt-in that overrides it)
// through the real script — running it, not reading it.

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// devKeysScript returns the absolute path of the script under test, derived
// from this file's own location so the test does not depend on the working
// directory.
func devKeysScript(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	// backend/services/signing/<this file> → repo root is three levels up.
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", "..", ".."))
	script := filepath.Join(repoRoot, "scripts", "signing", "dev-keys.sh")
	if _, err := os.Stat(script); err != nil {
		t.Fatalf("dev-keys.sh not found at %s: %v", script, err)
	}
	return script
}

// runDevKeysGuard runs dev-keys.sh against keysDir in check-only mode (the
// script exits right after the guard, so no key derivation happens) and returns
// its combined output plus whether it exited 0.
func runDevKeysGuard(t *testing.T, keysDir string, extraEnv ...string) (string, bool) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("dev-keys.sh is a bash script")
	}
	cmd := exec.Command("bash", devKeysScript(t))
	cmd.Env = append(os.Environ(),
		"VULOS_DEV_KEYS_DIR="+keysDir,
		"VULOS_DEV_KEYS_CHECK_ONLY=1",
	)
	cmd.Env = append(cmd.Env, extraEnv...)
	out, err := cmd.CombinedOutput()
	return string(out), err == nil
}

// prodAnchor is a plausible non-dev anchor: the shape of real ceremony output
// (a base64 Ed25519 public key) that is NOT DevAnchorPubB64.
const prodAnchor = "jAkiIZ98WqTfeaIVOMRZ3jiaC+tOLM0lHnWRJ5kh/dM="

func writeKeysFixture(t *testing.T, anchor, keyID string) string {
	t.Helper()
	dir := t.TempDir()
	if anchor != "" {
		if err := os.WriteFile(filepath.Join(dir, "trust-anchor.pub"), []byte(anchor+"\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if keyID != "" {
		cert := `{"release_pubkey":"00","key_id":"` + keyID +
			`","not_after":"2027-08-03T00:00:00Z","min_epoch":1,"root_sig":"AA=="}`
		if err := os.WriteFile(filepath.Join(dir, "release-cert.json"), []byte(cert), 0644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// TestDevKeysScript_RefusesToClobberProductionAnchor is the headline: a keys/
// directory holding a non-dev trust anchor must stop the script dead, loudly,
// and leave the anchor byte-for-byte intact.
// The fixture deliberately carries the anchor ALONE (no release cert): the two
// discriminators must each be able to stop the script on their own. With both
// present, deleting the anchor check still left this test green — mutation
// found exactly that.
func TestDevKeysScript_RefusesToClobberProductionAnchor(t *testing.T) {
	dir := writeKeysFixture(t, prodAnchor, "")
	anchorPath := filepath.Join(dir, "trust-anchor.pub")
	before, err := os.ReadFile(anchorPath)
	if err != nil {
		t.Fatal(err)
	}

	out, ok := runDevKeysGuard(t, dir)
	if ok {
		t.Fatalf("dev-keys.sh SUCCEEDED against a production trust anchor — it would have overwritten it.\n%s", out)
	}
	if !strings.Contains(out, "REFUSING") {
		t.Errorf("refusal is not loud; output was:\n%s", out)
	}
	// Instructive, not just loud: it must name the way forward.
	for _, want := range []string{"RELEASE_PRIV", "KEY-CEREMONY.md", "VULOS_DEV_KEYS_OVERWRITE"} {
		if !strings.Contains(out, want) {
			t.Errorf("refusal message does not mention %q; output was:\n%s", want, out)
		}
	}

	after, err := os.ReadFile(anchorPath)
	if err != nil {
		t.Fatalf("anchor disappeared: %v", err)
	}
	if string(after) != string(before) {
		t.Errorf("trust anchor was modified despite the refusal\nbefore: %q\nafter:  %q", before, after)
	}
}

// TestDevKeysScript_RefusesForeignReleaseCert covers the second discriminator:
// even with a dev anchor in place, a release cert issued to a non-dev key id is
// ceremony output and must not be overwritten.
func TestDevKeysScript_RefusesForeignReleaseCert(t *testing.T) {
	dir := writeKeysFixture(t, DevAnchorPubB64, "release-2026-08")
	out, ok := runDevKeysGuard(t, dir)
	if ok {
		t.Fatalf("dev-keys.sh SUCCEEDED against a production release cert.\n%s", out)
	}
	if !strings.Contains(out, "release-2026-08") {
		t.Errorf("refusal should name the offending key id; output was:\n%s", out)
	}
}

// TestDevKeysScript_AllowsDevMaterial — the guard must not become a wall: a
// keys/ directory that holds exactly what this script produces is safe to
// regenerate, and so is an empty one (the fresh-clone case the automatic
// invocation in `make sign-registry` exists for).
func TestDevKeysScript_AllowsDevMaterial(t *testing.T) {
	t.Run("dev anchor and dev cert", func(t *testing.T) {
		dir := writeKeysFixture(t, DevAnchorPubB64, "dev-release-DO-NOT-TRUST")
		out, ok := runDevKeysGuard(t, dir)
		if !ok {
			t.Fatalf("dev-keys.sh refused its OWN material:\n%s", out)
		}
	})
	t.Run("empty keys dir", func(t *testing.T) {
		out, ok := runDevKeysGuard(t, t.TempDir())
		if !ok {
			t.Fatalf("dev-keys.sh refused an empty keys dir (the fresh-clone case):\n%s", out)
		}
	})
}

// TestDevKeysScript_ExplicitOverwriteOptIn — the escape hatch exists and has to
// be typed out; it is never implied by anything the Makefile does on its own.
func TestDevKeysScript_ExplicitOverwriteOptIn(t *testing.T) {
	dir := writeKeysFixture(t, prodAnchor, "release-2026-08")
	out, ok := runDevKeysGuard(t, dir, "VULOS_DEV_KEYS_OVERWRITE=1")
	if !ok {
		t.Fatalf("explicit VULOS_DEV_KEYS_OVERWRITE=1 was still refused:\n%s", out)
	}
	if !strings.Contains(out, "overwriting existing trust material") {
		t.Errorf("the override should still announce itself; output was:\n%s", out)
	}
}
