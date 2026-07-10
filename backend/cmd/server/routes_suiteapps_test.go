package main

// BUNDLE-01 tests: the default-everything (batteries-included, opt-out) suite
// selection endpoints. Covers the fail-open default (no file ⇒ everything on),
// round-trip persistence of opt-outs, partial-body normalisation, and corrupt-
// file fail-open.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func suiteReq(t *testing.T, mux *http.ServeMux, method, body string) suiteSelection {
	t.Helper()
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, "/api/setup/apps", strings.NewReader(body))
	} else {
		r = httptest.NewRequest(method, "/api/setup/apps", nil)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("%s /api/setup/apps: status %d, body %s", method, w.Code, w.Body.String())
	}
	var sel suiteSelection
	if err := json.Unmarshal(w.Body.Bytes(), &sel); err != nil {
		t.Fatalf("decode response: %v (body %s)", err, w.Body.String())
	}
	return sel
}

func TestSuiteApps_DefaultEverythingWhenAbsent(t *testing.T) {
	home := t.TempDir()
	mux := http.NewServeMux()
	registerSuiteAppsRoutes(mux, home)

	sel := suiteReq(t, mux, http.MethodGet, "")
	if !sel.Email || !sel.Workspace {
		t.Fatalf("no selection file ⇒ everything on; got %+v", sel)
	}
	if sel.Chosen {
		t.Fatalf("default must report Chosen=false; got %+v", sel)
	}
}

func TestSuiteApps_RoundTripOptOut(t *testing.T) {
	home := t.TempDir()
	mux := http.NewServeMux()
	registerSuiteAppsRoutes(mux, home)

	// A gamer opts out of Workspace but keeps the email/Mail.
	got := suiteReq(t, mux, http.MethodPost, `{"email":true,"workspace":false}`)
	if !got.Email || got.Workspace || !got.Chosen {
		t.Fatalf("POST opt-out: want email=true workspace=false chosen=true; got %+v", got)
	}

	// GET now reflects the persisted opt-out.
	after := suiteReq(t, mux, http.MethodGet, "")
	if !after.Email || after.Workspace || !after.Chosen {
		t.Fatalf("GET after opt-out: want email=true workspace=false chosen=true; got %+v", after)
	}

	// File actually exists on disk.
	if _, err := os.Stat(filepath.Join(home, ".vulos", "db", "suite-selection.json")); err != nil {
		t.Fatalf("selection file not persisted: %v", err)
	}
}

func TestSuiteApps_DeclineEmailDropsMail(t *testing.T) {
	home := t.TempDir()
	mux := http.NewServeMux()
	registerSuiteAppsRoutes(mux, home)

	got := suiteReq(t, mux, http.MethodPost, `{"email":false,"workspace":true}`)
	if got.Email || !got.Workspace || !got.Chosen {
		t.Fatalf("decline email: want email=false workspace=true chosen=true; got %+v", got)
	}
}

func TestSuiteApps_PartialBodyDefaultsOn(t *testing.T) {
	home := t.TempDir()
	mux := http.NewServeMux()
	registerSuiteAppsRoutes(mux, home)

	// Body omits workspace — it must default ON, never silently off.
	got := suiteReq(t, mux, http.MethodPost, `{"email":false}`)
	if got.Email {
		t.Fatalf("email omitted from opt-out should be false; got %+v", got)
	}
	if !got.Workspace {
		t.Fatalf("omitted workspace must default ON (batteries-included); got %+v", got)
	}
}

func TestSuiteApps_CorruptFileFailsOpen(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".vulos", "db")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "suite-selection.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	registerSuiteAppsRoutes(mux, home)

	sel := suiteReq(t, mux, http.MethodGet, "")
	if !sel.Email || !sel.Workspace {
		t.Fatalf("corrupt file must fail open to everything-on; got %+v", sel)
	}
}
