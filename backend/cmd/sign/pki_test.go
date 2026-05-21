package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"

	"vulos/backend/services/signing"
)

// ─── helpers ─────────────────────────────────────────────────────────────────

func genKeyPair(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	return pub, priv
}

func futureTime() time.Time {
	return time.Now().UTC().Add(365 * 24 * time.Hour)
}

func pastTime() time.Time {
	return time.Now().UTC().Add(-24 * time.Hour)
}

// ─── SIGN-03 AC: root signs a release cert (offline) ─────────────────────────

// SIGN03_TestIssueReleaseCert_RoundTrip verifies that IssueReleaseCert
// produces a cert whose RootSig can be re-verified directly, satisfying the
// "root signs a release cert offline" acceptance criterion.
func SIGN03_TestIssueReleaseCert_RoundTrip(t *testing.T) {
	rootPub, rootPriv := genKeyPair(t)
	releasePub, _ := genKeyPair(t)

	cert, err := IssueReleaseCert(rootPriv, releasePub, "release-2026-05", futureTime(), 3)
	if err != nil {
		t.Fatalf("IssueReleaseCert: %v", err)
	}

	// Basic field sanity.
	if cert.KeyID != "release-2026-05" {
		t.Errorf("KeyID: got %q", cert.KeyID)
	}
	if cert.MinEpoch != 3 {
		t.Errorf("MinEpoch: got %d", cert.MinEpoch)
	}
	if cert.RootSig == "" {
		t.Error("RootSig must not be empty")
	}

	// Device-side validate must succeed.
	if err := ValidateReleaseCert(rootPub, cert); err != nil {
		t.Fatalf("ValidateReleaseCert: %v", err)
	}
}

// ─── SIGN-03 AC: device-side validates release cert against baked root ────────

// SIGN03_TestValidateReleaseCert_WrongRootKey verifies that validation fails
// when a different (wrong) root public key is supplied — fail-closed.
func SIGN03_TestValidateReleaseCert_WrongRootKey(t *testing.T) {
	_, rootPriv := genKeyPair(t)
	wrongRootPub, _ := genKeyPair(t)
	releasePub, _ := genKeyPair(t)

	cert, err := IssueReleaseCert(rootPriv, releasePub, "k", futureTime(), 1)
	if err != nil {
		t.Fatalf("IssueReleaseCert: %v", err)
	}

	if err := ValidateReleaseCert(wrongRootPub, cert); err == nil {
		t.Fatal("ValidateReleaseCert: expected error with wrong root key, got nil")
	}
}

// SIGN03_TestValidateReleaseCert_Expired verifies that an expired cert is rejected.
func SIGN03_TestValidateReleaseCert_Expired(t *testing.T) {
	rootPub, rootPriv := genKeyPair(t)
	releasePub, _ := genKeyPair(t)

	// Issue a cert that is already past its NotAfter.
	cert, err := IssueReleaseCert(rootPriv, releasePub, "k", pastTime(), 1)
	if err != nil {
		t.Fatalf("IssueReleaseCert: %v", err)
	}

	if err := ValidateReleaseCert(rootPub, cert); err == nil {
		t.Fatal("ValidateReleaseCert: expected error for expired cert, got nil")
	}
}

// SIGN03_TestValidateReleaseCert_TamperedSig verifies that flipping a byte in
// RootSig causes validation to fail.
func SIGN03_TestValidateReleaseCert_TamperedSig(t *testing.T) {
	rootPub, rootPriv := genKeyPair(t)
	releasePub, _ := genKeyPair(t)

	cert, err := IssueReleaseCert(rootPriv, releasePub, "k", futureTime(), 1)
	if err != nil {
		t.Fatalf("IssueReleaseCert: %v", err)
	}

	// Corrupt the last character of the base64 signature.
	rs := []byte(cert.RootSig)
	rs[len(rs)-1] ^= 0x01
	cert.RootSig = string(rs)

	if err := ValidateReleaseCert(rootPub, cert); err == nil {
		t.Fatal("ValidateReleaseCert: expected error for tampered RootSig, got nil")
	}
}

// SIGN03_TestValidateReleaseCert_TamperedBody verifies that mutating a cert
// field after issuance causes the root signature to fail.
func SIGN03_TestValidateReleaseCert_TamperedBody(t *testing.T) {
	rootPub, rootPriv := genKeyPair(t)
	releasePub, _ := genKeyPair(t)

	cert, err := IssueReleaseCert(rootPriv, releasePub, "original-key-id", futureTime(), 1)
	if err != nil {
		t.Fatalf("IssueReleaseCert: %v", err)
	}

	// Tamper: change the key_id.
	cert.KeyID = "tampered-key-id"

	if err := ValidateReleaseCert(rootPub, cert); err == nil {
		t.Fatal("ValidateReleaseCert: expected error for tampered KeyID, got nil")
	}
}

