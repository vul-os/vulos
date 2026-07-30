package sync

// Tests for the latest.json authenticity gap (anti-rollback authenticity):
// cluster/snapshot/latest.json lives in a bucket every node's shared S3
// credentials can write, so `Version` alone is not a trustworthy anti-rollback
// counter — anyone who can reach the bucket (but not necessarily anyone who
// holds the cluster passphrase) can write it directly, bypassing the
// Compactor/lease entirely.
//
// Two attacks are modeled here, matching the two real consumers of the doc:
//
//   - TestCompactorIgnoresForgedHighVersion: an attacker sets an absurdly high
//     Version to freeze the cluster (every legitimate snapshot looks "not
//     newer" forever). The fix must NOT let an unauthenticated Version block a
//     legitimate compaction.
//   - TestRestoreRejectsForgedLatestDoc: an attacker points Key/Version at a
//     stale or malicious blob to force a rollback on restore. The fix must
//     fail closed: Restore must refuse rather than apply it.
//
// The *RefusesRollbackWhenPassphraseMissing tests below cover the residual that
// the first pass left open: the authenticity check used to be conditional on a
// MAC key being configured, so a blank Passphrase / a missing WithLatestMACKey
// silently skipped it and every attack above landed again. Those tests plant an
// EMPTY passphrase (the forgotten-field case, not an attack) and assert the
// rollback is refused; they fail against the pre-fix behaviour.
//
// Both simulate the attacker by writing directly to the mock bucket via
// PutEncrypted — i.e. NOT through the Compactor/lease — exactly the capability
// "anyone who can write that object" describes.

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"sync"
	"testing"
	"time"
)

const testClusterPassphrase = "correct-cluster-passphrase-only-legit-nodes-have-this"

// testMACKey is the latest.json MAC key for testClusterPassphrase, derived ONCE
// per test binary. deriveLatestMACKey runs Argon2id at the production work
// factor (64 MiB, t=3, ~0.3s+ per call under load), so helpers that re-derived
// it per call made this package several times slower for no extra coverage.
// The Compactor/Bootstrap paths still derive it for real from their Passphrase
// field, so the config→key wiring stays covered.
var testMACKey = sync.OnceValue(func() []byte { return deriveLatestMACKey(testClusterPassphrase) })

// TestCompactorIgnoresForgedHighVersion proves a forged latest.json with an
// absurd Version cannot freeze the cluster: a Compactor configured with the
// real cluster passphrase must still write a legitimate, lower-versioned
// snapshot because the forged doc's MAC does not authenticate.
func TestCompactorIgnoresForgedHighVersion(t *testing.T) {
	ctx := context.Background()
	s3 := newMockSnapshotS3()

	// ── Attacker: write latest.json directly (bucket access, no passphrase). ──
	forged := LatestDoc{
		Version:   math.MaxInt64,
		Key:       snapshotBlobKey(999999999), // never actually written
		CreatedAt: time.Now().UTC(),
		// No MAC — the attacker doesn't hold the cluster passphrase.
	}
	forgedJSON, err := json.Marshal(forged)
	if err != nil {
		t.Fatalf("marshal forged doc: %v", err)
	}
	if err := s3.PutEncrypted(ctx, latestKey, forgedJSON); err != nil {
		t.Fatalf("inject forged latest.json: %v", err)
	}

	// ── Legitimate compactor, real passphrase, real (much lower) version. ─────
	compactor := NewCompactor(
		CompactorConfig{NodeID: "legit-node", Passphrase: testClusterPassphrase},
		newFakeLeaseFacade(),
		s3,
		simpleFakeSnapshot([]byte("legit-db-image"), 5),
	)
	if err := compactor.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// The legitimate blob MUST have been written — a forged, unauthenticated
	// high Version must not have frozen compaction.
	if !s3.has(snapshotBlobKey(5)) {
		t.Fatal("legitimate snapshot blob (version=5) was never written — forged Version froze the cluster")
	}

	// latest.json must now point at the legitimate version, MAC'd correctly.
	data, err := s3.GetEncrypted(ctx, latestKey)
	if err != nil {
		t.Fatalf("read latest.json after Run: %v", err)
	}
	var doc LatestDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parse latest.json after Run: %v", err)
	}
	if doc.Version != 5 {
		t.Fatalf("latest.json version after Run = %d, want 5 (forged version must not have stuck)", doc.Version)
	}
	if !verifyLatestDoc(testMACKey(), doc) {
		t.Fatal("latest.json written by the legitimate compactor does not carry a valid MAC")
	}
}

