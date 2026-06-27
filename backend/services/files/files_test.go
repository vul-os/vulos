package files

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"vulos/backend/internal/storage"
)

// fakeBroker records mint calls so tests can assert that an UNAUTHORIZED request
// never reaches the minter, and lets tests observe grant TTL/expiry.
type fakeBroker struct {
	reads, writes int
	lastTTL       time.Duration
}

func (f *fakeBroker) MintRead(_ context.Context, ownerID, bucket, key string, ttl time.Duration) (storage.ObjectGrant, error) {
	f.reads++
	f.lastTTL = ttl
	return storage.ObjectGrant{Type: storage.GrantLocal, Method: "GET", Bucket: bucket, Key: key, ExpiresAt: time.Now().Add(ttl)}, nil
}

func (f *fakeBroker) MintWrite(_ context.Context, ownerID, bucket, key string, ttl time.Duration) (storage.ObjectGrant, error) {
	f.writes++
	f.lastTTL = ttl
	return storage.ObjectGrant{Type: storage.GrantLocal, Method: "PUT", Bucket: bucket, Key: key, ExpiresAt: time.Now().Add(ttl)}, nil
}

func newTestService(t *testing.T) (*Service, *fakeBroker) {
	t.Helper()
	fb := &fakeBroker{}
	dbPath := filepath.Join(t.TempDir(), "files.db")
	svc, err := New(dbPath, fb, func(uid string) string { return "vulos-" + uid })
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { svc.Close() })
	return svc, fb
}

const (
	owner = "userOwner"
	other = "userOther"
	ttl   = 5 * time.Minute
)

// uploadFile creates a file node owned by owner under parent and returns it.
func uploadFile(t *testing.T, svc *Service, ownerID, parent, name string) *Node {
	t.Helper()
	n, _, err := svc.UploadGrant(context.Background(), ownerID, parent, name, "text/plain", ttl)
	if err != nil {
		t.Fatalf("UploadGrant(%s): %v", name, err)
	}
	return n
}

func TestDriveKeyLayout(t *testing.T) {
	svc, _ := newTestService(t)
	n := uploadFile(t, svc, owner, "", "report.txt")
	wantKey := owner + "/drive/report.txt"
	if n.ObjectKey != wantKey {
		t.Fatalf("object key = %q, want %q", n.ObjectKey, wantKey)
	}
	if n.Bucket != "vulos-"+owner {
		t.Fatalf("bucket = %q, want vulos-%s", n.Bucket, owner)
	}
	// Nested path.
	dir, err := svc.CreateFolder(owner, "", "docs")
	if err != nil {
		t.Fatalf("CreateFolder: %v", err)
	}
	child := uploadFile(t, svc, owner, dir.ID, "memo.txt")
	if want := owner + "/drive/docs/memo.txt"; child.ObjectKey != want {
		t.Fatalf("nested key = %q, want %q", child.ObjectKey, want)
	}
}

// TestUnauthorizedGetsNoGrant: a user with no ACL gets NO grant and the broker
// is never invoked (fail-closed: gating happens before minting).
func TestUnauthorizedGetsNoGrant(t *testing.T) {
	svc, fb := newTestService(t)
	n := uploadFile(t, svc, owner, "", "secret.txt")
	fb.reads, fb.writes = 0, 0 // ignore the owner's own upload

	if _, _, err := svc.DownloadGrant(context.Background(), other, n.ID, ttl); err != ErrForbidden {
		t.Fatalf("DownloadGrant by stranger: err=%v, want ErrForbidden", err)
	}
	if fb.reads != 0 {
		t.Fatalf("broker.MintRead called %d times for unauthorized user — must be 0", fb.reads)
	}

	// Stranger also cannot obtain a write grant (upload into someone else's root
	// is creating in their OWN root, so test a shared subtree instead below).
	if _, _, err := svc.DownloadGrant(context.Background(), "thirdUser", n.ID, ttl); err != ErrForbidden {
		t.Fatalf("DownloadGrant by third user: err=%v, want ErrForbidden", err)
	}
}

// TestACLGatingReadWrite: viewer can read but not write; editor can do both;
// the broker is called only for authorized accesses.
func TestACLGatingReadWrite(t *testing.T) {
	svc, fb := newTestService(t)
	n := uploadFile(t, svc, owner, "", "doc.txt")

	// Share viewer with `other`.
	if _, err := svc.Share(owner, n.ID, other, RoleViewer); err != nil {
		t.Fatalf("Share viewer: %v", err)
	}
	fb.reads, fb.writes = 0, 0

	// Viewer: read OK.
	if _, _, err := svc.DownloadGrant(context.Background(), other, n.ID, ttl); err != nil {
		t.Fatalf("viewer DownloadGrant: %v", err)
	}
	if fb.reads != 1 {
		t.Fatalf("MintRead calls=%d, want 1", fb.reads)
	}

	// Viewer: a write (editor-gated) on the shared node must be denied. Commit is
	// the editor-gated mutation; UploadGrant at parent "" would create in the
	// caller's OWN root (unrelated), so we assert on Commit here.
	if _, err := svc.Commit(other, n.ID, 10, "text/plain", "etag"); err != ErrForbidden {
		t.Fatalf("viewer Commit: err=%v, want ErrForbidden", err)
	}

	// Upgrade `other` to editor → commit + write grant succeed.
	if _, err := svc.Share(owner, n.ID, other, RoleEditor); err != nil {
		t.Fatalf("Share editor: %v", err)
	}
	if _, err := svc.Commit(other, n.ID, 10, "text/plain", "etag"); err != nil {
		t.Fatalf("editor Commit: %v", err)
	}
}

