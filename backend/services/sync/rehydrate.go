// Package sync — TOPO-01: RehydrateFromDurable.
//
// RehydrateFromDurable is the single, explicit OS-side entrypoint for
// reconstructing local state after a host/Machine loss.  It treats the local
// volume (Fly Volume, local SQLite) as a non-authoritative CACHE that may be
// stale, wiped, or absent.  The durable sources of truth are:
//
//  1. The SSE-C-encrypted snapshot in S3/Tigris (cluster/snapshot/latest.json +
//     the versioned blob it references).
//  2. The per-node changeset tail in S3/Tigris (nodes/*/changes/*.bin) that
//     records writes since the last compaction.
//
// RehydrateFromDurable discards any existing local state (via ClearLocalCache)
// before applying the durable snapshot + changeset tail, so callers never
// merge a corrupt or lagging local copy into the durable truth.
//
// This entrypoint builds on the existing Bootstrap function (SYNC-03) — it is
// not a parallel mechanism.  Bootstrap is the core algorithm; RehydrateFromDurable
// is the named entry-point that:
//   - makes the "wipe local, restore from durable" semantics explicit,
//   - surfaces a RehyResult so callers can log/report what was restored,
//   - and is the target of the TOPO-01 test.
package sync

import (
	"context"
	"errors"
	"fmt"
	"log"
)

// ClearLocalCache is the caller-supplied function that wipes any existing
// local state before rehydration begins.  Implementations typically truncate
// or delete the local SQLite file / in-memory store.
//
// If ClearLocalCache returns an error, RehydrateFromDurable aborts before
// touching the durable sources — the local cache is left in whatever state it
// was in.
type ClearLocalCache func(ctx context.Context) error

// RehyResult reports what RehydrateFromDurable did.
type RehyResult struct {
	// SnapshotVersion is the cr-sqlite db_version covered by the installed
	// snapshot blob.  0 means no snapshot was present (full-replay fallback).
	SnapshotVersion int64
	// ChangesetsTailCount is the number of per-node changeset objects replayed
	// on top of the snapshot (or the total replayed when no snapshot exists).
	ChangesetsTailCount int
	// FromEmpty is true when the bucket contained neither a snapshot nor any
	// changesets (bucket is brand-new / account never written anything).
	FromEmpty bool
}

// RehydrateConfig holds the parameters for RehydrateFromDurable.
type RehydrateConfig struct {
	// NodeID identifies the rehydrating instance (used for logging only).
	NodeID string

	// ClearLocalCache is called first to discard any stale local state.
	// Must not be nil.
	ClearLocalCache ClearLocalCache

	// Install installs the snapshot blob as the new local DB base.
	// Same as Bootstrap's DBInstaller parameter — passed through unchanged.
	Install DBInstaller

	// Applier downloads and applies one changeset object from S3.
	// Same as Bootstrap's ChangesetApplier parameter — passed through unchanged.
	Applier ChangesetApplier
}

// RehydrateFromDurable reconstructs local state from the durable S3/Tigris
// bucket, treating the local volume as a non-authoritative cache.
//
//  1. ClearLocalCache() — discard stale/wiped local state.
//  2. Bootstrap(snapshot + changeset tail) — restore from the durable copy.
//
// s3 is the SSE-C-encrypted S3 client (BootstrapS3 interface; *cluster.Client
// satisfies this; the in-memory mockSnapshotS3 satisfies it for tests).
// Neither RehydrateFromDurable nor Bootstrap ever sees the raw passphrase.
func RehydrateFromDurable(ctx context.Context, cfg RehydrateConfig, s3 BootstrapS3) (RehyResult, error) {
	if cfg.ClearLocalCache == nil {
		return RehyResult{}, errors.New("rehydrate: ClearLocalCache must not be nil")
	}
	if cfg.Install == nil {
		return RehyResult{}, errors.New("rehydrate: Install must not be nil")
	}
	if cfg.Applier == nil {
		return RehyResult{}, errors.New("rehydrate: Applier must not be nil")
	}

	nodeID := cfg.NodeID
	if nodeID == "" {
		nodeID = "unknown"
	}

	log.Printf("[rehydrate/%s] starting rehydration — discarding local cache", nodeID)

	// ── 1. Wipe local cache — it is not authoritative ─────────────────────────
	if err := cfg.ClearLocalCache(ctx); err != nil {
		return RehyResult{}, fmt.Errorf("rehydrate: clear local cache: %w", err)
	}

	log.Printf("[rehydrate/%s] local cache cleared — restoring from durable bucket", nodeID)

	// ── 2. Track changeset counts via wrapping applier ────────────────────────
	var changesetCount int
	countingApplier := ChangesetApplier(func(ctx context.Context, s3 BootstrapS3, key string) error {
		if err := cfg.Applier(ctx, s3, key); err != nil {
			return err
		}
		changesetCount++
		return nil
	})

	// Track snapshot install via wrapping installer.
	var snapshotVersion int64
	trackingInstall := DBInstaller(func(ctx context.Context, data []byte, version int64) error {
		if err := cfg.Install(ctx, data, version); err != nil {
			return err
		}
		snapshotVersion = version
		return nil
	})

	// ── 3. Bootstrap from snapshot + changeset tail ───────────────────────────
	// Bootstrap is the core algorithm (SYNC-03 / bootstrap.go).  It reads
	// latest.json, installs the snapshot blob (if present), and replays the
	// changeset tail above the snapshot version.  We pass the same s3 client
	// so the entire restore happens through the SSE-C-encrypted interface.
	bCfg := BootstrapConfig{NodeID: nodeID}
	if err := Bootstrap(ctx, bCfg, s3, trackingInstall, countingApplier); err != nil {
		return RehyResult{}, fmt.Errorf("rehydrate: bootstrap from durable: %w", err)
	}

	result := RehyResult{
		SnapshotVersion:     snapshotVersion,
		ChangesetsTailCount: changesetCount,
		FromEmpty:           snapshotVersion == 0 && changesetCount == 0,
	}

	log.Printf("[rehydrate/%s] rehydration complete: snapshot_version=%d changeset_tail=%d from_empty=%v",
		nodeID, result.SnapshotVersion, result.ChangesetsTailCount, result.FromEmpty)

	return result, nil
}
