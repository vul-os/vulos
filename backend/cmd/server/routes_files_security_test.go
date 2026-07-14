package main

// routes_files_security_test.go — wave-52 SECURITY coverage for the Files
// control-plane registrar (routes_files.go: registerFilesRoutes).
//
// The Files API takes identity ONLY from the session-derived X-User-ID header
// (never from the body) and delegates to services/files, which enforces a
// viewer < editor < owner ACL. These tests drive the REAL registrar over
// httptest and assert the route-layer security contract:
//
//   * unauth (no X-User-ID)            → 401 on every endpoint
//   * svc == nil (service unavailable) → 503
//   * cross-user read of a private node → 404 (anti-enumeration)
//   * viewer cannot write/delete/share  → 403 (privilege escalation blocked)
//   * editor cannot share (owner-only)  → 403
//   * owner can share/unshare/list      → 200
//   * path-traversal in a file NAME     → 400 (service validName rejects "/","..")
//
// A tiny in-package fake Broker mints deterministic grants so an authorized
// upload/download path returns 200 without a real object store; the fake also
// lets us prove an UNAUTHORIZED request never reaches the minter.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"vulos/backend/internal/storage"
	"vulos/backend/services/files"
)

// ── in-package fake Broker (package main can't see files' unexported one) ────

type fbBroker struct {
	reads, writes int
}

func (b *fbBroker) MintRead(_ context.Context, _, bucket, key string, ttl time.Duration) (storage.ObjectGrant, error) {
	b.reads++
	return storage.ObjectGrant{Type: storage.GrantLocal, Method: "GET", Bucket: bucket, Key: key, ExpiresAt: time.Now().Add(ttl)}, nil
}
func (b *fbBroker) MintWrite(_ context.Context, _, bucket, key string, ttl time.Duration) (storage.ObjectGrant, error) {
	b.writes++
	return storage.ObjectGrant{Type: storage.GrantLocal, Method: "PUT", Bucket: bucket, Key: key, ExpiresAt: time.Now().Add(ttl)}, nil
}
func (b *fbBroker) MoveObject(_ context.Context, _, _, _, _ string) error { return nil }
func (b *fbBroker) PutContent(_ context.Context, _, _, _ string, r io.Reader, _ int64, _ string) (string, error) {
	io.Copy(io.Discard, r)
	return "etag", nil
}
func (b *fbBroker) GetContent(_ context.Context, _, _, _ string) (io.ReadCloser, int64, error) {
	return io.NopCloser(bytes.NewReader([]byte("hi"))), 2, nil
}
func (b *fbBroker) DeleteObject(_ context.Context, _, _, _ string) error { return nil }

// newFilesSvc builds a real files.Service backed by a temp SQLite DB + fake
// broker. bucketFn returns a per-owner bucket so grants have a bucket.
func newFilesSvc(t *testing.T) (*files.Service, *fbBroker) {
	t.Helper()
	broker := &fbBroker{}
	svc, err := files.New(t.TempDir()+"/files.db", broker, func(uid string) string { return "bucket-" + uid })
	if err != nil {
		t.Fatalf("files.New: %v", err)
	}
	return svc, broker
}