// SIGN03_TestValidateReleaseCert_FailClosedOnBadPub verifies that a zero-length
// root pubkey returns an error.
func SIGN03_TestValidateReleaseCert_FailClosedOnBadPub(t *testing.T) {
	_, rootPriv := genKeyPair(t)
	releasePub, _ := genKeyPair(t)

	cert, err := IssueReleaseCert(rootPriv, releasePub, "k", futureTime(), 1)
	if err != nil {
		t.Fatalf("IssueReleaseCert: %v", err)
	}

	if err := ValidateReleaseCert(nil, cert); err == nil {
		t.Fatal("ValidateReleaseCert: expected error for nil root pubkey, got nil")
	}
}

// ─── SIGN-03 AC: release key signs image + manifest ──────────────────────────

// SIGN03_TestSignImage_RoundTrip verifies SignImage + VerifyImage round-trip.
func SIGN03_TestSignImage_RoundTrip(t *testing.T) {
	rootPub, rootPriv := genKeyPair(t)
	releasePub, releasePriv := genKeyPair(t)

	cert, err := IssueReleaseCert(rootPriv, releasePub, "release-2026-05", futureTime(), 3)
	if err != nil {
		t.Fatalf("IssueReleaseCert: %v", err)
	}
	if err := ValidateReleaseCert(rootPub, cert); err != nil {
		t.Fatalf("ValidateReleaseCert: %v", err)
	}

	payload := ImagePayload{
		Path:       "os/v08/os-core.squashfs",
		RootHash:   "abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890ab",
		Size:       734003200,
		MinEpoch:   3,
		ReleasedAt: "2026-05-20T09:00:00Z",
	}

	sig, err := SignImage(releasePriv, cert.KeyID, payload)
	if err != nil {
		t.Fatalf("SignImage: %v", err)
	}
	if sig.Algorithm != signing.AlgorithmID {
		t.Errorf("Algorithm: got %q want %q", sig.Algorithm, signing.AlgorithmID)
	}
	if sig.KeyID != cert.KeyID {
		t.Errorf("KeyID: got %q want %q", sig.KeyID, cert.KeyID)
	}

	ok, err := VerifyImage(releasePub, payload, sig)
	if err != nil {
		t.Fatalf("VerifyImage: %v", err)
	}
	if !ok {
		t.Fatal("VerifyImage returned false for valid signature")
	}
}

// SIGN03_TestSignImage_TamperPayload verifies that modifying a field in the
// payload after signing causes VerifyImage to return false.
func SIGN03_TestSignImage_TamperPayload(t *testing.T) {
	_, releasePriv := genKeyPair(t)
	releasePub, _ := genKeyPair(t) // wrong pub — intentional, tests tamper path

	payload := ImagePayload{
		Path:       "os/v08/os-core.squashfs",
		RootHash:   "deadbeef",
		Size:       1024,
		MinEpoch:   1,
		ReleasedAt: "2026-05-20T09:00:00Z",
	}
	sig, _ := SignImage(releasePriv, "k", payload)

	// Tamper: change roothash.
	payload.RootHash = "00000000"
	ok, err := VerifyImage(releasePub, payload, sig)
	if err != nil {
		t.Fatalf("VerifyImage: %v", err)
	}
	if ok {
		t.Fatal("VerifyImage returned true for tampered payload")
	}
}

// SIGN03_TestSignManifest_RoundTrip verifies SignManifest + VerifyManifest.
func SIGN03_TestSignManifest_RoundTrip(t *testing.T) {
	rootPub, rootPriv := genKeyPair(t)
	releasePub, releasePriv := genKeyPair(t)

	cert, err := IssueReleaseCert(rootPriv, releasePub, "release-2026-05", futureTime(), 3)
	if err != nil {
		t.Fatalf("IssueReleaseCert: %v", err)
	}
	if err := ValidateReleaseCert(rootPub, cert); err != nil {
		t.Fatalf("ValidateReleaseCert: %v", err)
	}

	payload := ManifestPayload{
		Channel:    "stable",
		Latest:     "v08",
		MinEpoch:   3,
		Path:       "os/v08/os-core.squashfs",
		ReleasedAt: "2026-05-20T09:00:00Z",
		RootHash:   "abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890ab",
		Size:       734003200,
	}

	sig, err := SignManifest(releasePriv, cert.KeyID, payload)
	if err != nil {
		t.Fatalf("SignManifest: %v", err)
	}

	ok, err := VerifyManifest(releasePub, payload, sig)
	if err != nil {
		t.Fatalf("VerifyManifest: %v", err)
	}
	if !ok {
		t.Fatal("VerifyManifest returned false for valid signature")
	}
}

