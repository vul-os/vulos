package sync

// bootstrap_test.go — SYNC-03 unit tests for Bootstrap.
//
// All tests use the in-memory mockSnapshotS3 from snapshot_test.go (same
// package) and purely in-memory DBInstaller / ChangesetApplier — no real S3,
// no real SQLite, no network.
//
// Invariants exercised:
//   - snapshot present → snapshot installed + only tail changesets (version >
//     snapshotVersion) replayed.
//   - no snapshot (latest.json absent) → full changeset replay fallback,
//     install never called.
//   - empty bucket → no error, no install, no replay.
//   - multi-peer: tail filter applies across all peers.
//   - passphrase oracle: Bootstrap never receives a passphrase; no oracle
//     string appears in any S3 object key or value during the test.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// ── fake callbacks ────────────────────────────────────────────────────────────

type installCall struct {
	data            []byte
	snapshotVersion int64
}

type fakeInstaller struct {
	calls []installCall
}

func (f *fakeInstaller) Install(_ context.Context, data []byte, version int64) error {
	cp := make([]byte, len(data))
	copy(cp, data)
	f.calls = append(f.calls, installCall{data: cp, snapshotVersion: version})
	return nil
}

type fakeApplier struct {
	applied []string
}

func (f *fakeApplier) Apply(_ context.Context, _ BootstrapS3, key string) error {
	f.applied = append(f.applied, key)
	return nil
}

// ── helpers ───────────────────────────────────────────────────────────────────

// seedSnapshot writes a LatestDoc and the snapshot blob into a mock S3.
func seedSnapshot(t *testing.T, s3 *mockSnapshotS3, version int64, blobData []byte) {
	t.Helper()
	ctx := context.Background()
	blobKey := snapshotBlobKey(version)
	if err := s3.PutEncrypted(ctx, blobKey, blobData); err != nil {
		t.Fatalf("seed snapshot blob: %v", err)
	}
	doc := LatestDoc{Version: version, Key: blobKey}
	latestData, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("seed latest.json marshal: %v", err)
	}
	if err := s3.PutEncrypted(ctx, latestKey, latestData); err != nil {
		t.Fatalf("seed latest.json: %v", err)
	}
}

// parsedVersion extracts the db_version integer from a changeset key like
// "nodes/{id}/changes/{ver}.bin".
func parsedVersion(key string) int64 {
	parts := strings.SplitN(key, "/", 4)
	if len(parts) != 4 {
		return -1
	}
	vStr := strings.TrimSuffix(parts[3], ".bin")
	var ver int64
	fmt.Sscanf(vStr, "%d", &ver)
	return ver
}

// ── Tests ─────────────────────────────────────────────────────────────────────

// TestBootstrap_SnapshotInstalled_TailReplayed verifies the happy path:
// snapshot blob is installed and only changesets above snapshotVersion are
// replayed.
func TestBootstrap_SnapshotInstalled_TailReplayed(t *testing.T) {
	ctx := context.Background()
	s3 := newMockSnapshotS3()

	const snapVer = int64(10)
	snapBlob := []byte("fake-snapshot-db-v10")
	seedSnapshot(t, s3, snapVer, snapBlob)

	// Seed changesets: versions 5, 10 (covered by snapshot) and 11, 15, 20 (tail).
	for _, v := range []int64{5, 10, 11, 15, 20} {
		seedChangeset(s3, "peer-A", v)
	}

	installer := &fakeInstaller{}
	applier := &fakeApplier{}

	if err := Bootstrap(ctx, BootstrapConfig{NodeID: "new-node"}, s3, installer.Install, applier.Apply); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}

	// Snapshot must have been installed exactly once with the correct data.
	if len(installer.calls) != 1 {
		t.Fatalf("expected 1 install call, got %d", len(installer.calls))
	}
	if string(installer.calls[0].data) != string(snapBlob) {
		t.Errorf("installed data: got %q, want %q", installer.calls[0].data, snapBlob)
	}
	if installer.calls[0].snapshotVersion != snapVer {
		t.Errorf("installed version: got %d, want %d", installer.calls[0].snapshotVersion, snapVer)
	}

	// Versions ≤ snapshotVersion must NOT have been replayed.
	for _, key := range applier.applied {
		ver := parsedVersion(key)
		if ver <= snapVer {
			t.Errorf("changeset version %d replayed but should be skipped (≤ snapshot %d): key=%s",
				ver, snapVer, key)
		}
	}

	// Versions 11, 15, 20 MUST have been replayed.
	replayed := make(map[int64]bool)
	for _, key := range applier.applied {
		replayed[parsedVersion(key)] = true
	}
	for _, want := range []int64{11, 15, 20} {
		if !replayed[want] {
			t.Errorf("version %d expected in replay but not found", want)
		}
	}
	if len(applier.applied) != 3 {
		t.Errorf("expected exactly 3 tail changesets replayed, got %d: %v", len(applier.applied), applier.applied)
	}
}

