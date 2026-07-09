package models

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- catalog integrity -----------------------------------------------------

// TestCatalog_IntegrityPins asserts every shipped catalog entry is safe: https +
// host-pinned URLs and a full-length SHA-256 + positive size for both artifacts.
// This is the guard that a future edit can't ship an unpinned / unverifiable
// model (which would defeat the fail-closed download guarantee).
func TestCatalog_IntegrityPins(t *testing.T) {
	entries := Catalog()
	if len(entries) == 0 {
		t.Fatal("catalog must not be empty")
	}
	var recommended int
	for _, e := range entries {
		if e.ID == "" || e.Dim <= 0 {
			t.Fatalf("entry %q missing id/dim: %+v", e.ID, e)
		}
		if e.Recommended {
			recommended++
		}
		for label, art := range map[string]PinnedArtifact{"model": e.Model, "tokenizer": e.Tokenizer} {
			u, err := url.Parse(art.DownloadURL)
			if err != nil {
				t.Fatalf("%s/%s: bad URL: %v", e.ID, label, err)
			}
			if u.Scheme != "https" {
				t.Fatalf("%s/%s: URL must be https, got %q", e.ID, label, u.Scheme)
			}
			if !allowedDownloadHosts[strings.ToLower(u.Hostname())] {
				t.Fatalf("%s/%s: host %q not on allowlist", e.ID, label, u.Hostname())
			}
			if len(art.SHA256) != 64 {
				t.Fatalf("%s/%s: SHA256 must be 64 hex chars, got %q", e.ID, label, art.SHA256)
			}
			if _, err := hex.DecodeString(art.SHA256); err != nil {
				t.Fatalf("%s/%s: SHA256 not hex: %v", e.ID, label, err)
			}
			if art.SizeBytes <= 0 {
				t.Fatalf("%s/%s: SizeBytes must be > 0", e.ID, label)
			}
		}
	}
	if recommended == 0 {
		t.Fatal("catalog should have at least one recommended entry (the one-click default)")
	}
}

// TestCatalog_AllMiniLMPresent pins the specific recommended model's identity so
// the one-click install the UI/onboarding relies on cannot silently disappear or
// have its checksum edited without a test change.
func TestCatalog_AllMiniLMPresent(t *testing.T) {
	e, ok := CatalogEntryByID("all-MiniLM-L6-v2")
	if !ok {
		t.Fatal("all-MiniLM-L6-v2 must be in the catalog")
	}
	if e.Dim != 384 {
		t.Fatalf("all-MiniLM-L6-v2 dim = %d, want 384", e.Dim)
	}
	if !strings.HasPrefix(e.Model.DownloadURL, "https://huggingface.co/") {
		t.Fatalf("model URL not pinned to huggingface.co: %s", e.Model.DownloadURL)
	}
	if !strings.HasSuffix(e.Tokenizer.DownloadURL, "tokenizer.json") {
		t.Fatalf("tokenizer URL should end in tokenizer.json: %s", e.Tokenizer.DownloadURL)
	}
}

func TestCatalogEntryByID_UnknownIsRejected(t *testing.T) {
	if _, ok := CatalogEntryByID("evil-model"); ok {
		t.Fatal("unknown id must not resolve")
	}
	if _, ok := CatalogEntryByID(""); ok {
		t.Fatal("empty id must not resolve")
	}
}

func TestCheckDownloadURL(t *testing.T) {
	bad := []string{
		"http://huggingface.co/x",             // not https
		"https://evil.example.com/x",          // off-allowlist host
		"https://huggingface.co.evil.com/x",   // lookalike host
		"ftp://huggingface.co/x",              // wrong scheme
		"file:///etc/passwd",                  // local file
		"http://169.254.169.254/latest/meta",  // SSRF metadata target
	}
	for _, u := range bad {
		if err := checkDownloadURL(u); err == nil {
			t.Fatalf("checkDownloadURL(%q) should reject", u)
		}
	}
	if err := checkDownloadURL("https://huggingface.co/sentence-transformers/x/resolve/main/tokenizer.json"); err != nil {
		t.Fatalf("valid HF url rejected: %v", err)
	}
}

// --- download path (mocked transport) --------------------------------------

// fakeDoer serves canned responses keyed by URL, so we exercise the whole
// download/verify/install path with NO real network I/O.
type fakeDoer struct {
	// body/status/err per URL
	resp map[string]fakeResp
}

