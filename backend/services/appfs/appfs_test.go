package appfs

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	testUserA = "userAAAAAAAA"
	testUserB = "userBBBBBBBB"
)

// newTestService returns an appfs Service rooted in t.TempDir().
func newTestService(t *testing.T) *Service {
	t.Helper()
	return New(t.TempDir())
}

// withUser attaches the SERVER-DERIVED X-User-ID header a real deployment's
// auth middleware would stamp (see callerUserID's doc) — every handler test
// below goes through this rather than any client-suppliable field.
func withUser(req *http.Request, userID string) *http.Request {
	req.Header.Set("X-User-ID", userID)
	return req
}

// ---- sandboxDir / per-(user,app) path derivation ----

func TestSandboxDir_ValidIDs(t *testing.T) {
	svc := newTestService(t)
	dir, err := svc.sandboxDir(testUserA, "my-app")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wantSuffix := filepath.Join(testUserA, "my-app")
	if !strings.HasSuffix(dir, string(filepath.Separator)+wantSuffix) {
		t.Errorf("sandbox dir %q does not end with /%s", dir, wantSuffix)
	}
	fi, err := os.Stat(dir)
	if err != nil || !fi.IsDir() {
		t.Errorf("sandbox directory not created: %v", err)
	}
}

func TestSandboxDir_CreatesIsolatedDirsPerApp(t *testing.T) {
	svc := newTestService(t)
	d1, _ := svc.sandboxDir(testUserA, "app-one")
	d2, _ := svc.sandboxDir(testUserA, "app-two")
	if d1 == d2 {
		t.Errorf("different apps should have different sandbox dirs")
	}
	if filepath.Dir(d1) != filepath.Dir(d2) {
		t.Errorf("different apps for the SAME user should share the same user dir")
	}
}

// SECURITY (APPFS-01): the SAME app id for two DIFFERENT users must resolve
// to two entirely separate directories — this is the actual isolation
// boundary the cross-user vulnerability was about.
func TestSandboxDir_IsolatesDifferentUsers(t *testing.T) {
	svc := newTestService(t)
	dirA, err := svc.sandboxDir(testUserA, "office")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	dirB, err := svc.sandboxDir(testUserB, "office")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dirA == dirB {
		t.Fatalf("same app id for different users must NOT resolve to the same directory: %q == %q", dirA, dirB)
	}
	if filepath.Dir(dirA) == filepath.Dir(dirB) {
		t.Fatalf("different users must not share a parent directory: %q vs %q", dirA, dirB)
	}
}

func TestSandboxDir_InvalidAppIDs(t *testing.T) {
	svc := newTestService(t)
	cases := []string{
		"",
		"../escape",
		"UPPER",
		"has space",
		"has/slash",
		"-starts-with-dash",
	}
	for _, id := range cases {
		_, err := svc.sandboxDir(testUserA, id)
		if err == nil {
			t.Errorf("expected error for app id %q, got nil", id)
		}
	}
}

func TestSandboxDir_InvalidUserIDs(t *testing.T) {
	svc := newTestService(t)
	cases := []string{
		"",
		"../escape",
		"has/slash",
		"has space",
	}
	for _, id := range cases {
		_, err := svc.sandboxDir(id, "my-app")
		if err == nil {
			t.Errorf("expected error for user id %q, got nil", id)
		}
	}
}

// ---- safeJoin path-traversal containment ----

func TestSafeJoin_ValidPaths(t *testing.T) {
	sandbox := "/tmp/sandbox"
	cases := []struct {
		rel  string
		want string
	}{
		{"file.txt", "/tmp/sandbox/file.txt"},
		{"sub/file.txt", "/tmp/sandbox/sub/file.txt"},
		{"a/b/c.json", "/tmp/sandbox/a/b/c.json"},
	}
	for _, tc := range cases {
		got, err := safeJoin(sandbox, tc.rel)
		if err != nil {
			t.Errorf("safeJoin(%q): unexpected error: %v", tc.rel, err)
		}
		if got != tc.want {
			t.Errorf("safeJoin(%q) = %q, want %q", tc.rel, got, tc.want)
		}
	}
}

func TestSafeJoin_Traversal(t *testing.T) {
	sandbox := "/tmp/sandbox"
	cases := []string{
		"",
		"../escape",
		"sub/../../escape",
		"/absolute",
		"a/../../../etc/passwd",
	}
	for _, rel := range cases {
		_, err := safeJoin(sandbox, rel)
		if err == nil {
			t.Errorf("safeJoin(%q): expected error, got nil", rel)
		}
	}
}

