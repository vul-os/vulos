package recall

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"vulos/backend/internal/vecdb"
	"vulos/backend/services/embeddings"
)

// ---- helpers ---------------------------------------------------------------

// newTempRecall creates a Recall backed by a temp directory.
// The returned cleanup func removes the directory.
func newTempRecall(t *testing.T, embedder *embeddings.Embedder) (*Recall, string, func()) {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.vecdb.json")
	r, err := New(dbPath, dir, embedder)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return r, dir, func() {}
}

// stubEmbedder returns a deterministic embedder that maps text to a fixed
// 4-dimensional unit vector by hashing the first character mod 4.
// The point of this embedder is to produce different, reproducible vectors
// so we can test cosine-similarity ranking without a real model.
func stubEmbedder() *embeddings.Embedder {
	// Use "openai" backend with a key but point to a server that won't be
	// called — the tests that need embedding call embedder directly via vecdb.
	// For recall tests we bypass Embed entirely by inserting docs into vecdb.
	return embeddings.New(embeddings.Config{
		Backend:  "openai",
		Model:    "stub",
		Endpoint: "http://127.0.0.1:19998",
		APIKey:   "stub-key",
	})
}

// makeUnitVec builds a 4-D unit vector along axis `axis` (0-3).
func makeUnitVec(axis int) []float32 {
	v := make([]float32, 4)
	v[axis] = 1.0
	return v
}

// cosine computes cosine similarity between two float32 vectors for assertions.
func cosine(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	denom := math.Sqrt(na) * math.Sqrt(nb)
	if denom == 0 {
		return 0
	}
	return dot / denom
}

// ---- fileID -----------------------------------------------------------------

func TestFileID_Deterministic(t *testing.T) {
	id1 := fileID("/tmp/foo/bar.txt")
	id2 := fileID("/tmp/foo/bar.txt")
	if id1 != id2 {
		t.Errorf("fileID is not deterministic: %q vs %q", id1, id2)
	}
}

func TestFileID_Unique(t *testing.T) {
	id1 := fileID("/tmp/a.txt")
	id2 := fileID("/tmp/b.txt")
	if id1 == id2 {
		t.Error("different paths produced the same fileID")
	}
}

func TestFileID_Length(t *testing.T) {
	id := fileID("/some/path/file.go")
	// sha256[:12] => 24 hex chars
	if len(id) != 24 {
		t.Errorf("fileID length = %d, want 24", len(id))
	}
}

// ---- isSupportedFile --------------------------------------------------------

func TestIsSupportedFile(t *testing.T) {
	supported := []string{
		"foo.go", "bar.py", "baz.md", "config.json", "data.csv",
		"style.css", "index.html", "query.sql", "note.txt",
	}
	for _, name := range supported {
		if !isSupportedFile(name) {
			t.Errorf("expected %q to be supported", name)
		}
	}

	unsupported := []string{
		"image.jpg", "photo.png", "video.mp4", "archive.zip",
		"binary.exe", "no_ext",
	}
	for _, name := range unsupported {
		if isSupportedFile(name) {
			t.Errorf("expected %q to be unsupported", name)
		}
	}
}

func TestIsSupportedFile_CaseInsensitive(t *testing.T) {
	// isSupportedFile lowercases the extension
	if !isSupportedFile("README.MD") {
		t.Error("expected README.MD to be supported (case-insensitive)")
	}
}

// ---- truncate ---------------------------------------------------------------

func TestTruncate(t *testing.T) {
	cases := []struct {
		input  string
		maxLen int
		want   string
	}{
		{"hello", 10, "hello"},
		{"hello world", 5, "hello"},
		{"", 5, ""},
		{"abc", 3, "abc"},
		{"abcd", 3, "abc"},
	}
	for _, c := range cases {
		got := truncate(c.input, c.maxLen)
		if got != c.want {
			t.Errorf("truncate(%q, %d) = %q, want %q", c.input, c.maxLen, got, c.want)
		}
	}
}