// TestACLInheritance: sharing a FOLDER grants access to its children.
func TestACLInheritance(t *testing.T) {
	svc, _ := newTestService(t)
	dir, err := svc.CreateFolder(owner, "", "shared")
	if err != nil {
		t.Fatalf("CreateFolder: %v", err)
	}
	child := uploadFile(t, svc, owner, dir.ID, "inside.txt")

	// No access yet.
	if _, _, err := svc.DownloadGrant(context.Background(), other, child.ID, ttl); err != ErrForbidden {
		t.Fatalf("pre-share child read: err=%v, want ErrForbidden", err)
	}
	// Share the folder editor → child inherits editor.
	if _, err := svc.Share(owner, dir.ID, other, RoleEditor); err != nil {
		t.Fatalf("Share folder: %v", err)
	}
	if role, _ := svc.EffectiveRole(other, child.ID); role != RoleEditor {
		t.Fatalf("inherited role on child = %q, want editor", role)
	}
	if _, _, err := svc.DownloadGrant(context.Background(), other, child.ID, ttl); err != nil {
		t.Fatalf("post-share child read: %v", err)
	}
	// And `other` can create within the shared folder (editor on parent).
	created := uploadFile(t, svc, other, dir.ID, "by-collaborator.txt")
	if created.OwnerID != owner {
		t.Fatalf("collaborator-created node owner = %q, want bucket owner %q", created.OwnerID, owner)
	}
}

// TestGrantExpiry: minted grants carry an ExpiresAt within the requested TTL,
// and the requested TTL is propagated to the broker.
func TestGrantExpiry(t *testing.T) {
	svc, fb := newTestService(t)
	n := uploadFile(t, svc, owner, "", "exp.txt")

	before := time.Now()
	_, grant, err := svc.DownloadGrant(context.Background(), owner, n.ID, ttl)
	if err != nil {
		t.Fatalf("DownloadGrant: %v", err)
	}
	if fb.lastTTL != ttl {
		t.Fatalf("broker received ttl=%v, want %v", fb.lastTTL, ttl)
	}
	if !grant.ExpiresAt.After(before) {
		t.Fatalf("grant ExpiresAt %v not after request time %v", grant.ExpiresAt, before)
	}
	if grant.ExpiresAt.After(before.Add(ttl + time.Second)) {
		t.Fatalf("grant ExpiresAt %v exceeds request+ttl", grant.ExpiresAt)
	}
}

// TestRealBrokerLocalGrantExpiry exercises the REAL storage.GrantBroker in
// standalone mode (no object store): it returns a local-FS grant whose ExpiresAt
// honors the TTL and whose LocalPath mirrors the Drive key.
func TestRealBrokerLocalGrantExpiry(t *testing.T) {
	resolver := storage.NewResolver(storage.ResolverConfig{
		BucketPrefix: "vulos-",
		LocalRoot:    t.TempDir(),
	})
	broker := storage.NewGrantBroker(resolver, storage.STSConfig{}, 2*time.Minute)

	key := owner + "/drive/a.txt"
	before := time.Now()
	g, err := broker.MintRead(context.Background(), owner, "vulos-"+owner, key, 0)
	if err != nil {
		t.Fatalf("MintRead: %v", err)
	}
	if g.Type != storage.GrantLocal {
		t.Fatalf("grant type = %q, want local (standalone)", g.Type)
	}
	if g.LocalPath == "" {
		t.Fatalf("local grant has empty LocalPath")
	}
	if !g.ExpiresAt.After(before) || g.ExpiresAt.After(before.Add(2*time.Minute+time.Second)) {
		t.Fatalf("local grant expiry %v outside (now, now+2m]", g.ExpiresAt)
	}
}

