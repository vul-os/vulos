package embeddings

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestOnnxEmbedder_RealSemanticVectors is the end-to-end proof that the SHIPPED
// embedder (NewOnnxEmbedder + the onnxHelperScript in onnx.go) produces genuine
// 384-dim semantic vectors when a real all-MiniLM-L6-v2 model.onnx + its
// tokenizer.json are installed — i.e. that a curated catalog download flips RAG
// from the degraded FNV fallback to real semantic retrieval.
//
// It is GATED so CI never fails on a box without the model/deps:
//   - requires python3 + onnxruntime + tokenizers importable, AND
//   - requires VULA_TEST_MODELS_DIR to point at a dir containing a real
//     model.onnx + tokenizer.json (install via the new download command).
//
// Manual run:
//
//	go run the download (POST /api/models/download {"id":"all-MiniLM-L6-v2"}) OR
//	drop model.onnx + tokenizer.json into a dir, then:
//	VULA_TEST_MODELS_DIR=/path/to/models go test -run RealSemantic ./backend/services/embeddings/
func TestOnnxEmbedder_RealSemanticVectors(t *testing.T) {
	dir := os.Getenv("VULA_TEST_MODELS_DIR")
	if dir == "" {
		t.Skip("set VULA_TEST_MODELS_DIR to a dir with a real model.onnx + tokenizer.json to run the semantic smoke test")
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}
	if exec.Command("python3", "-c", "import onnxruntime, tokenizers, numpy").Run() != nil {
		t.Skip("python embed deps (onnxruntime/tokenizers/numpy) not installed")
	}
	if _, err := os.Stat(filepath.Join(dir, "tokenizer.json")); err != nil {
		t.Skipf("no tokenizer.json in %s — this test needs the REAL tokenizer for semantic vectors", dir)
	}

	emb, err := NewOnnxEmbedder(dir)
	if err != nil {
		t.Fatalf("NewOnnxEmbedder: %v", err)
	}

	ctx := context.Background()
	vecInvoice, err := emb.Embed(ctx, "The invoice for the server renewal is due next week.")
	if err != nil {
		t.Fatalf("embed invoice: %v", err)
	}
	vecBill, err := emb.Embed(ctx, "Please pay the hosting bill before the deadline.")
	if err != nil {
		t.Fatalf("embed bill: %v", err)
	}
	vecCat, err := emb.Embed(ctx, "The cat sat lazily on the warm windowsill.")
	if err != nil {
		t.Fatalf("embed cat: %v", err)
	}

	// Real all-MiniLM-L6-v2 is 384-dim.
	if len(vecInvoice) != 384 {
		t.Fatalf("embedding dim = %d, want 384 (is this the real model + tokenizer?)", len(vecInvoice))
	}
	if emb.Dimension() != 384 {
		t.Fatalf("Dimension() = %d, want 384", emb.Dimension())
	}

	// Semantic property: the two related sentences (invoice ~ bill) must be
	// closer than an unrelated pair (invoice ~ cat). This is exactly what the
	// degraded FNV fallback CANNOT reliably deliver.
	related := cosine(vecInvoice, vecBill)
	unrelated := cosine(vecInvoice, vecCat)
	t.Logf("cosine(invoice,bill)=%.4f  cosine(invoice,cat)=%.4f", related, unrelated)
	if !(related > unrelated) {
		t.Fatalf("expected related > unrelated, got related=%.4f unrelated=%.4f", related, unrelated)
	}

	// Sanity: OnInstance is the sovereign-certification signal the mail index checks.
	if !emb.OnInstance() {
		t.Fatalf("OnInstance() should be true for the on-box ONNX embedder")
	}
}

func cosine(a, b []float32) float64 {
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (sqrt(na) * sqrt(nb))
}

func sqrt(x float64) float64 {
	// tiny local sqrt to avoid an extra import in a test-only helper
	if x <= 0 {
		return 0
	}
	z := x
	for i := 0; i < 40; i++ {
		z = (z + x/z) / 2
	}
	return z
}
