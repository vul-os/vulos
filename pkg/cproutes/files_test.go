// routes_files_test.go — the cloud Files wire contract.
//
// The box shell's Files surface already speaks this API against the OS box and
// must work UNCHANGED against the CP. So
// these tests assert the wire, not just the behaviour: exact JSON field names,
// the exact grant shape, the listing order, and — the bug that motivated the
// whole surface — that an EMPTY Drive is a 200 with an empty list, never the
// 404/503 that flips the client to "Files isn't available".
package cproutes

// COORDINATOR: depends on the storage test harness (storageTestEnv,
// newStorageTestEnv / newStorageEnvWith, sessionFor, byoStatusResponse) defined
// in routes_storage_test.go — that harness (and its call to RegisterFiles /
// RegisterAccountExport) must land in package cproutes for this test to compile.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/vul-os/vulos-management/pkg/auth"
	"github.com/vul-os/vulos-management/pkg/files"
	"github.com/vul-os/vulos-management/pkg/storage"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

func newFilesTestEnv(t *testing.T) *storageTestEnv {
	t.Helper()
	return newStorageEnvWith(t, nil)
}

// filesUser signs a user up and returns their id + session token. Emails are
// per-test because buildAuthStore's SQLite DB is shared across the package.
func filesUser(t *testing.T, e *storageTestEnv, who string) (userID, token string) {
	t.Helper()
	return sessionFor(t, e.authSt, "files-"+who+"-"+strings.ToLower(t.Name())+"@example.com")
}

// filesGrantEnvelope is the {"node":…,"grant":…} the client destructures.
type filesGrantEnvelope struct {
	Node  files.Node        `json:"node"`
	Grant files.ObjectGrant `json:"grant"`
}

// uploadThroughAPI runs the client's own sequence: upload-grant → PUT the bytes
// straight at the bucket the grant names → commit.
func uploadThroughAPI(t *testing.T, e *storageTestEnv, tok, parentID, name, contentType, body string) (files.Node, files.Version) {
	t.Helper()
	w := e.do(t, http.MethodPost, "/api/files/upload-grant", map[string]any{
		"parent_id": parentID, "name": name, "content_type": contentType,
	}, tok)
	if w.Code != http.StatusOK {
		t.Fatalf("upload-grant %s: status %d: %s", name, w.Code, w.Body.String())
	}
	var env filesGrantEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("upload-grant decode: %v", err)
	}
	// The browser PUTs the presigned URL; MemProvider is the bucket behind it.
	if err := e.mem.PutObject(context.Background(), env.Grant.Bucket, env.Grant.Key,
		bytes.NewReader([]byte(body)), int64(len(body))); err != nil {
		t.Fatalf("PUT bytes: %v", err)
	}
	w = e.do(t, http.MethodPost, "/api/files/commit", map[string]any{
		"node_id": env.Node.ID, "size": len(body), "content_type": contentType, "etag": "etag-" + name,
	}, tok)
	if w.Code != http.StatusOK {
		t.Fatalf("commit %s: status %d: %s", name, w.Code, w.Body.String())
	}
	var v files.Version
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatalf("commit decode: %v", err)
	}
	return env.Node, v
}

func createFolderThroughAPI(t *testing.T, e *storageTestEnv, tok, parentID, name string) files.Node {
	t.Helper()
	w := e.do(t, http.MethodPost, "/api/files/folder", map[string]any{
		"parent_id": parentID, "name": name,
	}, tok)
	if w.Code != http.StatusOK {
		t.Fatalf("folder %s: status %d: %s", name, w.Code, w.Body.String())
	}
	var n files.Node
	if err := json.Unmarshal(w.Body.Bytes(), &n); err != nil {
		t.Fatalf("folder decode: %v", err)
	}
	return n
}

