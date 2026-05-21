package signing

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"testing"
)

// ─── Canonical byte-stability tests ──────────────────────────────────────────

// SIGN01_TestCanonical_ByteStability verifies that Canonical produces
// identical bytes across multiple calls for the same logical value, and that
// map-iteration order (which Go randomises) has no effect.
func SIGN01_TestCanonical_ByteStability(t *testing.T) {
	t.Helper()

	type Manifest struct {
		Channel    string `json:"channel"`
		Latest     string `json:"latest"`
		MinEpoch   int    `json:"min_epoch"`
		RootHash   string `json:"roothash"`
		Size       int64  `json:"size"`
		ReleasedAt string `json:"released_at"`
		Path       string `json:"path"`
	}

	m := Manifest{
		Channel:    "stable",
		Latest:     "v08",
		MinEpoch:   3,
		RootHash:   "abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890",
		Size:       734003200,
		ReleasedAt: "2026-05-20T09:00:00Z",
		Path:       "os/v08/os-core.squashfs",
	}

	first, err := Canonical(m)
	if err != nil {
		t.Fatalf("Canonical: %v", err)
	}
	// Run 20 times; Go map randomises iteration order on each run.
	for i := 0; i < 20; i++ {
		got, err := Canonical(m)
		if err != nil {
			t.Fatalf("iteration %d: Canonical: %v", i, err)
		}
		if !bytes.Equal(first, got) {
			t.Fatalf("iteration %d: canonical bytes differ:\n  first: %s\n  got:   %s", i, first, got)
		}
	}
}

// SIGN01_TestCanonical_MapKeyOrder verifies that two map[string]any values
// with the same key-value pairs but constructed in opposite insertion order
// produce identical canonical bytes.
func SIGN01_TestCanonical_MapKeyOrder(t *testing.T) {
	a := map[string]any{
		"zebra":   "z",
		"apple":   "a",
		"mango":   "m",
		"channel": "stable",
	}
	b := map[string]any{
		"channel": "stable",
		"mango":   "m",
		"apple":   "a",
		"zebra":   "z",
	}

	ca, err := Canonical(a)
	if err != nil {
		t.Fatalf("Canonical(a): %v", err)
	}
	cb, err := Canonical(b)
	if err != nil {
		t.Fatalf("Canonical(b): %v", err)
	}
	if !bytes.Equal(ca, cb) {
		t.Fatalf("map key order affected output:\n  a: %s\n  b: %s", ca, cb)
	}
}

// SIGN01_TestCanonical_NestedSorted verifies that nested object keys are also
// sorted, not just top-level ones.
func SIGN01_TestCanonical_NestedSorted(t *testing.T) {
	v := map[string]any{
		"z": map[string]any{"b": 2, "a": 1},
		"a": map[string]any{"d": 4, "c": 3},
	}
	got, err := Canonical(v)
	if err != nil {
		t.Fatalf("Canonical: %v", err)
	}
	want := `{"a":{"c":3,"d":4},"z":{"a":1,"b":2}}`
	if string(got) != want {
		t.Fatalf("nested sort wrong:\n  got:  %s\n  want: %s", got, want)
	}
}

// SIGN01_TestCanonical_NoWhitespace verifies the output contains no
// insignificant whitespace (spaces, tabs, newlines).
func SIGN01_TestCanonical_NoWhitespace(t *testing.T) {
	v := map[string]any{"key": "value", "num": 42}
	got, err := Canonical(v)
	if err != nil {
		t.Fatalf("Canonical: %v", err)
	}
	for _, b := range got {
		if b == ' ' || b == '\t' || b == '\n' || b == '\r' {
			t.Fatalf("canonical output contains whitespace: %q", got)
		}
	}
}

// SIGN01_TestCanonical_ValidJSON verifies the canonical output is valid JSON.
func SIGN01_TestCanonical_ValidJSON(t *testing.T) {
	v := map[string]any{
		"channel":     "stable",
		"min_epoch":   3,
		"released_at": "2026-05-20T09:00:00Z",
	}
	got, err := Canonical(v)
	if err != nil {
		t.Fatalf("Canonical: %v", err)
	}
	var check any
	if err := json.Unmarshal(got, &check); err != nil {
		t.Fatalf("canonical output is not valid JSON: %v\noutput: %s", err, got)
	}
}

// ─── Sign / Verify round-trip tests ──────────────────────────────────────────

func genKeyPair(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	return pub, priv
}

// SIGN01_TestSignVerify_RoundTrip verifies that Sign + Verify succeeds for a
// valid canonical payload.
func SIGN01_TestSignVerify_RoundTrip(t *testing.T) {
	pub, priv := genKeyPair(t)

	payload := map[string]any{
		"channel":   "stable",
		"latest":    "v08",
		"min_epoch": 3,
	}
	canonical, err := Canonical(payload)
	if err != nil {
		t.Fatalf("Canonical: %v", err)
	}

	sig := Sign(priv, canonical)
	if !Verify(pub, canonical, sig) {
		t.Fatal("Verify returned false for a valid signature")
	}
}

