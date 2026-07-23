// spa_csp_test.go — M2 regression: the SPA CSP is nonce-based with NO script-src
// 'unsafe-inline', object-src 'none', and the inline theme-bootstrap script is
// stamped with the per-request nonce at serve time.
package cproutes

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSPAStrictCSP_DropsUnsafeInlineScript(t *testing.T) {
	nonce := "TESTNONCE123"
	csp := SPAStrictCSP(nonce)

	var scriptSrc string
	for _, d := range strings.Split(csp, ";") {
		d = strings.TrimSpace(d)
		if strings.HasPrefix(d, "script-src") {
			scriptSrc = d
		}
	}
	if scriptSrc == "" {
		t.Fatalf("no script-src directive in CSP: %q", csp)
	}
	if strings.Contains(scriptSrc, "'unsafe-inline'") {
		t.Errorf("script-src still contains 'unsafe-inline': %q", scriptSrc)
	}
	if !strings.Contains(scriptSrc, "'nonce-"+nonce+"'") {
		t.Errorf("script-src missing the per-request nonce: %q", scriptSrc)
	}
	if !strings.Contains(csp, "object-src 'none'") {
		t.Errorf("CSP missing object-src 'none': %q", csp)
	}
}

// TestSelfContainedPage_IsFramable is the regression guard for the product
// mini-site render bug: landing.html files are embedded in a SAME-ORIGIN iframe,
// so serving them with the SPA shell's frame-ancestors 'none' + X-Frame-Options:
// DENY made the browser refuse to render them (blank product page). A static
// *.html sub-page must be served framable-by-self + inline-friendly, while the
// SPA shell and hashed assets stay strict.
func TestSelfContainedPage_IsFramable(t *testing.T) {
	dir := buildSPADir(t)
	// A product mini-site landing (self-contained, with an inline <script>).
	landing := filepath.Join(dir, "products", "wede")
	if err := os.MkdirAll(landing, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(landing, "landing.html"),
		[]byte("<!doctype html><title>wede</title><script>1</script>"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CP_STATIC_DIR", dir)

	mux := http.NewServeMux()
	// Emulate the middleware default that the handler must override.
	base := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Frame-Options", "DENY")
		mux.ServeHTTP(w, r)
	})
	RegisterSPAFallback(mux)
	srv := httptest.NewServer(base)
	defer srv.Close()

	head := func(path string) http.Header {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		resp.Body.Close()
		return resp.Header
	}

	// The mini-site landing must be framable by its own origin + allow its inline
	// script; X-Frame-Options must NOT be DENY.
	h := head("/products/wede/landing.html")
	csp := h.Get("Content-Security-Policy")
	if !strings.Contains(csp, "frame-ancestors 'self'") {
		t.Errorf("landing CSP not framable-by-self: %q", csp)
	}
	if strings.Contains(csp, "frame-ancestors 'none'") {
		t.Errorf("landing CSP still frame-ancestors 'none' (iframe would be blocked): %q", csp)
	}
	if !strings.Contains(csp, "script-src 'self' 'unsafe-inline'") {
		t.Errorf("landing CSP does not admit its inline script: %q", csp)
	}
	if xfo := h.Get("X-Frame-Options"); strings.EqualFold(xfo, "DENY") {
		t.Errorf("landing still has X-Frame-Options: DENY — iframe blocked")
	}

	// The SPA shell (index.html fallback) must STAY strict — not framable.
	if csp := head("/products").Get("Content-Security-Policy"); !strings.Contains(csp, "frame-ancestors 'none'") {
		t.Errorf("SPA shell should remain frame-ancestors 'none', got: %q", csp)
	}
}

func TestSPANonce_UniquePerCall(t *testing.T) {
	a, b := SPANonce(), SPANonce()
	if a == "" || b == "" {
		t.Fatal("SPANonce returned empty")
	}
	if a == b {
		t.Errorf("SPANonce not unique across calls: %q == %q", a, b)
	}
}

func TestWriteSPAIndexHTML_StampsNonceOnScripts(t *testing.T) {
	dir := t.TempDir()
	idx := filepath.Join(dir, "index.html")
	const html = `<!doctype html><html><head>` +
		`<script>console.log('inline theme bootstrap')</script>` +
		`<script type="module" crossorigin src="/assets/app.js"></script>` +
		`<link rel="modulepreload" href="/assets/x.js"></head><body></body></html>`
	if err := os.WriteFile(idx, []byte(html), 0o600); err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	if err := WriteSPAIndexHTML(rr, idx, "N0NCE"); err != nil {
		t.Fatalf("WriteSPAIndexHTML: %v", err)
	}
	out := rr.Body.String()

	if strings.Count(out, `nonce="N0NCE"`) != 2 {
		t.Errorf("expected 2 nonced <script> tags, body:\n%s", out)
	}
	if !strings.Contains(out, `<script nonce="N0NCE">console.log`) {
		t.Errorf("inline theme script not nonced:\n%s", out)
	}
	if !strings.Contains(out, `<script nonce="N0NCE" type="module"`) {
		t.Errorf("module entry script not nonced:\n%s", out)
	}
	if strings.Contains(out, `modulepreload" nonce`) {
		t.Errorf("modulepreload link should not be nonced:\n%s", out)
	}
}
