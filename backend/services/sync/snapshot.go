// Package sync — SYNC-02: bucket snapshot/compaction of merged DB state.
//
// Compactor periodically writes a compacted, SSE-C-encrypted snapshot of the
// merged cr-sqlite DB state to:
//
//	cluster/snapshot/<version>.db.enc   — encrypted snapshot blob
//	cluster/snapshot/latest.json        — {version, key, created_at, mac}
//
// Exactly one instance compacts at a time, guarded by the snapshot ownership
// lease (leases/snapshot.json, LEASE-01) with fencing so a stalled compactor
// cannot clobber a newer snapshot.  Per-node changesets below the snapshot's
// covered version are pruned after a successful compaction.
//
// The SSE-C key is derived in-process from a caller-supplied passphrase using
// Argon2id (matching the cluster/s3.go parameters).  The passphrase and derived
// key are NEVER persisted to disk or sent to the cloud control plane.
//
// latest.json authenticity: the anti-rollback check (`version <= existing`)
// below is only as sound as the authenticity of the `existing` value it reads.
// cluster/snapshot/latest.json lives in the same bucket every node's shared S3
// credentials can write — there is no per-writer bucket ACL distinguishing
// "the current lease holder" from "any node with cluster credentials" (see
// cluster/s3.go, lease/lease.go: same credential pair drives all of it).
// Without authenticity, anyone who can reach the bucket could set Version
// arbitrarily — either freezing the cluster (an absurdly high Version makes
// every real snapshot look stale forever) or pointing Key at a stale/malicious
// blob to force a rollback on restore. See deriveLatestMACKey / verifyLatestDoc.
//
// Authenticity is MANDATORY, not opt-in. Every consumer of latest.json
// (Compactor.Run, Restorer.LatestSnapshot, Bootstrap) refuses to operate at all
// when no MAC key is configured, returning ErrLatestAuthenticityUnconfigured.
// There is deliberately NO "authenticity off" mode: the very same cluster
// passphrase is already structurally required to read or write any object in
// this bucket (cluster.NewClient derives the SSE-C key from it), so
// "no passphrase available" is not a real operating mode — it was only ever
// reachable by leaving a config field blank, which is exactly the fail-open
// hole this closes. An anti-rollback check that silently stops checking when a
// field is empty is worse than none, because the surrounding code reads as
// though the protection is present.
package sync

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
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

// ── latest.json authenticity (anti-rollback authenticity, see package doc) ──

// latestMACDomain domain-separates the latest.json MAC key from the SSE-C
// encryption key derived from the same passphrase. It is a fixed, public
// constant — not a secret — so computing the MAC key never needs a bucket
// round-trip for a salt, unlike the SSE-C key (whose salt lives in the bucket
// at cluster/encryption-salt). Verification must work even when the object
// under suspicion is the one you'd otherwise have to trust for that salt.
const latestMACDomain = "vulos-sync-latest-json-mac-v1"

// deriveLatestMACKey derives the key that authenticates cluster/snapshot/
// latest.json from the cluster passphrase (the same passphrase already
// required to reach the bucket's SSE-C content — see cmd_backup.go/main.go's
// VULOS_CLUSTER_PASSPHRASE). It uses Argon2id with the same work parameters as
// deriveSnapshotKey but a distinct, fixed domain salt: reusing an encryption
// key as a MAC key is a classic key-separation mistake, so this is a different
// key even though both trace back to one shared passphrase.
func deriveLatestMACKey(passphrase string) []byte {
	return argon2.IDKey([]byte(passphrase), []byte(latestMACDomain), snapshotArgonTime, snapshotArgonMem, snapshotArgonThreads, snapshotArgonKeyLen)
}

// LatestMACKeyFromPassphrase derives the latest.json authenticity MAC key from
// the cluster passphrase, for callers outside this package that construct a
// Restorer directly: WithLatestMACKey deliberately takes derived material
// rather than a passphrase, so without this the documented way to satisfy it
// would be unreachable. Production callers should prefer BuildRestorer /
// BuildCompactor, which derive it internally and never expose the key.
func LatestMACKeyFromPassphrase(passphrase string) []byte {
	return deriveLatestMACKey(passphrase)
}

