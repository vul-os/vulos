package files

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log"
	"strings"
	"time"

	"vulos/backend/internal/deploymode"
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

	// PHASE-2B OS peer-share seam (wired via WithPeer; nil ⇒ peer-share returns
	// ErrPeerUnavailable). signer is THIS box's capability identity; transport
	// streams bytes box-to-box; stagingDir holds redeemed bytes on local disk
	// until promoted into the recipient's Drive.
	signer     PeerSigner
	transport  PeerTransport
	stagingDir string

	// PHASE-4 external-store seam (wired via WithExternal; nil/empty ⇒ external
	// mounts return ErrExternalUnavailable). extTokens mints short-lived provider
	// access tokens from the CP integration broker; extProviders maps a mount kind
	// (e.g. "gdrive") to its SSRF-safe API implementation.
	extTokens    TokenSource
	extProviders map[string]ExternalProvider

	// IMPORT engine seam (wired via WithImport; empty ⇒ ImportEnabled() is false
	// and the Import action is "not available"). importSources maps an import
	// source kind (e.g. "gdrive") to its SSRF-safe read implementation. Minting
	// reuses extTokens (the CP integration broker), so an import requires a token
	// source too.
	importSources map[string]ImportSource

	// PIM import seam (wired via WithPIMConfig; nil ⇒ contacts/calendar import
	// jobs fail with ErrImportUnavailable). pimMailURL is the lilmail instance
	// base URL (e.g. "http://localhost:3000"); pimMailSecret is the
	// LILMAIL_BROKER_SECRET shared secret. pimAccountResolver maps an OS ownerID
	// to the mail account email address used as the CardDAV/CalDAV account key.
	pimMailURL         string
	pimMailSecret      string
	pimAccountResolver func(ownerID string) string

	// ACCOUNT-SHARE seam (wired via WithShareResolver; nil ⇒ ShareByEmail returns
	// ErrShareResolveUnavailable). shareResolver maps a recipient EMAIL to a
	// ShareRecipient and decides locality (Contract 2 + 3): a co-cloud principal
	// uses the ACL grant path, a remote VulaID uses the peershare capability path.
	// capDeliverer (optional) delivers a minted capability to the remote
	// recipient's server intake; nil ⇒ the capability is returned but not
	// auto-delivered.
	shareResolver ShareResolver
	capDeliverer  CapabilityDeliverer
}

// WithPIMConfig wires the PIM import seam: the URL of the lilmail instance
// the OS importer will POST bulk contacts/events to, the matching broker secret
// (LILMAIL_BROKER_SECRET), and a resolver that maps an OS ownerID to the mail
// account email address (the CardDAV/CalDAV account key). Call this after
// WithImport when wiring contacts/calendar import sources. Returns s for chaining.
func (s *Service) WithPIMConfig(mailURL, mailSecret string, accountResolver func(ownerID string) string) *Service {
	s.pimMailURL = mailURL
	s.pimMailSecret = mailSecret
	s.pimAccountResolver = accountResolver
	return s
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
	// SealDefault (WRITE grants only, WAVE-3 follow-up): a CP-provisioned cloud
	// deployment (DEPLOY_MODE=cloud) presign-brokers this PUT, so the control
	// plane is *technically capable* of reading the plaintext bytes it hands the
	// URL for — honest, not content-blind by default. Advertising SealDefault=true
	// tells the client it SHOULD seal
	// the bytes first with the existing content-seal path (contentseal.go /
	// contentSeal.js), wrapped to the uploader's OWN published content key, so
	// what the CP brokers is ciphertext it cannot open. Gated strictly on the
	// grant actually being a presigned URL to a real object store — a local-FS
	// fallback or an object-scoped STS credential (self-host, no CP in the
	// loop) gets no such advisory. This flag is ADVISORY ONLY: the broker
	// cannot force a client to seal, so ignoring it still uploads plaintext.
	if grant.Type == storage.GrantPresigned && s.sealDefaultMode() {
		grant.SealDefault = true
	}
	s.audit(userID, "grant.write", n.ID, string(grant.Type))
	return n, grant, nil
}

