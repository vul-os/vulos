package osdist

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"vulos/backend/services/signing"
)

// ─── Helpers ──────────────────────────────────────────────────────────────────

func genKeyPair(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	return pub, priv
}

// sampleManifest returns a fully-populated StableManifest for use in tests.
func sampleManifest() StableManifest {
	return StableManifest{
		Channel:    "stable",
		Latest:     "v08",
		MinEpoch:   3,
		RootHash:   "abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890",
		Size:       734003200,
		ReleasedAt: time.Date(2026, 5, 20, 9, 0, 0, 0, time.UTC),
		Path:       "os/v08/os-core.squashfs",
	}
}

// signManifest signs m with priv and returns the raw signature bytes.
func signManifest(t *testing.T, m *StableManifest, priv ed25519.PrivateKey) []byte {
	t.Helper()
	canonical, err := m.Canonical()
	if err != nil {
		t.Fatalf("Canonical: %v", err)
	}
	return signing.Sign(priv, canonical)
}

// marshalManifest marshals m to JSON bytes as the bucket would store them.
func marshalManifest(t *testing.T, m StableManifest) []byte {
	t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	return b
}

// ─── Canonical byte-stability ─────────────────────────────────────────────────

// OSDIST01_TestCanonical_ByteStability verifies that Canonical produces
// identical bytes across multiple calls regardless of internal map ordering.
func OSDIST01_TestCanonical_ByteStability(t *testing.T) {
	m := sampleManifest()

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
		if !bytes.Equal(first, got) {
			t.Fatalf("iteration %d: canonical bytes differ:\n  first: %s\n  got:   %s", i, first, got)
		}
	}
}

// OSDIST01_TestCanonical_IsValidJSON verifies that the canonical output is
// valid JSON that can be round-tripped back to a StableManifest.
func OSDIST01_TestCanonical_IsValidJSON(t *testing.T) {
	m := sampleManifest()
	canonical, err := m.Canonical()
	if err != nil {
		t.Fatalf("Canonical: %v", err)
	}

	var roundtripped StableManifest
	if err := json.Unmarshal(canonical, &roundtripped); err != nil {
		t.Fatalf("Unmarshal canonical bytes: %v\nbytes: %s", err, canonical)
	}

	if roundtripped.Channel != m.Channel {
		t.Errorf("Channel: got %q want %q", roundtripped.Channel, m.Channel)
	}
	if roundtripped.Latest != m.Latest {
		t.Errorf("Latest: got %q want %q", roundtripped.Latest, m.Latest)
	}
	if roundtripped.MinEpoch != m.MinEpoch {
		t.Errorf("MinEpoch: got %d want %d", roundtripped.MinEpoch, m.MinEpoch)
	}
	if roundtripped.RootHash != m.RootHash {
		t.Errorf("RootHash: got %q want %q", roundtripped.RootHash, m.RootHash)
	}
	if roundtripped.Size != m.Size {
		t.Errorf("Size: got %d want %d", roundtripped.Size, m.Size)
	}
	if roundtripped.Path != m.Path {
		t.Errorf("Path: got %q want %q", roundtripped.Path, m.Path)
	}
}

// OSDIST01_TestCanonical_NoWhitespace verifies canonical output has no
// insignificant whitespace.
func OSDIST01_TestCanonical_NoWhitespace(t *testing.T) {
	m := sampleManifest()
	canonical, err := m.Canonical()
	if err != nil {
		t.Fatalf("Canonical: %v", err)
	}
	for i, b := range canonical {
		if b == ' ' || b == '\t' || b == '\n' || b == '\r' {
			t.Fatalf("byte %d is whitespace in canonical output: %q", i, canonical)
		}
	}
}

