package signing

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
)

// writeTempAnchor writes a Base64-encoded Ed25519 public key to a temp file
// and returns the path.  The file is cleaned up when the test ends.
func writeTempAnchor(t *testing.T, pub ed25519.PublicKey) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "trust-anchor.pub")
	encoded := base64.StdEncoding.EncodeToString(pub)
	if err := os.WriteFile(path, []byte(encoded+"\n"), 0444); err != nil {
		t.Fatalf("writeTempAnchor: %v", err)
	}
	return path
}

// ─── LoadAnchor tests ─────────────────────────────────────────────────────────

// SIGN02_TestLoadAnchor_Valid verifies that LoadAnchor parses a well-formed
// anchor file and returns the expected public key.
func SIGN02_TestLoadAnchor_Valid(t *testing.T) {
	pub, _ := genKeyPair(t)
	path := writeTempAnchor(t, pub)

	got, err := LoadAnchor(path)
	if err != nil {
		t.Fatalf("LoadAnchor: unexpected error: %v", err)
	}
	if len(got) != ed25519.PublicKeySize {
		t.Fatalf("LoadAnchor: got %d bytes, want %d", len(got), ed25519.PublicKeySize)
	}
	for i := range pub {
		if got[i] != pub[i] {
			t.Fatalf("LoadAnchor: key mismatch at byte %d", i)
		}
	}
}

// SIGN02_TestLoadAnchor_Missing verifies that LoadAnchor returns a non-nil
// error (not a panic) when the file does not exist.
func SIGN02_TestLoadAnchor_Missing(t *testing.T) {
	_, err := LoadAnchor("/nonexistent/path/trust-anchor.pub")
	if err == nil {
		t.Fatal("LoadAnchor: expected error for missing file, got nil")
	}
}

// SIGN02_TestLoadAnchor_Empty verifies that LoadAnchor returns an error for an
// empty file.
func SIGN02_TestLoadAnchor_Empty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "trust-anchor.pub")
	if err := os.WriteFile(path, []byte{}, 0444); err != nil {
		t.Fatalf("setup: %v", err)
	}
	_, err := LoadAnchor(path)
	if err == nil {
		t.Fatal("LoadAnchor: expected error for empty file, got nil")
	}
}

// SIGN02_TestLoadAnchor_NotBase64 verifies that LoadAnchor returns an error
// when the file contains invalid Base64.
func SIGN02_TestLoadAnchor_NotBase64(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "trust-anchor.pub")
	if err := os.WriteFile(path, []byte("!!not-valid-base64!!\n"), 0444); err != nil {
		t.Fatalf("setup: %v", err)
	}
	_, err := LoadAnchor(path)
	if err == nil {
		t.Fatal("LoadAnchor: expected error for invalid Base64, got nil")
	}
}

// SIGN02_TestLoadAnchor_WrongLength verifies that LoadAnchor returns an error
// when the decoded key is not exactly 32 bytes.
func SIGN02_TestLoadAnchor_WrongLength(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "trust-anchor.pub")
	// 16 random bytes — valid Base64 but wrong decoded length.
	short := make([]byte, 16)
	if _, err := rand.Read(short); err != nil {
		t.Fatalf("rand: %v", err)
	}
	encoded := base64.StdEncoding.EncodeToString(short)
	if err := os.WriteFile(path, []byte(encoded+"\n"), 0444); err != nil {
		t.Fatalf("setup: %v", err)
	}
	_, err := LoadAnchor(path)
	if err == nil {
		t.Fatal("LoadAnchor: expected error for wrong-length key, got nil")
	}
}

// ─── VerifyWithAnchor tests ───────────────────────────────────────────────────

// SIGN02_TestVerifyWithAnchor_Valid verifies that a correct signature over the
// correct canonical bytes passes against the anchor that holds the matching key.
func SIGN02_TestVerifyWithAnchor_Valid(t *testing.T) {
	pub, priv := genKeyPair(t)
	path := writeTempAnchor(t, pub)

	canonical, err := Canonical(map[string]any{"channel": "stable", "min_epoch": 1})
	if err != nil {
		t.Fatalf("Canonical: %v", err)
	}
	sig := Sign(priv, canonical)

	if err := VerifyWithAnchor(path, canonical, sig); err != nil {
		t.Fatalf("VerifyWithAnchor: unexpected error: %v", err)
	}
}

// SIGN02_TestVerifyWithAnchor_WrongKey verifies that a signature produced with
// a different private key is rejected.
func SIGN02_TestVerifyWithAnchor_WrongKey(t *testing.T) {
	pub, _ := genKeyPair(t)   // anchor key
	_, priv2 := genKeyPair(t) // different signing key
	path := writeTempAnchor(t, pub)

	canonical, err := Canonical(map[string]any{"channel": "stable", "min_epoch": 1})
	if err != nil {
		t.Fatalf("Canonical: %v", err)
	}
	sig := Sign(priv2, canonical) // signed with the wrong key

	if err := VerifyWithAnchor(path, canonical, sig); err == nil {
		t.Fatal("VerifyWithAnchor: expected error for wrong key, got nil")
	}
}

