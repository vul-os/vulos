package files

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"vulos/backend/internal/storage"
	"vulos/backend/internal/ulid"
)

// Sentinel errors returned by the service. Callers (HTTP routes) map these to
// status codes; ErrForbidden is deliberately distinct from ErrNotFound so the
// route layer can choose to 404 unauthorized reads (anti-enumeration) while
// 403-ing authorized-but-insufficient writes.
var (
	ErrForbidden    = errors.New("files: forbidden")
	ErrInvalid      = errors.New("files: invalid request")
	ErrLinkInactive = errors.New("files: share link expired or revoked")
)

// maxLinkTTL caps how long a share link may live. Defense against effectively
// permanent public links.
const maxLinkTTL = 30 * 24 * time.Hour

// defaultLinkTTL is used when a link-create request omits a TTL.
const defaultLinkTTL = 7 * 24 * time.Hour

// Service is the Files metadata/control-plane. It owns the Drive index, ACLs,
// share links and versions, and orchestrates ACL-gated object-scoped grants via
// the storage Broker. It is safe for concurrent use (SQLite serializes writes;
// reads are independent).
type Service struct {
	db       *sql.DB
	broker   Broker
	bucketFn BucketResolver
}

// New opens (creating + migrating) the Files DB at dbPath and returns a Service.
// broker mints object-scoped storage grants (ACL-gated by this service before
// any mint). bucketFn maps an owner to their per-user bucket. Either dependency
// may be nil in tests that don't exercise grants.
func New(dbPath string, broker Broker, bucketFn BucketResolver) (*Service, error) {
	db, err := openDB(dbPath)
	if err != nil {
		return nil, fmt.Errorf("files: open db: %w", err)
	}
	if bucketFn == nil {
		bucketFn = func(string) string { return "" }
	}
	return &Service{db: db, broker: broker, bucketFn: bucketFn}, nil
}

// --- ACL resolution -------------------------------------------------------

// EffectiveRole resolves userID's role on nodeID, taking the HIGHEST of: bucket
// ownership, a direct ACL on the node, and ACLs inherited from ancestor folders.
// Returns ("", nil) when the user has no access. ErrNotFound if the node is gone.
func (s *Service) EffectiveRole(userID, nodeID string) (Role, error) {
	n, err := s.getNode(nodeID)
	if err != nil {
		return "", err
	}
	return s.effectiveRoleNode(userID, n), nil
}

// effectiveRoleNode walks the node + its ancestors accumulating the highest role.
// Bounded depth guards against a malformed cyclic parent chain.
func (s *Service) effectiveRoleNode(userID string, n *Node) Role {
	best := 0
	cur := n
	for i := 0; i < 256 && cur != nil; i++ {
		if cur.OwnerID == userID {
			return RoleOwner // owner of the bucket subtree
		}
		if r, ok := s.aclFor(cur.ID, userID); ok {
			if rr := roleRank(r); rr > best {
				best = rr
			}
		}
		if cur.ParentID == "" {
			break
		}
		p, err := s.getNode(cur.ParentID)
		if err != nil {
			break
		}
		cur = p
	}
	switch best {
	case 3:
		return RoleOwner
	case 2:
		return RoleEditor
	case 1:
		return RoleViewer
	default:
		return ""
	}
}

// authorize reports whether userID holds at least required on node.
func (s *Service) authorize(userID string, n *Node, required Role) bool {
	return roleRank(s.effectiveRoleNode(userID, n)) >= roleRank(required)
}

// --- listing / folders ----------------------------------------------------

// List returns the children of parentID visible to userID. parentID "" lists the
// user's own Drive root. For a non-root folder the caller needs viewer+.
func (s *Service) List(userID, parentID string) ([]*Node, error) {
	if parentID == "" {
		// Drive root is implicit and private to its owner.
		return s.childrenOwnedBy(userID, "")
	}
	parent, err := s.getNode(parentID)
	if err != nil {
		return nil, err
	}
	if !s.authorize(userID, parent, RoleViewer) {
		return nil, ErrForbidden
	}
	return s.listChildren(parentID)
}

// childrenOwnedBy lists root-level nodes owned by userID (their Drive root).
func (s *Service) childrenOwnedBy(ownerID, parentID string) ([]*Node, error) {
	all, err := s.listChildren(parentID)
	if err != nil {
		return nil, err
	}
	out := all[:0]
	for _, n := range all {
		if n.OwnerID == ownerID {
			out = append(out, n)
		}
	}
	return out, nil
}