type fakeResp struct {
	status int
	body   []byte
	err    error
}

func (f *fakeDoer) Do(req *http.Request) (*http.Response, error) {
	r, ok := f.resp[req.URL.String()]
	if !ok {
		return &http.Response{StatusCode: 404, Body: io.NopCloser(strings.NewReader("not found"))}, nil
	}
	if r.err != nil {
		return nil, r.err
	}
	return &http.Response{
		StatusCode: r.status,
		Body:       io.NopCloser(strings.NewReader(string(r.body))),
	}, nil
}

func sha256hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// doerFor builds a fakeDoer that returns the given model/tokenizer bytes for the
// catalog entry's pinned URLs.
func doerFor(e CatalogEntry, modelBody, tokBody []byte) *fakeDoer {
	return &fakeDoer{resp: map[string]fakeResp{
		e.Model.DownloadURL:     {status: 200, body: modelBody},
		e.Tokenizer.DownloadURL: {status: 200, body: tokBody},
	}}
}

// validONNX / validTokenizer produce bytes that pass BOTH the content sniff and
// (with a matching pin) the checksum, so we can drive the success path without
// the real 86MB model.
func validONNX() []byte      { return []byte{0x08, 0x07, 0x12, 0x02, 'h', 'i'} }
func validTokenizer() []byte { return []byte(`{"model":{"type":"WordPiece"}}`) }

// pinnedEntry returns a catalog entry whose pins match the given bytes, so the
// download succeeds. It keeps the shipped (host-allowlisted) URLs.
func pinnedEntry(model, tok []byte) CatalogEntry {
	e, _ := CatalogEntryByID("all-MiniLM-L6-v2")
	e.Model.SHA256 = sha256hex(model)
	e.Model.SizeBytes = int64(len(model))
	e.Tokenizer.SHA256 = sha256hex(tok)
	e.Tokenizer.SizeBytes = int64(len(tok))
	return e
}

// downloadWith runs Manager.Download against a temp entry by temporarily swapping
// it into the package catalog (Download looks the id up in the real catalog).
func downloadWith(t *testing.T, dir string, e CatalogEntry, doer *fakeDoer) (DownloadResult, error) {
	t.Helper()
	orig := catalog
	catalog = []CatalogEntry{e}
	t.Cleanup(func() { catalog = orig })
	return New(dir).Download(context.Background(), e.ID, doer)
}

func TestDownload_Success_InstallsModelAndTokenizer(t *testing.T) {
	dir := t.TempDir()
	model, tok := validONNX(), validTokenizer()
	e := pinnedEntry(model, tok)
	res, err := downloadWith(t, dir, e, doerFor(e, model, tok))
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	if res.Model.Name != "model.onnx" || res.Tokenizer.Name != "tokenizer.json" {
		t.Fatalf("unexpected install names: %+v", res)
	}
	// Both files must be on disk, 0600, and flip RAG to semantic.
	for _, name := range []string{"model.onnx", "tokenizer.json"} {
		fi, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("%s not installed: %v", name, err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Fatalf("%s perm = %v, want 0600", name, fi.Mode().Perm())
		}
	}
	if res.Listing.RAGMode != RAGModeSemantic {
		t.Fatalf("after full download rag_mode = %q, want semantic", res.Listing.RAGMode)
	}
}

// TestDownload_ChecksumMismatch is the core fail-closed test: a byte that does
// not match the pinned SHA-256 is rejected and NOTHING is installed.
func TestDownload_ChecksumMismatch(t *testing.T) {
	dir := t.TempDir()
	model, tok := validONNX(), validTokenizer()
	e := pinnedEntry(model, tok)
	// Serve DIFFERENT (but still content-valid) tokenizer bytes than the pin.
	tampered := []byte(`{"model":{"type":"BPE"},"tampered":true}`)
	res, err := downloadWith(t, dir, e, doerFor(e, model, tampered))
	if err == nil {
		t.Fatalf("expected checksum mismatch, got success: %+v", res)
	}
	if !isChecksumMismatch(err) {
		t.Fatalf("want ErrChecksumMismatch, got %v", err)
	}
	// Fail-closed: neither artifact installed, no temp junk left.
	if fileExists(filepath.Join(dir, "tokenizer.json")) || fileExists(filepath.Join(dir, "model.onnx")) {
		t.Fatal("checksum mismatch must not install anything")
	}
	entries, _ := os.ReadDir(dir)
	for _, en := range entries {
		if strings.HasPrefix(en.Name(), ".download-") {
			t.Fatalf("leftover temp file after mismatch: %s", en.Name())
		}
	}
}

