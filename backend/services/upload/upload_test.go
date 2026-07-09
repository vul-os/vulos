package upload

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// memSink records the last stored blob per (user,parent,name) and returns a
// deterministic node id. It also lets a test force an error to exercise the
// finalize-failure path.
type memSink struct {
	mu      sync.Mutex
	stored  map[string][]byte
	failNext bool
}

func newMemSink() *memSink { return &memSink{stored: map[string][]byte{}} }

func (s *memSink) Store(ctx context.Context, userID, parentID, name, contentType string, r io.Reader, size int64) (string, error) {
	s.mu.Lock()
	fail := s.failNext
	s.failNext = false
	s.mu.Unlock()
	if fail {
		return "", errors.New("sink boom")
	}
	b, err := io.ReadAll(r)
	if err != nil {
		return "", err
	}
	if int64(len(b)) != size {
		return "", fmt.Errorf("size mismatch: read %d, want %d", len(b), size)
	}
	s.mu.Lock()
	s.stored[userID+"/"+parentID+"/"+name] = b
	s.mu.Unlock()
	return "node-" + name, nil
}

func (s *memSink) get(user, parent, name string) ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.stored[user+"/"+parent+"/"+name]
	return b, ok
}

func newMgr(t *testing.T, sink Sink, cfg Config) *Manager {
	t.Helper()
	dir := t.TempDir()
	if cfg.StateDir == "" {
		cfg.StateDir = dir
	}
	m, err := New(filepath.Join(dir, "uploads.db"), sink, cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { m.Close() })
	return m
}

func hexSHA(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func TestChunkedUploadHappyPath(t *testing.T) {
	sink := newMemSink()
	m := newMgr(t, sink, Config{})
	data := bytes.Repeat([]byte("abcdefghij"), 500) // 5000 bytes
	u, err := m.Create("alice", CreateParams{Length: int64(len(data)), Name: "file.bin", SHA256: hexSHA(data)})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if u.Offset != 0 {
		t.Fatalf("new upload offset = %d, want 0", u.Offset)
	}
	// Upload in 1000-byte chunks.
	off := int64(0)
	for off < int64(len(data)) {
		end := off + 1000
		if end > int64(len(data)) {
			end = int64(len(data))
		}
		chunk := data[off:end]
		res, err := m.Patch(context.Background(), "alice", u.ID, off, bytes.NewReader(chunk), hexSHA(chunk))
		if err != nil {
			t.Fatalf("Patch @%d: %v", off, err)
		}
		off = res.Offset
		if off == int64(len(data)) {
			if !res.Complete {
				t.Fatalf("last chunk not complete")
			}
			if res.NodeID != "node-file.bin" {
				t.Fatalf("node id = %q", res.NodeID)
			}
		} else if res.Complete {
			t.Fatalf("premature complete at offset %d", off)
		}
	}
	got, ok := sink.get("alice", "", "file.bin")
	if !ok {
		t.Fatal("blob not stored in sink")
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("stored bytes differ (len %d vs %d)", len(got), len(data))
	}
}

func TestResumeAfterInterruption(t *testing.T) {
	sink := newMemSink()
	m := newMgr(t, sink, Config{})
	data := bytes.Repeat([]byte("X"), 3000)
	u, _ := m.Create("bob", CreateParams{Length: 3000, Name: "resume.bin"})
	// First chunk lands.
	if _, err := m.Patch(context.Background(), "bob", u.ID, 0, bytes.NewReader(data[:1000]), ""); err != nil {
		t.Fatalf("chunk1: %v", err)
	}
	// Simulate a dropped connection: client HEADs to learn the offset.
	h, err := m.Head("bob", u.ID)
	if err != nil {
		t.Fatalf("Head: %v", err)
	}
	if h.Offset != 1000 {
		t.Fatalf("resume offset = %d, want 1000", h.Offset)
	}
	// Resume from the reported offset.
	if _, err := m.Patch(context.Background(), "bob", u.ID, h.Offset, bytes.NewReader(data[1000:]), ""); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if got, ok := sink.get("bob", "", "resume.bin"); !ok || !bytes.Equal(got, data) {
		t.Fatalf("resumed upload mismatch")
	}
}

func TestOffsetConflictRejected(t *testing.T) {
	m := newMgr(t, newMemSink(), Config{})
	u, _ := m.Create("alice", CreateParams{Length: 100, Name: "x"})
	// Correct first chunk.
	if _, err := m.Patch(context.Background(), "alice", u.ID, 0, bytes.NewReader(make([]byte, 50)), ""); err != nil {
		t.Fatalf("chunk1: %v", err)
	}
	// A retried/misordered chunk at the wrong offset must 409.
	if _, err := m.Patch(context.Background(), "alice", u.ID, 0, bytes.NewReader(make([]byte, 50)), ""); !errors.Is(err, ErrOffsetConflict) {
		t.Fatalf("want ErrOffsetConflict, got %v", err)
	}
	// Also a forward gap.
	if _, err := m.Patch(context.Background(), "alice", u.ID, 80, bytes.NewReader(make([]byte, 20)), ""); !errors.Is(err, ErrOffsetConflict) {
		t.Fatalf("gap: want ErrOffsetConflict, got %v", err)
	}
}

func TestPerChunkChecksumMismatch(t *testing.T) {
	m := newMgr(t, newMemSink(), Config{})
	u, _ := m.Create("alice", CreateParams{Length: 10, Name: "x"})
	// Wrong checksum for the chunk → rejected, and no bytes committed.
	_, err := m.Patch(context.Background(), "alice", u.ID, 0, bytes.NewReader([]byte("0123456789")), hexSHA([]byte("different")))
	if !errors.Is(err, ErrChecksum) {
		t.Fatalf("want ErrChecksum, got %v", err)
	}
	if h, _ := m.Head("alice", u.ID); h.Offset != 0 {
		t.Fatalf("corrupt chunk advanced offset to %d", h.Offset)
	}
}

func TestWholeFileChecksumMismatch(t *testing.T) {
	sink := newMemSink()
	m := newMgr(t, sink, Config{})
	data := []byte("hello world!!")
	u, _ := m.Create("alice", CreateParams{Length: int64(len(data)), Name: "x", SHA256: hexSHA([]byte("not the data"))})
	_, err := m.Patch(context.Background(), "alice", u.ID, 0, bytes.NewReader(data), "")
	if !errors.Is(err, ErrChecksum) {
		t.Fatalf("want ErrChecksum on finalize, got %v", err)
	}
	// Nothing promoted to the sink on a bad whole-file digest.
	if _, ok := sink.get("alice", "", "x"); ok {
		t.Fatal("corrupt file was promoted to Drive")
	}
}

func TestCrossOwnerIsolation(t *testing.T) {
	m := newMgr(t, newMemSink(), Config{})
	u, _ := m.Create("alice", CreateParams{Length: 10, Name: "secret"})
	// Mallory cannot HEAD, PATCH, or DELETE alice's upload.
	if _, err := m.Head("mallory", u.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Head cross-owner: want ErrNotFound, got %v", err)
	}
	if _, err := m.Patch(context.Background(), "mallory", u.ID, 0, bytes.NewReader([]byte("x")), ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Patch cross-owner: want ErrNotFound, got %v", err)
	}
	if err := m.Delete("mallory", u.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Delete cross-owner: want ErrNotFound, got %v", err)
	}
	// Alice's upload is untouched.
	if h, err := m.Head("alice", u.ID); err != nil || h.Offset != 0 {
		t.Fatalf("owner Head after cross-owner attempts: %v off=%d", err, h.Offset)
	}
}

func TestOverlongChunkRejected(t *testing.T) {
	m := newMgr(t, newMemSink(), Config{})
	u, _ := m.Create("alice", CreateParams{Length: 10, Name: "x"})
	// A chunk that claims offset 0 but pushes past the declared length.
	_, err := m.Patch(context.Background(), "alice", u.ID, 0, bytes.NewReader(make([]byte, 20)), "")
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("want ErrTooLarge, got %v", err)
	}
}

