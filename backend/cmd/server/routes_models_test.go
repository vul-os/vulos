package main

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"vulos/backend/services/models"
)

// ownerOK / ownerDeny are fake owner predicates for the model routes.
func ownerOK(string) bool   { return true }
func ownerDeny(string) bool { return false }

func onnxBytes() []byte { return []byte{0x08, 0x07, 0x12, 0x02, 'h', 'i'} }

func newModelMux(t *testing.T, owner isOwner) (*http.ServeMux, string) {
	t.Helper()
	dir := t.TempDir()
	mux := http.NewServeMux()
	// llmux nil → chat_models is best-effort null, never fails the handler.
	registerModelRoutes(mux, models.New(dir), nil, owner)
	return mux, dir
}

func TestModelRoutes_OwnerGate(t *testing.T) {
	mux, _ := newModelMux(t, ownerDeny)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/models", nil)
	req.Header.Set("X-User-ID", "someone")
	mux.ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Fatalf("non-owner GET /api/models: got %d, want 403", rec.Code)
	}
}

func TestModelRoutes_ListLexicalByDefault(t *testing.T) {
	mux, _ := newModelMux(t, ownerOK)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/models", nil)
	req.Header.Set("X-User-ID", "owner")
	mux.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("GET /api/models: got %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	var resp struct {
		Embeddings struct {
			RAGMode       string `json:"rag_mode"`
			NeedsRegistry bool   `json:"needs_registry"`
		} `json:"embeddings"`
		ChatModelsError string `json:"chat_models_error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Embeddings.RAGMode != models.RAGModeLexical {
		t.Fatalf("rag_mode = %q, want lexical", resp.Embeddings.RAGMode)
	}
	if !resp.Embeddings.NeedsRegistry {
		t.Fatalf("needs_registry should be true (no catalog backend)")
	}
	if resp.ChatModelsError == "" {
		t.Fatalf("expected chat_models_error when llmux is unconfigured")
	}
}

// buildImport builds a multipart body with the given kind + artifact bytes.
func buildImport(t *testing.T, kind string, artifact []byte) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	if err := w.WriteField("kind", kind); err != nil {
		t.Fatal(err)
	}
	fw, err := w.CreateFormFile("artifact", "upload.bin")
	if err != nil {
		t.Fatal(err)
	}
	fw.Write(artifact)
	w.Close()
	return &buf, w.FormDataContentType()
}

func TestModelRoutes_ImportModel_UpgradesRAG(t *testing.T) {
	mux, dir := newModelMux(t, ownerOK)

	// Import a valid model → RAG becomes degraded (no tokenizer yet).
	body, ct := buildImport(t, models.ArtifactModel, onnxBytes())
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/models/import", body)
	req.Header.Set("X-User-ID", "owner")
	req.Header.Set("Content-Type", ct)
	mux.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("import model: got %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "model.onnx")); err != nil {
		t.Fatalf("model.onnx not installed: %v", err)
	}

	// Now import the tokenizer → RAG becomes semantic.
	body, ct = buildImport(t, models.ArtifactTokenizer, []byte(`{"model":{}}`))
	rec = httptest.NewRecorder()
	req = httptest.NewRequest("POST", "/api/models/import", body)
	req.Header.Set("X-User-ID", "owner")
	req.Header.Set("Content-Type", ct)
	mux.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("import tokenizer: got %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	var resp struct {
		Embeddings struct {
			RAGMode string `json:"rag_mode"`
		} `json:"embeddings"`
	}
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Embeddings.RAGMode != models.RAGModeSemantic {
		t.Fatalf("after tokenizer import rag_mode = %q, want semantic", resp.Embeddings.RAGMode)
	}
}

func TestModelRoutes_ImportRejectsGarbage(t *testing.T) {
	mux, _ := newModelMux(t, ownerOK)
	body, ct := buildImport(t, models.ArtifactModel, []byte(`{"totally":"not onnx"}`))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/models/import", body)
	req.Header.Set("X-User-ID", "owner")
	req.Header.Set("Content-Type", ct)
	mux.ServeHTTP(rec, req)
	if rec.Code != 400 {
		t.Fatalf("garbage model import: got %d, want 400 (body=%s)", rec.Code, rec.Body.String())
	}
}

func TestModelRoutes_ImportRejectsBadKind(t *testing.T) {
	mux, _ := newModelMux(t, ownerOK)
	body, ct := buildImport(t, "weights", onnxBytes())
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/models/import", body)
	req.Header.Set("X-User-ID", "owner")
	req.Header.Set("Content-Type", ct)
	mux.ServeHTTP(rec, req)
	if rec.Code != 400 {
		t.Fatalf("bad kind import: got %d, want 400", rec.Code)
	}
}

func TestModelRoutes_ImportOwnerGate(t *testing.T) {
	mux, _ := newModelMux(t, ownerDeny)
	body, ct := buildImport(t, models.ArtifactModel, onnxBytes())
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/models/import", body)
	req.Header.Set("X-User-ID", "intruder")
	req.Header.Set("Content-Type", ct)
	mux.ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Fatalf("non-owner import: got %d, want 403", rec.Code)
	}
}
