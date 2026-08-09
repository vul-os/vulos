package files

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"vulos/backend/internal/storage"
)

// aclSvc builds a real local-FS-backed service so content ops (Put/Get) really
// gate, not just the metadata ops.
func aclSvc(t *testing.T) *Service {
	t.Helper()
	root := t.TempDir()
	resolver := storage.NewResolver(storage.ResolverConfig{BucketPrefix: "vulos-", LocalRoot: root})
	broker := storage.NewGrantBroker(resolver, storage.STSConfig{}, time.Minute)
	svc, err := New(filepath.Join(t.TempDir(), "files.db"), broker, resolver.BucketFor)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { svc.Close() })
	return svc
}

// The ACL role ladder is enforced at every mutating operation with the CORRECT
// minimum role. A viewer can read but not write/delete/move/share; an editor can
// write/delete/move but not manage ACLs; only the owner can share/unshare/list
// shares. This pins the exact viewer<editor<owner boundary the whole Files
// product relies on.
func TestACLRoleBoundaries(t *testing.T) {
	const (
		owner  = "owner"
		editor = "editor"
		viewer = "viewer"
		nobody = "stranger"
	)
	ctx := context.Background()

	newFile := func(t *testing.T) (svc *Service, fileID string) {
		svc = aclSvc(t)
		n, _, err := svc.UploadGrant(ctx, owner, "", "doc.txt", "text/plain", time.Minute)
		if err != nil {
			t.Fatalf("UploadGrant: %v", err)
		}
		if _, err := svc.PutContent(ctx, owner, n.ID, strings.NewReader("v1"), 2, "text/plain"); err != nil {
			t.Fatalf("seed content: %v", err)
		}
		if _, err := svc.Share(owner, n.ID, editor, RoleEditor); err != nil {
			t.Fatalf("share editor: %v", err)
		}
		if _, err := svc.Share(owner, n.ID, viewer, RoleViewer); err != nil {
			t.Fatalf("share viewer: %v", err)
		}
		return svc, n.ID
	}

	// --- read: viewer+ allowed, stranger denied ---
	t.Run("read requires viewer", func(t *testing.T) {
		svc, id := newFile(t)
		for _, u := range []string{owner, editor, viewer} {
			rc, _, _, err := svc.GetContent(ctx, u, id)
			if err != nil {
				t.Fatalf("%s GetContent: want ok, got %v", u, err)
			}
			rc.Close()
		}
		if _, _, _, err := svc.GetContent(ctx, nobody, id); err != ErrNoAccess {
			t.Fatalf("stranger GetContent: want ErrNoAccess (404, not an existence oracle), got %v", err)
		}
	})

	// --- write: editor+ allowed, viewer denied ---
	t.Run("write requires editor", func(t *testing.T) {
		svc, id := newFile(t)
		if _, err := svc.PutContent(ctx, editor, id, strings.NewReader("v2"), 2, "text/plain"); err != nil {
			t.Fatalf("editor PutContent: want ok, got %v", err)
		}
		if _, err := svc.PutContent(ctx, viewer, id, strings.NewReader("nope"), 4, "text/plain"); err != ErrForbidden {
			t.Fatalf("viewer PutContent: want ErrForbidden, got %v", err)
		}
	})

	// --- delete: editor+ allowed, viewer denied ---
	t.Run("delete requires editor, viewer denied", func(t *testing.T) {
		svc, id := newFile(t)
		if err := svc.Delete(viewer, id); err != ErrForbidden {
			t.Fatalf("viewer Delete: want ErrForbidden, got %v", err)
		}
		if err := svc.Delete(editor, id); err != nil {
			t.Fatalf("editor Delete: want ok, got %v", err)
		}
	})

	// --- move: editor+ allowed, viewer denied ---
	t.Run("move requires editor", func(t *testing.T) {
		svc, id := newFile(t)
		if _, err := svc.Move(ctx, viewer, id, "", "renamed.txt"); err != ErrForbidden {
			t.Fatalf("viewer Move: want ErrForbidden, got %v", err)
		}
		if _, err := svc.Move(ctx, editor, id, "", "renamed.txt"); err != nil {
			t.Fatalf("editor Move: want ok, got %v", err)
		}
	})

	// --- share / unshare / list shares: OWNER ONLY (editor is denied) ---
	t.Run("acl management is owner-only", func(t *testing.T) {
		svc, id := newFile(t)
		// Editor — despite being able to edit bytes — cannot manage the ACL.
		if _, err := svc.Share(editor, id, nobody, RoleViewer); err != ErrForbidden {
			t.Fatalf("editor Share: want ErrForbidden, got %v", err)
		}
		if err := svc.Unshare(editor, id, viewer); err != ErrForbidden {
			t.Fatalf("editor Unshare: want ErrForbidden, got %v", err)
		}
		if _, err := svc.ListShares(editor, id); err != ErrForbidden {
			t.Fatalf("editor ListShares: want ErrForbidden, got %v", err)
		}
		if _, err := svc.ListShares(viewer, id); err != ErrForbidden {
			t.Fatalf("viewer ListShares: want ErrForbidden, got %v", err)
		}
		// Owner can list and sees exactly the two grants seeded.
		shares, err := svc.ListShares(owner, id)
		if err != nil {
			t.Fatalf("owner ListShares: %v", err)
		}
		if len(shares) != 2 {
			t.Fatalf("owner ListShares: got %d entries, want 2", len(shares))
		}
	})

	// --- share links: owner-only mint, and role can't be escalated to owner ---
	t.Run("share role cannot be escalated to owner", func(t *testing.T) {
		svc, id := newFile(t)
		if _, err := svc.Share(owner, id, nobody, RoleOwner); err == nil {
			t.Fatal("Share must reject RoleOwner (ownership is not grantable)")
		}
		if _, err := svc.CreateLink(owner, id, RoleOwner, time.Minute); err == nil {
			t.Fatal("CreateLink must reject RoleOwner")
		}
	})
}

