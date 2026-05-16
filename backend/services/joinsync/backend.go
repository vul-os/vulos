package joinsync

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"vulos/backend/services/cluster"

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

// pull performs the post-join data pull. The actual cr-sqlite changeset sync
// is owned by cluster.SyncLoop and runs continuously once the server boots in
// "sync" mode with the persisted credentials; here we just verify the bucket
// is browsable with the derived key and report progress so the setup UI can
// advance. Heavy lifting is intentionally NOT duplicated from cluster.
func (realBackend) pull(ctx context.Context, cfg cluster.S3Config, passphrase string, progress func(phase string, pct int)) error {
	progress("connecting", 5)

	client, err := cluster.NewClient(ctx, cfg, passphrase)
	if err != nil {
		return errors.Join(ErrUnreachable, err)
	}

	progress("listing", 20)
	keys, err := client.ListPrefix(ctx, "nodes/")
	if err != nil {
		return errors.Join(ErrUnreachable, err)
	}

	progress("downloading", 60)
	// Touch each peer changeset so credentials/key are proven end-to-end.
	// cluster.SyncLoop applies them to the local DB on the normal boot path;
	// we only validate readability here to keep this surgical.
	for _, k := range keys {
		if filepath.Ext(k) != ".bin" {
			continue
		}
		if _, err := client.GetEncrypted(ctx, k); err != nil {
			return errors.Join(ErrUnreachable, err)
		}
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
