package osdist

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ─── helpers ─────────────────────────────────────────────────────────────────

// writeURLFile writes url to a temp file and returns its path.
func writeURLFile(t *testing.T, url string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "os-bucket-url")
	if err := os.WriteFile(path, []byte(url), 0o644); err != nil {
		t.Fatalf("writeURLFile: %v", err)
	}
	return path
}

// newOKServer returns an httptest.Server that responds 200 with body for every
// request, plus a cleanup function.
func newOKServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, body) //nolint:errcheck
	}))
	t.Cleanup(srv.Close)
	return srv
}

// newErrorServer returns an httptest.Server that always responds with the
// given HTTP status code (non-200).
func newErrorServer(t *testing.T, status int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// readBody drains and closes rc, returning the string content.
func readBody(t *testing.T, rc io.ReadCloser) string {
	t.Helper()
	defer rc.Close()
	b, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("readBody: %v", err)
	}
	return string(b)
}

// ─── Resolve: resolution order ───────────────────────────────────────────────

// SEED02_TestResolve_OverrideFirst verifies that an explicit override URL
// appears before the embedded default and mirrors.
func SEED02_TestResolve_OverrideFirst(t *testing.T) {
	embeddedURL := "https://embedded.example.com"
	mirrorURL := "https://mirror.example.com"
	overrideURL := "https://override.example.com"

	path := writeURLFile(t, embeddedURL)
	s := NewSource(
		WithEmbeddedURLPath(path),
		WithOverride(overrideURL),
		WithMirrors(mirrorURL),
	)

	got := s.Resolve()
	if len(got) != 3 {
		t.Fatalf("Resolve() returned %d candidates, want 3: %v", len(got), got)
	}
	if got[0] != overrideURL {
		t.Errorf("got[0] = %q, want override %q", got[0], overrideURL)
	}
	if got[1] != embeddedURL {
		t.Errorf("got[1] = %q, want embedded %q", got[1], embeddedURL)
	}
	if got[2] != mirrorURL {
		t.Errorf("got[2] = %q, want mirror %q", got[2], mirrorURL)
	}
}

// SEED02_TestResolve_EmbeddedBeforeMirrors verifies that the embedded default
// URL comes before mirrors when no override is present.
func SEED02_TestResolve_EmbeddedBeforeMirrors(t *testing.T) {
	embeddedURL := "https://embedded.example.com"
	mirror1 := "https://mirror1.example.com"
	mirror2 := "https://mirror2.example.com"

	path := writeURLFile(t, embeddedURL)
	s := NewSource(
		WithEmbeddedURLPath(path),
		WithMirrors(mirror1, mirror2),
	)

	got := s.Resolve()
	if len(got) != 3 {
		t.Fatalf("Resolve() returned %d candidates, want 3: %v", len(got), got)
	}
	if got[0] != embeddedURL {
		t.Errorf("got[0] = %q, want embedded %q", got[0], embeddedURL)
	}
	if got[1] != mirror1 {
		t.Errorf("got[1] = %q, want mirror1 %q", got[1], mirror1)
	}
	if got[2] != mirror2 {
		t.Errorf("got[2] = %q, want mirror2 %q", got[2], mirror2)
	}
}

// SEED02_TestResolve_OnlyOverride verifies that when only an override is set
// (no embedded file, no mirrors) Resolve returns exactly that one URL.
func SEED02_TestResolve_OnlyOverride(t *testing.T) {
	overrideURL := "https://override.example.com"
	// Point embedded path at a non-existent file.
	s := NewSource(
		WithEmbeddedURLPath("/nonexistent/os-bucket-url"),
		WithOverride(overrideURL),
	)

	got := s.Resolve()
	if len(got) != 1 || got[0] != overrideURL {
		t.Errorf("Resolve() = %v, want [%q]", got, overrideURL)
	}
}

// SEED02_TestResolve_MissingEmbeddedFile verifies that a missing embedded-URL
// file is silently skipped (only mirrors are returned).
func SEED02_TestResolve_MissingEmbeddedFile(t *testing.T) {
	mirrorURL := "https://mirror.example.com"
	s := NewSource(
		WithEmbeddedURLPath("/nonexistent/os-bucket-url"),
		WithMirrors(mirrorURL),
	)

	got := s.Resolve()
	if len(got) != 1 || got[0] != mirrorURL {
		t.Errorf("Resolve() = %v, want [%q]", got, mirrorURL)
	}
}

// SEED02_TestResolve_DeduplicatesURLs verifies that duplicate URLs appear only
// once in the candidate list (first occurrence wins).
func SEED02_TestResolve_DeduplicatesURLs(t *testing.T) {
	url := "https://same.example.com"
	path := writeURLFile(t, url)
	s := NewSource(
		WithEmbeddedURLPath(path),
		WithOverride(url), // same as embedded — should be deduplicated
		WithMirrors(url),  // also same — also deduped
	)

	got := s.Resolve()
	if len(got) != 1 {
		t.Errorf("Resolve() = %v (len %d), want exactly one deduplicated URL", got, len(got))
	}
}

