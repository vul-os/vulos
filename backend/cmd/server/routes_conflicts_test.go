package main

// CLUSTER-10 tests: the /api/sync/conflicts list + resolve API must SURFACE the
// .conflict-* files that services/sync actually writes. The conflict-copy
// filename format (conflictCopyPath) is:
//
//	<stem>.conflict-<nodeID>-<YYYYMMDD-HHMMSS><ext>
//
// These tests build files in that exact shape (including a dashed node ID and an
// extension after the timestamp — the cases the OLD regex silently missed) and
// assert list + resolve(ours|theirs) behave correctly.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// conflictName mirrors services/sync.conflictCopyPath's basename format so the
// test exercises the REAL writer shape.
func conflictName(stem, ext, nodeID string, ts time.Time) string {
	return stem + ".conflict-" + nodeID + "-" + ts.UTC().Format("20060102-150405") + ext
}

func newConflictServer(t *testing.T, dataDir string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	registerConflictRoutes(mux, dataDir, nil) // nil notify svc is tolerated
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestConflictList_MatchesRealWriterFormat(t *testing.T) {
	dataDir := t.TempDir()
	ts := time.Date(2026, 5, 16, 14, 30, 22, 0, time.UTC)

	// Base files + their conflict copies, including the cases the old regex missed:
	//  - extension AFTER the timestamp
	//  - a node ID that itself contains a dash ("home-server")
	mustWrite(t, filepath.Join(dataDir, "notes.txt"), "mine")
	mustWrite(t, filepath.Join(dataDir, conflictName("notes", ".txt", "office", ts)), "theirs")

	mustWrite(t, filepath.Join(dataDir, "sub", "data.bin"), "mine")
	mustWrite(t, filepath.Join(dataDir, "sub", conflictName("data", ".bin", "home-server", ts)), "theirs")

	// A non-conflict file must be ignored.
	mustWrite(t, filepath.Join(dataDir, "regular.md"), "x")

	srv := newConflictServer(t, dataDir)
	res, err := http.Get(srv.URL + "/api/sync/conflicts")
	if err != nil {
		t.Fatalf("GET conflicts: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("status = %d", res.StatusCode)
	}
	var entries []struct {
		Path      string `json:"path"`
		BasePath  string `json:"base_path"`
		Node      string `json:"node"`
		Timestamp string `json:"timestamp"`
		Size      int64  `json:"size"`
	}
	if err := json.NewDecoder(res.Body).Decode(&entries); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 conflicts surfaced, got %d: %+v", len(entries), entries)
	}

	byNode := map[string]struct {
		base string
		ts   string
	}{}
	for _, e := range entries {
		byNode[e.Node] = struct {
			base string
			ts   string
		}{e.BasePath, e.Timestamp}
	}
	office, ok := byNode["office"]
	if !ok {
		t.Fatal("missing 'office' conflict")
	}
	if office.base != "notes.txt" {
		t.Errorf("office base_path = %q, want notes.txt", office.base)
	}
	if office.ts != "20260516-143022" {
		t.Errorf("office timestamp = %q, want 20260516-143022", office.ts)
	}
	hs, ok := byNode["home-server"]
	if !ok {
		t.Fatal("missing 'home-server' conflict (dashed node ID dropped?)")
	}
	if hs.base != filepath.Join("sub", "data.bin") {
		t.Errorf("home-server base_path = %q, want sub/data.bin", hs.base)
	}
}

func TestConflictResolve_KeepOurs(t *testing.T) {
	dataDir := t.TempDir()
	ts := time.Date(2026, 5, 16, 14, 30, 22, 0, time.UTC)
	basePath := filepath.Join(dataDir, "notes.txt")
	confRel := conflictName("notes", ".txt", "office", ts)
	confAbs := filepath.Join(dataDir, confRel)
	mustWrite(t, basePath, "mine")
	mustWrite(t, confAbs, "theirs")

	srv := newConflictServer(t, dataDir)
	resolveConflict(t, srv, confRel, "ours")

	// "ours": conflict copy deleted, base unchanged.
	if _, err := os.Stat(confAbs); !os.IsNotExist(err) {
		t.Fatal("keep-ours must delete the conflict copy")
	}
	if got := mustRead(t, basePath); got != "mine" {
		t.Fatalf("base file changed on keep-ours: %q", got)
	}
}

func TestConflictResolve_KeepTheirs(t *testing.T) {
	dataDir := t.TempDir()
	ts := time.Date(2026, 5, 16, 14, 30, 22, 0, time.UTC)
	basePath := filepath.Join(dataDir, "notes.txt")
	confRel := conflictName("notes", ".txt", "office", ts)
	confAbs := filepath.Join(dataDir, confRel)
	mustWrite(t, basePath, "mine")
	mustWrite(t, confAbs, "theirs")

	srv := newConflictServer(t, dataDir)
	resolveConflict(t, srv, confRel, "theirs")

	// "theirs": conflict copy promoted to the base path; conflict copy gone.
	if _, err := os.Stat(confAbs); !os.IsNotExist(err) {
		t.Fatal("keep-theirs must remove the conflict copy after promotion")
	}
	if got := mustRead(t, basePath); got != "theirs" {
		t.Fatalf("keep-theirs must overwrite base with theirs; got %q", got)
	}
}

func TestConflictResolve_RejectsPathTraversal(t *testing.T) {
	dataDir := t.TempDir()
	srv := newConflictServer(t, dataDir)

	body, _ := json.Marshal(map[string]string{"path": "../../etc/passwd", "keep": "ours"})
	res, err := http.Post(srv.URL+"/api/sync/conflicts/resolve", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != 400 {
		t.Fatalf("path traversal must be rejected with 400, got %d", res.StatusCode)
	}
}

// ── helpers ────────────────────────────────────────────────────────────────

func resolveConflict(t *testing.T, srv *httptest.Server, relPath, keep string) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"path": relPath, "keep": keep})
	res, err := http.Post(srv.URL+"/api/sync/conflicts/resolve", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST resolve: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		b, _ := json.Marshal(res.Status)
		t.Fatalf("resolve %s/%s status = %d (%s)", relPath, keep, res.StatusCode, b)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}