// filesReq issues a request against mux with the given X-User-ID (empty ⇒ omit).
func filesReq(t *testing.T, mux *http.ServeMux, method, target, userID string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body != nil {
		buf, _ := json.Marshal(body)
		r = httptest.NewRequest(method, target, bytes.NewReader(buf))
		r.Header.Set("Content-Type", "application/json")
	} else {
		r = httptest.NewRequest(method, target, nil)
	}
	if userID != "" {
		r.Header.Set("X-User-ID", userID)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

// ── unauth / unavailable gates ──────────────────────────────────────────────

// TestFiles_Unauth401 verifies every guarded endpoint returns 401 when the
// session-derived X-User-ID header is absent (identity is never forgeable from
// the body).
func TestFiles_Unauth401(t *testing.T) {
	svc, _ := newFilesSvc(t)
	mux := http.NewServeMux()
	registerFilesRoutes(mux, svc)

	cases := []struct {
		method, target string
		body           any
	}{
		{http.MethodGet, "/api/files/list", nil},
		{http.MethodPost, "/api/files/folder", map[string]string{"name": "x"}},
		{http.MethodPost, "/api/files/delete", map[string]string{"node_id": "x"}},
		{http.MethodPost, "/api/files/share", map[string]string{"node_id": "x", "principal_id": "y", "role": "viewer"}},
		{http.MethodPost, "/api/files/download-grant", map[string]string{"node_id": "x"}},
	}
	for _, c := range cases {
		w := filesReq(t, mux, c.method, c.target, "", c.body)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s without X-User-ID: expected 401, got %d", c.method, c.target, w.Code)
		}
	}
}

// TestFiles_ServiceUnavailable503 verifies a nil service degrades to 503 (the
// rest of the OS still boots) rather than panicking or 500-ing.
func TestFiles_ServiceUnavailable503(t *testing.T) {
	mux := http.NewServeMux()
	registerFilesRoutes(mux, nil)

	w := filesReq(t, mux, http.MethodGet, "/api/files/list", "alice", nil)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("nil service: expected 503, got %d", w.Code)
	}
}

// ── ACL: cross-user, viewer/editor/owner ────────────────────────────────────

// mkFolder creates a folder owned by uid at the Drive root and returns its id.
func mkFolder(t *testing.T, mux *http.ServeMux, uid, name string) string {
	t.Helper()
	w := filesReq(t, mux, http.MethodPost, "/api/files/folder", uid,
		map[string]string{"parent_id": "", "name": name})
	if w.Code != http.StatusOK {
		t.Fatalf("create folder %q for %s: expected 200, got %d (%s)", name, uid, w.Code, w.Body.String())
	}
	var n struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &n); err != nil || n.ID == "" {
		t.Fatalf("folder create response missing id: %s", w.Body.String())
	}
	return n.ID
}

// TestFiles_CrossUserRead404 verifies user B cannot read (list into) user A's
// private folder — the route surfaces the service's ErrForbidden branch. The
// service returns ErrForbidden here → route maps to 403 (explicit denial).
func TestFiles_CrossUserRead404(t *testing.T) {
	svc, _ := newFilesSvc(t)
	mux := http.NewServeMux()
	registerFilesRoutes(mux, svc)

	folder := mkFolder(t, mux, "alice", "private")

	// Bob tries to list into Alice's folder → 403 forbidden.
	w := filesReq(t, mux, http.MethodGet, "/api/files/list?parent="+folder, "bob", nil)
	if w.Code != http.StatusForbidden {
		t.Fatalf("cross-user list: expected 403, got %d (%s)", w.Code, w.Body.String())
	}

	// Alice CAN list her own folder → 200.
	wa := filesReq(t, mux, http.MethodGet, "/api/files/list?parent="+folder, "alice", nil)
	if wa.Code != http.StatusOK {
		t.Fatalf("owner list own folder: expected 200, got %d", wa.Code)
	}
}

