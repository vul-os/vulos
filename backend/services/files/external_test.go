package files

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// fakeTokenSource is a TokenSource whose mint result is scriptable: an empty
// token models "not connected", a non-empty one models a live connection.
type fakeTokenSource struct {
	token string
	err   error
	calls int
}

func (f *fakeTokenSource) MintToken(_ context.Context, _ string) (string, error) {
	f.calls++
	return f.token, f.err
}

// fakeProvider is an in-memory ExternalProvider for exercising the mount path
// without a network round-trip. It is writable so it also covers create/upload.
type fakeProvider struct {
	files    map[string][]*Node // folderID ("" = root) -> children
	blob     string
	readOnly bool
	uploads  []string // names actually written (post-conflict resolution)
}

func (p *fakeProvider) Kind() string                        { return "gdrive" }
func (p *fakeProvider) IntegrationProvider() string         { return "google" }
func (p *fakeProvider) DisplayName() string                 { return "Google Drive" }
func (p *fakeProvider) Writable() bool                      { return !p.readOnly }
func (p *fakeProvider) ConfigFields() []ProviderConfigField { return nil }
func (p *fakeProvider) ValidateConfig(map[string]string) (map[string]string, error) {
	return nil, nil
}
func (p *fakeProvider) List(_ context.Context, call ProviderCall, folderID string) ([]*Node, error) {
	if call.Token == "" {
		return nil, ErrExternalNotConnected
	}
	return p.files[folderID], nil
}
func (p *fakeProvider) Download(_ context.Context, _ ProviderCall, _ string) (io.ReadCloser, string, int64, error) {
	return io.NopCloser(strings.NewReader(p.blob)), "text/plain", int64(len(p.blob)), nil
}
func (p *fakeProvider) CreateFolder(_ context.Context, _ ProviderCall, parentID, name string) (*Node, error) {
	n := &Node{ID: "fld-" + name, Name: name, IsDir: true}
	if p.files == nil {
		p.files = map[string][]*Node{}
	}
	p.files[parentID] = append(p.files[parentID], n)
	return n, nil
}
func (p *fakeProvider) Upload(_ context.Context, _ ProviderCall, parentID, name, _ string, _ int64, r io.Reader, policy ConflictPolicy) (*Node, error) {
	taken := map[string]bool{}
	for _, n := range p.files[parentID] {
		taken[n.Name] = true
	}
	if taken[name] {
		switch policy {
		case ConflictFail:
			return nil, ErrExternalConflict
		case ConflictOverwrite:
			// keep name
		default:
			name = uniqueName(name, func(n string) bool { return taken[n] })
		}
	}
	io.Copy(io.Discard, r)
	p.uploads = append(p.uploads, name)
	n := &Node{ID: "file-" + name, Name: name}
	if p.files == nil {
		p.files = map[string][]*Node{}
	}
	p.files[parentID] = append(p.files[parentID], n)
	return n, nil
}

func newTestSvc(t *testing.T) *Service {
	t.Helper()
	svc, err := New(filepath.Join(t.TempDir(), "files.db"), &fakeBroker{}, func(string) string { return "bucket" })
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { svc.Close() })
	return svc
}

func TestExternalDegradesWhenNotWired(t *testing.T) {
	svc := newTestSvc(t)
	if svc.ExternalEnabled() {
		t.Fatal("expected external mounts disabled with no seam")
	}
	// Local Drive still works.
	if _, err := svc.CreateFolder("u1", "", "docs"); err != nil {
		t.Fatalf("local CreateFolder should work standalone: %v", err)
	}
	// External methods report unavailable, not a hard error elsewhere.
	if _, err := svc.ConnectExternal(context.Background(), "u1", "gdrive", "", nil); err != ErrExternalUnavailable {
		t.Fatalf("ConnectExternal = %v, want ErrExternalUnavailable", err)
	}
	if mounts, err := svc.ListExternalMounts("u1"); err != nil || mounts != nil {
		t.Fatalf("ListExternalMounts unwired = (%v,%v), want (nil,nil)", mounts, err)
	}
}