// SIGN01_TestSignVerify_TamperPayload verifies that flipping a byte in the
// canonical payload causes Verify to return false.
func SIGN01_TestSignVerify_TamperPayload(t *testing.T) {
	pub, priv := genKeyPair(t)

	canonical, err := Canonical(map[string]any{"channel": "stable", "min_epoch": 3})
	if err != nil {
		t.Fatalf("Canonical: %v", err)
	}

	sig := Sign(priv, canonical)

	// Tamper: flip a byte in the middle of the payload.
	tampered := make([]byte, len(canonical))
	copy(tampered, canonical)
	tampered[len(tampered)/2] ^= 0xFF

	if Verify(pub, tampered, sig) {
		t.Fatal("Verify returned true for tampered payload — expected false")
	}
}

// SIGN01_TestSignVerify_TamperSignature verifies that flipping a byte in the
// signature itself causes Verify to return false.
func SIGN01_TestSignVerify_TamperSignature(t *testing.T) {
	pub, priv := genKeyPair(t)

	canonical, err := Canonical(map[string]any{"channel": "stable"})
	if err != nil {
		t.Fatalf("Canonical: %v", err)
	}

	sig := Sign(priv, canonical)

	// Tamper: flip a byte in the signature.
	tampered := make([]byte, len(sig))
	copy(tampered, sig)
	tampered[len(tampered)/2] ^= 0xFF

	if Verify(pub, canonical, tampered) {
		t.Fatal("Verify returned true for tampered signature — expected false")
	}
}

// SIGN01_TestSignVerify_WrongKey verifies that a signature produced by one key
// does not verify against a different key.
func SIGN01_TestSignVerify_WrongKey(t *testing.T) {
	_, priv1 := genKeyPair(t)
	pub2, _ := genKeyPair(t)

	canonical, err := Canonical(map[string]any{"channel": "stable"})
	if err != nil {
		t.Fatalf("Canonical: %v", err)
	}

	sig := Sign(priv1, canonical)
	if Verify(pub2, canonical, sig) {
		t.Fatal("Verify returned true for wrong public key — expected false")
	}
}

// SIGN01_TestVerify_BadLengths verifies that Verify handles malformed inputs
// gracefully (returns false, not panic).
func SIGN01_TestVerify_BadLengths(t *testing.T) {
	pub, priv := genKeyPair(t)
	canonical, _ := Canonical(map[string]any{"x": 1})
	sig := Sign(priv, canonical)

	cases := []struct {
		name string
		pub  ed25519.PublicKey
		sig  []byte
	}{
		{"empty pub", nil, sig},
		{"short pub", pub[:10], sig},
		{"empty sig", pub, nil},
		{"short sig", pub, sig[:10]},
	}
	for _, tc := range cases {
		if Verify(tc.pub, canonical, tc.sig) {
			t.Errorf("case %q: Verify returned true for bad lengths", tc.name)
		}
	}
}

// ─── Detached-signature format round-trip tests ───────────────────────────────

// SIGN01_TestSigFormat_RoundTrip verifies MarshalSig → ParseSig is lossless.
func SIGN01_TestSigFormat_RoundTrip(t *testing.T) {
	_, priv := genKeyPair(t)
	canonical, err := Canonical(map[string]any{"channel": "stable", "min_epoch": 3})
	if err != nil {
		t.Fatalf("Canonical: %v", err)
	}

	original := Signature{
		Algorithm: AlgorithmID,
		KeyID:     "release-2026-05",
		SigBytes:  Sign(priv, canonical),
	}

	data, err := MarshalSig(original)
	if err != nil {
		t.Fatalf("MarshalSig: %v", err)
	}

	parsed, err := ParseSig(data)
	if err != nil {
		t.Fatalf("ParseSig: %v", err)
	}

	if parsed.Algorithm != original.Algorithm {
		t.Errorf("Algorithm: got %q want %q", parsed.Algorithm, original.Algorithm)
	}
	if parsed.KeyID != original.KeyID {
		t.Errorf("KeyID: got %q want %q", parsed.KeyID, original.KeyID)
	}
	if !bytes.Equal(parsed.SigBytes, original.SigBytes) {
		t.Error("SigBytes differ after round-trip")
	}
}

// SIGN01_TestSigFormat_ContainsHeader verifies the marshalled .sig starts with
// the expected header line.
func SIGN01_TestSigFormat_ContainsHeader(t *testing.T) {
	_, priv := genKeyPair(t)
	canonical, _ := Canonical(map[string]any{"x": 1})
	s := Signature{
		Algorithm: AlgorithmID,
		KeyID:     "test-key",
		SigBytes:  Sign(priv, canonical),
	}
	data, err := MarshalSig(s)
	if err != nil {
		t.Fatalf("MarshalSig: %v", err)
	}
	if len(data) == 0 || string(data[:len(sigFilePrefix)]) != sigFilePrefix {
		t.Fatalf("missing header in .sig output:\n%s", data)
	}
}

