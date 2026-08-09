package files

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeImportSource is an in-memory ImportSource for exercising the import engine
// without a network round-trip. content holds each file's bytes by external id;
// a "deleted" external id is removed from children to model an upstream delete.
// A child whose ContentType is the Google-Doc mime is exported to ".docx" on
// open (mirroring gdrive.OpenForImport) so the engine's export handling is
// covered without the real provider.
type fakeImportSource struct {
	children  map[string][]*Node // folderID ("" = root) -> children
	content   map[string]string  // external file id -> bytes
	listCalls int
	openCalls int
}

func (f *fakeImportSource) Kind() string                { return "gdrive" }
func (f *fakeImportSource) IntegrationProvider() string { return "google" }

func (f *fakeImportSource) List(_ context.Context, call ProviderCall, folderID string) ([]*Node, error) {
	if call.Token == "" {
		return nil, ErrExternalNotConnected
	}
	f.listCalls++
	return f.children[folderID], nil
}

func (f *fakeImportSource) OpenForImport(_ context.Context, call ProviderCall, file *Node) (io.ReadCloser, string, string, int64, error) {
	if call.Token == "" {
		return nil, "", "", 0, ErrExternalNotConnected
	}
	f.openCalls++
	name := file.Name
	ct := file.ContentType
	if file.ContentType == "application/vnd.google-apps.document" {
		name += ".docx"
		ct = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	}
	body := f.content[file.ID]
	return io.NopCloser(strings.NewReader(body)), name, ct, int64(len(body)), nil
}

// findChildNode returns the named non-deleted child of parentID in ownerID's
// Drive, or nil.
func findChildNode(t *testing.T, svc *Service, ownerID, parentID, name string) *Node {
	t.Helper()
	n, err := svc.childByName(ownerID, parentID, name)
	if err != nil {
		return nil
	}
	return n
}

func TestImportDegradesWhenNotWired(t *testing.T) {
	svc, _ := newTestService(t)
	if svc.ImportEnabled() {
		t.Fatal("import should be disabled with no seam")
	}
	if _, err := svc.StartImport(context.Background(), "u1", "gdrive", "", "once"); err != ErrImportUnavailable {
		t.Fatalf("StartImport unwired = %v, want ErrImportUnavailable", err)
	}
	if jobs, err := svc.ListImportJobs("u1"); err != nil || jobs != nil {
		t.Fatalf("ListImportJobs unwired = (%v,%v), want (nil,nil)", jobs, err)
	}
}

// TestImportCopiesAndPersistsAfterDisconnect proves IMPORT writes Vulos-owned
// copies into the Drive and that those copies SURVIVE the integration being
// disconnected (the token source going dead).
func TestImportCopiesAndPersistsAfterDisconnect(t *testing.T) {
	ctx := context.Background()
	svc, broker := newTestService(t)
	src := &fakeImportSource{
		children: map[string][]*Node{
			"":   {{ID: "f1", Name: "a.txt"}, {ID: "d1", Name: "Docs", IsDir: true}},
			"d1": {{ID: "f2", Name: "notes.txt"}},
		},
		content: map[string]string{"f1": "hello", "f2": "world"},
	}
	svc.WithExternal(&fakeTokenSource{token: "live"}) // sets the token source
	svc.WithImport(src)
	if !svc.ImportEnabled() {
		t.Fatal("import should be enabled")
	}

	job, err := svc.StartImport(ctx, "u1", "gdrive", "", "once")
	if err != nil {
		t.Fatalf("StartImport: %v", err)
	}
	if err := svc.RunImportJob(ctx, job.ID); err != nil {
		t.Fatalf("RunImportJob: %v", err)
	}

	// Root has the copied file + folder.
	aFile := findChildNode(t, svc, "u1", "", "a.txt")
	docs := findChildNode(t, svc, "u1", "", "Docs")
	if aFile == nil || aFile.IsDir {
		t.Fatalf("a.txt not imported as a file: %+v", aFile)
	}
	if docs == nil || !docs.IsDir {
		t.Fatalf("Docs folder not imported: %+v", docs)
	}
	notes := findChildNode(t, svc, "u1", docs.ID, "notes.txt")
	if notes == nil {
		t.Fatal("nested notes.txt not imported")
	}

	// The copy carries the right canonical Drive key + bytes.
	if body, ok := broker.get("vulos-u1", driveKey("u1", "a.txt")); !ok || body != "hello" {
		t.Fatalf("a.txt bytes = (%q,%v), want hello", body, ok)
	}
	if body, ok := broker.get("vulos-u1", driveKey("u1", "Docs/notes.txt")); !ok || body != "world" {
		t.Fatalf("notes.txt bytes = (%q,%v), want world", body, ok)
	}

	jobs, _ := svc.ListImportJobs("u1")
	if len(jobs) != 1 || jobs[0].Status != ImportStatusDone || jobs[0].Imported != 2 {
		t.Fatalf("job after run = %+v", jobs)
	}

	// Disconnect the integration: token source goes dead. The OWNED copies must
	// remain fully intact and readable from the Drive index.
	svc.WithExternal(&fakeTokenSource{token: ""}) // not connected anymore
	if n := findChildNode(t, svc, "u1", "", "a.txt"); n == nil {
		t.Fatal("imported copy disappeared after disconnect")
	}
	if body, ok := broker.get("vulos-u1", driveKey("u1", "a.txt")); !ok || body != "hello" {
		t.Fatalf("imported bytes lost after disconnect: (%q,%v)", body, ok)
	}
}

