// spa_csp_test.go — M2 regression: the SPA CSP is nonce-based with NO script-src
// 'unsafe-inline', object-src 'none', and the inline theme-bootstrap script is
// stamped with the per-request nonce at serve time.
package cproutes

import (
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