// A grant on a folder inherits to a NEW child created after the grant, but a
// viewer grant never silently becomes an editor grant on descendants (the highest
// inherited role is taken, never elevated).
func TestACLInheritanceDoesNotElevate(t *testing.T) {
	svc := aclSvc(t)
	const owner, collab = "owner", "collab"

	dir, err := svc.CreateFolder(owner, "", "shared")
	if err != nil {
		t.Fatalf("CreateFolder: %v", err)
	}
	if _, err := svc.Share(owner, dir.ID, collab, RoleViewer); err != nil {
		t.Fatalf("Share viewer: %v", err)
	}
	// Owner creates a child under the shared folder.
	child, _, err := svc.UploadGrant(context.Background(), owner, dir.ID, "c.txt", "text/plain", time.Minute)
	if err != nil {
		t.Fatalf("UploadGrant child: %v", err)
	}
	if _, err := svc.PutContent(context.Background(), owner, child.ID, strings.NewReader("body"), 4, "text/plain"); err != nil {
		t.Fatalf("seed child content: %v", err)
	}
	// collab inherits VIEWER on the child — not editor.
	if role, _ := svc.EffectiveRole(collab, child.ID); role != RoleViewer {
		t.Fatalf("inherited role = %q, want viewer", role)
	}
	// So collab can read but not write the inherited child.
	if _, _, _, err := svc.GetContent(context.Background(), collab, child.ID); err != nil {
		t.Fatalf("inherited viewer GetContent: %v", err)
	}
	if _, err := svc.PutContent(context.Background(), collab, child.ID, strings.NewReader("x"), 1, ""); err != ErrForbidden {
		t.Fatalf("inherited viewer PutContent: want ErrForbidden, got %v", err)
	}
}

// TestErrNoAccessIsFailClosedByWrapping pins the sentinel's WRAPPING, not just
// the status code the handler happens to produce today.
//
// writeFilesErr checks errors.Is(err, ErrNoAccess) explicitly and first, so the
// handler would behave identically no matter what ErrNoAccess wrapped — which
// means inverting the wrap is invisible to every route test. It was: flipping it
// to wrap ErrForbidden left the whole suite green.
//
// The wrap is what makes this fail CLOSED everywhere else. Any code that asks
// the generic question "is this a not-found?" — other services, a future
// handler, a caller that has not learned about ErrNoAccess — must treat "you
// have no access" as "there is nothing here", and must never fall through to a
// branch that reveals the node exists.
func TestErrNoAccessIsFailClosedByWrapping(t *testing.T) {
	if !errors.Is(ErrNoAccess, ErrNotFound) {
		t.Error("ErrNoAccess must wrap ErrNotFound: a generic not-found check has to hide the node, " +
			"otherwise any code path that does not know about ErrNoAccess leaks its existence")
	}
	if errors.Is(ErrNoAccess, ErrForbidden) {
		t.Error("ErrNoAccess must NOT wrap ErrForbidden: that turns every generic forbidden check " +
			"back into an existence oracle — the exact bug this sentinel was introduced to remove")
	}
	// And the two must stay distinguishable, or the handler cannot keep giving
	// a genuine viewer-lacking-write an honest 403.
	if errors.Is(ErrForbidden, ErrNoAccess) {
		t.Error("ErrForbidden must not satisfy ErrNoAccess, or insufficient-role would 404 too")
	}
}
