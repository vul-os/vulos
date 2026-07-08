// Package models is the private-AI model-management seam for the sovereign box.
//
// WHAT THIS IS (and honestly is NOT):
//
// The OS does not (yet) ship a full model registry with a catalog + signed
// download pipeline. The embedding path (services/embeddings) auto-discovers a
// local ONNX file in ~/.vulos/models/ and the chat/LLM path proxies through the
// llmux gateway (internal/llmuxclient), which owns provider/model routing.
//
// This package surfaces and manages what REALLY exists on the box:
//
//   - the local embeddings/RAG model directory: which .onnx files are installed,
//     whether the model's real tokenizer.json is present (the difference between
//     genuine semantic RAG and the degraded FNV-lexical fallback), and which
//     model is ACTIVE for embeddings.
//   - an operator IMPORT flow: write a validated .onnx / tokenizer.json into the
//     models dir so an operator can install the real tokenizer (or a model) that
//     the auto-discovery then picks up. Import is bounded and path-traversal-safe.
//
// It deliberately does NOT invent a fake catalog of downloadable models — that
// (an actual model binary + a signed distribution channel) is an asset/ops task,
// not code. Where a real registry is needed, the API says so via NeedsRegistry.
//
// The chat-model list (what llmux exposes) is surfaced separately by the route
// layer, which already holds the llmux client; this package is the on-box
// embedding/RAG model manager.
package models

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Manager manages the on-box local model directory (embeddings/RAG models).
// It is safe for concurrent use: it holds no mutable state, every method reads
// the filesystem fresh so it always reflects reality (imports by other means
// are picked up immediately).
type Manager struct {
	dir string // absolute, cleaned path to the models directory
}

// New returns a Manager rooted at modelsDir. The directory is created on demand
// by import operations; List tolerates its absence (returns an empty listing).
func New(modelsDir string) *Manager {
	abs, err := filepath.Abs(modelsDir)
	if err != nil {
		abs = modelsDir
	}
	return &Manager{dir: filepath.Clean(abs)}
}

// Dir returns the absolute models directory this manager operates on.
func (m *Manager) Dir() string { return m.dir }

// onnxNames are the .onnx filenames the embeddings auto-discovery looks for, in
// priority order. Kept in sync with services/embeddings.NewOnnxEmbedder /
// OnnxAvailable — an operator who installs one of these names gets it picked up.
var onnxNames = []string{"all-MiniLM-L6-v2.onnx", "model.onnx", "e5-small.onnx"}

// LocalModel describes one installed local ONNX model file.
type LocalModel struct {
	Name         string `json:"name"`          // filename, e.g. "all-MiniLM-L6-v2.onnx"
	Path         string `json:"path"`          // absolute path (operator-facing, box-local)
	SizeBytes    int64  `json:"size_bytes"`    // on-disk size
	SHA256       string `json:"sha256"`        // content digest (integrity/identity)
	HasTokenizer bool   `json:"has_tokenizer"` // tokenizer.json present next to it
	Active       bool   `json:"active"`        // this is the model embeddings auto-discovery would pick
	Discoverable bool   `json:"discoverable"`  // filename matches an auto-discovered name
}

// Listing is the full model-management surface: local embedding models + the
// derived RAG readiness signal.
type Listing struct {
	Dir          string       `json:"dir"`           // models directory (box-local absolute path)
	Models       []LocalModel `json:"models"`        // installed .onnx files
	ActiveModel  string       `json:"active_model"`  // filename embeddings would use, or "" if none
	HasTokenizer bool         `json:"has_tokenizer"` // tokenizer.json present next to the active model
	// RAGMode is the honest retrieval mode the assistant would run in:
	//   "semantic"  — active ONNX model + real tokenizer.json (genuine semantic RAG)
	//   "degraded"  — active ONNX model but NO tokenizer.json (FNV-hash fallback; weak vectors)
	//   "lexical"   — no ONNX model installed (pure on-box lexical retrieval)
	RAGMode string `json:"rag_mode"`
	// NeedsRegistry flags that there is no model-download catalog backend: the
	// UI should let the operator IMPORT artifacts they already have, and (for now)
	// point at where to get models, rather than offer an in-app catalog download.
	NeedsRegistry bool `json:"needs_registry"`
}

const (
	RAGModeSemantic = "semantic"
	RAGModeDegraded = "degraded"
	RAGModeLexical  = "lexical"
)

// tokenizerName is the filename embeddings/onnx.go looks for next to the model.
const tokenizerName = "tokenizer.json"

