package appnet

// registry_acceptance_test.go — REGISTRY-SIGN-02 acceptance suite.
//
// The question this file answers is not "does Ed25519 work" (registry_signing_test.go
// covers that) but "can a real box, holding nothing but the anchor we actually
// ship, install an app with no escape hatch — and does it refuse everything else?"
//
// The two halves:
//
//	fresh box, SHIPPED anchor  → the 55 committed registry.json entries verify,
//	                             and an install completes with NO insecure flag.
//	fresh box, PROD anchor     → the same, under VULOS_ENV=prod, using an anchor
//	                             from a simulated offline ceremony.
//	tampered / unsigned / wrong-key / expired-cert / dev-key-in-prod → REFUSED.
//
// Both halves stage the trust anchor + release cert into a temp directory and
// point the resolver at it, which is exactly what /etc/vulos looks like on a
// real box (see Dockerfile and scripts/seed/embed-anchor.sh).
//
// "EVERY entry signed" here is absolute and has no quarantine or pending state.
// An entry not yet fit to be signed is not excused — it is moved out of
// registry.json entirely, into registry-unverified.json, which nothing loads.
// See registry_quarantine_test.go and docs/KEY-CEREMONY.md § 5.1.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"vulos/backend/services/signing"
)

// repoRoot is this package's path back to the repository root, where the shipped
// registry.json and keys/ live.
const repoRoot = "../../.."

// stageBox writes anchor (and, when cert is non-nil, the release cert) into a
// temp directory and points the trust resolver at them — simulating the
// /etc/vulos of a freshly flashed box.  Nothing else is configured: no
// VULOS_REGISTRY_PUBKEY, and emphatically no VULOS_REGISTRY_INSECURE.
func stageBox(t *testing.T, env string, anchor ed25519.PublicKey, cert *signing.ReleaseCert) {
	t.Helper()

	etc := t.TempDir()
	anchorPath := filepath.Join(etc, "trust-anchor.pub")
	if err := os.WriteFile(anchorPath, []byte(encodeAnchor(anchor)), 0444); err != nil {
		t.Fatalf("stage anchor: %v", err)
	}

	certPath := filepath.Join(etc, "release-cert.json")
	if cert != nil {
		data, err := json.Marshal(cert)
		if err != nil {
			t.Fatalf("marshal cert: %v", err)
		}
		if err := os.WriteFile(certPath, data, 0444); err != nil {
			t.Fatalf("stage cert: %v", err)
		}
	}

	t.Setenv("VULOS_ENV", env)
	t.Setenv(envTrustAnchor, anchorPath)
	t.Setenv(envReleaseCert, certPath) // absent when cert == nil — that is a valid box
	t.Setenv(envRegistryPubKey, "")
	t.Setenv(envRegistryInsecure, "") // the whole point: no escape hatch
}

// encodeAnchor renders a public key in the trust-anchor.pub wire format.
func encodeAnchor(pub ed25519.PublicKey) string {
	return signing.EncodeAnchor(pub)
}

// shippedTrust loads the anchor + release cert this repo actually ships.
func shippedTrust(t *testing.T) (ed25519.PublicKey, signing.ReleaseCert) {
	t.Helper()
	anchor, err := signing.LoadAnchor(filepath.Join(repoRoot, "keys", "trust-anchor.pub"))
	if err != nil {
		t.Fatalf("load shipped trust anchor (run scripts/signing/dev-keys.sh): %v", err)
	}
	cert, err := signing.LoadReleaseCert(filepath.Join(repoRoot, "keys", "release-cert.json"))
	if err != nil {
		t.Fatalf("load shipped release cert (run scripts/signing/dev-keys.sh): %v", err)
	}
	return anchor, cert
}

// shippedRegistry loads the registry.json this repo actually ships.
func shippedRegistry(t *testing.T) *Registry {
	t.Helper()
	reg, err := LoadRegistry(filepath.Join(repoRoot, "registry.json"))
	if err != nil {
		t.Fatalf("load shipped registry.json: %v", err)
	}
	if len(reg.Apps) == 0 {
		t.Fatal("shipped registry.json has no apps")
	}
	return reg
}