// TestBootstrap_NoSnapshot_FullReplay verifies the fallback: when latest.json
// is absent, all changesets are replayed and install is never called.
func TestBootstrap_NoSnapshot_FullReplay(t *testing.T) {
	ctx := context.Background()
	s3 := newMockSnapshotS3()
	// No latest.json seeded → no snapshot.

	for _, v := range []int64{1, 5, 9} {
		seedChangeset(s3, "peer-B", v)
	}

	installer := &fakeInstaller{}
	applier := &fakeApplier{}

	if err := Bootstrap(ctx, BootstrapConfig{NodeID: "new-no-snap"}, s3, installer.Install, applier.Apply); err != nil {
		t.Fatalf("Bootstrap (no snapshot): %v", err)
	}

	// Install must NOT have been called.
	if len(installer.calls) != 0 {
		t.Errorf("install called %d times, expected 0 (no snapshot)", len(installer.calls))
	}

	// All 3 changesets must have been replayed.
	if len(applier.applied) != 3 {
		t.Errorf("expected 3 changesets replayed, got %d: %v", len(applier.applied), applier.applied)
	}
}

// TestBootstrap_EmptyBucket_NoError verifies that an empty bucket (no snapshot,
// no changesets) returns without error and calls nothing.
func TestBootstrap_EmptyBucket_NoError(t *testing.T) {
	ctx := context.Background()
	s3 := newMockSnapshotS3()

	installer := &fakeInstaller{}
	applier := &fakeApplier{}

	if err := Bootstrap(ctx, BootstrapConfig{NodeID: "new-empty"}, s3, installer.Install, applier.Apply); err != nil {
		t.Fatalf("Bootstrap on empty bucket: %v", err)
	}
	if len(installer.calls) != 0 {
		t.Errorf("unexpected install calls: %d", len(installer.calls))
	}
	if len(applier.applied) != 0 {
		t.Errorf("unexpected changesets replayed: %v", applier.applied)
	}
}

// TestBootstrap_MultiPeer_OnlyTailReplayed verifies that the tail filter works
// across multiple peers.
func TestBootstrap_MultiPeer_OnlyTailReplayed(t *testing.T) {
	ctx := context.Background()
	s3 := newMockSnapshotS3()

	const snapVer = int64(7)
	seedSnapshot(t, s3, snapVer, []byte("snap-v7"))

	// peer-X: v3 (covered), v8 (tail), v12 (tail)
	for _, v := range []int64{3, 8, 12} {
		seedChangeset(s3, "peer-X", v)
	}
	// peer-Y: v7 (covered), v9 (tail)
	for _, v := range []int64{7, 9} {
		seedChangeset(s3, "peer-Y", v)
	}

	installer := &fakeInstaller{}
	applier := &fakeApplier{}

	if err := Bootstrap(ctx, BootstrapConfig{NodeID: "joiner"}, s3, installer.Install, applier.Apply); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}

	if len(installer.calls) != 1 {
		t.Fatalf("expected 1 install call, got %d", len(installer.calls))
	}

	// Expected tail: peer-X/8, peer-X/12, peer-Y/9 → 3 changesets total.
	if len(applier.applied) != 3 {
		t.Errorf("expected 3 tail changesets, got %d: %v", len(applier.applied), applier.applied)
	}
	for _, key := range applier.applied {
		if ver := parsedVersion(key); ver <= snapVer {
			t.Errorf("changeset version %d should not be replayed (≤ snapshot %d): %s",
				ver, snapVer, key)
		}
	}
}

// TestBootstrap_PassphraseNeverPersisted asserts that Bootstrap never receives
// a passphrase and no oracle string appears in any S3 object.
// (The invariant is structural: BootstrapConfig has no passphrase field, and
// SnapshotS3 is the post-derivation interface.)
func TestBootstrap_PassphraseNeverPersisted(t *testing.T) {
	ctx := context.Background()
	s3 := newMockSnapshotS3()

	const oracle = "SYNC03-PASSPHRASE-ORACLE-never-persist"

	// Seed a snapshot whose data deliberately does NOT contain the oracle.
	seedSnapshot(t, s3, 5, []byte("clean-snapshot-data"))
	seedChangeset(s3, "peer-C", 6)

	installer := &fakeInstaller{}
	applier := &fakeApplier{}

	if err := Bootstrap(ctx, BootstrapConfig{NodeID: "sec-node"}, s3, installer.Install, applier.Apply); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}

	// Verify no S3 object key or value contains the oracle.
	s3.mu.Lock()
	defer s3.mu.Unlock()
	for key, val := range s3.objects {
		if strings.Contains(key, oracle) {
			t.Errorf("passphrase oracle found in S3 key %q", key)
		}
		if strings.Contains(string(val), oracle) {
			t.Errorf("passphrase oracle found in S3 value for key %q", key)
		}
	}

	// Verify the installer did not receive data containing the oracle.
	for i, call := range installer.calls {
		if strings.Contains(string(call.data), oracle) {
			t.Errorf("passphrase oracle found in installed snapshot data (call %d)", i)
		}
	}
}