// SIGN03_TestSignManifest_TamperPayload verifies that mutating the manifest
// after signing causes VerifyManifest to return false.
func SIGN03_TestSignManifest_TamperPayload(t *testing.T) {
	releasePub, releasePriv := genKeyPair(t)

	payload := ManifestPayload{
		Channel:    "stable",
		Latest:     "v08",
		MinEpoch:   3,
		Path:       "os/v08/os-core.squashfs",
		ReleasedAt: "2026-05-20T09:00:00Z",
		RootHash:   "deadbeef",
		Size:       734003200,
	}
	sig, _ := SignManifest(releasePriv, "k", payload)

	// Tamper: bump epoch.
	payload.MinEpoch = 4
	ok, err := VerifyManifest(releasePub, payload, sig)
	if err != nil {
		t.Fatalf("VerifyManifest: %v", err)
	}
	if ok {
		t.Fatal("VerifyManifest returned true for tampered manifest")
	}
}

// SIGN03_TestSignManifest_WrongKey verifies that a manifest signed by one key
// does not verify against a different release key.
func SIGN03_TestSignManifest_WrongKey(t *testing.T) {
	_, releasePriv1 := genKeyPair(t)
	releasePub2, _ := genKeyPair(t)

	payload := ManifestPayload{
		Channel:  "stable",
		Latest:   "v08",
		MinEpoch: 3,
	}
	sig, _ := SignManifest(releasePriv1, "k", payload)

	ok, err := VerifyManifest(releasePub2, payload, sig)
	if err != nil {
		t.Fatalf("VerifyManifest: %v", err)
	}
	if ok {
		t.Fatal("VerifyManifest returned true for wrong release key")
	}
}

// ─── Sig serialisation integration: MarshalSig / ParseSig with release sigs ──

// SIGN03_TestSignImage_SigMarshalRoundTrip verifies that an image sig can be
// round-tripped through signing.MarshalSig / signing.ParseSig.
func SIGN03_TestSignImage_SigMarshalRoundTrip(t *testing.T) {
	_, releasePriv := genKeyPair(t)
	releasePub := releasePriv.Public().(ed25519.PublicKey)

	payload := ImagePayload{
		Path: "os/v09/os-core.squashfs", RootHash: "abc123", Size: 1024,
		MinEpoch: 5, ReleasedAt: "2026-06-01T00:00:00Z",
	}

	sig, err := SignImage(releasePriv, "release-2026-06", payload)
	if err != nil {
		t.Fatalf("SignImage: %v", err)
	}

	data, err := signing.MarshalSig(sig)
	if err != nil {
		t.Fatalf("MarshalSig: %v", err)
	}

	parsed, err := signing.ParseSig(data)
	if err != nil {
		t.Fatalf("ParseSig: %v", err)
	}

	if !bytes.Equal(parsed.SigBytes, sig.SigBytes) {
		t.Error("SigBytes differ after marshal/parse round-trip")
	}

	ok, err := VerifyImage(releasePub, payload, parsed)
	if err != nil {
		t.Fatalf("VerifyImage: %v", err)
	}
	if !ok {
		t.Fatal("VerifyImage returned false after MarshalSig/ParseSig round-trip")
	}
}

// ─── Key serialisation helpers ────────────────────────────────────────────────

// SIGN03_TestKeyMarshalRoundTrip verifies that a key pair can be serialised and
// re-parsed without loss.
func SIGN03_TestKeyMarshalRoundTrip(t *testing.T) {
	pub, priv := genKeyPair(t)

	privData, err := MarshalPrivateKey(priv)
	if err != nil {
		t.Fatalf("MarshalPrivateKey: %v", err)
	}
	pubData, err := MarshalPublicKey(pub)
	if err != nil {
		t.Fatalf("MarshalPublicKey: %v", err)
	}

	parsedPriv, err := ParsePrivateKey(privData)
	if err != nil {
		t.Fatalf("ParsePrivateKey: %v", err)
	}
	parsedPub, err := ParsePublicKey(pubData)
	if err != nil {
		t.Fatalf("ParsePublicKey: %v", err)
	}

	if !bytes.Equal(priv, parsedPriv) {
		t.Error("private key differs after round-trip")
	}
	if !bytes.Equal(pub, parsedPub) {
		t.Error("public key differs after round-trip")
	}
}

