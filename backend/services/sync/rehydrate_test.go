package sync

// TOPO-01 tests: RehydrateFromDurable — wipe local, restore from durable bucket.
//
// Uses the same in-memory mocks (mockSnapshotS3, seedSnapshot, seedChangeset,
// fakeInstaller, fakeApplier) already present in snapshot_test.go and
// bootstrap_test.go (all in the same package).
//
// Simulates host kill by discarding the "local DB" (tracked via fakeLocalCache)
// and rebuilding it from the durable bucket snapshot + changeset tail, using
// two in-process stores.

import (
	"bytes"
	"context"
	"errors"
	"testing"
)

// ── in-process local cache simulation ─────────────────────────────────────────

// fakeLocalCache simulates the local (non-authoritative) state on a volume.
// Clearing it is equivalent to a host kill that wipes the local volume.
type fakeLocalCache struct {
	data     []byte
	version  int64
	cleared  bool
	clearErr error
}

func (c *fakeLocalCache) clear(_ context.Context) error {
	if c.clearErr != nil {
		return c.clearErr
	}
	c.data = nil
	c.version = 0
	c.cleared = true
	return nil
}

func (c *fakeLocalCache) install(_ context.Context, data []byte, version int64) error {
	cp := make([]byte, len(data))
	copy(cp, data)
	c.data = cp
	c.version = version
	return nil
}

// ── Tests ─────────────────────────────────────────────────────────────────────

// TestRehydrateFromDurable_WipedCacheRestoresFromSnapshot verifies the core
// TOPO-01 AC: an instance with a wiped local cache rehydrates to the correct
// state from the bucket snapshot + CRDT changeset tail.
//
// Simulates host kill by starting with a "full" local cache (version=5) that
// is discarded, then rehydrated from a bucket snapshot at version=10 plus
// two tail changesets at versions 11 and 12.
func TestRehydrateFromDurable_WipedCacheRestoresFromSnapshot(t *testing.T) {
	ctx := context.Background()
	s3 := newMockSnapshotS3()

	// ── Durable state: snapshot at version 10 + tail changesets 11 and 12 ────
	durableBlob := []byte("durable-db-state-v10")
	seedSnapshot(t, s3, 10, durableBlob)
	seedChangeset(s3, "peer-A", 11)
	seedChangeset(s3, "peer-A", 12)

	// ── Local cache: simulate a stale/wiped volume ───────────────────────────
	local := &fakeLocalCache{
		data:    []byte("stale-local-state-v5"), // stale data from before the host kill
		version: 5,
	}

	// Record changesets applied to the local store.
	var appliedKeys []string
	applier := ChangesetApplier(func(_ context.Context, s3 BootstrapS3, key string) error {
		appliedKeys = append(appliedKeys, key)
		return nil
	})

	result, err := RehydrateFromDurable(ctx, RehydrateConfig{
		NodeID:          "rehydrate-node-1",
		ClearLocalCache: local.clear,
		Install:         local.install,
		Applier:         applier,
	}, s3)
	if err != nil {
		t.Fatalf("RehydrateFromDurable: %v", err)
	}

	// ── Local cache was wiped ─────────────────────────────────────────────────
	if !local.cleared {
		t.Error("local cache was not cleared (ClearLocalCache not called)")
	}

	// ── Snapshot installed from durable bucket ────────────────────────────────
	if !bytes.Equal(local.data, durableBlob) {
		t.Errorf("local data after rehydrate:\n got=%q\nwant=%q", local.data, durableBlob)
	}
	if local.version != 10 {
		t.Errorf("local version after rehydrate = %d, want 10", local.version)
	}
	if result.SnapshotVersion != 10 {
		t.Errorf("RehyResult.SnapshotVersion = %d, want 10", result.SnapshotVersion)
	}

	// ── Changeset tail replayed ───────────────────────────────────────────────
	if result.ChangesetsTailCount != 2 {
		t.Errorf("RehyResult.ChangesetsTailCount = %d, want 2", result.ChangesetsTailCount)
	}
	if len(appliedKeys) != 2 {
		t.Errorf("changeset applier called %d times, want 2: %v", len(appliedKeys), appliedKeys)
	}
	if result.FromEmpty {
		t.Error("RehyResult.FromEmpty must be false when snapshot + changesets exist")
	}
}

// TestRehydrateFromDurable_NoSnapshot_FullReplayFallback verifies that when no
// snapshot is present (bucket is new or no compaction has run), RehydrateFromDurable
// falls back to replaying all changesets — and still wipes the local cache first.
func TestRehydrateFromDurable_NoSnapshot_FullReplayFallback(t *testing.T) {
	ctx := context.Background()
	s3 := newMockSnapshotS3()
	// No snapshot seeded.
	seedChangeset(s3, "peer-B", 1)
	seedChangeset(s3, "peer-B", 2)
	seedChangeset(s3, "peer-B", 3)

	local := &fakeLocalCache{data: []byte("old"), version: 1}

	var installCalled bool
	install := DBInstaller(func(_ context.Context, _ []byte, _ int64) error {
		installCalled = true
		return nil
	})

	var appliedCount int
	applier := ChangesetApplier(func(_ context.Context, s3 BootstrapS3, key string) error {
		appliedCount++
		return nil
	})

	result, err := RehydrateFromDurable(ctx, RehydrateConfig{
		NodeID:          "rehydrate-node-2",
		ClearLocalCache: local.clear,
		Install:         install,
		Applier:         applier,
	}, s3)
	if err != nil {
		t.Fatalf("RehydrateFromDurable (no snapshot): %v", err)
	}

	// Cache cleared even in the no-snapshot path.
	if !local.cleared {
		t.Error("local cache was not cleared even when no snapshot exists")
	}
	// Install must NOT have been called (no snapshot).
	if installCalled {
		t.Error("Install called with no snapshot in bucket")
	}
	// All 3 changesets replayed.
	if appliedCount != 3 {
		t.Errorf("changeset applier called %d times, want 3", appliedCount)
	}
	if result.SnapshotVersion != 0 {
		t.Errorf("SnapshotVersion = %d, want 0 (no snapshot)", result.SnapshotVersion)
	}
	if result.ChangesetsTailCount != 3 {
		t.Errorf("ChangesetsTailCount = %d, want 3", result.ChangesetsTailCount)
	}
	if result.FromEmpty {
		t.Error("FromEmpty must be false when changesets exist")
	}
}

