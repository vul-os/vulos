package models

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestList_EmptyAndMissingDir(t *testing.T) {
	// A non-existent dir must not error — it is the pre-install state.
	m := New(filepath.Join(t.TempDir(), "does-not-exist"))
	l, err := m.List()
	if err != nil {
		t.Fatalf("List on missing dir: %v", err)
	}
	if l.RAGMode != RAGModeLexical {
		t.Fatalf("empty dir should be lexical, got %q", l.RAGMode)
	}
	if len(l.Models) != 0 || l.ActiveModel != "" || l.HasTokenizer {
		t.Fatalf("empty dir listing should be empty: %+v", l)
	}
	if !l.NeedsRegistry {
		t.Fatalf("NeedsRegistry should be true (no catalog backend)")
	}
}

func writeFile(t *testing.T, dir, name string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// onnxBytes returns a minimal byte slice that passes the protobuf-tag sniff.
func onnxBytes() []byte { return []byte{0x08, 0x07, 0x12, 0x04, 't', 'e', 's', 't'} }

func TestList_DegradedVsSemantic(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "model.onnx", onnxBytes())

	// Model present, no tokenizer → degraded.
	m := New(dir)
	l, err := m.List()
	if err != nil {
		t.Fatal(err)
	}
	if l.RAGMode != RAGModeDegraded {
		t.Fatalf("model without tokenizer should be degraded, got %q", l.RAGMode)
	}
	if l.ActiveModel != "model.onnx" {
		t.Fatalf("active model = %q, want model.onnx", l.ActiveModel)
	}
	if len(l.Models) != 1 || !l.Models[0].Active || l.Models[0].HasTokenizer {
		t.Fatalf("model listing wrong: %+v", l.Models)
	}
	if l.Models[0].SHA256 == "" {
		t.Fatalf("expected sha256 to be computed")
	}

	// Add tokenizer.json → semantic.
	writeFile(t, dir, "tokenizer.json", []byte(`{"version":"1.0"}`))
	l, err = m.List()
	if err != nil {
		t.Fatal(err)
	}
	if l.RAGMode != RAGModeSemantic {
		t.Fatalf("model + tokenizer should be semantic, got %q", l.RAGMode)
	}
	if !l.HasTokenizer || !l.Models[0].HasTokenizer {
		t.Fatalf("tokenizer should be reported present: %+v", l)
	}
}

func TestList_ActivePriorityOrder(t *testing.T) {
	dir := t.TempDir()
	// Both present; the priority list puts all-MiniLM first.
	writeFile(t, dir, "model.onnx", onnxBytes())
	writeFile(t, dir, "all-MiniLM-L6-v2.onnx", onnxBytes())
	l, err := New(dir).List()
	if err != nil {
		t.Fatal(err)
	}
	if l.ActiveModel != "all-MiniLM-L6-v2.onnx" {
		t.Fatalf("priority order: active = %q, want all-MiniLM-L6-v2.onnx", l.ActiveModel)
	}
	var activeCount int
	for _, mm := range l.Models {
		if mm.Active {
			activeCount++
		}
	}
	if activeCount != 1 {
		t.Fatalf("exactly one model should be active, got %d", activeCount)
	}
}

func TestImport_Model_OK(t *testing.T) {
	dir := t.TempDir()
	m := New(dir)
	res, err := m.Import(ArtifactModel, bytes.NewReader(onnxBytes()))
	if err != nil {
		t.Fatalf("import model: %v", err)
	}
	if res.Name != "model.onnx" {
		t.Fatalf("import wrote %q, want model.onnx", res.Name)
	}
	if res.SHA256 == "" || res.SizeBytes == 0 {
		t.Fatalf("import result incomplete: %+v", res)
	}
	// It must land in the dir and become the active model.
	l, _ := m.List()
	if l.ActiveModel != "model.onnx" {
		t.Fatalf("imported model not active: %+v", l)
	}
	// Written file must be 0600 (mail-adjacent artifacts stay private).
	fi, _ := os.Stat(filepath.Join(dir, "model.onnx"))
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("model perm = %v, want 0600", fi.Mode().Perm())
	}
}

