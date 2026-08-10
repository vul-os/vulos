package signing

// signregistry_make_test.go — the guard on `make sign-registry`.
//
// The Makefile defaults RELEASE_PRIV to keys/release.priv.json.  That file is
// gitignored, so it is absent on a fresh clone — but on any machine where
// `make dev-keys` has ever run it is the DEV release key, derived from a
// published seed.  keys/release-cert.json and keys/trust-anchor.pub are TRACKED
// and, on a tree carrying real ceremony output, are PRODUCTION material.  So
// `make sign-registry`, typed with no arguments and nothing missing, re-signed
// every entry of the TRACKED registry.json with a key whose private half is
// published.  verify-registry catches the result, but only AFTER the tracked
// file has been rewritten, and only if whoever ran it reads the output.
//
// `make check-release-key` closes it, and sign-registry / publish-feed run it
// before the signer.  It decides on the committed material itself — the public
// half recorded in RELEASE_PRIV must be the release_pubkey the SHIPPED
// certificate authorises — so it cannot drift away from the cert the image
// actually ships.
//
// These tests run the real Makefile target.  Reading the recipe would prove
// nothing; the refusal has to happen.

import (
	"encoding/base64"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// makeRepoRoot returns the repository root, derived from this file's own
// location so the tests do not depend on the working directory.
func makeRepoRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	// backend/services/signing/<this file> → repo root is three levels up.
	root := filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", "..", ".."))
	if _, err := os.Stat(filepath.Join(root, "Makefile")); err != nil {
		t.Fatalf("Makefile not found at %s: %v", root, err)
	}
	return root
}

// runMake runs `make <args...>` at the repo root and returns its combined
// output plus whether it exited 0.
func runMake(t *testing.T, env []string, args ...string) (string, bool) {
	t.Helper()
	if _, err := exec.LookPath("make"); err != nil {
		t.Skip("make not available")
	}
	cmd := exec.Command("make", args...)
	cmd.Dir = makeRepoRoot(t)
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	return string(out), err == nil
}

