// wire_files.go — opens the cloud Files control plane and adapts the CP storage
// plane to it.
//
// THE SEAM. internal/files owns the Drive index and knows nothing about how the
// CP holds bucket credentials. Everything that DOES know — the cross-tenant
// bucket pin (STORAGE-IDOR), the Drive key-prefix confinement, the per-account
// provider selection (managed vs BYO) — lives here, in the one package that
// already carries those rules for /api/storage/*. The Files service only ever
// reaches the bucket through this broker, and only after its own self-only check
// has passed.
//
// The DB handle is deliberately not closed: it lives for the process lifetime
// and is released at exit, like the process's other long-lived pools.
//
// COORDINATOR: this file depends on the *storageHandlers value from
// routes_storage.go (sh.svc, sh.callerOwnsBucket, sh.svc.ProviderForAccount,
// sh.svc.GetConfig) and on boxULID (composition-root helper, boxid.go). Unify
// once routes_storage.go lands in cproutes.
package cproutes

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"strings"
	"time"

	"github.com/vul-os/vulos-management/pkg/cpdb"
	"github.com/vul-os/vulos-management/pkg/files"
	"github.com/vul-os/vulos-management/pkg/storage"
)

// filesPurgeSvc is the Files service the scheduler's tombstone-purge job sweeps.
// Set once by WireFiles; nil (Files failed to open) makes the job a no-op. Same
// package-level-handoff idiom as appIdentityStore (wire_apptoken.go).
var filesPurgeSvc *files.Service

// FilesPurgeSvc returns the Files service the tombstone-purge sweeper runs
// against (nil when Files failed to open). Exported so a commercial scheduler
// (wire_scheduler.go) can reach the SAME service WireFiles opened, rather than
// opening a second Files index.
func FilesPurgeSvc() *files.Service { return filesPurgeSvc }

// WireFiles opens the Files DB through the cpdb seam and returns a ready
// Service, or nil when the DB cannot be opened.
//
// A nil Service makes every /api/files route answer 503, and the Workspace
// surface shows its calm "Files isn't available" screen. There is deliberately
// NO in-memory fallback: an index that forgets the tree on restart while the
// bytes persist in the bucket is worse than an honest outage — the files would
// still be billed and would no longer be reachable.
//
// VULOS_DB_DIR is already set by the time this runs (wireStorage → openCPDB →
// setDBDirIfUnset), so cpdb.Open lands the SQLite file alongside every other
// store; on cloud it opens the "files" Postgres schema instead.
func WireFiles(sh *storageHandlers) *files.Service {
	db, err := cpdb.Open("files")
	if err != nil {
		log.Printf("[files] WARNING: failed to open files DB (%v) — /api/files routes return 503", err)
		return nil
	}
	svc, err := files.Open(db, &filesBroker{sh: sh}, filesBucketResolver(sh))
	if err != nil {
		_ = db.Close()
		log.Printf("[files] WARNING: failed to migrate files DB (%v) — /api/files routes return 503", err)
		return nil
	}
	log.Printf("[files] Drive index opened (backend=%s)", db.Backend())
	filesPurgeSvc = svc
	return svc
}

// filesBucketResolver resolves an owner to the unified bucket their Drive lives
// in: their BYO bucket when they have connected one, else the canonical managed
// bucket callerOwnsBucket pins them to. Files and every per-app prefix share
// that one bucket, so a Drive upload is sampled by the SAME usage gauge the
// storage quota is enforced against.
func filesBucketResolver(sh *storageHandlers) files.BucketResolver {
	return func(ctx context.Context, ownerID string) string {
		if cfg, err := sh.svc.GetConfig(ctx, ownerID); err == nil && cfg.Bucket != "" {
			return cfg.Bucket
		}
		return "vulos-" + strings.ToLower(boxULID(ownerID))
	}
}

// filesBroker adapts the CP storage plane to files.Broker. Every method re-checks
// the two invariants the Files index cannot enforce for itself, on the way to the
// credential:
//
//   - callerOwnsBucket — a managed account may only ever address its own
//     "vulos-<boxULID>" bucket (the fleet-wide Tigris credential can sign any of
//     them), so a corrupted row naming another tenant's bucket mints nothing.
//   - keyWithinDrivePrefix — the object must live under "<owner>/drive/". Keys
//     are already derived server-side from the DB path, so this can only fail on a
//     row that should not exist; it is refused rather than signed.
type filesBroker struct {
	sh *storageHandlers // COORDINATOR: needs storageHandlers (routes_storage.go)
}

var _ files.Broker = (*filesBroker)(nil)

// checkTarget enforces both invariants and returns the account's provider.
func (b *filesBroker) checkTarget(ctx context.Context, ownerID, bucket string, keys ...string) (storage.Provider, error) {
	if !b.sh.callerOwnsBucket(ctx, ownerID, bucket) {
		return nil, files.ErrForbidden
	}
	for _, k := range keys {
		if !keyWithinDrivePrefix(k, ownerID) {
			return nil, files.ErrForbidden
		}
	}
	return b.sh.svc.ProviderForAccount(ctx, ownerID)
}