func listThroughAPI(t *testing.T, e *storageTestEnv, tok, parentID string) []files.Node {
	t.Helper()
	w := e.do(t, http.MethodGet, "/api/files/list?parent="+parentID, nil, tok)
	if w.Code != http.StatusOK {
		t.Fatalf("list: status %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Nodes []files.Node `json:"nodes"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("list decode: %v", err)
	}
	return resp.Nodes
}

// ---------------------------------------------------------------------------
// Availability: an empty Drive is NOT an unavailable Drive
// ---------------------------------------------------------------------------

// isUnavailable(err) in filesClient.js is `status === 404 || 503`, and it flips
// the whole surface to "Files isn't available". A brand-new account has an empty
// Drive; that MUST be a 200 with an empty list.
func TestFiles_EmptyRootIsTwoHundredWithAnEmptyList(t *testing.T) {
	e := newFilesTestEnv(t)
	_, tok := filesUser(t, e, "alice")

	w := e.do(t, http.MethodGet, "/api/files/list?parent=", nil, tok)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d, want 200: %s", w.Code, w.Body.String())
	}
	if got := strings.TrimSpace(w.Body.String()); got != `{"nodes":[]}` {
		t.Errorf("body = %s, want {\"nodes\":[]}", got)
	}
}

// ---------------------------------------------------------------------------
// Round-trip: grant → bytes → commit
// ---------------------------------------------------------------------------