// resolveParent validates parentID for a create operation under userID and
// returns (ownerID, parentPath). For root (""), the owner is the caller.
func (s *Service) resolveParent(userID, parentID string) (ownerID, parentPath string, err error) {
	if parentID == "" {
		return userID, "", nil
	}
	parent, e := s.getNode(parentID)
	if e != nil {
		return "", "", e
	}
	if !parent.IsDir {
		return "", "", ErrInvalid
	}
	if !s.authorize(userID, parent, RoleEditor) {
		return "", "", ErrForbidden
	}
	return parent.OwnerID, parent.Path, nil
}

// CreateFolder creates a folder named name under parentID. Bytes-less; the new
// node belongs to the parent's bucket owner (so collaborators create within the
// owner's Drive). Requires editor+ on a non-root parent.
func (s *Service) CreateFolder(userID, parentID, name string) (*Node, error) {
	if err := validName(name); err != nil {
		return nil, err
	}
	ownerID, parentPath, err := s.resolveParent(userID, parentID)
	if err != nil {
		return nil, err
	}
	if _, err := s.childByName(ownerID, parentID, name); err == nil {
		return nil, fmt.Errorf("%w: name already exists", ErrInvalid)
	}
	now := time.Now()
	n := &Node{
		ID:        ulid.NewULID(),
		OwnerID:   ownerID,
		ParentID:  parentID,
		Name:      name,
		IsDir:     true,
		Path:      joinPath(parentPath, name),
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.insertNode(n); err != nil {
		return nil, err
	}
	s.audit(userID, "folder.create", n.ID, n.Path)
	return n, nil
}

// --- upload / download grants --------------------------------------------

// UploadGrant locates or creates the file node (parentID,name) and mints an
// ACL-gated object-scoped WRITE grant for its bytes. Requires editor+ on the
// parent (or ownership of the Drive root). The node is created "pending" (size
// 0) so it appears in listings; Commit finalizes a version after the bytes land.
func (s *Service) UploadGrant(ctx context.Context, userID, parentID, name, contentType string, ttl time.Duration) (*Node, storage.ObjectGrant, error) {
	if err := validName(name); err != nil {
		return nil, storage.ObjectGrant{}, err
	}
	ownerID, parentPath, err := s.resolveParent(userID, parentID)
	if err != nil {
		return nil, storage.ObjectGrant{}, err
	}
	n, err := s.childByName(ownerID, parentID, name)
	if errors.Is(err, ErrNotFound) {
		now := time.Now()
		path := joinPath(parentPath, name)
		n = &Node{
			ID:          ulid.NewULID(),
			OwnerID:     ownerID,
			ParentID:    parentID,
			Name:        name,
			IsDir:       false,
			Bucket:      s.bucketFn(ownerID),
			ObjectKey:   driveKey(ownerID, path),
			Path:        path,
			ContentType: contentType,
			CreatedAt:   now,
			UpdatedAt:   now,
		}
		if err := s.insertNode(n); err != nil {
			return nil, storage.ObjectGrant{}, err
		}
	} else if err != nil {
		return nil, storage.ObjectGrant{}, err
	} else if n.IsDir {
		return nil, storage.ObjectGrant{}, fmt.Errorf("%w: target is a folder", ErrInvalid)
	}
	grant, err := s.mint(ctx, AccessWrite, n, ttl)
	if err != nil {
		return nil, storage.ObjectGrant{}, err
	}
	s.audit(userID, "grant.write", n.ID, string(grant.Type))
	return n, grant, nil
}

// DownloadGrant mints an ACL-gated object-scoped READ grant for nodeID. Requires
// viewer+. Folders have no bytes, so they are rejected.
func (s *Service) DownloadGrant(ctx context.Context, userID, nodeID string, ttl time.Duration) (*Node, storage.ObjectGrant, error) {
	n, err := s.getNode(nodeID)
	if err != nil {
		return nil, storage.ObjectGrant{}, err
	}
	if n.IsDir {
		return nil, storage.ObjectGrant{}, fmt.Errorf("%w: folders have no bytes", ErrInvalid)
	}
	if !s.authorize(userID, n, RoleViewer) {
		return nil, storage.ObjectGrant{}, ErrForbidden
	}
	grant, err := s.mint(ctx, AccessRead, n, ttl)
	if err != nil {
		return nil, storage.ObjectGrant{}, err
	}
	s.audit(userID, "grant.read", n.ID, string(grant.Type))
	return n, grant, nil
}

// mint is the single chokepoint that turns an authorized request into a storage
// grant. It assumes the ACL has ALREADY been checked by the caller; it never
// performs the mint for a write when only read was authorized because the caller
// passes the access it verified.
func (s *Service) mint(ctx context.Context, access Access, n *Node, ttl time.Duration) (storage.ObjectGrant, error) {
	if s.broker == nil {
		return storage.ObjectGrant{}, fmt.Errorf("files: storage broker unavailable")
	}
	if access == AccessWrite {
		return s.broker.MintWrite(ctx, n.OwnerID, n.Bucket, n.ObjectKey, ttl)
	}
	return s.broker.MintRead(ctx, n.OwnerID, n.Bucket, n.ObjectKey, ttl)
}

// Commit records a new version after bytes have been uploaded via a write grant,
// updating the node's size/content-type/current version. Requires editor+.
func (s *Service) Commit(userID, nodeID string, size int64, contentType, etag string) (*Version, error) {
	n, err := s.getNode(nodeID)
	if err != nil {
		return nil, err
	}
	if n.IsDir {
		return nil, ErrInvalid
	}
	if !s.authorize(userID, n, RoleEditor) {
		return nil, ErrForbidden
	}
	v := Version{
		ID:          ulid.NewULID(),
		NodeID:      n.ID,
		VersionKey:  n.ObjectKey,
		Size:        size,
		ContentType: contentType,
		ETag:        etag,
		CreatedBy:   userID,
		CreatedAt:   time.Now(),
	}
	if err := s.insertVersion(v); err != nil {
		return nil, err
	}
	n.Size = size
	if contentType != "" {
		n.ContentType = contentType
	}
	n.CurrentVersionID = v.ID
	if err := s.touchNode(n); err != nil {
		return nil, err
	}
	s.audit(userID, "file.commit", n.ID, fmt.Sprintf("size=%d", size))
	return &v, nil
}

// Versions lists a node's version history (newest first). Requires viewer+.
func (s *Service) Versions(userID, nodeID string) ([]Version, error) {
	n, err := s.getNode(nodeID)
	if err != nil {
		return nil, err
	}
	if !s.authorize(userID, n, RoleViewer) {
		return nil, ErrForbidden
	}
	return s.listVersions(nodeID)
}

// --- move / rename / delete ----------------------------------------------

// Move renames nodeID and/or reparents it to newParentID (empty = keep parent).
// newName empty keeps the name. Requires editor+ on the node and on the new
// parent, and forbids moving across bucket owners (a Phase-2+ concern). Folder
// moves recompute descendant paths and object keys.
func (s *Service) Move(userID, nodeID, newParentID, newName string) (*Node, error) {
	n, err := s.getNode(nodeID)
	if err != nil {
		return nil, err
	}
	if !s.authorize(userID, n, RoleEditor) {
		return nil, ErrForbidden
	}
	parentID := n.ParentID
	parentPath := ""
	if newParentID != "" && newParentID != n.ParentID {
		parent, err := s.getNode(newParentID)
		if err != nil {
			return nil, err
		}
		if !parent.IsDir {
			return nil, ErrInvalid
		}
		if parent.OwnerID != n.OwnerID {
			return nil, fmt.Errorf("%w: cross-owner move not supported", ErrInvalid)
		}
		if !s.authorize(userID, parent, RoleEditor) {
			return nil, ErrForbidden
		}
		if isAncestor(s, n.ID, parent) {
			return nil, fmt.Errorf("%w: cannot move a folder into itself", ErrInvalid)
		}
		parentID = newParentID
		parentPath = parent.Path
	} else if n.ParentID != "" {
		if p, err := s.getNode(n.ParentID); err == nil {
			parentPath = p.Path
		}
	}
	name := n.Name
	if newName != "" {
		if err := validName(newName); err != nil {
			return nil, err
		}
		name = newName
	}
	// Reject collisions with an existing sibling.
	if existing, err := s.childByName(n.OwnerID, parentID, name); err == nil && existing.ID != n.ID {
		return nil, fmt.Errorf("%w: name already exists in destination", ErrInvalid)
	}
	n.ParentID = parentID
	n.Name = name
	if err := s.reapplyPaths(n, parentPath); err != nil {
		return nil, err
	}
	s.audit(userID, "node.move", n.ID, n.Path)
	return n, nil
}

// reapplyPaths recomputes path (and object_key for files) for n and, when n is a
// folder, its whole subtree. Bytes are NOT physically relocated here — the index
// records the new canonical Drive key; a data-plane mover reconciles bytes in a
// later phase. Bounded recursion via the natural tree depth.
func (s *Service) reapplyPaths(n *Node, parentPath string) error {
	n.Path = joinPath(parentPath, n.Name)
	if !n.IsDir {
		n.ObjectKey = driveKey(n.OwnerID, n.Path)
	}
	if err := s.touchNode(n); err != nil {
		return err
	}
	if !n.IsDir {
		return nil
	}
	kids, err := s.listChildren(n.ID)
	if err != nil {
		return err
	}
	for _, k := range kids {
		if err := s.reapplyPaths(k, n.Path); err != nil {
			return err
		}
	}
	return nil
}

// Delete soft-deletes nodeID (and, for a folder, leaves descendants tombstoned
// implicitly via the parent filter). Requires editor+.
func (s *Service) Delete(userID, nodeID string) error {
	n, err := s.getNode(nodeID)
	if err != nil {
		return err
	}
	if !s.authorize(userID, n, RoleEditor) {
		return ErrForbidden
	}
	if err := s.softDelete(nodeID); err != nil {
		return err
	}
	s.audit(userID, "node.delete", nodeID, n.Path)
	return nil
}

// --- sharing --------------------------------------------------------------

// Share grants principalID the role on nodeID. Only the owner may manage ACLs.
// role must be editor or viewer (ownership is not transferable here).
func (s *Service) Share(actorID, nodeID, principalID string, role Role) (*ACLEntry, error) {
	if !validShareRole(role) {
		return nil, fmt.Errorf("%w: role must be editor or viewer", ErrInvalid)
	}
	if principalID == "" {
		return nil, fmt.Errorf("%w: principal required", ErrInvalid)
	}
	n, err := s.getNode(nodeID)
	if err != nil {
		return nil, err
	}
	if !s.authorize(actorID, n, RoleOwner) {
		return nil, ErrForbidden
	}
	if principalID == n.OwnerID {
		return nil, fmt.Errorf("%w: cannot share with the owner", ErrInvalid)
	}
	e := ACLEntry{
		ID:          ulid.NewULID(),
		NodeID:      nodeID,
		PrincipalID: principalID,
		Role:        role,
		CreatedBy:   actorID,
		CreatedAt:   time.Now(),
	}
	if err := s.upsertACL(e); err != nil {
		return nil, err
	}
	s.audit(actorID, "share.grant", nodeID, principalID+"="+string(role))
	return &e, nil
}

// Unshare revokes principalID's direct ACL on nodeID. Owner only.
func (s *Service) Unshare(actorID, nodeID, principalID string) error {
	n, err := s.getNode(nodeID)
	if err != nil {
		return err
	}
	if !s.authorize(actorID, n, RoleOwner) {
		return ErrForbidden
	}
	if err := s.deleteACL(nodeID, principalID); err != nil {
		return err
	}
	s.audit(actorID, "share.revoke", nodeID, principalID)
	return nil
}

// ListShares returns the direct ACL entries on nodeID. Owner only.
func (s *Service) ListShares(actorID, nodeID string) ([]ACLEntry, error) {
	n, err := s.getNode(nodeID)
	if err != nil {
		return nil, err
	}
	if !s.authorize(actorID, n, RoleOwner) {
		return nil, ErrForbidden
	}
	return s.listACLs(nodeID)
}

// SharedWithMe returns the nodes directly shared with userID (the roots of the
// "shared with me" view). Inherited children are reachable by listing into them.
func (s *Service) SharedWithMe(userID string) ([]*Node, error) {
	ids, err := s.nodesSharedWith(userID)
	if err != nil {
		return nil, err
	}
	var out []*Node
	for _, id := range ids {
		n, err := s.getNode(id)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, nil
}

// --- share links ----------------------------------------------------------

// CreateLink mints an expiring, revocable capability token granting role on
// nodeID. Owner only. ttl is clamped to (0, maxLinkTTL]; 0 uses defaultLinkTTL.
func (s *Service) CreateLink(actorID, nodeID string, role Role, ttl time.Duration) (*ShareLink, error) {
	if !validShareRole(role) {
		return nil, fmt.Errorf("%w: role must be editor or viewer", ErrInvalid)
	}
	n, err := s.getNode(nodeID)
	if err != nil {
		return nil, err
	}
	if !s.authorize(actorID, n, RoleOwner) {
		return nil, ErrForbidden
	}
	if ttl <= 0 {
		ttl = defaultLinkTTL
	}
	if ttl > maxLinkTTL {
		ttl = maxLinkTTL
	}
	now := time.Now()
	l := ShareLink{
		Token:     ulid.NewULID() + ulid.NewULID(), // 52 chars of entropy
		NodeID:    nodeID,
		Role:      role,
		CreatedBy: actorID,
		CreatedAt: now,
		ExpiresAt: now.Add(ttl),
	}
	if err := s.insertLink(l); err != nil {
		return nil, err
	}
	s.audit(actorID, "link.create", nodeID, string(role))
	return &l, nil
}

// RevokeLink revokes a share link. Owner of the linked node only.
func (s *Service) RevokeLink(actorID, token string) error {
	l, err := s.getLink(token)
	if err != nil {
		return err
	}
	n, err := s.getNode(l.NodeID)
	if err != nil {
		return err
	}
	if !s.authorize(actorID, n, RoleOwner) {
		return ErrForbidden
	}
	if err := s.revokeLink(token); err != nil {
		return err
	}
	s.audit(actorID, "link.revoke", l.NodeID, "")
	return nil
}

// ListLinks returns the share links on nodeID. Owner only.
func (s *Service) ListLinks(actorID, nodeID string) ([]ShareLink, error) {
	n, err := s.getNode(nodeID)
	if err != nil {
		return nil, err
	}
	if !s.authorize(actorID, n, RoleOwner) {
		return nil, ErrForbidden
	}
	return s.listLinks(nodeID)
}

// RedeemLink turns an active share-link token into a READ grant for the linked
// file, for any authenticated redeemer (the token is the capability). The link's
// role governs future write support; the foundation issues read grants only.
func (s *Service) RedeemLink(ctx context.Context, userID, token string, ttl time.Duration) (*Node, storage.ObjectGrant, error) {
	l, err := s.getLink(token)
	if err != nil {
		return nil, storage.ObjectGrant{}, err
	}
	if !l.Active() {
		return nil, storage.ObjectGrant{}, ErrLinkInactive
	}
	n, err := s.getNode(l.NodeID)
	if err != nil {
		return nil, storage.ObjectGrant{}, err
	}
	if n.IsDir {
		return nil, storage.ObjectGrant{}, fmt.Errorf("%w: link targets a folder", ErrInvalid)
	}
	grant, err := s.mint(ctx, AccessRead, n, ttl)
	if err != nil {
		return nil, storage.ObjectGrant{}, err
	}
	s.audit(userID, "link.redeem", n.ID, l.Token[:8])
	return n, grant, nil
}

// --- audit ----------------------------------------------------------------

// audit appends a security-sensitive event to the durable audit table AND the
// process log. Best-effort: a DB failure is logged but never blocks the action.
func (s *Service) audit(actorID, action, nodeID, detail string) {
	id := ulid.NewULID()
	ts := time.Now().UTC()
	if _, err := s.db.Exec(`INSERT INTO files_audit(id, ts, actor_id, action, node_id, detail)
		VALUES(?,?,?,?,?,?)`, id, ts.Format(rfc), actorID, action, nodeID, detail); err != nil {
		log.Printf("[files-audit] persist failed: %v", err)
	}
	log.Printf("[files-audit] %s actor=%q action=%s node=%s %s",
		ts.Format(time.RFC3339), actorID, action, nodeID, detail)
}

// --- helpers --------------------------------------------------------------

// validName rejects empty, over-long, traversal, and separator-bearing names.
func validName(name string) error {
	if name == "" || len(name) > 255 {
		return fmt.Errorf("%w: invalid name length", ErrInvalid)
	}
	if name == "." || name == ".." {
		return fmt.Errorf("%w: reserved name", ErrInvalid)
	}
	if strings.ContainsAny(name, "/\\\x00") {
		return fmt.Errorf("%w: name contains a path separator", ErrInvalid)
	}
	return nil
}

// joinPath joins a parent Drive-relative path with a child name.
func joinPath(parentPath, name string) string {
	if parentPath == "" {
		return name
	}
	return parentPath + "/" + name
}

// driveKey returns the canonical object key for a Drive path: the per-user Drive
// area "<ownerID>/drive/<path>". This is the single source of the bucket layout.
func driveKey(ownerID, path string) string {
	return ownerID + "/" + DrivePrefix + path
}

// isAncestor reports whether candidate is nodeID itself or a descendant of it —
// used to forbid moving a folder into its own subtree.
func isAncestor(s *Service, nodeID string, candidate *Node) bool {
	cur := candidate
	for i := 0; i < 256 && cur != nil; i++ {
		if cur.ID == nodeID {
			return true
		}
		if cur.ParentID == "" {
			return false
		}
		p, err := s.getNode(cur.ParentID)
		if err != nil {
			return false
		}
		cur = p
	}
	return false
}