func TestExternalConnectRequiresConnection(t *testing.T) {
	svc := newTestSvc(t)
	ts := &fakeTokenSource{token: ""} // not connected
	svc.WithExternal(ts, &fakeProvider{})
	if !svc.ExternalEnabled() {
		t.Fatal("expected external enabled after WithExternal")
	}
	if _, err := svc.ConnectExternal(context.Background(), "u1", "gdrive", "", nil); err != ErrExternalNotConnected {
		t.Fatalf("ConnectExternal not-connected = %v, want ErrExternalNotConnected", err)
	}
	if _, err := svc.ConnectExternal(context.Background(), "u1", "dropbox", "", nil); err != ErrExternalProvider {
		t.Fatalf("ConnectExternal unknown provider = %v, want ErrExternalProvider", err)
	}
}

func TestExternalMountListAndIsolation(t *testing.T) {
	ctx := context.Background()
	svc := newTestSvc(t)
	prov := &fakeProvider{
		files: map[string][]*Node{
			"":     {{ID: "fld1", Name: "Reports", IsDir: true}, {ID: "f1", Name: "a.txt", Size: 3}},
			"fld1": {{ID: "f2", Name: "b.txt", Size: 5}},
		},
		blob: "hi!",
	}
	svc.WithExternal(&fakeTokenSource{token: "live-token"}, prov)

	m, err := svc.ConnectExternal(ctx, "u1", "gdrive", "", nil)
	if err != nil {
		t.Fatalf("ConnectExternal: %v", err)
	}
	if m.Name != "Google Drive" || m.Provider != "gdrive" {
		t.Fatalf("mount defaults wrong: %+v", m)
	}

	mounts, err := svc.ListExternalMounts("u1")
	if err != nil || len(mounts) != 1 || mounts[0].ID != m.ID {
		t.Fatalf("ListExternalMounts = (%+v,%v)", mounts, err)
	}

	// Root listing maps provider items into Node shape; parent stamped to mount.
	nodes, err := svc.ListExternal(ctx, "u1", m.ID, "")
	if err != nil {
		t.Fatalf("ListExternal root: %v", err)
	}
	if len(nodes) != 2 || nodes[0].ID != "fld1" || !nodes[0].IsDir || nodes[0].ParentID != m.ID {
		t.Fatalf("root nodes wrong: %+v", nodes)
	}

	// Descend into a folder by its provider id.
	kids, err := svc.ListExternal(ctx, "u1", m.ID, "fld1")
	if err != nil || len(kids) != 1 || kids[0].ID != "f2" {
		t.Fatalf("ListExternal folder = (%+v,%v)", kids, err)
	}

	// Download streams bytes through the provider.
	rc, ct, size, err := svc.DownloadExternal(ctx, "u1", m.ID, "f1")
	if err != nil {
		t.Fatalf("DownloadExternal: %v", err)
	}
	defer rc.Close()
	b, _ := io.ReadAll(rc)
	if string(b) != "hi!" || ct != "text/plain" || size != 3 {
		t.Fatalf("download = (%q,%q,%d)", b, ct, size)
	}

	// Cross-user access is denied (404, not enumerable).
	if _, err := svc.ListExternal(ctx, "intruder", m.ID, ""); err != ErrNotFound {
		t.Fatalf("cross-user ListExternal = %v, want ErrNotFound", err)
	}
	if _, _, _, err := svc.DownloadExternal(ctx, "intruder", m.ID, "f1"); err != ErrNotFound {
		t.Fatalf("cross-user DownloadExternal = %v, want ErrNotFound", err)
	}

	// Disconnect removes the mount.
	if err := svc.DisconnectExternal("u1", m.ID); err != nil {
		t.Fatalf("DisconnectExternal: %v", err)
	}
	if mounts, _ := svc.ListExternalMounts("u1"); len(mounts) != 0 {
		t.Fatalf("mount not removed: %+v", mounts)
	}
}