func TestFiles_UploadRoundTrip(t *testing.T) {
	e := newFilesTestEnv(t)
	uid, tok := filesUser(t, e, "alice")

	w := e.do(t, http.MethodPost, "/api/files/upload-grant", map[string]any{
		"parent_id": "", "name": "report.txt", "content_type": "text/plain",
	}, tok)
	if w.Code != http.StatusOK {
		t.Fatalf("upload-grant: status %d: %s", w.Code, w.Body.String())
	}

	// The grant is exactly what the client destructures: a presigned PUT.
	var raw struct {
		Grant map[string]any `json:"grant"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for k, want := range map[string]any{"type": "presigned", "method": "PUT"} {
		if raw.Grant[k] != want {
			t.Errorf("grant[%q] = %v, want %v", k, raw.Grant[k], want)
		}
	}
	for _, k := range []string{"bucket", "key", "url", "expires_at"} {
		if v, ok := raw.Grant[k].(string); !ok || v == "" {
			t.Errorf("grant[%q] missing/empty", k)
		}
	}

	var env filesGrantEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// The key is derived server-side, under the Drive prefix — NOT the per-app
	// "<account>/files/" prefix an app token can reach.
	if want := uid + "/drive/report.txt"; env.Node.ObjectKey != want {
		t.Errorf("object key = %q, want %q", env.Node.ObjectKey, want)
	}
	if env.Grant.Key != env.Node.ObjectKey || env.Grant.Bucket != env.Node.Bucket {
		t.Errorf("grant does not name the node's own object: %+v", env.Grant)
	}

	// The pending node is listed immediately, before any byte lands.
	pending := listThroughAPI(t, e, tok, "")
	if len(pending) != 1 || pending[0].ID != env.Node.ID || pending[0].Size != 0 {
		t.Fatalf("pending node not listed with size 0: %+v", pending)
	}

	body := "hello drive"
	if err := e.mem.PutObject(context.Background(), env.Grant.Bucket, env.Grant.Key,
		bytes.NewReader([]byte(body)), int64(len(body))); err != nil {
		t.Fatalf("PUT bytes: %v", err)
	}
	w = e.do(t, http.MethodPost, "/api/files/commit", map[string]any{
		"node_id": env.Node.ID, "size": len(body), "content_type": "text/plain", "etag": "abc123",
	}, tok)
	if w.Code != http.StatusOK {
		t.Fatalf("commit: status %d: %s", w.Code, w.Body.String())
	}
	var v files.Version
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatalf("commit decode: %v", err)
	}
	if v.NodeID != env.Node.ID || v.VersionKey != env.Node.ObjectKey || v.Size != int64(len(body)) || v.ETag != "abc123" || v.CreatedBy != uid {
		t.Errorf("version = %+v", v)
	}

	after := listThroughAPI(t, e, tok, "")
	if len(after) != 1 {
		t.Fatalf("listing after commit: %+v", after)
	}
	if after[0].Size != int64(len(body)) || after[0].ContentType != "text/plain" || after[0].CurrentVersionID != v.ID {
		t.Errorf("node not finalized by commit: %+v", after[0])
	}
}

func TestFiles_DownloadGrantIsAPresignedGET(t *testing.T) {
	e := newFilesTestEnv(t)
	_, tok := filesUser(t, e, "alice")
	n, _ := uploadThroughAPI(t, e, tok, "", "doc.txt", "text/plain", "payload")

	w := e.do(t, http.MethodPost, "/api/files/download-grant", map[string]any{"node_id": n.ID}, tok)
	if w.Code != http.StatusOK {
		t.Fatalf("download-grant: status %d: %s", w.Code, w.Body.String())
	}
	var env filesGrantEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.Grant.Type != files.GrantPresigned || env.Grant.Method != "GET" || env.Grant.URL == "" {
		t.Errorf("grant = %+v, want a presigned GET with a URL", env.Grant)
	}
	if env.Grant.Key != n.ObjectKey {
		t.Errorf("grant key = %q, want %q", env.Grant.Key, n.ObjectKey)
	}
}

func TestFiles_DownloadGrantForAFolderIsRejected(t *testing.T) {
	e := newFilesTestEnv(t)
	_, tok := filesUser(t, e, "alice")
	dir := createFolderThroughAPI(t, e, tok, "", "docs")

	w := e.do(t, http.MethodPost, "/api/files/download-grant", map[string]any{"node_id": dir.ID}, tok)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "folders have no bytes") {
		t.Errorf("body = %s", w.Body.String())
	}
}

// ---------------------------------------------------------------------------
// Folders are metadata-only
// ---------------------------------------------------------------------------

func TestFiles_FolderWritesNoObject(t *testing.T) {
	e := newFilesTestEnv(t)
	uid, tok := filesUser(t, e, "alice")
	dir := createFolderThroughAPI(t, e, tok, "", "docs")

	if dir.ObjectKey != "" || dir.Bucket != "" || !dir.IsDir {
		t.Errorf("folder node = %+v, want a bytes-less dir", dir)
	}
	// Not even a zero-byte marker: the bucket does not exist yet at all, because
	// nothing has ever been written to it.
	bucket := "vulos-" + strings.ToLower(boxULID(uid))
	objs, err := e.mem.ListBucket(context.Background(), bucket, "", 0)
	if err == nil && len(objs) != 0 {
		t.Errorf("folder created %d object(s): %+v", len(objs), objs)
	}
}

// ---------------------------------------------------------------------------
// Versions
// ---------------------------------------------------------------------------

func TestFiles_ReuploadOverwritesAndAppendsAVersion(t *testing.T) {
	e := newFilesTestEnv(t)
	_, tok := filesUser(t, e, "alice")
	first, _ := uploadThroughAPI(t, e, tok, "", "notes.txt", "text/plain", "v1")
	second, _ := uploadThroughAPI(t, e, tok, "", "notes.txt", "text/plain", "v2-longer")

	if first.ID != second.ID || first.ObjectKey != second.ObjectKey {
		t.Fatalf("re-upload made a new node/key: %+v vs %+v", first, second)
	}
	w := e.do(t, http.MethodGet, "/api/files/versions?node="+first.ID, nil, tok)
	if w.Code != http.StatusOK {
		t.Fatalf("versions: status %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Versions []files.Version `json:"versions"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Versions) != 2 {
		t.Fatalf("got %d versions, want 2", len(resp.Versions))
	}
	// Newest first, and every version points at the SAME key: history is
	// metadata, the bytes were overwritten.
	if resp.Versions[0].CreatedAt.Before(resp.Versions[1].CreatedAt) {
		t.Errorf("versions are not newest-first")
	}
	if resp.Versions[0].VersionKey != first.ObjectKey || resp.Versions[1].VersionKey != first.ObjectKey {
		t.Errorf("versions do not share the node's object key: %+v", resp.Versions)
	}
}