// SIGN01_TestSigFormat_BadAlgo verifies MarshalSig rejects unknown algorithms.
func SIGN01_TestSigFormat_BadAlgo(t *testing.T) {
	_, priv := genKeyPair(t)
	canonical, _ := Canonical(map[string]any{"x": 1})
	s := Signature{
		Algorithm: "rsa-pss",
		KeyID:     "k",
		SigBytes:  Sign(priv, canonical),
	}
	if _, err := MarshalSig(s); err == nil {
		t.Fatal("expected error for unknown algorithm")
	}
}

// SIGN01_TestSigFormat_ParseInvalid verifies ParseSig rejects malformed input.
func SIGN01_TestSigFormat_ParseInvalid(t *testing.T) {
	cases := []string{
		"",
		"wrong-header\nalgorithm: ed25519\nkey-id: k\nsig: AAAA",
		sigFilePrefix + "\nalgorithm: ed25519\nkey-id: k", // missing sig field
		sigFilePrefix + "\nalgorithm: ed25519\nsig: AAAA", // missing key-id
		sigFilePrefix + "\nkey-id: k\nsig: AAAA",          // missing algorithm
		sigFilePrefix + "\nalgorithm: ed25519\nkey-id: k\nsig: !not-base64!",
	}
	for _, c := range cases {
		if _, err := ParseSig([]byte(c)); err == nil {
			t.Errorf("ParseSig succeeded on invalid input %q — expected error", c)
		}
	}
}

// SIGN01_TestSigFormat_VerifyAfterParse demonstrates the full signing workflow:
// Canonical → Sign → MarshalSig → ParseSig → Verify.
func SIGN01_TestSigFormat_VerifyAfterParse(t *testing.T) {
	pub, priv := genKeyPair(t)

	manifest := map[string]any{
		"channel":   "stable",
		"latest":    "v08",
		"min_epoch": 3,
		"roothash":  "deadbeef",
	}
	canonical, err := Canonical(manifest)
	if err != nil {
		t.Fatalf("Canonical: %v", err)
	}

	s := Signature{
		Algorithm: AlgorithmID,
		KeyID:     "release-2026-05",
		SigBytes:  Sign(priv, canonical),
	}
	data, err := MarshalSig(s)
	if err != nil {
		t.Fatalf("MarshalSig: %v", err)
	}

	parsed, err := ParseSig(data)
	if err != nil {
		t.Fatalf("ParseSig: %v", err)
	}

	if !Verify(pub, canonical, parsed.SigBytes) {
		t.Fatal("Verify failed after full marshal/parse round-trip")
	}
}

// ─── Standard test entry points ───────────────────────────────────────────────

func TestCanonical_ByteStability(t *testing.T)    { SIGN01_TestCanonical_ByteStability(t) }
func TestCanonical_MapKeyOrder(t *testing.T)      { SIGN01_TestCanonical_MapKeyOrder(t) }
func TestCanonical_NestedSorted(t *testing.T)     { SIGN01_TestCanonical_NestedSorted(t) }
func TestCanonical_NoWhitespace(t *testing.T)     { SIGN01_TestCanonical_NoWhitespace(t) }
func TestCanonical_ValidJSON(t *testing.T)        { SIGN01_TestCanonical_ValidJSON(t) }
func TestSignVerify_RoundTrip(t *testing.T)       { SIGN01_TestSignVerify_RoundTrip(t) }
func TestSignVerify_TamperPayload(t *testing.T)   { SIGN01_TestSignVerify_TamperPayload(t) }
func TestSignVerify_TamperSignature(t *testing.T) { SIGN01_TestSignVerify_TamperSignature(t) }
func TestSignVerify_WrongKey(t *testing.T)        { SIGN01_TestSignVerify_WrongKey(t) }
func TestVerify_BadLengths(t *testing.T)          { SIGN01_TestVerify_BadLengths(t) }
func TestSigFormat_RoundTrip(t *testing.T)        { SIGN01_TestSigFormat_RoundTrip(t) }
func TestSigFormat_ContainsHeader(t *testing.T)   { SIGN01_TestSigFormat_ContainsHeader(t) }
func TestSigFormat_BadAlgo(t *testing.T)          { SIGN01_TestSigFormat_BadAlgo(t) }
func TestSigFormat_ParseInvalid(t *testing.T)     { SIGN01_TestSigFormat_ParseInvalid(t) }
func TestSigFormat_VerifyAfterParse(t *testing.T) { SIGN01_TestSigFormat_VerifyAfterParse(t) }
