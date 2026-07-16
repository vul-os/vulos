// Package files is the vulos.cloud control-plane Files subsystem: the source of
// truth for a cloud account's Drive index (files + folders) and its version
// history. Bytes live in the account's unified object-store bucket; the tree
// lives here, in the CP's own "files" schema.
//
// This is the CLOUD Drive (phase 1). It is deliberately narrower than the OS
// Files service it mirrors: there are no ACLs, no shares, no share links, no
// external mounts, no import jobs and no peer sharing on the CP. A node belongs
// to exactly one owner and only that owner can ever reach it, so authorization
// collapses to a single self-only check on every call.
//
// The object key is DERIVED FROM the DB-held path (driveKey), never the other
// way round — which is why a move/rename has to relocate bytes as well as rows.
package files

import (
	"context"
	"errors"
	"time"
)

// DrivePrefix is the Drive area within an account's unified bucket. A user's
// real documents live at "<ownerID>/drive/…", DISTINCT from the per-app state
// at "<accountID>/<appID>/" that /api/storage/presign scopes an app token to.
// Nothing reachable with an app token lives under this prefix.
const DrivePrefix = "drive/"

// Sentinel errors returned by the Service. The route layer maps them to status
// codes; ErrForbidden stays distinct from ErrNotFound so a caller that asked
// for a node that exists but is not theirs is answered differently from one
// that asked for a node that is simply gone.
var (
	ErrNotFound  = errors.New("files: not found")
	ErrForbidden = errors.New("files: forbidden")
	ErrInvalid   = errors.New("files: invalid request")
)

// Node is a file or folder in the Drive index. Folders are metadata-only: they
// carry no bucket and no object key, and no object is ever written for them.
type Node struct {
	ID               string    `json:"id"`
	OwnerID          string    `json:"owner_id"`
	ParentID         string    `json:"parent_id"`
	Name             string    `json:"name"`
	IsDir            bool      `json:"is_dir"`
	Bucket           string    `json:"bucket,omitempty"`
	ObjectKey        string    `json:"object_key,omitempty"`
	Path             string    `json:"path"`
	Size             int64     `json:"size"`
	ContentType      string    `json:"content_type,omitempty"`
	CurrentVersionID string    `json:"current_version_id,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

// Version is an append-only history row for a file node. VersionKey is the
// node's object key: every version of a node points at the SAME key, and an
// upload overwrites the previous bytes. Version history is metadata-only — the
// CP does not use S3 object versioning, so an older version's bytes are gone.
type Version struct {
	ID          string    `json:"id"`
	NodeID      string    `json:"node_id"`
	VersionKey  string    `json:"version_key"`
	Size        int64     `json:"size"`
	ContentType string    `json:"content_type,omitempty"`
	ETag        string    `json:"etag,omitempty"`
	CreatedBy   string    `json:"created_by"`
	CreatedAt   time.Time `json:"created_at"`
}

// ObjectGrant is a short-lived authorization to access ONE object directly in
// the bucket. The CP only ever mints presigned grants (the browser PUTs/GETs
// the bucket itself; bytes never transit the CP), so Type is always
// "presigned" and URL is always set.
type ObjectGrant struct {
	Type      string    `json:"type"`
	Method    string    `json:"method"`
	Bucket    string    `json:"bucket"`
	Key       string    `json:"key"`
	URL       string    `json:"url"`
	ExpiresAt time.Time `json:"expires_at"`
}

// GrantPresigned is the only grant type the CP mints.
const GrantPresigned = "presigned"

// Broker mints object-scoped grants and moves/removes object bytes. The Service
// calls it ONLY after its self-only check has authorized the caller, so an
// unauthorized request never reaches the bucket. It is an interface (rather
// than *storage.Service directly) so this package stays free of the storage
// plane's cross-tenant pinning rules — those live with the credential, in
// cmd/server.
type Broker interface {
	MintRead(ctx context.Context, ownerID, bucket, key string, ttl time.Duration) (ObjectGrant, error)
	MintWrite(ctx context.Context, ownerID, bucket, key string, ttl time.Duration) (ObjectGrant, error)
	// MoveObject relocates one object's bytes from srcKey to dstKey. A missing
	// source is a no-op success, so a PENDING node (one whose upload grant was
	// minted but whose bytes never landed) still reparents cleanly.
	MoveObject(ctx context.Context, ownerID, bucket, srcKey, dstKey string) error
	// DeleteObject removes one object's bytes. A missing object is a no-op
	// success. Used by the tombstone purge to reclaim bytes — soft delete alone
	// only flips deleted=1 and never frees a byte.
	DeleteObject(ctx context.Context, ownerID, bucket, key string) error
}

// BucketResolver returns the unified bucket an owner's Drive lives in (their
// managed "vulos-<boxULID>" bucket, or their BYO bucket).
type BucketResolver func(ctx context.Context, ownerID string) string
