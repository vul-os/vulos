package main

// routes_files_resumable_test.go — route-layer coverage for the tus-style
// resumable upload endpoints (routes_files_resumable.go). Drives the REAL
// registrar over httptest through the full create → PATCH → HEAD → resume →
// finalize lifecycle, and asserts the security contract: unauth 401, nil-manager
// 503, cross-owner 404, offset-conflict 409.

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"vulos/backend/services/upload"
)

func newResumableMux(t *testing.T) (*http.ServeMux, *upload.Manager) {
	t.Helper()
	svc, _ := newFilesSvc(t)
	mgr, err := upload.New(
		t.TempDir()+"/uploads.db",
		&filesUploadSink{svc: svc},
		upload.Config{StateDir: t.TempDir(), MaxChunkBytes: 1024},
	)
	if err != nil {
		t.Fatalf("upload.New: %v", err)
	}
	t.Cleanup(func() { mgr.Close() })
	mux := http.NewServeMux()
	registerFilesRoutes(mux, svc)
	registerFilesResumableRoutes(mux, mgr)
	return mux, mgr
}

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

func chunkSHAHeader(b []byte) string {
	h := sha256.Sum256(b)
	return "sha256 " + base64.StdEncoding.EncodeToString(h[:])
}

func hexSHA256(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func createUpload(t *testing.T, mux *http.ServeMux, uid string, length int, name, sha string) string {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/api/files/upload/resumable", nil)
	r.Header.Set("X-User-ID", uid)
	r.Header.Set("Upload-Length", strconv.Itoa(length))
	meta := "filename " + b64(name)
	if sha != "" {
		meta += ",sha256 " + b64(sha)
	}
	r.Header.Set("Upload-Metadata", meta)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: got %d, want 201 (%s)", w.Code, w.Body.String())
	}
	loc := w.Header().Get("Location")
	if loc == "" {
		t.Fatal("create: no Location header")
	}
	// id is the last path segment.
	return loc[len("/api/files/upload/resumable/"):]
}

func patchChunk(t *testing.T, mux *http.ServeMux, uid, id string, offset int, chunk []byte, withSHA bool) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPatch, "/api/files/upload/resumable/"+id, bytes.NewReader(chunk))
	r.Header.Set("X-User-ID", uid)
	r.Header.Set("Content-Type", "application/offset+octet-stream")
	r.Header.Set("Upload-Offset", strconv.Itoa(offset))
	if withSHA {
		r.Header.Set("Upload-Checksum", chunkSHAHeader(chunk))
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

func TestResumable_FullLifecycle(t *testing.T) {
	mux, _ := newResumableMux(t)
	data := bytes.Repeat([]byte("A"), 2500)
	id := createUpload(t, mux, "alice", len(data), "movie.bin", hexSHA256(data))

	// Chunk 1 (1000 bytes) with per-chunk checksum.
	w := patchChunk(t, mux, "alice", id, 0, data[:1000], true)
	if w.Code != http.StatusNoContent {
		t.Fatalf("chunk1: got %d (%s)", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Upload-Offset"); got != "1000" {
		t.Fatalf("offset after chunk1 = %s, want 1000", got)
	}

	// Simulate interruption: HEAD to learn the offset.
	hr := httptest.NewRequest(http.MethodHead, "/api/files/upload/resumable/"+id, nil)
	hr.Header.Set("X-User-ID", "alice")
	hw := httptest.NewRecorder()
	mux.ServeHTTP(hw, hr)
	if hw.Code != http.StatusOK || hw.Header().Get("Upload-Offset") != "1000" {
		t.Fatalf("HEAD: code=%d offset=%s", hw.Code, hw.Header().Get("Upload-Offset"))
	}

	// Chunk 2 + 3 resume to completion.
	if w := patchChunk(t, mux, "alice", id, 1000, data[1000:2000], true); w.Code != http.StatusNoContent {
		t.Fatalf("chunk2: %d", w.Code)
	}
	w = patchChunk(t, mux, "alice", id, 2000, data[2000:], true)
	if w.Code != http.StatusNoContent {
		t.Fatalf("chunk3: got %d (%s)", w.Code, w.Body.String())
	}
	if w.Header().Get("Upload-Complete") != "1" {
		t.Fatalf("final chunk not marked complete")
	}
	if w.Header().Get("Vulos-Node-Id") == "" {
		t.Fatalf("no node id on completion")
	}
}

func TestResumable_Unauth401(t *testing.T) {
	mux, _ := newResumableMux(t)
	r := httptest.NewRequest(http.MethodPost, "/api/files/upload/resumable", nil)
	r.Header.Set("Upload-Length", "10")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("unauth create: got %d, want 401", w.Code)
	}
}

func TestResumable_NilManager503(t *testing.T) {
	mux := http.NewServeMux()
	registerFilesResumableRoutes(mux, nil)
	r := httptest.NewRequest(http.MethodPost, "/api/files/upload/resumable", nil)
	r.Header.Set("X-User-ID", "alice")
	r.Header.Set("Upload-Length", "10")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("nil manager: got %d, want 503", w.Code)
	}
}