// TestRestoreRejectsForgedLatestDoc proves Restore refuses to apply a
// latest.json an attacker wrote directly to the bucket without the cluster
// passphrase — even though it points at a real, readable blob. The rehydrate
// callback must never be invoked.
func TestRestoreRejectsForgedLatestDoc(t *testing.T) {
	ctx := context.Background()
	s3 := newMockSnapshotS3()

	// A blob exists in the bucket (e.g. a genuinely stale prior snapshot, or
	// attacker-supplied content) that the forged pointer will target.
	staleKey := snapshotBlobKey(1)
	if err := s3.PutEncrypted(ctx, staleKey, []byte("stale-or-malicious-content")); err != nil {
		t.Fatalf("seed stale blob: %v", err)
	}

	// ── Attacker: write latest.json directly, pointing at that blob. ─────────
	forged := LatestDoc{
		Version:   1,
		Key:       staleKey,
		CreatedAt: time.Now().UTC(),
		// No MAC — the attacker doesn't hold the cluster passphrase.
	}
	forgedJSON, err := json.Marshal(forged)
	if err != nil {
		t.Fatalf("marshal forged doc: %v", err)
	}
	if err := s3.PutEncrypted(ctx, latestKey, forgedJSON); err != nil {
		t.Fatalf("inject forged latest.json: %v", err)
	}

	rehydrateCalled := false
	restorer := NewRestorer(s3, func(context.Context, []byte, int64) error {
		rehydrateCalled = true
		return nil
	}, WithLatestMACKey(testMACKey()))

	_, err = restorer.Restore(ctx)
	if !errors.Is(err, ErrSnapshotTampered) {
		t.Fatalf("Restore(forged doc) error = %v, want ErrSnapshotTampered", err)
	}
	if rehydrateCalled {
		t.Fatal("rehydrate was invoked on an unauthenticated latest.json — the forged/stale snapshot was applied")
	}
}

// TestRestoreAcceptsAuthenticSnapshot is the control: a Restorer configured
// with WithLatestMACKey must still restore a snapshot legitimately produced by
// a Compactor holding the same passphrase. This guards against the fix being
// so strict it breaks the happy path.
func TestRestoreAcceptsAuthenticSnapshot(t *testing.T) {
	ctx := context.Background()
	s3 := newMockSnapshotS3()

	compactor := NewCompactor(
		CompactorConfig{NodeID: "legit-node", Passphrase: testClusterPassphrase},
		newFakeLeaseFacade(),
		s3,
		simpleFakeSnapshot([]byte("legit-db-image"), 3),
	)
	if err := compactor.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}

	var gotVersion int64
	restorer := NewRestorer(s3, func(_ context.Context, _ []byte, version int64) error {
		gotVersion = version
		return nil
	}, WithLatestMACKey(testMACKey()))

	res, err := restorer.Restore(ctx)
	if err != nil {
		t.Fatalf("Restore of an authentic snapshot must succeed: %v", err)
	}
	if res.Version != 3 || gotVersion != 3 {
		t.Fatalf("restored version = %d/%d, want 3", res.Version, gotVersion)
	}
}

// ── Fail-closed: an ABSENT passphrase must refuse, not skip ──────────────────
//
// These three tests share one shape: the bucket holds an unauthenticated
// latest.json (exactly what an attacker with bucket write access can produce),
// and the consumer is configured with NO passphrase / NO MAC key — the
// forgotten-config case. Before the fail-closed fix each consumer treated
// "no key" as "nothing to check" and honoured the forged document; each
// assertion below fails against that behaviour.

