package snapshot

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
)

// ErrIntegrity is returned when a snapshot fails verification. A restore that
// hits it has NOT mutated the box (verification runs fully before any swap).
var ErrIntegrity = errors.New("snapshot: integrity check failed")

// RestoreResult reports what a restore did.
type RestoreResult struct {
	RestoredID       string `json:"restored_id"`
	ObjectsWritten   int    `json:"objects_written"`
	ObjectsDeleted   int    `json:"objects_deleted"`
	SafetySnapshotID string `json:"safety_snapshot_id,omitempty"`
}

// RestoreOptions tunes a restore.
type RestoreOptions struct {
	// SkipSafetySnapshot disables the pre-restore snapshot of the current state.
	// Off by default — a safety snapshot is taken so a bad restore is reversible.
	SkipSafetySnapshot bool
}

// Verify checks a snapshot end to end WITHOUT mutating anything: the manifest
// hash must match the index, every referenced blob must exist AND its
// decompressed content must hash to the recorded value, and every key must be
// path-traversal-safe. Any failure returns an error (ErrIntegrity for content
// problems). This is the gate restore runs before touching the box.
func (s *Snapshotter) Verify(ctx context.Context, id string) error {
	idx, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	if idx.SourcePrefix != s.dataPfx {
		return fmt.Errorf("%w: snapshot source prefix %q != box data prefix %q", ErrIntegrity, idx.SourcePrefix, s.dataPfx)
	}
	m, err := s.loadManifest(ctx, idx) // hash-checked inside
	if err != nil {
		return err
	}
	for _, e := range m.Entries {
		if _, err := safeLiveKey(s.dataPfx, e.Key); err != nil {
			return fmt.Errorf("%w: %v", ErrIntegrity, err)
		}
		if !validBlobHash(e.SHA256) {
			return fmt.Errorf("%w: bad blob hash for %q", ErrIntegrity, e.Key)
		}
		got, _, err := s.readBlob(ctx, e.SHA256)
		if err != nil {
			return fmt.Errorf("%w: blob %s for %q: %v", ErrIntegrity, e.SHA256, e.Key, err)
		}
		if got != e.SHA256 {
			return fmt.Errorf("%w: blob content hash mismatch for %q (want %s got %s)", ErrIntegrity, e.Key, e.SHA256, got)
		}
	}
	return nil
}

// Restore rolls the box back to snapshot id, FAIL-CLOSED:
//  1. Verify the whole snapshot; abort untouched on any integrity failure.
//  2. Take a pre-restore safety snapshot of the current live state (unless
//     opted out) so a bad restore is itself reversible.
//  3. Write every verified object to its live key, then delete live objects not
//     present in the snapshot, leaving the box exactly at the snapshot state.
func (s *Snapshotter) Restore(ctx context.Context, id string, opts RestoreOptions) (*RestoreResult, error) {
	// (1) Verify BEFORE anything is mutated.
	if err := s.Verify(ctx, id); err != nil {
		return nil, err
	}
	idx, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	m, err := s.loadManifest(ctx, idx)
	if err != nil {
		return nil, err
	}

	res := &RestoreResult{RestoredID: id}

	// (2) Safety snapshot of the current state so a bad restore is reversible.
	if !opts.SkipSafetySnapshot {
		safety, err := s.Create(ctx, KindPreRestore, id)
		if err != nil {
			return nil, fmt.Errorf("snapshot: pre-restore safety snapshot: %w", err)
		}
		res.SafetySnapshotID = safety.ID
	}

	// (3a) Write every snapshot object to its live key. Each blob is re-verified
	// as it is streamed (defense in depth beyond the Verify pass).
	want := make(map[string]struct{}, len(m.Entries))
	for _, e := range m.Entries {
		full, err := safeLiveKey(s.dataPfx, e.Key)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrIntegrity, err) // unreachable post-Verify
		}
		want[full] = struct{}{}
		got, raw, err := s.readBlob(ctx, e.SHA256)
		if err != nil {
			return nil, fmt.Errorf("snapshot: restore read blob %s: %w", e.SHA256, err)
		}
		if got != e.SHA256 {
			return nil, fmt.Errorf("%w: blob changed under restore for %q", ErrIntegrity, e.Key)
		}
		if err := s.store.Put(ctx, full, bytes.NewReader(raw), int64(len(raw)), ""); err != nil {
			return nil, fmt.Errorf("snapshot: restore write %q: %w", full, err)
		}
		res.ObjectsWritten++
	}

	// (3b) Delete live objects that are not part of the snapshot (roll back
	// additions). The reserved snapshot area is never touched.
	live, err := s.liveObjects(ctx)
	if err != nil {
		return nil, fmt.Errorf("snapshot: restore reconcile list: %w", err)
	}
	for _, o := range live {
		if _, keep := want[o.Key]; keep {
			continue
		}
		if err := s.store.Delete(ctx, o.Key); err != nil {
			return nil, fmt.Errorf("snapshot: restore delete %q: %w", o.Key, err)
		}
		res.ObjectsDeleted++
	}
	return res, nil
}

// readBlob fetches a content-addressed blob, decompresses it, and returns the
// hash of the decompressed content plus the bytes.
func (s *Snapshotter) readBlob(ctx context.Context, hash string) (string, []byte, error) {
	rc, err := s.store.Get(ctx, s.blobKey(hash))
	if err != nil {
		return "", nil, err
	}
	defer rc.Close()
	comp, err := io.ReadAll(rc)
	if err != nil {
		return "", nil, err
	}
	raw, err := gunzipBytes(comp)
	if err != nil {
		return "", nil, err
	}
	return sha256hex(raw), raw, nil
}
