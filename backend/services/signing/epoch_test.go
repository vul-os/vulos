package signing

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// ─── Helpers ──────────────────────────────────────────────────────────────────

// newTempEpochStore creates a fresh EpochStore backed by a temp directory.
func newTempEpochStore(t *testing.T) *EpochStore {
	t.Helper()
	path := filepath.Join(t.TempDir(), "epoch-floor.json")
	s, err := NewEpochStore(path)
	if err != nil {
		t.Fatalf("NewEpochStore: %v", err)
	}
	return s
}

// writeTempAnchorKey writes an Ed25519 public key as a Base64 anchor file and
// returns the path and corresponding private key.
func writeTempAnchorKey(t *testing.T) (anchorPath string, priv ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "trust-anchor.pub")
	encoded := base64.StdEncoding.EncodeToString(pub)
	if err := os.WriteFile(path, []byte(encoded+"\n"), 0444); err != nil {
		t.Fatalf("writeTempAnchorKey: %v", err)
	}
	return path, priv
}

// signBump signs an EpochBumpMessage using priv and returns the raw signature.
func signBump(t *testing.T, priv ed25519.PrivateKey, msg EpochBumpMessage) []byte {
	t.Helper()
	canonical, err := Canonical(msg)
	if err != nil {
		t.Fatalf("Canonical(bump): %v", err)
	}
	return Sign(priv, canonical)
}

// ─── NewEpochStore tests ──────────────────────────────────────────────────────

// SIGN04_TestNewEpochStore_InitialFloorIsZero verifies that a brand-new store
// starts at floor 0.
func SIGN04_TestNewEpochStore_InitialFloorIsZero(t *testing.T) {
	s := newTempEpochStore(t)
	if got := s.Current(); got != 0 {
		t.Fatalf("initial floor: got %d, want 0", got)
	}
}

// SIGN04_TestNewEpochStore_Reopen verifies that the floor is persisted across
// store reopens.
func SIGN04_TestNewEpochStore_Reopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "epoch-floor.json")

	s1, err := NewEpochStore(path)
	if err != nil {
		t.Fatalf("NewEpochStore (first open): %v", err)
	}
	if err := s1.RaiseTo(7); err != nil {
		t.Fatalf("RaiseTo: %v", err)
	}

	// Reopen from the same path.
	s2, err := NewEpochStore(path)
	if err != nil {
		t.Fatalf("NewEpochStore (reopen): %v", err)
	}
	if got := s2.Current(); got != 7 {
		t.Fatalf("floor after reopen: got %d, want 7", got)
	}
}

// ─── RaiseTo / monotonicity tests ────────────────────────────────────────────

// SIGN04_TestRaiseTo_Monotonic verifies that RaiseTo raises the floor when the
// new value is higher than the current floor.
func SIGN04_TestRaiseTo_Monotonic(t *testing.T) {
	s := newTempEpochStore(t)

	steps := []int64{1, 3, 5, 10, 10, 9, 8, 11}
	expected := []int64{1, 3, 5, 10, 10, 10, 10, 11}

	for i, n := range steps {
		if err := s.RaiseTo(n); err != nil {
			t.Fatalf("step %d: RaiseTo(%d): %v", i, n, err)
		}
		if got := s.Current(); got != expected[i] {
			t.Fatalf("step %d: RaiseTo(%d): floor = %d, want %d", i, n, got, expected[i])
		}
	}
}

// SIGN04_TestRaiseTo_NeverDecreases verifies that calling RaiseTo with a value
// below the current floor is a no-op (floor must never decrease).
func SIGN04_TestRaiseTo_NeverDecreases(t *testing.T) {
	s := newTempEpochStore(t)

	if err := s.RaiseTo(5); err != nil {
		t.Fatalf("RaiseTo(5): %v", err)
	}

	// All of these must be no-ops.
	for _, lower := range []int64{0, 1, 4, 5} {
		if err := s.RaiseTo(lower); err != nil {
			t.Fatalf("RaiseTo(%d): unexpected error: %v", lower, err)
		}
		if got := s.Current(); got != 5 {
			t.Fatalf("RaiseTo(%d) lowered the floor to %d (want 5)", lower, got)
		}
	}
}

// ─── AcceptEpoch tests ────────────────────────────────────────────────────────