func TestResumable_CrossOwner404(t *testing.T) {
	mux, _ := newResumableMux(t)
	id := createUpload(t, mux, "alice", 100, "secret.bin", "")
	// Mallory HEADs alice's upload → 404 (existence hidden).
	hr := httptest.NewRequest(http.MethodHead, "/api/files/upload/resumable/"+id, nil)
	hr.Header.Set("X-User-ID", "mallory")
	hw := httptest.NewRecorder()
	mux.ServeHTTP(hw, hr)
	if hw.Code != http.StatusNotFound {
		t.Fatalf("cross-owner HEAD: got %d, want 404", hw.Code)
	}
	// Mallory PATCHes → 404.
	pw := patchChunk(t, mux, "mallory", id, 0, []byte("x"), false)
	if pw.Code != http.StatusNotFound {
		t.Fatalf("cross-owner PATCH: got %d, want 404", pw.Code)
	}
}

func TestResumable_OffsetConflict409(t *testing.T) {
	mux, _ := newResumableMux(t)
	data := bytes.Repeat([]byte("Z"), 200)
	id := createUpload(t, mux, "alice", len(data), "x.bin", "")
	if w := patchChunk(t, mux, "alice", id, 0, data[:100], false); w.Code != http.StatusNoContent {
		t.Fatalf("chunk1: %d", w.Code)
	}
	// Replay at offset 0 → 409.
	if w := patchChunk(t, mux, "alice", id, 0, data[:100], false); w.Code != http.StatusConflict {
		t.Fatalf("replay: got %d, want 409", w.Code)
	}
}

func TestResumable_ChecksumMismatch460(t *testing.T) {
	mux, _ := newResumableMux(t)
	id := createUpload(t, mux, "alice", 10, "x.bin", "")
	// Send a chunk with a checksum that doesn't match the body.
	r := httptest.NewRequest(http.MethodPatch, "/api/files/upload/resumable/"+id, bytes.NewReader([]byte("0123456789")))
	r.Header.Set("X-User-ID", "alice")
	r.Header.Set("Content-Type", "application/offset+octet-stream")
	r.Header.Set("Upload-Offset", "0")
	r.Header.Set("Upload-Checksum", chunkSHAHeader([]byte("wrong")))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != 460 {
		t.Fatalf("checksum mismatch: got %d, want 460", w.Code)
	}
}

func TestResumable_OptionsDiscovery(t *testing.T) {
	mux, _ := newResumableMux(t)
	r := httptest.NewRequest(http.MethodOptions, "/api/files/upload/resumable", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusNoContent {
		t.Fatalf("OPTIONS: got %d, want 204", w.Code)
	}
	if w.Header().Get("Tus-Resumable") != upload.TusVersion {
		t.Fatalf("OPTIONS missing Tus-Resumable")
	}
}
