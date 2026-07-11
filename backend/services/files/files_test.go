package files

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"vulos/backend/internal/storage"
)

// fakeBroker records mint calls so tests can assert that an UNAUTHORIZED request
// never reaches the minter, and lets tests observe grant TTL/expiry. It also
// models a tiny in-memory object store (objs, keyed by bucket+"|"+key) so the
// PHASE-2A byte-mover can be exercised — including a failAfter injection that
// makes the Nth MoveObject fail, to prove the move rolls back.
type fakeBroker struct {
	reads, writes int
	lastTTL       time.Duration
	objs          map[string]string // bucket|key -> contents (object presence)
	moves         int               // successful MoveObject count
	failAfter     int               // when >0, the failAfter-th MoveObject fails
	deletes       int               // successful DeleteObject count
	deleteFail    bool              // when true, DeleteObject always errors (non-missing)
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

func okey(bucket, key string) string { return bucket + "|" + key }

// put seeds a virtual object so MoveObject has bytes to relocate.
func (f *fakeBroker) put(bucket, key, body string) {
	if f.objs == nil {
		f.objs = map[string]string{}
	}
	f.objs[okey(bucket, key)] = body
}

func (f *fakeBroker) get(bucket, key string) (string, bool) {
	v, ok := f.objs[okey(bucket, key)]
	return v, ok
}

// MoveObject relocates a virtual object. A missing source is a no-op (matching
// the real broker's pending-node behaviour). When failAfter is reached it errors
// WITHOUT moving, so the service must revert prior moves.
func (f *fakeBroker) MoveObject(_ context.Context, _ /*ownerID*/, bucket, srcKey, dstKey string) error {
	if srcKey == "" || dstKey == "" || srcKey == dstKey {
		return nil
	}
	f.moves++
	if f.failAfter > 0 && f.moves == f.failAfter {
		return fmt.Errorf("fakeBroker: injected MoveObject failure")
	}
	if f.objs == nil {
		f.objs = map[string]string{}
	}
	body, ok := f.objs[okey(bucket, srcKey)]
	if !ok {
		return nil // pending node: nothing to move
	}
	delete(f.objs, okey(bucket, srcKey))
	f.objs[okey(bucket, dstKey)] = body
	return nil
}

func (f *fakeBroker) PutContent(_ context.Context, _ /*ownerID*/, bucket, key string, r io.Reader, _ int64, _ string) (string, error) {
	b, err := io.ReadAll(r)
	if err != nil {
		return "", err
	}
	f.put(bucket, key, string(b))
	return "etag-" + key, nil
}

func (f *fakeBroker) GetContent(_ context.Context, _ /*ownerID*/, bucket, key string) (io.ReadCloser, int64, error) {
	body, ok := f.get(bucket, key)
	if !ok {
		return nil, 0, fmt.Errorf("fakeBroker: no object %s", okey(bucket, key))
	}
	return io.NopCloser(strings.NewReader(body)), int64(len(body)), nil
}

// DeleteObject removes a virtual object, mirroring the real broker's
// treat-missing-as-success behaviour. Set deleteFail to inject a non-missing
// failure (e.g. to test that PurgeTombstones leaves the row tombstoned).
func (f *fakeBroker) DeleteObject(_ context.Context, _ /*ownerID*/, bucket, key string) error {
	if f.deleteFail {
		return fmt.Errorf("fakeBroker: injected DeleteObject failure")
	}
	if key == "" {
		return nil
	}
	if f.objs != nil {
		delete(f.objs, okey(bucket, key))
	}
	f.deletes++
	return nil
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

// TestRedeemLinkHonorsRole guards against a real bug: RedeemLink used to
// always mint a READ grant regardless of the link's own role, so an "editor"
// share link silently granted no writes. A viewer link must mint a read
// grant; an editor link must mint a write grant.
func TestRedeemLinkHonorsRole(t *testing.T) {
	svc, _ := newTestService(t)
	n := uploadFile(t, svc, owner, "", "linked.txt")

	viewerLink, err := svc.CreateLink(owner, n.ID, RoleViewer, time.Minute)
	if err != nil {
		t.Fatalf("CreateLink viewer: %v", err)
	}
	_, g, err := svc.RedeemLink(context.Background(), other, viewerLink.Token, ttl)
	if err != nil {
		t.Fatalf("redeem viewer link: %v", err)
	}
	if g.Method != "GET" {
		t.Fatalf("viewer link minted %s grant, want GET (read)", g.Method)
	}

	editorLink, err := svc.CreateLink(owner, n.ID, RoleEditor, time.Minute)
	if err != nil {
		t.Fatalf("CreateLink editor: %v", err)
	}
	_, g2, err := svc.RedeemLink(context.Background(), other, editorLink.Token, ttl)
	if err != nil {
		t.Fatalf("redeem editor link: %v", err)
	}
	if g2.Method != "PUT" {
		t.Fatalf("editor link minted %s grant, want PUT (write) — editor links must not be read-only", g2.Method)
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
	if _, err := svc.Move(context.Background(), owner, dir.ID, "", "b"); err != nil {
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

// --- PHASE-2A byte-mover ---------------------------------------------------

// TestByteMoverSubtree: renaming a folder physically relocates EVERY file's
// bytes in its subtree (not just the index keys), via the broker.
func TestByteMoverSubtree(t *testing.T) {
	svc, fb := newTestService(t)
	bucket := "vulos-" + owner

	a, err := svc.CreateFolder(owner, "", "a")
	if err != nil {
		t.Fatalf("CreateFolder a: %v", err)
	}
	sub, err := svc.CreateFolder(owner, a.ID, "sub")
	if err != nil {
		t.Fatalf("CreateFolder sub: %v", err)
	}
	f := uploadFile(t, svc, owner, a.ID, "f.txt")   // a/f.txt
	g := uploadFile(t, svc, owner, sub.ID, "g.txt") // a/sub/g.txt
	fb.put(bucket, f.ObjectKey, "FFF")              // seed the bytes
	fb.put(bucket, g.ObjectKey, "GGG")

	// Rename a → b. Bytes for the whole subtree must follow.
	if _, err := svc.Move(context.Background(), owner, a.ID, "", "b"); err != nil {
		t.Fatalf("Move folder: %v", err)
	}

	wantF := owner + "/drive/b/f.txt"
	wantG := owner + "/drive/b/sub/g.txt"
	if body, ok := fb.get(bucket, wantF); !ok || body != "FFF" {
		t.Fatalf("f.txt bytes not at new key %q (ok=%v body=%q)", wantF, ok, body)
	}
	if body, ok := fb.get(bucket, wantG); !ok || body != "GGG" {
		t.Fatalf("g.txt bytes not at new key %q (ok=%v body=%q)", wantG, ok, body)
	}
	// Old keys must be gone (copy+delete, not copy).
	if _, ok := fb.get(bucket, owner+"/drive/a/f.txt"); ok {
		t.Fatalf("old f.txt bytes still present after move")
	}
	if _, ok := fb.get(bucket, owner+"/drive/a/sub/g.txt"); ok {
		t.Fatalf("old g.txt bytes still present after move")
	}
	// Index agrees with the bytes.
	if mv, _ := svc.getNode(g.ID); mv.ObjectKey != wantG {
		t.Fatalf("index g key=%q want %q", mv.ObjectKey, wantG)
	}
}

// TestByteMoverRollback: when a byte move fails partway through a subtree move,
// the already-moved bytes are reverted AND the index is left unchanged, so the
// store and the index stay consistent.
func TestByteMoverRollback(t *testing.T) {
	svc, fb := newTestService(t)
	bucket := "vulos-" + owner

	a, err := svc.CreateFolder(owner, "", "a")
	if err != nil {
		t.Fatalf("CreateFolder: %v", err)
	}
	f1 := uploadFile(t, svc, owner, a.ID, "f1.txt")
	f2 := uploadFile(t, svc, owner, a.ID, "f2.txt")
	k1, k2 := f1.ObjectKey, f2.ObjectKey
	fb.put(bucket, k1, "ONE")
	fb.put(bucket, k2, "TWO")

	// Fail the 2nd object move: the 1st will have moved and must be reverted.
	fb.failAfter = 2
	if _, err := svc.Move(context.Background(), owner, a.ID, "", "b"); err == nil {
		t.Fatalf("Move should have failed (injected byte-move failure)")
	}

	// Bytes back at their ORIGINAL keys (nothing left at the new keys).
	if body, ok := fb.get(bucket, k1); !ok || body != "ONE" {
		t.Fatalf("f1 bytes not reverted to %q (ok=%v body=%q)", k1, ok, body)
	}
	if body, ok := fb.get(bucket, k2); !ok || body != "TWO" {
		t.Fatalf("f2 bytes disappeared from %q (ok=%v body=%q)", k2, ok, body)
	}
	for _, bad := range []string{owner + "/drive/b/f1.txt", owner + "/drive/b/f2.txt"} {
		if _, ok := fb.get(bucket, bad); ok {
			t.Fatalf("bytes leaked at new key %q after rollback", bad)
		}
	}

	// Index unchanged: folder still "a", children still under a/.
	if av, _ := svc.getNode(a.ID); av.Name != "a" || av.Path != "a" {
		t.Fatalf("folder index changed after failed move: name=%q path=%q", av.Name, av.Path)
	}
	if mv, _ := svc.getNode(f1.ID); mv.ObjectKey != k1 || mv.Path != "a/f1.txt" {
		t.Fatalf("f1 index changed after failed move: key=%q path=%q", mv.ObjectKey, mv.Path)
	}
}

// TestContentDataPlaneLocalFS: the OS-mediated data plane writes and reads bytes
// through the REAL broker in standalone (local-FS) mode, and ACL-gates both.
func TestContentDataPlaneLocalFS(t *testing.T) {
	root := t.TempDir()
	resolver := storage.NewResolver(storage.ResolverConfig{BucketPrefix: "vulos-", LocalRoot: root})
	broker := storage.NewGrantBroker(resolver, storage.STSConfig{}, time.Minute)
	dbPath := filepath.Join(t.TempDir(), "files.db")
	svc, err := New(dbPath, broker, resolver.BucketFor)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { svc.Close() })

	n := uploadFile(t, svc, owner, "", "hello.txt")
	if _, err := svc.PutContent(context.Background(), owner, n.ID, strings.NewReader("hello world"), 11, "text/plain"); err != nil {
		t.Fatalf("PutContent: %v", err)
	}
	// Bytes really landed on disk under LocalRoot/<key>.
	onDisk, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(n.ObjectKey)))
	if err != nil || string(onDisk) != "hello world" {
		t.Fatalf("on-disk bytes=%q err=%v", onDisk, err)
	}
	// Read them back via the data plane.
	rc, _, size, err := svc.GetContent(context.Background(), owner, n.ID)
	if err != nil {
		t.Fatalf("GetContent: %v", err)
	}
	defer rc.Close()
	got, _ := io.ReadAll(rc)
	if string(got) != "hello world" || size != 11 {
		t.Fatalf("read back=%q size=%d want %q/11", got, size, "hello world")
	}
	// A stranger can neither write nor read.
	if _, err := svc.PutContent(context.Background(), other, n.ID, strings.NewReader("x"), 1, ""); err != ErrForbidden {
		t.Fatalf("stranger PutContent: err=%v want ErrForbidden", err)
	}
	if _, _, _, err := svc.GetContent(context.Background(), other, n.ID); err != ErrForbidden {
		t.Fatalf("stranger GetContent: err=%v want ErrForbidden", err)
	}
}

// TestByteMoverRealLocalFS exercises the REAL storage.GrantBroker in standalone
// (local-FS) mode through the service: a folder rename os.Renames the actual
// files on disk for the whole subtree.
func TestByteMoverRealLocalFS(t *testing.T) {
	root := t.TempDir()
	resolver := storage.NewResolver(storage.ResolverConfig{BucketPrefix: "vulos-", LocalRoot: root})
	broker := storage.NewGrantBroker(resolver, storage.STSConfig{}, time.Minute)
	dbPath := filepath.Join(t.TempDir(), "files.db")
	svc, err := New(dbPath, broker, resolver.BucketFor)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { svc.Close() })

	a, _ := svc.CreateFolder(owner, "", "a")
	f := uploadFile(t, svc, owner, a.ID, "f.txt")

	// Write the real bytes where the local grant points (LocalRoot/<key>).
	srcPath := filepath.Join(root, filepath.FromSlash(f.ObjectKey))
	if err := os.MkdirAll(filepath.Dir(srcPath), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(srcPath, []byte("disk-bytes"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	if _, err := svc.Move(context.Background(), owner, a.ID, "", "b"); err != nil {
		t.Fatalf("Move: %v", err)
	}

	if _, err := os.Stat(srcPath); !os.IsNotExist(err) {
		t.Fatalf("source file still present after move (err=%v)", err)
	}
	dstPath := filepath.Join(root, filepath.FromSlash(owner+"/drive/b/f.txt"))
	body, err := os.ReadFile(dstPath)
	if err != nil {
		t.Fatalf("read moved file: %v", err)
	}
	if string(body) != "disk-bytes" {
		t.Fatalf("moved file contents=%q want disk-bytes", body)
	}
}

// TestSearchACLScoped proves the wave-55 read-only Search is ACL-safe by
// construction: it returns a user's OWN files and files explicitly SHARED with
// them, and NEVER another user's private files. This is the primary ACL boundary
// the sovereign assistant's find_file tool relies on.
func TestSearchACLScoped(t *testing.T) {
	svc, _ := newTestService(t)

	// owner has two files; other has one private file.
	_ = uploadFile(t, svc, owner, "", "Q3-plan.md")
	shared := uploadFile(t, svc, owner, "", "budget-shared.csv")
	_ = uploadFile(t, svc, other, "", "other-secret.md")

	// owner searching sees only their own files, never other's.
	res, err := svc.Search(owner, "", 25)
	if err != nil {
		t.Fatalf("Search(owner): %v", err)
	}
	for _, n := range res {
		if n.OwnerID != owner {
			t.Fatalf("Search leaked a non-owned node: %+v", n)
		}
	}
	if len(res) != 2 {
		t.Fatalf("owner should see 2 own files, got %d", len(res))
	}

	// other searching for the owner's file by name gets NOTHING (not shared).
	res, err = svc.Search(other, "budget", 25)
	if err != nil {
		t.Fatalf("Search(other): %v", err)
	}
	for _, n := range res {
		if n.OwnerID == owner {
			t.Fatalf("CROSS-USER LEAK: other saw owner's private file %+v", n)
		}
	}

	// Once owner shares that file with other (viewer), it surfaces in other's
	// search — proving real grants are honored, not ignored.
	if _, err := svc.Share(owner, shared.ID, other, RoleViewer); err != nil {
		t.Fatalf("Share: %v", err)
	}
	res, err = svc.Search(other, "budget", 25)
	if err != nil {
		t.Fatalf("Search(other, after share): %v", err)
	}
	found := false
	for _, n := range res {
		if n.ID == shared.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("shared file did not surface in the recipient's search: %+v", res)
	}
}

// TestSearchLikeEscaping proves a query full of LIKE metacharacters is matched
// literally (cannot widen the pattern to match everything).
func TestSearchLikeEscaping(t *testing.T) {
	svc, _ := newTestService(t)
	_ = uploadFile(t, svc, owner, "", "report.txt")
	_ = uploadFile(t, svc, owner, "", "100%-done.txt")

	// A bare "%" must NOT act as a wildcard matching every file; it should match
	// only the file whose name literally contains "%".
	res, err := svc.Search(owner, "%", 25)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(res) != 1 || res[0].Name != "100%-done.txt" {
		t.Fatalf("LIKE metachar not escaped — '%%' matched %d files: %+v", len(res), res)
	}
}

// --- tombstone purge --------------------------------------------------------

// TestPurgeTombstones guards the real bug: soft delete (Delete) tombstones a
// row but never frees bucket bytes and offers no purge. It must: (1) leave
// bytes untouched right after a soft delete, (2) leave a too-young tombstone
// alone, (3) once past retention, delete the bucket bytes AND hard-delete the
// index row, and (4) if byte deletion fails, leave the row tombstoned for a
// retry rather than losing the pointer to still-live bytes.
func TestPurgeTombstones(t *testing.T) {
	svc, fb := newTestService(t)
	bucket := "vulos-" + owner

	n := uploadFile(t, svc, owner, "", "trash.txt")
	fb.put(bucket, n.ObjectKey, "bytes")
	if _, err := svc.Commit(owner, n.ID, 5, "text/plain", "e1"); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if err := svc.Delete(owner, n.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok := fb.get(bucket, n.ObjectKey); !ok {
		t.Fatalf("bytes removed by soft delete alone — bytes must only go at purge time")
	}

	// Not yet past retention: purge is a no-op.
	if purged, err := svc.PurgeTombstones(context.Background(), time.Hour); err != nil || purged != 0 {
		t.Fatalf("purged=%d err=%v, want 0 (tombstone not yet past retention)", purged, err)
	}
	if _, ok := fb.get(bucket, n.ObjectKey); !ok {
		t.Fatalf("bytes removed before retention elapsed")
	}

	time.Sleep(5 * time.Millisecond)
	purged, err := svc.PurgeTombstones(context.Background(), time.Millisecond)
	if err != nil {
		t.Fatalf("PurgeTombstones: %v", err)
	}
	if purged != 1 {
		t.Fatalf("purged=%d want 1", purged)
	}
	if _, ok := fb.get(bucket, n.ObjectKey); ok {
		t.Fatalf("bucket bytes still present after purge — soft-delete-forever bug not fixed")
	}
	stale, err := svc.staleTombstones(time.Now())
	if err != nil {
		t.Fatalf("staleTombstones: %v", err)
	}
	for _, sn := range stale {
		if sn.ID == n.ID {
			t.Fatalf("index row for %s still present after purge — expected hard delete", n.ID)
		}
	}

	// A byte-deletion failure must NOT hard-delete the row (would strand
	// unreachable-but-still-live bytes with no index pointer).
	n2 := uploadFile(t, svc, owner, "", "trash2.txt")
	fb.put(bucket, n2.ObjectKey, "bytes2")
	if err := svc.Delete(owner, n2.ID); err != nil {
		t.Fatalf("Delete n2: %v", err)
	}
	fb.deleteFail = true
	time.Sleep(5 * time.Millisecond)
	purged2, err := svc.PurgeTombstones(context.Background(), time.Millisecond)
	if err != nil {
		t.Fatalf("PurgeTombstones (deleteFail): %v", err)
	}
	if purged2 != 0 {
		t.Fatalf("purged=%d want 0 when the broker fails to delete bytes", purged2)
	}
	stale2, err := svc.staleTombstones(time.Now())
	if err != nil {
		t.Fatalf("staleTombstones: %v", err)
	}
	found := false
	for _, sn := range stale2 {
		if sn.ID == n2.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("row for %s was hard-deleted despite a failed byte deletion", n2.ID)
	}
}