// sealDefaultMode reports whether this deployment is the CP-provisioned,
// multi-tenant cloud (DEPLOY_MODE=cloud) — the case DEPLOY-SECURITY.md
// documents as "the control plane holds the presign-brokering credential and
// can read object bytes". It deliberately does NOT include DEPLOY_MODE=os
// (a self-hosted/operator-provisioned box that is merely CP-adjacent for
// optional features): that operator already holds their own bucket keys, so
// there is no CP standing between them and their bytes to seal against. An
// unset/invalid DEPLOY_MODE resolves to Standalone (false) — the same
// fail-safe default deploymode.FromEnv() uses everywhere else; this reads the
// env directly (not deploymode.Load()) so a per-request call never spams the
// boot log.
func (s *Service) sealDefaultMode() bool {
	mode, _ := deploymode.FromEnv()
	return mode == deploymode.Cloud
}

// SealDefault exports sealDefaultMode for the HTTP layer (GET
// /api/files/seal-policy) so the UI can honestly surface this deployment's
// default posture WITHOUT requiring an upload-grant round trip first.
func (s *Service) SealDefault() bool { return s.sealDefaultMode() }

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

// PutContent streams r (size bytes) into nodeID's object via the OS-mediated data
// plane and returns the stored ETag. Requires editor+. This is how the Files app
// uploads bytes when a direct presigned PUT is unavailable (standalone local-FS /
// STS). The caller still calls Commit afterwards to record the version. The ACL
// is checked here BEFORE any bytes are written.
func (s *Service) PutContent(ctx context.Context, userID, nodeID string, r io.Reader, size int64, contentType string) (string, error) {
	n, err := s.getNode(nodeID)
	if err != nil {
		return "", err
	}
	if n.IsDir {
		return "", fmt.Errorf("%w: folders have no bytes", ErrInvalid)
	}
	if !s.authorize(userID, n, RoleEditor) {
		return "", ErrForbidden
	}
	if s.broker == nil {
		return "", fmt.Errorf("files: storage broker unavailable")
	}
	if contentType == "" {
		contentType = n.ContentType
	}
	etag, err := s.broker.PutContent(ctx, n.OwnerID, n.Bucket, n.ObjectKey, r, size, contentType)
	if err != nil {
		return "", err
	}
	s.audit(userID, "content.put", n.ID, fmt.Sprintf("size=%d", size))
	return etag, nil
}

// StoreContent is the one-call data-plane entry the resumable-upload manager
// uses to promote a fully-assembled file into a user's Drive: it locates or
// creates the target node (parentID,name), streams size bytes from r into the
// owner's bucket via the OS-mediated data plane, and records a version — the
// same UploadGrant→PutContent→Commit sequence a single-shot upload runs, minus
// the client round-trips. It is ACL-gated exactly like UploadGrant: editor+ on
// the parent (or ownership of the Drive root), enforced BEFORE any bytes move,
// so a resumable upload can never write into a Drive the caller cannot write.
// Returns the created/updated node id.
func (s *Service) StoreContent(ctx context.Context, userID, parentID, name, contentType string, r io.Reader, size int64) (string, error) {
	if err := validName(name); err != nil {
		return "", err
	}
	ownerID, parentPath, err := s.resolveParent(userID, parentID)
	if err != nil {
		return "", err
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
			return "", err
		}
	} else if err != nil {
		return "", err
	} else if n.IsDir {
		return "", fmt.Errorf("%w: target is a folder", ErrInvalid)
	}
	// PutContent re-checks editor+ on the node itself and streams via the broker.
	if _, err := s.PutContent(ctx, userID, n.ID, r, size, contentType); err != nil {
		return "", err
	}
	if _, err := s.Commit(userID, n.ID, size, contentType, ""); err != nil {
		return "", err
	}
	return n.ID, nil
}