func TestImport_Tokenizer_OK(t *testing.T) {
	dir := t.TempDir()
	m := New(dir)
	if _, err := m.Import(ArtifactTokenizer, strings.NewReader(`  {"model":{}}`)); err != nil {
		t.Fatalf("import tokenizer: %v", err)
	}
	if !fileExists(filepath.Join(dir, "tokenizer.json")) {
		t.Fatalf("tokenizer.json not written")
	}
}

func TestImport_RejectsNonONNX(t *testing.T) {
	cases := map[string][]byte{
		"zip":  {'P', 'K', 0x03, 0x04},
		"json": []byte(`{"not":"a model"}`),
		"html": []byte(`<!doctype html>`),
		"gzip": {0x1f, 0x8b, 0x08},
		// wire type 5 (32-bit) is not a valid ONNX ModelProto first field.
		"badtag": {0x0d, 0x00, 0x00, 0x00},
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			m := New(t.TempDir())
			_, err := m.Import(ArtifactModel, bytes.NewReader(data))
			if err == nil {
				t.Fatalf("expected rejection for %s", name)
			}
			if !isBad(err) {
				t.Fatalf("%s: want ErrBadArtifact, got %v", name, err)
			}
		})
	}
}

func TestImport_RejectsNonJSONTokenizer(t *testing.T) {
	m := New(t.TempDir())
	if _, err := m.Import(ArtifactTokenizer, strings.NewReader("not json at all")); err == nil || !isBad(err) {
		t.Fatalf("want ErrBadArtifact for non-JSON tokenizer, got %v", err)
	}
}

func TestImport_RejectsEmpty(t *testing.T) {
	m := New(t.TempDir())
	if _, err := m.Import(ArtifactModel, bytes.NewReader(nil)); err != ErrBadArtifact {
		t.Fatalf("empty import: want ErrBadArtifact, got %v", err)
	}
}

func TestImport_SizeCap(t *testing.T) {
	dir := t.TempDir()
	m := New(dir)
	// One byte over the tokenizer cap must be rejected as too large, and must
	// NOT leave a partial tokenizer.json behind.
	big := bytes.NewReader(append([]byte("{"), make([]byte, MaxTokenizerBytes)...))
	if _, err := m.Import(ArtifactTokenizer, big); err != ErrArtifactTooLarge {
		t.Fatalf("oversize import: want ErrArtifactTooLarge, got %v", err)
	}
	if fileExists(filepath.Join(dir, "tokenizer.json")) {
		t.Fatalf("oversize import must not leave a partial file")
	}
	// No stray temp files left behind either.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".import-") {
			t.Fatalf("leftover temp file: %s", e.Name())
		}
	}
}

func TestImport_UnknownKind(t *testing.T) {
	m := New(t.TempDir())
	if _, err := m.Import("weights", bytes.NewReader(onnxBytes())); err == nil {
		t.Fatalf("expected error for unknown kind")
	}
}

// TestImport_DestinationIsFixed proves there is no path-traversal surface: the
// destination is derived from kind alone and always lands inside the models dir,
// regardless of anything a caller might try to influence.
func TestImport_DestinationIsFixed(t *testing.T) {
	dir := t.TempDir()
	m := New(dir)
	res, err := m.Import(ArtifactModel, bytes.NewReader(onnxBytes()))
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(res.Path) != m.Dir() {
		t.Fatalf("import escaped models dir: %s not in %s", res.Path, m.Dir())
	}
	if filepath.Base(res.Path) != "model.onnx" {
		t.Fatalf("import wrote unexpected filename: %s", res.Path)
	}
}

func TestWithinDir(t *testing.T) {
	dir := "/models"
	if !withinDir(dir, "/models/model.onnx") {
		t.Fatalf("in-dir path should be within")
	}
	if withinDir(dir, "/etc/passwd") {
		t.Fatalf("out-of-dir path should NOT be within")
	}
	if withinDir(dir, "/models/../etc/passwd") {
		t.Fatalf("traversal path should NOT be within")
	}
}

func isBad(err error) bool {
	return err != nil && (err == ErrBadArtifact || strings.Contains(err.Error(), "models:"))
}
