package devicekey

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"vulos/backend/services/fleetid"
)

func newTestRevocationStore(t *testing.T) *RevocationStore {
	t.Helper()
	store, err := NewRevocationStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewRevocationStore: %v", err)
	}
	return store
}

// resetRevocationChecker ensures the process-wide checker installed by a test
// does not leak into other tests (the hook is a package-level var).
func resetRevocationChecker(t *testing.T) {
	t.Helper()
	t.Cleanup(func() { SetRevocationChecker(nil) })
}

// ─── Self-revocation ─────────────────────────────────────────────────────────────

func TestSelfRevoke_RecordsAndGatesAdmission(t *testing.T) {
	resetRevocationChecker(t)
	ks := newTestSoftwareStore(t)
	store := newTestRevocationStore(t)

	pubDER, _ := ks.DeviceIdentity()
	fp := Fingerprint(pubDER)

	if IsDeviceKeyRevoked(fp) {
		t.Fatal("key should not be revoked before SelfRevoke")
	}

	cert, err := SelfRevoke(ks, store, "decommissioning box")
	if err != nil {
		t.Fatalf("SelfRevoke: %v", err)
	}
	if cert.Method != RevocationMethodSelf {
		t.Fatalf("Method = %q, want %q", cert.Method, RevocationMethodSelf)
	}
	if !store.IsRevoked(fp) {
		t.Fatal("store should record the revocation")
	}

	// Wire the process-wide gate and confirm admission now rejects the key.
	SetRevocationChecker(store.IsRevoked)
	if !IsDeviceKeyRevoked(fp) {
		t.Fatal("IsDeviceKeyRevoked should report true once wired")
	}

	digest := make([]byte, 32)
	sig, err := ks.Sign(digest, 0)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if err := VerifyDeviceSignatureChecked(pubDER, digest, sig); err == nil {
		t.Fatal("VerifyDeviceSignatureChecked should reject a signature from a revoked key")
	}
}

func TestIsDeviceKeyRevoked_NilCheckerFailsOpenOnlyWhenUnwired(t *testing.T) {
	resetRevocationChecker(t)
	SetRevocationChecker(nil)
	if IsDeviceKeyRevoked("anything") {
		t.Fatal("with no checker installed, IsDeviceKeyRevoked must return false (subsystem entirely absent)")
	}
}

func TestSelfRevoke_TamperedCertFailsVerification(t *testing.T) {
	ks := newTestSoftwareStore(t)
	store := newTestRevocationStore(t)
	cert, err := SelfRevoke(ks, store, "reason")
	if err != nil {
		t.Fatalf("SelfRevoke: %v", err)
	}
	cert.Reason = "tampered"
	if err := cert.Verify(nil, 0, time.Now()); err == nil {
		t.Fatal("expected verification failure for a tampered revocation cert")
	}
}

// RevocationStore.MergeVerified is monotonic: merging the same cert twice
// must not error and must not somehow "un-revoke".
func TestRevocationStore_MergeIsIdempotent(t *testing.T) {
	ks := newTestSoftwareStore(t)
	store := newTestRevocationStore(t)
	cert, err := SelfRevoke(ks, store, "reason")
	if err != nil {
		t.Fatalf("SelfRevoke: %v", err)
	}
	if err := store.MergeVerified(cert, nil, 0, time.Now()); err != nil {
		t.Fatalf("re-merging an already-known valid cert should be a no-op, got: %v", err)
	}
	if !store.IsRevoked(cert.Fingerprint) {
		t.Fatal("still revoked after idempotent re-merge")
	}
}

// TestRevocationStore_PersistsAcrossReopen proves the monotonic set survives
// a restart (mirrors epoch.go's persistence discipline).
func TestRevocationStore_PersistsAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	ks := newTestSoftwareStore(t)

	store1, err := NewRevocationStore(dir)
	if err != nil {
		t.Fatalf("NewRevocationStore: %v", err)
	}
	cert, err := SelfRevoke(ks, store1, "reason")
	if err != nil {
		t.Fatalf("SelfRevoke: %v", err)
	}

	store2, err := NewRevocationStore(dir)
	if err != nil {
		t.Fatalf("reopen NewRevocationStore: %v", err)
	}
	if !store2.IsRevoked(cert.Fingerprint) {
		t.Fatal("revocation did not survive reopen")
	}
}

func TestRevocationStore_CorruptStoreFailsClosed(t *testing.T) {
	dir := t.TempDir()
	if _, err := NewRevocationStore(dir); err != nil {
		t.Fatalf("NewRevocationStore: %v", err)
	}
	// Corrupt the on-disk file.
	path := filepath.Join(dir, revocationFileName)
	if err := os.WriteFile(path, []byte("not json"), 0600); err != nil {
		t.Fatalf("write corrupt store: %v", err)
	}
	if _, err := NewRevocationStore(dir); err == nil {
		t.Fatal("expected NewRevocationStore to fail closed on a corrupt store")
	}
}

// ─── Break-glass revocation ──────────────────────────────────────────────────────