// ceremony simulates the offline key ceremony: a root key signs a cert for a
// release key.  The returned keys are freshly generated, so they are NOT the
// pinned dev keys and are therefore trusted even under VULOS_ENV=prod.
func ceremony(t *testing.T, notAfter time.Time) (rootPub ed25519.PublicKey, releasePriv ed25519.PrivateKey, cert signing.ReleaseCert) {
	t.Helper()
	rootPub, rootPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate root key: %v", err)
	}
	releasePub, releasePriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate release key: %v", err)
	}
	cert, err = signing.IssueReleaseCert(rootPriv, releasePub, "acceptance-release", notAfter, 0)
	if err != nil {
		t.Fatalf("issue release cert: %v", err)
	}
	return rootPub, releasePriv, cert
}

// hermeticApp builds a registry entry that installs from a local httptest server
// with a pinned sha256, so an install can complete end-to-end with no network,
// no apt, and no shell.  Signed with releasePriv.
func hermeticApp(t *testing.T, appID string, releasePriv ed25519.PrivateKey) *Registry {
	t.Helper()

	content := []byte("#!/bin/sh\necho acceptance\n")
	sum := sha256.Sum256(content)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(content)
	}))
	t.Cleanup(srv.Close)

	entry := &RegistryEntry{
		Name:        "Acceptance App",
		Vetted:      true,
		Type:        "web",
		Description: "hermetic install fixture",
		Category:    "developer",
		Author:      "Vulos",
		License:     "MIT",
		Homepage:    "https://example.com",
		Icon:        "A",
		Versions: map[string]*VersionRecipe{
			"1.0": {
				DownloadURL: srv.URL + "/" + appID,
				Checksum:    hex.EncodeToString(sum[:]),
				Command:     "bin/" + appID,
				Port:        8080,
			},
		},
	}
	if err := SignEntry(entry, appID, releasePriv); err != nil {
		t.Fatalf("SignEntry: %v", err)
	}
	return &Registry{Apps: map[string]*RegistryEntry{appID: entry}}
}

// assertInstalled checks that an install actually put the binary and manifest on
// disk — proof the gate was cleared, not merely that no error was returned.
func assertInstalled(t *testing.T, appsDir, appID string) {
	t.Helper()
	for _, rel := range []string{filepath.Join("bin", appID), "app.json"} {
		if _, err := os.Stat(filepath.Join(appsDir, appID, rel)); err != nil {
			t.Fatalf("expected %s after install: %v", rel, err)
		}
	}
}

// ─── The acceptance test ──────────────────────────────────────────────────────

// TestAcceptance_ShippedAnchorVerifiesShippedRegistry is the headline claim: a
// box holding ONLY the anchor we ship can verify every entry of the registry we
// ship, with no insecure flag and no other configuration.
//
// It also fails if a new app is added to registry.json without re-signing —
// which is exactly what `make sign-registry` is for.
func TestAcceptance_ShippedAnchorVerifiesShippedRegistry(t *testing.T) {
	anchor, cert := shippedTrust(t)
	stageBox(t, "dev", anchor, &cert)

	key, err := TrustedKey()
	if err != nil {
		t.Fatalf("fresh box could not resolve the trust chain: %v", err)
	}
	if key == nil {
		t.Fatal("TrustedKey returned a nil key with no error — verification would be skipped")
	}

	reg := shippedRegistry(t)
	for appID, entry := range reg.Apps {
		if entry.Signature == "" {
			t.Errorf("registry entry %q is UNSIGNED — run `make sign-registry`", appID)
			continue
		}
		if err := VerifyEntrySignature(entry, appID, key); err != nil {
			t.Errorf("registry entry %q failed verification against the shipped anchor: %v", appID, err)
		}
	}
	t.Logf("verified %d shipped registry entries against the shipped trust anchor", len(reg.Apps))
}

