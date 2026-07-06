package main

// assistant_files_acl_realdb_test.go — wave-56 adversarial ACL regression for the
// wave-55 read-only FILES seam, exercised against the REAL files.Service (not a
// fake) through the SAME production adapter (assistantFilesBackend + OSFilesSource)
// the server wires. The existing assistant-package tests prove the contract against
// an in-memory fake; these prove the LIVE ACL — owner-scoped SQL + explicit shares —
// actually denies cross-user / crafted / revoked-share reads end-to-end.
//
// The sharpest attack (per the review): can user A read user B's file by handing
// read_file B's opaque node id? The answer must be NO — GetContent re-resolves the
// id and re-checks A's role on THAT node, so a foreign id yields an error, never
// bytes. And a file B shares with A then UNSHARES must stop being readable.

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"vulos/backend/internal/storage"
	"vulos/backend/services/assistant"
	"vulos/backend/services/files"
)

// realFilesSeam builds a real local-FS-backed files.Service and wraps it in the
// EXACT production adapter chain the assistant uses.
func realFilesSeam(t *testing.T) (*files.Service, assistant.FilesSource) {
	t.Helper()
	root := t.TempDir()
	resolver := storage.NewResolver(storage.ResolverConfig{BucketPrefix: "vulos-", LocalRoot: root})
	broker := storage.NewGrantBroker(resolver, storage.STSConfig{}, time.Minute)
	svc, err := files.New(filepath.Join(t.TempDir(), "files.db"), broker, resolver.BucketFor)
	if err != nil {
		t.Fatalf("files.New: %v", err)
	}
	t.Cleanup(func() { svc.Close() })
	// This is the same wrapping registerAssistantRoutes does in production.
	src := assistant.NewOSFilesSource(assistantFilesBackend{svc: svc})
	return svc, src
}

// seedFile uploads + commits a text file owned by ownerID at the Drive root.
func seedFile(t *testing.T, svc *files.Service, ownerID, name, body string) string {
	t.Helper()
	ctx := context.Background()
	n, _, err := svc.UploadGrant(ctx, ownerID, "", name, "text/plain", time.Minute)
	if err != nil {
		t.Fatalf("UploadGrant(%s/%s): %v", ownerID, name, err)
	}
	if _, err := svc.PutContent(ctx, ownerID, n.ID, strings.NewReader(body), int64(len(body)), "text/plain"); err != nil {
		t.Fatalf("PutContent: %v", err)
	}
	return n.ID
}

// THE cross-user-id ACL re-check, against the LIVE ACL. Alice cannot read Bob's
// file by handing read_file Bob's real node id — GetContent re-checks HER role on
// THAT id and returns an error, never bytes. This is the invariant the whole
// "ACL-safe-by-construction" claim rests on.
func TestRealDB_ReadFileCrossUserIDDenied(t *testing.T) {
	svc, src := realFilesSeam(t)
	ctx := context.Background()

	bobFile := seedFile(t, svc, "bob", "bob-secret.txt", "BOB PRIVATE PAYROLL")
	aliceFile := seedFile(t, svc, "alice", "alice-notes.txt", "alice's own notes")

	// Sanity: alice reads her OWN file.
	fc, err := src.ReadFile(ctx, assistant.Auth{UserID: "alice"}, aliceFile, 32<<10)
	if err != nil {
		t.Fatalf("alice reading her own file: %v", err)
	}
	if !strings.Contains(fc.Text, "alice's own notes") {
		t.Fatalf("own-file content wrong: %q", fc.Text)
	}

	// ATTACK: alice hands read_file BOB's real node id.
	got, err := src.ReadFile(ctx, assistant.Auth{UserID: "alice"}, bobFile, 32<<10)
	if err == nil {
		t.Fatalf("CROSS-USER READ ALLOWED via foreign id — ACL bypassed; got %q", got.Text)
	}
	if strings.Contains(got.Text, "BOB PRIVATE") {
		t.Fatalf("bytes of another user's file leaked despite error: %q", got.Text)
	}

	// A crafted/nonexistent id is likewise denied (opaque ids, no traversal).
	if _, err := src.ReadFile(ctx, assistant.Auth{UserID: "alice"}, "../../etc/passwd", 32<<10); err == nil {
		t.Fatal("crafted traversal-shaped id resolved to a readable file")
	}
	if _, err := src.ReadFile(ctx, assistant.Auth{UserID: "alice"}, "01HDOESNOTEXIST", 32<<10); err == nil {
		t.Fatal("nonexistent id returned content instead of an error")
	}

	// find_file must never surface bob's file to alice.
	refs, err := src.FindFiles(ctx, assistant.Auth{UserID: "alice"}, "", 25)
	if err != nil {
		t.Fatalf("FindFiles: %v", err)
	}
	for _, r := range refs {
		if r.ID == bobFile || strings.Contains(r.Name, "bob-secret") {
			t.Fatalf("find_file leaked bob's file to alice: %+v", r)
		}
	}
}

