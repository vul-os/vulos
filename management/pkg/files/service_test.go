package files

// service_test.go — Drive semantics: naming, the sibling invariant, the
// move-with-descendants byte relocation (and its rollback), soft delete and the
// tombstone purge. The HTTP contract and the security gates are tested against
// the real routes in cmd/server.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/vul-os/vulos-management/pkg/cpdb"
)

// ─── fixtures ─────────────────────────────────────────────────────────────────

// memBroker is an in-memory Broker: it records every object it is asked to
// hold, so a test can assert exactly which keys exist after a move.
type memBroker struct {
	objs map[string]string // "bucket/key" -> body
	// failMoveFrom makes MoveObject fail for one source key, so the rollback
	// path can be driven.
	failMoveFrom string
	// failDeleteOf makes DeleteObject fail for one key, so the purge's
	// "leave it for the next sweep" path can be driven.
	failDeleteOf string
	moves        int
}

func newMemBroker() *memBroker { return &memBroker{objs: map[string]string{}} }

func (b *memBroker) put(bucket, key, body string) { b.objs[bucket+"/"+key] = body }

func (b *memBroker) has(bucket, key string) bool {
	_, ok := b.objs[bucket+"/"+key]
	return ok
}

func (b *memBroker) MintRead(_ context.Context, _, bucket, key string, ttl time.Duration) (ObjectGrant, error) {
	return ObjectGrant{Type: GrantPresigned, Method: "GET", Bucket: bucket, Key: key,
		URL: "mem://" + bucket + "/" + key, ExpiresAt: time.Now().UTC().Add(ttl)}, nil
}

func (b *memBroker) MintWrite(_ context.Context, _, bucket, key string, ttl time.Duration) (ObjectGrant, error) {
	return ObjectGrant{Type: GrantPresigned, Method: "PUT", Bucket: bucket, Key: key,
		URL: "mem://" + bucket + "/" + key, ExpiresAt: time.Now().UTC().Add(ttl)}, nil
}

func (b *memBroker) MoveObject(_ context.Context, _, bucket, srcKey, dstKey string) error {
	if srcKey == b.failMoveFrom {
		return errors.New("memBroker: injected move failure")
	}
	body, ok := b.objs[bucket+"/"+srcKey]
	if !ok {
		return nil // missing source is a no-op success (pending node)
	}
	delete(b.objs, bucket+"/"+srcKey)
	b.objs[bucket+"/"+dstKey] = body
	b.moves++
	return nil
}

func (b *memBroker) DeleteObject(_ context.Context, _, bucket, key string) error {
	if key == b.failDeleteOf {
		return errors.New("memBroker: injected delete failure")
	}
	delete(b.objs, bucket+"/"+key)
	return nil
}

const testBucket = "vulos-testbucket"

