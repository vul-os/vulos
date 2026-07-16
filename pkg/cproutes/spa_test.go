package cproutes

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// buildSPADir writes a minimal built-SPA layout (index.html + a hashed asset)
// and returns the directory.
func buildSPADir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<!doctype html><title>Vulos SPA</title>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "assets", "app-abc123.js"), []byte("console.log('spa')"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestSPAFallback_ServesSPAAndDoesNotShadowAPI(t *testing.T) {
	dir := buildSPADir(t)
	t.Setenv("CP_STATIC_DIR", dir)

	mux := http.NewServeMux()
	// A concrete API route + health route, exactly as main.go registers them.
	mux.HandleFunc("GET /api/billing/rates", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	RegisterSPAFallback(mux)

	srv := httptest.NewServer(mux)
	defer srv.Close()

	get := func(path string) (int, string) {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		defer resp.Body.Close()
		buf := make([]byte, 4096)
		n, _ := resp.Body.Read(buf)
		return resp.StatusCode, string(buf[:n])
	}

	// Apex + deep SPA routes fall back to index.html (client-side routing).
	for _, p := range []string{"/", "/products/office", "/pricing", "/console", "/some/unknown/deep/link"} {
		code, body := get(p)
		if code != http.StatusOK {
			t.Errorf("GET %s: status %d, want 200", p, code)
		}
		if !strings.Contains(body, "Vulos SPA") {
			t.Errorf("GET %s: body did not serve index.html (%q)", p, body)
		}
	}

	// A real hashed asset is served as itself, not index.html.
	if code, body := get("/assets/app-abc123.js"); code != http.StatusOK || !strings.Contains(body, "console.log") {
		t.Errorf("GET /assets/app-abc123.js: code=%d body=%q — asset not served", code, body)
	}

	// The API route is NOT shadowed by the SPA fallback.
	if code, body := get("/api/billing/rates"); code != http.StatusOK || !strings.Contains(body, `"ok":true`) {
		t.Errorf("GET /api/billing/rates: code=%d body=%q — API shadowed by SPA", code, body)
	}
	// An UNKNOWN /api/* path must 404 (JSON-less NotFound), never index.html.
	if code, body := get("/api/does/not/exist"); code != http.StatusNotFound {
		t.Errorf("GET /api/does/not/exist: code=%d body=%q — want 404, SPA must not answer API paths", code, body)
	}
	// Health route still works.
	if code, _ := get("/healthz"); code != http.StatusOK {
		t.Errorf("GET /healthz: code=%d, want 200", code)
	}
}

func TestSPAFallback_DisabledWhenNoBuild(t *testing.T) {
	// Point CP_STATIC_DIR at an empty dir (no index.html) → handler stays off.
	t.Setenv("CP_STATIC_DIR", t.TempDir())
	// Also neutralise the conventional-location probes so a stray repo dist/ on
	// the build host doesn't accidentally enable the fallback in this test.
	if d := spaStaticDir(); d != "" {
		t.Skipf("a built SPA is present at %s on this host; disabled-path not exercised", d)
	}
	mux := http.NewServeMux()
	RegisterSPAFallback(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("with no SPA bundled, GET / = %d, want 404 (fallback disabled)", resp.StatusCode)
	}
}
