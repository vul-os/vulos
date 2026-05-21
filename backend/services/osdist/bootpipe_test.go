package osdist

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"testing"

	"vulos/backend/services/signing"
)

// ─── Helpers ──────────────────────────────────────────────────────────────────

// sampleBootPipe returns a fully-populated BootPipeManifest for tests.
func sampleBootPipe() BootPipeManifest {
	return BootPipeManifest{
		Version: 1,
		Channel: "stable",
		Artifacts: []ArtifactEntry{
			{
				Name:      "kernel",
				URL:       "http://boot.vulos.org/v08/kernel",
				SigURL:    "http://boot.vulos.org/v08/kernel.sig",
				Algorithm: "ed25519",
			},
			{
				Name:      "initramfs",
				URL:       "http://boot.vulos.org/v08/initramfs.img",
				SigURL:    "http://boot.vulos.org/v08/initramfs.img.sig",
				Algorithm: "ed25519",
			},
		},
	}
}

// signBootPipeHelper signs m and returns raw sig bytes.
func signBootPipeHelper(t *testing.T, m *BootPipeManifest, priv ed25519.PrivateKey) []byte {
	t.Helper()
	sig, err := SignBootPipe(m, priv)
	if err != nil {
		t.Fatalf("SignBootPipe: %v", err)
	}
	return sig
}

// marshalBootPipe marshals m to JSON bytes.
func marshalBootPipe(t *testing.T, m BootPipeManifest) []byte {
	t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("json.Marshal BootPipeManifest: %v", err)
	}
	return b
}

// ─── Canonical byte-stability ─────────────────────────────────────────────────

// NETB02_TestBootPipe_Canonical_ByteStability verifies that Canonical produces
// identical bytes on repeated calls regardless of map-iteration order.
func NETB02_TestBootPipe_Canonical_ByteStability(t *testing.T) {
	m := sampleBootPipe()
	first, err := m.Canonical()
	if err != nil {
		t.Fatalf("Canonical: %v", err)
	}
	if len(first) == 0 {
		t.Fatal("Canonical returned empty bytes")
	}
	for i := 0; i < 20; i++ {
		got, err := m.Canonical()
		if err != nil {
			t.Fatalf("iteration %d: Canonical: %v", i, err)
		}
		if string(first) != string(got) {
			t.Fatalf("iteration %d: canonical bytes differ:\n  first: %s\n  got:   %s", i, first, got)
		}
	}
}

// NETB02_TestBootPipe_Canonical_SortedKeys verifies that top-level keys appear
// in lexicographic order in the canonical output.
func NETB02_TestBootPipe_Canonical_SortedKeys(t *testing.T) {
	m := sampleBootPipe()
	canonical, err := m.Canonical()
	if err != nil {
		t.Fatalf("Canonical: %v", err)
	}
	s := string(canonical)
	// "artifacts" < "channel" < "version" lexicographically.
	pairs := [][2]string{
		{"\"artifacts\"", "\"channel\""},
		{"\"channel\"", "\"version\""},
	}
	for _, p := range pairs {
		ia := indexOf(s, p[0])
		ib := indexOf(s, p[1])
		if ia < 0 || ib < 0 {
			t.Errorf("key %q or %q not found in canonical output: %s", p[0], p[1], s)
			continue
		}
		if ia >= ib {
			t.Errorf("key %q should precede %q in canonical output:\n  %s", p[0], p[1], s)
		}
	}
}

