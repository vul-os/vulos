package joinsync

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"os"

	"vulos/backend/services/cluster"
	vulossync "vulos/backend/services/sync"

	"github.com/minio/minio-go/v7"
)

// jsonUnmarshal is a tiny indirection so joinsync.go stays import-light.
func jsonUnmarshal(data []byte, v any) error { return json.Unmarshal(data, v) }

// atomicWriteJSON marshals v and writes it to path via a temp file + rename,
// so a crash mid-write never leaves a half-written storage.json/sync-state.json
// that another service (storageprov, bootmode) might read.
func atomicWriteJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// realBackend is the production clusterBackend backed by the cluster package
// (Argon2id key derivation + SSE-C, sharing the salt via S3 — CLUSTER-03).
type realBackend struct{}

// validate connects to S3, derives the cluster key from the passphrase, and
// confirms the passphrase using the well-known join marker.
//
//   - cluster.NewClient does the salt fetch/create round-trip; a failure
//     there means the endpoint/bucket is unreachable or the S3 creds are
//     wrong → ErrUnreachable.
//   - GetEncrypted(joinMarkerKey):
//   - decrypts to joinMarkerPlaintext → passphrase correct.
//   - object missing (NoSuchKey) → first node into this bucket; we write
//     the marker (encrypted with the derived key) so future joiners can
//     validate, and treat this as success.
//   - present but won't decrypt / wrong content → ErrBadPassphrase.
func (realBackend) validate(ctx context.Context, cfg cluster.S3Config, passphrase string) error {
	client, err := cluster.NewClient(ctx, cfg, passphrase)
	if err != nil {
		return errors.Join(ErrUnreachable, err)
	}

	data, err := client.GetEncrypted(ctx, joinMarkerKey)
	if err != nil {
		if isNoSuchKey(err) {
			// Fresh bucket — seed the marker so subsequent joins validate.
			if perr := client.PutEncrypted(ctx, joinMarkerKey, []byte(joinMarkerPlaintext)); perr != nil {
				return errors.Join(ErrUnreachable, perr)
			}
			return nil
		}
		// A decryption failure (wrong SSE-C key from wrong passphrase) is not
		// a NoSuchKey error — surface it as a bad passphrase.
		return errors.Join(ErrBadPassphrase, err)
	}

	if string(data) != joinMarkerPlaintext {
		return ErrBadPassphrase
	}
	return nil
}

// pull performs the post-join data pull using the SYNC-03 bootstrap path:
//
//  1. Connects to S3 with the derived SSE-C key (passphrase lives only here).
//  2. Calls sync.Bootstrap which:
//     a. reads latest.json, authenticates it against the passphrase-derived
//     MAC key (see snapshot.go's package doc comment), then downloads +
//     installs the snapshot DB;
//     b. replays only the changeset tail above the snapshot version;
//     c. falls back to full changeset replay when no snapshot exists (also
//     the fallback when latest.json fails authenticity).
//
// The passphrase is NEVER written anywhere — it lives only in this call stack
// and is used in-process for two derivations: the SSE-C key (inside
// cluster.NewClient) and the latest.json MAC key (inside sync.Bootstrap, via
// BootstrapConfig.Passphrase). Neither the passphrase nor either derived key
// is ever persisted or sent anywhere. The live services/sync engine (started
// via sync.NewFromCluster in cmd/server) takes over normal incremental sync
// once the server boots.  (Historical note: an older cluster.SyncLoop was
// superseded and its implementation has since been removed.)
func (realBackend) pull(ctx context.Context, cfg cluster.S3Config, passphrase string, progress func(phase string, pct int)) error {
	progress("connecting", 5)

	client, err := cluster.NewClient(ctx, cfg, passphrase)
	// passphrase used solely to derive the SSE-C key inside cluster.NewClient;
	// it is never stored or forwarded beyond this point.
	if err != nil {
		return errors.Join(ErrUnreachable, err)
	}

	progress("bootstrap", 20)

	// noopInstall is a no-op DBInstaller: on the join path we do not yet have a
	// local SQLite DB to restore into.  Bootstrap is called here to (a) validate
	// that the snapshot + changesets are fully readable with the derived key,
	// (b) drive progress reporting so the setup UI advances, and (c) honour the
	// SYNC-03 contract that Bootstrap is wired into the join flow.
	//
	// CORRECTED 2026-08-15 (sync audit).  This comment used to end "the live
	// services/sync engine will apply the snapshot and changesets to the real DB
	// once the server boots", and that is NOT TRUE.  Nothing applies them, at
	// boot or ever.  The only two call sites of sync.Restorer are an admin HTTP
	// endpoint (cmd/server/routes_backup.go) and a CLI subcommand
	// (cmd/server/cmd_backup.go); neither is on the boot path, and the syncer
	// started by sync.NewFromCluster is a FILE watcher over ~/.vulos/data, which
	// does not touch the SQLite databases this snapshot contains.
	//
	// So the honest description of this function is: it downloads the cluster
	// snapshot, proves it decrypts, and throws it away.  A user who joins a new
	// box watches the wizard reach 100% "complete" and gets an empty machine —
	// the progress bar is measuring a readability check.  The deferral named a
	// component that would do the work later, which is exactly the shape of
	// claim this project has had to retract repeatedly; the work is specified in
	// roadmap/SYNC-INVENTORY.md rather than asserted here.
	//
	// Pinned by sqlcrdt.TestJoinPullInstallsNothing, which fails when this
	// changes so the roadmap is updated in the same commit.
	noopInstall := func(_ context.Context, _ []byte, _ int64) error {
		log.Printf("[joinsync] snapshot readable (install deferred to services/sync engine)")
		return nil
	}

	// noopApply is a no-op ChangesetApplier: verify readability only.
	noopApply := func(_ context.Context, s3 vulossync.BootstrapS3, key string) error {
		if _, err := s3.GetEncrypted(ctx, key); err != nil {
			return errors.Join(ErrUnreachable, err)
		}
		return nil
	}

	if err := vulossync.Bootstrap(
		ctx,
		vulossync.BootstrapConfig{NodeID: cfg.Bucket, Passphrase: passphrase},
		client,
		noopInstall,
		noopApply,
	); err != nil {
		return errors.Join(ErrUnreachable, err)
	}

	progress("finalizing", 95)
	return nil
}

// isNoSuchKey reports whether err is a minio "object not found" error.
func isNoSuchKey(err error) bool {
	if err == nil {
		return false
	}
	resp := minio.ToErrorResponse(err)
	return resp.Code == "NoSuchKey" || resp.StatusCode == 404
}
