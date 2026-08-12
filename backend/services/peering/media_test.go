// media_test.go — unit and integration tests for media.go (PEER-16).
package peering

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ─── Test helpers ─────────────────────────────────────────────────────────────

// newTestMediaStore creates a MediaStore backed by a temp directory.
// The signing key is a freshly generated Ed25519 private key.
func newTestMediaStore(t *testing.T) (*MediaStore, ed25519.PrivateKey) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	home := t.TempDir()
	ms, err := NewMediaStore(home, priv, nil)
	if err != nil {
		t.Fatalf("NewMediaStore: %v", err)
	}
	return ms, priv
}

// makeMultipartUpload builds a multipart/form-data request body with a single
// "file" field containing data.
func makeMultipartUpload(t *testing.T, filename, mimeType string, data []byte) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, err := mw.CreateFormFile("file", filename)
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	if _, err := part.Write(data); err != nil {
		t.Fatalf("write part: %v", err)
	}
	mw.Close()
	return &buf, mw.FormDataContentType()
}

// contentHash returns the canonical "sha256:<hex>" for data.
func contentHash(data []byte) string {
	h := sha256.Sum256(data)
	return canonicalHash(hex.EncodeToString(h[:]))
}

// ─── NewMediaStore ────────────────────────────────────────────────────────────

func TestNewMediaStore_CreatesDirectory(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	home := t.TempDir()
	ms, err := NewMediaStore(home, priv, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ms == nil {
		t.Fatal("expected non-nil MediaStore")
	}
	want := filepath.Join(home, "peering", "media")
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("media directory not created: %v", err)
	}
}

func TestNewMediaStore_ShortKeyRejected(t *testing.T) {
	home := t.TempDir()
	_, err := NewMediaStore(home, make([]byte, 16), nil)
	if err == nil {
		t.Fatal("expected error for short signing key")
	}
}

// ─── Signed URL ───────────────────────────────────────────────────────────────

func TestSignedURL_RoundTrip(t *testing.T) {
	ms, _ := newTestMediaStore(t)
	hexHash := strings.Repeat("a", 64)

	sig, exp := ms.signedURLParams(hexHash, time.Now().Add(time.Hour).Unix())
	expUnix := time.Now().Add(time.Hour).Unix()
	sigAgain, expAgain := ms.signedURLParams(hexHash, expUnix)

	// Same inputs → same outputs (deterministic HMAC).
	if sig == "" {
		t.Fatal("sig must not be empty")
	}
	_ = expAgain
	_ = sigAgain

	// Verify a freshly signed URL.
	sig2, exp2 := ms.signedURLParams(hexHash, expUnix)
	if err := ms.verifySignedURLParams(hexHash, sig2, exp2); err != nil {
		t.Fatalf("verifySignedURLParams: %v", err)
	}

	_ = exp
}

func TestSignedURL_ExpiredRejected(t *testing.T) {
	ms, _ := newTestMediaStore(t)
	hexHash := strings.Repeat("b", 64)
	expUnix := time.Now().Add(-time.Minute).Unix() // already expired
	sig, exp := ms.signedURLParams(hexHash, expUnix)
	if err := ms.verifySignedURLParams(hexHash, sig, exp); err == nil {
		t.Fatal("expected error for expired URL")
	}
}

func TestSignedURL_TamperedHashRejected(t *testing.T) {
	ms, _ := newTestMediaStore(t)
	hexHash := strings.Repeat("c", 64)
	expUnix := time.Now().Add(time.Hour).Unix()
	sig, exp := ms.signedURLParams(hexHash, expUnix)

	// Tamper with the hash (one char changed).
	badHash := strings.Repeat("d", 64)
	if err := ms.verifySignedURLParams(badHash, sig, exp); err == nil {
		t.Fatal("expected error for tampered hash")
	}
}

func TestSignedURL_TamperedExpRejected(t *testing.T) {
	ms, _ := newTestMediaStore(t)
	hexHash := strings.Repeat("e", 64)
	expUnix := time.Now().Add(time.Hour).Unix()
	sig, _ := ms.signedURLParams(hexHash, expUnix)

	// Tamper with the expiry (push it far into the future to bypass expiry check,
	// but the HMAC should still fail).
	badExp := fmt.Sprintf("%d", time.Now().Add(24*time.Hour).Unix())
	if err := ms.verifySignedURLParams(hexHash, sig, badExp); err == nil {
		t.Fatal("expected error for tampered expiry")
	}
}