func (b *filesBroker) MintRead(ctx context.Context, ownerID, bucket, key string, ttl time.Duration) (files.ObjectGrant, error) {
	p, err := b.checkTarget(ctx, ownerID, bucket, key)
	if err != nil {
		return files.ObjectGrant{}, err
	}
	url, err := p.PresignGet(ctx, bucket, key, ttl)
	if err != nil {
		return files.ObjectGrant{}, err
	}
	return files.ObjectGrant{
		Type:      files.GrantPresigned,
		Method:    "GET",
		Bucket:    bucket,
		Key:       key,
		URL:       url,
		ExpiresAt: time.Now().UTC().Add(ttl),
	}, nil
}

func (b *filesBroker) MintWrite(ctx context.Context, ownerID, bucket, key string, ttl time.Duration) (files.ObjectGrant, error) {
	p, err := b.checkTarget(ctx, ownerID, bucket, key)
	if err != nil {
		return files.ObjectGrant{}, err
	}
	url, err := p.PresignPut(ctx, bucket, key, ttl)
	if err != nil {
		return files.ObjectGrant{}, err
	}
	return files.ObjectGrant{
		Type:      files.GrantPresigned,
		Method:    "PUT",
		Bucket:    bucket,
		Key:       key,
		URL:       url,
		ExpiresAt: time.Now().UTC().Add(ttl),
	}, nil
}

// MoveObject relocates one object server-side: a copy followed by a delete of the
// source. TigrisProvider implements CopyObject, so production never streams the
// bytes through the CP; a provider without it (MemProvider) falls back to
// Get→Put→Delete.
//
// A missing source is a NO-OP SUCCESS: a node whose upload grant was minted but
// whose bytes never landed still has an object_key, and moving it must reparent
// the row rather than fail.
func (b *filesBroker) MoveObject(ctx context.Context, ownerID, bucket, srcKey, dstKey string) error {
	p, err := b.checkTarget(ctx, ownerID, bucket, srcKey, dstKey)
	if err != nil {
		return err
	}
	if c, ok := p.(interface {
		CopyObject(ctx context.Context, bucket, srcKey, dstKey string) error
	}); ok {
		if err := c.CopyObject(ctx, bucket, srcKey, dstKey); err != nil {
			if errors.Is(err, storage.ErrUnknownBucket) {
				return nil
			}
			return err
		}
		return b.deleteIfPresent(ctx, p, bucket, srcKey)
	}
	rc, err := p.GetObject(ctx, bucket, srcKey)
	if err != nil {
		if errors.Is(err, storage.ErrUnknownBucket) {
			return nil
		}
		return err
	}
	body, err := io.ReadAll(rc)
	_ = rc.Close()
	if err != nil {
		return err
	}
	if err := p.PutObject(ctx, bucket, dstKey, bytes.NewReader(body), int64(len(body))); err != nil {
		return err
	}
	return b.deleteIfPresent(ctx, p, bucket, srcKey)
}

func (b *filesBroker) DeleteObject(ctx context.Context, ownerID, bucket, key string) error {
	p, err := b.checkTarget(ctx, ownerID, bucket, key)
	if err != nil {
		return err
	}
	return b.deleteIfPresent(ctx, p, bucket, key)
}

// deleteIfPresent removes one object, treating "already gone" as success — the
// purge sweep and the move's source-delete must both be idempotent.
func (b *filesBroker) deleteIfPresent(ctx context.Context, p storage.Provider, bucket, key string) error {
	if err := p.DeleteObject(ctx, bucket, key); err != nil && !errors.Is(err, storage.ErrUnknownBucket) {
		return err
	}
	return nil
}

// drivePrefixFor returns an owner's Drive area within their unified bucket:
// "<ownerID>/drive/". It is its OWN prefix, distinct from the per-app
// "<accountID>/<appID>/" prefixes appStoragePrefix composes — which is why an app
// token, whose presigns are pinned to its own app prefix, can never reach a
// Drive object through /api/storage/presign.
func drivePrefixFor(ownerID string) string {
	return ownerID + "/" + files.DrivePrefix
}

// keyWithinDrivePrefix reports whether key lives strictly under
// drivePrefixFor(ownerID). Mirrors keyWithinAppPrefix: a bare prefix match, an
// empty suffix, or a ".." segment all fail.
func keyWithinDrivePrefix(key, ownerID string) bool {
	prefix := drivePrefixFor(ownerID)
	if !strings.HasPrefix(key, prefix) {
		return false
	}
	suffix := key[len(prefix):]
	if suffix == "" {
		return false
	}
	for _, seg := range strings.Split(suffix, "/") {
		if seg == ".." {
			return false
		}
	}
	return true
}
