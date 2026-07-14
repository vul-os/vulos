package appnet

// registry_prodgate_test.go — REGISTRY-SIGN-02: VULOS_REGISTRY_INSECURE is a
// DEV-ONLY escape hatch and is refused in production.
//
// The repo-wide idiom (services/env) is that an unset VULOS_ENV means prod. So
// the default posture of an unconfigured box is "refuse", not "allow" — these
// tests pin that, because the failure mode of getting it backwards is a box that
// installs unsigned apps and never says a word.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"vulos/backend/services/signing"
)

// unsignedApp is a registry whose single entry carries no signature. In any
// correct configuration it must be refused.
func unsignedApp() *Registry {
	return &Registry{
		Apps: map[string]*RegistryEntry{
			"myapp": {
				Name:     "My App",
				Vetted:   true,
				Type:     "web",
				Versions: map[string]*VersionRecipe{"1.0": {Install: "apt-get install -y myapp", Command: "myapp", Port: 8080}},
			},
		},
	}
}

// TestProdGate_InsecureFlagRefusedInProd — the headline: the escape hatch does
// not work in prod, and the refusal is an error, not a silent downgrade.
func TestProdGate_InsecureFlagRefusedInProd(t *testing.T) {
	t.Setenv("VULOS_ENV", "prod")
	t.Setenv(envRegistryInsecure, "1")
	isolateTrustSources(t)

	if registryInsecureActive() {
		t.Fatal("registryInsecureActive() is true in prod — the escape hatch is open")
	}

	_, err := TrustedKey()
	if err == nil {
		t.Fatal("TrustedKey succeeded with VULOS_REGISTRY_INSECURE=1 in prod")
	}
	if !strings.Contains(err.Error(), "production") {
		t.Errorf("expected the error to name the production environment, got: %v", err)
	}

	err = InstallFromRegistry(context.Background(), unsignedApp(), "myapp", "1.0", t.TempDir())
	if err == nil {
		t.Fatal("unsigned app INSTALLED with VULOS_REGISTRY_INSECURE=1 in prod")
	}
	t.Logf("correctly refused: %v", err)
}

// TestProdGate_InsecureFlagRefusedWhenEnvUnset — an operator who never sets
// VULOS_ENV is in prod by default (services/env). The escape hatch must not
// quietly work there, which is the whole point of defaulting to prod.
func TestProdGate_InsecureFlagRefusedWhenEnvUnset(t *testing.T) {
	t.Setenv("VULOS_ENV", "") // explicitly unset → env.Parse defaults to prod
	t.Setenv(envRegistryInsecure, "1")
	isolateTrustSources(t)

	if registryInsecureActive() {
		t.Fatal("insecure mode active with VULOS_ENV unset — default must be prod/closed")
	}
	if err := InstallFromRegistry(context.Background(), unsignedApp(), "myapp", "1.0", t.TempDir()); err == nil {
		t.Fatal("unsigned app INSTALLED with VULOS_ENV unset — default is fail-open")
	}
}

// TestProdGate_InsecureFlagWorksInLocal — the hatch is still usable where it is
// meant to be usable. A dev-only gate that also blocks dev is just a bug.
func TestProdGate_InsecureFlagWorksInLocal(t *testing.T) {
	for _, e := range []string{"local", "dev"} {
		t.Run(e, func(t *testing.T) {
			t.Setenv("VULOS_ENV", e)
			t.Setenv(envRegistryInsecure, "1")
			isolateTrustSources(t)

			if !registryInsecureActive() {
				t.Fatalf("insecure mode should be permitted in VULOS_ENV=%s", e)
			}
			key, err := TrustedKey()
			if err != nil {
				t.Fatalf("TrustedKey in %s with insecure=1: %v", e, err)
			}
			if key != nil {
				t.Fatalf("expected a nil key in insecure mode, got one")
			}
		})
	}
}

// TestProdGate_NoAnchorRefusedInProd — no anchor, no flag, prod: refuse.
func TestProdGate_NoAnchorRefusedInProd(t *testing.T) {
	t.Setenv("VULOS_ENV", "prod")
	t.Setenv(envRegistryInsecure, "")
	t.Setenv(envRegistryPubKey, "")
	isolateTrustSources(t)

	_, err := TrustedKey()
	if err == nil {
		t.Fatal("TrustedKey succeeded with no trust anchor in prod")
	}
	if !strings.Contains(err.Error(), "no trust anchor") {
		t.Errorf("expected a 'no trust anchor' error, got: %v", err)
	}
}

