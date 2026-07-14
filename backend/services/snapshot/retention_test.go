package snapshot

import (
	"context"
	"testing"
	"time"
)

// createAt creates a snapshot with a specific CreatedAt by swapping the clock.
func createAt(t *testing.T, s *Snapshotter, m *memStore, ts time.Time, changed string) *Index {
	t.Helper()
	s.WithClock(atClock(ts))
	// Mutate one object so each snapshot differs (exercises incremental path).
	if changed != "" {
		m.set(dataPfx+"rolling.txt", []byte(changed))
	}
	idx, err := s.Create(context.Background(), KindScheduled, "")
	if err != nil {
		t.Fatalf("create @ %s: %v", ts, err)
	}
	return idx
}

// Rule: retention prunes correctly (grandfather-father-son) and always keeps
// the newest snapshot.
func TestRetentionPrunesCorrectly(t *testing.T) {
	ctx := context.Background()
	m := newMemStore()
	seedLive(m, map[string]string{"rolling.txt": "seed"})
	s := newSnap(m)

	base := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	// 10 daily snapshots, one per day going back.
	var ids []string
	for i := 0; i < 10; i++ {
		ts := base.AddDate(0, 0, -i)
		idx := createAt(t, s, m, ts, "day-"+ts.Format("0102"))
		ids = append(ids, idx.ID)
	}

	// Keep 3 daily, 0 weekly.
	pol := Policy{KeepDaily: 3, KeepWeekly: 0}
	res, err := s.Prune(ctx, pol)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	// 10 distinct days → keep 3 most-recent days, prune 7.
	if len(res.Kept) != 3 {
		t.Fatalf("kept = %d, want 3 (%v)", len(res.Kept), res.Kept)
	}
	if len(res.Pruned) != 7 {
		t.Fatalf("pruned = %d, want 7", len(res.Pruned))
	}

	// The newest snapshot must survive.
	remaining, _ := s.List(ctx)
	if len(remaining) != 3 {
		t.Fatalf("remaining snapshots = %d, want 3", len(remaining))
	}
	newest := ids[0]
	found := false
	for _, r := range remaining {
		if r.ID == newest {
			found = true
		}
	}
	if !found {
		t.Fatalf("newest snapshot %s was pruned", newest)
	}

	// Pruned snapshots' index+manifest must be gone; blob GC must have run.
	for _, id := range res.Pruned {
		if _, ok := m.getRaw(s.indexKey(id)); ok {
			t.Fatalf("pruned index %s still present", id)
		}
	}
}

// Blob GC must NOT delete a blob still referenced by a surviving snapshot, and
// MUST reclaim blobs no longer referenced by anyone.
func TestBlobGCKeepsSharedBlobsReclaimsOrphans(t *testing.T) {
	ctx := context.Background()
	m := newMemStore()
	// "stable.txt" never changes → its blob is shared by every snapshot.
	seedLive(m, map[string]string{"stable.txt": "constant", "rolling.txt": "r0"})
	s := newSnap(m)

	base := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		ts := base.AddDate(0, 0, -i)
		createAt(t, s, m, ts, "roll-"+ts.Format("0102"))
	}

	blobPfx := dataPfx + artifactMarker + "blobs/"
	blobsBefore := m.count(blobPfx)

	// Keep only the newest day.
	res, err := s.Prune(ctx, Policy{KeepDaily: 1, KeepWeekly: 0})
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if res.BlobsDeleted == 0 {
		t.Fatalf("expected orphaned rolling.txt blobs to be reclaimed")
	}
	blobsAfter := m.count(blobPfx)
	if blobsAfter >= blobsBefore {
		t.Fatalf("GC did not reclaim blobs: before=%d after=%d", blobsBefore, blobsAfter)
	}

	// The surviving snapshot must still be fully restorable — the shared
	// "stable.txt" blob must not have been GC'd out from under it.
	remaining, _ := s.List(ctx)
	if len(remaining) != 1 {
		t.Fatalf("want 1 surviving snapshot, got %d", len(remaining))
	}
	if err := s.Verify(ctx, remaining[0].ID); err != nil {
		t.Fatalf("surviving snapshot no longer verifies after GC: %v", err)
	}
}

// GC is fail-closed: if a surviving manifest cannot be loaded, prune aborts
// rather than delete blobs it cannot prove are unreferenced.
func TestPruneGCFailClosedOnUnreadableSurvivor(t *testing.T) {
	ctx := context.Background()
	m := newMemStore()
	seedLive(m, map[string]string{"rolling.txt": "r0"})
	s := newSnap(m)

	base := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	var kept *Index
	for i := 0; i < 3; i++ {
		ts := base.AddDate(0, 0, -i)
		idx := createAt(t, s, m, ts, "roll-"+ts.Format("0102"))
		if i == 0 {
			kept = idx // newest, will survive
		}
	}
	// Corrupt the SURVIVING snapshot's manifest so GC cannot enumerate its blobs.
	m.set(kept.ManifestKey, []byte("garbage"))

	blobPfx := dataPfx + artifactMarker + "blobs/"
	blobsBefore := m.count(blobPfx)
	if _, err := s.Prune(ctx, Policy{KeepDaily: 1, KeepWeekly: 0}); err == nil {
		t.Fatal("prune should fail-closed when a surviving manifest is unreadable")
	}
	if got := m.count(blobPfx); got != blobsBefore {
		t.Fatalf("fail-closed GC still deleted blobs: before=%d after=%d", blobsBefore, got)
	}
}

// Pure policy planning is deterministic and keeps the newest.
func TestPolicyPlanKeepsNewest(t *testing.T) {
	base := time.Date(2026, 7, 14, 0, 0, 0, 0, time.UTC)
	var idx []Index
	for i := 0; i < 6; i++ {
		idx = append(idx, Index{ID: "s" + time.Duration(i).String(), CreatedAt: base.AddDate(0, 0, -i)})
	}
	keep, prune := Policy{KeepDaily: 2, KeepWeekly: 0}.plan(idx)
	if len(keep) != 2 || len(prune) != 4 {
		t.Fatalf("plan keep=%d prune=%d, want 2/4", len(keep), len(prune))
	}
}