// SEED02_TestResolve_TrailingSlashNormalised verifies that trailing slashes in
// URLs are stripped so Fetch can always join with "/" + relPath.
func SEED02_TestResolve_TrailingSlashNormalised(t *testing.T) {
	s := NewSource(
		WithEmbeddedURLPath("/nonexistent/os-bucket-url"),
		WithOverride("https://override.example.com/"),
	)

	got := s.Resolve()
	if len(got) != 1 {
		t.Fatalf("Resolve() returned %d candidates, want 1", len(got))
	}
	if strings.HasSuffix(got[0], "/") {
		t.Errorf("Resolve() = %q still has trailing slash", got[0])
	}
}

// SEED02_TestResolve_EnvOverride verifies that VULOS_OS_BUCKET_URL is picked
// up automatically at NewSource construction time.
func SEED02_TestResolve_EnvOverride(t *testing.T) {
	envURL := "https://env-override.example.com"
	t.Setenv(envOverrideKey, envURL)

	s := NewSource(
		WithEmbeddedURLPath("/nonexistent/os-bucket-url"),
	)

	got := s.Resolve()
	if len(got) == 0 || got[0] != envURL {
		t.Errorf("Resolve()[0] = %q, want env override %q (got %v)", got[0], envURL, got)
	}
}

// ─── Fetch: happy path ────────────────────────────────────────────────────────

// SEED02_TestFetch_HappyPath verifies that Fetch returns the response body from
// the first (and only) successful candidate.
func SEED02_TestFetch_HappyPath(t *testing.T) {
	const wantBody = "manifest-content"
	srv := newOKServer(t, wantBody)

	s := NewSource(
		WithEmbeddedURLPath("/nonexistent/os-bucket-url"),
		WithOverride(srv.URL),
		withHTTPClient(srv.Client()),
	)

	rc, err := s.Fetch(context.Background(), "os/stable.json")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if got := readBody(t, rc); got != wantBody {
		t.Errorf("body = %q, want %q", got, wantBody)
	}
}

// ─── Fetch: failover ──────────────────────────────────────────────────────────

// SEED02_TestFetch_FailoverOnNetworkError verifies that Fetch advances to the
// next candidate when the first URL produces a network-level error (server
// closed / unreachable).
func SEED02_TestFetch_FailoverOnNetworkError(t *testing.T) {
	const wantBody = "from-mirror"
	goodSrv := newOKServer(t, wantBody)

	// A server that we immediately close so requests to it fail with a
	// connection-refused / EOF error.
	deadSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadSrv.Close() // closed immediately — network error on connect

	// Use a shared client that can reach both httptest servers.
	client := goodSrv.Client()

	s := NewSource(
		WithEmbeddedURLPath("/nonexistent/os-bucket-url"),
		WithOverride(deadSrv.URL), // first: network error
		WithMirrors(goodSrv.URL),  // second: success
		withHTTPClient(client),
	)

	rc, err := s.Fetch(context.Background(), "os/stable.json")
	if err != nil {
		t.Fatalf("Fetch: expected success after failover, got: %v", err)
	}
	if got := readBody(t, rc); got != wantBody {
		t.Errorf("body = %q, want %q", got, wantBody)
	}
}

// SEED02_TestFetch_FailoverOnNon200 verifies that Fetch advances to the next
// candidate when a server returns a non-200 status (e.g. 503).
func SEED02_TestFetch_FailoverOnNon200(t *testing.T) {
	const wantBody = "from-second-mirror"
	badSrv := newErrorServer(t, http.StatusServiceUnavailable)
	goodSrv := newOKServer(t, wantBody)

	// Both httptest servers are local; use goodSrv.Client() (standard http client).
	s := NewSource(
		WithEmbeddedURLPath("/nonexistent/os-bucket-url"),
		WithOverride(badSrv.URL),
		WithMirrors(goodSrv.URL),
		withHTTPClient(goodSrv.Client()),
	)

	rc, err := s.Fetch(context.Background(), "os/stable.json")
	if err != nil {
		t.Fatalf("Fetch: expected success after failover, got: %v", err)
	}
	if got := readBody(t, rc); got != wantBody {
		t.Errorf("body = %q, want %q", got, wantBody)
	}
}