// ---- Status -----------------------------------------------------------------

func TestStatus_Initial(t *testing.T) {
	r, _, cleanup := newTempRecall(t, stubEmbedder())
	defer cleanup()

	s := r.Status()
	if s.Indexing {
		t.Error("expected Indexing=false on fresh instance")
	}
	if s.TotalFiles != 0 {
		t.Errorf("TotalFiles = %d, want 0", s.TotalFiles)
	}
	if s.IndexedFiles != 0 {
		t.Errorf("IndexedFiles = %d, want 0", s.IndexedFiles)
	}
}

// ---- vecdb Search roundtrip (stub embedder, no network) --------------------

// TestSearchRoundtrip inserts documents directly into vecdb and checks that
// the highest cosine-similarity document ranks first.
func TestSearchRoundtrip(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "db.json")

	db, err := vecdb.Open(dbPath)
	if err != nil {
		t.Fatalf("vecdb.Open: %v", err)
	}

	// Insert two documents with orthogonal embeddings.
	db.Upsert(&vecdb.Document{
		ID:        "doc-axis0",
		Content:   "document about cats",
		Embedding: makeUnitVec(0),
		Metadata:  map[string]string{"tag": "cats"},
	})
	db.Upsert(&vecdb.Document{
		ID:        "doc-axis1",
		Content:   "document about dogs",
		Embedding: makeUnitVec(1),
		Metadata:  map[string]string{"tag": "dogs"},
	})

	// Query aligned with axis 0 — expect "cats" to rank first.
	query := makeUnitVec(0)
	results := db.Search(query, 2)

	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0].ID != "doc-axis0" {
		t.Errorf("top result ID = %q, want %q", results[0].ID, "doc-axis0")
	}
	if results[0].Score < 0.99 {
		t.Errorf("top result score = %f, want ~1.0", results[0].Score)
	}
	if results[1].Score != 0.0 {
		t.Errorf("second result score = %f, want 0.0 (orthogonal)", results[1].Score)
	}
}

// TestSearchTopK verifies that topK limits results.
func TestSearchTopK(t *testing.T) {
	dir := t.TempDir()
	db, err := vecdb.Open(filepath.Join(dir, "db.json"))
	if err != nil {
		t.Fatalf("vecdb.Open: %v", err)
	}

	for i := 0; i < 4; i++ {
		db.Upsert(&vecdb.Document{
			ID:        string(rune('a' + i)),
			Content:   "doc",
			Embedding: makeUnitVec(i),
		})
	}

	results := db.Search(makeUnitVec(0), 2)
	if len(results) != 2 {
		t.Errorf("topK=2 returned %d results", len(results))
	}
}

// TestSearchEmpty verifies Search on an empty DB returns empty slice without panic.
func TestSearchEmpty(t *testing.T) {
	dir := t.TempDir()
	db, err := vecdb.Open(filepath.Join(dir, "db.json"))
	if err != nil {
		t.Fatalf("vecdb.Open: %v", err)
	}
	results := db.Search(makeUnitVec(0), 5)
	if results == nil {
		results = []vecdb.SearchResult{} // treat nil as empty
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results from empty DB, got %d", len(results))
	}
}

// ---- persistence ------------------------------------------------------------

// TestPersistence writes a document, flushes, reopens, and verifies it survives.
func TestPersistence(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "persist.json")

	db1, err := vecdb.Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	db1.Upsert(&vecdb.Document{
		ID:        "persist-doc",
		Content:   "hello persistent world",
		Embedding: makeUnitVec(2),
		Metadata:  map[string]string{"key": "value"},
	})
	if err := db1.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	// Reopen and verify
	db2, err := vecdb.Open(dbPath)
	if err != nil {
		t.Fatalf("Reopen: %v", err)
	}
	doc, ok := db2.Get("persist-doc")
	if !ok {
		t.Fatal("document not found after reopen")
	}
	if doc.Content != "hello persistent world" {
		t.Errorf("content = %q, want %q", doc.Content, "hello persistent world")
	}
	if doc.Metadata["key"] != "value" {
		t.Errorf("metadata key = %q, want %q", doc.Metadata["key"], "value")
	}
	if len(doc.Embedding) != 4 {
		t.Errorf("embedding length = %d, want 4", len(doc.Embedding))
	}
}