// TestGDriveProviderMockedList drives the REAL Google Drive provider against a
// mocked Drive v3 API (httptest), proving the list path parses the Drive JSON
// into Nodes and the download path follows the export/media branch.
func TestGDriveProviderMockedList(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok-123" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case strings.HasPrefix(r.URL.Path, "/files") && r.URL.Query().Get("q") != "":
			// List call. Assert the folder filter is present.
			if !strings.Contains(r.URL.Query().Get("q"), "in parents") {
				t.Errorf("missing parents filter: %q", r.URL.Query().Get("q"))
			}
			json.NewEncoder(w).Encode(driveListResp{Files: []driveFile{
				{ID: "F1", Name: "Folder", MimeType: gdriveFolderMime},
				{ID: "D1", Name: "doc.txt", MimeType: "text/plain", Size: "12", ModifiedTime: "2026-06-01T10:00:00Z"},
			}})
		case strings.HasSuffix(r.URL.Path, "/export"):
			w.Header().Set("Content-Type", "application/pdf")
			io.WriteString(w, "%PDF-fake")
		case strings.Contains(r.URL.RawQuery, "alt=media"):
			w.Header().Set("Content-Type", "text/plain")
			io.WriteString(w, "hello bytes")
		default:
			// Metadata lookup for download.
			json.NewEncoder(w).Encode(driveFile{ID: "D1", Name: "doc.txt", MimeType: "text/plain", Size: "12"})
		}
	}))
	defer srv.Close()

	g := NewGDriveProvider()
	g.base = srv.URL // point the fixed host at the mock

	nodes, err := g.List(ctx, ProviderCall{Token: "tok-123"}, "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(nodes) != 2 {
		t.Fatalf("want 2 nodes, got %d", len(nodes))
	}
	if !nodes[0].IsDir || nodes[0].ID != "F1" {
		t.Fatalf("folder node wrong: %+v", nodes[0])
	}
	if nodes[1].IsDir || nodes[1].Size != 12 || nodes[1].Name != "doc.txt" {
		t.Fatalf("file node wrong: %+v", nodes[1])
	}

	// Regular binary file → alt=media branch.
	rc, ct, _, err := g.Download(ctx, ProviderCall{Token: "tok-123"}, "D1")
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	b, _ := io.ReadAll(rc)
	rc.Close()
	if string(b) != "hello bytes" || ct != "text/plain" {
		t.Fatalf("download = (%q,%q)", b, ct)
	}

	// Auth rejection surfaces as an error.
	if _, err := g.List(ctx, ProviderCall{Token: "wrong"}, ""); err == nil {
		t.Fatal("expected error on bad token")
	}
}

func TestEscapeDriveQ(t *testing.T) {
	if got := escapeDriveQ("a'b\\c"); got != `a\'b\\c` {
		t.Fatalf("escapeDriveQ = %q", got)
	}
}