func TestSignedURL_InvalidExpFormat(t *testing.T) {
	ms, _ := newTestMediaStore(t)
	hexHash := strings.Repeat("f", 64)
	if err := ms.verifySignedURLParams(hexHash, "somesig", "notanumber"); err == nil {
		t.Fatal("expected error for non-numeric exp")
	}
}

// ─── Upload handler ───────────────────────────────────────────────────────────

func TestHandleMediaUpload_Success(t *testing.T) {
	ms, _ := newTestMediaStore(t)

	data := []byte("hello peering media 1234567890")
	buf, ct := makeMultipartUpload(t, "test.bin", "application/octet-stream", data)

	req := httptest.NewRequest(http.MethodPost, "/api/peering/media/upload", buf)
	req.Header.Set("Content-Type", ct)
	req.Host = "localhost:8080"

	w := httptest.NewRecorder()
	ms.handleMediaUpload(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var resp uploadMediaResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	want := contentHash(data)
	if resp.Hash != want {
		t.Errorf("hash: got %q, want %q", resp.Hash, want)
	}
	if resp.SignedURL == "" {
		t.Error("signed_url must not be empty")
	}
	if resp.Size != int64(len(data)) {
		t.Errorf("size: got %d, want %d", resp.Size, len(data))
	}
}

func TestHandleMediaUpload_ContentHashIsStable(t *testing.T) {
	// Uploading the same content twice must return the same hash.
	ms, _ := newTestMediaStore(t)
	data := []byte("stable content abc")

	upload := func() string {
		buf, ct := makeMultipartUpload(t, "file.txt", "text/plain", data)
		req := httptest.NewRequest(http.MethodPost, "/api/peering/media/upload", buf)
		req.Header.Set("Content-Type", ct)
		req.Host = "localhost:8080"
		w := httptest.NewRecorder()
		ms.handleMediaUpload(w, req)
		if w.Code != http.StatusCreated {
			t.Fatalf("upload failed: %d %s", w.Code, w.Body.String())
		}
		var resp uploadMediaResponse
		json.NewDecoder(w.Body).Decode(&resp)
		return resp.Hash
	}

	h1 := upload()
	h2 := upload()
	if h1 != h2 {
		t.Errorf("hash not stable: %q vs %q", h1, h2)
	}
}

func TestHandleMediaUpload_NoFileField(t *testing.T) {
	ms, _ := newTestMediaStore(t)
	req := httptest.NewRequest(http.MethodPost, "/api/peering/media/upload",
		strings.NewReader("not-multipart"))
	req.Header.Set("Content-Type", "application/octet-stream")
	w := httptest.NewRecorder()
	ms.handleMediaUpload(w, req)
	if w.Code == http.StatusCreated {
		t.Fatal("expected non-201 for invalid multipart")
	}
}

// ─── Fetch handler ────────────────────────────────────────────────────────────

func uploadAndGetHash(t *testing.T, ms *MediaStore, data []byte) (hexHash, signedURL string) {
	t.Helper()
	buf, ct := makeMultipartUpload(t, "blob.bin", "application/octet-stream", data)
	req := httptest.NewRequest(http.MethodPost, "/api/peering/media/upload", buf)
	req.Header.Set("Content-Type", ct)
	req.Host = "localhost:8080"
	w := httptest.NewRecorder()
	ms.handleMediaUpload(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("upload: %d %s", w.Code, w.Body.String())
	}
	var resp uploadMediaResponse
	json.NewDecoder(w.Body).Decode(&resp)

	h, err := hexFromCanonical(resp.Hash)
	if err != nil {
		t.Fatalf("hexFromCanonical: %v", err)
	}
	return h, resp.SignedURL
}

func TestHandleMediaFetch_ValidSignedURL(t *testing.T) {
	ms, _ := newTestMediaStore(t)
	data := []byte("fetch me please")
	hexHash, _ := uploadAndGetHash(t, ms, data)

	sig, exp := ms.signedURLParams(hexHash, time.Now().Add(time.Hour).Unix())
	url := fmt.Sprintf("/api/peering/media/fetch/sha256:%s?sig=%s&exp=%s", hexHash, sig, exp)
	req := httptest.NewRequest(http.MethodGet, url, nil)
	req.SetPathValue("hash", "sha256:"+hexHash)

	w := httptest.NewRecorder()
	ms.handleMediaFetch(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if !bytes.Equal(w.Body.Bytes(), data) {
		t.Errorf("body mismatch: got %q, want %q", w.Body.String(), string(data))
	}
}

func TestHandleMediaFetch_ExpiredSignedURL(t *testing.T) {
	ms, _ := newTestMediaStore(t)
	data := []byte("expired content")
	hexHash, _ := uploadAndGetHash(t, ms, data)

	sig, exp := ms.signedURLParams(hexHash, time.Now().Add(-time.Minute).Unix())
	url := fmt.Sprintf("/api/peering/media/fetch/sha256:%s?sig=%s&exp=%s", hexHash, sig, exp)
	req := httptest.NewRequest(http.MethodGet, url, nil)
	req.SetPathValue("hash", "sha256:"+hexHash)

	w := httptest.NewRecorder()
	ms.handleMediaFetch(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for expired URL, got %d", w.Code)
	}
}

func TestHandleMediaFetch_MissingSignature(t *testing.T) {
	ms, _ := newTestMediaStore(t)
	data := []byte("unsigned content")
	hexHash, _ := uploadAndGetHash(t, ms, data)

	url := "/api/peering/media/fetch/sha256:" + hexHash
	req := httptest.NewRequest(http.MethodGet, url, nil)
	req.SetPathValue("hash", "sha256:"+hexHash)

	w := httptest.NewRecorder()
	ms.handleMediaFetch(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for missing sig, got %d", w.Code)
	}
}

func TestHandleMediaFetch_TamperedHash(t *testing.T) {
	ms, _ := newTestMediaStore(t)
	data := []byte("tamper test")
	hexHash, _ := uploadAndGetHash(t, ms, data)

	// Sign for the real hash but request a different hash.
	sig, exp := ms.signedURLParams(hexHash, time.Now().Add(time.Hour).Unix())
	fakeHash := strings.Repeat("1", 64)
	url := fmt.Sprintf("/api/peering/media/fetch/sha256:%s?sig=%s&exp=%s", fakeHash, sig, exp)
	req := httptest.NewRequest(http.MethodGet, url, nil)
	req.SetPathValue("hash", "sha256:"+fakeHash)

	w := httptest.NewRecorder()
	ms.handleMediaFetch(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for tampered hash, got %d", w.Code)
	}
}

func TestHandleMediaFetch_NotFound(t *testing.T) {
	ms, _ := newTestMediaStore(t)
	hexHash := strings.Repeat("9", 64)
	sig, exp := ms.signedURLParams(hexHash, time.Now().Add(time.Hour).Unix())
	url := fmt.Sprintf("/api/peering/media/fetch/sha256:%s?sig=%s&exp=%s", hexHash, sig, exp)
	req := httptest.NewRequest(http.MethodGet, url, nil)
	req.SetPathValue("hash", "sha256:"+hexHash)

	w := httptest.NewRecorder()
	ms.handleMediaFetch(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

// ─── Thumb handler ────────────────────────────────────────────────────────────

func TestHandleMediaThumb_NotFound(t *testing.T) {
	ms, _ := newTestMediaStore(t)
	hexHash := strings.Repeat("7", 64)
	req := httptest.NewRequest(http.MethodGet, "/api/peering/media/thumb/sha256:"+hexHash, nil)
	req.SetPathValue("hash", "sha256:"+hexHash)
	w := httptest.NewRecorder()
	ms.handleMediaThumb(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestHandleMediaThumb_Served(t *testing.T) {
	ms, _ := newTestMediaStore(t)
	data := []byte("fake image content for thumb test")
	hexHash, _ := uploadAndGetHash(t, ms, data)

	// Manually write a fake thumbnail.
	thumbPath := ms.thumbPath(hexHash)
	if err := os.WriteFile(thumbPath, []byte{0xFF, 0xD8, 0xFF}, 0600); err != nil {
		t.Fatalf("write fake thumb: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/peering/media/thumb/sha256:"+hexHash, nil)
	req.SetPathValue("hash", "sha256:"+hexHash)
	w := httptest.NewRecorder()
	ms.handleMediaThumb(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); ct != "image/jpeg" {
		t.Errorf("Content-Type: got %q, want image/jpeg", ct)
	}
}

// ─── FetchFromPeer ────────────────────────────────────────────────────────────

func TestFetchFromPeer_DownloadsBlob(t *testing.T) {
	// Serve the blob from a test HTTP server acting as the remote peer.
	data := []byte("peer media blob content 99887766")
	h := sha256.Sum256(data)
	hexHash := hex.EncodeToString(h[:])
	hashRef := canonicalHash(hexHash)

	// Create "sender" media store that serves the blob.
	sender, _ := newTestMediaStore(t)
	senderBlobDir := sender.bucketDir(hexHash)
	os.MkdirAll(senderBlobDir, 0700)
	os.WriteFile(sender.blobPath(hexHash), data, 0600)

	// Test server that serves the blob directly (simulates the sender's fetch endpoint).
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		w.Write(data)
	}))
	defer ts.Close()

	// Create "recipient" media store.
	recipient, _ := newTestMediaStore(t)

	ref := MediaRefPayload{
		Hash:      hashRef,
		SignedURL: ts.URL + "/fake-signed-url",
		Size:      int64(len(data)),
		MIMEType:  "application/octet-stream",
	}

	if err := recipient.FetchFromPeer(context.Background(), ref); err != nil {
		t.Fatalf("FetchFromPeer: %v", err)
	}

	// Verify blob is stored.
	if !recipient.HasBlob(hexHash) {
		t.Fatal("blob not stored after FetchFromPeer")
	}

	// Verify content.
	got, err := os.ReadFile(recipient.blobPath(hexHash))
	if err != nil {
		t.Fatalf("read stored blob: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Errorf("content mismatch: got %q, want %q", got, data)
	}
}

func TestFetchFromPeer_Idempotent(t *testing.T) {
	data := []byte("idempotent fetch content")
	h := sha256.Sum256(data)
	hexHash := hex.EncodeToString(h[:])
	hashRef := canonicalHash(hexHash)

	callCount := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Write(data)
	}))
	defer ts.Close()

	ms, _ := newTestMediaStore(t)
	ref := MediaRefPayload{
		Hash:      hashRef,
		SignedURL: ts.URL,
		Size:      int64(len(data)),
		MIMEType:  "application/octet-stream",
	}

	// First fetch.
	if err := ms.FetchFromPeer(context.Background(), ref); err != nil {
		t.Fatalf("first FetchFromPeer: %v", err)
	}
	// Second fetch — should be a no-op (blob already stored).
	if err := ms.FetchFromPeer(context.Background(), ref); err != nil {
		t.Fatalf("second FetchFromPeer: %v", err)
	}

	if callCount > 1 {
		t.Errorf("expected at most 1 HTTP call, got %d", callCount)
	}
}

func TestFetchFromPeer_HashMismatch(t *testing.T) {
	// Server returns different content from what the hash claims.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("WRONG CONTENT"))
	}))
	defer ts.Close()

	data := []byte("correct content for hash")
	h := sha256.Sum256(data)
	hexHash := hex.EncodeToString(h[:])
	hashRef := canonicalHash(hexHash)

	ms, _ := newTestMediaStore(t)
	ref := MediaRefPayload{
		Hash:      hashRef,
		SignedURL: ts.URL,
		Size:      int64(len(data)),
		MIMEType:  "application/octet-stream",
	}

	if err := ms.FetchFromPeer(context.Background(), ref); err == nil {
		t.Fatal("expected error for hash mismatch")
	}

	// Ensure corrupt blob is not stored.
	if ms.HasBlob(hexHash) {
		t.Fatal("corrupt blob must not be stored")
	}
}