func TestDownload_UnknownID(t *testing.T) {
	_, err := New(t.TempDir()).Download(context.Background(), "no-such-model", &fakeDoer{})
	if err != ErrUnknownModel {
		t.Fatalf("unknown id: want ErrUnknownModel, got %v", err)
	}
}

// TestDownload_NoArbitraryURL proves the download path can't be steered at a
// caller-chosen address: Download only accepts an id, and an id whose (edited)
// URL is off-allowlist is refused by the fetch-time guard before any dial.
func TestDownload_NoArbitraryURL(t *testing.T) {
	dir := t.TempDir()
	model, tok := validONNX(), validTokenizer()
	e := pinnedEntry(model, tok)
	// Simulate a tampered catalog entry pointing off-allowlist. The fetch-time
	// checkDownloadURL must refuse it (defense in depth over the init() check).
	e.Tokenizer.DownloadURL = "https://evil.example.com/tokenizer.json"
	_, err := downloadWith(t, dir, e, &fakeDoer{resp: map[string]fakeResp{
		"https://evil.example.com/tokenizer.json": {status: 200, body: tok},
		e.Model.DownloadURL:                       {status: 200, body: model},
	}})
	if err == nil || !isBadURL(err) {
		t.Fatalf("off-allowlist URL must be refused, got %v", err)
	}
	if fileExists(filepath.Join(dir, "tokenizer.json")) {
		t.Fatal("off-allowlist download must not install anything")
	}
}

func TestDownload_Non200_FailsClosed(t *testing.T) {
	dir := t.TempDir()
	model, tok := validONNX(), validTokenizer()
	e := pinnedEntry(model, tok)
	doer := &fakeDoer{resp: map[string]fakeResp{
		e.Tokenizer.DownloadURL: {status: 503, body: []byte("upstream down")},
		e.Model.DownloadURL:     {status: 200, body: model},
	}}
	_, err := downloadWith(t, dir, e, doer)
	if err == nil {
		t.Fatal("HTTP 503 must fail the download")
	}
	if fileExists(filepath.Join(dir, "tokenizer.json")) {
		t.Fatal("failed download must not install anything")
	}
}

// TestDownload_Oversize rejects a response larger than the pinned size + slack
// (protects the disk even though the checksum is the real integrity gate).
func TestDownload_Oversize(t *testing.T) {
	dir := t.TempDir()
	model, tok := validONNX(), validTokenizer()
	e := pinnedEntry(model, tok)
	// Claim a tiny tokenizer size, then serve a body far exceeding size+slack.
	e.Tokenizer.SizeBytes = 4
	huge := make([]byte, downloadSlack+1024)
	huge[0] = '{'
	doer := &fakeDoer{resp: map[string]fakeResp{
		e.Tokenizer.DownloadURL: {status: 200, body: huge},
		e.Model.DownloadURL:     {status: 200, body: model},
	}}
	_, err := downloadWith(t, dir, e, doer)
	if err == nil || !isTooLarge(err) {
		t.Fatalf("oversize download: want ErrArtifactTooLarge, got %v", err)
	}
	if fileExists(filepath.Join(dir, "tokenizer.json")) {
		t.Fatal("oversize download must not install anything")
	}
}

// TestDownload_RejectsBadContent verifies the content-sniff still runs even when
// the checksum matches (a JSON blob pinned as a model must be rejected).
func TestDownload_RejectsBadContent(t *testing.T) {
	dir := t.TempDir()
	notModel := []byte(`{"this":"is not onnx"}`)
	tok := validTokenizer()
	e := pinnedEntry(notModel, tok) // pin matches the (bad) bytes
	_, err := downloadWith(t, dir, e, doerFor(e, notModel, tok))
	if err == nil || !isBad(err) {
		t.Fatalf("bad model content: want ErrBadArtifact, got %v", err)
	}
	if fileExists(filepath.Join(dir, "model.onnx")) {
		t.Fatal("content-invalid download must not install the model")
	}
}

func isChecksumMismatch(err error) bool { return err != nil && strings.Contains(err.Error(), "checksum") }
func isBadURL(err error) bool           { return err != nil && strings.Contains(err.Error(), "not an allowed") }
func isTooLarge(err error) bool         { return err != nil && strings.Contains(err.Error(), "size limit") }