// TestShareLinkExpiryAndRevoke: a redeemed link works while active, fails once
// expired, and fails once revoked.
func TestShareLinkExpiryAndRevoke(t *testing.T) {
	svc, _ := newTestService(t)
	n := uploadFile(t, svc, owner, "", "linked.txt")

	// Active link → redeem succeeds for an arbitrary authenticated user.
	link, err := svc.CreateLink(owner, n.ID, RoleViewer, time.Minute)
	if err != nil {
		t.Fatalf("CreateLink: %v", err)
	}
	if _, _, err := svc.RedeemLink(context.Background(), other, link.Token, ttl); err != nil {
		t.Fatalf("redeem active link: %v", err)
	}

	// Expired link → redeem fails.
	expired, err := svc.CreateLink(owner, n.ID, RoleViewer, time.Millisecond)
	if err != nil {
		t.Fatalf("CreateLink expired: %v", err)
	}
	time.Sleep(5 * time.Millisecond)
	if _, _, err := svc.RedeemLink(context.Background(), other, expired.Token, ttl); err != ErrLinkInactive {
		t.Fatalf("redeem expired link: err=%v, want ErrLinkInactive", err)
	}

	// Revoked link → redeem fails.
	if err := svc.RevokeLink(owner, link.Token); err != nil {
		t.Fatalf("RevokeLink: %v", err)
	}
	if _, _, err := svc.RedeemLink(context.Background(), other, link.Token, ttl); err != ErrLinkInactive {
		t.Fatalf("redeem revoked link: err=%v, want ErrLinkInactive", err)
	}

	// Only the owner may create/revoke links.
	if _, err := svc.CreateLink(other, n.ID, RoleViewer, time.Minute); err != ErrForbidden {
		t.Fatalf("non-owner CreateLink: err=%v, want ErrForbidden", err)
	}
}

// TestShareManagementOwnerOnly: only the owner may grant/revoke ACLs.
func TestShareManagementOwnerOnly(t *testing.T) {
	svc, _ := newTestService(t)
	n := uploadFile(t, svc, owner, "", "acl.txt")

	// Editor cannot manage shares.
	if _, err := svc.Share(owner, n.ID, other, RoleEditor); err != nil {
		t.Fatalf("Share editor: %v", err)
	}
	if _, err := svc.Share(other, n.ID, "thirdUser", RoleViewer); err != ErrForbidden {
		t.Fatalf("editor Share: err=%v, want ErrForbidden", err)
	}
	// Owner-only invariant: cannot share with owner; role must be valid.
	if _, err := svc.Share(owner, n.ID, owner, RoleViewer); err == nil {
		t.Fatalf("sharing with owner should fail")
	}
	if _, err := svc.Share(owner, n.ID, "x", RoleOwner); err == nil {
		t.Fatalf("granting owner role should fail")
	}

	// SharedWithMe reflects the grant.
	nodes, err := svc.SharedWithMe(other)
	if err != nil {
		t.Fatalf("SharedWithMe: %v", err)
	}
	if len(nodes) != 1 || nodes[0].ID != n.ID {
		t.Fatalf("SharedWithMe = %+v, want [%s]", nodes, n.ID)
	}
}

// TestMoveRenameUpdatesKeys: rename/move updates path and the canonical key.
func TestMoveRenameUpdatesKeys(t *testing.T) {
	svc, _ := newTestService(t)
	dir, _ := svc.CreateFolder(owner, "", "a")
	f := uploadFile(t, svc, owner, dir.ID, "f.txt")
	if want := owner + "/drive/a/f.txt"; f.ObjectKey != want {
		t.Fatalf("key=%q want %q", f.ObjectKey, want)
	}
	// Rename the folder; child key should follow.
	if _, err := svc.Move(owner, dir.ID, "", "b"); err != nil {
		t.Fatalf("Move/rename folder: %v", err)
	}
	moved, err := svc.getNode(f.ID)
	if err != nil {
		t.Fatalf("getNode: %v", err)
	}
	if want := owner + "/drive/b/f.txt"; moved.ObjectKey != want {
		t.Fatalf("post-rename child key=%q want %q", moved.ObjectKey, want)
	}
	if moved.Path != "b/f.txt" {
		t.Fatalf("post-rename child path=%q want b/f.txt", moved.Path)
	}
}

// TestVersionsListed: commit appends versions; list returns them newest-first.
func TestVersionsListed(t *testing.T) {
	svc, _ := newTestService(t)
	n := uploadFile(t, svc, owner, "", "v.txt")
	if _, err := svc.Commit(owner, n.ID, 1, "text/plain", "e1"); err != nil {
		t.Fatalf("commit1: %v", err)
	}
	if _, err := svc.Commit(owner, n.ID, 2, "text/plain", "e2"); err != nil {
		t.Fatalf("commit2: %v", err)
	}
	vs, err := svc.Versions(owner, n.ID)
	if err != nil {
		t.Fatalf("Versions: %v", err)
	}
	if len(vs) != 2 {
		t.Fatalf("versions=%d want 2", len(vs))
	}
	if vs[0].Size != 2 {
		t.Fatalf("newest version size=%d want 2", vs[0].Size)
	}
	// A stranger cannot list versions.
	if _, err := svc.Versions(other, n.ID); err != ErrForbidden {
		t.Fatalf("stranger Versions: err=%v, want ErrForbidden", err)
	}
}
