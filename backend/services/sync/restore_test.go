package sync

// SYNC-03 tests: Restorer — the inverse of the Compactor. A snapshot written to
// the (mock, in-memory) bucket is downloaded and rehydrated into fresh local
// state, proving a backup → restore roundtrip without touching real S3.

import (
	"bytes"
	"context"
	"errors"
	"testing"
)

// TestRestoreRoundTrip backs up a blob via the Compactor, then restores it via
// the Restorer into a fresh sink, asserting the bytes + version survive.
func TestRestoreRoundTrip(t *testing.T) {
	ctx := context.Background()
	s3 := newMockSnapshotS3()

	// ── Backup: compact a known blob at version 7. ──────────────────────────
	want := []byte("merged-db-image-bytes-\x00\x01\x02-binary-safe")
	compactor := NewCompactor(
		CompactorConfig{NodeID: "node-A"},
		newFakeLeaseFacade(),
		s3,
		simpleFakeSnapshot(want, 7),
	)
	if err := compactor.Run(ctx); err != nil {
		t.Fatalf("compaction: %v", err)
	}

	// ── Restore: rehydrate into a fresh sink. ───────────────────────────────
	var (
		gotData    []byte
		gotVersion int64
	)
	restorer := NewRestorer(s3, func(_ context.Context, data []byte, version int64) error {
		gotData = append([]byte(nil), data...)
		gotVersion = version
		return nil
	})
	res, err := restorer.Restore(ctx)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}

	if !bytes.Equal(gotData, want) {
		t.Fatalf("restored bytes differ:\n got=%q\nwant=%q", gotData, want)
	}
	if gotVersion != 7 {
		t.Fatalf("restored version = %d, want 7", gotVersion)
	}
	if res.Version != 7 || res.Bytes != len(want) {
		t.Fatalf("RestoreResult = %+v, want version=7 bytes=%d", res, len(want))
	}
	if res.Key == "" {
		t.Fatal("RestoreResult.Key is empty")
	}
}

// TestRestoreNoSnapshot returns ErrNoSnapshot on a bucket that was never
// backed up.
func TestRestoreNoSnapshot(t *testing.T) {
	ctx := context.Background()
	s3 := newMockSnapshotS3()
	restorer := NewRestorer(s3, func(context.Context, []byte, int64) error { return nil })

	_, err := restorer.Restore(ctx)
	if !errors.Is(err, ErrNoSnapshot) {
		t.Fatalf("Restore on empty bucket: got %v, want ErrNoSnapshot", err)
	}
}

// TestRestoreLatestOfMultiple restores the CURRENT snapshot pointer (latest.json)
// even when older snapshot blobs remain in the bucket.
func TestRestoreLatestOfMultiple(t *testing.T) {
	ctx := context.Background()
	s3 := newMockSnapshotS3()

	// Two successive compactions: version 3 then version 9. latest.json must
	// point at 9.
	c3 := NewCompactor(CompactorConfig{NodeID: "n"}, newFakeLeaseFacade(), s3, simpleFakeSnapshot([]byte("v3"), 3))
	if err := c3.Run(ctx); err != nil {
		t.Fatalf("compact v3: %v", err)
	}
	c9 := NewCompactor(CompactorConfig{NodeID: "n"}, newFakeLeaseFacade(), s3, simpleFakeSnapshot([]byte("v9-newer-image"), 9))
	if err := c9.Run(ctx); err != nil {
		t.Fatalf("compact v9: %v", err)
	}

	restorer := NewRestorer(s3, func(context.Context, []byte, int64) error { return nil })
	res, err := restorer.Restore(ctx)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if res.Version != 9 {
		t.Fatalf("restored version = %d, want the latest (9)", res.Version)
	}
}

// TestRestorePropagatesRehydrateError surfaces a rehydrate failure to the caller
// (so a bad restore is not silently swallowed).
func TestRestorePropagatesRehydrateError(t *testing.T) {
	ctx := context.Background()
	s3 := newMockSnapshotS3()
	c := NewCompactor(CompactorConfig{NodeID: "n"}, newFakeLeaseFacade(), s3, simpleFakeSnapshot([]byte("data"), 1))
	if err := c.Run(ctx); err != nil {
		t.Fatalf("compact: %v", err)
	}

	sentinel := errors.New("apply failed")
	restorer := NewRestorer(s3, func(context.Context, []byte, int64) error { return sentinel })
	if _, err := restorer.Restore(ctx); !errors.Is(err, sentinel) {
		t.Fatalf("Restore must propagate rehydrate error; got %v", err)
	}
}

// TestLatestSnapshotParsesPointer checks the LatestSnapshot accessor returns the
// parsed pointer doc.
func TestLatestSnapshotParsesPointer(t *testing.T) {
	ctx := context.Background()
	s3 := newMockSnapshotS3()
	c := NewCompactor(CompactorConfig{NodeID: "n"}, newFakeLeaseFacade(), s3, simpleFakeSnapshot([]byte("xyz"), 42))
	if err := c.Run(ctx); err != nil {
		t.Fatalf("compact: %v", err)
	}
	restorer := NewRestorer(s3, func(context.Context, []byte, int64) error { return nil })
	doc, err := restorer.LatestSnapshot(ctx)
	if err != nil {
		t.Fatalf("LatestSnapshot: %v", err)
	}
	if doc.Version != 42 || doc.Key == "" {
		t.Fatalf("LatestSnapshot doc = %+v, want version=42 + non-empty key", doc)
	}
}