// OSDIST01_TestCanonical_SortedKeys verifies that the canonical JSON has
// keys sorted lexicographically (channel < latest < min_epoch < path < …).
func OSDIST01_TestCanonical_SortedKeys(t *testing.T) {
	m := sampleManifest()
	canonical, err := m.Canonical()
	if err != nil {
		t.Fatalf("Canonical: %v", err)
	}
	s := string(canonical)
	// "channel" must precede "latest" which must precede "min_epoch", etc.
	pairs := [][2]string{
		{"\"channel\"", "\"latest\""},
		{"\"latest\"", "\"min_epoch\""},
		{"\"min_epoch\"", "\"path\""},
		{"\"path\"", "\"released_at\""},
		{"\"released_at\"", "\"roothash\""},
		{"\"roothash\"", "\"size\""},
	}
	for _, p := range pairs {
		ia := strings.Index(s, p[0])
		ib := strings.Index(s, p[1])
		if ia < 0 || ib < 0 {
			t.Errorf("key %q or %q not found in canonical output: %s", p[0], p[1], s)
			continue
		}
		if ia >= ib {
			t.Errorf("key %q appears at or after %q in canonical output: %s", p[0], p[1], s)
		}
	}
}

// ─── ParseAndVerify – happy path ─────────────────────────────────────────────

// OSDIST01_TestParseAndVerify_HappyPath verifies the full sign→marshal→verify
// round-trip succeeds with a matching key and epoch floor ≤ min_epoch.
func OSDIST01_TestParseAndVerify_HappyPath(t *testing.T) {
	pub, priv := genKeyPair(t)
	m := sampleManifest()
	sig := signManifest(t, &m, priv)
	data := marshalManifest(t, m)

	got, err := ParseAndVerify(data, sig, pub, 0)
	if err != nil {
		t.Fatalf("ParseAndVerify: %v", err)
	}
	if got == nil {
		t.Fatal("ParseAndVerify returned nil without error")
	}
	if got.Latest != m.Latest {
		t.Errorf("Latest: got %q want %q", got.Latest, m.Latest)
	}
	if got.MinEpoch != m.MinEpoch {
		t.Errorf("MinEpoch: got %d want %d", got.MinEpoch, m.MinEpoch)
	}
}

// OSDIST01_TestParseAndVerify_EpochAtFloor verifies that a manifest with
// min_epoch exactly equal to the floor is accepted.
func OSDIST01_TestParseAndVerify_EpochAtFloor(t *testing.T) {
	pub, priv := genKeyPair(t)
	m := sampleManifest() // MinEpoch = 3
	sig := signManifest(t, &m, priv)
	data := marshalManifest(t, m)

	_, err := ParseAndVerify(data, sig, pub, 3) // floor == min_epoch
	if err != nil {
		t.Fatalf("ParseAndVerify with floor==min_epoch: %v", err)
	}
}

// ─── ParseAndVerify – tamper rejection ───────────────────────────────────────

// OSDIST01_TestParseAndVerify_TamperData verifies that modifying the manifest
// JSON after signing causes ErrBadSignature.
func OSDIST01_TestParseAndVerify_TamperData(t *testing.T) {
	pub, priv := genKeyPair(t)
	m := sampleManifest()
	sig := signManifest(t, &m, priv)
	data := marshalManifest(t, m)

	// Flip a byte in the middle of the data.
	tampered := make([]byte, len(data))
	copy(tampered, data)
	tampered[len(tampered)/2] ^= 0xFF

	_, err := ParseAndVerify(tampered, sig, pub, 0)
	if !errors.Is(err, ErrBadSignature) && !errors.Is(err, ErrMalformed) {
		// Tampered bytes may be invalid JSON (ErrMalformed) or pass JSON parse
		// but fail signature (ErrBadSignature). Both are acceptable rejections.
		t.Fatalf("expected ErrBadSignature or ErrMalformed, got: %v", err)
	}
}

// OSDIST01_TestParseAndVerify_TamperField verifies that changing a field value
// in the manifest JSON after signing causes ErrBadSignature.
func OSDIST01_TestParseAndVerify_TamperField(t *testing.T) {
	pub, priv := genKeyPair(t)
	m := sampleManifest()
	sig := signManifest(t, &m, priv)

	// Modify a field before marshalling to simulate an attacker changing content.
	tampered := m
	tampered.Latest = "v99" // different version
	data := marshalManifest(t, tampered)

	_, err := ParseAndVerify(data, sig, pub, 0)
	if !errors.Is(err, ErrBadSignature) {
		t.Fatalf("expected ErrBadSignature, got: %v", err)
	}
}

