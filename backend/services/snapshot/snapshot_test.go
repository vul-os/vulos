package snapshot

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

const dataPfx = "os/"

func newSnap(store ObjectStore) *Snapshotter {
	// Deterministic clock advanced by tests via WithClock closures where needed.
	return New(store, Config{DataPrefix: "os", AccountID: "acct-1"})
}

// seedLive writes a set of live objects under the data prefix.
func seedLive(m *memStore, kv map[string]string) {
	for k, v := range kv {
		m.set(dataPfx+k, []byte(v))
	}
}

// liveState returns the current live (non-artifact) objects as key→content.
func liveState(t *testing.T, m *memStore) map[string]string {
	t.Helper()
	out := map[string]string{}
	objs, _ := m.List(context.Background(), dataPfx)
	for _, o := range objs {
		if strings.HasPrefix(o.Key, dataPfx+artifactMarker) {
			continue
		}
		b, _ := m.getRaw(o.Key)
		out[strings.TrimPrefix(o.Key, dataPfx)] = string(b)
	}
	return out
}

// Rule: incremental snapshot only stores CHANGED objects (unchanged content is
// not re-uploaded; identical content is deduped to a single blob).
func TestIncrementalOnlyStoresChangedObjects(t *testing.T) {
	ctx := context.Background()
	m := newMemStore()
	seedLive(m, map[string]string{
		"a.txt":       "alpha",
		"b.txt":       "beta",
		"dup/one.txt": "same-content",
		"dup/two.txt": "same-content", // identical → should dedupe to ONE blob
	})
	s := newSnap(m)

	first, err := s.Create(ctx, KindManual, "")
	if err != nil {
		t.Fatalf("first snapshot: %v", err)
	}
	if first.ObjectCount != 4 {
		t.Fatalf("want 4 objects captured, got %d", first.ObjectCount)
	}
	// 3 distinct contents → 3 blobs (dedupe proven).
	blobPfx := dataPfx + artifactMarker + "blobs/"
	if got := m.count(blobPfx); got != 3 {
		t.Fatalf("want 3 deduped blobs, got %d", got)
	}

	// Snapshot again with NO changes: no new blobs uploaded at all.
	putsBefore := 0
	m.mu.Lock()
	for k, n := range m.putCount {
		if strings.HasPrefix(k, blobPfx) {
			putsBefore += n
		}
	}
	m.mu.Unlock()

	if _, err := s.Create(ctx, KindManual, first.ID); err != nil {
		t.Fatalf("second snapshot: %v", err)
	}
	putsAfter := 0
	m.mu.Lock()
	for k, n := range m.putCount {
		if strings.HasPrefix(k, blobPfx) {
			putsAfter += n
		}
	}
	m.mu.Unlock()
	if putsAfter != putsBefore {
		t.Fatalf("unchanged snapshot re-uploaded blobs: puts before=%d after=%d", putsBefore, putsAfter)
	}
	if got := m.count(blobPfx); got != 3 {
		t.Fatalf("blob count changed after no-op snapshot: got %d", got)
	}

	// Change one object; only its (new) blob is added.
	m.set(dataPfx+"a.txt", []byte("ALPHA-changed"))
	third, err := s.Create(ctx, KindManual, "")
	if err != nil {
		t.Fatalf("third snapshot: %v", err)
	}
	if third.BlobBytesAdded == 0 {
		t.Fatalf("changed snapshot should add bytes for the new blob")
	}
	if got := m.count(blobPfx); got != 4 { // 3 old + 1 new content
		t.Fatalf("want 4 blobs after one change, got %d", got)
	}
}

// Rule: restore returns the box to the snapshot state (writes back removed/
// changed objects and deletes objects added after the snapshot).
func TestRestoreReturnsToSnapshotState(t *testing.T) {
	ctx := context.Background()
	m := newMemStore()
	seedLive(m, map[string]string{
		"keep.txt":    "v1",
		"delete.txt":  "will-be-removed",
		"change.txt":  "original",
		"nested/x.md": "hello",
	})
	s := newSnap(m)

	snap, err := s.Create(ctx, KindManual, "")
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}

	// Mutate live state after the snapshot.
	m.set(dataPfx+"change.txt", []byte("MUTATED"))
	m.Delete(ctx, dataPfx+"keep.txt")                    // removed a file that existed
	m.set(dataPfx+"added-after.txt", []byte("new-junk")) // added a new file
	delete(m.data, dataPfx+"delete.txt")                 // also remove another

	res, err := s.Restore(ctx, snap.ID, RestoreOptions{})
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if res.SafetySnapshotID == "" {
		t.Fatalf("restore must take a pre-restore safety snapshot by default")
	}

	got := liveState(t, m)
	want := map[string]string{
		"keep.txt":    "v1",
		"delete.txt":  "will-be-removed",
		"change.txt":  "original",
		"nested/x.md": "hello",
	}
	if len(got) != len(want) {
		t.Fatalf("live object count = %d, want %d (%v)", len(got), len(want), got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("after restore %q = %q, want %q", k, got[k], v)
		}
	}
	if _, ok := got["added-after.txt"]; ok {
		t.Fatalf("restore did not delete object added after the snapshot")
	}
}