// SIGN04_TestAcceptEpoch_AtOrAboveFloor verifies that epochs at or above the
// floor are accepted.
func SIGN04_TestAcceptEpoch_AtOrAboveFloor(t *testing.T) {
	s := newTempEpochStore(t)
	if err := s.RaiseTo(3); err != nil {
		t.Fatalf("RaiseTo: %v", err)
	}

	for _, ok := range []int64{3, 4, 5, 100} {
		if err := s.AcceptEpoch(ok); err != nil {
			t.Errorf("AcceptEpoch(%d): unexpected error (floor=3): %v", ok, err)
		}
	}
}

// SIGN04_TestAcceptEpoch_BelowFloor verifies that claimed epochs below the
// floor are rejected (rollback / downgrade protection).
func SIGN04_TestAcceptEpoch_BelowFloor(t *testing.T) {
	s := newTempEpochStore(t)
	if err := s.RaiseTo(5); err != nil {
		t.Fatalf("RaiseTo: %v", err)
	}

	for _, bad := range []int64{0, 1, 2, 4} {
		if err := s.AcceptEpoch(bad); err == nil {
			t.Errorf("AcceptEpoch(%d): expected rejection (floor=5), got nil", bad)
		}
	}
}

// SIGN04_TestAcceptEpoch_DoesNotRaiseFloor verifies that AcceptEpoch does NOT
// automatically raise the floor — raising is explicit.
func SIGN04_TestAcceptEpoch_DoesNotRaiseFloor(t *testing.T) {
	s := newTempEpochStore(t)
	if err := s.RaiseTo(2); err != nil {
		t.Fatalf("RaiseTo: %v", err)
	}

	// Accept a higher epoch — floor must stay at 2.
	if err := s.AcceptEpoch(99); err != nil {
		t.Fatalf("AcceptEpoch(99): %v", err)
	}
	if got := s.Current(); got != 2 {
		t.Fatalf("AcceptEpoch raised the floor to %d (want 2)", got)
	}
}

// ─── RaiseFromReleaseCert (the automatic raise) tests ────────────────────────

// issueTestCert issues a root-signed release cert at minEpoch using rootPriv.
func issueTestCert(t *testing.T, rootPriv ed25519.PrivateKey, minEpoch int64) ReleaseCert {
	t.Helper()
	relPub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	cert, err := IssueReleaseCert(rootPriv, relPub, "release-test",
		time.Now().Add(24*time.Hour), minEpoch)
	if err != nil {
		t.Fatalf("IssueReleaseCert: %v", err)
	}
	return cert
}

// SIGN04_TestRaiseFromReleaseCert_RaisesToCertEpoch — the mechanism that was
// missing entirely: a verified root-signed cert moves the floor.
func SIGN04_TestRaiseFromReleaseCert_RaisesToCertEpoch(t *testing.T) {
	s := newTempEpochStore(t)
	anchorPath, rootPriv := writeTempAnchorKey(t)
	rootPub, err := LoadAnchor(anchorPath)
	if err != nil {
		t.Fatalf("LoadAnchor: %v", err)
	}

	if err := s.RaiseFromReleaseCert(rootPub, issueTestCert(t, rootPriv, 4)); err != nil {
		t.Fatalf("RaiseFromReleaseCert: %v", err)
	}
	if got := s.Current(); got != 4 {
		t.Fatalf("floor after raising from a cert at epoch 4: got %d, want 4", got)
	}
	if err := s.AcceptEpoch(3); err == nil {
		t.Fatal("epoch 3 still accepted after the floor rose to 4 — revocation is inert")
	}
}

// SIGN04_TestRaiseFromReleaseCert_ForgedCertRaisesNothing — the raise is gated
// on the root signature, not on the caller's say-so.
func SIGN04_TestRaiseFromReleaseCert_ForgedCertRaisesNothing(t *testing.T) {
	s := newTempEpochStore(t)
	anchorPath, _ := writeTempAnchorKey(t)
	rootPub, err := LoadAnchor(anchorPath)
	if err != nil {
		t.Fatalf("LoadAnchor: %v", err)
	}
	_, attackerRoot, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	if err := s.RaiseFromReleaseCert(rootPub, issueTestCert(t, attackerRoot, 500)); err == nil {
		t.Fatal("a cert signed by an unrelated key was accepted for a floor raise")
	}
	if got := s.Current(); got != 0 {
		t.Fatalf("a forged cert moved the floor to %d", got)
	}
}

