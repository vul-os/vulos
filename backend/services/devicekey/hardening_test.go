package devicekey

import (
	"bytes"
	"errors"
	"testing"
	"time"
)

// TestForceInstallIdentity_CompareAndSwap covers the TOCTOU fix: the install
// primitive refuses (ErrActiveKeyChanged, nothing installed) unless the active
// key still equals the key the caller verified the quorum against, so a
// concurrent rotation can never make a break-glass cert bind to a key the
// quorum never signed over.
func TestForceInstallIdentity_CompareAndSwap(t *testing.T) {
	resetRevocationChecker(t)
	ks := newTestSoftwareStore(t)
	curDER, err := ks.DeviceIdentity()
	if err != nil {
		t.Fatalf("DeviceIdentity: %v", err)
	}
	newPriv, err := GenerateCandidateKey()
	if err != nil {
		t.Fatalf("GenerateCandidateKey: %v", err)
	}

	// Wrong expected old key => refused, identity unchanged.
	wrong := append([]byte(nil), curDER...)
	wrong[0] ^= 0xff
	if _, err := ks.forceInstallIdentity(newPriv, wrong); !errors.Is(err, ErrActiveKeyChanged) {
		t.Fatalf("wrong expected old key: got %v, want ErrActiveKeyChanged", err)
	}
	if afterDER, _ := ks.DeviceIdentity(); !bytes.Equal(curDER, afterDER) {
		t.Fatal("identity must be unchanged after a failed compare-and-swap")
	}

	// Empty expected => also refused (break-glass callers always know the old key).
	if _, err := ks.forceInstallIdentity(newPriv, nil); !errors.Is(err, ErrActiveKeyChanged) {
		t.Fatalf("nil expected: got %v, want ErrActiveKeyChanged", err)
	}

	// Correct expected => installs and returns the retired key.
	got, err := ks.forceInstallIdentity(newPriv, curDER)
	if err != nil {
		t.Fatalf("compare-and-swap with correct expected: %v", err)
	}
	if !bytes.Equal(got, curDER) {
		t.Fatal("returned old key must equal the expected/verified key")
	}
	if newDER, _ := ks.DeviceIdentity(); bytes.Equal(newDER, curDER) {
		t.Fatal("active identity should have changed after a successful install")
	}
}

// TestRevocationStore_CapacityRefusesBeyondMax covers the poisoning backstop:
// once at capacity the store refuses NEW fingerprints (so a malicious rostered
// peer serving unlimited valid self-revocations cannot grow the store without
// bound) while already-present fingerprints still merge idempotently.
func TestRevocationStore_CapacityRefusesBeyondMax(t *testing.T) {
	resetRevocationChecker(t)
	target := newTestRevocationStore(t)
	target.maxEntries = 2

	mkSelfCert := func() *DeviceRevocationCert {
		ks := newTestSoftwareStore(t)
		c, err := SelfRevoke(ks, newTestRevocationStore(t), "throwaway")
		if err != nil {
			t.Fatalf("SelfRevoke: %v", err)
		}
		return c
	}
	c1, c2, c3 := mkSelfCert(), mkSelfCert(), mkSelfCert()

	if err := target.MergeVerified(c1, nil, 0, time.Now()); err != nil {
		t.Fatalf("merge c1: %v", err)
	}
	if err := target.MergeVerified(c2, nil, 0, time.Now()); err != nil {
		t.Fatalf("merge c2: %v", err)
	}
	// At capacity: a 3rd DISTINCT fingerprint is refused.
	if err := target.MergeVerified(c3, nil, 0, time.Now()); err == nil {
		t.Fatal("expected capacity refusal merging a 3rd distinct revocation")
	}
	if target.IsRevoked(c3.Fingerprint) {
		t.Fatal("refused revocation must not have been recorded")
	}
	// An already-present fingerprint still merges idempotently (no growth, no error).
	if err := target.MergeVerified(c1, nil, 0, time.Now()); err != nil {
		t.Fatalf("idempotent re-merge of an existing entry should succeed: %v", err)
	}
}