// TestRehydrateFromDurable_EmptyBucket verifies that an empty bucket (account
// has never written data) results in a clean, empty local state with no errors.
func TestRehydrateFromDurable_EmptyBucket(t *testing.T) {
	ctx := context.Background()
	s3 := newMockSnapshotS3() // empty bucket
	local := &fakeLocalCache{data: []byte("leftover"), version: 99}

	result, err := RehydrateFromDurable(ctx, RehydrateConfig{
		NodeID:          "rehydrate-node-3",
		ClearLocalCache: local.clear,
		Install:         local.install,
		Applier:         ChangesetApplier(func(_ context.Context, _ BootstrapS3, _ string) error { return nil }),
	}, s3)
	if err != nil {
		t.Fatalf("RehydrateFromDurable (empty bucket): %v", err)
	}

	if !local.cleared {
		t.Error("local cache was not cleared on empty bucket rehydration")
	}
	if !result.FromEmpty {
		t.Errorf("FromEmpty must be true for an empty bucket; got result=%+v", result)
	}
	if result.SnapshotVersion != 0 || result.ChangesetsTailCount != 0 {
		t.Errorf("unexpected result for empty bucket: %+v", result)
	}
}

// TestRehydrateFromDurable_ClearError_Aborts verifies that a ClearLocalCache
// failure prevents any restore attempt (don't corrupt a partially-wiped cache).
func TestRehydrateFromDurable_ClearError_Aborts(t *testing.T) {
	ctx := context.Background()
	s3 := newMockSnapshotS3()
	seedSnapshot(t, s3, 5, []byte("snap"))

	clearErr := errors.New("disk I/O error")
	local := &fakeLocalCache{clearErr: clearErr}

	var installCalled bool
	_, err := RehydrateFromDurable(ctx, RehydrateConfig{
		NodeID:          "rehydrate-node-4",
		ClearLocalCache: local.clear,
		Install: DBInstaller(func(_ context.Context, _ []byte, _ int64) error {
			installCalled = true
			return nil
		}),
		Applier: ChangesetApplier(func(_ context.Context, _ BootstrapS3, _ string) error { return nil }),
	}, s3)

	if !errors.Is(err, clearErr) {
		t.Errorf("expected ClearLocalCache error to propagate; got %v", err)
	}
	if installCalled {
		t.Error("Install must not be called after ClearLocalCache fails")
	}
}

// TestRehydrateFromDurable_LocalCacheIsNonAuthoritative demonstrates that
// two in-process stores converge to the SAME durable state regardless of
// what the local store had before the rehydration.  This directly validates
// that the local volume is treated as cache, not truth.
func TestRehydrateFromDurable_LocalCacheIsNonAuthoritative(t *testing.T) {
	ctx := context.Background()

	// Shared durable bucket — represents S3/Tigris.
	durableBucket := newMockSnapshotS3()
	durableBlob := []byte("shared-durable-snapshot-v7")
	seedSnapshot(t, durableBucket, 7, durableBlob)
	seedChangeset(durableBucket, "peer-Z", 8)

	// instance1: starts with a fresh/empty local store (simulate new machine).
	instance1 := &fakeLocalCache{}
	// instance2: starts with stale data (simulate a host that was killed mid-write).
	instance2 := &fakeLocalCache{data: []byte("corrupt-partial-write"), version: 3}

	var inst1Applied, inst2Applied int
	rehydrateInstance := func(local *fakeLocalCache, count *int) {
		_, err := RehydrateFromDurable(ctx, RehydrateConfig{
			NodeID:          "convergence-test",
			ClearLocalCache: local.clear,
			Install:         local.install,
			Applier: ChangesetApplier(func(_ context.Context, _ BootstrapS3, _ string) error {
				*count++
				return nil
			}),
		}, durableBucket)
		if err != nil {
			t.Errorf("RehydrateFromDurable: %v", err)
		}
	}

	rehydrateInstance(instance1, &inst1Applied)
	rehydrateInstance(instance2, &inst2Applied)

	// Both stores must converge to the same durable state.
	if !bytes.Equal(instance1.data, durableBlob) {
		t.Errorf("instance1 data mismatch: got %q want %q", instance1.data, durableBlob)
	}
	if !bytes.Equal(instance2.data, durableBlob) {
		t.Errorf("instance2 data mismatch: got %q want %q", instance2.data, durableBlob)
	}
	if instance1.version != 7 || instance2.version != 7 {
		t.Errorf("version mismatch: inst1=%d inst2=%d, both want 7", instance1.version, instance2.version)
	}
	if inst1Applied != inst2Applied || inst1Applied != 1 {
		t.Errorf("tail changeset counts differ: inst1=%d inst2=%d, both want 1", inst1Applied, inst2Applied)
	}
}