// SIGN04_TestRaiseFromReleaseCert_NeverLowers — a validly signed cert carrying
// an OLDER epoch must not walk the floor back down. Refusing it is the caller's
// job (AcceptEpoch); silently lowering here would be the worst outcome.
func SIGN04_TestRaiseFromReleaseCert_NeverLowers(t *testing.T) {
	s := newTempEpochStore(t)
	anchorPath, rootPriv := writeTempAnchorKey(t)
	rootPub, err := LoadAnchor(anchorPath)
	if err != nil {
		t.Fatalf("LoadAnchor: %v", err)
	}

	if err := s.RaiseFromReleaseCert(rootPub, issueTestCert(t, rootPriv, 9)); err != nil {
		t.Fatalf("RaiseFromReleaseCert(9): %v", err)
	}
	if err := s.RaiseFromReleaseCert(rootPub, issueTestCert(t, rootPriv, 2)); err != nil {
		t.Fatalf("RaiseFromReleaseCert(2) should be a no-op, got: %v", err)
	}
	if got := s.Current(); got != 9 {
		t.Fatalf("floor LOWERED to %d by an older cert (want 9)", got)
	}
}

// SIGN04_TestRaiseFromReleaseCert_ExpiredCertRaisesNothing — cert validation is
// the full ValidateReleaseCert, expiry included, not just the signature.
func SIGN04_TestRaiseFromReleaseCert_ExpiredCertRaisesNothing(t *testing.T) {
	s := newTempEpochStore(t)
	anchorPath, rootPriv := writeTempAnchorKey(t)
	rootPub, err := LoadAnchor(anchorPath)
	if err != nil {
		t.Fatalf("LoadAnchor: %v", err)
	}

	relPub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	expired, err := IssueReleaseCert(rootPriv, relPub, "release-old",
		time.Now().Add(-1*time.Hour), 42)
	if err != nil {
		t.Fatalf("IssueReleaseCert: %v", err)
	}

	if err := s.RaiseFromReleaseCert(rootPub, expired); err == nil {
		t.Fatal("an EXPIRED cert was accepted for a floor raise")
	}
	if got := s.Current(); got != 0 {
		t.Fatalf("an expired cert moved the floor to %d", got)
	}
}

// ─── BumpFloor (root-signed) tests ───────────────────────────────────────────

// SIGN04_TestBumpFloor_Valid verifies that a root-signed bump message raises
// the floor.
func SIGN04_TestBumpFloor_Valid(t *testing.T) {
	s := newTempEpochStore(t)
	anchorPath, priv := writeTempAnchorKey(t)

	msg := EpochBumpMessage{Type: "epoch-bump", NewFloor: 7}
	sig := signBump(t, priv, msg)

	if err := s.BumpFloor(anchorPath, msg, sig); err != nil {
		t.Fatalf("BumpFloor: %v", err)
	}
	if got := s.Current(); got != 7 {
		t.Fatalf("floor after bump: got %d, want 7", got)
	}
}

// SIGN04_TestBumpFloor_UnsignedRejected verifies that a bump with a zeroed /
// random signature (not from the root key) is rejected and the floor is not
// raised.
func SIGN04_TestBumpFloor_UnsignedRejected(t *testing.T) {
	s := newTempEpochStore(t)
	anchorPath, _ := writeTempAnchorKey(t) // anchor key is generated; we don't sign with it

	// Sign with a completely different key (not the anchor).
	_, wrongPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	msg := EpochBumpMessage{Type: "epoch-bump", NewFloor: 10}
	badSig := signBump(t, wrongPriv, msg)

	if err := s.BumpFloor(anchorPath, msg, badSig); err == nil {
		t.Fatal("BumpFloor: expected rejection for wrong-key signature, got nil")
	}
	if got := s.Current(); got != 0 {
		t.Fatalf("floor was modified despite bad signature: got %d, want 0", got)
	}
}

// SIGN04_TestBumpFloor_WrongMessageType verifies that a bump message with an
// incorrect type field is rejected even if the signature is valid.
func SIGN04_TestBumpFloor_WrongMessageType(t *testing.T) {
	s := newTempEpochStore(t)
	anchorPath, priv := writeTempAnchorKey(t)

	// Sign a message with wrong type — the canonical bytes will be valid but
	// BumpFloor must check the type field before accepting.
	msg := EpochBumpMessage{Type: "not-epoch-bump", NewFloor: 10}
	sig := signBump(t, priv, msg)

	if err := s.BumpFloor(anchorPath, msg, sig); err == nil {
		t.Fatal("BumpFloor: expected rejection for wrong message type, got nil")
	}
	if got := s.Current(); got != 0 {
		t.Fatalf("floor was modified despite wrong type: got %d, want 0", got)
	}
}