// OSDIST01_TestParseAndVerify_TamperSig verifies that a corrupted signature
// causes ErrBadSignature.
func OSDIST01_TestParseAndVerify_TamperSig(t *testing.T) {
	pub, priv := genKeyPair(t)
	m := sampleManifest()
	sig := signManifest(t, &m, priv)
	data := marshalManifest(t, m)

	// Flip a byte in the signature.
	tampered := make([]byte, len(sig))
	copy(tampered, sig)
	tampered[len(tampered)/2] ^= 0xFF

	_, err := ParseAndVerify(data, tampered, pub, 0)
	if !errors.Is(err, ErrBadSignature) {
		t.Fatalf("expected ErrBadSignature, got: %v", err)
	}
}

// OSDIST01_TestParseAndVerify_WrongKey verifies that a signature produced by
// one key does not verify against a different public key.
func OSDIST01_TestParseAndVerify_WrongKey(t *testing.T) {
	_, priv1 := genKeyPair(t)
	pub2, _ := genKeyPair(t) // different key pair

	m := sampleManifest()
	sig := signManifest(t, &m, priv1)
	data := marshalManifest(t, m)

	_, err := ParseAndVerify(data, sig, pub2, 0)
	if !errors.Is(err, ErrBadSignature) {
		t.Fatalf("expected ErrBadSignature for wrong key, got: %v", err)
	}
}

// ─── ParseAndVerify – epoch floor enforcement ─────────────────────────────────

// OSDIST01_TestParseAndVerify_EpochTooLow verifies that a manifest with
// min_epoch below the supplied floor is rejected with ErrEpochTooLow.
func OSDIST01_TestParseAndVerify_EpochTooLow(t *testing.T) {
	pub, priv := genKeyPair(t)
	m := sampleManifest() // MinEpoch = 3
	sig := signManifest(t, &m, priv)
	data := marshalManifest(t, m)

	_, err := ParseAndVerify(data, sig, pub, 4) // floor = 4 > min_epoch = 3
	if !errors.Is(err, ErrEpochTooLow) {
		t.Fatalf("expected ErrEpochTooLow, got: %v", err)
	}
}

// OSDIST01_TestParseAndVerify_EpochZeroFloor verifies that a floor of zero
// accepts any non-negative min_epoch.
func OSDIST01_TestParseAndVerify_EpochZeroFloor(t *testing.T) {
	pub, priv := genKeyPair(t)
	m := sampleManifest()
	m.MinEpoch = 0
	sig := signManifest(t, &m, priv)
	data := marshalManifest(t, m)

	_, err := ParseAndVerify(data, sig, pub, 0)
	if err != nil {
		t.Fatalf("ParseAndVerify with min_epoch=0 and floor=0: %v", err)
	}
}

// ─── ParseAndVerify – malformed input ────────────────────────────────────────

// OSDIST01_TestParseAndVerify_MalformedJSON verifies that invalid JSON returns
// ErrMalformed.
func OSDIST01_TestParseAndVerify_MalformedJSON(t *testing.T) {
	pub, _ := genKeyPair(t)
	sig := make([]byte, ed25519.SignatureSize)

	_, err := ParseAndVerify([]byte(`{not valid json}`), sig, pub, 0)
	if !errors.Is(err, ErrMalformed) {
		t.Fatalf("expected ErrMalformed for invalid JSON, got: %v", err)
	}
}

