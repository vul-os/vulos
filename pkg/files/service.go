package files

// service.go — the Drive control plane. Every entry point takes the CALLER's id
// (resolved from the CP session by the route layer, never from a request header
// or body) and re-checks that the node it touches is owned by that caller. The
// CP has no ACLs and no sharing, so "owned by the caller" is the whole
// authorization model — there is no path by which one account reaches another's
// node.

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"
)

// DefaultTombstoneRetention is how long a soft-deleted node's bytes are kept
// before PurgeTombstones reclaims them.
const DefaultTombstoneRetention = 30 * 24 * time.Hour

// maxAncestorDepth bounds the parent walk in isAncestor, so a malformed cyclic
// parent chain can never spin forever.
const maxAncestorDepth = 256

// ─── listing / folders ────────────────────────────────────────────────────────

// List returns the children of parentID. An empty parentID lists the caller's
// own Drive root (owner-filtered in SQL). A non-empty parentID must name a node
// the caller owns. An empty folder is an empty list, never an error.
func (s *Service) List(ctx context.Context, userID, parentID string) ([]*Node, error) {
	if parentID == "" {
		return s.childrenOwnedBy(ctx, userID)
	}
	parent, err := s.getNode(ctx, parentID)
	if err != nil {
		return nil, err
	}
	if parent.OwnerID != userID {
		return nil, ErrForbidden
	}
	return s.listChildren(ctx, parentID)
}

// resolveParent validates parentID for a create under userID and returns the
// parent's Drive-relative path. Root ("") is the caller's own, and always valid.
func (s *Service) resolveParent(ctx context.Context, userID, parentID string) (parentPath string, err error) {
	if parentID == "" {
		return "", nil
	}
	parent, err := s.getNode(ctx, parentID)
	if err != nil {
		return "", err
	}
	if parent.OwnerID != userID {
		return "", ErrForbidden
	}
	if !parent.IsDir {
		return "", fmt.Errorf("%w: parent is not a folder", ErrInvalid)
	}
	return parent.Path, nil
}