// latestDocMAC computes the hex-encoded HMAC-SHA256 authenticating a
// LatestDoc's Version/Key/CreatedAt under macKey. It is computed over an
// explicit canonical string — not raw marshaled JSON — so verification never
// depends on encoding/json's field ordering or time-formatting matching
// byte-for-byte between the writer and a later reader.
func latestDocMAC(macKey []byte, version int64, key string, createdAt time.Time) string {
	mac := hmac.New(sha256.New, macKey)
	fmt.Fprintf(mac, "v1|%d|%s|%s", version, key, createdAt.UTC().Format(time.RFC3339Nano))
	return hex.EncodeToString(mac.Sum(nil))
}

// verifyLatestDoc reports whether doc.MAC authenticates doc's Version/Key/
// CreatedAt under macKey. A nil/empty macKey can never authenticate anything:
// it returns false rather than "no opinion", so a caller that forgets the
// configuration gate below still gets a refusal and not a free pass.
func verifyLatestDoc(macKey []byte, doc LatestDoc) bool {
	if len(macKey) == 0 {
		return false
	}
	want := latestDocMAC(macKey, doc.Version, doc.Key, doc.CreatedAt)
	return hmac.Equal([]byte(want), []byte(doc.MAC))
}

// ErrLatestAuthenticityUnconfigured is the fail-closed refusal returned when a
// latest.json consumer has no MAC key at all — i.e. its cluster passphrase was
// left blank. It is NOT an attack signal; it is a misconfiguration that would
// silently disable the anti-rollback protection described in the package doc
// comment, so every consumer refuses instead of proceeding unprotected.
var ErrLatestAuthenticityUnconfigured = errors.New(
	"sync: latest.json anti-rollback authenticity cannot be verified: no cluster passphrase configured " +
		"(set VULOS_CLUSTER_PASSPHRASE / CompactorConfig.Passphrase / BootstrapConfig.Passphrase, or build the " +
		"Restorer with WithLatestMACKey(LatestMACKeyFromPassphrase(passphrase))) — refusing to trust latest.json unverified")

// requireLatestMACKey is the single fail-closed configuration gate shared by
// every latest.json consumer. It returns ErrLatestAuthenticityUnconfigured when
// no MAC key is available, so an absent passphrase REFUSES TO VERIFY rather
// than skipping verification. It deliberately errors rather than logging a
// "skipping authenticity check" warning: skip output is routinely swallowed by
// service supervisors and CI runners, so the one run where it matters is
// exactly the run where nobody sees it.
func requireLatestMACKey(macKey []byte) error {
	if len(macKey) == 0 {
		return ErrLatestAuthenticityUnconfigured
	}
	return nil
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
	// MAC authenticates Version/Key/CreatedAt (see deriveLatestMACKey /
	// latestDocMAC) so an anti-rollback decision never trusts Version until
	// its authenticity is established. Consumers MUST call verifyLatestDoc
	// before trusting Version/Key — see existingSnapshotVersion and
	// LatestSnapshot. A doc with an empty MAC is unauthenticated and is never
	// trusted: writers always populate it (Compactor.Run refuses to run without
	// a MAC key), so an empty MAC on the wire means the doc was written by
	// something other than a passphrase-holding compactor.
	MAC string `json:"mac,omitempty"`
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

	// macKey authenticates cluster/snapshot/latest.json (see the package doc
	// and deriveLatestMACKey). nil means no Passphrase was configured in
	// CompactorConfig; Run then refuses to compact at all
	// (ErrLatestAuthenticityUnconfigured) rather than performing an
	// anti-rollback comparison against a Version it cannot authenticate.
	macKey []byte

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
	// Passphrase is the cluster passphrase (VULOS_CLUSTER_PASSPHRASE) used to
	// derive the HMAC key that authenticates cluster/snapshot/latest.json
	// before its Version is trusted for the anti-rollback check — see the
	// package doc comment above and deriveLatestMACKey.
	//
	// REQUIRED. It is not an optional hardening knob: Run returns
	// ErrLatestAuthenticityUnconfigured when it is empty, because a compactor
	// that cannot authenticate latest.json cannot make a sound anti-rollback
	// decision and would also publish an unauthenticated latest.json that no
	// Restorer/Bootstrap would accept. It is the same passphrase the SSE-C
	// bucket client already needs, so there is no configuration in which it is
	// legitimately unavailable.
	Passphrase string
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
	var macKey []byte
	if cfg.Passphrase != "" {
		macKey = deriveLatestMACKey(cfg.Passphrase)
	}
	return &Compactor{
		nodeID:   nodeID,
		leaseFcd: leaseFcd,
		s3:       s3,
		snapshot: snapshot,
		leaseTTL: ttl,
		macKey:   macKey,
	}
}