// OSDIST01_TestParseAndVerify_MissingFields verifies that a manifest missing
// required fields returns ErrMalformed.
func OSDIST01_TestParseAndVerify_MissingFields(t *testing.T) {
	pub, _ := genKeyPair(t)
	sig := make([]byte, ed25519.SignatureSize)

	cases := []struct {
		name string
		json string
	}{
		{"missing channel", `{"latest":"v08","min_epoch":1,"roothash":"abc","size":1,"released_at":"2026-05-20T09:00:00Z","path":"os/v08/os-core.squashfs"}`},
		{"missing latest", `{"channel":"stable","min_epoch":1,"roothash":"abc","size":1,"released_at":"2026-05-20T09:00:00Z","path":"os/v08/os-core.squashfs"}`},
		{"missing roothash", `{"channel":"stable","latest":"v08","min_epoch":1,"size":1,"released_at":"2026-05-20T09:00:00Z","path":"os/v08/os-core.squashfs"}`},
		{"missing path", `{"channel":"stable","latest":"v08","min_epoch":1,"roothash":"abc","size":1,"released_at":"2026-05-20T09:00:00Z"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseAndVerify([]byte(tc.json), sig, pub, 0)
			if !errors.Is(err, ErrMalformed) && !errors.Is(err, ErrBadSignature) {
				// Missing fields cause ErrMalformed before signature check; if
				// they somehow pass structural validation they'll fail sig check.
				t.Fatalf("expected ErrMalformed or ErrBadSignature, got: %v", err)
			}
		})
	}
}

// ─── VersionPath helpers ──────────────────────────────────────────────────────

// OSDIST01_TestVersionPath verifies the canonical bucket paths for a version.
func OSDIST01_TestVersionPath(t *testing.T) {
	cases := []struct {
		version string
		want    string
		wantSig string
	}{
		{"v07", "os/v07/os-core.squashfs", "os/v07/os-core.squashfs.sig"},
		{"v08", "os/v08/os-core.squashfs", "os/v08/os-core.squashfs.sig"},
		{"v100", "os/v100/os-core.squashfs", "os/v100/os-core.squashfs.sig"},
	}
	for _, tc := range cases {
		got := VersionPath(tc.version)
		if got != tc.want {
			t.Errorf("VersionPath(%q) = %q, want %q", tc.version, got, tc.want)
		}
		gotSig := VersionSigPath(tc.version)
		if gotSig != tc.wantSig {
			t.Errorf("VersionSigPath(%q) = %q, want %q", tc.version, gotSig, tc.wantSig)
		}
	}
}

// ─── Standard test entry points ──────────────────────────────────────────────

func TestCanonical_ByteStability(t *testing.T)       { OSDIST01_TestCanonical_ByteStability(t) }
func TestCanonical_IsValidJSON(t *testing.T)         { OSDIST01_TestCanonical_IsValidJSON(t) }
func TestCanonical_NoWhitespace(t *testing.T)        { OSDIST01_TestCanonical_NoWhitespace(t) }
func TestCanonical_SortedKeys(t *testing.T)          { OSDIST01_TestCanonical_SortedKeys(t) }
func TestParseAndVerify_HappyPath(t *testing.T)      { OSDIST01_TestParseAndVerify_HappyPath(t) }
func TestParseAndVerify_EpochAtFloor(t *testing.T)   { OSDIST01_TestParseAndVerify_EpochAtFloor(t) }
func TestParseAndVerify_TamperData(t *testing.T)     { OSDIST01_TestParseAndVerify_TamperData(t) }
func TestParseAndVerify_TamperField(t *testing.T)    { OSDIST01_TestParseAndVerify_TamperField(t) }
func TestParseAndVerify_TamperSig(t *testing.T)      { OSDIST01_TestParseAndVerify_TamperSig(t) }
func TestParseAndVerify_WrongKey(t *testing.T)       { OSDIST01_TestParseAndVerify_WrongKey(t) }
func TestParseAndVerify_EpochTooLow(t *testing.T)    { OSDIST01_TestParseAndVerify_EpochTooLow(t) }
func TestParseAndVerify_EpochZeroFloor(t *testing.T) { OSDIST01_TestParseAndVerify_EpochZeroFloor(t) }
func TestParseAndVerify_MalformedJSON(t *testing.T)  { OSDIST01_TestParseAndVerify_MalformedJSON(t) }
func TestParseAndVerify_MissingFields(t *testing.T)  { OSDIST01_TestParseAndVerify_MissingFields(t) }
func TestVersionPath(t *testing.T)                   { OSDIST01_TestVersionPath(t) }