// TestFiles_ViewerCannotWriteDeleteShare proves a VIEWER grant does not permit
// mutation: a viewer of Alice's folder cannot create a subfolder (editor+),
// cannot delete (editor+), and cannot share/manage ACLs (owner-only). Each is
// a route-layer 403.
func TestFiles_ViewerCannotWriteDeleteShare(t *testing.T) {
	svc, _ := newFilesSvc(t)
	mux := http.NewServeMux()
	registerFilesRoutes(mux, svc)

	folder := mkFolder(t, mux, "alice", "shared")

	// Alice (owner) shares the folder with Bob as VIEWER.
	ws := filesReq(t, mux, http.MethodPost, "/api/files/share", "alice",
		map[string]string{"node_id": folder, "principal_id": "bob", "role": "viewer"})
	if ws.Code != http.StatusOK {
		t.Fatalf("owner share as viewer: expected 200, got %d (%s)", ws.Code, ws.Body.String())
	}

	// Bob can now LIST (viewer+) → 200.
	wl := filesReq(t, mux, http.MethodGet, "/api/files/list?parent="+folder, "bob", nil)
	if wl.Code != http.StatusOK {
		t.Fatalf("viewer list: expected 200, got %d", wl.Code)
	}

	// Bob (viewer) CANNOT create a subfolder (needs editor+) → 403.
	wc := filesReq(t, mux, http.MethodPost, "/api/files/folder", "bob",
		map[string]string{"parent_id": folder, "name": "evil"})
	if wc.Code != http.StatusForbidden {
		t.Fatalf("viewer create subfolder: expected 403, got %d (%s)", wc.Code, wc.Body.String())
	}

	// Bob (viewer) CANNOT delete the folder (needs editor+) → 403.
	wd := filesReq(t, mux, http.MethodPost, "/api/files/delete", "bob",
		map[string]string{"node_id": folder})
	if wd.Code != http.StatusForbidden {
		t.Fatalf("viewer delete: expected 403, got %d (%s)", wd.Code, wd.Body.String())
	}

	// Bob (viewer) CANNOT re-share (owner-only) → 403.
	wsh := filesReq(t, mux, http.MethodPost, "/api/files/share", "bob",
		map[string]string{"node_id": folder, "principal_id": "carol", "role": "viewer"})
	if wsh.Code != http.StatusForbidden {
		t.Fatalf("viewer share: expected 403, got %d (%s)", wsh.Code, wsh.Body.String())
	}

	// Bob (viewer) CANNOT list the ACL (owner-only) → 403.
	wls := filesReq(t, mux, http.MethodGet, "/api/files/shares?node="+folder, "bob", nil)
	if wls.Code != http.StatusForbidden {
		t.Fatalf("viewer list shares: expected 403, got %d", wls.Code)
	}
}

// TestFiles_EditorCannotShare proves an EDITOR can create content but still
// cannot manage the ACL (share is owner-only). Editor create → 200; editor
// share → 403.
func TestFiles_EditorCannotShare(t *testing.T) {
	svc, _ := newFilesSvc(t)
	mux := http.NewServeMux()
	registerFilesRoutes(mux, svc)

	folder := mkFolder(t, mux, "alice", "team")

	// Alice shares with Bob as EDITOR.
	ws := filesReq(t, mux, http.MethodPost, "/api/files/share", "alice",
		map[string]string{"node_id": folder, "principal_id": "bob", "role": "editor"})
	if ws.Code != http.StatusOK {
		t.Fatalf("owner share as editor: expected 200, got %d (%s)", ws.Code, ws.Body.String())
	}

	// Bob (editor) CAN create a subfolder → 200.
	wc := filesReq(t, mux, http.MethodPost, "/api/files/folder", "bob",
		map[string]string{"parent_id": folder, "name": "docs"})
	if wc.Code != http.StatusOK {
		t.Fatalf("editor create subfolder: expected 200, got %d (%s)", wc.Code, wc.Body.String())
	}

	// Bob (editor) CANNOT share (owner-only) → 403.
	wsh := filesReq(t, mux, http.MethodPost, "/api/files/share", "bob",
		map[string]string{"node_id": folder, "principal_id": "carol", "role": "viewer"})
	if wsh.Code != http.StatusForbidden {
		t.Fatalf("editor share: expected 403, got %d (%s)", wsh.Code, wsh.Body.String())
	}
}