// certFixture writes a release certificate and a private-key file into a temp
// dir, in the exact shapes cmd/sign emits (issue-release-cert writes
// release_pubkey as hex; gen-key writes public_key as hex).
func certFixture(t *testing.T, certPub, keyID, privPub string) (certPath, privPath string) {
	t.Helper()
	dir := t.TempDir()
	certPath = filepath.Join(dir, "release-cert.json")
	privPath = filepath.Join(dir, "release.priv.json")
	cert := `{
  "release_pubkey": "` + certPub + `",
  "key_id": "` + keyID + `",
  "not_after": "2027-08-03T00:00:00Z",
  "min_epoch": 1,
  "root_sig": "AA=="
}`
	if err := os.WriteFile(certPath, []byte(cert), 0644); err != nil {
		t.Fatal(err)
	}
	if privPub != "" {
		priv := `{"algorithm":"ed25519","private_key":"00","public_key":"` + privPub + `"}`
		if err := os.WriteFile(privPath, []byte(priv), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return certPath, privPath
}

const (
	// prodReleasePub is the shape of real ceremony output: a hex Ed25519
	// public key that is NOT the dev release key.
	prodReleasePub = "dbc913bf7b1e806bf2a1c9a146bde683ea514e0c8845639907f2d75781f7a71c"
	// devReleasePubHex is the published-seed DEV release key in the encoding
	// the private-key file uses.  Derived from DevReleasePubB64 below so the
	// two cannot drift.
	devReleasePubHex = "ba8b1e8be03cb3cdc23acbb40812239827f81f83e9c576934096abeeb51a7f01"
)

// TestCheckReleaseKey_DevPubHexMatchesPinnedConstant keeps the hex literal above
// honest: it is the same key as signing.DevReleasePubB64, just re-encoded.
func TestCheckReleaseKey_DevPubHexMatchesPinnedConstant(t *testing.T) {
	pub, err := base64.StdEncoding.DecodeString(DevReleasePubB64)
	if err != nil {
		t.Fatalf("decode DevReleasePubB64: %v", err)
	}
	if got := hex.EncodeToString(pub); got != devReleasePubHex {
		t.Fatalf("devReleasePubHex is stale: DevReleasePubB64 is %s", got)
	}
}

// TestCheckReleaseKey_RefusesDevKeyAgainstProductionCert is the headline: the
// exact tree this guard exists for — a leftover dev private key from `make
// dev-keys`, next to a tracked PRODUCTION release certificate.
func TestCheckReleaseKey_RefusesDevKeyAgainstProductionCert(t *testing.T) {
	cert, priv := certFixture(t, prodReleasePub, "release-2026-08", devReleasePubHex)

	out, ok := runMake(t, nil, "check-release-key", "CERT="+cert, "RELEASE_PRIV="+priv)
	if ok {
		t.Fatalf("check-release-key SUCCEEDED with the dev key against a production cert — sign-registry would have re-signed the tracked registry with it.\n%s", out)
	}
	if !strings.Contains(out, "REFUSING") {
		t.Errorf("refusal is not loud; output was:\n%s", out)
	}
	// Instructive: it must name what the cert authorises, and the ways forward.
	for _, want := range []string{
		"release-2026-08",               // the cert's key id
		devReleasePubHex[:16],           // the key it was asked to sign with
		prodReleasePub[:16],             // the key the cert authorises
		"RELEASE_PRIV=",                 // point at the real key
		"KEY-CEREMONY.md",               // the ceremony doc
		"VULOS_SIGN_ALLOW_KEY_MISMATCH", // the typed-out opt-in
	} {
		if !strings.Contains(out, want) {
			t.Errorf("refusal does not mention %q; output was:\n%s", want, out)
		}
	}
}

// TestCheckReleaseKey_RefusalIsOnStderr — sign-registry's sibling steps send
// stdout to /dev/null, so a refusal printed on stdout could vanish entirely.
func TestCheckReleaseKey_RefusalIsOnStderr(t *testing.T) {
	if _, err := exec.LookPath("make"); err != nil {
		t.Skip("make not available")
	}
	cert, priv := certFixture(t, prodReleasePub, "release-2026-08", devReleasePubHex)

	cmd := exec.Command("make", "check-release-key", "CERT="+cert, "RELEASE_PRIV="+priv)
	cmd.Dir = makeRepoRoot(t)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err == nil {
		t.Fatalf("expected a refusal; stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "REFUSING") {
		t.Errorf("the refusal must reach STDERR (stdout is piped to /dev/null); stderr was:\n%s", stderr.String())
	}
}

// TestCheckReleaseKey_AllowsTheCertifiedKey — the guard must not become a wall.
// A private key whose public half IS what the cert authorises signs freely, and
// hex case must not decide it.
func TestCheckReleaseKey_AllowsTheCertifiedKey(t *testing.T) {
	t.Run("exact match", func(t *testing.T) {
		cert, priv := certFixture(t, prodReleasePub, "release-2026-08", prodReleasePub)
		out, ok := runMake(t, nil, "check-release-key", "CERT="+cert, "RELEASE_PRIV="+priv)
		if !ok {
			t.Fatalf("the certified key was refused:\n%s", out)
		}
	})
	t.Run("case-insensitive hex", func(t *testing.T) {
		cert, priv := certFixture(t, prodReleasePub, "release-2026-08", strings.ToUpper(prodReleasePub))
		out, ok := runMake(t, nil, "check-release-key", "CERT="+cert, "RELEASE_PRIV="+priv)
		if !ok {
			t.Fatalf("the certified key was refused over hex case alone:\n%s", out)
		}
	})
	t.Run("dev key against dev cert", func(t *testing.T) {
		cert, priv := certFixture(t, devReleasePubHex, "dev-release-DO-NOT-TRUST", devReleasePubHex)
		out, ok := runMake(t, nil, "check-release-key", "CERT="+cert, "RELEASE_PRIV="+priv)
		if !ok {
			t.Fatalf("dev signing on a dev tree must keep working:\n%s", out)
		}
	})
}

// TestCheckReleaseKey_MissingPrivateKeyIsNotThisGuardsBusiness — on a fresh
// clone RELEASE_PRIV does not exist yet, and sign-registry's own missing-key
// block (which calls dev-keys.sh, itself guarded) handles that case.  This
// guard must pass it through rather than break the fresh-clone path.
func TestCheckReleaseKey_MissingPrivateKeyIsNotThisGuardsBusiness(t *testing.T) {
	cert, priv := certFixture(t, prodReleasePub, "release-2026-08", "")
	out, ok := runMake(t, nil, "check-release-key", "CERT="+cert, "RELEASE_PRIV="+priv)
	if !ok {
		t.Fatalf("an absent RELEASE_PRIV must not be refused here:\n%s", out)
	}
}

// TestCheckReleaseKey_RefusesWhenItCannotDecide — an unreadable cert, or one
// carrying no release_pubkey, leaves nothing saying which key may sign.  Refuse
// rather than guess: the failure mode this guard exists for is silent.
func TestCheckReleaseKey_RefusesWhenItCannotDecide(t *testing.T) {
	t.Run("no cert", func(t *testing.T) {
		_, priv := certFixture(t, prodReleasePub, "release-2026-08", devReleasePubHex)
		out, ok := runMake(t, nil, "check-release-key",
			"CERT="+filepath.Join(t.TempDir(), "absent.json"), "RELEASE_PRIV="+priv)
		if ok {
			t.Fatalf("a missing certificate must not be signed past:\n%s", out)
		}
	})
	t.Run("cert without release_pubkey", func(t *testing.T) {
		dir := t.TempDir()
		cert := filepath.Join(dir, "release-cert.json")
		if err := os.WriteFile(cert, []byte(`{"key_id":"release-2026-08"}`), 0644); err != nil {
			t.Fatal(err)
		}
		_, priv := certFixture(t, prodReleasePub, "release-2026-08", devReleasePubHex)
		out, ok := runMake(t, nil, "check-release-key", "CERT="+cert, "RELEASE_PRIV="+priv)
		if ok {
			t.Fatalf("a cert naming no release key must not be signed past:\n%s", out)
		}
	})
}

// TestCheckReleaseKey_ExplicitOptIn — the escape hatch exists, has to be typed
// out, and announces itself.  Nothing the Makefile does on its own sets it.
func TestCheckReleaseKey_ExplicitOptIn(t *testing.T) {
	cert, priv := certFixture(t, prodReleasePub, "release-2026-08", devReleasePubHex)
	out, ok := runMake(t, []string{"VULOS_SIGN_ALLOW_KEY_MISMATCH=1"},
		"check-release-key", "CERT="+cert, "RELEASE_PRIV="+priv)
	if !ok {
		t.Fatalf("the explicit opt-in was still refused:\n%s", out)
	}
	if !strings.Contains(out, "VULOS_SIGN_ALLOW_KEY_MISMATCH=1") {
		t.Errorf("the override must announce itself; output was:\n%s", out)
	}
}

// TestCheckReleaseKey_IsWiredIntoTheSigningTargets — a guard nothing calls is a
// guard that checks nothing.  `make -n` resolves the recipes through make
// itself rather than by reading the file, so this fails if the invocation is
// dropped, renamed, or moved behind a variable that stops expanding.
func TestCheckReleaseKey_IsWiredIntoTheSigningTargets(t *testing.T) {
	for _, target := range []string{"sign-registry", "publish-feed"} {
		t.Run(target, func(t *testing.T) {
			out, ok := runMake(t, nil, "-n", target)
			if !ok {
				t.Fatalf("make -n %s failed:\n%s", target, out)
			}
			if !strings.Contains(out, "--no-print-directory check-release-key") {
				t.Fatalf("%s does not run check-release-key before signing; recipe was:\n%s", target, out)
			}
			// ...and it must run BEFORE the signer, not after it.
			guard := strings.Index(out, "--no-print-directory check-release-key")
			signer := strings.Index(out, "go run ./cmd/sign "+target)
			if signer < 0 {
				t.Fatalf("could not find the signer invocation in:\n%s", out)
			}
			if guard > signer {
				t.Fatalf("%s runs the guard AFTER signing — the tracked file would already be rewritten;\n%s", target, out)
			}
		})
	}
}
