package webhost

import (
	"archive/tar"
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// deployServe deploys a small representative site and returns the serving
// handler plus the site root on disk. index.html is the SPA entry; assets/app.js
// is a real cacheable asset; secret.txt sits OUTSIDE the root as an escape
// canary.
func deployServe(t *testing.T) (http.Handler, string, string) {
	t.Helper()
	root := t.TempDir()
	svc := New(root)

	bundle := makeTarGz(t,
		tarEntry{Name: "index.html", Body: "<!doctype html><title>SPA</title>"},
		tarEntry{Typeflag: tar.TypeDir, Name: "assets"},
		tarEntry{Name: "assets/app.js", Body: "export const x = 1"},
		tarEntry{Name: ".env", Body: "SECRET=hunter2"},
	)
	if err := svc.Deploy("userA", "blog", bytes.NewReader(bundle)); err != nil {
		t.Fatalf("Deploy: %v", err)
	}

	// Escape canary next to (above) the served htdocs.
	htdocs := filepath.Join(root, "userA", "blog", "htdocs")
	secret := filepath.Join(root, "userA", "blog", "meta-adjacent-secret.txt")
	if err := os.WriteFile(secret, []byte("OUTSIDE-SECRET"), 0o600); err != nil {
		t.Fatal(err)
	}
	return svc.ServeSite("userA", "blog"), htdocs, secret
}

// do runs one request against the handler with an explicit URL path (set
// directly so traversal sequences are not pre-cleaned by request parsing).
func do(t *testing.T, h http.Handler, rawPath string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "http://blog.userA.os.vulos.org/", nil)
	req.URL.Path = rawPath
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// TestServe_AssetHasPublicCacheHeader: a real asset serves 200 with the
// public, max-age=300 header so the PUBWEB-03 nginx edge cache stores it.
func TestServe_AssetHasPublicCacheHeader(t *testing.T) {
	h, _, _ := deployServe(t)
	rec := do(t, h, "/assets/app.js")
	if rec.Code != http.StatusOK {
		t.Fatalf("asset status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); got != "public, max-age=300" {
		t.Errorf("asset Cache-Control = %q, want %q", got, "public, max-age=300")
	}
	if !strings.Contains(rec.Body.String(), "export const x") {
		t.Errorf("asset body not served: %q", rec.Body.String())
	}
}

// TestServe_DeepLinkFallsBackToIndex: an SPA deep link (/about) that is not a
// real file serves index.html with no-cache.
func TestServe_DeepLinkFallsBackToIndex(t *testing.T) {
	h, _, _ := deployServe(t)
	rec := do(t, h, "/about")
	if rec.Code != http.StatusOK {
		t.Fatalf("deep link status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
		t.Errorf("index Cache-Control = %q, want no-cache", got)
	}
	if !strings.Contains(rec.Body.String(), "<title>SPA</title>") {
		t.Errorf("deep link did not serve index.html: %q", rec.Body.String())
	}
}

// TestServe_RootServesIndexNoCache: "/" serves the SPA entry with no-cache.
func TestServe_RootServesIndexNoCache(t *testing.T) {
	h, _, _ := deployServe(t)
	rec := do(t, h, "/")
	if rec.Code != http.StatusOK {
		t.Fatalf("root status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
		t.Errorf("root Cache-Control = %q, want no-cache", got)
	}
}

// TestServe_RefusesTraversal: a "../"-laden path must never serve a file from
// outside the site root; it collapses to the SPA fallback and never returns the
// outside canary's contents.
func TestServe_RefusesTraversal(t *testing.T) {
	h, _, secret := deployServe(t)
	secretBody, _ := os.ReadFile(secret)

	for _, p := range []string{
		"/../meta-adjacent-secret.txt",
		"/../../meta-adjacent-secret.txt",
		"/assets/../../meta-adjacent-secret.txt",
		"/%2e%2e/meta-adjacent-secret.txt", // literal (never URL-decoded by handler)
	} {
		rec := do(t, h, p)
		if strings.Contains(rec.Body.String(), string(secretBody)) {
			t.Errorf("traversal %q leaked outside file: %q", p, rec.Body.String())
		}
		// Must resolve to SPA fallback or 404 — never a 200 of the secret.
		if rec.Code == http.StatusOK && !strings.Contains(rec.Body.String(), "<title>SPA</title>") {
			t.Errorf("traversal %q returned unexpected 200 body: %q", p, rec.Body.String())
		}
	}
}

// TestServe_RefusesDotfiles: any dotfile segment is 404'd, even though .env was
// present in the deployed bundle. This also keeps sibling metadata unreachable.
func TestServe_RefusesDotfiles(t *testing.T) {
	h, _, _ := deployServe(t)
	for _, p := range []string{"/.env", "/.git/config", "/assets/../.env", "/.ssh/id_rsa"} {
		rec := do(t, h, p)
		if rec.Code != http.StatusNotFound {
			t.Errorf("dotfile %q status = %d, want 404 (body=%q)", p, rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), "hunter2") {
			t.Errorf("dotfile %q leaked .env contents", p)
		}
	}
}

// TestServe_NoDirectoryListing: a request for a directory ("/assets") does NOT
// return an index of files; it serves the SPA fallback instead.
func TestServe_NoDirectoryListing(t *testing.T) {
	h, _, _ := deployServe(t)
	rec := do(t, h, "/assets")
	if rec.Code != http.StatusOK {
		t.Fatalf("dir request status = %d, want 200 (SPA fallback)", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "app.js") {
		t.Errorf("directory listing leaked file names: %q", body)
	}
	if !strings.Contains(body, "<title>SPA</title>") {
		t.Errorf("dir request did not fall back to index.html: %q", body)
	}
}

// TestServe_MissingSite404s: the handler for a site that was never deployed
// 404s every request rather than erroring.
func TestServe_MissingSite404s(t *testing.T) {
	svc := New(t.TempDir())
	h := svc.ServeSite("ghost", "nope")
	rec := do(t, h, "/anything")
	if rec.Code != http.StatusNotFound {
		t.Errorf("missing site status = %d, want 404", rec.Code)
	}
}

// TestServe_RejectsNonGET: only GET/HEAD are served; a POST is 405.
func TestServe_RejectsNonGET(t *testing.T) {
	h, _, _ := deployServe(t)
	req := httptest.NewRequest(http.MethodPost, "http://blog.userA.os.vulos.org/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST status = %d, want 405", rec.Code)
	}
}
