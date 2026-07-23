// files_pg_test.go — Postgres integration tests for the Files index (the cloud
// backend; self-host runs the same SQL on SQLite). Skipped unless
// VULOS_TEST_POSTGRES is set to a valid Postgres DSN.
//
// The migration is dialect-neutral and every statement is Rebind-ed, so this
// re-runs the semantics that are most likely to diverge between the two engines:
// the partial unique index that makes the sibling rule a DB invariant, the
// BIGINT sizes (a Postgres INTEGER would cap at 2 GiB), the transactional
// move-with-descendants, and the tombstone purge's subtree walk.
package files

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/vul-os/vulos-management/pkg/cpdb"
)

func newPGTestService(t *testing.T) (*Service, *memBroker) {
	t.Helper()
	dsn := os.Getenv("VULOS_TEST_POSTGRES")
	if dsn == "" {
		t.Skip("set VULOS_TEST_POSTGRES to run Postgres integration tests")
	}
	t.Setenv("DATABASE_URL", dsn)
	db, err := cpdb.Open("files_pgtest")
	if err != nil {
		t.Fatalf("cpdb.Open (postgres): %v", err)
	}
	broker := newMemBroker()
	svc, err := Open(db, broker, func(_ context.Context, _ string) string { return testBucket })
	if err != nil {
		_ = db.Close()
		t.Fatalf("files.Open (postgres): %v", err)
	}
	// The schema is shared across runs; start each test from an empty tree.
	ctx := context.Background()
	for _, tbl := range []string{"files_versions", "files_nodes"} {
		if _, err := svc.db.ExecContext(ctx, "DELETE FROM "+tbl); err != nil {
			_ = svc.Close()
			t.Fatalf("truncate %s: %v", tbl, err)
		}
	}
	t.Cleanup(func() { _ = svc.Close() })
	return svc, broker
}

func TestPG_SiblingUniquenessIsADatabaseInvariant(t *testing.T) {
	svc, _ := newPGTestService(t)
	ctx := context.Background()
	if _, err := svc.CreateFolder(ctx, "u1", "", "docs"); err != nil {
		t.Fatalf("CreateFolder: %v", err)
	}
	// Bypass the service's find-or-create pre-check and hit the index directly —
	// this is the race a concurrent create would lose.
	now := time.Now().UTC()
	err := svc.insertNode(ctx, &Node{
		ID: "DUPLICATE0000000000000000", OwnerID: "u1", ParentID: "", Name: "docs",
		IsDir: true, Path: "docs", CreatedAt: now, UpdatedAt: now,
	})
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "name already exists") {
		t.Fatalf("duplicate sibling insert: want ErrInvalid/name already exists, got %v", err)
	}
	// A tombstoned name is reusable: the unique index is partial (WHERE deleted=0).
	docs, err := svc.childByName(ctx, "u1", "", "docs")
	if err != nil {
		t.Fatalf("childByName: %v", err)
	}
	if err := svc.Delete(ctx, "u1", docs.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := svc.CreateFolder(ctx, "u1", "", "docs"); err != nil {
		t.Errorf("recreating a soft-deleted name: %v", err)
	}
}

// A Postgres INTEGER caps at 2 GiB; size must round-trip a value past it.
func TestPG_SizeIsABigint(t *testing.T) {
	svc, broker := newPGTestService(t)
	ctx := context.Background()
	n, grant, err := svc.UploadGrant(ctx, "u1", "", "huge.bin", "application/octet-stream", time.Minute)
	if err != nil {
		t.Fatalf("UploadGrant: %v", err)
	}
	broker.put(grant.Bucket, grant.Key, "x")
	const big = int64(8) << 30 // 8 GiB
	if _, err := svc.Commit(ctx, "u1", n.ID, big, "application/octet-stream", "e"); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	got, err := svc.getNode(ctx, n.ID)
	if err != nil {
		t.Fatalf("getNode: %v", err)
	}
	if got.Size != big {
		t.Errorf("size = %d, want %d", got.Size, big)
	}
}

func TestPG_MoveRekeysSubtreeInOneTransaction(t *testing.T) {
	svc, broker := newPGTestService(t)
	ctx := context.Background()
	docs, err := svc.CreateFolder(ctx, "u1", "", "docs")
	if err != nil {
		t.Fatalf("CreateFolder: %v", err)
	}
	sub, err := svc.CreateFolder(ctx, "u1", docs.ID, "sub")
	if err != nil {
		t.Fatalf("CreateFolder: %v", err)
	}
	deep := upload(t, svc, broker, "u1", sub.ID, "deep.txt", "D")
	archive, err := svc.CreateFolder(ctx, "u1", "", "archive")
	if err != nil {
		t.Fatalf("CreateFolder: %v", err)
	}

	if _, err := svc.Move(ctx, "u1", docs.ID, archive.ID, ""); err != nil {
		t.Fatalf("Move: %v", err)
	}

	got, err := svc.getNode(ctx, deep.ID)
	if err != nil {
		t.Fatalf("getNode: %v", err)
	}
	wantPath := "archive/docs/sub/deep.txt"
	if got.Path != wantPath {
		t.Errorf("descendant path = %q, want %q", got.Path, wantPath)
	}
	wantKey := driveKey("u1", wantPath)
	if got.ObjectKey != wantKey {
		t.Errorf("descendant key = %q, want %q", got.ObjectKey, wantKey)
	}
	if !broker.has(testBucket, wantKey) {
		t.Errorf("descendant bytes were not relocated to %q", wantKey)
	}
	if broker.has(testBucket, deep.ObjectKey) {
		t.Errorf("descendant bytes left behind at the old key %q", deep.ObjectKey)
	}
}

func TestPG_PurgeReclaimsTheWholeSubtree(t *testing.T) {
	svc, broker := newPGTestService(t)
	ctx := context.Background()
	docs, err := svc.CreateFolder(ctx, "u1", "", "docs")
	if err != nil {
		t.Fatalf("CreateFolder: %v", err)
	}
	child := upload(t, svc, broker, "u1", docs.ID, "child.txt", "C")

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
	if broker.has(testBucket, child.ObjectKey) {
		t.Errorf("the descendant's bytes survived the purge of its deleted ancestor")
	}
	for _, id := range []string{docs.ID, child.ID} {
		if _, err := svc.getNode(ctx, id); !errors.Is(err, ErrNotFound) {
			t.Errorf("row %s survived the purge", id)
		}
	}
}

// The root listing shares an empty parent_id across every account, so its owner
// filter has to hold on Postgres too.
func TestPG_RootListingIsOwnerFiltered(t *testing.T) {
	svc, _ := newPGTestService(t)
	ctx := context.Background()
	if _, err := svc.CreateFolder(ctx, "u1", "", "mine"); err != nil {
		t.Fatalf("CreateFolder: %v", err)
	}
	if _, err := svc.CreateFolder(ctx, "u2", "", "theirs"); err != nil {
		t.Fatalf("CreateFolder: %v", err)
	}
	nodes, err := svc.List(ctx, "u1", "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(nodes) != 1 || nodes[0].Name != "mine" {
		t.Fatalf("root listing = %+v, want only the caller's own node", nodes)
	}
}
