// Package sync — SYNC-03: new-instance bootstrap from snapshot + short changeset tail.
//
// Bootstrap fetches the latest compacted snapshot (written by SYNC-02's
// Compactor) and installs it as the local DB base, then replays only the
// per-node changesets whose db_version is strictly above the snapshot's covered
// version.  If no snapshot exists (latest.json absent) the caller falls back to
// replaying all per-node changesets — the same full-replay behaviour that existed
// before SYNC-02.
//
// Security invariant (matches SYNC-02 / CLUSTER-03):
//
//	The passphrase and the SSE-C key derived from it are NEVER written to disk
//	or sent to the cloud control plane.  The SnapshotS3 interface is the
//	post-derivation layer; the caller constructs the client with the derived key
//	and passes it here — Bootstrap never sees the raw passphrase.
package sync

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
)

// ── BootstrapS3 — narrow read-only S3 interface for Bootstrap ────────────────

// BootstrapS3 is the subset of S3 operations required by Bootstrap.
// It covers only read + list — Bootstrap never writes or deletes objects.
//
// *cluster.Client satisfies this interface via GetEncrypted / ListPrefix.
// *mockSnapshotS3 (snapshot_test.go) also satisfies it.
type BootstrapS3 interface {
	// GetEncrypted downloads and decrypts an SSE-C-encrypted object.
	GetEncrypted(ctx context.Context, key string) ([]byte, error)
	// ListPrefix returns object keys that start with prefix.
	ListPrefix(ctx context.Context, prefix string) ([]string, error)
}

// ── DBInstaller — caller-supplied function to install a snapshot DB ───────────

// DBInstaller is the caller-supplied function that installs a raw snapshot blob
// (previously produced by DBSnapshot and stored by Compactor) as the local
// database base state.
//
// The blob is exactly what DBSnapshot returned — callers must treat it as
// opaque bytes (e.g. a sqlite3_serialize buffer or a serialised changeset
// bundle).  After Install returns without error, the local DB must reflect the
// state encoded in data up to snapshotVersion.
type DBInstaller func(ctx context.Context, data []byte, snapshotVersion int64) error

// ── ChangesetApplier — caller-supplied function to apply changeset tail ───────

// ChangesetApplier is the caller-supplied function that downloads and applies a
// single per-node changeset object (key == nodes/{id}/changes/{ver}.bin) from
// S3, decrypting it via the same SSE-C client used for the snapshot.
//
// It is called for each changeset in nodes/{id}/changes/ whose version is
// strictly greater than the snapshot's covered version (or 0 for full replay).
// The s3 parameter is the same BootstrapS3 instance passed to Bootstrap so the
// applier can fetch objects without needing a second encrypted client.
//
// Returning a non-nil error halts the replay and Bootstrap propagates it.
type ChangesetApplier func(ctx context.Context, s3 BootstrapS3, key string) error

// ── BootstrapConfig ───────────────────────────────────────────────────────────

// BootstrapConfig holds the parameters for a single Bootstrap call.
type BootstrapConfig struct {
	// NodeID is used only for logging; it identifies the bootstrapping instance.
	NodeID string
	// Passphrase is the cluster passphrase used to derive the HMAC key that
	// authenticates cluster/snapshot/latest.json before its Version/Key are
	// trusted to install a snapshot — see snapshot.go's package doc comment
	// (deriveLatestMACKey) for the anti-rollback-authenticity threat this
	// closes.
	//
	// REQUIRED. Bootstrap returns ErrLatestAuthenticityUnconfigured when it is
	// empty: installing a snapshot blob chosen by an unauthenticated pointer is
	// the most damaging form of the rollback this check exists to stop (it
	// becomes the new node's entire DB base), so a blank field must refuse, not
	// proceed unprotected. It is the same passphrase the SSE-C client already
	// needs to read the bucket at all.
	Passphrase string
}

// ── Bootstrap ─────────────────────────────────────────────────────────────────