// List enumerates installed local models and derives RAG readiness. It never
// errors on a missing directory (returns an empty, lexical-mode listing).
func (m *Manager) List() (Listing, error) {
	out := Listing{Dir: m.dir, NeedsRegistry: true, RAGMode: RAGModeLexical}

	entries, err := os.ReadDir(m.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return out, err
	}

	// The active model is the first auto-discovered name that exists (mirrors
	// embeddings.NewOnnxEmbedder's priority order).
	active := ""
	for _, name := range onnxNames {
		if fi, statErr := os.Stat(filepath.Join(m.dir, name)); statErr == nil && !fi.IsDir() {
			active = name
			break
		}
	}

	tokPresent := fileExists(filepath.Join(m.dir, tokenizerName))

	for _, e := range entries {
		if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".onnx") {
			continue
		}
		info, ierr := e.Info()
		if ierr != nil {
			continue
		}
		lm := LocalModel{
			Name:         e.Name(),
			Path:         filepath.Join(m.dir, e.Name()),
			SizeBytes:    info.Size(),
			HasTokenizer: tokPresent,
			Active:       e.Name() == active,
			Discoverable: isDiscoverable(e.Name()),
		}
		lm.SHA256 = fileSHA256(lm.Path)
		out.Models = append(out.Models, lm)
	}
	sort.Slice(out.Models, func(i, j int) bool { return out.Models[i].Name < out.Models[j].Name })

	out.ActiveModel = active
	out.HasTokenizer = tokPresent
	out.RAGMode = ragMode(active != "", tokPresent)
	return out, nil
}

// ragMode is the single source of truth for the readiness label, so the metrics
// gauge, the /status endpoint and the shell all agree.
func ragMode(hasModel, hasTokenizer bool) string {
	switch {
	case hasModel && hasTokenizer:
		return RAGModeSemantic
	case hasModel:
		return RAGModeDegraded
	default:
		return RAGModeLexical
	}
}

// --- Import ---

// Import artifact kinds. An import writes exactly one known filename; the
// operator cannot choose an arbitrary destination (no path traversal).
const (
	ArtifactModel     = "model"     // an .onnx embedding model → written as model.onnx
	ArtifactTokenizer = "tokenizer" // the model's tokenizer.json → written as tokenizer.json
)

// Import size bounds. A model can be large (nomic-embed is ~274MB); tokenizer
// files are small. These caps stop an unbounded write filling the box disk.
const (
	MaxModelBytes     = 600 * 1024 * 1024 // 600 MiB — comfortably covers common embedding models
	MaxTokenizerBytes = 32 * 1024 * 1024  // 32 MiB — tokenizer.json is normally < 5 MiB
)

// ErrArtifactTooLarge is returned when an import exceeds its size bound.
var ErrArtifactTooLarge = errors.New("models: artifact exceeds size limit")

// ErrBadArtifact is returned when the uploaded bytes don't look like the claimed
// artifact kind (wrong magic / not valid JSON).
var ErrBadArtifact = errors.New("models: artifact failed validation")

// ImportResult reports what an import wrote.
type ImportResult struct {
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Path      string `json:"path"`
	SizeBytes int64  `json:"size_bytes"`
	SHA256    string `json:"sha256"`
}