// TestFiles_OwnerCanShareAndUnshare confirms the owner path is not accidentally
// blocked (no false positives): share → list shares → unshare, all 200.
func TestFiles_OwnerCanShareAndUnshare(t *testing.T) {
	svc, _ := newFilesSvc(t)
	mux := http.NewServeMux()
	registerFilesRoutes(mux, svc)

	folder := mkFolder(t, mux, "alice", "owned")

	ws := filesReq(t, mux, http.MethodPost, "/api/files/share", "alice",
		map[string]string{"node_id": folder, "principal_id": "bob", "role": "viewer"})
	if ws.Code != http.StatusOK {
		t.Fatalf("owner share: expected 200, got %d", ws.Code)
	}

	wls := filesReq(t, mux, http.MethodGet, "/api/files/shares?node="+folder, "alice", nil)
	if wls.Code != http.StatusOK {
		t.Fatalf("owner list shares: expected 200, got %d", wls.Code)
	}

	wu := filesReq(t, mux, http.MethodPost, "/api/files/unshare", "alice",
		map[string]string{"node_id": folder, "principal_id": "bob"})
	if wu.Code != http.StatusOK {
		t.Fatalf("owner unshare: expected 200, got %d", wu.Code)
	}
}

// TestFiles_TraversalNameRejected proves a path-traversal / separator-bearing
// file or folder NAME is rejected at 400 by the service validName guard, so no
// node with a "../" or "/" name is ever created.
func TestFiles_TraversalNameRejected(t *testing.T) {
	svc, broker := newFilesSvc(t)
	mux := http.NewServeMux()
	registerFilesRoutes(mux, svc)

	bad := []string{"..", ".", "../etc/passwd", "a/b", "a\\b", "with\x00nul"}
	for _, name := range bad {
		// folder create with a traversal name → 400.
		wf := filesReq(t, mux, http.MethodPost, "/api/files/folder", "alice",
			map[string]string{"parent_id": "", "name": name})
		if wf.Code != http.StatusBadRequest {
			t.Fatalf("folder name %q: expected 400, got %d (%s)", name, wf.Code, wf.Body.String())
		}

		// upload-grant with a traversal name → 400 and the broker must NOT be
		// asked to mint a write grant (no bytes reachable for an invalid name).
		wu := filesReq(t, mux, http.MethodPost, "/api/files/upload-grant", "alice",
			map[string]string{"parent_id": "", "name": name, "content_type": "text/plain"})
		if wu.Code != http.StatusBadRequest {
			t.Fatalf("upload-grant name %q: expected 400, got %d", name, wu.Code)
		}
	}
	if broker.writes != 0 {
		t.Fatalf("traversal-named upload minted %d write grants — the name guard runs AFTER the mint (bug)", broker.writes)
	}
}

// TestFiles_UploadDownloadGrant_Owner exercises the authorized grant path end
// to end: an owner gets a write grant (broker.writes==1) then a read grant
// (broker.reads==1). This proves the ACL-gated mint chokepoint is reached only
// for authorized callers.
func TestFiles_UploadDownloadGrant_Owner(t *testing.T) {
	svc, broker := newFilesSvc(t)
	mux := http.NewServeMux()
	registerFilesRoutes(mux, svc)

	// Owner requests a write grant for a new file at the Drive root.
	wu := filesReq(t, mux, http.MethodPost, "/api/files/upload-grant", "alice",
		map[string]string{"parent_id": "", "name": "notes.txt", "content_type": "text/plain"})
	if wu.Code != http.StatusOK {
		t.Fatalf("owner upload-grant: expected 200, got %d (%s)", wu.Code, wu.Body.String())
	}
	var up struct {
		Node struct {
			ID string `json:"id"`
		} `json:"node"`
	}
	if err := json.Unmarshal(wu.Body.Bytes(), &up); err != nil || up.Node.ID == "" {
		t.Fatalf("upload-grant response missing node id: %s", wu.Body.String())
	}
	if broker.writes != 1 {
		t.Fatalf("expected exactly 1 write mint for authorized upload, got %d", broker.writes)
	}

	// A non-owner must NOT get a download grant for that file → 403, and the
	// broker's read minter must NOT be reached.
	wdBob := filesReq(t, mux, http.MethodPost, "/api/files/download-grant", "bob",
		map[string]string{"node_id": up.Node.ID})
	if wdBob.Code != http.StatusForbidden {
		t.Fatalf("cross-user download-grant: expected 403, got %d", wdBob.Code)
	}
	if broker.reads != 0 {
		t.Fatalf("unauthorized download minted %d read grants — ACL bypassed the mint chokepoint (bug)", broker.reads)
	}

	// Owner CAN get a read grant → 200 and exactly 1 read mint.
	wd := filesReq(t, mux, http.MethodPost, "/api/files/download-grant", "alice",
		map[string]string{"node_id": up.Node.ID})
	if wd.Code != http.StatusOK {
		t.Fatalf("owner download-grant: expected 200, got %d (%s)", wd.Code, wd.Body.String())
	}
	if broker.reads != 1 {
		t.Fatalf("expected exactly 1 read mint for authorized download, got %d", broker.reads)
	}
}