// CreateFolder creates a folder under parentID. The row is metadata-only: no
// bucket, no object key, and no object is written — a folder that exists only in
// the index is exactly as real as one with a zero-byte marker, and costs nothing.
func (s *Service) CreateFolder(ctx context.Context, userID, parentID, name string) (*Node, error) {
	if err := validName(name); err != nil {
		return nil, err
	}
	parentPath, err := s.resolveParent(ctx, userID, parentID)
	if err != nil {
		return nil, err
	}
	if _, err := s.childByName(ctx, userID, parentID, name); err == nil {
		return nil, fmt.Errorf("%w: name already exists", ErrInvalid)
	} else if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	id, err := newID()
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	n := &Node{
		ID:        id,
		OwnerID:   userID,
		ParentID:  parentID,
		Name:      name,
		IsDir:     true,
		Path:      joinPath(parentPath, name),
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.insertNode(ctx, n); err != nil {
		return nil, err
	}
	return n, nil
}

// ─── grants ───────────────────────────────────────────────────────────────────

// UploadGrant locates or creates the file node (parentID, name) and mints a
// presigned WRITE grant for its bytes. Re-uploading an existing name reuses the
// node AND its key, so the new bytes overwrite the old. A new node is created
// PENDING (size 0, object key set) and appears in listings immediately.
func (s *Service) UploadGrant(ctx context.Context, userID, parentID, name, contentType string, ttl time.Duration) (*Node, ObjectGrant, error) {
	if err := validName(name); err != nil {
		return nil, ObjectGrant{}, err
	}
	parentPath, err := s.resolveParent(ctx, userID, parentID)
	if err != nil {
		return nil, ObjectGrant{}, err
	}
	n, err := s.childByName(ctx, userID, parentID, name)
	switch {
	case errors.Is(err, ErrNotFound):
		id, idErr := newID()
		if idErr != nil {
			return nil, ObjectGrant{}, idErr
		}
		now := time.Now().UTC()
		path := joinPath(parentPath, name)
		n = &Node{
			ID:          id,
			OwnerID:     userID,
			ParentID:    parentID,
			Name:        name,
			Bucket:      s.bucketFor(ctx, userID),
			ObjectKey:   driveKey(userID, path),
			Path:        path,
			ContentType: contentType,
			CreatedAt:   now,
			UpdatedAt:   now,
		}
		if err := s.insertNode(ctx, n); err != nil {
			return nil, ObjectGrant{}, err
		}
	case err != nil:
		return nil, ObjectGrant{}, err
	case n.IsDir:
		return nil, ObjectGrant{}, fmt.Errorf("%w: target is a folder", ErrInvalid)
	}
	grant, err := s.mint(ctx, true, n, ttl)
	if err != nil {
		return nil, ObjectGrant{}, err
	}
	return n, grant, nil
}

// DownloadGrant mints a presigned READ grant for nodeID. Folders have no bytes.
func (s *Service) DownloadGrant(ctx context.Context, userID, nodeID string, ttl time.Duration) (*Node, ObjectGrant, error) {
	n, err := s.getNode(ctx, nodeID)
	if err != nil {
		return nil, ObjectGrant{}, err
	}
	if n.OwnerID != userID {
		return nil, ObjectGrant{}, ErrForbidden
	}
	if n.IsDir {
		return nil, ObjectGrant{}, fmt.Errorf("%w: folders have no bytes", ErrInvalid)
	}
	grant, err := s.mint(ctx, false, n, ttl)
	if err != nil {
		return nil, ObjectGrant{}, err
	}
	return n, grant, nil
}

// mint is the single chokepoint that turns an authorized request into a grant.
// The caller has already checked ownership; mint never re-derives access.
func (s *Service) mint(ctx context.Context, write bool, n *Node, ttl time.Duration) (ObjectGrant, error) {
	if s.broker == nil {
		return ObjectGrant{}, errors.New("files: storage broker unavailable")
	}
	if write {
		return s.broker.MintWrite(ctx, n.OwnerID, n.Bucket, n.ObjectKey, ttl)
	}
	return s.broker.MintRead(ctx, n.OwnerID, n.Bucket, n.ObjectKey, ttl)
}

// bucketFor resolves the owner's unified bucket, or "" when no resolver is wired.
func (s *Service) bucketFor(ctx context.Context, ownerID string) string {
	if s.bucket == nil {
		return ""
	}
	return s.bucket(ctx, ownerID)
}

// Commit records a version after the bytes have landed via a write grant, and
// updates the node's size / content type / current version. The version's key is
// the node's key: history is metadata-only and the previous bytes are gone.
func (s *Service) Commit(ctx context.Context, userID, nodeID string, size int64, contentType, etag string) (*Version, error) {
	n, err := s.getNode(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	if n.OwnerID != userID {
		return nil, ErrForbidden
	}
	if n.IsDir {
		return nil, fmt.Errorf("%w: folders have no bytes", ErrInvalid)
	}
	id, err := newID()
	if err != nil {
		return nil, err
	}
	v := Version{
		ID:          id,
		NodeID:      n.ID,
		VersionKey:  n.ObjectKey,
		Size:        size,
		ContentType: contentType,
		ETag:        etag,
		CreatedBy:   userID,
		CreatedAt:   time.Now().UTC(),
	}
	if err := s.insertVersion(ctx, v); err != nil {
		return nil, err
	}
	n.Size = size
	if contentType != "" {
		n.ContentType = contentType
	}
	n.CurrentVersionID = v.ID
	if err := s.touchNode(ctx, n); err != nil {
		return nil, err
	}
	return &v, nil
}

// Versions lists a node's history, newest first.
func (s *Service) Versions(ctx context.Context, userID, nodeID string) ([]Version, error) {
	n, err := s.getNode(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	if n.OwnerID != userID {
		return nil, ErrForbidden
	}
	return s.listVersions(ctx, nodeID)
}

// ─── move / rename / delete ───────────────────────────────────────────────────

// Move reparents nodeID to newParentID (empty = keep parent) and/or renames it
// to newName (empty = keep name).
//
// The key is derived from the path, so a move re-keys the node AND EVERY
// DESCENDANT FILE. To keep the index and the bucket consistent the bytes move
// FIRST and the index rows are then committed in ONE transaction: if the commit
// fails the bytes are rolled back, and if a byte move fails partway the
// completed ones are reverted — so either the whole move lands or nothing does.
//
// Rollback is best-effort and logged: a revert can itself fail against an object
// store that has no transactions. That is a real limitation, not a papered-over
// one — a failed revert leaves bytes at the new key with the index still at the
// old one, and is loud in the log.
func (s *Service) Move(ctx context.Context, userID, nodeID, newParentID, newName string) (*Node, error) {
	n, err := s.getNode(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	if n.OwnerID != userID {
		return nil, ErrForbidden
	}

	parentID := n.ParentID
	parentPath := ""
	if newParentID != "" && newParentID != n.ParentID {
		parent, err := s.getNode(ctx, newParentID)
		if err != nil {
			return nil, err
		}
		if parent.OwnerID != n.OwnerID {
			return nil, fmt.Errorf("%w: cross-owner move not supported", ErrInvalid)
		}
		if !parent.IsDir {
			return nil, fmt.Errorf("%w: destination is not a folder", ErrInvalid)
		}
		if s.isAncestor(ctx, n.ID, parent) {
			return nil, fmt.Errorf("%w: cannot move a folder into itself", ErrInvalid)
		}
		parentID = newParentID
		parentPath = parent.Path
	} else if n.ParentID != "" {
		p, err := s.getNode(ctx, n.ParentID)
		if err != nil {
			return nil, err
		}
		parentPath = p.Path
	}

	name := n.Name
	if newName != "" {
		if err := validName(newName); err != nil {
			return nil, err
		}
		name = newName
	}
	if existing, err := s.childByName(ctx, n.OwnerID, parentID, name); err == nil && existing.ID != n.ID {
		return nil, fmt.Errorf("%w: name already exists in destination", ErrInvalid)
	} else if err != nil && !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	n.ParentID = parentID
	n.Name = name

	var plan []movePlan
	if err := s.planMoves(ctx, n, parentPath, &plan); err != nil {
		return nil, err
	}

	done, err := s.relocateBytes(ctx, plan)
	if err != nil {
		s.revertBytes(ctx, done)
		return nil, err
	}
	if err := s.commitMovePlan(ctx, plan); err != nil {
		s.revertBytes(ctx, done)
		return nil, err
	}

	// Reflect the committed plan onto the in-memory nodes (n is plan[0]).
	for _, p := range plan {
		p.node.Path = p.newPath
		if !p.node.IsDir {
			p.node.ObjectKey = p.newKey
		}
	}
	return n, nil
}

// movePlan is the precomputed relocation for one node in a moved subtree.
type movePlan struct {
	node    *Node
	newPath string
	newKey  string // files only; empty for folders
}

// planMoves appends the relocation for n and, when n is a folder, its whole
// subtree (depth-first) to out. It reads the tree; it mutates nothing.
func (s *Service) planMoves(ctx context.Context, n *Node, parentPath string, out *[]movePlan) error {
	newPath := joinPath(parentPath, n.Name)
	mp := movePlan{node: n, newPath: newPath}
	if !n.IsDir {
		mp.newKey = driveKey(n.OwnerID, newPath)
	}
	*out = append(*out, mp)
	if !n.IsDir {
		return nil
	}
	kids, err := s.listChildren(ctx, n.ID)
	if err != nil {
		return err
	}
	for _, k := range kids {
		if err := s.planMoves(ctx, k, newPath, out); err != nil {
			return err
		}
	}
	return nil
}

// movedByte records a completed byte relocation so it can be reverted.
type movedByte struct {
	ownerID, bucket, from, to string
}

// relocateBytes moves the bytes for every file node whose key changes, returning
// the completed moves (newest last) so the caller can revert them. Folders and
// key-less nodes carry no bytes; a node whose object was never uploaded is
// handled by the Broker, for which a missing source is a no-op success.
func (s *Service) relocateBytes(ctx context.Context, plan []movePlan) ([]movedByte, error) {
	var done []movedByte
	for _, p := range plan {
		if p.node.IsDir || p.node.ObjectKey == "" || p.newKey == p.node.ObjectKey {
			continue
		}
		if s.broker == nil {
			return done, errors.New("files: storage broker unavailable")
		}
		if err := s.broker.MoveObject(ctx, p.node.OwnerID, p.node.Bucket, p.node.ObjectKey, p.newKey); err != nil {
			return done, err
		}
		done = append(done, movedByte{ownerID: p.node.OwnerID, bucket: p.node.Bucket, from: p.node.ObjectKey, to: p.newKey})
	}
	return done, nil
}

// revertBytes moves completed relocations back to their source keys, in reverse
// order, so the bucket matches the (unchanged) index again.
func (s *Service) revertBytes(ctx context.Context, done []movedByte) {
	if s.broker == nil {
		return
	}
	for i := len(done) - 1; i >= 0; i-- {
		d := done[i]
		if err := s.broker.MoveObject(ctx, d.ownerID, d.bucket, d.to, d.from); err != nil {
			log.Printf("[files] move rollback failed for %s -> %s: %v", d.to, d.from, err)
		}
	}
}

// commitMovePlan writes every planned node's new name/parent/path (and, for
// files, object key) in a single transaction, so the index moves all-or-nothing.
func (s *Service) commitMovePlan(ctx context.Context, plan []movePlan) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op once Commit succeeded
	now := time.Now().UTC().Format(rfc)
	q := s.db.Rebind(`UPDATE files_nodes SET name=?, parent_id=?, path=?, object_key=?, updated_at=? WHERE id=?`)
	for _, p := range plan {
		key := p.node.ObjectKey
		if !p.node.IsDir {
			key = p.newKey
		}
		if _, err := tx.ExecContext(ctx, q, p.node.Name, p.node.ParentID, p.newPath, key, now, p.node.ID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Delete soft-deletes nodeID. Descendants become unreachable through the parent
// (every listing filters on deleted=0 and walks down from a live parent). No
// bytes are freed here — PurgeTombstones does that, later.
func (s *Service) Delete(ctx context.Context, userID, nodeID string) error {
	n, err := s.getNode(ctx, nodeID)
	if err != nil {
		return err
	}
	if n.OwnerID != userID {
		return ErrForbidden
	}
	return s.softDelete(ctx, nodeID)
}

// PurgeTombstones reclaims the bytes and index rows of nodes soft-deleted more
// than retention ago. Soft delete alone never frees a byte, so without this
// sweep a deleted file occupies (and is billed for) bucket storage forever.
//
// It sweeps the whole SUBTREE under each tombstone, not just the tombstoned row.
// Delete only marks the node it was given, so a deleted folder's children keep
// deleted=0 and are merely unreachable through their (gone) parent — reclaiming
// the folder alone would strand their bytes in the bucket, still sampled, still
// billed, and now unreachable by anyone.
//
// Returns the number of nodes fully purged.
func (s *Service) PurgeTombstones(ctx context.Context, retention time.Duration) (int, error) {
	if retention <= 0 {
		retention = DefaultTombstoneRetention
	}
	candidates, err := s.staleTombstones(ctx, time.Now().Add(-retention))
	if err != nil {
		return 0, err
	}
	purged := 0
	// A tombstone nested under another tombstone is reclaimed by its ancestor's
	// walk; done keeps the second visit from counting it twice.
	done := map[string]bool{}
	for _, n := range candidates {
		if done[n.ID] {
			continue
		}
		got, _ := s.purgeSubtree(ctx, n, done)
		purged += got
	}
	return purged, nil
}

// purgeSubtree reclaims n and everything beneath it, DEEPEST-FIRST, reporting how
// many nodes were fully purged and whether the subtree came away clean.
//
// A node whose byte deletion fails is left in place for the next sweep rather
// than hard-deleted — dropping the row would make the bytes permanently
// unreachable, and therefore permanently billed. That is also why a folder is
// only ever hard-deleted once every descendant beneath it is gone: removing it
// while a child survived would orphan that child into exactly the state this
// sweep exists to prevent.
func (s *Service) purgeSubtree(ctx context.Context, n *Node, done map[string]bool) (int, bool) {
	purged, clean := 0, true
	if n.IsDir {
		kids, err := s.childrenAll(ctx, n.ID)
		if err != nil {
			log.Printf("[files] purge: list children of %s: %v", n.ID, err)
			return 0, false
		}
		for _, k := range kids {
			got, ok := s.purgeSubtree(ctx, k, done)
			purged += got
			if !ok {
				clean = false
			}
		}
	}
	if !clean {
		return purged, false
	}
	if done[n.ID] {
		return purged, true
	}
	if !s.purgeBytes(ctx, n) {
		return purged, false
	}
	if err := s.hardDeleteNode(ctx, n.ID); err != nil {
		log.Printf("[files] purge: hard-delete failed for %s: %v", n.ID, err)
		return purged, false
	}
	done[n.ID] = true
	return purged + 1, true
}

// purgeBytes removes n's object plus every version's object. Every version of a
// node shares one key, so this is normally a single delete — the loop exists
// because a moved node's older versions can still point at its pre-move key.
// Returns false (leave the row tombstoned) if any deletion failed.
func (s *Service) purgeBytes(ctx context.Context, n *Node) bool {
	if s.broker == nil || n.IsDir {
		return true
	}
	ok := true
	vs, err := s.listVersions(ctx, n.ID)
	if err != nil {
		log.Printf("[files] purge: list versions for %s: %v", n.ID, err)
		return false
	}
	seen := map[string]bool{}
	for _, v := range vs {
		if v.VersionKey == "" || seen[v.VersionKey] {
			continue
		}
		seen[v.VersionKey] = true
		if err := s.broker.DeleteObject(ctx, n.OwnerID, n.Bucket, v.VersionKey); err != nil {
			log.Printf("[files] purge: delete version object %s: %v", v.VersionKey, err)
			ok = false
		}
	}
	if n.ObjectKey != "" && !seen[n.ObjectKey] {
		if err := s.broker.DeleteObject(ctx, n.OwnerID, n.Bucket, n.ObjectKey); err != nil {
			log.Printf("[files] purge: delete object %s: %v", n.ObjectKey, err)
			ok = false
		}
	}
	return ok
}

// ─── helpers ──────────────────────────────────────────────────────────────────

// validName rejects empty, over-long, traversal and separator-bearing names.
// It is applied on every create and rename, so no path segment in the index can
// ever contain a separator — which is what makes driveKey's composition safe.
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

// driveKey returns the canonical object key for a Drive path:
// "<ownerID>/drive/<path>". This is the single source of the bucket layout — a
// key is never accepted from a client, only derived here from the DB-held path.
func driveKey(ownerID, path string) string {
	return ownerID + "/" + DrivePrefix + path
}

// isAncestor reports whether candidate is nodeID itself or lives inside it —
// the check that forbids moving a folder into its own subtree.
func (s *Service) isAncestor(ctx context.Context, nodeID string, candidate *Node) bool {
	cur := candidate
	for i := 0; i < maxAncestorDepth && cur != nil; i++ {
		if cur.ID == nodeID {
			return true
		}
		if cur.ParentID == "" {
			return false
		}
		p, err := s.getNode(ctx, cur.ParentID)
		if err != nil {
			return false
		}
		cur = p
	}
	return false
}