// SIGN04_TestBumpFloor_Monotonic verifies that a valid but lower-than-current
// bump is a no-op (consistent with RaiseTo semantics).
func SIGN04_TestBumpFloor_Monotonic(t *testing.T) {
	s := newTempEpochStore(t)
	anchorPath, priv := writeTempAnchorKey(t)

	// Raise to 10 first.
	bump10 := EpochBumpMessage{Type: "epoch-bump", NewFloor: 10}
	if err := s.BumpFloor(anchorPath, bump10, signBump(t, priv, bump10)); err != nil {
		t.Fatalf("BumpFloor(10): %v", err)
	}

	// A subsequent valid-but-lower bump must be a no-op.
	bump5 := EpochBumpMessage{Type: "epoch-bump", NewFloor: 5}
	if err := s.BumpFloor(anchorPath, bump5, signBump(t, priv, bump5)); err != nil {
		t.Fatalf("BumpFloor(5): unexpected error: %v", err)
	}
	if got := s.Current(); got != 10 {
		t.Fatalf("lower signed bump changed floor: got %d, want 10", got)
	}
}

// SIGN04_TestBumpFloor_TamperedBytes verifies that tampering with the
// canonical bytes of a bump message causes verification to fail.
func SIGN04_TestBumpFloor_TamperedBytes(t *testing.T) {
	s := newTempEpochStore(t)
	anchorPath, priv := writeTempAnchorKey(t)

	msg := EpochBumpMessage{Type: "epoch-bump", NewFloor: 8}
	// Sign the real message, then alter the floor value — the signature no
	// longer matches the new canonical encoding.
	sig := signBump(t, priv, msg)
	msg.NewFloor = 9999 // tamper after signing

	if err := s.BumpFloor(anchorPath, msg, sig); err == nil {
		t.Fatal("BumpFloor: expected rejection for tampered message, got nil")
	}
}

// ─── Standard test entry points ───────────────────────────────────────────────

func TestNewEpochStore_InitialFloorIsZero(t *testing.T) {
	SIGN04_TestNewEpochStore_InitialFloorIsZero(t)
}
func TestNewEpochStore_Reopen(t *testing.T)          { SIGN04_TestNewEpochStore_Reopen(t) }
func TestRaiseTo_Monotonic(t *testing.T)             { SIGN04_TestRaiseTo_Monotonic(t) }
func TestRaiseTo_NeverDecreases(t *testing.T)        { SIGN04_TestRaiseTo_NeverDecreases(t) }
func TestAcceptEpoch_AtOrAboveFloor(t *testing.T)    { SIGN04_TestAcceptEpoch_AtOrAboveFloor(t) }
func TestAcceptEpoch_BelowFloor(t *testing.T)        { SIGN04_TestAcceptEpoch_BelowFloor(t) }
func TestAcceptEpoch_DoesNotRaiseFloor(t *testing.T) { SIGN04_TestAcceptEpoch_DoesNotRaiseFloor(t) }
func TestBumpFloor_Valid(t *testing.T)               { SIGN04_TestBumpFloor_Valid(t) }
func TestBumpFloor_UnsignedRejected(t *testing.T)    { SIGN04_TestBumpFloor_UnsignedRejected(t) }
func TestBumpFloor_WrongMessageType(t *testing.T)    { SIGN04_TestBumpFloor_WrongMessageType(t) }
func TestBumpFloor_Monotonic(t *testing.T)           { SIGN04_TestBumpFloor_Monotonic(t) }
func TestBumpFloor_TamperedBytes(t *testing.T)       { SIGN04_TestBumpFloor_TamperedBytes(t) }

func TestRaiseFromReleaseCert_RaisesToCertEpoch(t *testing.T) {
	SIGN04_TestRaiseFromReleaseCert_RaisesToCertEpoch(t)
}
func TestRaiseFromReleaseCert_ForgedCertRaisesNothing(t *testing.T) {
	SIGN04_TestRaiseFromReleaseCert_ForgedCertRaisesNothing(t)
}
func TestRaiseFromReleaseCert_NeverLowers(t *testing.T) {
	SIGN04_TestRaiseFromReleaseCert_NeverLowers(t)
}
func TestRaiseFromReleaseCert_ExpiredCertRaisesNothing(t *testing.T) {
	SIGN04_TestRaiseFromReleaseCert_ExpiredCertRaisesNothing(t)
}
