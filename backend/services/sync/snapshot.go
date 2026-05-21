// Package sync — SYNC-02: bucket snapshot/compaction of merged DB state.
//
// Compactor periodically writes a compacted, SSE-C-encrypted snapshot of the
// merged cr-sqlite DB state to:
//
//	cluster/snapshot/<version>.db.enc   — encrypted snapshot blob
//	cluster/snapshot/latest.json        — {version, key, created_at}
//
// Exactly one instance compacts at a time, guarded by the snapshot ownership
// lease (leases/snapshot.json, LEASE-01) with fencing so a stalled compactor
// cannot clobber a newer snapshot.  Per-node changesets below the snapshot's
// covered version are pruned after a successful compaction.
//
// The SSE-C key is derived in-process from a caller-supplied passphrase using
// Argon2id (matching the cluster/s3.go parameters).  The passphrase and derived
// key are NEVER persisted to disk or sent to the cloud control plane.
package sync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/argon2"

	"vulos/backend/services/lease"
)

// ── SSE-C key derivation (mirrors cluster/s3.go — same parameters) ───────────

// snapshotArgon2* parameters match cluster/s3.go so keys derived from the
// same passphrase+salt are identical across packages (forward-compatible).
const (
	snapshotArgonTime    uint32 = 3
	snapshotArgonMem     uint32 = 64 * 1024 // 64 MiB
	snapshotArgonThreads uint8  = 4
	snapshotArgonKeyLen  uint32 = 32
)

// deriveSnapshotKey derives a 32-byte AES key from passphrase+salt using
// Argon2id.  The passphrase is used only in-process; neither it nor the
// derived key is ever written to disk or sent to the control plane.
func deriveSnapshotKey(passphrase string, salt []byte) []byte {
	return argon2.IDKey([]byte(passphrase), salt, snapshotArgonTime, snapshotArgonMem, snapshotArgonThreads, snapshotArgonKeyLen)
}

// ── S3 abstraction (mockable) ─────────────────────────────────────────────────

// SnapshotS3 is the subset of S3 operations used by Compactor.
// *cluster.Client satisfies this interface via PutEncrypted / GetEncrypted /
// ListPrefix.  The mock in snapshot_test.go also satisfies it.
type SnapshotS3 interface {
	// PutEncrypted uploads data to bucket/key encrypted with SSE-C.
	PutEncrypted(ctx context.Context, key string, data []byte) error
	// GetEncrypted downloads and decrypts an SSE-C-encrypted object.
	GetEncrypted(ctx context.Context, key string) ([]byte, error)
	// ListPrefix returns object keys that start with prefix.
	ListPrefix(ctx context.Context, prefix string) ([]string, error)
	// DeleteObject removes an object from the bucket.
	DeleteObject(ctx context.Context, key string) error
}

// ── LeaseFacade — narrow interface for the lease manager ─────────────────────

// LeaseFacade is the subset of *lease.Manager operations used by Compactor.
// Declaring it here (rather than hard-coding *lease.Manager) lets tests inject
// a pure in-memory implementation without depending on the unexported
// lease.backend interface.
//
// Production use: wrap a *lease.Manager with LeaseManagerFacade.
// Test use: implement directly in snapshot_test.go.
type LeaseFacade interface {
	// AcquireSnapshot tries to acquire the snapshot lease for holderID.
	// Returns the fence token on success.
	// Returns lease.ErrNotHolder when another node holds the lease.
	// Returns lease.ErrLost on a CAS 412.
	AcquireSnapshot(ctx context.Context, holderID string, ttl time.Duration) (fence int64, err error)
	// ReleaseSnapshot releases the snapshot lease previously acquired.
	ReleaseSnapshot(ctx context.Context) error
}

// LeaseManagerFacade wraps a *lease.Manager and implements LeaseFacade.
// It stores the *lease.Held token internally so Release can use it.
//
// This is the production adapter.  Construct with NewLeaseManagerFacade.
type LeaseManagerFacade struct {
	mgr  *lease.Manager
	held *lease.Held
}

// NewLeaseManagerFacade wraps mgr as a LeaseFacade for the snapshot scope.
func NewLeaseManagerFacade(mgr *lease.Manager) *LeaseManagerFacade {
	return &LeaseManagerFacade{mgr: mgr}
}

// AcquireSnapshot acquires the snapshot ownership lease.
func (f *LeaseManagerFacade) AcquireSnapshot(ctx context.Context, holderID string, ttl time.Duration) (int64, error) {
	held, err := f.mgr.Acquire(ctx, snapshotLeapScope, holderID, ttl)
	if err != nil {
		return 0, err
	}
	f.held = held
	return held.Fence, nil
}

// ReleaseSnapshot releases the snapshot ownership lease.
func (f *LeaseManagerFacade) ReleaseSnapshot(ctx context.Context) error {
	if f.held == nil {
		return nil
	}
	err := f.mgr.Release(ctx, f.held)
	f.held = nil
	return err
}

// ── DBSnapshot — abstraction over the merged DB state ────────────────────────