// TestExternalWriteLifecycle exercises the service write surface end-to-end on a
// writable mount: create-folder, upload, rename-on-collision, and the read-only
// guard.
func TestExternalWriteLifecycle(t *testing.T) {
	ctx := context.Background()
	svc := newTestSvc(t)
	prov := &fakeProvider{files: map[string][]*Node{"": {{ID: "f1", Name: "a.txt"}}}}
	svc.WithExternal(&fakeTokenSource{token: "live"}, prov)

	m, err := svc.ConnectExternal(ctx, "u1", "gdrive", "", nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if !m.Writable {
		t.Fatal("mount should be writable")
	}

	if _, err := svc.CreateFolderExternal(ctx, "u1", m.ID, "", "docs"); err != nil {
		t.Fatalf("CreateFolderExternal: %v", err)
	}

	// First upload of a fresh name keeps it.
	n, err := svc.UploadExternal(ctx, "u1", m.ID, "", "new.txt", "text/plain", 3, strings.NewReader("abc"), ConflictRename)
	if err != nil || n.Name != "new.txt" {
		t.Fatalf("upload new = (%+v,%v)", n, err)
	}
	// Re-uploading a colliding name under rename policy gets a suffixed name.
	n2, err := svc.UploadExternal(ctx, "u1", m.ID, "", "a.txt", "text/plain", 3, strings.NewReader("abc"), ConflictRename)
	if err != nil || n2.Name != "a (1).txt" {
		t.Fatalf("upload rename = (%+v,%v)", n2, err)
	}
	// Fail policy on a collision returns ErrExternalConflict (service-wrapped).
	if _, err := svc.UploadExternal(ctx, "u1", m.ID, "", "a.txt", "text/plain", 3, strings.NewReader("x"), ConflictFail); !errors.Is(err, ErrExternalConflict) {
		t.Fatalf("upload fail = %v, want ErrExternalConflict", err)
	}

	// Read-only provider rejects writes.
	ro := &fakeProvider{readOnly: true}
	svc2 := newTestSvc(t)
	svc2.WithExternal(&fakeTokenSource{token: "live"}, ro)
	rm, err := svc2.ConnectExternal(ctx, "u1", "gdrive", "", nil)
	if err != nil {
		t.Fatalf("connect ro: %v", err)
	}
	if rm.Writable {
		t.Fatal("read-only mount must not be writable")
	}
	if _, err := svc2.CreateFolderExternal(ctx, "u1", rm.ID, "", "x"); err != ErrExternalReadOnly {
		t.Fatalf("CreateFolderExternal ro = %v, want ErrExternalReadOnly", err)
	}
	if _, err := svc2.UploadExternal(ctx, "u1", rm.ID, "", "x", "", 0, strings.NewReader(""), ConflictRename); err != ErrExternalReadOnly {
		t.Fatalf("UploadExternal ro = %v, want ErrExternalReadOnly", err)
	}
}

// multipartName extracts the "name" from a Drive multipart/related upload body so
// the mock can echo back what the provider actually sent (proving rename worked).
func multipartName(t *testing.T, r *http.Request) string {
	t.Helper()
	_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		t.Fatalf("parse media type: %v", err)
	}
	mr := multipart.NewReader(r.Body, params["boundary"])
	part, err := mr.NextPart()
	if err != nil {
		t.Fatalf("next part: %v", err)
	}
	var meta struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(part).Decode(&meta); err != nil {
		t.Fatalf("decode meta: %v", err)
	}
	return meta.Name
}

// TestGDriveProviderWrite drives the REAL Drive provider's write paths against a
// mocked Drive v3 API: create-folder, fresh upload, rename-on-collision, and
// overwrite-by-id.
func TestGDriveProviderWrite(t *testing.T) {
	ctx := context.Background()
	// Server state: one existing file "report.txt" (id EXIST) in root.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/files") && r.URL.Query().Get("q") != "":
			json.NewEncoder(w).Encode(driveListResp{Files: []driveFile{
				{ID: "EXIST", Name: "report.txt", MimeType: "text/plain", Size: "3"},
			}})
		case r.Method == http.MethodPost && r.URL.Query().Get("uploadType") == "multipart":
			name := multipartName(t, r)
			json.NewEncoder(w).Encode(driveFile{ID: "NEW", Name: name, MimeType: "text/plain"})
		case r.Method == http.MethodPatch:
			// overwrite-by-id: assert the path carries the existing id.
			if !strings.Contains(r.URL.Path, "EXIST") {
				t.Errorf("overwrite did not target EXIST: %s", r.URL.Path)
			}
			json.NewEncoder(w).Encode(driveFile{ID: "EXIST", Name: "report.txt", MimeType: "text/plain"})
		case r.Method == http.MethodPost: // create-folder (application/json)
			json.NewEncoder(w).Encode(driveFile{ID: "FLD", Name: "docs", MimeType: gdriveFolderMime})
		default:
			t.Errorf("unexpected request %s %s?%s", r.Method, r.URL.Path, r.URL.RawQuery)
		}
	}))
	defer srv.Close()

	g := NewGDriveProvider()
	g.base = srv.URL
	g.uploadBase = srv.URL
	call := ProviderCall{Token: "t"}

	fld, err := g.CreateFolder(ctx, call, "", "docs")
	if err != nil || !fld.IsDir || fld.Name != "docs" {
		t.Fatalf("CreateFolder = (%+v,%v)", fld, err)
	}

	// Fresh name → kept as-is.
	n, err := g.Upload(ctx, call, "", "fresh.txt", "text/plain", 3, strings.NewReader("abc"), ConflictRename)
	if err != nil || n.Name != "fresh.txt" {
		t.Fatalf("upload fresh = (%+v,%v)", n, err)
	}
	// Colliding name under rename → "report (1).txt".
	n, err = g.Upload(ctx, call, "", "report.txt", "text/plain", 3, strings.NewReader("abc"), ConflictRename)
	if err != nil || n.Name != "report (1).txt" {
		t.Fatalf("upload rename = (%+v,%v)", n, err)
	}
	// Colliding name under overwrite → PATCH the existing id (node keeps name).
	n, err = g.Upload(ctx, call, "", "report.txt", "text/plain", 3, strings.NewReader("abc"), ConflictOverwrite)
	if err != nil || n.ID != "EXIST" {
		t.Fatalf("upload overwrite = (%+v,%v)", n, err)
	}
	// Colliding name under fail → ErrExternalConflict (no write attempted).
	if _, err := g.Upload(ctx, call, "", "report.txt", "text/plain", 3, strings.NewReader("abc"), ConflictFail); err != ErrExternalConflict {
		t.Fatalf("upload fail = %v, want ErrExternalConflict", err)
	}
}

