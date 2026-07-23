package web

// serve_csp_test.go — M1 regression: the /console SPA (which hosts the ADMIN
// section) is served with the SAME strict, nonce-based CSP as the apex SPA — NO
// script-src 'unsafe-inline', object-src 'none' — and the fallback index.html has
// the per-request nonce stamped on its inline scripts. Previously /console used a
// weaker legacy policy (script-src 'self' 'unsafe-inline', no object-src).

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vul-os/vulos-management/pkg/cproutes"
)

// consoleServed reports whether a built console SPA is embedded. When only the
// placeholder index is embedded the handler still applies the CSP, so these
// tests run regardless — but skip cleanly if the embed is entirely absent.
func mountConsole(t *testing.T) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	Register(mux)
	return mux
}

func TestConsoleCSP_IsStrictNonceBased(t *testing.T) {
	mux := mountConsole(t)

	// The SPA fallback (a deep link that is not a real asset) returns index.html
	// with the strict CSP.
	req := httptest.NewRequest(http.MethodGet, MountPrefix+"admin", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code == http.StatusNotFound {
		t.Skip("no built console SPA embedded (web/dist missing) — CSP handler not exercised")
	}

	csp := rr.Header().Get("Content-Security-Policy")
	if csp == "" {
		t.Fatal("console fallback served without a Content-Security-Policy header")
	}

	// script-src must be nonce-based with NO 'unsafe-inline' (the M1 fix).
	var scriptSrc string
	for _, d := range strings.Split(csp, ";") {
		if d = strings.TrimSpace(d); strings.HasPrefix(d, "script-src") {
			scriptSrc = d
		}
	}
	if scriptSrc == "" {
		t.Fatalf("no script-src in console CSP: %q", csp)
	}
	if strings.Contains(scriptSrc, "'unsafe-inline'") {
		t.Errorf("console script-src still has 'unsafe-inline' (weak legacy policy): %q", scriptSrc)
	}
	if !strings.Contains(scriptSrc, "'nonce-") {
		t.Errorf("console script-src is not nonce-based: %q", scriptSrc)
	}
	if !strings.Contains(csp, "object-src 'none'") {
		t.Errorf("console CSP missing object-src 'none': %q", csp)
	}
	if !strings.Contains(csp, "frame-ancestors 'none'") {
		t.Errorf("console CSP should not be framable (admin surface): %q", csp)
	}

	// The console must serve the SAME policy shape as the apex SPA (extract the
	// nonce from the header and rebuild — they must match directive-for-directive).
	nonce := extractNonce(scriptSrc)
	if nonce == "" {
		t.Fatalf("could not extract nonce from %q", scriptSrc)
	}
	if csp != cproutes.SPAStrictCSP(nonce) {
		t.Errorf("console CSP differs from apex SPAStrictCSP:\n got:  %q\n want: %q", csp, cproutes.SPAStrictCSP(nonce))
	}

	// The stamped index must carry the nonce on its inline theme-bootstrap script.
	body := rr.Body.String()
	if !strings.Contains(body, `nonce="`+nonce+`"`) {
		t.Errorf("console index.html did not stamp the CSP nonce onto its scripts; body head:\n%s", head(body))
	}
}

func TestConsoleCSP_NonceUniquePerRequest(t *testing.T) {
	mux := mountConsole(t)
	get := func() string {
		req := httptest.NewRequest(http.MethodGet, MountPrefix+"dashboard", nil)
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		if rr.Code == http.StatusNotFound {
			t.Skip("no built console SPA embedded")
		}
		return rr.Header().Get("Content-Security-Policy")
	}
	a, b := get(), get()
	if a == b {
		t.Errorf("console CSP nonce is not per-request (identical across two requests): %q", a)
	}
}

func extractNonce(scriptSrc string) string {
	const marker = "'nonce-"
	i := strings.Index(scriptSrc, marker)
	if i < 0 {
		return ""
	}
	rest := scriptSrc[i+len(marker):]
	j := strings.Index(rest, "'")
	if j < 0 {
		return ""
	}
	return rest[:j]
}

func head(s string) string {
	if len(s) > 400 {
		return s[:400]
	}
	return s
}
