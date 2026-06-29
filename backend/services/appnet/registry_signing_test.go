package appnet

// registry_signing_test.go — Ed25519 publisher signature tests (REGISTRY-SIGN-01, C1).
//
// Trust model post-C1: DEFAULT-CLOSED.
//   - A trust anchor (file or env) is required for installs.
//   - VULOS_REGISTRY_INSECURE=1 is the only way to skip verification.
//   - Set-but-malformed keys are fatal (tested via parseEd25519PubKey).
//   - Signatures bind appID so re-keying is detected (M4).

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

// withInsecureRegistry sets VULOS_REGISTRY_INSECURE=1 for the test duration.
// Used by tests that need to bypass signature checks (e.g. testing pipe-to-shell
// rejection which is independent of signing).
func withInsecureRegistry(t *testing.T) {
	t.Helper()
	t.Setenv(envRegistryInsecure, "1")
}

// withTrustAnchor injects a trust anchor via VULOS_REGISTRY_PUBKEY.
func withTrustAnchor(t *testing.T, pub ed25519.PublicKey) {
	t.Helper()
	t.Setenv(envRegistryPubKey, base64.StdEncoding.EncodeToString([]byte(pub)))
	t.Setenv(envRegistryInsecure, "") // ensure insecure mode is off
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

	if err := SignEntry(entry, "testapp", priv); err != nil {
		t.Fatalf("SignEntry: %v", err)
	}
	if entry.Signature == "" {
		t.Fatal("expected non-empty Signature after signing")
	}

	if err := VerifyEntrySignature(entry, "testapp", pub); err != nil {
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

	if err := SignEntry(entry, "legitapp", priv); err != nil {
		t.Fatalf("SignEntry: %v", err)
	}

	// Tamper: change the Name field after signing.
	entry.Name = "Malicious App"

	err := VerifyEntrySignature(entry, "legitapp", pub)
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

	if err := SignEntry(entry, "app", priv1); err != nil {
		t.Fatalf("SignEntry: %v", err)
	}

	err := VerifyEntrySignature(entry, "app", pub2)
	if err == nil {
		t.Fatal("VerifyEntrySignature should have failed for mismatched key — got nil")
	}
	t.Logf("correctly rejected wrong-key signature: %v", err)
}

// TestRegistryVerifyRejectsNilPubkeyWithoutInsecureFlag confirms DEFAULT-CLOSED:
// a nil pubkey (no trust anchor) is rejected unless VULOS_REGISTRY_INSECURE=1.
func TestRegistryVerifyRejectsNilPubkeyWithoutInsecureFlag(t *testing.T) {
	// Ensure insecure mode is off.
	t.Setenv(envRegistryInsecure, "")

	entry := &RegistryEntry{
		Name:     "Any App",
		Vetted:   true,
		Versions: map[string]*VersionRecipe{"1.0": {Install: "apt-get install myapp"}},
	}

	err := VerifyEntrySignature(entry, "myapp", nil)
	if err == nil {
		t.Fatal("expected error when pubkey is nil and INSECURE flag is off — got nil (fail-open bug)")
	}
	t.Logf("correctly rejected: %v", err)
}

// TestRegistryVerifyInsecureModeSkips confirms that nil pubkey is accepted ONLY
// when VULOS_REGISTRY_INSECURE=1 is explicitly set.
func TestRegistryVerifyInsecureModeSkips(t *testing.T) {
	withInsecureRegistry(t)

	entry := &RegistryEntry{
		Name:     "Self-Hosted App",
		Vetted:   true,
		Versions: map[string]*VersionRecipe{"1.0": {Install: "apt-get install myapp"}},
		// No Signature — allowed only in insecure mode.
	}

	if err := VerifyEntrySignature(entry, "myapp", nil); err != nil {
		t.Fatalf("expected nil (insecure skip) for nil pubkey with INSECURE=1, got: %v", err)
	}
}

// TestRegistryVerifyRejectsMissingSignatureWhenPubkeySet confirms fail-closed:
// if a trust anchor is configured (pubkey non-nil) and the entry has no
// signature, verification must fail.
func TestRegistryVerifyRejectsMissingSignatureWhenPubkeySet(t *testing.T) {
	pub, _ := generateTestKey(t)

	entry := &RegistryEntry{
		Name:     "Unsigned App",
		Vetted:   true,
		Versions: map[string]*VersionRecipe{"1.0": {Install: "apt-get install unsigned"}},
		// No Signature field.
	}

	err := VerifyEntrySignature(entry, "unsignedapp", pub)
	if err == nil {
		t.Fatal("expected error for unsigned entry when pubkey is configured — got nil")
	}
	if !strings.Contains(err.Error(), "signature") {
		t.Errorf("expected 'signature' in error, got: %v", err)
	}
	t.Logf("correctly rejected unsigned entry: %v", err)
}

// TestRegistryVerifyRejectsRekeyedEntry confirms M4: a valid signature for
// "appA" is rejected when the entry is placed under "appB" (re-keying attack).
func TestRegistryVerifyRejectsRekeyedEntry(t *testing.T) {
	pub, priv := generateTestKey(t)

	entry := &RegistryEntry{
		Name:     "App A",
		Vetted:   true,
		Versions: map[string]*VersionRecipe{"1.0": {Install: "apt-get install appa"}},
	}
	if err := SignEntry(entry, "appa", priv); err != nil {
		t.Fatalf("SignEntry: %v", err)
	}

	// Verify with the CORRECT appID passes.
	if err := VerifyEntrySignature(entry, "appa", pub); err != nil {
		t.Fatalf("expected valid sig for appa: %v", err)
	}

	// Verify with a DIFFERENT appID must fail (re-key protection).
	err := VerifyEntrySignature(entry, "appb", pub)
	if err == nil {
		t.Fatal("expected error when verifying appa signature under appb — re-keying must be rejected")
	}
	t.Logf("correctly rejected re-keyed entry: %v", err)
}

// TestInstallFromRegistryRejectsUnsignedEntryByDefault confirms the install
// path rejects unsigned entries when no trust anchor is configured and
// VULOS_REGISTRY_INSECURE is not set.
func TestInstallFromRegistryRejectsUnsignedEntryByDefault(t *testing.T) {
	// No VULOS_REGISTRY_PUBKEY, no trust-anchor file, no INSECURE flag.
	t.Setenv(envRegistryPubKey, "")
	t.Setenv(envRegistryInsecure, "")

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
				// No Signature — must be rejected by default-closed policy.
			},
		},
	}

	dir := t.TempDir()
	err := InstallFromRegistry(context.Background(), reg, "myapp", "1.0", dir)
	if err == nil {
		t.Fatal("expected error for unsigned entry with no trust anchor — got nil (fail-open)")
	}
	t.Logf("correctly rejected: %v", err)
}