func newTestService(t *testing.T) (*Service, *memBroker) {
	t.Helper()
	db, err := cpdb.OpenSQLiteDSN(":memory:")
	if err != nil {
		t.Fatalf("cpdb.OpenSQLiteDSN: %v", err)
	}
	broker := newMemBroker()
	svc, err := Open(db, broker, func(_ context.Context, _ string) string { return testBucket })
	if err != nil {
		_ = db.Close()
		t.Fatalf("files.Open: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	return svc, broker
}

// upload runs the full grant→bytes→commit sequence, landing `body` in the broker.
func upload(t *testing.T, svc *Service, broker *memBroker, userID, parentID, name, body string) *Node {
	t.Helper()
	ctx := context.Background()
	n, grant, err := svc.UploadGrant(ctx, userID, parentID, name, "text/plain", time.Minute)
	if err != nil {
		t.Fatalf("UploadGrant(%s): %v", name, err)
	}
	broker.put(grant.Bucket, grant.Key, body)
	if _, err := svc.Commit(ctx, userID, n.ID, int64(len(body)), "text/plain", "etag-"+name); err != nil {
		t.Fatalf("Commit(%s): %v", name, err)
	}
	return n
}

// ─── naming ───────────────────────────────────────────────────────────────────

func TestValidName_Rejects(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	for _, name := range []string{"", strings.Repeat("a", 256), ".", "..", "a/b", "a\\b", "a\x00b"} {
		if _, err := svc.CreateFolder(ctx, "u1", "", name); !errors.Is(err, ErrInvalid) {
			t.Errorf("CreateFolder(%q): want ErrInvalid, got %v", name, err)
		}
	}
	if _, err := svc.CreateFolder(ctx, "u1", "", strings.Repeat("a", 255)); err != nil {
		t.Errorf("CreateFolder(255 chars): want ok, got %v", err)
	}
}

func TestCreateFolder_DuplicateSibling(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	if _, err := svc.CreateFolder(ctx, "u1", "", "docs"); err != nil {
		t.Fatalf("CreateFolder: %v", err)
	}
	_, err := svc.CreateFolder(ctx, "u1", "", "docs")
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "name already exists") {
		t.Fatalf("duplicate folder: want ErrInvalid/name already exists, got %v", err)
	}
	// The SAME name is fine for a different owner — the sibling invariant is
	// scoped to (owner, parent, name).
	if _, err := svc.CreateFolder(ctx, "u2", "", "docs"); err != nil {
		t.Fatalf("CreateFolder for a second owner: %v", err)
	}
}

// The sibling rule is a DB invariant, not just an app pre-check: a direct insert
// that skips CreateFolder's read must still be refused.
func TestSiblingUniqueness_IsADatabaseInvariant(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	n, err := svc.CreateFolder(ctx, "u1", "", "docs")
	if err != nil {
		t.Fatalf("CreateFolder: %v", err)
	}
	dup := *n
	dup.ID = "0000000000DUPLICATE0000000"
	if err := svc.insertNode(ctx, &dup); !errors.Is(err, ErrInvalid) {
		t.Fatalf("duplicate insert: want ErrInvalid, got %v", err)
	}
	// A tombstoned row frees its name again.
	if err := svc.softDelete(ctx, n.ID); err != nil {
		t.Fatalf("softDelete: %v", err)
	}
	if err := svc.insertNode(ctx, &dup); err != nil {
		t.Fatalf("insert after tombstone: %v", err)
	}
}

// ─── folders are metadata-only ────────────────────────────────────────────────

func TestCreateFolder_WritesNoObject(t *testing.T) {
	svc, broker := newTestService(t)
	n, err := svc.CreateFolder(context.Background(), "u1", "", "docs")
	if err != nil {
		t.Fatalf("CreateFolder: %v", err)
	}
	if n.ObjectKey != "" || n.Bucket != "" {
		t.Errorf("folder carries bytes: bucket=%q key=%q", n.Bucket, n.ObjectKey)
	}
	if len(broker.objs) != 0 {
		t.Errorf("folder created %d object(s) in the bucket; want 0", len(broker.objs))
	}
}

// ─── keys ─────────────────────────────────────────────────────────────────────

func TestUploadGrant_KeyIsUnderTheDrivePrefix(t *testing.T) {
	svc, broker := newTestService(t)
	ctx := context.Background()
	docs, err := svc.CreateFolder(ctx, "u1", "", "docs")
	if err != nil {
		t.Fatalf("CreateFolder: %v", err)
	}
	n := upload(t, svc, broker, "u1", docs.ID, "report.txt", "hello")
	if want := "u1/drive/docs/report.txt"; n.ObjectKey != want {
		t.Errorf("object key = %q, want %q", n.ObjectKey, want)
	}
	if n.Path != "docs/report.txt" {
		t.Errorf("path = %q, want docs/report.txt", n.Path)
	}
}

// A re-upload of an existing name reuses the node AND its key: the bytes are
// overwritten and the history is append-only metadata over one key.
func TestUploadGrant_ReuploadReusesNodeAndKey(t *testing.T) {
	svc, broker := newTestService(t)
	ctx := context.Background()
	first := upload(t, svc, broker, "u1", "", "notes.txt", "v1")
	second := upload(t, svc, broker, "u1", "", "notes.txt", "v2-longer")
	if first.ID != second.ID || first.ObjectKey != second.ObjectKey {
		t.Fatalf("re-upload made a new node/key: %s/%s vs %s/%s",
			first.ID, first.ObjectKey, second.ID, second.ObjectKey)
	}
	vs, err := svc.Versions(ctx, "u1", first.ID)
	if err != nil {
		t.Fatalf("Versions: %v", err)
	}
	if len(vs) != 2 {
		t.Fatalf("got %d versions, want 2", len(vs))
	}
	if vs[0].VersionKey != vs[1].VersionKey || vs[0].VersionKey != first.ObjectKey {
		t.Errorf("versions do not share the node's key: %q, %q", vs[0].VersionKey, vs[1].VersionKey)
	}
	if vs[0].CreatedAt.Before(vs[1].CreatedAt) {
		t.Errorf("versions are not newest-first")
	}
	if broker.objs[testBucket+"/"+first.ObjectKey] != "v2-longer" {
		t.Errorf("bytes were not overwritten: %q", broker.objs[testBucket+"/"+first.ObjectKey])
	}
}

// ─── listing ──────────────────────────────────────────────────────────────────

func TestList_EmptyRootIsAnEmptyList(t *testing.T) {
	svc, _ := newTestService(t)
	nodes, err := svc.List(context.Background(), "u1", "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(nodes) != 0 {
		t.Fatalf("got %d nodes, want 0", len(nodes))
	}
}

func TestList_OrderIsFoldersThenNamesAscending(t *testing.T) {
	svc, broker := newTestService(t)
	ctx := context.Background()
	upload(t, svc, broker, "u1", "", "b.txt", "x")
	upload(t, svc, broker, "u1", "", "a.txt", "x")
	if _, err := svc.CreateFolder(ctx, "u1", "", "zeta"); err != nil {
		t.Fatalf("CreateFolder: %v", err)
	}
	if _, err := svc.CreateFolder(ctx, "u1", "", "alpha"); err != nil {
		t.Fatalf("CreateFolder: %v", err)
	}
	nodes, err := svc.List(ctx, "u1", "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var got []string
	for _, n := range nodes {
		got = append(got, n.Name)
	}
	want := []string{"alpha", "zeta", "a.txt", "b.txt"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("order = %v, want %v", got, want)
	}
}

// ─── move / rename ────────────────────────────────────────────────────────────

func TestMove_RenameRekeysTheObject(t *testing.T) {
	svc, broker := newTestService(t)
	ctx := context.Background()
	n := upload(t, svc, broker, "u1", "", "old.txt", "payload")
	oldKey := n.ObjectKey

	moved, err := svc.Move(ctx, "u1", n.ID, "", "new.txt")
	if err != nil {
		t.Fatalf("Move: %v", err)
	}
	if want := "u1/drive/new.txt"; moved.ObjectKey != want {
		t.Errorf("object key = %q, want %q", moved.ObjectKey, want)
	}
	if broker.has(testBucket, oldKey) {
		t.Errorf("old key %q still holds bytes", oldKey)
	}
	if broker.objs[testBucket+"/"+moved.ObjectKey] != "payload" {
		t.Errorf("bytes did not follow the rename")
	}
	// The index agrees with the bytes.
	reread, err := svc.getNode(ctx, n.ID)
	if err != nil {
		t.Fatalf("getNode: %v", err)
	}
	if reread.ObjectKey != moved.ObjectKey || reread.Path != "new.txt" || reread.Name != "new.txt" {
		t.Errorf("index not committed: %+v", reread)
	}
}

func TestMove_FolderRekeysEveryDescendant(t *testing.T) {
	svc, broker := newTestService(t)
	ctx := context.Background()
	docs, err := svc.CreateFolder(ctx, "u1", "", "docs")
	if err != nil {
		t.Fatalf("CreateFolder: %v", err)
	}
	deep, err := svc.CreateFolder(ctx, "u1", docs.ID, "deep")
	if err != nil {
		t.Fatalf("CreateFolder: %v", err)
	}
	archive, err := svc.CreateFolder(ctx, "u1", "", "archive")
	if err != nil {
		t.Fatalf("CreateFolder: %v", err)
	}
	top := upload(t, svc, broker, "u1", docs.ID, "top.txt", "T")
	nested := upload(t, svc, broker, "u1", deep.ID, "nested.txt", "N")

	if _, err := svc.Move(ctx, "u1", docs.ID, archive.ID, ""); err != nil {
		t.Fatalf("Move: %v", err)
	}

	for _, tc := range []struct {
		id       string
		wantPath string
		wantKey  string
		wantBody string
	}{
		{top.ID, "archive/docs/top.txt", "u1/drive/archive/docs/top.txt", "T"},
		{nested.ID, "archive/docs/deep/nested.txt", "u1/drive/archive/docs/deep/nested.txt", "N"},
	} {
		n, err := svc.getNode(ctx, tc.id)
		if err != nil {
			t.Fatalf("getNode: %v", err)
		}
		if n.Path != tc.wantPath || n.ObjectKey != tc.wantKey {
			t.Errorf("descendant: path=%q key=%q, want %q / %q", n.Path, n.ObjectKey, tc.wantPath, tc.wantKey)
		}
		if broker.objs[testBucket+"/"+tc.wantKey] != tc.wantBody {
			t.Errorf("bytes not relocated to %q", tc.wantKey)
		}
	}
	// The folder rows moved too, and nothing was left at the old keys.
	if broker.has(testBucket, "u1/drive/docs/top.txt") || broker.has(testBucket, "u1/drive/docs/deep/nested.txt") {
		t.Errorf("bytes left behind at the pre-move keys")
	}
	moved, err := svc.getNode(ctx, deep.ID)
	if err != nil {
		t.Fatalf("getNode: %v", err)
	}
	if moved.Path != "archive/docs/deep" {
		t.Errorf("nested folder path = %q, want archive/docs/deep", moved.Path)
	}
}

func TestMove_IntoOwnSubtreeIsRefusedAndMovesNoByte(t *testing.T) {
	svc, broker := newTestService(t)
	ctx := context.Background()
	docs, _ := svc.CreateFolder(ctx, "u1", "", "docs")
	deep, _ := svc.CreateFolder(ctx, "u1", docs.ID, "deep")
	upload(t, svc, broker, "u1", docs.ID, "f.txt", "F")
	before := broker.moves

	if _, err := svc.Move(ctx, "u1", docs.ID, deep.ID, ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("move into own subtree: want ErrInvalid, got %v", err)
	}
	if broker.moves != before {
		t.Errorf("a refused move relocated bytes")
	}
	if !broker.has(testBucket, "u1/drive/docs/f.txt") {
		t.Errorf("bytes moved despite the refusal")
	}
}

func TestMove_RejectsCollisionNonFolderParentAndCrossOwner(t *testing.T) {
	svc, broker := newTestService(t)
	ctx := context.Background()
	a := upload(t, svc, broker, "u1", "", "a.txt", "A")
	upload(t, svc, broker, "u1", "", "b.txt", "B")
	other, err := svc.CreateFolder(ctx, "u2", "", "theirs")
	if err != nil {
		t.Fatalf("CreateFolder: %v", err)
	}
	before := broker.moves

	if _, err := svc.Move(ctx, "u1", a.ID, "", "b.txt"); !errors.Is(err, ErrInvalid) {
		t.Errorf("rename onto a sibling: want ErrInvalid, got %v", err)
	}
	fileParent := upload(t, svc, broker, "u1", "", "notafolder.txt", "X")
	if _, err := svc.Move(ctx, "u1", a.ID, fileParent.ID, ""); !errors.Is(err, ErrInvalid) {
		t.Errorf("move under a file: want ErrInvalid, got %v", err)
	}
	if _, err := svc.Move(ctx, "u1", a.ID, other.ID, ""); !errors.Is(err, ErrInvalid) {
		t.Errorf("cross-owner move: want ErrInvalid, got %v", err)
	}
	if broker.moves != before {
		t.Errorf("a refused move relocated bytes")
	}
	if !broker.has(testBucket, "u1/drive/a.txt") {
		t.Errorf("a.txt lost its bytes to a refused move")
	}
}

// A node whose grant was minted but whose bytes never landed still carries an
// object_key. Moving it must reparent the row, not fail on the absent object.
func TestMove_PendingNodeReparentsCleanly(t *testing.T) {
	svc, broker := newTestService(t)
	ctx := context.Background()
	dst, err := svc.CreateFolder(ctx, "u1", "", "dst")
	if err != nil {
		t.Fatalf("CreateFolder: %v", err)
	}
	pending, _, err := svc.UploadGrant(ctx, "u1", "", "never-uploaded.bin", "application/octet-stream", time.Minute)
	if err != nil {
		t.Fatalf("UploadGrant: %v", err)
	}
	moved, err := svc.Move(ctx, "u1", pending.ID, dst.ID, "")
	if err != nil {
		t.Fatalf("Move of a pending node: %v", err)
	}
	if want := "u1/drive/dst/never-uploaded.bin"; moved.ObjectKey != want {
		t.Errorf("object key = %q, want %q", moved.ObjectKey, want)
	}
	if len(broker.objs) != 0 {
		t.Errorf("a pending move created objects: %v", broker.objs)
	}
}

// A byte move that fails partway must leave BOTH the bucket and the index
// exactly as they were.
func TestMove_ByteFailureRollsBackAndLeavesTheIndexUntouched(t *testing.T) {
	svc, broker := newTestService(t)
	ctx := context.Background()
	docs, _ := svc.CreateFolder(ctx, "u1", "", "docs")
	archive, _ := svc.CreateFolder(ctx, "u1", "", "archive")
	first := upload(t, svc, broker, "u1", docs.ID, "aaa.txt", "A")
	second := upload(t, svc, broker, "u1", docs.ID, "bbb.txt", "B")

	// The subtree is planned depth-first in listing order, so aaa.txt relocates
	// before bbb.txt fails — leaving one completed move to unwind.
	broker.failMoveFrom = second.ObjectKey

	if _, err := svc.Move(ctx, "u1", docs.ID, archive.ID, ""); err == nil {
		t.Fatal("Move: want the injected failure, got nil")
	}
	if !broker.has(testBucket, first.ObjectKey) {
		t.Errorf("completed move was not reverted: %q is gone", first.ObjectKey)
	}
	if broker.has(testBucket, "u1/drive/archive/docs/aaa.txt") {
		t.Errorf("reverted bytes are still at the destination key")
	}
	for _, id := range []string{docs.ID, first.ID, second.ID} {
		n, err := svc.getNode(ctx, id)
		if err != nil {
			t.Fatalf("getNode: %v", err)
		}
		if !strings.HasPrefix(n.Path, "docs") {
			t.Errorf("index moved despite the failure: %q", n.Path)
		}
	}
}

// ─── delete / purge ───────────────────────────────────────────────────────────

func TestDelete_IsSoftAndFreesNoBytes(t *testing.T) {
	svc, broker := newTestService(t)
	ctx := context.Background()
	docs, _ := svc.CreateFolder(ctx, "u1", "", "docs")
	child := upload(t, svc, broker, "u1", docs.ID, "inside.txt", "I")

	if err := svc.Delete(ctx, "u1", docs.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	nodes, err := svc.List(ctx, "u1", "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(nodes) != 0 {
		t.Errorf("deleted folder still listed: %v", nodes)
	}
	if _, err := svc.List(ctx, "u1", docs.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("listing a deleted folder: want ErrNotFound, got %v", err)
	}
	if !broker.has(testBucket, child.ObjectKey) {
		t.Errorf("soft delete freed bytes; it must not")
	}
}

func TestPurgeTombstones_ReclaimsBytesPastRetentionOnly(t *testing.T) {
	svc, broker := newTestService(t)
	ctx := context.Background()
	stale := upload(t, svc, broker, "u1", "", "stale.txt", "S")
	fresh := upload(t, svc, broker, "u1", "", "fresh.txt", "F")
	if err := svc.Delete(ctx, "u1", stale.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := svc.Delete(ctx, "u1", fresh.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	// Age the first tombstone past the retention window.
	old := time.Now().UTC().Add(-48 * time.Hour).Format(rfc)
	if _, err := svc.db.ExecContext(ctx, svc.db.Rebind(
		`UPDATE files_nodes SET updated_at=? WHERE id=?`), old, stale.ID); err != nil {
		t.Fatalf("age tombstone: %v", err)
	}

	n, err := svc.PurgeTombstones(ctx, 24*time.Hour)
	if err != nil {
		t.Fatalf("PurgeTombstones: %v", err)
	}
	if n != 1 {
		t.Fatalf("purged %d nodes, want 1", n)
	}
	if broker.has(testBucket, stale.ObjectKey) {
		t.Errorf("stale tombstone's bytes were not reclaimed")
	}
	if !broker.has(testBucket, fresh.ObjectKey) {
		t.Errorf("fresh tombstone's bytes were reclaimed early")
	}
	if _, err := svc.getNode(ctx, stale.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("stale row survived the purge")
	}
	vs, err := svc.listVersions(ctx, stale.ID)
	if err != nil {
		t.Fatalf("listVersions: %v", err)
	}
	if len(vs) != 0 {
		t.Errorf("purged node kept %d version row(s)", len(vs))
	}
}

// Deleting a FOLDER tombstones only that one row — its children keep deleted=0
// and simply become unreachable through the (now-gone) parent. The purge sweep
// must therefore reclaim the whole SUBTREE, not just the tombstoned row: a
// descendant left behind would keep its bytes in the bucket forever, where they
// are still sampled, still billed and still counted against the storage quota,
// while being permanently unreachable by the user.
func TestPurgeTombstones_ReclaimsTheWholeSubtree(t *testing.T) {
	svc, broker := newTestService(t)
	ctx := context.Background()
	docs, err := svc.CreateFolder(ctx, "u1", "", "docs")
	if err != nil {
		t.Fatalf("CreateFolder: %v", err)
	}
	sub, err := svc.CreateFolder(ctx, "u1", docs.ID, "sub")
	if err != nil {
		t.Fatalf("CreateFolder: %v", err)
	}
	top := upload(t, svc, broker, "u1", docs.ID, "top.txt", "T")
	deep := upload(t, svc, broker, "u1", sub.ID, "deep.txt", "D")
	keep := upload(t, svc, broker, "u1", "", "keep.txt", "K")

	// Delete the folder, then age its tombstone past the retention window.
	if err := svc.Delete(ctx, "u1", docs.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	old := time.Now().UTC().Add(-48 * time.Hour).Format(rfc)
	if _, err := svc.db.ExecContext(ctx, svc.db.Rebind(
		`UPDATE files_nodes SET updated_at=? WHERE id=?`), old, docs.ID); err != nil {
		t.Fatalf("age tombstone: %v", err)
	}

	if _, err := svc.PurgeTombstones(ctx, 24*time.Hour); err != nil {
		t.Fatalf("PurgeTombstones: %v", err)
	}

	for _, n := range []*Node{top, deep} {
		if broker.has(testBucket, n.ObjectKey) {
			t.Errorf("%s: bytes survived the purge of its deleted ancestor — billed forever, reachable never", n.Name)
		}
		if _, err := svc.getNode(ctx, n.ID); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s: row survived the purge of its deleted ancestor", n.Name)
		}
	}
	for _, id := range []string{docs.ID, sub.ID} {
		if _, err := svc.getNode(ctx, id); !errors.Is(err, ErrNotFound) {
			t.Errorf("folder row %s survived the purge", id)
		}
	}
	// A node OUTSIDE the deleted subtree is untouched.
	if !broker.has(testBucket, keep.ObjectKey) {
		t.Errorf("keep.txt: bytes outside the deleted subtree were reclaimed")
	}
	if _, err := svc.getNode(ctx, keep.ID); err != nil {
		t.Errorf("keep.txt: row outside the deleted subtree was purged: %v", err)
	}
}

// A descendant whose bytes cannot be deleted must leave its ANCESTORS in place
// too. Hard-deleting the folder above a node that survived would orphan that node
// into exactly the unreachable-but-billed state the purge exists to prevent — and
// the next sweep, which only ever starts from a tombstone, would never find it
// again. The whole subtree is retried instead.
func TestPurgeTombstones_AFailedByteDeleteStrandsNothing(t *testing.T) {
	svc, broker := newTestService(t)
	ctx := context.Background()
	docs, err := svc.CreateFolder(ctx, "u1", "", "docs")
	if err != nil {
		t.Fatalf("CreateFolder: %v", err)
	}
	stuck := upload(t, svc, broker, "u1", docs.ID, "stuck.txt", "S")
	broker.failDeleteOf = stuck.ObjectKey

	if err := svc.Delete(ctx, "u1", docs.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	old := time.Now().UTC().Add(-48 * time.Hour).Format(rfc)
	if _, err := svc.db.ExecContext(ctx, svc.db.Rebind(
		`UPDATE files_nodes SET updated_at=? WHERE id=?`), old, docs.ID); err != nil {
		t.Fatalf("age tombstone: %v", err)
	}

	if _, err := svc.PurgeTombstones(ctx, 24*time.Hour); err != nil {
		t.Fatalf("PurgeTombstones: %v", err)
	}

	// The file kept its bytes, so its row must survive...
	if _, err := svc.getNode(ctx, stuck.ID); err != nil {
		t.Errorf("the row of a node whose bytes survived was dropped: %v", err)
	}
	// ...and so must the folder above it, or the next sweep loses the trail.
	var deleted int
	if err := svc.db.QueryRowContext(ctx, svc.db.Rebind(
		`SELECT deleted FROM files_nodes WHERE id=?`), docs.ID).Scan(&deleted); err != nil {
		t.Fatalf("the tombstoned ancestor was purged while a descendant survived: %v", err)
	}
	if deleted != 1 {
		t.Errorf("ancestor deleted flag = %d, want 1 (still tombstoned, awaiting retry)", deleted)
	}

	// Once the object store recovers, the next sweep reclaims the whole subtree.
	broker.failDeleteOf = ""
	if _, err := svc.PurgeTombstones(ctx, 24*time.Hour); err != nil {
		t.Fatalf("PurgeTombstones (retry): %v", err)
	}
	if broker.has(testBucket, stuck.ObjectKey) {
		t.Errorf("retry did not reclaim the bytes")
	}
	for _, id := range []string{docs.ID, stuck.ID} {
		if _, err := svc.getNode(ctx, id); !errors.Is(err, ErrNotFound) {
			t.Errorf("retry left row %s behind", id)
		}
	}
}

// ─── self-only ────────────────────────────────────────────────────────────────

// There are no ACLs on the CP: every node op re-checks ownership, so another
// account's node is simply unreachable.
func TestSelfOnly_AnotherOwnerCannotReachANode(t *testing.T) {
	svc, broker := newTestService(t)
	ctx := context.Background()
	victimFolder, err := svc.CreateFolder(ctx, "victim", "", "private")
	if err != nil {
		t.Fatalf("CreateFolder: %v", err)
	}
	victimFile := upload(t, svc, broker, "victim", victimFolder.ID, "secret.txt", "S")

	if _, err := svc.List(ctx, "attacker", victimFolder.ID); !errors.Is(err, ErrForbidden) {
		t.Errorf("List: want ErrForbidden, got %v", err)
	}
	if _, _, err := svc.DownloadGrant(ctx, "attacker", victimFile.ID, time.Minute); !errors.Is(err, ErrForbidden) {
		t.Errorf("DownloadGrant: want ErrForbidden, got %v", err)
	}
	if _, err := svc.Move(ctx, "attacker", victimFile.ID, "", "mine.txt"); !errors.Is(err, ErrForbidden) {
		t.Errorf("Move: want ErrForbidden, got %v", err)
	}
	if err := svc.Delete(ctx, "attacker", victimFile.ID); !errors.Is(err, ErrForbidden) {
		t.Errorf("Delete: want ErrForbidden, got %v", err)
	}
	if _, err := svc.Versions(ctx, "attacker", victimFile.ID); !errors.Is(err, ErrForbidden) {
		t.Errorf("Versions: want ErrForbidden, got %v", err)
	}
	if _, err := svc.Commit(ctx, "attacker", victimFile.ID, 1, "text/plain", "x"); !errors.Is(err, ErrForbidden) {
		t.Errorf("Commit: want ErrForbidden, got %v", err)
	}
	if _, _, err := svc.UploadGrant(ctx, "attacker", victimFolder.ID, "planted.txt", "text/plain", time.Minute); !errors.Is(err, ErrForbidden) {
		t.Errorf("UploadGrant into another owner's folder: want ErrForbidden, got %v", err)
	}
	// The attacker's own root shows nothing of the victim's.
	nodes, err := svc.List(ctx, "attacker", "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(nodes) != 0 {
		t.Errorf("root listing leaked %d node(s) from another owner", len(nodes))
	}
}

// ─── ids ──────────────────────────────────────────────────────────────────────

func TestNewID_IsOpaqueSortableAndUnique(t *testing.T) {
	seen := map[string]bool{}
	prev := ""
	for i := 0; i < 200; i++ {
		id, err := newID()
		if err != nil {
			t.Fatalf("newID: %v", err)
		}
		if len(id) != 26 {
			t.Fatalf("id %q is %d chars, want 26", id, len(id))
		}
		if strings.ContainsAny(id, "/. ") {
			t.Fatalf("id %q is not opaque", id)
		}
		if seen[id] {
			t.Fatalf("duplicate id %q", id)
		}
		seen[id] = true
		if prev != "" && id[:10] < prev[:10] {
			t.Fatalf("ids are not time-sortable: %q then %q", prev, id)
		}
		prev = id
	}
}