// SIGN03_TestDecodePubKey verifies ReleaseCert.DecodePubKey returns the
// original public key.
func SIGN03_TestDecodePubKey(t *testing.T) {
	releasePub, releasePriv := genKeyPair(t)
	_, rootPriv := genKeyPair(t)

	cert, err := IssueReleaseCert(rootPriv, releasePub, "k", futureTime(), 1)
	if err != nil {
		t.Fatalf("IssueReleaseCert: %v", err)
	}

	decoded, err := cert.DecodePubKey()
	if err != nil {
		t.Fatalf("DecodePubKey: %v", err)
	}
	if !bytes.Equal(decoded, releasePub) {
		t.Error("DecodePubKey returned different key")
	}

	// Confirm decoded key verifies a release signature.
	payload := ManifestPayload{Channel: "stable", Latest: "v01", MinEpoch: 1}
	sig, _ := SignManifest(releasePriv, "k", payload)
	ok, err := VerifyManifest(decoded, payload, sig)
	if err != nil {
		t.Fatalf("VerifyManifest: %v", err)
	}
	if !ok {
		t.Fatal("VerifyManifest failed with decoded release pubkey")
	}
}

// ─── IssueReleaseCert input validation ───────────────────────────────────────

func SIGN03_TestIssueReleaseCert_BadInputs(t *testing.T) {
	_, rootPriv := genKeyPair(t)
	releasePub, _ := genKeyPair(t)

	cases := []struct {
		name     string
		rootPriv ed25519.PrivateKey
		relPub   ed25519.PublicKey
		keyID    string
		minEpoch int64
	}{
		{"nil root priv", nil, releasePub, "k", 0},
		{"nil release pub", rootPriv, nil, "k", 0},
		{"empty keyID", rootPriv, releasePub, "", 0},
		{"negative minEpoch", rootPriv, releasePub, "k", -1},
	}
	for _, tc := range cases {
		_, err := IssueReleaseCert(tc.rootPriv, tc.relPub, tc.keyID, futureTime(), tc.minEpoch)
		if err == nil {
			t.Errorf("case %q: expected error, got nil", tc.name)
		}
	}
}

// ─── Standard test entry points ──────────────────────────────────────────────

func TestIssueReleaseCert_RoundTrip(t *testing.T) { SIGN03_TestIssueReleaseCert_RoundTrip(t) }
func TestValidateReleaseCert_WrongRootKey(t *testing.T) {
	SIGN03_TestValidateReleaseCert_WrongRootKey(t)
}
func TestValidateReleaseCert_Expired(t *testing.T)     { SIGN03_TestValidateReleaseCert_Expired(t) }
func TestValidateReleaseCert_TamperedSig(t *testing.T) { SIGN03_TestValidateReleaseCert_TamperedSig(t) }
func TestValidateReleaseCert_TamperedBody(t *testing.T) {
	SIGN03_TestValidateReleaseCert_TamperedBody(t)
}
func TestValidateReleaseCert_FailClosedOnBadPub(t *testing.T) {
	SIGN03_TestValidateReleaseCert_FailClosedOnBadPub(t)
}
func TestSignImage_RoundTrip(t *testing.T)           { SIGN03_TestSignImage_RoundTrip(t) }
func TestSignImage_TamperPayload(t *testing.T)       { SIGN03_TestSignImage_TamperPayload(t) }
func TestSignManifest_RoundTrip(t *testing.T)        { SIGN03_TestSignManifest_RoundTrip(t) }
func TestSignManifest_TamperPayload(t *testing.T)    { SIGN03_TestSignManifest_TamperPayload(t) }
func TestSignManifest_WrongKey(t *testing.T)         { SIGN03_TestSignManifest_WrongKey(t) }
func TestSignImage_SigMarshalRoundTrip(t *testing.T) { SIGN03_TestSignImage_SigMarshalRoundTrip(t) }
func TestKeyMarshalRoundTrip(t *testing.T)           { SIGN03_TestKeyMarshalRoundTrip(t) }
func TestDecodePubKey(t *testing.T)                  { SIGN03_TestDecodePubKey(t) }
func TestIssueReleaseCert_BadInputs(t *testing.T)    { SIGN03_TestIssueReleaseCert_BadInputs(t) }