// TestInstallFromRegistryRejectsUnsignedWhenPubkeySet confirms the install
// path rejects unsigned entries when a trust anchor is configured.
func TestInstallFromRegistryRejectsUnsignedWhenPubkeySet(t *testing.T) {
	pub, _ := generateTestKey(t)
	withTrustAnchor(t, pub)

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
	withTrustAnchor(t, pub)

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
	if err := SignEntry(entry, "signedapp", priv); err != nil {
		t.Fatalf("SignEntry: %v", err)
	}

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

// TestInstallFromRegistryEnforcesMinVersion confirms downgrade protection (M4):
// requesting a version below MinVersion is refused.
func TestInstallFromRegistryEnforcesMinVersion(t *testing.T) {
	withInsecureRegistry(t)

	reg := &Registry{
		Apps: map[string]*RegistryEntry{
			"myapp": {
				Name:       "My App",
				Vetted:     true,
				MinVersion: "2.0",
				Versions: map[string]*VersionRecipe{
					"1.0": {Install: "apt-get install -y myapp", Command: "myapp", Port: 8080},
					"2.0": {Install: "apt-get install -y myapp", Command: "myapp", Port: 8080},
				},
			},
		},
	}

	dir := t.TempDir()
	err := InstallFromRegistry(context.Background(), reg, "myapp", "1.0", dir)
	if err == nil {
		t.Fatal("expected downgrade error for version 1.0 with MinVersion=2.0 — got nil")
	}
	if !strings.Contains(err.Error(), "minimum") && !strings.Contains(err.Error(), "downgrade") {
		t.Errorf("expected 'minimum'/'downgrade' in error, got: %v", err)
	}
	t.Logf("correctly rejected downgrade: %v", err)
}

// TestParseEd25519PubKey_malformed confirms that a malformed key returns an
// error (not nil), so callers that treat errors as fatal are exercised.
func TestParseEd25519PubKey_malformed(t *testing.T) {
	cases := []struct {
		name string
		s    string
	}{
		{"not_base64", "not-valid-base64!!!"},
		{"too_short", base64.StdEncoding.EncodeToString([]byte("tooshort"))},
		{"too_long", base64.StdEncoding.EncodeToString(make([]byte, 64))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseEd25519PubKey(tc.s)
			if err == nil {
				t.Errorf("expected error for malformed key %q, got nil", tc.s)
			} else {
				t.Logf("correctly rejected: %v", err)
			}
		})
	}
}