// Rule: integrity failure ABORTS restore, fail-closed (box untouched).
func TestIntegrityFailureAbortsRestore(t *testing.T) {
	ctx := context.Background()
	m := newMemStore()
	seedLive(m, map[string]string{"a.txt": "alpha", "b.txt": "beta"})
	s := newSnap(m)

	snap, err := s.Create(ctx, KindManual, "")
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}

	// Corrupt a blob's stored content so its hash no longer matches.
	blobPfx := dataPfx + artifactMarker + "blobs/"
	objs, _ := m.List(ctx, blobPfx)
	if len(objs) == 0 {
		t.Fatal("no blobs written")
	}
	m.set(objs[0].Key, []byte("corrupted-not-gzip"))

	// Mutate the live state so we can prove restore did NOT run.
	m.set(dataPfx+"a.txt", []byte("MUTATED"))

	// Verify must fail-closed.
	if err := s.Verify(ctx, snap.ID); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("Verify should return ErrIntegrity on corrupt blob, got %v", err)
	}
	// Restore must abort and NOT touch the live state or take a safety snapshot.
	before := liveState(t, m)
	if _, err := s.Restore(ctx, snap.ID, RestoreOptions{}); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("Restore should abort with ErrIntegrity, got %v", err)
	}
	after := liveState(t, m)
	if after["a.txt"] != "MUTATED" {
		t.Fatalf("restore mutated live state despite integrity failure: a.txt=%q", after["a.txt"])
	}
	if len(before) != len(after) {
		t.Fatalf("live object set changed under aborted restore")
	}
}

// A tampered manifest hash must be rejected fail-closed.
func TestTamperedManifestRejected(t *testing.T) {
	ctx := context.Background()
	m := newMemStore()
	seedLive(m, map[string]string{"a.txt": "alpha"})
	s := newSnap(m)
	snap, err := s.Create(ctx, KindManual, "")
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	// Overwrite the manifest object with different bytes → hash mismatch.
	m.set(snap.ManifestKey, []byte("\x1f\x8b\x08tampered"))
	if err := s.Verify(ctx, snap.ID); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("tampered manifest should fail integrity, got %v", err)
	}
}

// Path-traversal: a crafted manifest key must never write outside the data prefix.
func TestSafeLiveKeyRejectsTraversal(t *testing.T) {
	bad := []string{
		"../escape",
		"a/../../etc/passwd",
		"/absolute",
		"",
		"_snapshots/blobs/x",
		"_snapshots/",
		"a/../b",
		"nested/../../out",
	}
	for _, r := range bad {
		if _, err := safeLiveKey(dataPfx, r); err == nil {
			t.Fatalf("safeLiveKey accepted traversal key %q", r)
		}
	}
	good := map[string]string{
		"a.txt":       dataPfx + "a.txt",
		"nested/x.md": dataPfx + "nested/x.md",
	}
	for r, want := range good {
		got, err := safeLiveKey(dataPfx, r)
		if err != nil || got != want {
			t.Fatalf("safeLiveKey(%q) = %q,%v; want %q", r, got, err, want)
		}
	}
}

// Metering: snapshot creation reports the bytes it adds to storage, and
// StorageUsage measures what is actually stored.
func TestMeteringCountsSnapshotStorage(t *testing.T) {
	ctx := context.Background()
	m := newMemStore()
	seedLive(m, map[string]string{"a.txt": "alpha", "b.txt": "beta"})

	meter := &captureMeter{}
	s := newSnap(m).WithMeter(meter)

	idx, err := s.Create(ctx, KindManual, "")
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if meter.total <= 0 {
		t.Fatalf("meter recorded no bytes")
	}
	if meter.account != "acct-1" {
		t.Fatalf("meter account = %q, want acct-1", meter.account)
	}
	// Metered bytes should equal blob bytes added + manifest + index for this run.
	// StorageUsage should be >= the blob+manifest+index bytes actually stored.
	u, err := s.StorageUsage(ctx)
	if err != nil {
		t.Fatalf("usage: %v", err)
	}
	if u.TotalBytes <= 0 || u.BlobCount != 2 || u.IndexCount != 1 {
		t.Fatalf("unexpected usage: %+v", u)
	}
	if u.TotalBytes < idx.BlobBytesAdded {
		t.Fatalf("usage total %d < blob bytes added %d", u.TotalBytes, idx.BlobBytesAdded)
	}
}

type captureMeter struct {
	total   int64
	account string
}

func (c *captureMeter) MeterSnapshotBytes(_ context.Context, account string, bytes int64) {
	c.total += bytes
	c.account = account
}

// atClock returns a clock function yielding a fixed time.
func atClock(ts time.Time) func() time.Time { return func() time.Time { return ts } }