// ---------------------------------------------------------------------------
// Listing
// ---------------------------------------------------------------------------

func TestFiles_ListOrderIsFoldersThenNames(t *testing.T) {
	e := newFilesTestEnv(t)
	_, tok := filesUser(t, e, "alice")
	uploadThroughAPI(t, e, tok, "", "b.txt", "text/plain", "b")
	uploadThroughAPI(t, e, tok, "", "a.txt", "text/plain", "a")
	createFolderThroughAPI(t, e, tok, "", "zeta")
	createFolderThroughAPI(t, e, tok, "", "alpha")

	var got []string
	for _, n := range listThroughAPI(t, e, tok, "") {
		got = append(got, n.Name)
	}
	if want := "alpha,zeta,a.txt,b.txt"; strings.Join(got, ",") != want {
		t.Errorf("order = %v, want %s", got, want)
	}
}

// ---------------------------------------------------------------------------
// Node JSON shape
// ---------------------------------------------------------------------------

func TestFiles_NodeJSONFieldNamesAndTimestamps(t *testing.T) {
	e := newFilesTestEnv(t)
	uid, tok := filesUser(t, e, "alice")
	n, _ := uploadThroughAPI(t, e, tok, "", "shape.txt", "text/plain", "x")

	w := e.do(t, http.MethodPost, "/api/files/download-grant", map[string]any{"node_id": n.ID}, tok)
	var raw struct {
		Node map[string]any `json:"node"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, k := range []string{"id", "owner_id", "parent_id", "name", "is_dir", "bucket",
		"object_key", "path", "size", "content_type", "current_version_id", "created_at", "updated_at"} {
		if _, ok := raw.Node[k]; !ok {
			t.Errorf("node JSON is missing %q", k)
		}
	}
	if raw.Node["owner_id"] != uid {
		t.Errorf("owner_id = %v, want %s", raw.Node["owner_id"], uid)
	}
	if _, err := time.Parse(time.RFC3339Nano, raw.Node["created_at"].(string)); err != nil {
		t.Errorf("created_at is not RFC3339Nano: %v", raw.Node["created_at"])
	}
}

// ---------------------------------------------------------------------------
// Name validation
// ---------------------------------------------------------------------------

func TestFiles_NameValidation(t *testing.T) {
	e := newFilesTestEnv(t)
	_, tok := filesUser(t, e, "alice")

	for _, name := range []string{"", strings.Repeat("a", 256), ".", "..", "a/b", "a\\b", "a\x00b"} {
		w := e.do(t, http.MethodPost, "/api/files/folder", map[string]any{"parent_id": "", "name": name}, tok)
		if w.Code != http.StatusBadRequest {
			t.Errorf("folder %q: status %d, want 400", name, w.Code)
		}
		w = e.do(t, http.MethodPost, "/api/files/upload-grant",
			map[string]any{"parent_id": "", "name": name, "content_type": "text/plain"}, tok)
		if w.Code != http.StatusBadRequest {
			t.Errorf("upload-grant %q: status %d, want 400", name, w.Code)
		}
	}
}

func TestFiles_DuplicateSiblingIsRejected(t *testing.T) {
	e := newFilesTestEnv(t)
	_, tok := filesUser(t, e, "alice")
	createFolderThroughAPI(t, e, tok, "", "docs")

	w := e.do(t, http.MethodPost, "/api/files/folder", map[string]any{"parent_id": "", "name": "docs"}, tok)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "name already exists") {
		t.Errorf("duplicate folder: status %d body %s", w.Code, w.Body.String())
	}
	// upload-grant onto a FOLDER of the same name is a 400 too (it is not a file
	// to overwrite) — distinct from re-uploading over an existing file, which is
	// the intended overwrite path.
	w = e.do(t, http.MethodPost, "/api/files/upload-grant",
		map[string]any{"parent_id": "", "name": "docs", "content_type": "text/plain"}, tok)
	if w.Code != http.StatusBadRequest {
		t.Errorf("upload-grant onto a folder: status %d, want 400", w.Code)
	}
}

// ---------------------------------------------------------------------------
// Move / rename
// ---------------------------------------------------------------------------

func TestFiles_RenameRekeysTheObject(t *testing.T) {
	e := newFilesTestEnv(t)
	_, tok := filesUser(t, e, "alice")
	n, _ := uploadThroughAPI(t, e, tok, "", "old.txt", "text/plain", "payload")
	oldKey := n.ObjectKey

	w := e.do(t, http.MethodPost, "/api/files/move", map[string]any{
		"node_id": n.ID, "new_parent_id": "", "new_name": "new.txt",
	}, tok)
	if w.Code != http.StatusOK {
		t.Fatalf("move: status %d: %s", w.Code, w.Body.String())
	}
	var moved files.Node
	if err := json.Unmarshal(w.Body.Bytes(), &moved); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !strings.HasSuffix(moved.ObjectKey, "/drive/new.txt") {
		t.Errorf("object key = %q, want …/drive/new.txt", moved.ObjectKey)
	}
	ctx := context.Background()
	if _, err := e.mem.GetObject(ctx, moved.Bucket, oldKey); err == nil {
		t.Errorf("bytes still at the old key %q", oldKey)
	}
	rc, err := e.mem.GetObject(ctx, moved.Bucket, moved.ObjectKey)
	if err != nil {
		t.Fatalf("bytes did not follow the rename: %v", err)
	}
	_ = rc.Close()
}

func TestFiles_MoveFolderRekeysDescendants(t *testing.T) {
	e := newFilesTestEnv(t)
	_, tok := filesUser(t, e, "alice")
	docs := createFolderThroughAPI(t, e, tok, "", "docs")
	deep := createFolderThroughAPI(t, e, tok, docs.ID, "deep")
	archive := createFolderThroughAPI(t, e, tok, "", "archive")
	nested, _ := uploadThroughAPI(t, e, tok, deep.ID, "nested.txt", "text/plain", "N")

	w := e.do(t, http.MethodPost, "/api/files/move", map[string]any{
		"node_id": docs.ID, "new_parent_id": archive.ID, "new_name": "",
	}, tok)
	if w.Code != http.StatusOK {
		t.Fatalf("move: status %d: %s", w.Code, w.Body.String())
	}

	kids := listThroughAPI(t, e, tok, deep.ID)
	if len(kids) != 1 {
		t.Fatalf("descendant listing: %+v", kids)
	}
	if kids[0].Path != "archive/docs/deep/nested.txt" {
		t.Errorf("descendant path = %q", kids[0].Path)
	}
	if !strings.HasSuffix(kids[0].ObjectKey, "/drive/archive/docs/deep/nested.txt") {
		t.Errorf("descendant key = %q", kids[0].ObjectKey)
	}
	ctx := context.Background()
	if _, err := e.mem.GetObject(ctx, nested.Bucket, nested.ObjectKey); err == nil {
		t.Errorf("bytes still at the pre-move key %q", nested.ObjectKey)
	}
	rc, err := e.mem.GetObject(ctx, kids[0].Bucket, kids[0].ObjectKey)
	if err != nil {
		t.Fatalf("descendant bytes not relocated: %v", err)
	}
	_ = rc.Close()
}

func TestFiles_MoveRejections(t *testing.T) {
	e := newFilesTestEnv(t)
	_, tok := filesUser(t, e, "alice")
	docs := createFolderThroughAPI(t, e, tok, "", "docs")
	deep := createFolderThroughAPI(t, e, tok, docs.ID, "deep")
	a, _ := uploadThroughAPI(t, e, tok, "", "a.txt", "text/plain", "A")
	uploadThroughAPI(t, e, tok, "", "b.txt", "text/plain", "B")

	for _, tc := range []struct {
		name string
		body map[string]any
	}{
		{"into own subtree", map[string]any{"node_id": docs.ID, "new_parent_id": deep.ID}},
		{"onto a sibling name", map[string]any{"node_id": a.ID, "new_parent_id": "", "new_name": "b.txt"}},
		{"under a file", map[string]any{"node_id": docs.ID, "new_parent_id": a.ID}},
	} {
		w := e.do(t, http.MethodPost, "/api/files/move", tc.body, tok)
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400 (%s)", tc.name, w.Code, w.Body.String())
		}
	}
}

// ---------------------------------------------------------------------------
// Delete
// ---------------------------------------------------------------------------

func TestFiles_DeleteIsSoftAndHidesDescendants(t *testing.T) {
	e := newFilesTestEnv(t)
	_, tok := filesUser(t, e, "alice")
	docs := createFolderThroughAPI(t, e, tok, "", "docs")
	child, _ := uploadThroughAPI(t, e, tok, docs.ID, "inside.txt", "text/plain", "I")

	w := e.do(t, http.MethodPost, "/api/files/delete", map[string]any{"node_id": docs.ID}, tok)
	if w.Code != http.StatusOK {
		t.Fatalf("delete: status %d: %s", w.Code, w.Body.String())
	}
	if got := strings.TrimSpace(w.Body.String()); got != `{"status":"deleted"}` {
		t.Errorf("body = %s", got)
	}
	if nodes := listThroughAPI(t, e, tok, ""); len(nodes) != 0 {
		t.Errorf("deleted folder still listed: %+v", nodes)
	}
	if w := e.do(t, http.MethodGet, "/api/files/list?parent="+docs.ID, nil, tok); w.Code != http.StatusNotFound {
		t.Errorf("listing a deleted folder: status %d, want 404", w.Code)
	}
	// Soft delete frees no bytes — the purge sweep does that, 30 days later.
	rc, err := e.mem.GetObject(context.Background(), child.Bucket, child.ObjectKey)
	if err != nil {
		t.Errorf("soft delete freed the bytes: %v", err)
	} else {
		_ = rc.Close()
	}
}

// ---------------------------------------------------------------------------
// Quota
// ---------------------------------------------------------------------------

// The write path runs the SAME single storage-quota gate as /api/storage/presign
// (BILLING-LOCATION-01). Over cap ⇒ 402, and — because the gate runs BEFORE the
// find-or-create — no pending node is left behind.
func TestFiles_UploadGrantOverQuotaIsRefused(t *testing.T) {
	e := newStorageEnvWith(t, alwaysOverQuotaResolver{})
	uid, tok := filesUser(t, e, "alice")

	// A usage sample far past any tier's storage allowance.
	if err := e.svc.InsertUsage(context.Background(), storage.UsageSample{
		AccountID: uid,
		Bucket:    "vulos-" + strings.ToLower(boxULID(uid)),
		SizeBytes: 100 << 40, // 100 TiB
		SampledAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("InsertUsage: %v", err)
	}

	w := e.do(t, http.MethodPost, "/api/files/upload-grant", map[string]any{
		"parent_id": "", "name": "too-big.bin", "content_type": "application/octet-stream",
	}, tok)
	if w.Code != http.StatusPaymentRequired {
		t.Fatalf("status %d, want 402: %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "url") {
		t.Errorf("a grant was minted for an over-quota account: %s", w.Body.String())
	}
	if nodes := listThroughAPI(t, e, tok, ""); len(nodes) != 0 {
		t.Errorf("a refused upload left %d node(s) behind: %+v", len(nodes), nodes)
	}
}

// ---------------------------------------------------------------------------
// Service unavailable
// ---------------------------------------------------------------------------

// The 503 the client's isUnavailable() looks for is reserved for exactly one
// case: the Files index could not be opened. There is no MemStore fallback — an
// index that forgets the tree while the bytes persist is worse than an outage.
func TestFiles_NilServiceIsFiveOhThree(t *testing.T) {
	e := newFilesTestEnv(t)
	_, tok := filesUser(t, e, "alice")

	mux := http.NewServeMux()
	RegisterFiles(mux, &storageHandlers{svc: e.svc, auth: e.authSt}, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/files/list?parent=", nil)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: tok})
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d, want 503: %s", w.Code, w.Body.String())
	}
}