func indexOf(s, sub string) int {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// ─── SignBootPipe ─────────────────────────────────────────────────────────────

// NETB02_TestSignBootPipe_ProducesEd25519Sig verifies that SignBootPipe returns
// a 64-byte signature for a valid manifest.
func NETB02_TestSignBootPipe_ProducesEd25519Sig(t *testing.T) {
	_, priv := genKeyPair(t)
	m := sampleBootPipe()
	sig, err := SignBootPipe(&m, priv)
	if err != nil {
		t.Fatalf("SignBootPipe: %v", err)
	}
	if len(sig) != ed25519.SignatureSize {
		t.Fatalf("expected %d-byte signature, got %d", ed25519.SignatureSize, len(sig))
	}
}

// NETB02_TestSignBootPipe_InvalidManifest_Empty verifies that SignBootPipe
// rejects an empty artifacts list.
func NETB02_TestSignBootPipe_InvalidManifest_Empty(t *testing.T) {
	_, priv := genKeyPair(t)
	m := BootPipeManifest{Version: 1, Channel: "stable", Artifacts: nil}
	_, err := SignBootPipe(&m, priv)
	if !errors.Is(err, ErrBootPipeMalformed) {
		t.Fatalf("expected ErrBootPipeMalformed for empty artifacts, got: %v", err)
	}
}

// ─── ParseAndVerifyBootPipe – happy path ──────────────────────────────────────

// NETB02_TestParseAndVerifyBootPipe_HappyPath verifies the full sign→marshal→
// verify round-trip succeeds with matching key and valid manifest.
func NETB02_TestParseAndVerifyBootPipe_HappyPath(t *testing.T) {
	pub, priv := genKeyPair(t)
	m := sampleBootPipe()
	sig := signBootPipeHelper(t, &m, priv)
	data := marshalBootPipe(t, m)

	got, err := ParseAndVerifyBootPipe(data, sig, pub)
	if err != nil {
		t.Fatalf("ParseAndVerifyBootPipe: %v", err)
	}
	if got == nil {
		t.Fatal("ParseAndVerifyBootPipe returned nil without error")
	}
	if got.Channel != m.Channel {
		t.Errorf("Channel: got %q want %q", got.Channel, m.Channel)
	}
	if len(got.Artifacts) != len(m.Artifacts) {
		t.Errorf("Artifacts len: got %d want %d", len(got.Artifacts), len(m.Artifacts))
	}
	if got.Artifacts[0].Name != "kernel" {
		t.Errorf("Artifacts[0].Name: got %q want %q", got.Artifacts[0].Name, "kernel")
	}
}

// NETB02_TestParseAndVerifyBootPipe_EdgeChannel verifies that a non-stable
// channel is accepted.
func NETB02_TestParseAndVerifyBootPipe_EdgeChannel(t *testing.T) {
	pub, priv := genKeyPair(t)
	m := sampleBootPipe()
	m.Channel = "edge"
	sig := signBootPipeHelper(t, &m, priv)
	data := marshalBootPipe(t, m)

	_, err := ParseAndVerifyBootPipe(data, sig, pub)
	if err != nil {
		t.Fatalf("ParseAndVerifyBootPipe edge channel: %v", err)
	}
}

// ─── ParseAndVerifyBootPipe – tamper rejection ────────────────────────────────

// NETB02_TestParseAndVerifyBootPipe_TamperData verifies that flipping a byte
// in the manifest JSON after signing causes ErrBootPipeBadSignature (or
// ErrBootPipeMalformed if the tampered bytes are no longer valid JSON).
func NETB02_TestParseAndVerifyBootPipe_TamperData(t *testing.T) {
	pub, priv := genKeyPair(t)
	m := sampleBootPipe()
	sig := signBootPipeHelper(t, &m, priv)
	data := marshalBootPipe(t, m)

	tampered := make([]byte, len(data))
	copy(tampered, data)
	tampered[len(tampered)/2] ^= 0xFF

	_, err := ParseAndVerifyBootPipe(tampered, sig, pub)
	if !errors.Is(err, ErrBootPipeBadSignature) && !errors.Is(err, ErrBootPipeMalformed) {
		t.Fatalf("expected ErrBootPipeBadSignature or ErrBootPipeMalformed, got: %v", err)
	}
}

// NETB02_TestParseAndVerifyBootPipe_TamperField verifies that changing a field
// value after signing causes ErrBootPipeBadSignature.
func NETB02_TestParseAndVerifyBootPipe_TamperField(t *testing.T) {
	pub, priv := genKeyPair(t)
	m := sampleBootPipe()
	sig := signBootPipeHelper(t, &m, priv)

	// Modify the channel to simulate an attacker changing the channel.
	tampered := m
	tampered.Channel = "edge"
	data := marshalBootPipe(t, tampered)

	_, err := ParseAndVerifyBootPipe(data, sig, pub)
	if !errors.Is(err, ErrBootPipeBadSignature) {
		t.Fatalf("expected ErrBootPipeBadSignature for tampered channel, got: %v", err)
	}
}

// NETB02_TestParseAndVerifyBootPipe_TamperArtifactURL verifies that changing
// an artifact URL after signing causes ErrBootPipeBadSignature.
func NETB02_TestParseAndVerifyBootPipe_TamperArtifactURL(t *testing.T) {
	pub, priv := genKeyPair(t)
	m := sampleBootPipe()
	sig := signBootPipeHelper(t, &m, priv)

	tampered := sampleBootPipe()
	tampered.Artifacts[0].URL = "http://evil.example.com/malicious-kernel"
	data := marshalBootPipe(t, tampered)

	_, err := ParseAndVerifyBootPipe(data, sig, pub)
	if !errors.Is(err, ErrBootPipeBadSignature) {
		t.Fatalf("expected ErrBootPipeBadSignature for tampered artifact URL, got: %v", err)
	}
}

// NETB02_TestParseAndVerifyBootPipe_TamperSig verifies that a corrupted
// signature causes ErrBootPipeBadSignature.
func NETB02_TestParseAndVerifyBootPipe_TamperSig(t *testing.T) {
	pub, priv := genKeyPair(t)
	m := sampleBootPipe()
	sig := signBootPipeHelper(t, &m, priv)
	data := marshalBootPipe(t, m)

	tampered := make([]byte, len(sig))
	copy(tampered, sig)
	tampered[len(tampered)/2] ^= 0xFF

	_, err := ParseAndVerifyBootPipe(data, tampered, pub)
	if !errors.Is(err, ErrBootPipeBadSignature) {
		t.Fatalf("expected ErrBootPipeBadSignature for tampered sig, got: %v", err)
	}
}

// NETB02_TestParseAndVerifyBootPipe_WrongKey verifies that a signature produced
// by one key does not verify against a different public key.
func NETB02_TestParseAndVerifyBootPipe_WrongKey(t *testing.T) {
	_, priv1 := genKeyPair(t)
	pub2, _ := genKeyPair(t)

	m := sampleBootPipe()
	sig := signBootPipeHelper(t, &m, priv1)
	data := marshalBootPipe(t, m)

	_, err := ParseAndVerifyBootPipe(data, sig, pub2)
	if !errors.Is(err, ErrBootPipeBadSignature) {
		t.Fatalf("expected ErrBootPipeBadSignature for wrong key, got: %v", err)
	}
}

// ─── ParseAndVerifyBootPipe – malformed input ─────────────────────────────────

// NETB02_TestParseAndVerifyBootPipe_MalformedJSON verifies that invalid JSON
// returns ErrBootPipeMalformed.
func NETB02_TestParseAndVerifyBootPipe_MalformedJSON(t *testing.T) {
	pub, _ := genKeyPair(t)
	sig := make([]byte, ed25519.SignatureSize)
	_, err := ParseAndVerifyBootPipe([]byte(`{not valid json}`), sig, pub)
	if !errors.Is(err, ErrBootPipeMalformed) {
		t.Fatalf("expected ErrBootPipeMalformed for invalid JSON, got: %v", err)
	}
}

// NETB02_TestParseAndVerifyBootPipe_EmptyChannel verifies that an empty channel
// returns ErrBootPipeMalformed.
func NETB02_TestParseAndVerifyBootPipe_EmptyChannel(t *testing.T) {
	pub, _ := genKeyPair(t)
	sig := make([]byte, ed25519.SignatureSize)
	data := []byte(`{"version":1,"channel":"","artifacts":[{"name":"kernel","url":"http://x/k","sig_url":"http://x/k.sig","algorithm":"ed25519"}]}`)
	_, err := ParseAndVerifyBootPipe(data, sig, pub)
	if !errors.Is(err, ErrBootPipeMalformed) {
		t.Fatalf("expected ErrBootPipeMalformed for empty channel, got: %v", err)
	}
}

// NETB02_TestParseAndVerifyBootPipe_EmptyArtifacts verifies that an empty
// artifacts array returns ErrBootPipeMalformed.
func NETB02_TestParseAndVerifyBootPipe_EmptyArtifacts(t *testing.T) {
	pub, _ := genKeyPair(t)
	sig := make([]byte, ed25519.SignatureSize)
	data := []byte(`{"version":1,"channel":"stable","artifacts":[]}`)
	_, err := ParseAndVerifyBootPipe(data, sig, pub)
	if !errors.Is(err, ErrBootPipeMalformed) {
		t.Fatalf("expected ErrBootPipeMalformed for empty artifacts, got: %v", err)
	}
}

// NETB02_TestParseAndVerifyBootPipe_MissingArtifactName verifies that an
// artifact with an empty name returns ErrBootPipeMalformed.
func NETB02_TestParseAndVerifyBootPipe_MissingArtifactName(t *testing.T) {
	pub, _ := genKeyPair(t)
	sig := make([]byte, ed25519.SignatureSize)
	data := []byte(`{"version":1,"channel":"stable","artifacts":[{"name":"","url":"http://x/k","sig_url":"http://x/k.sig","algorithm":"ed25519"}]}`)
	_, err := ParseAndVerifyBootPipe(data, sig, pub)
	if !errors.Is(err, ErrBootPipeMalformed) {
		t.Fatalf("expected ErrBootPipeMalformed for missing artifact name, got: %v", err)
	}
}

// NETB02_TestParseAndVerifyBootPipe_MissingURL verifies that an artifact with
// an empty URL returns ErrBootPipeMalformed.
func NETB02_TestParseAndVerifyBootPipe_MissingURL(t *testing.T) {
	pub, _ := genKeyPair(t)
	sig := make([]byte, ed25519.SignatureSize)
	data := []byte(`{"version":1,"channel":"stable","artifacts":[{"name":"kernel","url":"","sig_url":"http://x/k.sig","algorithm":"ed25519"}]}`)
	_, err := ParseAndVerifyBootPipe(data, sig, pub)
	if !errors.Is(err, ErrBootPipeMalformed) {
		t.Fatalf("expected ErrBootPipeMalformed for missing URL, got: %v", err)
	}
}

// NETB02_TestParseAndVerifyBootPipe_MissingSigURL verifies that an artifact
// with an empty sig_url returns ErrBootPipeMalformed.
func NETB02_TestParseAndVerifyBootPipe_MissingSigURL(t *testing.T) {
	pub, _ := genKeyPair(t)
	sig := make([]byte, ed25519.SignatureSize)
	data := []byte(`{"version":1,"channel":"stable","artifacts":[{"name":"kernel","url":"http://x/k","sig_url":"","algorithm":"ed25519"}]}`)
	_, err := ParseAndVerifyBootPipe(data, sig, pub)
	if !errors.Is(err, ErrBootPipeMalformed) {
		t.Fatalf("expected ErrBootPipeMalformed for missing sig_url, got: %v", err)
	}
}

// NETB02_TestParseAndVerifyBootPipe_UnsupportedAlgorithm verifies that an
// artifact with an unsupported algorithm returns ErrBootPipeMalformed.
func NETB02_TestParseAndVerifyBootPipe_UnsupportedAlgorithm(t *testing.T) {
	pub, _ := genKeyPair(t)
	sig := make([]byte, ed25519.SignatureSize)
	data := []byte(`{"version":1,"channel":"stable","artifacts":[{"name":"kernel","url":"http://x/k","sig_url":"http://x/k.sig","algorithm":"rsa"}]}`)
	_, err := ParseAndVerifyBootPipe(data, sig, pub)
	if !errors.Is(err, ErrBootPipeMalformed) {
		t.Fatalf("expected ErrBootPipeMalformed for unsupported algorithm, got: %v", err)
	}
}

// NETB02_TestParseAndVerifyBootPipe_DuplicateArtifactName verifies that
// duplicate artifact names are rejected.
func NETB02_TestParseAndVerifyBootPipe_DuplicateArtifactName(t *testing.T) {
	pub, _ := genKeyPair(t)
	sig := make([]byte, ed25519.SignatureSize)
	data := []byte(`{"version":1,"channel":"stable","artifacts":[` +
		`{"name":"kernel","url":"http://x/k","sig_url":"http://x/k.sig","algorithm":"ed25519"},` +
		`{"name":"kernel","url":"http://x/k2","sig_url":"http://x/k2.sig","algorithm":"ed25519"}` +
		`]}`)
	_, err := ParseAndVerifyBootPipe(data, sig, pub)
	if !errors.Is(err, ErrBootPipeMalformed) {
		t.Fatalf("expected ErrBootPipeMalformed for duplicate artifact names, got: %v", err)
	}
}

// NETB02_TestParseAndVerifyBootPipe_WrongVersion verifies that version != 1
// returns ErrBootPipeMalformed.
func NETB02_TestParseAndVerifyBootPipe_WrongVersion(t *testing.T) {
	pub, _ := genKeyPair(t)
	sig := make([]byte, ed25519.SignatureSize)
	data := []byte(`{"version":2,"channel":"stable","artifacts":[{"name":"kernel","url":"http://x/k","sig_url":"http://x/k.sig","algorithm":"ed25519"}]}`)
	_, err := ParseAndVerifyBootPipe(data, sig, pub)
	if !errors.Is(err, ErrBootPipeMalformed) {
		t.Fatalf("expected ErrBootPipeMalformed for version=2, got: %v", err)
	}
}

// ─── Detached .sig round-trip ─────────────────────────────────────────────────

// NETB02_TestBootPipe_DetachedSigRoundTrip verifies that a signature produced
// by SignBootPipe can be serialised via signing.MarshalSig, parsed back via
// signing.ParseSig, and still verifies correctly.
func NETB02_TestBootPipe_DetachedSigRoundTrip(t *testing.T) {
	pub, priv := genKeyPair(t)
	m := sampleBootPipe()
	rawSig := signBootPipeHelper(t, &m, priv)

	// Serialise to .sig format.
	sigFile, err := signing.MarshalSig(signing.Signature{
		Algorithm: "ed25519",
		KeyID:     "test-key-2026",
		SigBytes:  rawSig,
	})
	if err != nil {
		t.Fatalf("MarshalSig: %v", err)
	}

	// Parse back.
	parsed, err := signing.ParseSig(sigFile)
	if err != nil {
		t.Fatalf("ParseSig: %v", err)
	}

	// Verify using the parsed sig bytes.
	data := marshalBootPipe(t, m)
	got, err := ParseAndVerifyBootPipe(data, parsed.SigBytes, pub)
	if err != nil {
		t.Fatalf("ParseAndVerifyBootPipe after sig round-trip: %v", err)
	}
	if got.Channel != m.Channel {
		t.Errorf("Channel: got %q want %q", got.Channel, m.Channel)
	}
}

// ─── Standard test entry points ──────────────────────────────────────────────

func TestBootPipe_Canonical_ByteStability(t *testing.T) {
	NETB02_TestBootPipe_Canonical_ByteStability(t)
}
func TestBootPipe_Canonical_SortedKeys(t *testing.T) {
	NETB02_TestBootPipe_Canonical_SortedKeys(t)
}
func TestSignBootPipe_ProducesEd25519Sig(t *testing.T) {
	NETB02_TestSignBootPipe_ProducesEd25519Sig(t)
}
func TestSignBootPipe_InvalidManifest_Empty(t *testing.T) {
	NETB02_TestSignBootPipe_InvalidManifest_Empty(t)
}
func TestParseAndVerifyBootPipe_HappyPath(t *testing.T) {
	NETB02_TestParseAndVerifyBootPipe_HappyPath(t)
}
func TestParseAndVerifyBootPipe_EdgeChannel(t *testing.T) {
	NETB02_TestParseAndVerifyBootPipe_EdgeChannel(t)
}
func TestParseAndVerifyBootPipe_TamperData(t *testing.T) {
	NETB02_TestParseAndVerifyBootPipe_TamperData(t)
}
func TestParseAndVerifyBootPipe_TamperField(t *testing.T) {
	NETB02_TestParseAndVerifyBootPipe_TamperField(t)
}
func TestParseAndVerifyBootPipe_TamperArtifactURL(t *testing.T) {
	NETB02_TestParseAndVerifyBootPipe_TamperArtifactURL(t)
}
func TestParseAndVerifyBootPipe_TamperSig(t *testing.T) {
	NETB02_TestParseAndVerifyBootPipe_TamperSig(t)
}
func TestParseAndVerifyBootPipe_WrongKey(t *testing.T) {
	NETB02_TestParseAndVerifyBootPipe_WrongKey(t)
}
func TestParseAndVerifyBootPipe_MalformedJSON(t *testing.T) {
	NETB02_TestParseAndVerifyBootPipe_MalformedJSON(t)
}
func TestParseAndVerifyBootPipe_EmptyChannel(t *testing.T) {
	NETB02_TestParseAndVerifyBootPipe_EmptyChannel(t)
}
func TestParseAndVerifyBootPipe_EmptyArtifacts(t *testing.T) {
	NETB02_TestParseAndVerifyBootPipe_EmptyArtifacts(t)
}
func TestParseAndVerifyBootPipe_MissingArtifactName(t *testing.T) {
	NETB02_TestParseAndVerifyBootPipe_MissingArtifactName(t)
}
func TestParseAndVerifyBootPipe_MissingURL(t *testing.T) {
	NETB02_TestParseAndVerifyBootPipe_MissingURL(t)
}
func TestParseAndVerifyBootPipe_MissingSigURL(t *testing.T) {
	NETB02_TestParseAndVerifyBootPipe_MissingSigURL(t)
}
func TestParseAndVerifyBootPipe_UnsupportedAlgorithm(t *testing.T) {
	NETB02_TestParseAndVerifyBootPipe_UnsupportedAlgorithm(t)
}
func TestParseAndVerifyBootPipe_DuplicateArtifactName(t *testing.T) {
	NETB02_TestParseAndVerifyBootPipe_DuplicateArtifactName(t)
}
func TestParseAndVerifyBootPipe_WrongVersion(t *testing.T) {
	NETB02_TestParseAndVerifyBootPipe_WrongVersion(t)
}
func TestBootPipe_DetachedSigRoundTrip(t *testing.T) {
	NETB02_TestBootPipe_DetachedSigRoundTrip(t)
}