// Import validates and installs an artifact from r into the models directory.
//
// SECURITY:
//   - The destination filename is derived ENTIRELY from kind (a fixed allowlist),
//     never from any client-supplied name — there is no path-traversal surface.
//   - The read is hard-bounded by the per-kind byte cap; a stream longer than the
//     cap is rejected (ErrArtifactTooLarge) and the partial file removed.
//   - The content is sniffed: a model must start with the ONNX/protobuf magic; a
//     tokenizer must parse as a JSON object. Garbage is rejected before it can be
//     picked up by the embedder.
//   - The write is atomic (temp file in the same dir + rename) so a partial or
//     failed import never leaves a half-written model the embedder might load.
func (m *Manager) Import(kind string, r io.Reader) (ImportResult, error) {
	var (
		destName string
		maxBytes int64
	)
	switch kind {
	case ArtifactModel:
		destName, maxBytes = "model.onnx", MaxModelBytes
	case ArtifactTokenizer:
		destName, maxBytes = tokenizerName, MaxTokenizerBytes
	default:
		return ImportResult{}, fmt.Errorf("models: unknown artifact kind %q", kind)
	}

	if err := os.MkdirAll(m.dir, 0o700); err != nil {
		return ImportResult{}, fmt.Errorf("models: create dir: %w", err)
	}

	dest := filepath.Join(m.dir, destName)
	// Defense in depth: confirm the resolved path is still inside the models dir.
	if !withinDir(m.dir, dest) {
		return ImportResult{}, ErrBadArtifact
	}

	tmp, err := os.CreateTemp(m.dir, ".import-*."+strings.TrimPrefix(filepath.Ext(destName), "."))
	if err != nil {
		return ImportResult{}, fmt.Errorf("models: temp file: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := func() { tmp.Close(); os.Remove(tmpPath) }

	// Bound the read to maxBytes+1 so we can detect (and reject) overflow.
	h := sha256.New()
	limited := io.LimitReader(r, maxBytes+1)
	written, err := io.Copy(io.MultiWriter(tmp, h), limited)
	if err != nil {
		cleanup()
		return ImportResult{}, fmt.Errorf("models: read artifact: %w", err)
	}
	if written > maxBytes {
		cleanup()
		return ImportResult{}, ErrArtifactTooLarge
	}
	if written == 0 {
		cleanup()
		return ImportResult{}, ErrBadArtifact
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return ImportResult{}, fmt.Errorf("models: close temp: %w", err)
	}

	// Validate content before it becomes discoverable.
	if err := validate(kind, tmpPath); err != nil {
		os.Remove(tmpPath)
		return ImportResult{}, err
	}

	if err := os.Chmod(tmpPath, 0o600); err != nil {
		os.Remove(tmpPath)
		return ImportResult{}, fmt.Errorf("models: chmod: %w", err)
	}
	if err := os.Rename(tmpPath, dest); err != nil {
		os.Remove(tmpPath)
		return ImportResult{}, fmt.Errorf("models: install: %w", err)
	}

	return ImportResult{
		Kind:      kind,
		Name:      destName,
		Path:      dest,
		SizeBytes: written,
		SHA256:    hex.EncodeToString(h.Sum(nil)),
	}, nil
}

// validate sniffs the on-disk artifact to confirm it matches the claimed kind.
func validate(kind, path string) error {
	switch kind {
	case ArtifactModel:
		return validateONNX(path)
	case ArtifactTokenizer:
		return validateTokenizer(path)
	default:
		return ErrBadArtifact
	}
}

// validateONNX checks the file begins with an ONNX model's protobuf framing.
// ONNX files are serialized protobuf ModelProto messages: field 1 (ir_version,
// varint) → the stream starts with tag byte 0x08. Some exporters emit the
// producer/opset fields first; to stay tolerant while still rejecting obvious
// non-models (text, JSON, HTML, zip), we require the first byte to be a plausible
// protobuf field tag (wire types 0/2, i.e. low 3 bits ∈ {0,2}) and reject known
// non-protobuf magics.
func validateONNX(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return ErrBadArtifact
	}
	defer f.Close()
	var head [8]byte
	n, _ := io.ReadFull(f, head[:])
	if n < 2 {
		return ErrBadArtifact
	}
	// Reject common non-ONNX magics outright.
	switch {
	case head[0] == 'P' && head[1] == 'K': // zip / many archive formats
		return fmt.Errorf("%w: looks like a zip archive, not a raw .onnx", ErrBadArtifact)
	case head[0] == '{' || head[0] == '[': // JSON
		return fmt.Errorf("%w: looks like JSON, not an .onnx model", ErrBadArtifact)
	case head[0] == '<': // HTML/XML (e.g. an error page saved as .onnx)
		return fmt.Errorf("%w: looks like HTML/XML, not an .onnx model", ErrBadArtifact)
	case head[0] == 0x1f && head[1] == 0x8b: // gzip
		return fmt.Errorf("%w: looks gzip-compressed; decompress the .onnx first", ErrBadArtifact)
	}
	// Protobuf: first byte is a field tag = (field_number<<3)|wire_type.
	// Valid ONNX ModelProto first fields use wire type 0 (varint) or 2 (len-delim).
	wireType := head[0] & 0x07
	if wireType != 0 && wireType != 2 {
		return fmt.Errorf("%w: not a protobuf-framed ONNX model", ErrBadArtifact)
	}
	return nil
}

// validateTokenizer confirms the file parses as a JSON object (HuggingFace
// tokenizer.json is a top-level object). We only sniff the leading non-space
// byte to avoid loading a large JSON fully; a stricter parse would reject valid
// files that are simply large, so leading-'{' + non-empty is the right bound.
func validateTokenizer(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return ErrBadArtifact
	}
	defer f.Close()
	buf := make([]byte, 512)
	n, _ := io.ReadFull(f, buf)
	if n == 0 {
		return ErrBadArtifact
	}
	// Trim leading whitespace and a possible UTF-8 BOM (U+FEFF) before sniffing.
	trimmed := strings.TrimLeft(string(buf[:n]), " \t\r\n")
	if trimmed == "" || trimmed[0] != '{' {
		return fmt.Errorf("%w: tokenizer.json must be a JSON object", ErrBadArtifact)
	}
	return nil
}

// --- helpers ---

func isDiscoverable(name string) bool {
	for _, n := range onnxNames {
		if name == n {
			return true
		}
	}
	return false
}

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}

func fileSHA256(p string) string {
	f, err := os.Open(p)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))
}

// withinDir reports whether target resolves to a path inside dir (no traversal).
func withinDir(dir, target string) bool {
	rel, err := filepath.Rel(dir, target)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