func TestBreakGlassRevoke_ValidQuorumSucceeds(t *testing.T) {
	ks := newTestSoftwareStore(t)
	store := newTestRevocationStore(t)
	pubDER, _ := ks.DeviceIdentity()

	subject := newFleetBox(t)
	voucher1 := newFleetBox(t)
	voucher2 := newFleetBox(t)
	roster := newTestRoster(subject, voucher1, voucher2)

	now := time.Now()
	requestID := "revoke-lost-key"
	fp := Fingerprint(pubDER)
	payloadHash := revocationBreakGlassPayloadHash(requestID, fp)

	certs := []fleetid.VouchCert{
		vouchFor(t, voucher1, subject.vulaID, payloadHash, now),
		vouchFor(t, voucher2, subject.vulaID, payloadHash, now),
	}

	cert, err := BreakGlassRevoke(ks, store, "lost device", subject.vulaID, requestID, certs, roster, fleetid.MinThreshold, now)
	if err != nil {
		t.Fatalf("BreakGlassRevoke: %v", err)
	}
	if !store.IsRevoked(cert.Fingerprint) {
		t.Fatal("break-glass revocation was not recorded")
	}
}

// TestBreakGlassRevoke_InsufficientQuorumRejected proves a box cannot revoke
// (or, by the same mechanism, un-revoke expectations around) its own key via
// break-glass without a genuine quorum of OTHER boxes.
func TestBreakGlassRevoke_InsufficientQuorumRejected(t *testing.T) {
	ks := newTestSoftwareStore(t)
	store := newTestRevocationStore(t)
	pubDER, _ := ks.DeviceIdentity()

	subject := newFleetBox(t)
	voucher1 := newFleetBox(t)
	roster := newTestRoster(subject, voucher1)

	now := time.Now()
	requestID := "revoke-attempt"
	fp := Fingerprint(pubDER)
	payloadHash := revocationBreakGlassPayloadHash(requestID, fp)

	certs := []fleetid.VouchCert{
		vouchFor(t, voucher1, subject.vulaID, payloadHash, now),
	}

	_, err := BreakGlassRevoke(ks, store, "reason", subject.vulaID, requestID, certs, roster, fleetid.MinThreshold, now)
	if err == nil {
		t.Fatal("expected BreakGlassRevoke to fail with insufficient quorum")
	}
	if store.IsRevoked(fp) {
		t.Fatal("key must not be revoked when quorum was insufficient")
	}
}

func TestBreakGlassRevoke_SelfVouchNeverCounts(t *testing.T) {
	ks := newTestSoftwareStore(t)
	store := newTestRevocationStore(t)
	pubDER, _ := ks.DeviceIdentity()

	subject := newFleetBox(t)
	roster := newTestRoster(subject)

	now := time.Now()
	requestID := "self-revoke-attempt"
	fp := Fingerprint(pubDER)
	payloadHash := revocationBreakGlassPayloadHash(requestID, fp)

	selfVouch := vouchFor(t, subject, subject.vulaID, payloadHash, now)
	certs := []fleetid.VouchCert{selfVouch, selfVouch}

	_, err := BreakGlassRevoke(ks, store, "reason", subject.vulaID, requestID, certs, roster, fleetid.MinThreshold, now)
	if err == nil {
		t.Fatal("expected BreakGlassRevoke to reject a self-vouched quorum")
	}
	if store.IsRevoked(fp) {
		t.Fatal("key must not be revoked from a self-vouched 'quorum'")
	}
}

// ─── Propagation (gossip merge) ──────────────────────────────────────────────────

func TestMergeRevocationBatch_MergesValidSkipsInvalid(t *testing.T) {
	// Two independent devices, each with their own key.
	ksA := newTestSoftwareStore(t)
	storeA := newTestRevocationStore(t) // box A's own store, self-revokes itself

	certA, err := SelfRevoke(ksA, storeA, "box A retiring")
	if err != nil {
		t.Fatalf("SelfRevoke: %v", err)
	}

	// A forged/tampered cert that must NOT merge.
	forged := *certA
	forged.Fingerprint = "not-the-real-fingerprint"

	// box B receives a batch containing both certA (valid) and forged (invalid).
	storeB := newTestRevocationStore(t)
	batch := &RevocationBatch{Revocations: []*DeviceRevocationCert{certA, &forged}}

	merged, errs := MergeRevocationBatch(storeB, batch, nil, 0, time.Now())
	if merged != 1 {
		t.Fatalf("merged = %d, want 1 (only the valid cert)", merged)
	}
	if len(errs) != 1 {
		t.Fatalf("expected exactly 1 error for the forged cert, got %d: %v", len(errs), errs)
	}
	if !storeB.IsRevoked(certA.Fingerprint) {
		t.Fatal("box B should now know box A's key is revoked")
	}
	if storeB.IsRevoked("not-the-real-fingerprint") {
		t.Fatal("the forged cert must never be merged")
	}
}

func TestMergeRevocationBatch_RoundTripsThroughWire(t *testing.T) {
	ks := newTestSoftwareStore(t)
	store := newTestRevocationStore(t)
	cert, err := SelfRevoke(ks, store, "reason")
	if err != nil {
		t.Fatalf("SelfRevoke: %v", err)
	}

	batch := &RevocationBatch{Revocations: store.List()}
	wire, err := MarshalRevocationBatch(batch)
	if err != nil {
		t.Fatalf("MarshalRevocationBatch: %v", err)
	}
	got, err := UnmarshalRevocationBatch(wire)
	if err != nil {
		t.Fatalf("UnmarshalRevocationBatch: %v", err)
	}

	peerStore := newTestRevocationStore(t)
	merged, errs := MergeRevocationBatch(peerStore, got, nil, 0, time.Now())
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if merged != 1 {
		t.Fatalf("merged = %d, want 1", merged)
	}
	if !peerStore.IsRevoked(cert.Fingerprint) {
		t.Fatal("peer did not learn the revocation via the wire round-trip")
	}
}