// TestProdGate_DevKeyRefusedInProd — the pinned dev keys must be refused in prod
// whichever door they come through: the anchor file, or VULOS_REGISTRY_PUBKEY.
func TestProdGate_DevKeyRefusedInProd(t *testing.T) {
	devRootPub, _ := deriveDevRoot()
	devReleasePub, _ := deriveDevRelease()

	t.Run("via_VULOS_REGISTRY_PUBKEY_root", func(t *testing.T) {
		assertDevKeyRefused(t, devRootPub)
	})
	t.Run("via_VULOS_REGISTRY_PUBKEY_release", func(t *testing.T) {
		// Setting the dev RELEASE key directly would otherwise sidestep the
		// anchor/cert chain entirely.
		assertDevKeyRefused(t, devReleasePub)
	})
}

func assertDevKeyRefused(t *testing.T, pub ed25519.PublicKey) {
	t.Helper()
	t.Setenv("VULOS_ENV", "prod")
	t.Setenv(envRegistryInsecure, "")
	isolateTrustSources(t)
	t.Setenv(envRegistryPubKey, base64.StdEncoding.EncodeToString(pub))

	_, err := TrustedKey()
	if err == nil {
		t.Fatal("a development key was ACCEPTED in prod")
	}
	if !strings.Contains(err.Error(), "DEVELOPMENT") {
		t.Errorf("expected a dev-key refusal, got: %v", err)
	}
}

// TestProdGate_MalformedAnchorIsHardError — a present-but-corrupt anchor must
// abort, never fall through to a weaker source. Falling through would let an
// attacker who can corrupt one byte of the anchor file downgrade the box to
// whatever VULOS_REGISTRY_PUBKEY they can also set.
func TestProdGate_MalformedAnchorIsHardError(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "trust-anchor.pub")
	if err := os.WriteFile(bad, []byte("this is not a key"), 0444); err != nil {
		t.Fatalf("write anchor: %v", err)
	}

	goodPub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	t.Setenv("VULOS_ENV", "prod")
	t.Setenv(envRegistryInsecure, "")
	t.Setenv(envTrustAnchor, bad)
	t.Setenv(envReleaseCert, filepath.Join(dir, "absent.json"))
	// A perfectly good fallback key is available — it must NOT be used.
	t.Setenv(envRegistryPubKey, base64.StdEncoding.EncodeToString(goodPub))

	key, err := TrustedKey()
	if err == nil {
		t.Fatal("a malformed trust anchor fell through to VULOS_REGISTRY_PUBKEY — fail-open")
	}
	if key != nil {
		t.Fatal("TrustedKey returned a key alongside an error")
	}
	if !strings.Contains(err.Error(), "unusable") {
		t.Errorf("expected an 'unusable anchor' error, got: %v", err)
	}
}

// TestProdGate_UnrecognisedEnvIsRefused — a typo'd VULOS_ENV ("prd", "produciton")
// must not resolve to "not prod" and thereby open the escape hatch.
func TestProdGate_UnrecognisedEnvIsRefused(t *testing.T) {
	t.Setenv("VULOS_ENV", "prd")
	t.Setenv(envRegistryInsecure, "1")
	isolateTrustSources(t)

	if registryInsecureActive() {
		t.Fatal("insecure mode active under an unrecognised VULOS_ENV")
	}
	if _, err := TrustedKey(); err == nil {
		t.Fatal("TrustedKey succeeded under an unrecognised VULOS_ENV")
	}
}

// TestProdGate_EntryDisabledIsRefused — an app the maintainers pulled
// (_disabled at the entry level) must not install, even correctly signed.
func TestProdGate_EntryDisabledIsRefused(t *testing.T) {
	pub, priv := generateTestKey(t)
	withTrustAnchor(t, pub)

	entry := &RegistryEntry{
		Name:     "Pulled App",
		Vetted:   true,
		Type:     "web",
		Disabled: true,
		Versions: map[string]*VersionRecipe{"1.0": {Install: "apt-get install -y pulled", Command: "pulled"}},
	}
	if err := SignEntry(entry, "pulled", priv); err != nil {
		t.Fatalf("SignEntry: %v", err)
	}

	reg := &Registry{Apps: map[string]*RegistryEntry{"pulled": entry}}
	err := InstallFromRegistry(context.Background(), reg, "pulled", "1.0", t.TempDir())
	if err == nil {
		t.Fatal("an administratively disabled entry was INSTALLED")
	}
	if !strings.Contains(err.Error(), "disabled") {
		t.Errorf("expected a 'disabled' rejection, got: %v", err)
	}
}

// deriveDevRoot / deriveDevRelease re-derive the pinned dev keys from their
// published seeds — the same derivation scripts/signing/dev-keys.sh uses.
func deriveDevRoot() (ed25519.PublicKey, ed25519.PrivateKey) {
	return signing.DeriveDevKey(signing.DevRootSeed)
}

func deriveDevRelease() (ed25519.PublicKey, ed25519.PrivateKey) {
	return signing.DeriveDevKey(signing.DevReleaseSeed)
}
