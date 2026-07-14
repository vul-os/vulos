package snapshot

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// Policy is a grandfather-father-son retention policy. The newest snapshot is
// always kept regardless of the counts.
type Policy struct {
	KeepDaily  int // keep the most recent snapshot for each of the last N days
	KeepWeekly int // keep the most recent snapshot for each of the last M weeks
}

// DefaultPolicy keeps a week of dailies and a month of weeklies.
var DefaultPolicy = Policy{KeepDaily: 7, KeepWeekly: 4}

// PruneResult reports what a prune did.
type PruneResult struct {
	Kept           []string `json:"kept"`
	Pruned         []string `json:"pruned"`
	BlobsDeleted   int      `json:"blobs_deleted"`
	BytesReclaimed int64    `json:"bytes_reclaimed"`
}

// plan partitions indexes (any order) into keep/prune id sets per the policy.
// It is pure and deterministic so it can be unit-tested without a store.
func (p Policy) plan(indexes []Index) (keep, prune []string) {
	if len(indexes) == 0 {
		return nil, nil
	}
	sorted := make([]Index, len(indexes))
	copy(sorted, indexes)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].CreatedAt.After(sorted[j].CreatedAt) })

	keepSet := map[string]bool{}
	seenDay := map[string]bool{}
	seenWeek := map[string]bool{}

	// Newest snapshot is always kept.
	keepSet[sorted[0].ID] = true

	for _, idx := range sorted {
		day := idx.CreatedAt.UTC().Format("2006-01-02")
		if len(seenDay) < p.KeepDaily && !seenDay[day] {
			seenDay[day] = true
			keepSet[idx.ID] = true
		}
		y, w := idx.CreatedAt.UTC().ISOWeek()
		wk := fmt.Sprintf("%d-%02d", y, w)
		if len(seenWeek) < p.KeepWeekly && !seenWeek[wk] {
			seenWeek[wk] = true
			keepSet[idx.ID] = true
		}
	}

	for _, idx := range sorted {
		if keepSet[idx.ID] {
			keep = append(keep, idx.ID)
		} else {
			prune = append(prune, idx.ID)
		}
	}
	return keep, prune
}

// Prune applies the retention policy: pruned snapshots' index+manifest objects
// are removed, then a mark-and-sweep GC deletes blobs no longer referenced by
// ANY surviving snapshot.
//
// GC is fail-closed: if a surviving snapshot's manifest cannot be loaded, the
// sweep aborts rather than risk deleting a blob that snapshot still needs.
func (s *Snapshotter) Prune(ctx context.Context, p Policy) (*PruneResult, error) {
	indexes, err := s.List(ctx)
	if err != nil {
		return nil, err
	}
	keep, prune := p.plan(indexes)
	res := &PruneResult{Kept: keep, Pruned: prune}

	byID := map[string]Index{}
	for _, idx := range indexes {
		byID[idx.ID] = idx
	}

	// Delete pruned snapshots' manifest + index. Blobs are handled by GC below.
	for _, id := range prune {
		idx := byID[id]
		if err := s.store.Delete(ctx, idx.ManifestKey); err != nil {
			return nil, fmt.Errorf("snapshot: prune manifest %s: %w", id, err)
		}
		if err := s.store.Delete(ctx, s.indexKey(id)); err != nil {
			return nil, fmt.Errorf("snapshot: prune index %s: %w", id, err)
		}
	}

	// Mark: collect every blob hash referenced by a SURVIVING snapshot.
	referenced := map[string]bool{}
	for _, id := range keep {
		idx := byID[id]
		m, err := s.loadManifest(ctx, &idx) // hash-checked
		if err != nil {
			// Fail closed: we cannot prove which blobs this snapshot needs.
			return nil, fmt.Errorf("snapshot: prune GC aborted — cannot load surviving manifest %s: %w", id, err)
		}
		for _, e := range m.Entries {
			referenced[e.SHA256] = true
		}
	}

	// Sweep: delete blobs not referenced by any survivor.
	blobs, err := s.store.List(ctx, s.snapPfx+"blobs/")
	if err != nil {
		return nil, err
	}
	for _, b := range blobs {
		hash := blobHashFromKey(b.Key)
		if hash == "" || referenced[hash] {
			continue
		}
		if err := s.store.Delete(ctx, b.Key); err != nil {
			return nil, fmt.Errorf("snapshot: prune GC delete %q: %w", b.Key, err)
		}
		res.BlobsDeleted++
		res.BytesReclaimed += b.Size
	}
	return res, nil
}

// blobHashFromKey extracts the trailing hash element of a blob key
// (".../blobs/ab/<hash>"), returning "" for a key that is not a valid blob.
func blobHashFromKey(key string) string {
	i := strings.LastIndex(key, "/")
	if i < 0 {
		return ""
	}
	h := key[i+1:]
	if !validBlobHash(h) {
		return ""
	}
	return h
}
