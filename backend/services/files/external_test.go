package files

import (
	"context"
	"encoding/json"
	"io"
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
// without a network round-trip.
type fakeProvider struct {
	files map[string][]*Node // folderID ("" = root) -> children
	blob  string
}

func (p *fakeProvider) Kind() string                { return "gdrive" }
func (p *fakeProvider) IntegrationProvider() string { return "google" }
func (p *fakeProvider) DisplayName() string         { return "Google Drive" }
func (p *fakeProvider) List(_ context.Context, token, folderID string) ([]*Node, error) {
	if token == "" {
		return nil, ErrExternalNotConnected
	}
	return p.files[folderID], nil
}
func (p *fakeProvider) Download(_ context.Context, _ /*token*/, _ /*fileID*/ string) (io.ReadCloser, string, int64, error) {
	return io.NopCloser(strings.NewReader(p.blob)), "text/plain", int64(len(p.blob)), nil
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
	if _, err := svc.ConnectExternal(context.Background(), "u1", "gdrive", ""); err != ErrExternalUnavailable {
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
	if _, err := svc.ConnectExternal(context.Background(), "u1", "gdrive", ""); err != ErrExternalNotConnected {
		t.Fatalf("ConnectExternal not-connected = %v, want ErrExternalNotConnected", err)
	}
	if _, err := svc.ConnectExternal(context.Background(), "u1", "dropbox", ""); err != ErrExternalProvider {
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

	m, err := svc.ConnectExternal(ctx, "u1", "gdrive", "")
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

	nodes, err := g.List(ctx, "tok-123", "")
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
	rc, ct, _, err := g.Download(ctx, "tok-123", "D1")
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	b, _ := io.ReadAll(rc)
	rc.Close()
	if string(b) != "hello bytes" || ct != "text/plain" {
		t.Fatalf("download = (%q,%q)", b, ct)
	}

	// Auth rejection surfaces as an error.
	if _, err := g.List(ctx, "wrong", ""); err == nil {
		t.Fatal("expected error on bad token")
	}
}

func TestEscapeDriveQ(t *testing.T) {
	if got := escapeDriveQ("a'b\\c"); got != `a\'b\\c` {
		t.Fatalf("escapeDriveQ = %q", got)
	}
}