// TestAcceptance_FreshBoxInstallsWithNoInsecureFlag drives a real install to
// completion on a simulated fresh box whose only trust material is the anchor
// and cert from an offline ceremony — under VULOS_ENV=prod.
//
// This is the state a box is in after the founder runs docs/KEY-CEREMONY.md.
func TestAcceptance_FreshBoxInstallsWithNoInsecureFlag(t *testing.T) {
	rootPub, releasePriv, cert := ceremony(t, time.Now().Add(24*time.Hour))
	stageBox(t, "prod", rootPub, &cert)

	reg := hermeticApp(t, "acceptapp", releasePriv)
	appsDir := t.TempDir()

	if err := InstallFromRegistry(context.Background(), reg, "acceptapp", "1.0", appsDir); err != nil {
		t.Fatalf("signed install on a fresh PROD box must succeed with no insecure flag, got: %v", err)
	}
	assertInstalled(t, appsDir, "acceptapp")
}

// TestAcceptance_FreshBoxRefusesTamperedEntry — same box, same key, one byte of
// the entry changed after signing. Must be refused.
func TestAcceptance_FreshBoxRefusesTamperedEntry(t *testing.T) {
	rootPub, releasePriv, cert := ceremony(t, time.Now().Add(24*time.Hour))
	stageBox(t, "prod", rootPub, &cert)

	reg := hermeticApp(t, "acceptapp", releasePriv)
	// Tamper AFTER signing: repoint the download at an attacker's host.
	reg.Apps["acceptapp"].Versions["1.0"].DownloadURL = "http://evil.example.com/payload"

	appsDir := t.TempDir()
	err := InstallFromRegistry(context.Background(), reg, "acceptapp", "1.0", appsDir)
	if err == nil {
		t.Fatal("tampered entry was INSTALLED — signature gate is fail-open")
	}
	if !strings.Contains(err.Error(), "signature") {
		t.Errorf("expected a signature rejection, got: %v", err)
	}
	assertNothingInstalled(t, appsDir)
}

// TestAcceptance_FreshBoxRefusesUnsignedEntry — an entry with the signature
// stripped must be refused, not silently installed.
func TestAcceptance_FreshBoxRefusesUnsignedEntry(t *testing.T) {
	rootPub, releasePriv, cert := ceremony(t, time.Now().Add(24*time.Hour))
	stageBox(t, "prod", rootPub, &cert)

	reg := hermeticApp(t, "acceptapp", releasePriv)
	reg.Apps["acceptapp"].Signature = ""

	appsDir := t.TempDir()
	err := InstallFromRegistry(context.Background(), reg, "acceptapp", "1.0", appsDir)
	if err == nil {
		t.Fatal("unsigned entry was INSTALLED — signature gate is fail-open")
	}
	if !strings.Contains(err.Error(), "signature") {
		t.Errorf("expected a signature rejection, got: %v", err)
	}
	assertNothingInstalled(t, appsDir)
}

// TestAcceptance_FreshBoxRefusesForeignKey — an entry signed by a key the box's
// anchor never certified must be refused, even though it carries a perfectly
// valid Ed25519 signature.
func TestAcceptance_FreshBoxRefusesForeignKey(t *testing.T) {
	rootPub, _, cert := ceremony(t, time.Now().Add(24*time.Hour))
	stageBox(t, "prod", rootPub, &cert)

	_, foreignPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate foreign key: %v", err)
	}
	reg := hermeticApp(t, "acceptapp", foreignPriv)

	appsDir := t.TempDir()
	if err := InstallFromRegistry(context.Background(), reg, "acceptapp", "1.0", appsDir); err == nil {
		t.Fatal("entry signed by an uncertified key was INSTALLED")
	}
	assertNothingInstalled(t, appsDir)
}

