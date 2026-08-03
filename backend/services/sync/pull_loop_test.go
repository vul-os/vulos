package sync

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// listableClient is an S3Client that can also enumerate, like *cluster.Client.
type listableClient struct {
	mu      sync.Mutex
	objects map[string][]byte
	lists   int
}

func newListableClient() *listableClient {
	return &listableClient{objects: map[string][]byte{}}
}

func (c *listableClient) PutEncrypted(_ context.Context, key string, data []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.objects[key] = append([]byte(nil), data...)
	return nil
}

func (c *listableClient) GetEncrypted(_ context.Context, key string) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	d, ok := c.objects[key]
	if !ok {
		return nil, os.ErrNotExist
	}
	return append([]byte(nil), d...), nil
}

func (c *listableClient) ListPrefix(_ context.Context, prefix string) ([]string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lists++
	var out []string
	for k := range c.objects {
		if strings.HasPrefix(k, prefix) {
			out = append(out, k)
		}
	}
	return out, nil
}

// putRemoteFile stages a file as if another node had uploaded it.
func (c *listableClient) putRemoteFile(t *testing.T, rel, content, nodeID string) {
	t.Helper()
	key := "files/" + rel
	if err := c.PutEncrypted(context.Background(), key, []byte(content)); err != nil {
		t.Fatal(err)
	}
	meta, err := json.Marshal(FileMeta{NodeID: nodeID, Timestamp: time.Now(), Hash: sha256Hex([]byte(content))})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.PutEncrypted(context.Background(), key+".meta", meta); err != nil {
		t.Fatal(err)
	}
}

func testSyncer(t *testing.T, client S3Client, pullEvery time.Duration) (*Syncer, string) {
	t.Helper()
	root := t.TempDir()
	cfg := Config{
		NodeID:            "node-local",
		VulosRoot:         root, // isolate to the temp dir — without this, absPath()
		DataDir:           filepath.Join(root, "data"),
		BrowserProfileDir: filepath.Join(root, "profiles"),
		IgnoreDir:         filepath.Join(root, "apps", "bin"),
		PullInterval:      pullEvery,
	}
	for _, d := range []string{cfg.DataDir, cfg.BrowserProfileDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	s, err := New(cfg, client)
	if err != nil {
		t.Fatal(err)
	}
	return s, root
}

// TestPullAllFetchesRemoteFiles is the regression this whole change exists for:
// before it, Pull had zero callers and a remote file never reached a box.
func TestPullAllFetchesRemoteFiles(t *testing.T) {
	c := newListableClient()
	c.putRemoteFile(t, "notes.txt", "from the other box", "node-remote")

	s, _ := testSyncer(t, c, time.Minute)
	if err := s.PullAll(context.Background()); err != nil {
		t.Fatalf("PullAll: %v", err)
	}

	got, err := os.ReadFile(s.absPath("notes.txt"))
	if err != nil {
		t.Fatalf("remote file did not land locally: %v", err)
	}
	if string(got) != "from the other box" {
		t.Fatalf("content = %q", got)
	}
}

// TestPullDoesNotDestroyALocalEdit is the property that made wiring this risky:
// Pull overwrites local state, so a box that edited a file while away must not
// lose that edit. The remote wins the canonical path, but the local version has
// to survive beside it.
func TestPullDoesNotDestroyALocalEdit(t *testing.T) {
	c := newListableClient()
	c.putRemoteFile(t, "doc.txt", "remote version", "node-remote")

	s, _ := testSyncer(t, c, time.Minute)
	local := s.absPath("doc.txt")
	if err := os.MkdirAll(filepath.Dir(local), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(local, []byte("my unsaved local edit"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := s.PullAll(context.Background()); err != nil {
		t.Fatalf("PullAll: %v", err)
	}

	got, err := os.ReadFile(local)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "remote version" {
		t.Fatalf("canonical path should hold the remote version, got %q", got)
	}

	entries, err := os.ReadDir(filepath.Dir(local))
	if err != nil {
		t.Fatal(err)
	}
	var conflict string
	for _, e := range entries {
		if strings.Contains(e.Name(), ".conflict-") {
			conflict = filepath.Join(filepath.Dir(local), e.Name())
		}
	}
	if conflict == "" {
		t.Fatal("local edit was destroyed: no conflict copy was written")
	}
	saved, err := os.ReadFile(conflict)
	if err != nil {
		t.Fatal(err)
	}
	if string(saved) != "my unsaved local edit" {
		t.Fatalf("conflict copy does not hold the local edit, got %q", saved)
	}
}

// TestPullSkipsOurOwnUploads: without this the box would re-download everything
// it just pushed, every interval, forever.
func TestPullSkipsOurOwnUploads(t *testing.T) {
	c := newListableClient()
	c.putRemoteFile(t, "mine.txt", "uploaded by me", "node-local")

	s, _ := testSyncer(t, c, time.Minute)
	if err := s.PullAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.absPath("mine.txt")); !os.IsNotExist(err) {
		t.Fatal("re-downloaded a file this node uploaded")
	}
}

// TestPullAllWithoutListerIsAnHonestError: a client that cannot enumerate makes
// downward sync impossible, and the caller must be able to tell that apart from
// "there was nothing to fetch".
func TestPullAllWithoutListerIsAnHonestError(t *testing.T) {
	s, _ := testSyncer(t, &pushOnlyClient{}, time.Minute)
	err := s.PullAll(context.Background())
	if err == nil {
		t.Fatal("want an error when the client cannot list")
	}
	if !strings.Contains(err.Error(), "downward sync unavailable") {
		t.Fatalf("error should name the consequence, got %v", err)
	}
}

type pushOnlyClient struct{}

func (pushOnlyClient) PutEncrypted(context.Context, string, []byte) error { return nil }
func (pushOnlyClient) GetEncrypted(context.Context, string) ([]byte, error) {
	return nil, os.ErrNotExist
}

// TestStartPullsAtBootAndOnTheTimer covers the wiring itself: a box that has
// been away needs the remote state on waking, not one interval later.
func TestStartPullsAtBootAndOnTheTimer(t *testing.T) {
	c := newListableClient()
	c.putRemoteFile(t, "boot.txt", "hello", "node-remote")

	s, _ := testSyncer(t, c, 60*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = s.Start(ctx) }()

	deadline := time.After(3 * time.Second)
	for {
		if _, err := os.Stat(s.absPath("boot.txt")); err == nil {
			break
		}
		select {
		case <-deadline:
			t.Fatal("Start never pulled at boot")
		case <-time.After(10 * time.Millisecond):
		}
	}

	// A file appearing later must arrive without a restart.
	c.putRemoteFile(t, "later.txt", "second", "node-remote")
	deadline = time.After(3 * time.Second)
	for {
		if _, err := os.Stat(s.absPath("later.txt")); err == nil {
			return
		}
		select {
		case <-deadline:
			t.Fatal("timer never fired a second pull")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// TestNegativePullIntervalStaysPushOnly preserves upload-only for a caller that
// deliberately asks for it. Zero cannot mean that: the production call site
// passes a bare Config{}, so zero has to mean "use the default".
func TestNegativePullIntervalStaysPushOnly(t *testing.T) {
	c := newListableClient()
	c.putRemoteFile(t, "x.txt", "content", "node-remote")

	s, _ := testSyncer(t, c, -1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = s.Start(ctx) }()
	time.Sleep(200 * time.Millisecond)

	c.mu.Lock()
	lists := c.lists
	c.mu.Unlock()
	if lists != 0 {
		t.Fatalf("negative PullInterval should never list, got %d lists", lists)
	}
}