// SIGN02_TestVerifyWithAnchor_TamperedBytes verifies that flipping a byte in
// the canonical payload causes verification to fail.
func SIGN02_TestVerifyWithAnchor_TamperedBytes(t *testing.T) {
	pub, priv := genKeyPair(t)
	path := writeTempAnchor(t, pub)

	canonical, err := Canonical(map[string]any{"channel": "stable", "min_epoch": 1})
	if err != nil {
		t.Fatalf("Canonical: %v", err)
	}
	sig := Sign(priv, canonical)

	tampered := make([]byte, len(canonical))
	copy(tampered, canonical)
	tampered[len(tampered)/2] ^= 0xFF

	if err := VerifyWithAnchor(path, tampered, sig); err == nil {
		t.Fatal("VerifyWithAnchor: expected error for tampered bytes, got nil")
	}
}

// SIGN02_TestVerifyWithAnchor_MissingAnchor verifies that a missing anchor
// file returns a non-nil error and does not panic.
func SIGN02_TestVerifyWithAnchor_MissingAnchor(t *testing.T) {
	_, priv := genKeyPair(t)

	canonical, err := Canonical(map[string]any{"channel": "stable"})
	if err != nil {
		t.Fatalf("Canonical: %v", err)
	}
	sig := Sign(priv, canonical)

	if err := VerifyWithAnchor("/nonexistent/trust-anchor.pub", canonical, sig); err == nil {
		t.Fatal("VerifyWithAnchor: expected error for missing anchor, got nil")
	}
}

// SIGN02_TestVerifyWithAnchor_MalformedAnchor verifies that a malformed anchor
// file (non-Base64 content) returns a non-nil error and does not panic.
func SIGN02_TestVerifyWithAnchor_MalformedAnchor(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "trust-anchor.pub")
	if err := os.WriteFile(path, []byte("this-is-not-a-key\n"), 0444); err != nil {
		t.Fatalf("setup: %v", err)
	}

	_, priv := genKeyPair(t)
	canonical, err := Canonical(map[string]any{"channel": "stable"})
	if err != nil {
		t.Fatalf("Canonical: %v", err)
	}
	sig := Sign(priv, canonical)

	if err := VerifyWithAnchor(path, canonical, sig); err == nil {
		t.Fatal("VerifyWithAnchor: expected error for malformed anchor, got nil")
	}
}

// SIGN02_TestVerifyWithAnchor_EmptyCanonicalBytes verifies that passing empty
// canonical bytes returns an error (fail closed).
func SIGN02_TestVerifyWithAnchor_EmptyCanonicalBytes(t *testing.T) {
	pub, priv := genKeyPair(t)
	path := writeTempAnchor(t, pub)

	canonical, err := Canonical(map[string]any{"x": 1})
	if err != nil {
		t.Fatalf("Canonical: %v", err)
	}
	sig := Sign(priv, canonical)

	if err := VerifyWithAnchor(path, nil, sig); err == nil {
		t.Fatal("VerifyWithAnchor: expected error for nil canonicalBytes, got nil")
	}
}

// SIGN02_TestVerifyWithAnchor_EmptySig verifies that passing an empty signature
// returns an error (fail closed).
func SIGN02_TestVerifyWithAnchor_EmptySig(t *testing.T) {
	pub, priv := genKeyPair(t)
	path := writeTempAnchor(t, pub)

	canonical, err := Canonical(map[string]any{"x": 1})
	if err != nil {
		t.Fatalf("Canonical: %v", err)
	}
	_ = Sign(priv, canonical)

	if err := VerifyWithAnchor(path, canonical, nil); err == nil {
		t.Fatal("VerifyWithAnchor: expected error for nil sig, got nil")
	}
}

// ─── Standard test entry points ──────────────────────────────────────────────

func TestLoadAnchor_Valid(t *testing.T)               { SIGN02_TestLoadAnchor_Valid(t) }
func TestLoadAnchor_Missing(t *testing.T)             { SIGN02_TestLoadAnchor_Missing(t) }
func TestLoadAnchor_Empty(t *testing.T)               { SIGN02_TestLoadAnchor_Empty(t) }
func TestLoadAnchor_NotBase64(t *testing.T)           { SIGN02_TestLoadAnchor_NotBase64(t) }
func TestLoadAnchor_WrongLength(t *testing.T)         { SIGN02_TestLoadAnchor_WrongLength(t) }
func TestVerifyWithAnchor_Valid(t *testing.T)         { SIGN02_TestVerifyWithAnchor_Valid(t) }
func TestVerifyWithAnchor_WrongKey(t *testing.T)      { SIGN02_TestVerifyWithAnchor_WrongKey(t) }
func TestVerifyWithAnchor_TamperedBytes(t *testing.T) { SIGN02_TestVerifyWithAnchor_TamperedBytes(t) }
func TestVerifyWithAnchor_MissingAnchor(t *testing.T) { SIGN02_TestVerifyWithAnchor_MissingAnchor(t) }
func TestVerifyWithAnchor_MalformedAnchor(t *testing.T) {
	SIGN02_TestVerifyWithAnchor_MalformedAnchor(t)
}
func TestVerifyWithAnchor_EmptyCanonicalBytes(t *testing.T) {
	SIGN02_TestVerifyWithAnchor_EmptyCanonicalBytes(t)
}
func TestVerifyWithAnchor_EmptySig(t *testing.T) { SIGN02_TestVerifyWithAnchor_EmptySig(t) }