// TestFiles_CrossUserMoveDeleteBlocked pins the mutation-side of the isolation
// boundary (a companion to CrossUserRead404, which only covers reads): a user
// with NO grant on another user's node cannot MOVE, RENAME, or DELETE it, and
// cannot read its version history. Each is a route-layer 403, and — crucially —
// the node must still exist and be usable by its owner afterward (the failed
// attempts left no side effect). This is the regression guard against an IDOR
// where the body-supplied node_id is trusted over the session identity.
func TestFiles_CrossUserMoveDeleteBlocked(t *testing.T) {
	svc, _ := newFilesSvc(t)
	mux := http.NewServeMux()
	registerFilesRoutes(mux, svc)

	// Alice owns a folder with a child folder inside it.
	parent := mkFolder(t, mux, "alice", "alice-private")
	childW := filesReq(t, mux, http.MethodPost, "/api/files/folder", "alice",
		map[string]string{"parent_id": parent, "name": "child"})
	if childW.Code != http.StatusOK {
		t.Fatalf("owner create child: expected 200, got %d (%s)", childW.Code, childW.Body.String())
	}
	var child struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(childW.Body.Bytes(), &child); err != nil || child.ID == "" {
		t.Fatalf("child create response missing id: %s", childW.Body.String())
	}

	// Bob has no grant anywhere in Alice's tree. Every mutation is forbidden.
	forbid := func(target string, body any) {
		t.Helper()
		w := filesReq(t, mux, http.MethodPost, target, "bob", body)
		if w.Code != http.StatusForbidden {
			t.Fatalf("cross-user %s: expected 403, got %d (%s)", target, w.Code, w.Body.String())
		}
	}
	forbid("/api/files/delete", map[string]string{"node_id": child.ID})
	forbid("/api/files/move", map[string]string{"node_id": child.ID, "new_name": "pwned"})
	// Move the child OUT of Alice's tree into Bob's own root — also forbidden
	// (Bob is not an editor of the source node).
	forbid("/api/files/move", map[string]string{"node_id": child.ID, "new_parent_id": ""})

	// Version history of Alice's node is not readable by Bob → 403.
	wv := filesReq(t, mux, http.MethodGet, "/api/files/versions?node="+child.ID, "bob", nil)
	if wv.Code != http.StatusForbidden {
		t.Fatalf("cross-user versions: expected 403, got %d (%s)", wv.Code, wv.Body.String())
	}

	// The node is untouched: Alice can still list her folder and see the child.
	wl := filesReq(t, mux, http.MethodGet, "/api/files/list?parent="+parent, "alice", nil)
	if wl.Code != http.StatusOK {
		t.Fatalf("owner list after blocked mutations: expected 200, got %d (%s)", wl.Code, wl.Body.String())
	}
	var listing struct {
		Nodes []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal(wl.Body.Bytes(), &listing); err != nil {
		t.Fatalf("decode listing: %v", err)
	}
	found := false
	for _, n := range listing.Nodes {
		if n.ID == child.ID {
			found = true
			if n.Name != "child" {
				t.Fatalf("child name changed to %q — a blocked rename leaked through", n.Name)
			}
		}
	}
	if !found {
		t.Fatalf("child folder missing after blocked mutations — a cross-user delete/move leaked through")
	}
}