// TestDropboxProvider drives the REAL Dropbox provider against a mocked v2 API:
// list, download, create-folder, and upload (rename + fail-on-conflict).
func TestDropboxProvider(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer t" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/2/files/list_folder":
			json.NewEncoder(w).Encode(dropboxListResp{Entries: []dropboxEntry{
				{Tag: "folder", Name: "Sub", PathDisplay: "/Sub"},
				{Tag: "file", Name: "a.txt", PathDisplay: "/a.txt", Size: 3, ServerModified: "2026-06-01T00:00:00Z"},
				{Tag: "deleted", Name: "gone.txt", PathDisplay: "/gone.txt"},
			}})
		case "/2/files/download":
			w.Header().Set("Content-Type", "text/plain")
			io.WriteString(w, "hello dropbox")
		case "/2/files/create_folder_v2":
			io.WriteString(w, `{"metadata":{".tag":"folder","name":"docs","path_display":"/docs"}}`)
		case "/2/files/upload":
			var arg struct {
				Path       string `json:"path"`
				Mode       string `json:"mode"`
				Autorename bool   `json:"autorename"`
			}
			json.Unmarshal([]byte(r.Header.Get("Dropbox-API-Arg")), &arg)
			if arg.Mode == "add" && !arg.Autorename {
				w.WriteHeader(http.StatusConflict) // fail policy collides
				return
			}
			name := arg.Path[strings.LastIndex(arg.Path, "/")+1:]
			json.NewEncoder(w).Encode(dropboxEntry{Tag: "file", Name: name, PathDisplay: arg.Path, Size: 3})
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	d := NewDropboxProvider()
	d.apiBase = srv.URL
	d.contentBase = srv.URL
	call := ProviderCall{Token: "t"}

	nodes, err := d.List(ctx, call, "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(nodes) != 2 || !nodes[0].IsDir || nodes[0].ID != "/Sub" || nodes[1].Name != "a.txt" {
		t.Fatalf("dropbox list nodes wrong: %+v", nodes)
	}

	rc, ct, _, err := d.Download(ctx, call, "/a.txt")
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	b, _ := io.ReadAll(rc)
	rc.Close()
	if string(b) != "hello dropbox" || ct != "text/plain" {
		t.Fatalf("download = (%q,%q)", b, ct)
	}

	fld, err := d.CreateFolder(ctx, call, "", "docs")
	if err != nil || !fld.IsDir || fld.Name != "docs" {
		t.Fatalf("CreateFolder = (%+v,%v)", fld, err)
	}

	n, err := d.Upload(ctx, call, "/Sub", "x.txt", "text/plain", 3, strings.NewReader("abc"), ConflictRename)
	if err != nil || n.Name != "x.txt" || n.ID != "/Sub/x.txt" {
		t.Fatalf("upload rename = (%+v,%v)", n, err)
	}
	if _, err := d.Upload(ctx, call, "", "x.txt", "text/plain", 3, strings.NewReader("abc"), ConflictFail); err != ErrExternalConflict {
		t.Fatalf("upload fail = %v, want ErrExternalConflict", err)
	}
}

