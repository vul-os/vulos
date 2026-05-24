package crdt

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestBucketRoundTrip_Mem persists a document via the in-memory bucket
// and rebuilds a fresh Doc from the snapshot - asserts equivalence.
func TestBucketRoundTrip_Mem(t *testing.T) {
	ctx := context.Background()
	src := NewDoc(DocKindText, "session-1", "A")
	for _, r := range "Round trip!" {
		id := src.NextOpID()
		tb := src.Text().LocalInsert(len(src.Text().VisibleIDs()), r, id)
		if _, err := src.ApplyLocal(tb, id); err != nil {
			t.Fatal(err)
		}
	}
	snap, err := src.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	b := NewMemBucket()
	if err := b.SaveSnapshot(ctx, "session-1", snap); err != nil {
		t.Fatal(err)
	}
	got, ok, err := b.LoadSnapshot(ctx, "session-1")
	if err != nil || !ok {
		t.Fatalf("LoadSnapshot ok=%v err=%v", ok, err)
	}
	dst := NewDoc(DocKindText, "session-1", "B")
	if err := dst.Restore(got); err != nil {
		t.Fatal(err)
	}
	if src.Text().String() != dst.Text().String() {
		t.Fatalf("bucket round-trip diverged: src=%q dst=%q",
			src.Text().String(), dst.Text().String())
	}
}

// TestBucketRoundTrip_File persists a document via the FileBucket
// to a temp dir, then loads it from a separate FileBucket instance.
func TestBucketRoundTrip_File(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	b1, err := NewFileBucket(filepath.Join(tmp, "bucket"))
	if err != nil {
		t.Fatal(err)
	}
	d := NewDoc(DocKindGrid, "session-X", "A")
	for i := 0; i < 5; i++ {
		id := d.NextOpID()
		gb := d.Grid().Set(CellKey{Row: i, Col: 0}, "v", id)
		op, err := d.ApplyLocal(gb, id)
		if err != nil {
			t.Fatal(err)
		}
		if err := b1.AppendOp(ctx, "session-X", op); err != nil {
			t.Fatal(err)
		}
	}
	snap, err := d.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if err := b1.SaveSnapshot(ctx, "session-X", snap); err != nil {
		t.Fatal(err)
	}

	// Fresh bucket pointing at the same dir.
	b2, err := NewFileBucket(filepath.Join(tmp, "bucket"))
	if err != nil {
		t.Fatal(err)
	}
	got, ok, err := b2.LoadSnapshot(ctx, "session-X")
	if err != nil || !ok {
		t.Fatalf("LoadSnapshot ok=%v err=%v", ok, err)
	}
	ops, err := b2.LoadOps(ctx, "session-X")
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 5 {
		t.Fatalf("expected 5 logged ops, got %d", len(ops))
	}
	cold := NewDoc(DocKindGrid, "session-X", "C")
	if err := cold.Restore(got); err != nil {
		t.Fatal(err)
	}
	// Cold joiner sees identical cell state.
	if len(cold.Grid().Keys()) != 5 {
		t.Fatalf("cold joiner cell count diverged: %d", len(cold.Grid().Keys()))
	}
	// And the on-disk snapshot file actually exists.
	if _, err := os.Stat(filepath.Join(tmp, "bucket", "session-X.snapshot.json")); err != nil {
		t.Fatalf("snapshot file missing: %v", err)
	}
}

// TestColdJoinerFromBucket simulates a late peer joining a session:
// it boots from the bucket snapshot, then receives tail ops over the
// transport and converges with the originator.
func TestColdJoinerFromBucket(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	bucket := NewMemBucket()
	hub := NewLoopbackHub("cold")

	// Origin writes some text and persists a snapshot.
	origin := NewDoc(DocKindText, "cold", "ORIG")
	originTr := hub.Join("ORIG")
	defer originTr.Close()
	go func() { _ = origin.Run(ctx, originTr, bucket) }()

	for _, r := range "warm-prefix" {
		id := origin.NextOpID()
		tb := origin.Text().LocalInsert(len(origin.Text().VisibleIDs()), r, id)
		op, _ := origin.ApplyLocal(tb, id)
		if err := origin.Broadcast(ctx, op, originTr, bucket); err != nil {
			t.Fatal(err)
		}
	}
	snap, _ := origin.Snapshot()
	_ = bucket.SaveSnapshot(ctx, "cold", snap)

	// Cold joiner bootstraps from the bucket then receives live ops.
	cold := NewDoc(DocKindText, "cold", "COLD")
	coldTr := hub.Join("COLD")
	defer coldTr.Close()
	go func() { _ = cold.Run(ctx, coldTr, bucket) }()

	// Wait for Run() to load the snapshot.
	waitFor(t, func() bool { return cold.Text().String() == "warm-prefix" })

	// Origin appends "!" live; cold joiner must receive it via hub.
	id := origin.NextOpID()
	tb := origin.Text().LocalInsert(len(origin.Text().VisibleIDs()), '!', id)
	op, _ := origin.ApplyLocal(tb, id)
	if err := origin.Broadcast(ctx, op, originTr, bucket); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return cold.Text().String() == "warm-prefix!" })
	if origin.Text().String() != cold.Text().String() {
		t.Fatalf("cold joiner diverged: origin=%q cold=%q",
			origin.Text().String(), cold.Text().String())
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("condition not met within timeout")
}