// Run performs one compaction cycle:
//
//  1. Acquire the snapshot lease (returns immediately if another node holds it).
//  2. Validate the fence token — abort if stale.
//  3. Capture the merged DB state via the DBSnapshot function.
//  4. Read latest.json; abort if the snapshot's version is not newer. An
//     existing doc whose MAC fails authenticity (see the package doc comment)
//     is treated the same as an absent/malformed one — it does not block the
//     write, which defeats a forged-high-Version freeze attempt.
//  5. Write the encrypted snapshot blob (cluster/snapshot/<version>.db.enc).
//  6. Write latest.json, always MAC'd (CompactorConfig.Passphrase is required;
//     Run refuses with ErrLatestAuthenticityUnconfigured when it is absent).
//  7. Prune per-node changesets below the covered version.
//  8. Release the lease.
//
// Passphrase / SSE-C key: the s3 client supplied to NewCompactor must already
// have an in-process SSE-C key (via cluster.NewClient).  The Compactor never
// sees nor persists the passphrase itself — that responsibility belongs to the
// caller who constructs the cluster.Client.
func (c *Compactor) Run(ctx context.Context) error {
	// ── 0. Fail-closed configuration gate ────────────────────────────────────
	// Without a MAC key the step-4 anti-rollback check would compare our
	// version against a Version nobody can authenticate, and step 6 would
	// publish a latest.json with no MAC. Refuse here — before the lease is even
	// acquired — instead of proceeding with the protection silently absent.
	// Checked in Run rather than inside existingSnapshotVersion because Run
	// deliberately treats an *unreadable/unauthentic* doc as "proceed with the
	// write" (that is what defeats a forged-high-Version freeze), so an error
	// returned from there could never act as a guard.
	if err := requireLatestMACKey(c.macKey); err != nil {
		return fmt.Errorf("snapshot: refusing to compact: %w", err)
	}

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
	// Always MAC'd: step 0 guarantees c.macKey is present.
	latest.MAC = latestDocMAC(c.macKey, latest.Version, latest.Key, latest.CreatedAt)
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

// ── Restore (SYNC-03: inverse of compaction) ──────────────────────────────────

// DBRehydrate is the caller-supplied inverse of DBSnapshot: it takes the raw
// decrypted snapshot blob (whatever DBSnapshot serialised — a sqlite3_serialize
// image or a changeset bundle) and the version it covers, and applies it to the
// local merged DB. It is the only state-mutating step of a restore; the
// Restorer treats the blob as opaque bytes.
type DBRehydrate func(ctx context.Context, data []byte, version int64) error

// RestoreResult reports what a restore did.
type RestoreResult struct {
	// Version is the snapshot version that was restored.
	Version int64
	// Key is the S3 key of the snapshot blob that was restored.
	Key string
	// Bytes is the size of the decrypted snapshot blob applied.
	Bytes int
	// CreatedAt is the snapshot's recorded creation time.
	CreatedAt time.Time
}

// ErrNoSnapshot is returned by Restore/LatestSnapshot when the bucket holds no
// latest.json (nothing has ever been backed up).
var ErrNoSnapshot = errors.New("sync: no snapshot present (latest.json absent)")

// ErrSnapshotTampered is returned by LatestSnapshot/Restore when latest.json's
// MAC does not authenticate under the configured macKey (see WithLatestMACKey
// and the package doc comment) — i.e. the document was not produced by
// someone holding the cluster passphrase, so its Version/Key cannot be
// trusted. This is a fail-closed rejection: Restore does NOT fall back to
// applying it anyway, unlike Compactor's own anti-rollback check (which fails
// over toward "proceed with the write", since a stuck compactor is the worse
// outcome there — a stuck Restore is the safe default here, since Restore is
// destructive).
//
// A Restorer with no MAC key at all does not reach this error: it fails earlier
// with ErrLatestAuthenticityUnconfigured (see requireLatestMACKey), so
// "forgot the passphrase" can never be mistaken for "MAC verified fine".
var ErrSnapshotTampered = errors.New("sync: latest.json failed authenticity check (bad or missing MAC) — refusing to restore")

// Restorer pulls the latest snapshot from the bucket and rehydrates local state.
// It is the inverse of Compactor and shares the same SnapshotS3 abstraction, so
// it works against the real SSE-C cluster client in production and the in-memory
// mock in tests.
//
// Restore is intentionally LEASE-FREE: it is a read of an immutable, versioned
// snapshot blob plus a local apply. Multiple nodes restoring concurrently is
// harmless (they converge on the same snapshot), and a node recovering from a
// blank disk must not need to win a write lease just to read its own backup.
type Restorer struct {
	s3        SnapshotS3
	rehydrate DBRehydrate

	// macKey authenticates cluster/snapshot/latest.json (see the package doc
	// and deriveLatestMACKey). nil means no WithLatestMACKey option was
	// supplied; LatestSnapshot/Restore then refuse outright with
	// ErrLatestAuthenticityUnconfigured rather than trusting Version/Key from a
	// document they cannot authenticate.
	macKey []byte
}

// RestorerOption configures optional Restorer behavior. See WithLatestMACKey.
type RestorerOption func(*Restorer)

// WithLatestMACKey supplies the key for the authenticity verification of
// latest.json: Restorer rejects (ErrSnapshotTampered, fail-closed) any
// latest.json whose MAC does not authenticate under macKey, rather than
// trusting Version/Key from an object anyone with bucket write access could
// have forged — see the package doc comment for the threat this closes.
//
// REQUIRED despite being expressed as an option: a Restorer built without it
// (or with an empty macKey) refuses to read latest.json at all
// (ErrLatestAuthenticityUnconfigured). It stays an option only because the key
// is derived material rather than a passphrase — the Restorer must never see
// the passphrase itself. Production callers should prefer BuildRestorer, which
// derives it for them.
//
// macKey should come from LatestMACKeyFromPassphrase(passphrase) — the SAME
// cluster passphrase already required to reach the bucket's SSE-C content
// (deriveLatestMACKey in-package). Never pass
// the SSE-C encryption key itself; it is a different, domain-separated key.
func WithLatestMACKey(macKey []byte) RestorerOption {
	return func(r *Restorer) { r.macKey = macKey }
}

// NewRestorer creates a Restorer.
//
//   - s3 must be the SAME encrypted cluster client used by the Compactor (the
//     SSE-C key is managed by the cluster.Client layer; the Restorer never sees
//     a passphrase).
//   - rehydrate applies the decrypted blob to local state.
//   - opts MUST include WithLatestMACKey; without it the returned Restorer
//     refuses every read with ErrLatestAuthenticityUnconfigured rather than
//     silently skipping latest.json authenticity verification. BuildRestorer
//     wires it from the cluster passphrase in production.
func NewRestorer(s3 SnapshotS3, rehydrate DBRehydrate, opts ...RestorerOption) *Restorer {
	r := &Restorer{s3: s3, rehydrate: rehydrate}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// LatestSnapshot reads and parses cluster/snapshot/latest.json. It returns
// ErrNoSnapshot when latest.json is absent so callers can distinguish "nothing
// backed up yet" from a transport error, and
// ErrLatestAuthenticityUnconfigured when no MAC key was supplied — a Restorer
// that cannot authenticate the document must not report or act on its
// Version/Key at all.
func (r *Restorer) LatestSnapshot(ctx context.Context) (LatestDoc, error) {
	// Fail-closed configuration gate — see requireLatestMACKey. Deliberately
	// before the fetch: an unverifiable pointer must not be reported as status
	// either, since Version/Key are exactly what an attacker controls.
	if err := requireLatestMACKey(r.macKey); err != nil {
		return LatestDoc{}, err
	}
	data, err := r.s3.GetEncrypted(ctx, latestKey)
	if err != nil {
		// Treat any miss as "no snapshot" — the mock and the cluster client both
		// surface a not-found as a plain error, and a restore on a fresh bucket
		// should report ErrNoSnapshot rather than a raw transport error.
		return LatestDoc{}, ErrNoSnapshot
	}
	var doc LatestDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		return LatestDoc{}, fmt.Errorf("sync: parse latest.json: %w", err)
	}
	if doc.Key == "" {
		return LatestDoc{}, fmt.Errorf("sync: latest.json has empty snapshot key")
	}
	if !verifyLatestDoc(r.macKey, doc) {
		return LatestDoc{}, ErrSnapshotTampered
	}
	return doc, nil
}

// Restore downloads the latest snapshot blob referenced by latest.json and
// applies it via the rehydrate function. It returns ErrNoSnapshot when there is
// nothing to restore.
//
// Steps:
//  1. Read latest.json → {version, key, created_at}.
//  2. Download + decrypt the snapshot blob at key.
//  3. Apply it locally via rehydrate(data, version).
func (r *Restorer) Restore(ctx context.Context) (RestoreResult, error) {
	if r.rehydrate == nil {
		return RestoreResult{}, fmt.Errorf("sync: Restore: nil rehydrate function")
	}
	doc, err := r.LatestSnapshot(ctx)
	if err != nil {
		return RestoreResult{}, err
	}

	blob, err := r.s3.GetEncrypted(ctx, doc.Key)
	if err != nil {
		return RestoreResult{}, fmt.Errorf("sync: Restore: download snapshot blob %s: %w", doc.Key, err)
	}
	if len(blob) == 0 {
		return RestoreResult{}, fmt.Errorf("sync: Restore: snapshot blob %s is empty", doc.Key)
	}

	if err := r.rehydrate(ctx, blob, doc.Version); err != nil {
		return RestoreResult{}, fmt.Errorf("sync: Restore: rehydrate version %d: %w", doc.Version, err)
	}

	log.Printf("[snapshot] restored snapshot version=%d key=%s (%d bytes)", doc.Version, doc.Key, len(blob))
	return RestoreResult{
		Version:   doc.Version,
		Key:       doc.Key,
		Bytes:     len(blob),
		CreatedAt: doc.CreatedAt,
	}, nil
}

// existingSnapshotVersion reads latest.json and returns the version field.
// Returns (0, err) when the object is absent, unreadable, or unauthentic.
//
// Its caller (Run step 4) treats ANY error as "no known existing version" and
// proceeds with the write, so this function must never be the place where a
// missing-configuration guard lives — Run step 0 owns that gate and refuses
// before we get here. See the comment there.
func (c *Compactor) existingSnapshotVersion(ctx context.Context) (int64, error) {
	if err := requireLatestMACKey(c.macKey); err != nil {
		// Unreachable via Run (step 0 already refused); belt-and-braces for any
		// future caller. NOT a substitute for that gate — see above.
		return 0, err
	}
	data, err := c.s3.GetEncrypted(ctx, latestKey)
	if err != nil {
		return 0, err
	}
	var doc LatestDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		return 0, fmt.Errorf("snapshot: parse latest.json: %w", err)
	}
	if !verifyLatestDoc(c.macKey, doc) {
		// doc.Version cannot be trusted as an anti-rollback counter until its
		// authenticity is established (see the package doc comment). Treat
		// exactly like the parse-failure case above: Run's caller falls back
		// to "no known existing version" and proceeds with the write. This is
		// what defeats a forged-high-Version freeze attack — an attacker who
		// can write the bucket but not produce a valid MAC cannot permanently
		// block legitimate compaction by claiming an absurd Version.
		return 0, fmt.Errorf("snapshot: latest.json failed authenticity check (bad or missing MAC)")
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
