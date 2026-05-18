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

// NewOnnxEmbedder creates an embedder using a local ONNX model.
func NewOnnxEmbedder(modelsDir string) (*OnnxEmbedder, error) {
	// Find model file
	modelPath := ""
	for _, name := range []string{"all-MiniLM-L6-v2.onnx", "model.onnx", "e5-small.onnx"} {
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
	cmd := exec.CommandContext(ctx, "python3", o.scriptPath, o.modelPath, text)
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
	for _, name := range []string{"all-MiniLM-L6-v2.onnx", "model.onnx"} {
		if _, err := os.Stat(filepath.Join(modelsDir, name)); err == nil {
			return true
		}
	}
	return false
}

const onnxHelperScript = `#!/usr/bin/env python3
"""Vula OS — ONNX embedding helper. Runs a single embedding and prints JSON."""
import sys, json, numpy as np
import onnxruntime as ort
from tokenizers import Tokenizer

def embed(model_path, text):
    # Load model
    session = ort.InferenceSession(model_path)

    # Simple tokenization (space-based fallback if no tokenizer.json)
    import os
    tok_path = os.path.join(os.path.dirname(model_path), "tokenizer.json")
    if os.path.exists(tok_path):
        tokenizer = Tokenizer.from_file(tok_path)
        encoded = tokenizer.encode(text)
        input_ids = encoded.ids[:512]
        attention_mask = [1] * len(input_ids)
    else:
        # Fallback: basic word-piece-like tokenization
        words = text.lower().split()[:512]
        input_ids = [hash(w) % 30000 for w in words]
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
    text = " ".join(sys.argv[2:])
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