// Bootstrap performs the SYNC-03 new-instance bootstrap sequence:
//
//  0. Refuse outright (ErrLatestAuthenticityUnconfigured) when cfg.Passphrase
//     is empty — latest.json's Version/Key could not be authenticated, and an
//     unverified snapshot must never become a node's DB base.
//  1. Read cluster/snapshot/latest.json from S3.
//     – If absent: log "no snapshot found" and fall back to full changeset replay
//     (snapshotVersion=0, all changesets replayed via applier).
//  2. If present: download + SSE-C-decrypt the snapshot blob and install it via
//     install(ctx, data, version).
//  3. List nodes/*/changes/*.bin and replay every changeset whose version is
//     strictly greater than the snapshot's covered version (0 for no-snapshot
//     fallback → replay everything).
//
// Passphrase / SSE-C: s3 is the post-derivation SnapshotS3 interface — the
// caller has already derived the key and built the client.  Bootstrap never
// receives, stores, or forwards the raw passphrase.
//
// Parameters:
//
//	ctx     – cancellation / timeout.
//	cfg     – logging configuration.
//	s3      – encrypted S3 client (BootstrapS3 interface; *cluster.Client satisfies this).
//	install – function to install the snapshot DB; called iff a snapshot exists.
//	applier – function to download + apply one changeset object from S3; called
//	          for each tail changeset (or all changesets in fallback mode).
func Bootstrap(
	ctx context.Context,
	cfg BootstrapConfig,
	s3 BootstrapS3,
	install DBInstaller,
	applier ChangesetApplier,
) error {
	nodeID := cfg.NodeID
	if nodeID == "" {
		nodeID = "unknown"
	}
	// ── 0. Fail-closed configuration gate ────────────────────────────────────
	// No passphrase → no MAC key → latest.json could not be authenticated.
	// Refuse rather than install an unverified snapshot as this node's DB base.
	if cfg.Passphrase == "" {
		return fmt.Errorf("bootstrap: refusing to bootstrap node %q: %w", nodeID, ErrLatestAuthenticityUnconfigured)
	}
	macKey := deriveLatestMACKey(cfg.Passphrase)

	// ── 1. Read latest.json ───────────────────────────────────────────────────
	snapshotVersion, err := bootstrapReadSnapshot(ctx, s3, nodeID, macKey, install)
	if err != nil {
		return err
	}

	// ── 2. Replay the changeset tail above snapshotVersion ───────────────────
	if err := bootstrapReplayTail(ctx, s3, nodeID, snapshotVersion, applier); err != nil {
		return err
	}

	return nil
}

// bootstrapReadSnapshot attempts to read latest.json and, if present, downloads
// and installs the snapshot.  Returns snapshotVersion (0 on no-snapshot fallback).
func bootstrapReadSnapshot(
	ctx context.Context,
	s3 BootstrapS3,
	nodeID string,
	macKey []byte,
	install DBInstaller,
) (snapshotVersion int64, err error) {
	// Try to read latest.json.
	latestData, err := s3.GetEncrypted(ctx, latestKey)
	if err != nil {
		// latest.json absent → no snapshot yet. Fall back to full replay.
		log.Printf("[bootstrap/%s] no snapshot found (latest.json absent) — full changeset replay", nodeID)
		return 0, nil
	}

	var doc LatestDoc
	if err := json.Unmarshal(latestData, &doc); err != nil {
		return 0, fmt.Errorf("bootstrap: parse latest.json: %w", err)
	}
	if doc.Version <= 0 || doc.Key == "" {
		// Malformed latest.json — treat as no snapshot and fall back.
		log.Printf("[bootstrap/%s] latest.json malformed (version=%d key=%q) — full changeset replay",
			nodeID, doc.Version, doc.Key)
		return 0, nil
	}
	if !verifyLatestDoc(macKey, doc) {
		// doc.Version/Key cannot be trusted enough to install as the new
		// node's DB base — treat exactly like the malformed case above and
		// fall back to a full changeset replay (always a safe, if slower,
		// path). See snapshot.go's package doc comment for the threat model.
		log.Printf("[bootstrap/%s] latest.json failed authenticity check (bad or missing MAC) — full changeset replay", nodeID)
		return 0, nil
	}

	log.Printf("[bootstrap/%s] found snapshot version=%d key=%s — downloading", nodeID, doc.Version, doc.Key)

	// Download the snapshot blob (SSE-C decrypted by the s3 client).
	blobData, err := s3.GetEncrypted(ctx, doc.Key)
	if err != nil {
		return 0, fmt.Errorf("bootstrap: download snapshot blob %s: %w", doc.Key, err)
	}

	// Install the snapshot as the local DB base.
	if err := install(ctx, blobData, doc.Version); err != nil {
		return 0, fmt.Errorf("bootstrap: install snapshot (version=%d): %w", doc.Version, err)
	}

	log.Printf("[bootstrap/%s] installed snapshot version=%d (%d bytes)", nodeID, doc.Version, len(blobData))
	return doc.Version, nil
}