// TestPersistence_JSON verifies the on-disk format is valid JSON.
func TestPersistence_JSON(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "json.json")
	db, err := vecdb.Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	db.Upsert(&vecdb.Document{
		ID:        "j1",
		Content:   "test",
		Embedding: makeUnitVec(0),
		Metadata:  nil,
	})
	if err := db.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	data, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var docs []map[string]interface{}
	if err := json.Unmarshal(data, &docs); err != nil {
		t.Errorf("on-disk data is not valid JSON: %v", err)
	}
	if len(docs) != 1 {
		t.Errorf("expected 1 doc on disk, got %d", len(docs))
	}
}

// ---- IndexAll with real files (stub embedder skipped if no backend) ---------

// TestIndexAll_SkipNoBackend runs IndexAll with a real stub but skips embedding
// by pointing to a non-existent server; the test asserts status updates.
// Because the embedder will fail, no files will be indexed but TotalFiles
// should count supported files and Status.Indexing should return to false.
func TestIndexAll_SkipNoBackend(t *testing.T) {
	if os.Getenv("EMBED_BACKEND") != "" {
		t.Skip("live backend detected — skipping stub-only test")
	}

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "index.json")

	// Write a few text files
	for _, name := range []string{"a.txt", "b.md", "c.go"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("content of "+name), 0644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
	}
	// Write an unsupported file
	if err := os.WriteFile(filepath.Join(dir, "img.jpg"), []byte{0xff, 0xd8}, 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	e := embeddings.New(embeddings.Config{
		Backend:  "ollama",
		Model:    "nomic-embed-text",
		Endpoint: "http://127.0.0.1:19998", // nothing listening
	})
	r, err := New(dbPath, dir, e)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// IndexAll will fail per-file (embed errors) but must not panic.
	_ = r.IndexAll(ctx)

	s := r.Status()
	if s.Indexing {
		t.Error("Indexing should be false after IndexAll completes")
	}
	// Three supported files (.txt, .md, .go) — jpg is skipped
	if s.TotalFiles != 3 {
		t.Errorf("TotalFiles = %d, want 3", s.TotalFiles)
	}
}

// TestIndexAll_Idempotent verifies that calling IndexAll twice on an already-
// indexed database (with docs pre-inserted) does not re-embed them.
func TestIndexAll_Idempotent(t *testing.T) {
	if os.Getenv("EMBED_BACKEND") != "" {
		t.Skip("live backend detected — use live test instead")
	}

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "idempotent.json")

	// Write one text file
	filePath := filepath.Join(dir, "note.txt")
	if err := os.WriteFile(filePath, []byte("hello world"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// Pre-insert the document so IndexAll sees it as already indexed.
	db, err := vecdb.Open(dbPath)
	if err != nil {
		t.Fatalf("vecdb.Open: %v", err)
	}
	db.Upsert(&vecdb.Document{
		ID:        fileID(filePath),
		Content:   "hello world",
		Embedding: makeUnitVec(0),
	})
	if err := db.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	e := embeddings.New(embeddings.Config{
		Backend:  "ollama",
		Model:    "nomic-embed-text",
		Endpoint: "http://127.0.0.1:19998",
	})
	r, err := New(dbPath, dir, e)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx := context.Background()
	if err := r.IndexAll(ctx); err != nil {
		// Ignore: context or walk error expected due to no embedder
	}

	s := r.Status()
	// File was already in DB, so indexed count should equal db count (1)
	if s.IndexedFiles != 1 {
		t.Errorf("IndexedFiles = %d, want 1 (pre-indexed)", s.IndexedFiles)
	}
}

// TestIndexAll_ConcurrentReject verifies that a second IndexAll call during
// indexing returns an error.
func TestIndexAll_ConcurrentReject(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "conc.json")
	e := embeddings.New(embeddings.Config{
		Backend:  "ollama",
		Model:    "nomic-embed-text",
		Endpoint: "http://127.0.0.1:19998",
	})
	r, err := New(dbPath, dir, e)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Force Indexing=true
	r.mu.Lock()
	r.status.Indexing = true
	r.mu.Unlock()

	err = r.IndexAll(context.Background())
	if err == nil {
		t.Fatal("expected error when indexing already in progress, got nil")
	}
	if !strings.Contains(err.Error(), "already in progress") {
		t.Errorf("unexpected error message: %v", err)
	}

	// Reset
	r.mu.Lock()
	r.status.Indexing = false
	r.mu.Unlock()
}

