package appnet

// registry_signing_test.go — Ed25519 publisher signature tests (REGISTRY-SIGN-01).
//
// Trust model: a publisher holds an Ed25519 private key; the OS is configured
// with the matching public key (VULOS_REGISTRY_PUBKEY). Every registry entry
// must carry a valid signature before install proceeds. When no pubkey is set
// (self-host/standalone), verification is skipped entirely.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"strings"
	"testing"
)

// generateTestKey creates a fresh Ed25519 keypair for test use.
func generateTestKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate Ed25519 key: %v", err)
	}
	return pub, priv
}

// TestRegistrySignAndVerify confirms the happy path: sign an entry, verify OK.
func TestRegistrySignAndVerify(t *testing.T) {
	pub, priv := generateTestKey(t)

	entry := &RegistryEntry{
		Name:   "Test App",
		Vetted: true,
		Type:   "web",
		Author: "Test Publisher",
		Versions: map[string]*VersionRecipe{
			"1.0": {Install: "apt-get install -y testapp", Command: "testapp", Port: 8080},
		},
	}

	if err := SignEntry(entry, priv); err != nil {
		t.Fatalf("SignEntry: %v", err)
	}
	if entry.Signature == "" {
		t.Fatal("expected non-empty Signature after signing")
	}

	if err := VerifyEntrySignature(entry, pub); err != nil {
		t.Fatalf("VerifyEntrySignature: %v", err)
	}
}

// TestRegistryVerifyRejectsAlteredEntry confirms that tampering with any field
// after signing causes verification to fail.
func TestRegistryVerifyRejectsAlteredEntry(t *testing.T) {
	pub, priv := generateTestKey(t)

	entry := &RegistryEntry{
		Name:   "Legit App",
		Vetted: true,
		Type:   "web",
		Versions: map[string]*VersionRecipe{
			"1.0": {Install: "apt-get install -y legitapp", Command: "legitapp"},
		},
	}

	if err := SignEntry(entry, priv); err != nil {
		t.Fatalf("SignEntry: %v", err)
	}

	// Tamper: change the Name field after signing.
	entry.Name = "Malicious App"

	err := VerifyEntrySignature(entry, pub)
	if err == nil {
		t.Fatal("VerifyEntrySignature should have failed on tampered entry — got nil")
	}
	t.Logf("correctly rejected tampered entry: %v", err)
}

// TestRegistryVerifyRejectsWrongKey confirms that a signature from a different
// key does not verify against the configured public key.
func TestRegistryVerifyRejectsWrongKey(t *testing.T) {
	_, priv1 := generateTestKey(t)
	pub2, _ := generateTestKey(t) // different pair — pub2 won't match priv1's sig

	entry := &RegistryEntry{
		Name:     "App",
		Vetted:   true,
		Versions: map[string]*VersionRecipe{"1.0": {Install: "apt-get install app"}},
	}

	if err := SignEntry(entry, priv1); err != nil {
		t.Fatalf("SignEntry: %v", err)
	}

	err := VerifyEntrySignature(entry, pub2)
	if err == nil {
		t.Fatal("VerifyEntrySignature should have failed for mismatched key — got nil")
	}
	t.Logf("correctly rejected wrong-key signature: %v", err)
}

// TestRegistryVerifyNoPubkeySkips confirms that when no public key is configured
// (self-host / standalone), verification is skipped for any entry.
func TestRegistryVerifyNoPubkeySkips(t *testing.T) {
	entry := &RegistryEntry{
		Name:     "Self-Hosted App",
		Vetted:   true,
		Versions: map[string]*VersionRecipe{"1.0": {Install: "apt-get install myapp"}},
		// Signature intentionally absent — self-host mode must accept this.
	}

	if err := VerifyEntrySignature(entry, nil); err != nil {
		t.Fatalf("expected nil (skip) for nil pubkey, got: %v", err)
	}
}