// TestGCSProvider drives the REAL GCS provider against a mocked JSON API: config
// validation, prefix-scoped list, download, create-folder marker, and upload
// (rename + fail-on-conflict via ifGenerationMatch=0).
func TestGCSProvider(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer t" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/o") && r.URL.Query().Get("alt") != "media" && !strings.Contains(r.URL.Path, "/o/"):
			// list under prefix "team/"
			json.NewEncoder(w).Encode(gcsListResp{
				Prefixes: []string{"team/sub/"},
				Items: []gcsObject{
					{Name: "team/", Size: "0"},                                 // folder marker, skipped
					{Name: "team/a.txt", Size: "3", ContentType: "text/plain"}, // file
				},
			})
		case r.Method == http.MethodGet && r.URL.Query().Get("alt") == "media":
			w.Header().Set("Content-Type", "text/plain")
			io.WriteString(w, "hello gcs")
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/upload/"):
			name := r.URL.Query().Get("name")
			if r.URL.Query().Get("ifGenerationMatch") == "0" && name == "team/a.txt" {
				w.WriteHeader(http.StatusPreconditionFailed) // fail policy collides
				return
			}
			json.NewEncoder(w).Encode(gcsObject{Name: name, ContentType: "text/plain"})
		default:
			t.Errorf("unexpected request %s %s?%s", r.Method, r.URL.Path, r.URL.RawQuery)
		}
	}))
	defer srv.Close()

	g := NewGCSProvider()
	g.base = srv.URL

	// Config validation: bucket required; prefix normalized to trailing slash.
	if _, err := g.ValidateConfig(map[string]string{}); err == nil {
		t.Fatal("ValidateConfig should require a bucket")
	}
	cfg, err := g.ValidateConfig(map[string]string{"bucket": "my-bucket", "prefix": "team"})
	if err != nil || cfg["bucket"] != "my-bucket" || cfg["prefix"] != "team/" {
		t.Fatalf("ValidateConfig = (%+v,%v)", cfg, err)
	}
	call := ProviderCall{Token: "t", Config: cfg}

	nodes, err := g.List(ctx, call, "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(nodes) != 2 || !nodes[0].IsDir || nodes[0].Name != "sub" || nodes[1].Name != "a.txt" || nodes[1].ID != "team/a.txt" {
		t.Fatalf("gcs list nodes wrong: %+v", nodes)
	}

	rc, _, _, err := g.Download(ctx, call, "team/a.txt")
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	b, _ := io.ReadAll(rc)
	rc.Close()
	if string(b) != "hello gcs" {
		t.Fatalf("download = %q", b)
	}
	// Scope guard: an object outside the mount prefix is refused.
	if _, _, _, err := g.Download(ctx, call, "other/secret"); err == nil {
		t.Fatal("expected out-of-scope download to fail")
	}

	fld, err := g.CreateFolder(ctx, call, "", "docs")
	if err != nil || !fld.IsDir || fld.ID != "team/docs/" {
		t.Fatalf("CreateFolder = (%+v,%v)", fld, err)
	}

	// rename: "a.txt" is taken under team/ → becomes "a (1).txt".
	n, err := g.Upload(ctx, call, "", "a.txt", "text/plain", 3, strings.NewReader("abc"), ConflictRename)
	if err != nil || n.Name != "a (1).txt" {
		t.Fatalf("upload rename = (%+v,%v)", n, err)
	}
	// fail: colliding object with ifGenerationMatch=0 → ErrExternalConflict.
	if _, err := g.Upload(ctx, call, "", "a.txt", "text/plain", 3, strings.NewReader("abc"), ConflictFail); err != ErrExternalConflict {
		t.Fatalf("upload fail = %v, want ErrExternalConflict", err)
	}
}