// A file bob SHARES with alice becomes readable; after UNSHARE it must STOP being
// readable AND stop surfacing in search — the seam re-checks the live ACL on every
// call, it does not cache a stale grant.
func TestRealDB_ReadFileSharedThenUnshared(t *testing.T) {
	svc, src := realFilesSeam(t)
	ctx := context.Background()

	doc := seedFile(t, svc, "bob", "shared-plan.txt", "SHARED PLAN BODY")

	// Before sharing: alice cannot read it.
	if _, err := src.ReadFile(ctx, assistant.Auth{UserID: "alice"}, doc, 32<<10); err == nil {
		t.Fatal("alice read bob's file before any share")
	}

	// bob shares viewer with alice.
	if _, err := svc.Share("bob", doc, "alice", files.RoleViewer); err != nil {
		t.Fatalf("Share: %v", err)
	}
	fc, err := src.ReadFile(ctx, assistant.Auth{UserID: "alice"}, doc, 32<<10)
	if err != nil {
		t.Fatalf("alice should read the shared file: %v", err)
	}
	if !strings.Contains(fc.Text, "SHARED PLAN BODY") {
		t.Fatalf("shared content not returned: %q", fc.Text)
	}
	// And it surfaces in her search.
	refs, _ := src.FindFiles(ctx, assistant.Auth{UserID: "alice"}, "shared", 25)
	found := false
	for _, r := range refs {
		if r.ID == doc {
			found = true
		}
	}
	if !found {
		t.Fatalf("shared file did not surface in alice's search: %+v", refs)
	}

	// bob UNSHARES. Alice must immediately lose read + search visibility.
	if err := svc.Unshare("bob", doc, "alice"); err != nil {
		t.Fatalf("Unshare: %v", err)
	}
	if got, err := src.ReadFile(ctx, assistant.Auth{UserID: "alice"}, doc, 32<<10); err == nil {
		t.Fatalf("REVOKED SHARE STILL READABLE — stale grant; got %q", got.Text)
	}
	refs2, _ := src.FindFiles(ctx, assistant.Auth{UserID: "alice"}, "shared", 25)
	for _, r := range refs2 {
		if r.ID == doc {
			t.Fatalf("revoked file still surfaces in search: %+v", r)
		}
	}
}

// The LIKE search cannot be widened by a query full of LIKE metacharacters: a
// wildcard-only query is matched LITERALLY (it finds a file literally named with
// those chars, and does NOT return every file). This proves escapeLike + ESCAPE
// '\' actually contain the pattern, so a crafted query can't enumerate a user's
// whole Drive (or, combined with any future cross-tenant bug, escape scope).
func TestRealDB_SearchLikeMetacharsCannotWiden(t *testing.T) {
	svc, src := realFilesSeam(t)
	ctx := context.Background()

	seedFile(t, svc, "alice", "alpha.txt", "a")
	seedFile(t, svc, "alice", "beta.txt", "b")
	seedFile(t, svc, "alice", "gamma.txt", "c")
	literal := seedFile(t, svc, "alice", "100%_done.txt", "d") // name contains % and _

	// A pure-wildcard query would, if unescaped, match EVERY file. Escaped, it
	// matches only a file whose NAME literally contains "%_".
	refs, err := src.FindFiles(ctx, assistant.Auth{UserID: "alice"}, "%_", 25)
	if err != nil {
		t.Fatalf("FindFiles: %v", err)
	}
	if len(refs) != 1 || refs[0].ID != literal {
		t.Fatalf("LIKE metachars widened the pattern: %q matched %d files %+v (want just the literal '100%%_done')", "%_", len(refs), refs)
	}

	// A lone '%' likewise matches literally (zero of the plain-named files).
	refs2, _ := src.FindFiles(ctx, assistant.Auth{UserID: "alice"}, "%", 25)
	for _, r := range refs2 {
		if r.ID != literal {
			t.Fatalf("query '%%' matched a file without a literal '%%' in its name: %+v", r)
		}
	}
}