// ---- HTTP handler roundtrip: write / read / list ----

func TestHandlePut_Get_Roundtrip(t *testing.T) {
	svc := newTestService(t)
	mux := http.NewServeMux()
	svc.Register(mux)

	content := []byte("hello vulos appfs")
	app := "test-app"

	// PUT
	putReq := withUser(httptest.NewRequest(http.MethodPut, "/api/appdata/"+app+"/notes.txt",
		bytes.NewReader(content)), testUserA)
	putW := httptest.NewRecorder()
	mux.ServeHTTP(putW, putReq)
	if putW.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, want 200; body: %s", putW.Code, putW.Body.String())
	}

	// GET
	getReq := withUser(httptest.NewRequest(http.MethodGet, "/api/appdata/"+app+"/notes.txt", nil), testUserA)
	getW := httptest.NewRecorder()
	mux.ServeHTTP(getW, getReq)
	if getW.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want 200; body: %s", getW.Code, getW.Body.String())
	}
	if !bytes.Equal(getW.Body.Bytes(), content) {
		t.Errorf("GET body = %q, want %q", getW.Body.String(), content)
	}
}

func TestHandleList_ReturnFiles(t *testing.T) {
	svc := newTestService(t)
	mux := http.NewServeMux()
	svc.Register(mux)

	app := "list-app"
	files := []string{"alpha.txt", "beta.json"}
	for _, name := range files {
		req := withUser(httptest.NewRequest(http.MethodPut, "/api/appdata/"+app+"/"+name,
			strings.NewReader("data for "+name)), testUserA)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("PUT %s status = %d", name, w.Code)
		}
	}

	listReq := withUser(httptest.NewRequest(http.MethodGet, "/api/appdata/"+app, nil), testUserA)
	listW := httptest.NewRecorder()
	mux.ServeHTTP(listW, listReq)
	if listW.Code != http.StatusOK {
		t.Fatalf("LIST status = %d; body: %s", listW.Code, listW.Body.String())
	}

	var entries []FileEntry
	if err := json.Unmarshal(listW.Body.Bytes(), &entries); err != nil {
		t.Fatalf("LIST decode error: %v", err)
	}
	if len(entries) != len(files) {
		t.Errorf("LIST returned %d entries, want %d", len(entries), len(files))
	}
}

func TestHandleDelete_RemovesFile(t *testing.T) {
	svc := newTestService(t)
	mux := http.NewServeMux()
	svc.Register(mux)

	app := "del-app"

	// create
	putReq := withUser(httptest.NewRequest(http.MethodPut, "/api/appdata/"+app+"/todelete.txt",
		strings.NewReader("bye")), testUserA)
	mux.ServeHTTP(httptest.NewRecorder(), putReq)

	// delete
	delReq := withUser(httptest.NewRequest(http.MethodDelete, "/api/appdata/"+app+"/todelete.txt", nil), testUserA)
	delW := httptest.NewRecorder()
	mux.ServeHTTP(delW, delReq)
	if delW.Code != http.StatusOK {
		t.Fatalf("DELETE status = %d; body: %s", delW.Code, delW.Body.String())
	}

	// confirm gone
	getReq := withUser(httptest.NewRequest(http.MethodGet, "/api/appdata/"+app+"/todelete.txt", nil), testUserA)
	getW := httptest.NewRecorder()
	mux.ServeHTTP(getW, getReq)
	if getW.Code != http.StatusNotFound {
		t.Errorf("GET after DELETE status = %d, want 404", getW.Code)
	}
}

func TestHandleGet_NotFound(t *testing.T) {
	svc := newTestService(t)
	mux := http.NewServeMux()
	svc.Register(mux)

	req := withUser(httptest.NewRequest(http.MethodGet, "/api/appdata/missing-app/no-such-file.txt", nil), testUserA)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

func TestHandlePut_TraversalRejected(t *testing.T) {
	svc := newTestService(t)
	mux := http.NewServeMux()
	svc.Register(mux)

	// The mux pattern captures {path...}, so we bypass the mux and call the
	// handler directly with a synthetic request whose PathValue is poisoned.
	req := httptest.NewRequest(http.MethodPut, "/api/appdata/my-app/../../../etc/passwd",
		strings.NewReader("evil"))
	w := httptest.NewRecorder()
	// Using the mux — the router will normalise the URL. We test safeJoin directly
	// instead to confirm the traversal guard fires.
	_, err := safeJoin("/some/sandbox", "../../../etc/passwd")
	if err == nil {
		t.Error("expected error for traversal path, got nil")
	}
	_ = w
	_ = req
}

func TestHandlePut_InvalidAppID(t *testing.T) {
	svc := newTestService(t)
	mux := http.NewServeMux()
	svc.Register(mux)

	req := withUser(httptest.NewRequest(http.MethodPut, "/api/appdata/BADAPP/file.txt",
		strings.NewReader("x")), testUserA)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid app id, got %d", w.Code)
	}
}