// bootstrapReplayTail lists all per-node changesets in S3 and replays those
// with db_version strictly greater than snapshotVersion.  When snapshotVersion
// is 0 (no snapshot), all changesets are replayed (full-replay fallback).
func bootstrapReplayTail(
	ctx context.Context,
	s3 BootstrapS3,
	nodeID string,
	snapshotVersion int64,
	applier ChangesetApplier,
) error {
	// List all objects under nodes/ to find changeset blobs.
	keys, err := s3.ListPrefix(ctx, changesNodePrefix)
	if err != nil {
		return fmt.Errorf("bootstrap: list changesets: %w", err)
	}

	// Parse and collect changeset entries above snapshotVersion.
	type changeEntry struct {
		key     string
		version int64
		peerID  string
	}

	var tail []changeEntry
	for _, key := range keys {
		if !strings.HasSuffix(key, ".bin") {
			continue
		}
		// Expected: nodes/{peer_id}/changes/{version}.bin
		parts := strings.SplitN(key, "/", 4)
		if len(parts) != 4 || parts[0] != "nodes" || parts[2] != "changes" {
			continue
		}
		peerID := parts[1]
		versionStr := strings.TrimSuffix(parts[3], ".bin")
		ver, err := strconv.ParseInt(versionStr, 10, 64)
		if err != nil {
			continue
		}
		if ver <= snapshotVersion {
			// Already covered by the snapshot (or equal → also covered).
			continue
		}
		tail = append(tail, changeEntry{key: key, version: ver, peerID: peerID})
	}

	if len(tail) == 0 {
		if snapshotVersion > 0 {
			log.Printf("[bootstrap/%s] no changeset tail above snapshot version=%d", nodeID, snapshotVersion)
		} else {
			log.Printf("[bootstrap/%s] no changesets to replay (bucket empty)", nodeID)
		}
		return nil
	}

	// Sort by (peerID, version) for deterministic application order.
	sort.Slice(tail, func(i, j int) bool {
		if tail[i].peerID != tail[j].peerID {
			return tail[i].peerID < tail[j].peerID
		}
		return tail[i].version < tail[j].version
	})

	if snapshotVersion > 0 {
		log.Printf("[bootstrap/%s] replaying %d changeset(s) above snapshot version=%d",
			nodeID, len(tail), snapshotVersion)
	} else {
		log.Printf("[bootstrap/%s] full replay: %d changeset(s) (no snapshot)", nodeID, len(tail))
	}

	applied := 0
	for _, entry := range tail {
		if err := applier(ctx, s3, entry.key); err != nil {
			return fmt.Errorf("bootstrap: apply changeset %s: %w", entry.key, err)
		}
		applied++
	}

	log.Printf("[bootstrap/%s] bootstrap complete: applied %d changeset(s)", nodeID, applied)
	return nil
}
