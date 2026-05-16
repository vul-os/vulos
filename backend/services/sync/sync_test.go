package sync

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeS3 is an in-memory S3Client for unit tests.
// It supports PutEncrypted and GetEncrypted without any network access.
type fakeS3 struct {
	mu      sync.Mutex
	objects map[string][]byte
	// putErr and getErr can be set to inject errors for specific keys.
	putErr func(key string) error
	getErr func(key string) error
}

func newFakeS3() *fakeS3 {
	return &fakeS3{objects: make(map[string][]byte)}
}

func (f *fakeS3) PutEncrypted(_ context.Context, key string, data []byte) error {
	if f.putErr != nil {
		if err := f.putErr(key); err != nil {
			return err
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := make([]byte, len(data))
	copy(cp, data)
	f.objects[key] = cp
	return nil
}

func (f *fakeS3) GetEncrypted(_ context.Context, key string) ([]byte, error) {
	if f.getErr != nil {
		if err := f.getErr(key); err != nil {
			return nil, err
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.objects[key]
	if !ok {
		return nil, fmt.Errorf("key not found: %s", key)
	}
	cp := make([]byte, len(v))
	copy(cp, v)
	return cp, nil
}

func (f *fakeS3) allKeys() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	ks := make([]string, 0, len(f.objects))
	for k := range f.objects {
		ks = append(ks, k)
	}
	return ks
}

// newTestSyncer creates a Syncer backed by fakeS3 inside a temp directory.
// The temp dir acts as the VulosRoot, containing: data/, db/browser-profiles/,
// apps/bin/.
func newTestSyncer(t *testing.T, s3 *fakeS3) (*Syncer, string) {
	t.Helper()
	root := t.TempDir()

	dataDir := filepath.Join(root, "data")
	browserDir := filepath.Join(root, "db", "browser-profiles")
	ignoreDir := filepath.Join(root, "apps", "bin")

	for _, d := range []string{dataDir, browserDir, ignoreDir} {
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatal(err)
		}
	}

	cfg := Config{
		NodeID:            "test-node",
		VulosRoot:         root, // override so relPath/absPath work in tests
		DataDir:           dataDir,
		BrowserProfileDir: browserDir,
		IgnoreDir:         ignoreDir,
	}

	snc, err := New(cfg, s3)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return snc, root
}

// helperWrite writes content to path, creating intermediate dirs.
func helperWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// TestUploadPutsFileAndMeta verifies that upload() stores both the file
// content and its .meta sidecar in S3.
func TestUploadPutsFileAndMeta(t *testing.T) {
	s3 := newFakeS3()
	snc, _ := newTestSyncer(t, s3)

	path := filepath.Join(snc.cfg.DataDir, "docs", "notes.txt")
	helperWrite(t, path, "hello world")

	ctx := context.Background()
	if err := snc.upload(ctx, path); err != nil {
		t.Fatalf("upload: %v", err)
	}

	// Collect all S3 keys that were written.
	ks := s3.allKeys()
	var fileKey, metaKey string
	for _, k := range ks {
		if strings.HasSuffix(k, ".meta") {
			metaKey = k
		} else {
			fileKey = k
		}
	}

	if fileKey == "" {
		t.Fatal("expected a file key in S3, got none")
	}
	if metaKey == "" {
		t.Fatal("expected a .meta key in S3, got none")
	}
	if !strings.HasPrefix(fileKey, "files/") {
		t.Errorf("file key %q should start with files/", fileKey)
	}
	if metaKey != fileKey+".meta" {
		t.Errorf("meta key %q should be %q", metaKey, fileKey+".meta")
	}
}

// TestPullOverwritesUnchangedFile verifies that when the local file has not
// been edited since the last pull, the remote version overwrites it silently.
func TestPullOverwritesUnchangedFile(t *testing.T) {
	s3 := newFakeS3()
	ctx := context.Background()

	tmpDir := t.TempDir()
	dataDir := filepath.Join(tmpDir, "data")
	_ = os.MkdirAll(dataDir, 0755)

	// Simulate: node "remote-node" uploads a file.
	remoteContent := []byte("remote version")
	remoteHash := sha256Hex(remoteContent)
	meta := FileMeta{
		NodeID:    "remote-node",
		Timestamp: time.Now().UTC(),
		Hash:      remoteHash,
	}
	metaJSON, _ := json.Marshal(meta)
	s3.objects["files/data/hello.txt"] = remoteContent
	s3.objects["files/data/hello.txt.meta"] = metaJSON

	// Local file: exists and is unchanged since the last pull.
	localContent := []byte("local unchanged")
	localHash := sha256Hex(localContent)

	localPath := filepath.Join(dataDir, "hello.txt")
	helperWrite(t, localPath, string(localContent))

	snc := &Syncer{
		cfg: Config{
			NodeID:    "test-node",
			VulosRoot: tmpDir,
			DataDir:   dataDir,
			IgnoreDir: filepath.Join(tmpDir, "apps", "bin"),
		},
		client:   s3,
		lastHash: map[string]string{localPath: localHash}, // marks file as unchanged
	}

	if err := snc.pullOne(ctx, "files/data/hello.txt"); err != nil {
		t.Fatalf("pullOne: %v", err)
	}

	// The local file should now contain the remote content.
	got, err := os.ReadFile(localPath)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != string(remoteContent) {
		t.Errorf("expected remote content %q, got %q", remoteContent, got)
	}

	// No conflict copies should exist.
	entries, _ := os.ReadDir(filepath.Dir(localPath))
	for _, e := range entries {
		if strings.Contains(e.Name(), ".conflict-") {
			t.Errorf("unexpected conflict copy: %s", e.Name())
		}
	}
}

// TestPullCreatesConflictCopyOnDivergentEdit verifies that when both local
// and remote edited the same file independently, a conflict copy is created
// and no data is lost.
func TestPullCreatesConflictCopyOnDivergentEdit(t *testing.T) {
	s3 := newFakeS3()
	ctx := context.Background()

	tmpDir := t.TempDir()
	dataDir := filepath.Join(tmpDir, "data")
	_ = os.MkdirAll(dataDir, 0755)

	remoteContent := []byte("remote divergent edit")
	remoteHash := sha256Hex(remoteContent)
	meta := FileMeta{
		NodeID:    "office",
		Timestamp: time.Date(2026, 5, 16, 14, 30, 22, 0, time.UTC),
		Hash:      remoteHash,
	}
	metaJSON, _ := json.Marshal(meta)
	s3.objects["files/data/notes.txt"] = remoteContent
	s3.objects["files/data/notes.txt.meta"] = metaJSON

	// Local file has been edited after the last pull.
	localContent := []byte("local divergent edit")
	localPath := filepath.Join(dataDir, "notes.txt")
	helperWrite(t, localPath, string(localContent))

	// knownHash is what the file looked like at last pull — different from current.
	knownHash := sha256Hex([]byte("original content before both edits"))

	snc := &Syncer{
		cfg: Config{
			NodeID:    "home",
			VulosRoot: tmpDir,
			DataDir:   dataDir,
			IgnoreDir: filepath.Join(tmpDir, "apps", "bin"),
		},
		client:   s3,
		lastHash: map[string]string{localPath: knownHash},
	}

	if err := snc.pullOne(ctx, "files/data/notes.txt"); err != nil {
		t.Fatalf("pullOne: %v", err)
	}

	// Canonical file should be the remote version.
	got, err := os.ReadFile(localPath)
	if err != nil {
		t.Fatalf("read canonical: %v", err)
	}
	if string(got) != string(remoteContent) {
		t.Errorf("canonical file: expected %q, got %q", remoteContent, got)
	}

	// A conflict copy must exist containing the old local content.
	dir := filepath.Dir(localPath)
	entries, _ := os.ReadDir(dir)
	var conflictFile string
	for _, e := range entries {
		if strings.Contains(e.Name(), ".conflict-") {
			conflictFile = filepath.Join(dir, e.Name())
			break
		}
	}
	if conflictFile == "" {
		t.Fatal("expected a conflict copy to be created, none found")
	}

	conflictData, err := os.ReadFile(conflictFile)
	if err != nil {
		t.Fatalf("read conflict copy: %v", err)
	}
	if string(conflictData) != string(localContent) {
		t.Errorf("conflict copy: expected %q, got %q", localContent, conflictData)
	}

	// Verify the conflict copy name contains the remote node ID.
	base := filepath.Base(conflictFile)
	if !strings.Contains(base, "office") {
		t.Errorf("conflict copy name %q should contain source node ID 'office'", base)
	}
}

// TestIgnoredDirSkipped verifies that files under apps/bin are never uploaded.
func TestIgnoredDirSkipped(t *testing.T) {
	s3 := newFakeS3()
	tmpDir := t.TempDir()

	ignoreDir := filepath.Join(tmpDir, "apps", "bin")
	_ = os.MkdirAll(ignoreDir, 0755)

	snc := &Syncer{
		cfg: Config{
			NodeID:    "test-node",
			VulosRoot: tmpDir,
			DataDir:   filepath.Join(tmpDir, "data"),
			IgnoreDir: ignoreDir,
		},
		client:   s3,
		lastHash: make(map[string]string),
	}

	// A path directly under apps/bin.
	binPath := filepath.Join(ignoreDir, "myapp")
	if !snc.isIgnored(binPath) {
		t.Error("expected apps/bin/myapp to be ignored")
	}

	// A path under apps/bin/sub.
	subPath := filepath.Join(ignoreDir, "sub", "file")
	if !snc.isIgnored(subPath) {
		t.Error("expected apps/bin/sub/file to be ignored")
	}

	// The ignore dir itself.
	if !snc.isIgnored(ignoreDir) {
		t.Error("expected apps/bin itself to be ignored")
	}

	// A regular data path should NOT be ignored.
	dataPath := filepath.Join(tmpDir, "data", "file.txt")
	if snc.isIgnored(dataPath) {
		t.Error("data/file.txt should not be ignored")
	}
}

// TestConflictCopyPath verifies the naming convention for conflict copies.
func TestConflictCopyPath(t *testing.T) {
	ts := time.Date(2026, 5, 16, 14, 30, 22, 0, time.UTC)
	path := "/home/alice/.vulos/data/notes.txt"
	got := conflictCopyPath(path, "office", ts)
	want := "/home/alice/.vulos/data/notes.conflict-office-20260516-143022.txt"
	if got != want {
		t.Errorf("conflictCopyPath: got %q, want %q", got, want)
	}
}

// TestConflictCopyPathNoExtension verifies conflict naming for files without
// an extension.
func TestConflictCopyPathNoExtension(t *testing.T) {
	ts := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	path := "/home/alice/.vulos/data/Makefile"
	got := conflictCopyPath(path, "laptop", ts)
	want := "/home/alice/.vulos/data/Makefile.conflict-laptop-20260102-030405"
	if got != want {
		t.Errorf("conflictCopyPath: got %q, want %q", got, want)
	}
}

// TestPullSkipsOwnNode verifies that files uploaded by this node are not
// re-applied during pull (avoids overwriting with the same data needlessly).
func TestPullSkipsOwnNode(t *testing.T) {
	s3 := newFakeS3()
	ctx := context.Background()

	tmpDir := t.TempDir()
	dataDir := filepath.Join(tmpDir, "data")
	_ = os.MkdirAll(dataDir, 0755)

	content := []byte("uploaded by self")
	meta := FileMeta{
		NodeID:    "home", // same as the syncer's NodeID
		Timestamp: time.Now().UTC(),
		Hash:      sha256Hex(content),
	}
	metaJSON, _ := json.Marshal(meta)
	s3.objects["files/data/self.txt"] = content
	s3.objects["files/data/self.txt.meta"] = metaJSON

	localPath := filepath.Join(dataDir, "self.txt")
	helperWrite(t, localPath, "different local version")

	snc := &Syncer{
		cfg: Config{
			NodeID:    "home", // same node — should be skipped
			VulosRoot: tmpDir,
			DataDir:   dataDir,
			IgnoreDir: filepath.Join(tmpDir, "apps", "bin"),
		},
		client:   s3,
		lastHash: make(map[string]string),
	}

	if err := snc.pullOne(ctx, "files/data/self.txt"); err != nil {
		t.Fatalf("pullOne: %v", err)
	}

	// File should be unchanged — not overwritten by our own upload.
	got, err := os.ReadFile(localPath)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != "different local version" {
		t.Errorf("expected unchanged local content, got %q", got)
	}
}