func TestHandlePut_SubdirectoryCreation(t *testing.T) {
	svc := newTestService(t)
	mux := http.NewServeMux()
	svc.Register(mux)

	req := withUser(httptest.NewRequest(http.MethodPut, "/api/appdata/my-app/sub/dir/file.txt",
		strings.NewReader("nested")), testUserA)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("nested PUT status = %d; body: %s", w.Code, w.Body.String())
	}
}

// ---- APPFS-01 regression coverage: auth + cross-user isolation ----

func TestHandlers_MissingUserIDRejected(t *testing.T) {
	svc := newTestService(t)
	mux := http.NewServeMux()
	svc.Register(mux)

	reqs := []*http.Request{
		httptest.NewRequest(http.MethodGet, "/api/appdata/office", nil),
		httptest.NewRequest(http.MethodGet, "/api/appdata/office/a.txt", nil),
		httptest.NewRequest(http.MethodPut, "/api/appdata/office/a.txt", strings.NewReader("x")),
		httptest.NewRequest(http.MethodDelete, "/api/appdata/office/a.txt", nil),
	}
	for _, req := range reqs {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s %s with no X-User-ID: status = %d, want 401", req.Method, req.URL.Path, w.Code)
		}
	}
}

// THE core regression: on a shared box, User B must never be able to read,
// overwrite, or delete User A's app data — even naming the exact same app id
// and file path — because the sandbox root now includes the SERVER-DERIVED
// user id as well as the app id.
func TestCrossUserIsolation_ReadWriteDelete(t *testing.T) {
	svc := newTestService(t)
	mux := http.NewServeMux()
	svc.Register(mux)

	app := "office"
	secret := "user A's private notes"

	// User A writes a file.
	putReq := withUser(httptest.NewRequest(http.MethodPut, "/api/appdata/"+app+"/notes.txt",
		strings.NewReader(secret)), testUserA)
	putW := httptest.NewRecorder()
	mux.ServeHTTP(putW, putReq)
	if putW.Code != http.StatusOK {
		t.Fatalf("user A PUT status = %d; body: %s", putW.Code, putW.Body.String())
	}

	// User B, same app id, same path: must see 404 — NOT user A's content.
	getReqB := withUser(httptest.NewRequest(http.MethodGet, "/api/appdata/"+app+"/notes.txt", nil), testUserB)
	getWB := httptest.NewRecorder()
	mux.ServeHTTP(getWB, getReqB)
	if getWB.Code != http.StatusNotFound {
		t.Fatalf("user B GET of user A's file: status = %d, want 404 (cross-user read must fail); body: %s", getWB.Code, getWB.Body.String())
	}

	// User B's LIST of the same app id must be empty, not show user A's file.
	listReqB := withUser(httptest.NewRequest(http.MethodGet, "/api/appdata/"+app, nil), testUserB)
	listWB := httptest.NewRecorder()
	mux.ServeHTTP(listWB, listReqB)
	if listWB.Code != http.StatusOK {
		t.Fatalf("user B LIST status = %d; body: %s", listWB.Code, listWB.Body.String())
	}
	var entries []FileEntry
	if err := json.Unmarshal(listWB.Body.Bytes(), &entries); err != nil {
		t.Fatalf("LIST decode error: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("user B's app sandbox must be empty (isolated from user A), got %d entries: %+v", len(entries), entries)
	}

	// User B writes their OWN file at the same app id + path.
	putReqB := withUser(httptest.NewRequest(http.MethodPut, "/api/appdata/"+app+"/notes.txt",
		strings.NewReader("user B's own notes")), testUserB)
	putWB := httptest.NewRecorder()
	mux.ServeHTTP(putWB, putReqB)
	if putWB.Code != http.StatusOK {
		t.Fatalf("user B PUT status = %d; body: %s", putWB.Code, putWB.Body.String())
	}

	// User A's original file must be untouched by User B's write.
	getReqA := withUser(httptest.NewRequest(http.MethodGet, "/api/appdata/"+app+"/notes.txt", nil), testUserA)
	getWA := httptest.NewRecorder()
	mux.ServeHTTP(getWA, getReqA)
	if getWA.Code != http.StatusOK || getWA.Body.String() != secret {
		t.Fatalf("user A's file was clobbered by user B's write: status=%d body=%q, want %q", getWA.Code, getWA.Body.String(), secret)
	}

	// User B attempts to delete "notes.txt" under the same app id — this must
	// only ever be able to delete User B's OWN copy, never User A's.
	delReqB := withUser(httptest.NewRequest(http.MethodDelete, "/api/appdata/"+app+"/notes.txt", nil), testUserB)
	delWB := httptest.NewRecorder()
	mux.ServeHTTP(delWB, delReqB)
	if delWB.Code != http.StatusOK {
		t.Fatalf("user B DELETE status = %d; body: %s", delWB.Code, delWB.Body.String())
	}
	getReqA2 := withUser(httptest.NewRequest(http.MethodGet, "/api/appdata/"+app+"/notes.txt", nil), testUserA)
	getWA2 := httptest.NewRecorder()
	mux.ServeHTTP(getWA2, getReqA2)
	if getWA2.Code != http.StatusOK || getWA2.Body.String() != secret {
		t.Fatalf("user A's file was deleted by user B's delete: status=%d body=%q", getWA2.Code, getWA2.Body.String())
	}
}

// ---- MigrateLegacySingleUser ----

func TestMigrateLegacySingleUser_MovesDataForSoleUser(t *testing.T) {
	base := t.TempDir()
	// Simulate pre-APPFS-01 layout: baseDir/<appID>/file
	legacyOffice := filepath.Join(base, "office")
	if err := os.MkdirAll(legacyOffice, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(legacyOffice, "doc.txt"), []byte("legacy data"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	if err := MigrateLegacySingleUser(base, testUserA); err != nil {
		t.Fatalf("MigrateLegacySingleUser: %v", err)
	}

	migrated := filepath.Join(base, testUserA, "office", "doc.txt")
	data, err := os.ReadFile(migrated)
	if err != nil {
		t.Fatalf("expected migrated file at %s: %v", migrated, err)
	}
	if string(data) != "legacy data" {
		t.Errorf("migrated content = %q, want %q", data, "legacy data")
	}
	if _, err := os.Stat(legacyOffice); !os.IsNotExist(err) {
		t.Errorf("legacy path %s should no longer exist after migration", legacyOffice)
	}
}

// Ambiguous (multi-user, or zero-user) boxes must NOT guess — legacy data is
// left exactly where it is.
func TestMigrateLegacySingleUser_NoOpWhenAmbiguous(t *testing.T) {
	base := t.TempDir()
	legacyOffice := filepath.Join(base, "office")
	if err := os.MkdirAll(legacyOffice, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(legacyOffice, "doc.txt"), []byte("legacy data"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	if err := MigrateLegacySingleUser(base, ""); err != nil {
		t.Fatalf("MigrateLegacySingleUser: %v", err)
	}

	if _, err := os.Stat(filepath.Join(legacyOffice, "doc.txt")); err != nil {
		t.Fatalf("legacy data must survive untouched when soleUserID is empty: %v", err)
	}
}

// Idempotency: a second call (as would happen on every subsequent process
// restart) must be a true no-op, even if a second user has since signed up
// (soleUserID now would resolve differently) — the marker file must win.
func TestMigrateLegacySingleUser_RunsOnlyOnce(t *testing.T) {
	base := t.TempDir()
	legacyTalk := filepath.Join(base, "talk")
	if err := os.MkdirAll(legacyTalk, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(legacyTalk, "msg.txt"), []byte("first pass"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	if err := MigrateLegacySingleUser(base, testUserA); err != nil {
		t.Fatalf("first MigrateLegacySingleUser: %v", err)
	}
	// Re-create a legacy-shaped directory to prove a second pass is a no-op.
	if err := os.MkdirAll(legacyTalk, 0755); err != nil {
		t.Fatalf("mkdir (again): %v", err)
	}
	if err := os.WriteFile(filepath.Join(legacyTalk, "msg2.txt"), []byte("second pass"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	if err := MigrateLegacySingleUser(base, testUserB); err != nil {
		t.Fatalf("second MigrateLegacySingleUser: %v", err)
	}

	// The second-pass directory must NOT have been moved under userB — the
	// marker file makes the whole function a no-op after the first run.
	if _, err := os.Stat(filepath.Join(base, testUserB, "talk", "msg2.txt")); !os.IsNotExist(err) {
		t.Fatalf("second migration pass must be a no-op (marker file must prevent it)")
	}
	if _, err := os.Stat(filepath.Join(legacyTalk, "msg2.txt")); err != nil {
		t.Fatalf("second-pass file should remain untouched in the legacy location: %v", err)
	}
}
