package embeddings

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// OnnxEmbedder runs a local ONNX model for offline embeddings.
// Uses a small Python helper script that loads onnxruntime + tokenizer.
// This avoids CGO and works on any platform with python3 + onnxruntime.
//
// Models:
//   - all-MiniLM-L6-v2 (22MB, 384 dims, good quality)
//   - nomic-embed-text-v1 (274MB, 768 dims, better quality)
//   - e5-small-v2 (33MB, 384 dims, balanced)
//
// Install: pip install onnxruntime tokenizers numpy
// Model: download .onnx file to ~/.vulos/models/

type OnnxEmbedder struct {
	modelPath  string
	scriptPath string
	dim        int
}

// onnxModelNames are the .onnx filenames auto-discovery looks for, in priority
// order. NewOnnxEmbedder and OnnxAvailable MUST use this single shared list —
// they previously carried two independently-maintained copies that drifted
// (OnnxAvailable was missing "e5-small.onnx"), so a box with a valid
// e5-small.onnx model was reported as having none and the semantic index
// silently never activated. Also kept in sync with services/models.onnxNames.
var onnxModelNames = []string{"all-MiniLM-L6-v2.onnx", "model.onnx", "e5-small.onnx"}

// NewOnnxEmbedder creates an embedder using a local ONNX model.
func NewOnnxEmbedder(modelsDir string) (*OnnxEmbedder, error) {
	// Find model file
	modelPath := ""
	for _, name := range onnxModelNames {
		p := filepath.Join(modelsDir, name)
		if _, err := os.Stat(p); err == nil {
			modelPath = p
			break
		}
	}
	if modelPath == "" {
		return nil, fmt.Errorf("no ONNX model found in %s", modelsDir)
	}

	// Write helper script
	scriptPath := filepath.Join(modelsDir, "embed.py")
	if err := os.WriteFile(scriptPath, []byte(onnxHelperScript), 0644); err != nil {
		return nil, err
	}

	return &OnnxEmbedder{modelPath: modelPath, scriptPath: scriptPath}, nil
}

// Embed generates an embedding using the local ONNX model.
func (o *OnnxEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	cmd := exec.CommandContext(ctx, "python3", o.scriptPath, o.modelPath)
	// Pass the (private mail) text on STDIN, never as a process argument — an argv
	// is world-readable via `ps`/`/proc` to other local users on the box.
	cmd.Stdin = strings.NewReader(text)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("onnx embed: %w", err)
	}

	// Parse JSON output: [0.123, -0.456, ...]
	var embedding []float64
	if err := json.Unmarshal(out, &embedding); err != nil {
		return nil, fmt.Errorf("parse embedding: %w", err)
	}

	result := make([]float32, len(embedding))
	for i, v := range embedding {
		result[i] = float32(v)
	}

	if o.dim == 0 {
		o.dim = len(result)
	}
	return result, nil
}

func (o *OnnxEmbedder) Dimension() int { return o.dim }

// OnInstance reports that this embedder runs entirely on the local box: it
// shells out to a local python3 + ONNX model and performs NO network I/O. This
// is the sovereign-certification signal consumers (e.g. the mail assistant's
// vector index) check before trusting an embedder with mail content. The HTTP
// Embedder deliberately does NOT implement this, so it can never be mistaken
// for an on-instance embedder.
func (o *OnnxEmbedder) OnInstance() bool { return true }

// Available checks if ONNX inference is possible.
func OnnxAvailable(modelsDir string) bool {
	// Check python3
	if _, err := exec.LookPath("python3"); err != nil {
		return false
	}
	// Check onnxruntime
	cmd := exec.Command("python3", "-c", "import onnxruntime")
	if cmd.Run() != nil {
		return false
	}
	// Check model exists
	for _, name := range onnxModelNames {
		if _, err := os.Stat(filepath.Join(modelsDir, name)); err == nil {
			return true
		}
	}
	return false
}