func TestCreateLengthBounds(t *testing.T) {
	m := newMgr(t, newMemSink(), Config{MaxUploadBytes: 100})
	if _, err := m.Create("alice", CreateParams{Length: 101, Name: "x"}); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("over-cap length: want ErrTooLarge, got %v", err)
	}
	if _, err := m.Create("alice", CreateParams{Length: -1, Name: "x"}); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("negative length: want ErrTooLarge, got %v", err)
	}
	if _, err := m.Create("alice", CreateParams{Length: 10, Name: ""}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing name: want ErrInvalid, got %v", err)
	}
	if _, err := m.Create("alice", CreateParams{Length: 10, Name: "x", SHA256: "zzz"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad sha: want ErrInvalid, got %v", err)
	}
}

func TestSweepRemovesAbandonedPartials(t *testing.T) {
	sink := newMemSink()
	// Controllable clock.
	now := time.Now()
	cfg := Config{TTL: time.Hour, now: func() time.Time { return now }}
	m := newMgr(t, sink, cfg)
	u, _ := m.Create("alice", CreateParams{Length: 1000, Name: "abandoned"})
	m.Patch(context.Background(), "alice", u.ID, 0, bytes.NewReader(make([]byte, 100)), "")
	stage := m.stagePath(u.ID)
	if _, err := os.Stat(stage); err != nil {
		t.Fatalf("staging file missing pre-sweep: %v", err)
	}
	// Not yet expired.
	if n, _ := m.Sweep(); n != 0 {
		t.Fatalf("swept %d before TTL", n)
	}
	// Advance past TTL.
	now = now.Add(2 * time.Hour)
	n, err := m.Sweep()
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if n != 1 {
		t.Fatalf("swept %d, want 1", n)
	}
	if _, err := m.Head("alice", u.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("upload still present after sweep: %v", err)
	}
	if _, err := os.Stat(stage); !os.IsNotExist(err) {
		t.Fatalf("staging file not removed: %v", err)
	}
}