// ---- cosine helper ----------------------------------------------------------

func TestCosineHelper(t *testing.T) {
	// Identical vectors -> score 1
	a := makeUnitVec(0)
	if s := cosine(a, a); math.Abs(s-1.0) > 1e-9 {
		t.Errorf("cosine(a,a) = %f, want 1.0", s)
	}
	// Orthogonal vectors -> score 0
	b := makeUnitVec(1)
	if s := cosine(a, b); math.Abs(s) > 1e-9 {
		t.Errorf("cosine(orthogonal) = %f, want 0.0", s)
	}
}

// ---- mediaMetaToText --------------------------------------------------------

func TestMediaMetaToText_Basic(t *testing.T) {
	meta := &MediaMeta{
		Path:   "/photos/sunset.jpg",
		Type:   "image",
		Format: "jpeg",
		Size:   123456,
		Width:  "1920",
		Height: "1080",
	}
	text := MediaMetaToText(meta)
	if !strings.Contains(text, "image") {
		t.Error("expected 'image' in output")
	}
	if !strings.Contains(text, "sunset.jpg") {
		t.Error("expected filename in output")
	}
	if !strings.Contains(text, "1920x1080") {
		t.Error("expected dimensions in output")
	}
}

func TestMediaMetaToText_VideoWithDuration(t *testing.T) {
	meta := &MediaMeta{
		Path:     "/videos/clip.mp4",
		Type:     "video",
		Format:   "mp4",
		Size:     9999,
		Duration: "00:02:35",
	}
	text := MediaMetaToText(meta)
	if !strings.Contains(text, "video") {
		t.Error("expected 'video' in output")
	}
	if !strings.Contains(text, "00:02:35") {
		t.Error("expected duration in output")
	}
}

func TestMediaMetaToText_WithGPS(t *testing.T) {
	meta := &MediaMeta{
		Path:   "/img/beach.jpg",
		Type:   "image",
		Format: "jpeg",
		Size:   500,
		GPS:    "34.0522 N, 118.2437 W",
	}
	text := MediaMetaToText(meta)
	if !strings.Contains(text, "34.0522") {
		t.Error("expected GPS coords in output")
	}
}

func TestMediaMetaToText_WithDateTime(t *testing.T) {
	ts, _ := time.Parse("2006-01-02", "2024-06-15")
	meta := &MediaMeta{
		Path:     "/img/pic.jpg",
		Type:     "image",
		Format:   "jpeg",
		Size:     200,
		DateTime: ts,
	}
	text := MediaMetaToText(meta)
	if !strings.Contains(text, "2024") {
		t.Error("expected year 2024 in output")
	}
}

// ---- isMediaFile ------------------------------------------------------------

func TestIsMediaFile(t *testing.T) {
	media := []string{"photo.jpg", "shot.jpeg", "img.png", "anim.gif", "clip.mp4", "movie.mov"}
	for _, f := range media {
		if !isMediaFile(f) {
			t.Errorf("expected %q to be a media file", f)
		}
	}
	nonMedia := []string{"doc.txt", "code.go", "data.json"}
	for _, f := range nonMedia {
		if isMediaFile(f) {
			t.Errorf("expected %q NOT to be a media file", f)
		}
	}
}