// fallbackTokenizerPy defines _fallback_token_id: the DETERMINISTIC token-id
// function used ONLY when no tokenizer.json ships beside the model. It is a
// pure-Python FNV-1a (32-bit) hash with NO imports, so it is byte-for-byte
// identical in every process and on every platform.
//
// WHY THIS EXISTS (honest note): the previous fallback used Python's builtin
// hash(), which is SALTED PER PROCESS (PYTHONHASHSEED). That made the same word
// map to a different id on every restart, so the same text produced a DIFFERENT
// embedding vector each run — semantic retrieval quality silently collapsed
// across process restarts with no error. FNV-1a is stable, so vectors are now
// reproducible.
//
// This is still only a DEGRADED fallback: these hashed ids do NOT index the
// model's trained vocabulary, so the resulting vectors are reproducible but only
// weakly meaningful. TRUE semantic RAG needs the model's real tokenizer.json
// next to the .onnx file. When it is absent, prefer the lexical retrieval
// baseline (the MailIndex already falls back to lexical when the vector index is
// empty or errors).
const fallbackTokenizerPy = `
def _fallback_token_id(word):
    # FNV-1a (32-bit). No imports and no per-process salt -> deterministic across
    # processes, restarts and platforms (unlike Python's builtin hash()).
    h = 2166136261
    for b in word.encode("utf-8"):
        h = ((h ^ b) * 16777619) & 0xFFFFFFFF
    return h % 30000
`

const onnxHelperScript = `#!/usr/bin/env python3
"""Vula OS — ONNX embedding helper. Runs a single embedding and prints JSON."""
import sys, json, os, numpy as np
import onnxruntime as ort
from tokenizers import Tokenizer
` + fallbackTokenizerPy + `
def embed(model_path, text):
    # Load model
    session = ort.InferenceSession(model_path)

    tok_path = os.path.join(os.path.dirname(model_path), "tokenizer.json")
    if os.path.exists(tok_path):
        # Real path: the model's own trained tokenizer -> genuine semantic tokens.
        tokenizer = Tokenizer.from_file(tok_path)
        encoded = tokenizer.encode(text)
        input_ids = encoded.ids[:512]
        attention_mask = [1] * len(input_ids)
    else:
        # NO tokenizer.json -> DEGRADED but DETERMINISTIC fallback. These ids are
        # a stable hash of each word, NOT this model's vocabulary, so the vectors
        # are reproducible but only weakly meaningful. Real semantic RAG needs
        # tokenizer.json; without it, prefer the lexical retrieval baseline. We
        # stay deterministic (was: per-process-randomized hash()) so retrieval
        # never silently varies across restarts.
        sys.stderr.write("vula-embed: no tokenizer.json found next to the model; using the deterministic DEGRADED fallback tokenizer -- semantic quality is reduced, prefer lexical retrieval\n")
        words = text.lower().split()[:512]
        input_ids = [_fallback_token_id(w) for w in words]
        attention_mask = [1] * len(input_ids)

    # Pad to length
    max_len = 512
    input_ids = input_ids[:max_len] + [0] * max(0, max_len - len(input_ids))
    attention_mask = attention_mask[:max_len] + [0] * max(0, max_len - len(attention_mask))

    # Run inference
    inputs = {
        "input_ids": np.array([input_ids], dtype=np.int64),
        "attention_mask": np.array([attention_mask], dtype=np.int64),
    }

    # Handle optional token_type_ids
    input_names = [i.name for i in session.get_inputs()]
    if "token_type_ids" in input_names:
        inputs["token_type_ids"] = np.zeros_like(inputs["input_ids"])

    outputs = session.run(None, inputs)

    # Mean pooling over token embeddings
    token_embeddings = outputs[0][0]  # (seq_len, hidden_dim)
    mask = np.array(attention_mask[:token_embeddings.shape[0]], dtype=np.float32)
    mask = mask[:, np.newaxis]
    pooled = (token_embeddings * mask).sum(axis=0) / mask.sum()

    # Normalize
    norm = np.linalg.norm(pooled)
    if norm > 0:
        pooled = pooled / norm

    return pooled.tolist()

if __name__ == "__main__":
    model_path = sys.argv[1]
    text = sys.stdin.read()
    result = embed(model_path, text)
    print(json.dumps(result))
`

// Helper to normalize embeddings
func normalize(v []float32) []float32 {
	var norm float64
	for _, x := range v {
		norm += float64(x) * float64(x)
	}
	norm = math.Sqrt(norm)
	if norm == 0 {
		return v
	}
	result := make([]float32, len(v))
	for i, x := range v {
		result[i] = float32(float64(x) / norm)
	}
	return result
}

func init() {
	_ = binary.LittleEndian // keep import
	_ = strings.TrimSpace
}