// TestRegistryVerifyRejectsMissingSignatureWhenPubkeySet confirms fail-closed:
// if VULOS_REGISTRY_PUBKEY is configured (pubkey non-nil) and the entry has
// no signature, verification must fail.
func TestRegistryVerifyRejectsMissingSignatureWhenPubkeySet(t *testing.T) {
	pub, _ := generateTestKey(t)

	entry := &RegistryEntry{
		Name:     "Unsigned App",
		Vetted:   true,
		Versions: map[string]*VersionRecipe{"1.0": {Install: "apt-get install unsigned"}},
		// No Signature field.
	}

	err := VerifyEntrySignature(entry, pub)
	if err == nil {
		t.Fatal("expected error for unsigned entry when pubkey is configured — got nil")
	}
	if !strings.Contains(err.Error(), "signature") {
		t.Errorf("expected 'signature' in error, got: %v", err)
	}
	t.Logf("correctly rejected unsigned entry: %v", err)
}

// TestInstallFromRegistryRejectsUnsignedEntry confirms the install path calls
// VerifyEntrySignature and refuses to proceed when VULOS_REGISTRY_PUBKEY is set
// but the entry has no signature.
func TestInstallFromRegistryRejectsUnsignedEntry(t *testing.T) {
	pub, _ := generateTestKey(t)

	// Inject the public key via env var so LoadTrustedPublicKey picks it up.
	pubB64 := base64.StdEncoding.EncodeToString([]byte(pub))
	t.Setenv(envRegistryPubKey, pubB64)

	reg := &Registry{
		Apps: map[string]*RegistryEntry{
			"myapp": {
				Name:   "My App",
				Vetted: true,
				Type:   "web",
				Versions: map[string]*VersionRecipe{
					"1.0": {
						Install: "apt-get install -y myapp",
						Command: "myapp",
						Port:    8080,
					},
				},
				// No Signature — must be rejected.
			},
		},
	}

	dir := t.TempDir()
	err := InstallFromRegistry(context.Background(), reg, "myapp", "1.0", dir)
	if err == nil {
		t.Fatal("expected error for unsigned entry when pubkey configured — got nil")
	}
	if !strings.Contains(err.Error(), "signature") {
		t.Errorf("expected 'signature' in error, got: %v", err)
	}
	t.Logf("correctly rejected unsigned install: %v", err)
}

// TestInstallFromRegistryAcceptsSignedEntry confirms the happy path in the
// install gate: a properly signed entry (+ pubkey in env) clears the signature
// check and only fails for non-security reasons (apt-get not available in CI).
func TestInstallFromRegistryAcceptsSignedEntry(t *testing.T) {
	pub, priv := generateTestKey(t)

	entry := &RegistryEntry{
		Name:   "Signed App",
		Vetted: true,
		Type:   "web",
		Versions: map[string]*VersionRecipe{
			"1.0": {
				Install: "apt-get install -y signedapp",
				Command: "signedapp",
				Port:    8080,
			},
		},
	}
	if err := SignEntry(entry, priv); err != nil {
		t.Fatalf("SignEntry: %v", err)
	}

	pubB64 := base64.StdEncoding.EncodeToString([]byte(pub))
	t.Setenv(envRegistryPubKey, pubB64)

	reg := &Registry{Apps: map[string]*RegistryEntry{"signedapp": entry}}
	dir := t.TempDir()
	err := InstallFromRegistry(context.Background(), reg, "signedapp", "1.0", dir)
	// The install may fail because apt-get is unavailable in test/CI —
	// but it must NOT be a signature-related rejection.
	if err != nil {
		if strings.Contains(err.Error(), "signature") {
			t.Errorf("unexpected signature-related error (signature gate should have passed): %v", err)
		} else {
			t.Logf("signature gate passed; install failed for non-security reason (expected in CI): %v", err)
		}
	} else {
		t.Log("install succeeded (signature gate + install both passed)")
	}
}
