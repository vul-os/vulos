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
// Both simulate the attacker by writing directly to the mock bucket via
// PutEncrypted — i.e. NOT through the Compactor/lease — exactly the capability
// "anyone who can write that object" describes.

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"testing"
	"time"
)

const testClusterPassphrase = "correct-cluster-passphrase-only-legit-nodes-have-this"

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
	if !verifyLatestDoc(deriveLatestMACKey(testClusterPassphrase), doc) {
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
	}, WithLatestMACKey(deriveLatestMACKey(testClusterPassphrase)))

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
	}, WithLatestMACKey(deriveLatestMACKey(testClusterPassphrase)))

	res, err := restorer.Restore(ctx)
	if err != nil {
		t.Fatalf("Restore of an authentic snapshot must succeed: %v", err)
	}
	if res.Version != 3 || gotVersion != 3 {
		t.Fatalf("restored version = %d/%d, want 3", res.Version, gotVersion)
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