// DBSnapshot is the caller-supplied function that captures the current merged
// cr-sqlite database state as a raw byte blob (e.g. a sqlite3_serialize
// snapshot or a serialised changeset bundle) up to the given version.
//
// The returned (data, version) tuple is what gets written to S3.  version MUST
// equal the highest db_version represented in data.
//
// Callers are responsible for ensuring the snapshot is consistent; Compactor
// treats it as opaque bytes.
type DBSnapshot func(ctx context.Context) (data []byte, version int64, err error)

// ── LatestDoc — the latest.json wire format ───────────────────────────────────

// LatestDoc is the JSON body written to cluster/snapshot/latest.json.
// It identifies which snapshot blob is current and what changeset version it
// covers so peers know which per-node changesets they still need.
type LatestDoc struct {
	// Version is the highest cr-sqlite db_version captured by this snapshot.
	Version int64 `json:"version"`
	// Key is the S3 object key of the snapshot blob.
	Key string `json:"key"`
	// CreatedAt is the wall-clock time this snapshot was written.
	CreatedAt time.Time `json:"created_at"`
}

// ── S3 key helpers ────────────────────────────────────────────────────────────

const (
	snapshotPrefix    = "cluster/snapshot/"
	latestKey         = "cluster/snapshot/latest.json"
	snapshotLeapScope = "snapshot" // → leases/snapshot.json
	changesNodePrefix = "nodes/"   // nodes/{id}/changes/{ver}.bin
)

// snapshotBlobKey returns the S3 key for a versioned snapshot blob.
func snapshotBlobKey(version int64) string {
	return fmt.Sprintf("%s%d.db.enc", snapshotPrefix, version)
}

// ── Compactor ─────────────────────────────────────────────────────────────────

// Compactor performs exactly one compaction cycle when Run is called.
// It acquires the snapshot lease, produces an encrypted snapshot, updates
// latest.json, and prunes per-node changesets below the covered version.
//
// Construct with NewCompactor; call Run(ctx) from a periodic timer.
type Compactor struct {
	nodeID   string
	leaseFcd LeaseFacade
	s3       SnapshotS3
	snapshot DBSnapshot
	leaseTTL time.Duration

	// lastSeenFence is the highest snapshot lease fence this Compactor has
	// successfully used.  It is an in-process guard against stale-fence writes.
	lastSeenFence int64
}

// CompactorConfig holds the parameters needed to build a Compactor.
type CompactorConfig struct {
	// NodeID uniquely identifies this node (used as the lease holder ID).
	NodeID string
	// LeaseTTL is the duration for which the snapshot lease is held.
	// Default: 5 minutes.
	LeaseTTL time.Duration
}

// NewCompactor creates a Compactor.
//
//   - leaseFcd should be a *LeaseManagerFacade (production) or a test double.
//   - s3 must be the encrypted cluster client (SSE-C key is managed by the
//     cluster.Client layer — the Compactor never receives or stores a passphrase).
//   - snapshot is the caller-supplied function that captures the current DB state.
func NewCompactor(cfg CompactorConfig, leaseFcd LeaseFacade, s3 SnapshotS3, snapshot DBSnapshot) *Compactor {
	ttl := cfg.LeaseTTL
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	nodeID := cfg.NodeID
	if nodeID == "" {
		nodeID = "unknown"
	}
	return &Compactor{
		nodeID:   nodeID,
		leaseFcd: leaseFcd,
		s3:       s3,
		snapshot: snapshot,
		leaseTTL: ttl,
	}
}

