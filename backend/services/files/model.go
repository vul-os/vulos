// Package files is the Vulos OS Files metadata / control-plane service: the
// source of truth for the per-user Drive index (files + folders), ACLs/shares,
// expiring/revocable share links, versions, and a seam for external-store
// connections. Bytes live in the per-user object-store buckets; the truth lives
// here, in the OS DB.
//
// This is PHASE 1 (foundation): the index + ACL + ACL-gated object-scoped broker
// grants + the session-authed HTTP control plane that Phase 2 (Files OS app,
// Workspace surface, OS peer-share) builds on. It deliberately does NOT include
// the Files UI, peer-share/p2p, the org/tenant directory, or external mounts —
// those are later phases. See the canonical design: vulos-files-architecture.
package files

import (
	"context"
	"io"
	"time"

	"vulos/backend/internal/storage"
)

// DrivePrefix is the canonical Drive area within a user's per-user bucket. A
// user's real documents live at "<userID>/drive/...", DISTINCT from per-app
// state at "<userID>/<appID>/". This is the layout the Office "Save" target and
// the Files app both browse.
const DrivePrefix = "drive/"

// Role is an access level on a node. Ordering: owner > editor > viewer.
type Role string

const (
	RoleOwner  Role = "owner"
	RoleEditor Role = "editor"
	RoleViewer Role = "viewer"
)

// roleRank ranks roles for "highest wins" resolution; unknown/none == 0.
func roleRank(r Role) int {
	switch r {
	case RoleOwner:
		return 3
	case RoleEditor:
		return 2
	case RoleViewer:
		return 1
	default:
		return 0
	}
}

// validShareRole reports whether r is a role that may be granted via a share or
// share link. Owner is never grantable — ownership follows the bucket.
func validShareRole(r Role) bool { return r == RoleEditor || r == RoleViewer }

// Access is the access level a grant request needs. read requires viewer+,
// write requires editor+.
type Access string

const (
	AccessRead  Access = "read"
	AccessWrite Access = "write"
)

func (a Access) requiredRole() Role {
	if a == AccessWrite {
		return RoleEditor
	}
	return RoleViewer
}

// Node is a file or folder in the Drive index.
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

// ACLEntry is a single (principal, role) grant on a node.
type ACLEntry struct {
	ID          string    `json:"id"`
	NodeID      string    `json:"node_id"`
	PrincipalID string    `json:"principal_id"`
	Role        Role      `json:"role"`
	CreatedBy   string    `json:"created_by"`
	CreatedAt   time.Time `json:"created_at"`
}

// ShareLink is an expiring, revocable capability token granting a role on a node.
type ShareLink struct {
	Token     string    `json:"token"`
	NodeID    string    `json:"node_id"`
	Role      Role      `json:"role"`
	CreatedBy string    `json:"created_by"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
	Revoked   bool      `json:"revoked"`
}

// Active reports whether the link can still be redeemed.
func (l ShareLink) Active() bool {
	return !l.Revoked && time.Now().Before(l.ExpiresAt)
}

// Version is an immutable history row for a file node.
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

// Broker mints object-scoped, short-lived storage grants for a single object.
// *storage.GrantBroker satisfies it. The Files service calls a Broker ONLY after
// its ACL check authorizes the requester, so an unauthorized user never reaches
// the minter. Defined as an interface so the gating can be tested with a fake.
type Broker interface {
	MintRead(ctx context.Context, ownerID, bucket, key string, ttl time.Duration) (storage.ObjectGrant, error)
	MintWrite(ctx context.Context, ownerID, bucket, key string, ttl time.Duration) (storage.ObjectGrant, error)
	// MoveObject physically relocates one object's bytes from srcKey to dstKey in
	// the owner's store (the PHASE-2A byte-mover). The Files service calls it on
	// move/rename, then commits the matching index keys; a missing source is a
	// no-op so pending (not-yet-uploaded) nodes still reparent cleanly.
	MoveObject(ctx context.Context, ownerID, bucket, srcKey, dstKey string) error
	// PutContent / GetContent are the OS-mediated data plane: they move the actual
	// bytes for callers that cannot talk to the bucket directly (chiefly STANDALONE
	// local-FS mode, where the browser has no presigned URL). The Files service
	// ACL-gates both before calling them.
	PutContent(ctx context.Context, ownerID, bucket, key string, r io.Reader, size int64, contentType string) (string, error)
	GetContent(ctx context.Context, ownerID, bucket, key string) (io.ReadCloser, int64, error)
	// DeleteObject permanently removes ONE object's bytes from the owner's store.
	// A missing object is a no-op success (already gone, or a pending node that
	// was never uploaded). Used by the tombstone purge sweep to reclaim bytes for
	// nodes that were soft-deleted past the retention window — soft delete alone
	// only marks a row deleted=1; it never frees bucket bytes without this.
	DeleteObject(ctx context.Context, ownerID, bucket, key string) error
}

// BucketResolver returns the per-user account bucket for a user. Wired from
// *storage.Resolver.BucketFor in production.
type BucketResolver func(userID string) string