// TestImportSyncAdditiveOnly proves a mode=sync re-pull is idempotent (no
// duplicate copies) and ADDITIVE-ONLY: when the upstream file is deleted, the
// Vulos copy is NOT removed; when a new upstream file appears, it is added.
func TestImportSyncAdditiveOnly(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService(t)
	src := &fakeImportSource{
		children: map[string][]*Node{
			"": {{ID: "f1", Name: "a.txt"}, {ID: "f2", Name: "b.txt"}},
		},
		content: map[string]string{"f1": "AAA", "f2": "BBB"},
	}
	svc.WithExternal(&fakeTokenSource{token: "live"})
	svc.WithImport(src)

	job, err := svc.StartImport(ctx, "u1", "gdrive", "", "sync")
	if err != nil {
		t.Fatalf("StartImport: %v", err)
	}
	if err := svc.RunImportJob(ctx, job.ID); err != nil {
		t.Fatalf("first run: %v", err)
	}
	root, _ := svc.List("u1", "")
	if len(root) != 2 {
		t.Fatalf("after first run want 2 nodes, got %d: %+v", len(root), root)
	}

	// Re-pull with no upstream change: idempotent — same node count, all skipped.
	if err := svc.RunImportJob(ctx, job.ID); err != nil {
		t.Fatalf("second run: %v", err)
	}
	root, _ = svc.List("u1", "")
	if len(root) != 2 {
		t.Fatalf("re-pull duplicated nodes: got %d: %+v", len(root), root)
	}
	jobs, _ := svc.ListImportJobs("u1")
	if jobs[0].Imported != 0 || jobs[0].Skipped != 2 {
		t.Fatalf("idempotent re-pull counts = imported %d skipped %d, want 0/2", jobs[0].Imported, jobs[0].Skipped)
	}

	// Upstream DELETES b.txt and ADDS c.txt. Re-pull must keep the b.txt copy
	// (additive-only) and add c.txt.
	src.children[""] = []*Node{{ID: "f1", Name: "a.txt"}, {ID: "f3", Name: "c.txt"}}
	src.content["f3"] = "CCC"
	if err := svc.RunImportJob(ctx, job.ID); err != nil {
		t.Fatalf("third run: %v", err)
	}
	root, _ = svc.List("u1", "")
	names := map[string]bool{}
	for _, n := range root {
		names[n.Name] = true
	}
	if !names["a.txt"] || !names["b.txt"] || !names["c.txt"] {
		t.Fatalf("additive re-pull wrong set: %v (b.txt must persist, c.txt must be added)", names)
	}
	if len(root) != 3 {
		t.Fatalf("want 3 nodes after additive re-pull, got %d", len(root))
	}
}

// TestGDriveImportExportMapping drives the REAL Google Drive provider's
// OpenForImport against a mocked Drive v3 API, proving a native Google Doc is
// EXPORTED to docx (mime + .docx name) while a regular file is copied as-is.
func TestGDriveImportExportMapping(t *testing.T) {
	ctx := context.Background()
	const docxMime = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/export"):
			if got := r.URL.Query().Get("mimeType"); got != docxMime {
				t.Errorf("export mimeType = %q, want docx", got)
			}
			w.Header().Set("Content-Type", docxMime)
			io.WriteString(w, "PK-docx-bytes")
		case strings.Contains(r.URL.RawQuery, "alt=media"):
			w.Header().Set("Content-Type", "text/plain")
			io.WriteString(w, "plain bytes")
		default:
			json.NewEncoder(w).Encode(driveFile{ID: "X", Name: "x"})
		}
	}))
	defer srv.Close()

	g := NewGDriveProvider()
	g.base = srv.URL
	call := ProviderCall{Token: "t"}

	// Native Google Doc → exported docx, name gets .docx.
	doc := &Node{ID: "DOC1", Name: "Quarterly Report", ContentType: "application/vnd.google-apps.document"}
	rc, name, ct, _, err := g.OpenForImport(ctx, call, doc)
	if err != nil {
		t.Fatalf("OpenForImport doc: %v", err)
	}
	b, _ := io.ReadAll(rc)
	rc.Close()
	if name != "Quarterly Report.docx" {
		t.Fatalf("doc name = %q, want .docx suffix", name)
	}
	if ct != docxMime || string(b) != "PK-docx-bytes" {
		t.Fatalf("doc export = (%q,%q)", ct, b)
	}

	// Regular file → copied as-is, name unchanged.
	reg := &Node{ID: "F1", Name: "photo.png", ContentType: "image/png"}
	rc2, name2, ct2, _, err := g.OpenForImport(ctx, call, reg)
	if err != nil {
		t.Fatalf("OpenForImport file: %v", err)
	}
	b2, _ := io.ReadAll(rc2)
	rc2.Close()
	if name2 != "photo.png" || string(b2) != "plain bytes" || ct2 != "text/plain" {
		t.Fatalf("regular import = (%q,%q,%q)", name2, ct2, b2)
	}
}