// Run performs one compaction cycle:
//
//  1. Acquire the snapshot lease (returns immediately if another node holds it).
//  2. Validate the fence token — abort if stale.
//  3. Capture the merged DB state via the DBSnapshot function.
//  4. Read latest.json; abort if the snapshot's version is not newer.
//  5. Write the encrypted snapshot blob (cluster/snapshot/<version>.db.enc).
//  6. Write latest.json.
//  7. Prune per-node changesets below the covered version.
//  8. Release the lease.
//
// Passphrase / SSE-C key: the s3 client supplied to NewCompactor must already
// have an in-process SSE-C key (via cluster.NewClient).  The Compactor never
// sees nor persists the passphrase itself — that responsibility belongs to the
// caller who constructs the cluster.Client.
func (c *Compactor) Run(ctx context.Context) error {
	// ── 1. Acquire the snapshot lease ────────────────────────────────────────
	fence, err := c.leaseFcd.AcquireSnapshot(ctx, c.nodeID, c.leaseTTL)
	if err != nil {
		if errors.Is(err, lease.ErrNotHolder) {
			log.Printf("[snapshot] lease held by another node — skipping compaction")
			return nil
		}
		if errors.Is(err, lease.ErrLost) {
			log.Printf("[snapshot] lost lease race (CAS 412) — skipping compaction")
			return nil
		}
		return fmt.Errorf("snapshot: acquire lease: %w", err)
	}
	defer func() {
		if releaseErr := c.leaseFcd.ReleaseSnapshot(ctx); releaseErr != nil {
			log.Printf("[snapshot] release lease: %v", releaseErr)
		}
	}()

	// ── 2. Fencing: reject a stale token before doing any work ───────────────
	// ValidateFence returns ErrStaleFence when fence < lastSeenFence,
	// meaning a newer compactor already ran and our token is obsolete.
	if err := lease.ValidateFence(fence, c.lastSeenFence); err != nil {
		return fmt.Errorf("snapshot: %w", err)
	}

	// ── 3. Capture merged DB state ───────────────────────────────────────────
	dbData, version, err := c.snapshot(ctx)
	if err != nil {
		return fmt.Errorf("snapshot: capture db: %w", err)
	}
	if len(dbData) == 0 {
		log.Printf("[snapshot] snapshot function returned empty data — nothing to compact")
		return nil
	}

	// ── 4. Check existing latest.json — only write if our version is newer ───
	if existingVersion, skipErr := c.existingSnapshotVersion(ctx); skipErr == nil {
		if version <= existingVersion {
			log.Printf("[snapshot] current version %d <= existing snapshot %d — skipping", version, existingVersion)
			return nil
		}
	}
	// If latest.json doesn't exist yet, existingVersion == 0 < any real version,
	// so we proceed normally.

	// ── Second fencing check after capture ───────────────────────────────────
	// A long-running DBSnapshot may have allowed the lease to lapse and be
	// re-acquired by another node.  Re-validate before writing.
	if err := lease.ValidateFence(fence, c.lastSeenFence); err != nil {
		return fmt.Errorf("snapshot: post-capture fence check: %w", err)
	}

	// ── 5. Write the encrypted snapshot blob ─────────────────────────────────
	blobKey := snapshotBlobKey(version)
	if err := c.s3.PutEncrypted(ctx, blobKey, dbData); err != nil {
		return fmt.Errorf("snapshot: write blob %s: %w", blobKey, err)
	}
	log.Printf("[snapshot] wrote snapshot blob %s (%d bytes)", blobKey, len(dbData))

	// ── 6. Write latest.json ─────────────────────────────────────────────────
	latest := LatestDoc{
		Version:   version,
		Key:       blobKey,
		CreatedAt: time.Now().UTC(),
	}
	latestJSON, err := json.Marshal(latest)
	if err != nil {
		return fmt.Errorf("snapshot: marshal latest.json: %w", err)
	}
	if err := c.s3.PutEncrypted(ctx, latestKey, latestJSON); err != nil {
		return fmt.Errorf("snapshot: write latest.json: %w", err)
	}
	log.Printf("[snapshot] updated latest.json → version=%d key=%s", version, blobKey)

	// ── Advance the in-process fence cursor ──────────────────────────────────
	c.lastSeenFence = fence

	// ── 7. Prune per-node changesets below the covered version ───────────────
	if pruneErr := c.pruneChangesets(ctx, version); pruneErr != nil {
		// Pruning is best-effort: log but don't fail the compaction cycle.
		log.Printf("[snapshot] prune changesets: %v", pruneErr)
	}

	log.Printf("[snapshot] compaction complete: version=%d fence=%d", version, fence)
	return nil
}

// existingSnapshotVersion reads latest.json and returns the version field.
// Returns (0, err) when the object is absent or unreadable.
func (c *Compactor) existingSnapshotVersion(ctx context.Context) (int64, error) {
	data, err := c.s3.GetEncrypted(ctx, latestKey)
	if err != nil {
		return 0, err
	}
	var doc LatestDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		return 0, fmt.Errorf("snapshot: parse latest.json: %w", err)
	}
	return doc.Version, nil
}

// pruneChangesets deletes per-node changeset objects whose version is strictly
// less than coveredVersion.  Key format: nodes/{id}/changes/{ver}.bin
func (c *Compactor) pruneChangesets(ctx context.Context, coveredVersion int64) error {
	keys, err := c.s3.ListPrefix(ctx, changesNodePrefix)
	if err != nil {
		return fmt.Errorf("list changesets: %w", err)
	}

	pruned := 0
	for _, key := range keys {
		if !strings.HasSuffix(key, ".bin") {
			continue
		}
		// Expected: nodes/{node_id}/changes/{version}.bin
		parts := strings.SplitN(key, "/", 4)
		if len(parts) != 4 || parts[0] != "nodes" || parts[2] != "changes" {
			continue
		}
		versionStr := strings.TrimSuffix(parts[3], ".bin")
		ver, err := strconv.ParseInt(versionStr, 10, 64)
		if err != nil {
			continue
		}
		if ver < coveredVersion {
			if delErr := c.s3.DeleteObject(ctx, key); delErr != nil {
				log.Printf("[snapshot] prune: delete %s: %v", key, delErr)
				continue
			}
			pruned++
		}
	}

	if pruned > 0 {
		log.Printf("[snapshot] pruned %d changeset(s) below version %d", pruned, coveredVersion)
	}
	return nil
}