// SEED02_TestFetch_AllMirrorsFail verifies that Fetch returns ErrAllMirrorsFailed
// when every candidate fails.
func SEED02_TestFetch_AllMirrorsFail(t *testing.T) {
	bad1 := newErrorServer(t, http.StatusNotFound)
	bad2 := newErrorServer(t, http.StatusInternalServerError)

	s := NewSource(
		WithEmbeddedURLPath("/nonexistent/os-bucket-url"),
		WithOverride(bad1.URL),
		WithMirrors(bad2.URL),
		withHTTPClient(bad1.Client()),
	)

	_, err := s.Fetch(context.Background(), "os/stable.json")
	if err == nil {
		t.Fatal("Fetch: expected error when all mirrors fail, got nil")
	}
	if !strings.Contains(err.Error(), ErrAllMirrorsFailed.Error()) {
		t.Errorf("Fetch error = %q, want it to contain %q", err.Error(), ErrAllMirrorsFailed.Error())
	}
}

// SEED02_TestFetch_NoURLs verifies that Fetch returns ErrNoMirrors when
// Resolve returns an empty list.
func SEED02_TestFetch_NoURLs(t *testing.T) {
	s := NewSource(
		WithEmbeddedURLPath("/nonexistent/os-bucket-url"),
		// no override, no mirrors
	)

	_, err := s.Fetch(context.Background(), "os/stable.json")
	if err == nil {
		t.Fatal("Fetch: expected ErrNoMirrors, got nil")
	}
	if !strings.Contains(err.Error(), ErrNoMirrors.Error()) {
		t.Errorf("Fetch error = %q, want ErrNoMirrors", err.Error())
	}
}

// SEED02_TestFetch_EmbeddedURLUsed verifies that a URL read from the embedded
// file is actually used for fetching.
func SEED02_TestFetch_EmbeddedURLUsed(t *testing.T) {
	const wantBody = "from-embedded"
	srv := newOKServer(t, wantBody)

	path := writeURLFile(t, srv.URL)
	s := NewSource(
		WithEmbeddedURLPath(path),
		withHTTPClient(srv.Client()),
	)

	rc, err := s.Fetch(context.Background(), "os/stable.json")
	if err != nil {
		t.Fatalf("Fetch via embedded URL: %v", err)
	}
	if got := readBody(t, rc); got != wantBody {
		t.Errorf("body = %q, want %q", got, wantBody)
	}
}

// SEED02_TestFetch_MirrorOrderRespected verifies that when the first mirror
// succeeds the second mirror is never contacted.
func SEED02_TestFetch_MirrorOrderRespected(t *testing.T) {
	const wantBody = "first-mirror-wins"
	firstSrv := newOKServer(t, wantBody)
	secondCalled := false
	secondSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secondCalled = true
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(secondSrv.Close)

	s := NewSource(
		WithEmbeddedURLPath("/nonexistent/os-bucket-url"),
		WithMirrors(firstSrv.URL, secondSrv.URL),
		withHTTPClient(firstSrv.Client()),
	)

	rc, err := s.Fetch(context.Background(), "os/stable.json")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	readBody(t, rc)

	if secondCalled {
		t.Error("second mirror was contacted even though the first succeeded")
	}
}

// ─── Standard test entry points ──────────────────────────────────────────────

func TestResolve_OverrideFirst(t *testing.T)           { SEED02_TestResolve_OverrideFirst(t) }
func TestResolve_EmbeddedBeforeMirrors(t *testing.T)   { SEED02_TestResolve_EmbeddedBeforeMirrors(t) }
func TestResolve_OnlyOverride(t *testing.T)            { SEED02_TestResolve_OnlyOverride(t) }
func TestResolve_MissingEmbeddedFile(t *testing.T)     { SEED02_TestResolve_MissingEmbeddedFile(t) }
func TestResolve_DeduplicatesURLs(t *testing.T)        { SEED02_TestResolve_DeduplicatesURLs(t) }
func TestResolve_TrailingSlashNormalised(t *testing.T) { SEED02_TestResolve_TrailingSlashNormalised(t) }
func TestResolve_EnvOverride(t *testing.T)             { SEED02_TestResolve_EnvOverride(t) }
func TestFetch_HappyPath(t *testing.T)                 { SEED02_TestFetch_HappyPath(t) }
func TestFetch_FailoverOnNetworkError(t *testing.T)    { SEED02_TestFetch_FailoverOnNetworkError(t) }
func TestFetch_FailoverOnNon200(t *testing.T)          { SEED02_TestFetch_FailoverOnNon200(t) }
func TestFetch_AllMirrorsFail(t *testing.T)            { SEED02_TestFetch_AllMirrorsFail(t) }
func TestFetch_NoURLs(t *testing.T)                    { SEED02_TestFetch_NoURLs(t) }
func TestFetch_EmbeddedURLUsed(t *testing.T)           { SEED02_TestFetch_EmbeddedURLUsed(t) }
func TestFetch_MirrorOrderRespected(t *testing.T)      { SEED02_TestFetch_MirrorOrderRespected(t) }