// TestAcceptance_FreshBoxRefusesExpiredCert — the release cert is the only thing
// binding the signing key to the anchor. Once it expires the box must stop
// trusting the key, even for entries whose signatures are still valid.
func TestAcceptance_FreshBoxRefusesExpiredCert(t *testing.T) {
	rootPub, releasePriv, cert := ceremony(t, time.Now().Add(-1*time.Hour)) // already expired
	stageBox(t, "prod", rootPub, &cert)

	reg := hermeticApp(t, "acceptapp", releasePriv)
	appsDir := t.TempDir()

	err := InstallFromRegistry(context.Background(), reg, "acceptapp", "1.0", appsDir)
	if err == nil {
		t.Fatal("expired release cert was accepted — install proceeded")
	}
	if !strings.Contains(err.Error(), "expired") {
		t.Errorf("expected an expiry rejection, got: %v", err)
	}
	assertNothingInstalled(t, appsDir)
}

// TestAcceptance_FreshBoxRefusesUncertifiedRootSwap — a cert signed by a
// DIFFERENT root than the one baked into the box must be refused. This is the
// attack the anchor exists to stop: an attacker who can write /etc/vulos/release-cert.json
// but cannot change the baked anchor gains nothing.
func TestAcceptance_FreshBoxRefusesUncertifiedRootSwap(t *testing.T) {
	bakedRootPub, _, _ := ceremony(t, time.Now().Add(24*time.Hour))
	_, attackerReleasePriv, attackerCert := ceremony(t, time.Now().Add(24*time.Hour))

	// Box has the legitimate baked anchor, but the attacker's cert on disk.
	stageBox(t, "prod", bakedRootPub, &attackerCert)

	reg := hermeticApp(t, "acceptapp", attackerReleasePriv)
	appsDir := t.TempDir()

	if err := InstallFromRegistry(context.Background(), reg, "acceptapp", "1.0", appsDir); err == nil {
		t.Fatal("a release cert signed by a foreign root was accepted — anchor is not load-bearing")
	}
	assertNothingInstalled(t, appsDir)
}

// TestAcceptance_ShippedAnchorIsProductionAndAcceptedInProd — the anchor this repo
// ships is the PRODUCTION (main) trust anchor, not a dev key: a box in prod accepts
// it. There is no dev anchor to fall back to, so if a dev key is ever committed as
// the shipped anchor again, TrustedKey() refuses it in prod and this test fails —
// which is exactly the guard we want. A dev-key-signed *entry* is still refused in
// prod regardless, as defense in depth.
func TestAcceptance_ShippedAnchorIsProductionAndAcceptedInProd(t *testing.T) {
	anchor, cert := shippedTrust(t)
	stageBox(t, "prod", anchor, &cert)

	key, err := TrustedKey()
	if err != nil {
		t.Fatalf("prod box REJECTED the shipped anchor — the repo must ship a PRODUCTION anchor, not a dev key (there is no dev anchor): %v", err)
	}
	if key == nil {
		t.Fatal("TrustedKey returned a nil key with no error — verification would be skipped")
	}

	// Defense in depth: even with a production anchor shipped, an entry signed by a
	// dev key must still be refused in prod — it must not verify or install.
	_, releasePriv := signing.DeriveDevKey(signing.DevReleaseSeed)
	reg := hermeticApp(t, "acceptapp", releasePriv)
	appsDir := t.TempDir()
	if err := InstallFromRegistry(context.Background(), reg, "acceptapp", "1.0", appsDir); err == nil {
		t.Fatal("dev-key-signed entry was INSTALLED in prod")
	}
	assertNothingInstalled(t, appsDir)
}

// assertNothingInstalled fails if a refused install left anything behind. A gate
// that rejects but still writes to disk is not a gate.
func assertNothingInstalled(t *testing.T, appsDir string) {
	t.Helper()
	entries, err := os.ReadDir(appsDir)
	if err != nil {
		t.Fatalf("read apps dir: %v", err)
	}
	if len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("refused install still wrote to the apps dir: %v", names)
	}
}