// TestRestoreRefusesRollbackWhenPassphraseMissing is the destructive case: a
// forged latest.json points at a stale blob, and the Restorer was built without
// WithLatestMACKey (someone forgot the passphrase). The rollback must be
// refused.
//
// Against the old behaviour this test FAILS: r.macKey == nil skipped
// verifyLatestDoc entirely, Restore returned nil, and rehydrate was invoked
// with the attacker-chosen blob — the exact rollback the MAC exists to prevent.
func TestRestoreRefusesRollbackWhenPassphraseMissing(t *testing.T) {
	ctx := context.Background()
	s3 := newMockSnapshotS3()

	// A stale (or attacker-planted) blob the forged pointer will target.
	staleKey := snapshotBlobKey(1)
	if err := s3.PutEncrypted(ctx, staleKey, []byte("stale-or-malicious-content")); err != nil {
		t.Fatalf("seed stale blob: %v", err)
	}
	forged := LatestDoc{Version: 1, Key: staleKey, CreatedAt: time.Now().UTC()} // no MAC
	forgedJSON, err := json.Marshal(forged)
	if err != nil {
		t.Fatalf("marshal forged doc: %v", err)
	}
	if err := s3.PutEncrypted(ctx, latestKey, forgedJSON); err != nil {
		t.Fatalf("inject forged latest.json: %v", err)
	}

	rehydrateCalled := false
	// NOTE: no WithLatestMACKey — the empty-passphrase / forgotten-field case.
	restorer := NewRestorer(s3, func(context.Context, []byte, int64) error {
		rehydrateCalled = true
		return nil
	})

	_, err = restorer.Restore(ctx)
	if !errors.Is(err, ErrLatestAuthenticityUnconfigured) {
		t.Fatalf("Restore with no MAC key: error = %v, want ErrLatestAuthenticityUnconfigured", err)
	}
	if rehydrateCalled {
		t.Fatal("rehydrate ran with no MAC key configured — the unauthenticated rollback was applied (fail-open)")
	}

	// The status accessor must not launder the untrusted pointer either: an
	// admin UI reporting version/key from a doc nobody authenticated is the same
	// trust decision one screen earlier.
	if _, err := restorer.LatestSnapshot(ctx); !errors.Is(err, ErrLatestAuthenticityUnconfigured) {
		t.Fatalf("LatestSnapshot with no MAC key: error = %v, want ErrLatestAuthenticityUnconfigured", err)
	}
}

// TestCompactorRefusesWhenPassphraseMissing: a Compactor with an empty
// Passphrase must refuse to run rather than make an anti-rollback decision
// against a Version it cannot authenticate.
//
// Against the old behaviour this test FAILS: macKey == nil meant the forged
// MaxInt64 Version was trusted unconditionally, so Run logged "skipping" and
// returned nil — an attacker with only bucket write access could freeze
// compaction forever, and nothing in the return value said so.
func TestCompactorRefusesWhenPassphraseMissing(t *testing.T) {
	ctx := context.Background()
	s3 := newMockSnapshotS3()

	forged := LatestDoc{
		Version:   math.MaxInt64,
		Key:       snapshotBlobKey(999999999),
		CreatedAt: time.Now().UTC(),
	} // no MAC
	forgedJSON, err := json.Marshal(forged)
	if err != nil {
		t.Fatalf("marshal forged doc: %v", err)
	}
	if err := s3.PutEncrypted(ctx, latestKey, forgedJSON); err != nil {
		t.Fatalf("inject forged latest.json: %v", err)
	}

	// NOTE: Passphrase deliberately left empty — the forgotten-field case.
	compactor := NewCompactor(
		CompactorConfig{NodeID: "misconfigured-node"},
		newFakeLeaseFacade(),
		s3,
		simpleFakeSnapshot([]byte("legit-db-image"), 5),
	)

	err = compactor.Run(ctx)
	if !errors.Is(err, ErrLatestAuthenticityUnconfigured) {
		t.Fatalf("Run with empty Passphrase: error = %v, want ErrLatestAuthenticityUnconfigured", err)
	}
	// It must also not have published an unauthenticated latest.json of its own
	// (a doc with no MAC that every reader would later have to reject).
	if !s3.has(latestKey) {
		t.Fatal("test setup lost the injected latest.json")
	}
	data, err := s3.GetEncrypted(ctx, latestKey)
	if err != nil {
		t.Fatalf("read latest.json: %v", err)
	}
	var doc LatestDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parse latest.json: %v", err)
	}
	if doc.Version != math.MaxInt64 {
		t.Fatalf("latest.json was rewritten (version=%d) by a compactor that cannot MAC it", doc.Version)
	}
	if s3.has(snapshotBlobKey(5)) {
		t.Fatal("snapshot blob written despite the refusal — Run did not fail closed before doing work")
	}
}