// GetContent opens nodeID's bytes for reading via the OS-mediated data plane.
// Requires viewer+. The caller MUST Close the returned reader. Used by the Files
// app to download when a direct presigned GET is unavailable (standalone / STS).
func (s *Service) GetContent(ctx context.Context, userID, nodeID string) (io.ReadCloser, *Node, int64, error) {
	n, err := s.getNode(nodeID)
	if err != nil {
		return nil, nil, 0, err
	}
	if n.IsDir {
		return nil, nil, 0, fmt.Errorf("%w: folders have no bytes", ErrInvalid)
	}
	if !s.authorize(userID, n, RoleViewer) {
		return nil, nil, 0, ErrForbidden
	}
	if s.broker == nil {
		return nil, nil, 0, fmt.Errorf("files: storage broker unavailable")
	}
	rc, size, err := s.broker.GetContent(ctx, n.OwnerID, n.Bucket, n.ObjectKey)
	if err != nil {
		return nil, nil, 0, err
	}
	s.audit(userID, "content.get", n.ID, "")
	return rc, n, size, nil
}

// Search returns up to limit non-deleted files/folders VISIBLE to userID whose
// name matches query (a case-insensitive substring). It is a pure READ used by
// the sovereign assistant's read-only find_file tool. It is ACL-SAFE BY
// CONSTRUCTION: it only ever returns (a) nodes the user OWNS — resolved by an
// owner_id-scoped SQL query, and (b) nodes explicitly SHARED with the user —
// resolved from that user's ACL grants via nodesSharedWith. There is no path in
// which a node the user cannot read is returned, and no filesystem path is ever
// walked, so a crafted name/path cannot traverse outside the user's own tree.
// An empty query lists the user's most-recent files. Never mutates.
func (s *Service) Search(userID, query string, limit int) ([]*Node, error) {
	if strings.TrimSpace(userID) == "" {
		return nil, ErrForbidden
	}
	if limit <= 0 || limit > 100 {
		limit = 25
	}
	like := "%" + escapeLike(strings.TrimSpace(query)) + "%"

	// (a) The caller's OWN files — owner_id-scoped SQL; ownership is viewer+.
	owned, err := s.searchOwnedByName(userID, like, limit)
	if err != nil {
		return nil, err
	}
	out := owned
	seen := make(map[string]bool, len(owned))
	for _, n := range owned {
		seen[n.ID] = true
	}

	// (b) Files explicitly SHARED with the caller (ACL grants) whose name also
	// matches. nodesSharedWith is the SAME grant lookup SharedWithMe uses, so this
	// cannot surface anything the user is not authorized for.
	if len(out) < limit {
		q := strings.ToLower(strings.TrimSpace(query))
		shared, serr := s.SharedWithMe(userID)
		if serr == nil {
			for _, n := range shared {
				if seen[n.ID] || n.OwnerID == userID {
					continue
				}
				if q == "" || strings.Contains(strings.ToLower(n.Name), q) {
					out = append(out, n)
					seen[n.ID] = true
					if len(out) >= limit {
						break
					}
				}
			}
		}
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// escapeLike escapes the LIKE metacharacters (%, _, and the \ escape itself) in
// user-supplied search text so a query containing them is matched literally and
// cannot widen the pattern. Paired with `ESCAPE '\'` in the query.
func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
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
// parent, and forbids moving across bucket owners (a Phase-2+ concern).
//
// PHASE-2A byte-mover: a move both recomputes the index (path + object_key for
// the node and, for a folder, its whole subtree) AND physically relocates the
// underlying bytes. To keep the index and the object store consistent the bytes
// are moved FIRST, then the index keys are committed in a single transaction;
// if the index commit fails the already-moved bytes are rolled back, and if a
// byte move fails partway the completed ones are reverted before returning — so
// either the whole move lands or nothing changes.
func (s *Service) Move(ctx context.Context, userID, nodeID, newParentID, newName string) (*Node, error) {
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

	// Plan the new path/key for n and, when it is a folder, its whole subtree.
	var plan []movePlan
	if err := s.planMoves(n, parentPath, &plan); err != nil {
		return nil, err
	}

	// 1) Relocate the bytes, tracking completed moves so a later failure can undo
	//    them. Folders and pending (key-less / unchanged) nodes carry no bytes.
	done, err := s.relocateBytes(ctx, plan)
	if err != nil {
		s.revertBytes(ctx, done)
		return nil, err
	}

	// 2) Commit the matching index keys atomically. On failure roll the bytes
	//    back so the store and index never diverge.
	if err := s.commitMovePlan(plan); err != nil {
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
	s.audit(userID, "node.move", n.ID, n.Path)
	return n, nil
}

// movePlan is the precomputed relocation for one node in a moved subtree: its new
// Drive-relative path and, for a file, the new canonical object key.
type movePlan struct {
	node    *Node
	newPath string
	newKey  string // file nodes only; empty for folders
}

// planMoves appends the relocation for n and, when n is a folder, its descendants
// (depth-first) to out. It reads the current tree; it does NOT mutate anything.
func (s *Service) planMoves(n *Node, parentPath string, out *[]movePlan) error {
	newPath := joinPath(parentPath, n.Name)
	mp := movePlan{node: n, newPath: newPath}
	if !n.IsDir {
		mp.newKey = driveKey(n.OwnerID, newPath)
	}
	*out = append(*out, mp)
	if !n.IsDir {
		return nil
	}
	kids, err := s.listChildren(n.ID)
	if err != nil {
		return err
	}
	for _, k := range kids {
		if err := s.planMoves(k, newPath, out); err != nil {
			return err
		}
	}
	return nil
}

// movedByte records a completed byte relocation so it can be reverted on failure.
type movedByte struct {
	ownerID, bucket, from, to string
}

// relocateBytes physically moves the bytes for each file node whose key changes,
// returning the moves that succeeded (newest last) so the caller can revert them.
func (s *Service) relocateBytes(ctx context.Context, plan []movePlan) ([]movedByte, error) {
	var done []movedByte
	for _, p := range plan {
		if p.node.IsDir || p.node.ObjectKey == "" || p.newKey == p.node.ObjectKey {
			continue
		}
		if s.broker == nil {
			return done, fmt.Errorf("files: storage broker unavailable")
		}
		if err := s.broker.MoveObject(ctx, p.node.OwnerID, p.node.Bucket, p.node.ObjectKey, p.newKey); err != nil {
			return done, err
		}
		done = append(done, movedByte{ownerID: p.node.OwnerID, bucket: p.node.Bucket, from: p.node.ObjectKey, to: p.newKey})
	}
	return done, nil
}

// revertBytes best-effort moves completed relocations back to their source keys,
// in reverse order. Used to unwind a partial move so bytes match the (unchanged)
// index.
func (s *Service) revertBytes(ctx context.Context, done []movedByte) {
	if s.broker == nil || len(done) == 0 {
		return
	}
	for i := len(done) - 1; i >= 0; i-- {
		d := done[i]
		if err := s.broker.MoveObject(ctx, d.ownerID, d.bucket, d.to, d.from); err != nil {
			log.Printf("[files] move rollback failed for %s->%s: %v", d.to, d.from, err)
		}
	}
}

// commitMovePlan writes every node's new path (and object_key for files) in a
// single transaction so the index moves all-or-nothing.
func (s *Service) commitMovePlan(plan []movePlan) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().Format(rfc)
	for _, p := range plan {
		key := p.node.ObjectKey
		if !p.node.IsDir {
			key = p.newKey
		}
		if _, err := tx.Exec(`UPDATE files_nodes SET name=?, parent_id=?, path=?, object_key=?, updated_at=? WHERE id=?`,
			p.node.Name, p.node.ParentID, p.newPath, key, now, p.node.ID); err != nil {
			return err
		}
	}
	return tx.Commit()
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

// DefaultTombstoneRetention is how long a soft-deleted node's bucket bytes are
// kept before PurgeTombstones reclaims them, absent an operator override.
const DefaultTombstoneRetention = 30 * 24 * time.Hour

// PurgeTombstones is the minimal trash/purge mechanism for soft delete: Delete
// above only flips deleted=1 — it never frees bucket bytes and offers no
// undelete/trash UI. Until a full trash feature exists, this sweep is what
// keeps deleted files from occupying bucket storage forever. For every node
// tombstoned more than `retention` ago it removes the object's bytes (and
// every prior version's bytes) from the bucket, then hard-deletes the index
// row (+ its versions/ACLs/share-links). A node whose byte deletion fails
// (network, permissions) is left tombstoned for the next sweep to retry rather
// than hard-deleting a row that might still point at live bytes. Returns the
// number of nodes fully purged. Safe to call with a nil broker (bytes are
// skipped; index rows still get reclaimed for dir nodes / already-byteless
// pending nodes) or concurrently with normal Service use — it never touches an
// undeleted row.
func (s *Service) PurgeTombstones(ctx context.Context, retention time.Duration) (int, error) {
	if retention <= 0 {
		retention = DefaultTombstoneRetention
	}
	cutoff := time.Now().Add(-retention)
	candidates, err := s.staleTombstones(cutoff)
	if err != nil {
		return 0, err
	}
	purged := 0
	for _, n := range candidates {
		if !s.purgeBytes(ctx, n) {
			continue // leave tombstoned; retry on the next sweep
		}
		if err := s.hardDeleteNode(n.ID); err != nil {
			log.Printf("[files] purge: hard-delete failed for %s: %v", n.ID, err)
			continue
		}
		s.audit(n.OwnerID, "node.purge", n.ID, n.Path)
		purged++
	}
	return purged, nil
}

// purgeBytes removes n's current object plus every prior version's object from
// the bucket. Returns false (leave the row tombstoned) if any deletion failed
// for a reason other than "already gone" — the caller must not hard-delete the
// index row in that case, or the bytes could become permanently unreachable.
func (s *Service) purgeBytes(ctx context.Context, n *Node) bool {
	if s.broker == nil {
		// No bucket wired (e.g. tests, or a degraded box) — nothing to purge;
		// the index row can still be reclaimed.
		return true
	}
	if n.IsDir {
		return true // folders carry no bytes of their own
	}
	ok := true
	if vs, err := s.listVersions(n.ID); err == nil {
		seen := map[string]bool{}
		for _, v := range vs {
			if v.VersionKey == "" || seen[v.VersionKey] {
				continue
			}
			seen[v.VersionKey] = true
			if err := s.broker.DeleteObject(ctx, n.OwnerID, n.Bucket, v.VersionKey); err != nil {
				log.Printf("[files] purge: delete version object %s failed: %v", v.VersionKey, err)
				ok = false
			}
		}
	} else {
		log.Printf("[files] purge: list versions for %s failed: %v", n.ID, err)
		ok = false
	}
	if n.ObjectKey != "" {
		if err := s.broker.DeleteObject(ctx, n.OwnerID, n.Bucket, n.ObjectKey); err != nil {
			log.Printf("[files] purge: delete object %s failed: %v", n.ObjectKey, err)
			ok = false
		}
	}
	return ok
}

// PurgeTombstoneLoop runs PurgeTombstones every interval until ctx is
// cancelled. Intended to be started in a goroutine by the server wiring
// (mirrors upload.Manager.SweepLoop).
func (s *Service) PurgeTombstoneLoop(ctx context.Context, retention, interval time.Duration) {
	if interval <= 0 {
		interval = time.Hour
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if n, err := s.PurgeTombstones(ctx, retention); err != nil {
				log.Printf("[files] tombstone purge sweep error: %v", err)
			} else if n > 0 {
				log.Printf("[files] tombstone purge: reclaimed %d node(s)", n)
			}
		}
	}
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

// RedeemLink turns an active share-link token into a grant for the linked
// file, for any authenticated redeemer (the token is the capability). The
// grant's access level follows the link's own role: an editor link mints a
// write grant, a viewer link mints a read grant.
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
	access := AccessRead
	if l.Role == RoleEditor {
		access = AccessWrite
	}
	grant, err := s.mint(ctx, access, n, ttl)
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