// TestOneDriveImportSource drives the REAL OneDrive source against a mocked Graph
// API: list children (folder + file) and open a file's bytes as-is (no export).
func TestOneDriveImportSource(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer t" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/children"):
			json.NewEncoder(w).Encode(graphListResp{Value: []graphItem{
				{ID: "FLD", Name: "Reports", Folder: &graphFolder{ChildCount: 1}},
				{ID: "DOC", Name: "report.docx", Size: 5, File: &graphFileFacet{MimeType: docxMimeOD}},
			}})
		case strings.HasSuffix(r.URL.Path, "/content"):
			w.Header().Set("Content-Type", docxMimeOD)
			io.WriteString(w, "xxxxx")
		default:
			t.Errorf("unexpected %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	o := NewOneDriveProvider()
	o.base = srv.URL
	call := ProviderCall{Token: "t"}

	nodes, err := o.List(ctx, call, "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(nodes) != 2 || !nodes[0].IsDir || nodes[0].ID != "FLD" || nodes[1].Name != "report.docx" {
		t.Fatalf("onedrive list wrong: %+v", nodes)
	}
	// Office files copy as-is (no export step).
	rc, name, ct, size, err := o.OpenForImport(ctx, call, nodes[1])
	if err != nil {
		t.Fatalf("OpenForImport: %v", err)
	}
	b, _ := io.ReadAll(rc)
	rc.Close()
	if name != "report.docx" || ct != docxMimeOD || string(b) != "xxxxx" || size != 5 {
		t.Fatalf("onedrive open = (%q,%q,%q,%d)", name, ct, b, size)
	}
}

const docxMimeOD = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"

// TestRunImportJobForOwner_CrossOwnerDenied is the isolation regression for the
// owner-scoped import runner. Running an import job is a WRITE: it mints a
// provider token as the job's owner and copies files into that owner's Drive.
// A second profile on the same box must not be able to trigger another
// profile's job by supplying its id.
func TestRunImportJobForOwner_CrossOwnerDenied(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService(t)
	src := &fakeImportSource{
		children: map[string][]*Node{"": {{ID: "f1", Name: "a.txt"}}},
		content:  map[string]string{"f1": "hello"},
	}
	svc.WithExternal(&fakeTokenSource{token: "live"})
	svc.WithImport(src)

	job, err := svc.StartImport(ctx, "victim", "gdrive", "", "once")
	if err != nil {
		t.Fatalf("StartImport: %v", err)
	}

	// The attacker is a legitimate, authenticated profile on the same box.
	before := src.listCalls
	err = svc.RunImportJobForOwner(ctx, "attacker", job.ID)
	if err != ErrNotFound {
		t.Fatalf("cross-owner run = %v, want ErrNotFound (no oracle)", err)
	}
	if src.listCalls != before {
		t.Fatal("SECURITY: attacker's cross-owner run reached the import source")
	}

	// Control: the real owner still runs, so the gate denies the attacker
	// specifically rather than breaking imports for everyone.
	if err := svc.RunImportJobForOwner(ctx, "victim", job.ID); err != nil {
		t.Fatalf("owner run: %v", err)
	}
	if src.listCalls == before {
		t.Fatal("owner's run never reached the import source")
	}
	findChildNode(t, svc, "victim", "", "a.txt")
}

// TestRunImportJobForOwner_UnknownJobDenied pins the FAIL-CLOSED direction: an
// id that does not exist must deny, not fall through to a run.
func TestRunImportJobForOwner_UnknownJobDenied(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService(t)
	src := &fakeImportSource{children: map[string][]*Node{"": {}}}
	svc.WithExternal(&fakeTokenSource{token: "live"})
	svc.WithImport(src)

	if err := svc.RunImportJobForOwner(ctx, "u1", "no-such-job"); err == nil {
		t.Fatal("SECURITY: unknown job id ran instead of being denied")
	}
	if src.listCalls != 0 {
		t.Fatal("SECURITY: unknown job id reached the import source")
	}
}
