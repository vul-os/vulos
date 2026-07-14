package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// legalDocHandler backs Settings → About. It must serve the notices/offer as
// inline text when present, and 404 honestly when they are not — never leak a
// path outside the configured dirs.
func TestLegalDocHandler(t *testing.T) {
	dir := t.TempDir()
	const body = "# Third-Party Notices\n\n### leaflet 1.9.4\n- Licence: BSD-2-Clause\n"
	if err := os.WriteFile(filepath.Join(dir, "THIRD_PARTY_NOTICES.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	h := legalDocHandler([]string{dir}, "THIRD_PARTY_NOTICES.md")

	t.Run("serves the notices inline as text", func(t *testing.T) {
		rr := httptest.NewRecorder()
		h(rr, httptest.NewRequest(http.MethodGet, "/api/system/licenses", nil))
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rr.Code)
		}
		if ct := rr.Header().Get("Content-Type"); ct != "text/plain; charset=utf-8" {
			t.Errorf("Content-Type = %q, want text/plain; charset=utf-8", ct)
		}
		if rr.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("missing X-Content-Type-Options: nosniff")
		}
		got, _ := io.ReadAll(rr.Body)
		if string(got) != body {
			t.Errorf("body mismatch:\n got %q\nwant %q", got, body)
		}
	})

	t.Run("404 when the document is absent", func(t *testing.T) {
		h404 := legalDocHandler([]string{dir}, "WRITTEN-OFFER.md")
		rr := httptest.NewRecorder()
		h404(rr, httptest.NewRequest(http.MethodGet, "/api/system/written-offer", nil))
		if rr.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", rr.Code)
		}
	})

	t.Run("falls through dirs in order to the first hit", func(t *testing.T) {
		empty := t.TempDir()
		h := legalDocHandler([]string{empty, dir}, "THIRD_PARTY_NOTICES.md")
		rr := httptest.NewRecorder()
		h(rr, httptest.NewRequest(http.MethodGet, "/api/system/licenses", nil))
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (should find it in the second dir)", rr.Code)
		}
	})
}