func TestDeleteDiscardsPartial(t *testing.T) {
	m := newMgr(t, newMemSink(), Config{})
	u, _ := m.Create("alice", CreateParams{Length: 100, Name: "x"})
	stage := m.stagePath(u.ID)
	if err := m.Delete("alice", u.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := m.Head("alice", u.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("still present after delete")
	}
	if _, err := os.Stat(stage); !os.IsNotExist(err) {
		t.Fatalf("staging not removed after delete")
	}
}

func TestFinalizeFailureIsResumable(t *testing.T) {
	sink := newMemSink()
	sink.failNext = true
	m := newMgr(t, sink, Config{})
	data := []byte("some bytes here")
	u, _ := m.Create("alice", CreateParams{Length: int64(len(data)), Name: "retry.bin"})
	// The completing PATCH fails in the sink but the bytes are staged.
	res, err := m.Patch(context.Background(), "alice", u.ID, 0, bytes.NewReader(data), "")
	if err == nil {
		t.Fatal("expected finalize error")
	}
	// Offset advanced to full even though promotion failed.
	if res == nil || res.Offset != int64(len(data)) {
		t.Fatalf("offset after failed finalize: %+v", res)
	}
	// A zero-byte PATCH at the full offset re-drives finalize; now it succeeds.
	res2, err := m.Patch(context.Background(), "alice", u.ID, int64(len(data)), bytes.NewReader(nil), "")
	if err != nil {
		t.Fatalf("re-finalize: %v", err)
	}
	if !res2.Complete {
		t.Fatalf("re-finalize did not complete")
	}
	if got, ok := sink.get("alice", "", "retry.bin"); !ok || !bytes.Equal(got, data) {
		t.Fatalf("re-finalized bytes mismatch")
	}
}

func TestPersistedStateSurvivesReopen(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "uploads.db")
	sink := newMemSink()
	m1, err := New(dbPath, sink, Config{StateDir: dir})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	u, _ := m1.Create("alice", CreateParams{Length: 2000, Name: "persist.bin"})
	m1.Patch(context.Background(), "alice", u.ID, 0, bytes.NewReader(make([]byte, 800)), "")
	m1.Close()
	// Reopen — the committed offset must survive (resume after box restart).
	m2, err := New(dbPath, sink, Config{StateDir: dir})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer m2.Close()
	h, err := m2.Head("alice", u.ID)
	if err != nil {
		t.Fatalf("Head after reopen: %v", err)
	}
	if h.Offset != 800 {
		t.Fatalf("offset after reopen = %d, want 800", h.Offset)
	}
}

func TestConcurrentPatchesSerialized(t *testing.T) {
	// Two goroutines racing PATCH on the SAME upload: exactly one should win the
	// offset-0 slot; the other must 409. No interleaved writes / data race.
	m := newMgr(t, newMemSink(), Config{})
	u, _ := m.Create("alice", CreateParams{Length: 2000, Name: "race.bin"})
	var wg sync.WaitGroup
	var okCount, conflictCount int
	var mu sync.Mutex
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := m.Patch(context.Background(), "alice", u.ID, 0, bytes.NewReader(make([]byte, 1000)), "")
			mu.Lock()
			if err == nil {
				okCount++
			} else if errors.Is(err, ErrOffsetConflict) {
				conflictCount++
			}
			mu.Unlock()
		}()
	}
	wg.Wait()
	if okCount != 1 || conflictCount != 1 {
		t.Fatalf("racing PATCHes: ok=%d conflict=%d (want 1/1)", okCount, conflictCount)
	}
}

// TestCreate_PerOwnerCap proves one owner cannot pin unbounded in-flight uploads
// (disk-fill DoS guard): past MaxIncompletePerOwner, Create returns
// ErrTooManyUploads, but completing or cancelling an upload frees a slot, and a
// DIFFERENT owner is unaffected (the cap is per-owner).
func TestCreate_PerOwnerCap(t *testing.T) {
	m := newMgr(t, newMemSink(), Config{})
	var ids []string
	for i := 0; i < MaxIncompletePerOwner; i++ {
		u, err := m.Create("alice", CreateParams{Length: 10, Name: fmt.Sprintf("f%d", i)})
		if err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
		ids = append(ids, u.ID)
	}
	// One past the cap → rejected.
	if _, err := m.Create("alice", CreateParams{Length: 10, Name: "over"}); !errors.Is(err, ErrTooManyUploads) {
		t.Fatalf("over-cap create err = %v, want ErrTooManyUploads", err)
	}
	// A DIFFERENT owner is unaffected (per-owner cap).
	if _, err := m.Create("bob", CreateParams{Length: 10, Name: "bobfile"}); err != nil {
		t.Fatalf("other owner blocked by alice's cap: %v", err)
	}
	// Cancelling one of alice's frees a slot.
	if err := m.Delete("alice", ids[0]); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := m.Create("alice", CreateParams{Length: 10, Name: "after-cancel"}); err != nil {
		t.Fatalf("create after freeing a slot failed: %v", err)
	}
}