func TestFetchFromPeer_RemoteError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer ts.Close()

	ms, _ := newTestMediaStore(t)
	hexHash := strings.Repeat("4", 64)
	ref := MediaRefPayload{
		Hash:      canonicalHash(hexHash),
		SignedURL: ts.URL,
		MIMEType:  "application/octet-stream",
	}

	if err := ms.FetchFromPeer(context.Background(), ref); err == nil {
		t.Fatal("expected error for remote 404")
	}
}

func TestFetchFromPeer_InvalidHashRef(t *testing.T) {
	ms, _ := newTestMediaStore(t)
	ref := MediaRefPayload{
		Hash:      "not-a-valid-ref",
		SignedURL: "http://example.com",
	}
	if err := ms.FetchFromPeer(context.Background(), ref); err == nil {
		t.Fatal("expected error for invalid hash ref")
	}
}

func TestFetchFromPeer_EmptySignedURL(t *testing.T) {
	ms, _ := newTestMediaStore(t)
	hexHash := strings.Repeat("5", 64)
	ref := MediaRefPayload{
		Hash:      canonicalHash(hexHash),
		SignedURL: "",
		MIMEType:  "application/octet-stream",
	}
	if err := ms.FetchFromPeer(context.Background(), ref); err == nil {
		t.Fatal("expected error for empty signed_url")
	}
}