// TestBootstrapRefusesRollbackWhenPassphraseMissing: joining with an empty
// Passphrase must refuse rather than install a snapshot chosen by an
// unauthenticated pointer as the new node's entire DB base.
//
// Against the old behaviour this test FAILS: macKey == nil skipped the check,
// so install() was called with the attacker-chosen blob.
func TestBootstrapRefusesRollbackWhenPassphraseMissing(t *testing.T) {
	ctx := context.Background()
	s3 := newMockSnapshotS3()

	staleKey := snapshotBlobKey(1)
	if err := s3.PutEncrypted(ctx, staleKey, []byte("stale-or-malicious-content")); err != nil {
		t.Fatalf("seed stale blob: %v", err)
	}
	forged := LatestDoc{Version: 1, Key: staleKey, CreatedAt: time.Now().UTC()} // no MAC
	forgedJSON, err := json.Marshal(forged)
	if err != nil {
		t.Fatalf("marshal forged doc: %v", err)
	}
	if err := s3.PutEncrypted(ctx, latestKey, forgedJSON); err != nil {
		t.Fatalf("inject forged latest.json: %v", err)
	}

	installCalled := false
	install := func(context.Context, []byte, int64) error {
		installCalled = true
		return nil
	}
	applyCalled := false
	apply := func(context.Context, BootstrapS3, string) error {
		applyCalled = true
		return nil
	}

	// NOTE: Passphrase deliberately left empty — the forgotten-field case.
	err = Bootstrap(ctx, BootstrapConfig{NodeID: "joiner-no-pass"}, s3, install, apply)
	if !errors.Is(err, ErrLatestAuthenticityUnconfigured) {
		t.Fatalf("Bootstrap with empty Passphrase: error = %v, want ErrLatestAuthenticityUnconfigured", err)
	}
	if installCalled {
		t.Fatal("install ran with no MAC key configured — an unauthenticated snapshot became the DB base (fail-open)")
	}
	if applyCalled {
		t.Fatal("changeset replay ran despite the refusal — Bootstrap did not fail closed before doing work")
	}
}

// TestVerifyLatestDocRejectsEmptyMACKey guards the primitive itself: the
// helper must never report success for a key it does not have. Callers gate on
// requireLatestMACKey, but the primitive returning "true" for an empty key
// would silently re-open the hole for any future caller that forgets.
func TestVerifyLatestDocRejectsEmptyMACKey(t *testing.T) {
	doc := LatestDoc{Version: 7, Key: snapshotBlobKey(7), CreatedAt: time.Now().UTC()}
	// Even when the attacker computes a MAC under a key of their choosing, an
	// empty verification key must not accept it.
	doc.MAC = latestDocMAC(deriveLatestMACKey("attacker-passphrase"), doc.Version, doc.Key, doc.CreatedAt)

	for name, key := range map[string][]byte{"nil": nil, "empty": {}} {
		if verifyLatestDoc(key, doc) {
			t.Errorf("verifyLatestDoc(%s macKey, ...) = true, want false", name)
		}
	}

	// And a doc with no MAC at all must not authenticate under a real key.
	unMACd := LatestDoc{Version: 7, Key: snapshotBlobKey(7), CreatedAt: doc.CreatedAt}
	if verifyLatestDoc(testMACKey(), unMACd) {
		t.Error("verifyLatestDoc accepted a doc with an empty MAC")
	}
}

// TestBootstrapFallsBackOnForgedLatestDoc proves the SYNC-03 join path treats
// a forged latest.json the same way it treats a malformed one: fall back to a
// full changeset replay rather than install untrusted state.
func TestBootstrapFallsBackOnForgedLatestDoc(t *testing.T) {
	ctx := context.Background()
	s3 := newMockSnapshotS3()

	staleKey := snapshotBlobKey(1)
	if err := s3.PutEncrypted(ctx, staleKey, []byte("stale-or-malicious-content")); err != nil {
		t.Fatalf("seed stale blob: %v", err)
	}
	forged := LatestDoc{Version: 1, Key: staleKey, CreatedAt: time.Now().UTC()}
	forgedJSON, err := json.Marshal(forged)
	if err != nil {
		t.Fatalf("marshal forged doc: %v", err)
	}
	if err := s3.PutEncrypted(ctx, latestKey, forgedJSON); err != nil {
		t.Fatalf("inject forged latest.json: %v", err)
	}

	installCalled := false
	install := func(context.Context, []byte, int64) error {
		installCalled = true
		return nil
	}
	applyCalled := false
	apply := func(context.Context, BootstrapS3, string) error {
		applyCalled = true
		return nil
	}

	err = Bootstrap(ctx, BootstrapConfig{NodeID: "joiner", Passphrase: testClusterPassphrase}, s3, install, apply)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	if installCalled {
		t.Fatal("install was invoked on an unauthenticated latest.json")
	}
	_ = applyCalled // no changesets seeded in this test; presence is not asserted
}
