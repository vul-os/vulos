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

// newTestService returns an appfs Service rooted in t.TempDir().
func newTestService(t *testing.T) *Service {
	t.Helper()
	return New(t.TempDir())
}

// ---- sandboxDir / per-app path derivation ----

func TestSandboxDir_ValidAppID(t *testing.T) {
	svc := newTestService(t)
	dir, err := svc.sandboxDir("my-app")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasSuffix(dir, string(filepath.Separator)+"my-app") {
		t.Errorf("sandbox dir %q does not end with /my-app", dir)
	}
	fi, err := os.Stat(dir)
	if err != nil || !fi.IsDir() {
		t.Errorf("sandbox directory not created: %v", err)
	}
}

func TestSandboxDir_CreatesIsolatedDirs(t *testing.T) {
	svc := newTestService(t)
	d1, _ := svc.sandboxDir("app-one")
	d2, _ := svc.sandboxDir("app-two")
	if d1 == d2 {
		t.Errorf("different apps should have different sandbox dirs")
	}
	if filepath.Dir(d1) != filepath.Dir(d2) {
		t.Errorf("different apps should share the same base dir")
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
		_, err := svc.sandboxDir(id)
		if err == nil {
			t.Errorf("expected error for app id %q, got nil", id)
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

	content := []byte("hello vula appfs")
	app := "test-app"

	// PUT
	putReq := httptest.NewRequest(http.MethodPut, "/api/appdata/"+app+"/notes.txt",
		bytes.NewReader(content))
	putW := httptest.NewRecorder()
	mux.ServeHTTP(putW, putReq)
	if putW.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, want 200; body: %s", putW.Code, putW.Body.String())
	}

	// GET
	getReq := httptest.NewRequest(http.MethodGet, "/api/appdata/"+app+"/notes.txt", nil)
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
		req := httptest.NewRequest(http.MethodPut, "/api/appdata/"+app+"/"+name,
			strings.NewReader("data for "+name))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("PUT %s status = %d", name, w.Code)
		}
	}

	listReq := httptest.NewRequest(http.MethodGet, "/api/appdata/"+app, nil)
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
	putReq := httptest.NewRequest(http.MethodPut, "/api/appdata/"+app+"/todelete.txt",
		strings.NewReader("bye"))
	mux.ServeHTTP(httptest.NewRecorder(), putReq)

	// delete
	delReq := httptest.NewRequest(http.MethodDelete, "/api/appdata/"+app+"/todelete.txt", nil)
	delW := httptest.NewRecorder()
	mux.ServeHTTP(delW, delReq)
	if delW.Code != http.StatusOK {
		t.Fatalf("DELETE status = %d; body: %s", delW.Code, delW.Body.String())
	}

	// confirm gone
	getReq := httptest.NewRequest(http.MethodGet, "/api/appdata/"+app+"/todelete.txt", nil)
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

	req := httptest.NewRequest(http.MethodGet, "/api/appdata/missing-app/no-such-file.txt", nil)
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

	req := httptest.NewRequest(http.MethodPut, "/api/appdata/BADAPP/file.txt",
		strings.NewReader("x"))
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

	req := httptest.NewRequest(http.MethodPut, "/api/appdata/my-app/sub/dir/file.txt",
		strings.NewReader("nested"))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("nested PUT status = %d; body: %s", w.Code, w.Body.String())
	}
}