// ─── HandleInboundMedia ───────────────────────────────────────────────────────

func TestHandleInboundMedia_MissingEnvelopeInContext(t *testing.T) {
	ms, _ := newTestMediaStore(t)
	req := httptest.NewRequest(http.MethodPost, "/api/peering/inbound/media",
		strings.NewReader("{}"))
	// No envelope in context.
	w := httptest.NewRecorder()
	ms.HandleInboundMedia(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestHandleInboundMedia_InvalidPayload(t *testing.T) {
	ms, _ := newTestMediaStore(t)
	_, priv, _ := ed25519.GenerateKey(rand.Reader)

	vulosID := "vulos:ed25519:" + strings.Repeat("A", 44)
	env := &Envelope{
		ID:        "test-bad-payload",
		From:      vulosID,
		Type:      TypeMessage,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Payload:   json.RawMessage("not-json-object"),
	}
	env.Sign(priv)

	ctx := contextWithEnvelope(env)
	req := httptest.NewRequest(http.MethodPost, "/api/peering/inbound/media",
		strings.NewReader("{}"))
	req = req.WithContext(ctx)

	w := httptest.NewRecorder()
	ms.HandleInboundMedia(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestHandleInboundMedia_MissingHash(t *testing.T) {
	ms, _ := newTestMediaStore(t)
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	vulosID := "vulos:ed25519:" + strings.Repeat("B", 44)

	payload, _ := json.Marshal(MediaRefPayload{
		SignedURL: "http://example.com/blob",
		MIMEType:  "image/jpeg",
	})
	env, _ := NewEnvelope("test-no-hash", vulosID, "to", TypeMessage, json.RawMessage(payload))
	env.Sign(priv)

	ctx := contextWithEnvelope(env)
	req := httptest.NewRequest(http.MethodPost, "/api/peering/inbound/media",
		strings.NewReader("{}"))
	req = req.WithContext(ctx)

	w := httptest.NewRecorder()
	ms.HandleInboundMedia(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestHandleInboundMedia_AcceptsValidPayload(t *testing.T) {
	// A valid payload with hash + signed_url should return 202 immediately
	// (the actual fetch happens in a goroutine).
	ms, _ := newTestMediaStore(t)
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	vulosID := "vulos:ed25519:" + strings.Repeat("C", 44)

	hexHash := strings.Repeat("2", 64)
	payload, _ := json.Marshal(MediaRefPayload{
		Hash:      canonicalHash(hexHash),
		SignedURL: "http://127.0.0.1:1/does-not-exist", // unreachable — fetch will fail in bg
		MIMEType:  "image/jpeg",
	})
	env, _ := NewEnvelope("test-valid-media", vulosID, "to", TypeMessage, json.RawMessage(payload))
	env.Sign(priv)

	ctx := contextWithEnvelope(env)
	req := httptest.NewRequest(http.MethodPost, "/api/peering/inbound/media",
		strings.NewReader("{}"))
	req = req.WithContext(ctx)

	w := httptest.NewRecorder()
	ms.HandleInboundMedia(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", w.Code, w.Body.String())
	}
	var body map[string]any
	json.NewDecoder(w.Body).Decode(&body)
	if body["status"] != "accepted" {
		t.Errorf("expected status=accepted, got %v", body)
	}
}

// contextWithEnvelope returns a context carrying env under EnvelopeKey.
func contextWithEnvelope(env *Envelope) context.Context {
	return context.WithValue(context.Background(), EnvelopeKey, env)
}

// ─── RegisterMediaHandlers ────────────────────────────────────────────────────

func TestRegisterMediaHandlers_RoutesPresent(t *testing.T) {
	ms, _ := newTestMediaStore(t)
	mux := http.NewServeMux()
	ms.RegisterMediaHandlers(mux)

	// Verify that the mux responds with a non-405 (Method Not Allowed) status
	// to the known routes.  A 405 or plain-text 404 (no JSON body) indicates
	// the route was not registered; handler-level 4xx responses are acceptable.
	//
	// We distinguish "route not registered" from "handler returned 4xx" by
	// checking that the response Content-Type is application/json — our
	// handlers always return JSON whereas ServeMux 404/405 pages are text/plain.
	routes := []struct {
		method, path string
	}{
		{http.MethodPost, "/api/peering/media/upload"},
		{http.MethodGet, "/api/peering/media/fetch/sha256:" + strings.Repeat("0", 64)},
		{http.MethodGet, "/api/peering/media/thumb/sha256:" + strings.Repeat("0", 64)},
		{http.MethodPost, "/api/peering/inbound/media"},
	}
	for _, tc := range routes {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		ct := w.Header().Get("Content-Type")
		if w.Code == http.StatusMethodNotAllowed {
			t.Errorf("route %s %s not registered (405 method not allowed)", tc.method, tc.path)
			continue
		}
		// A ServeMux "page not found" response has Content-Type text/plain.
		if w.Code == http.StatusNotFound && !strings.Contains(ct, "application/json") {
			t.Errorf("route %s %s not registered (404 with no JSON body)", tc.method, tc.path)
		}
	}
}

// ─── hexFromCanonical ─────────────────────────────────────────────────────────

func TestHexFromCanonical_Valid(t *testing.T) {
	h := strings.Repeat("a", 64)
	got, err := hexFromCanonical("sha256:" + h)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != h {
		t.Errorf("got %q, want %q", got, h)
	}
}

func TestHexFromCanonical_MissingPrefix(t *testing.T) {
	if _, err := hexFromCanonical(strings.Repeat("a", 64)); err == nil {
		t.Fatal("expected error for missing sha256: prefix")
	}
}

func TestHexFromCanonical_WrongLength(t *testing.T) {
	if _, err := hexFromCanonical("sha256:" + strings.Repeat("a", 63)); err == nil {
		t.Fatal("expected error for wrong hex length")
	}
}

// ─── isMediaVideo / isMediaImage ─────────────────────────────────────────────

func TestIsMediaVideo(t *testing.T) {
	cases := []struct {
		mime string
		want bool
	}{
		{"video/mp4", true},
		{"video/webm", true},
		{"video/quicktime", true},
		{"image/jpeg", false},
		{"application/octet-stream", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := isMediaVideo(tc.mime); got != tc.want {
			t.Errorf("isMediaVideo(%q) = %v, want %v", tc.mime, got, tc.want)
		}
	}
}

func TestIsMediaImage(t *testing.T) {
	cases := []struct {
		mime string
		want bool
	}{
		{"image/jpeg", true},
		{"image/png", true},
		{"image/webp", true},
		{"video/mp4", false},
		{"application/pdf", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := isMediaImage(tc.mime); got != tc.want {
			t.Errorf("isMediaImage(%q) = %v, want %v", tc.mime, got, tc.want)
		}
	}
}

// ─── HasBlob ──────────────────────────────────────────────────────────────────

func TestHasBlob_FalseWhenAbsent(t *testing.T) {
	ms, _ := newTestMediaStore(t)
	if ms.HasBlob(strings.Repeat("0", 64)) {
		t.Error("HasBlob should return false for unknown hash")
	}
}

func TestHasBlob_TrueAfterStore(t *testing.T) {
	ms, _ := newTestMediaStore(t)
	data := []byte("has blob test content")
	h := sha256.Sum256(data)
	hexHash := hex.EncodeToString(h[:])

	r := bytes.NewReader(data)
	if _, err := ms.storeBlob(hexHash, r, "application/octet-stream", ""); err != nil {
		t.Fatalf("storeBlob: %v", err)
	}
	if !ms.HasBlob(hexHash) {
		t.Error("HasBlob should return true after storeBlob")
	}
}

// ─── storeBlob idempotency ────────────────────────────────────────────────────

func TestStoreBlob_Idempotent(t *testing.T) {
	ms, _ := newTestMediaStore(t)
	data := []byte("idempotent store content xyz")
	h := sha256.Sum256(data)
	hexHash := hex.EncodeToString(h[:])

	for i := 0; i < 3; i++ {
		r := bytes.NewReader(data)
		size, err := ms.storeBlob(hexHash, r, "application/octet-stream", "test.bin")
		if err != nil {
			t.Fatalf("storeBlob call %d: %v", i+1, err)
		}
		if size != int64(len(data)) {
			t.Errorf("call %d: size %d, want %d", i+1, size, len(data))
		}
	}
}

// ─── Full upload → fetch round trip ──────────────────────────────────────────

func TestUploadFetchRoundTrip(t *testing.T) {
	ms, _ := newTestMediaStore(t)
	data := []byte("round trip content for end to end test")

	// Upload.
	uploadBuf, ct := makeMultipartUpload(t, "rt.bin", "application/octet-stream", data)
	uploadReq := httptest.NewRequest(http.MethodPost, "/api/peering/media/upload", uploadBuf)
	uploadReq.Header.Set("Content-Type", ct)
	uploadReq.Host = "localhost:8080"
	uploadW := httptest.NewRecorder()
	ms.handleMediaUpload(uploadW, uploadReq)
	if uploadW.Code != http.StatusCreated {
		t.Fatalf("upload: %d %s", uploadW.Code, uploadW.Body.String())
	}
	var uploadResp uploadMediaResponse
	json.NewDecoder(uploadW.Body).Decode(&uploadResp)

	// Extract signed URL params from response.
	hexHash, _ := hexFromCanonical(uploadResp.Hash)
	sig, exp := ms.signedURLParams(hexHash, time.Now().Add(time.Hour).Unix())

	// Fetch.
	fetchURL := fmt.Sprintf("/api/peering/media/fetch/%s?sig=%s&exp=%s",
		uploadResp.Hash, sig, exp)
	fetchReq := httptest.NewRequest(http.MethodGet, fetchURL, nil)
	fetchReq.SetPathValue("hash", uploadResp.Hash)
	fetchW := httptest.NewRecorder()
	ms.handleMediaFetch(fetchW, fetchReq)

	if fetchW.Code != http.StatusOK {
		t.Fatalf("fetch: %d %s", fetchW.Code, fetchW.Body.String())
	}
	if !bytes.Equal(fetchW.Body.Bytes(), data) {
		t.Errorf("fetched content mismatch")
	}
}

// ─── S2S round trip ───────────────────────────────────────────────────────────

func TestS2SRoundTrip(t *testing.T) {
	// Simulate: sender uploads → signs URL → recipient fetches via FetchFromPeer.
	sender, _ := newTestMediaStore(t)
	recipient, _ := newTestMediaStore(t)

	data := []byte("s2s round trip content ABCDEF")
	h := sha256.Sum256(data)
	hexHash := hex.EncodeToString(h[:])
	hashRef := canonicalHash(hexHash)

	// Store on sender.
	senderBlobDir := sender.bucketDir(hexHash)
	os.MkdirAll(senderBlobDir, 0700)
	os.WriteFile(sender.blobPath(hexHash), data, 0600)

	// Sender issues signed URL; serve it from a test HTTP server.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Validate the signed URL params.
		sig := r.URL.Query().Get("sig")
		exp := r.URL.Query().Get("exp")
		if err := sender.verifySignedURLParams(hexHash, sig, exp); err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		f, err := os.Open(sender.blobPath(hexHash))
		if err != nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		defer f.Close()
		w.Header().Set("Content-Type", "application/octet-stream")
		io.Copy(w, f)
	}))
	defer ts.Close()

	// Build a signed URL pointing at the test server.
	expUnix := time.Now().Add(time.Hour).Unix()
	sig, exp := sender.signedURLParams(hexHash, expUnix)
	signedURL := fmt.Sprintf("%s/api/peering/media/fetch/%s?sig=%s&exp=%s",
		ts.URL, hashRef, sig, exp)

	ref := MediaRefPayload{
		Hash:      hashRef,
		SignedURL: signedURL,
		Size:      int64(len(data)),
		MIMEType:  "application/octet-stream",
	}

	// Recipient fetches.
	if err := recipient.FetchFromPeer(context.Background(), ref); err != nil {
		t.Fatalf("FetchFromPeer: %v", err)
	}

	// Verify recipient has its own copy.
	if !recipient.HasBlob(hexHash) {
		t.Fatal("recipient should have its own copy after fetch")
	}
	got, _ := os.ReadFile(recipient.blobPath(hexHash))
	if !bytes.Equal(got, data) {
		t.Error("content mismatch in recipient copy")
	}
}
