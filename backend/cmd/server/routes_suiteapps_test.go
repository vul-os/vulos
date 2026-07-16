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

	"vulos/backend/services/auth"
)

// suiteEmptyStore is a fresh auth store with NO users: first-boot onboarding,
// where the suite-selection write is legitimately unauthenticated.
func suiteEmptyStore(t *testing.T) *auth.Store {
	t.Helper()
	store, err := auth.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return store
}

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
	registerSuiteAppsRoutes(mux, suiteEmptyStore(t), home)

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
	registerSuiteAppsRoutes(mux, suiteEmptyStore(t), home)

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
	registerSuiteAppsRoutes(mux, suiteEmptyStore(t), home)

	got := suiteReq(t, mux, http.MethodPost, `{"email":false,"workspace":true}`)
	if got.Email || !got.Workspace || !got.Chosen {
		t.Fatalf("decline email: want email=false workspace=true chosen=true; got %+v", got)
	}
}

func TestSuiteApps_PartialBodyDefaultsOn(t *testing.T) {
	home := t.TempDir()
	mux := http.NewServeMux()
	registerSuiteAppsRoutes(mux, suiteEmptyStore(t), home)

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
	registerSuiteAppsRoutes(mux, suiteEmptyStore(t), home)

	sel := suiteReq(t, mux, http.MethodGet, "")
	if !sel.Email || !sel.Workspace {
		t.Fatalf("corrupt file must fail open to everything-on; got %+v", sel)
	}
}

// TestSuiteApps_POSTGatedOncePersonalized pins the write gate: once the box has
// an account, an ANONYMOUS caller can no longer permanently strip Mail + the
// productivity bundle from the launcher, and a non-admin user cannot either. Only an
// authenticated admin can rewrite the selection. Reads stay public.
func TestSuiteApps_POSTGatedOncePersonalized(t *testing.T) {
	home := t.TempDir()
	store, err := auth.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	admin, err := store.Register("admin", "correct horse battery", "Admin")
	if err != nil {
		t.Fatalf("Register admin: %v", err)
	}
	member, err := store.Register("member", "correct horse battery", "Member")
	if err != nil {
		t.Fatalf("Register member: %v", err)
	}

	mux := http.NewServeMux()
	registerSuiteAppsRoutes(mux, store, home)

	post := func(userID string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/api/setup/apps", strings.NewReader(`{"email":false,"workspace":false}`))
		if userID != "" {
			r.Header.Set("X-User-ID", userID)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}

	if w := post(""); w.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous POST after setup: want 401, got %d (body %s)", w.Code, w.Body.String())
	}
	if w := post(member.ID); w.Code != http.StatusForbidden {
		t.Fatalf("non-admin POST: want 403, got %d (body %s)", w.Code, w.Body.String())
	}

	// The refused writes must not have touched disk — the launcher still shows everything.
	if _, err := os.Stat(filepath.Join(home, ".vulos", "db", "suite-selection.json")); !os.IsNotExist(err) {
		t.Fatalf("a refused POST persisted a selection file (err=%v)", err)
	}

	// GET stays public — the launcher reads it on every boot.
	rg := httptest.NewRequest(http.MethodGet, "/api/setup/apps", nil)
	wg := httptest.NewRecorder()
	mux.ServeHTTP(wg, rg)
	if wg.Code != http.StatusOK {
		t.Fatalf("GET /api/setup/apps must stay public: got %d", wg.Code)
	}

	// An admin can still opt out.
	if w := post(admin.ID); w.Code != http.StatusOK {
		t.Fatalf("admin POST: want 200, got %d (body %s)", w.Code, w.Body.String())
	}
	var sel suiteSelection
	data, err := os.ReadFile(filepath.Join(home, ".vulos", "db", "suite-selection.json"))
	if err != nil {
		t.Fatalf("admin POST did not persist: %v", err)
	}
	if err := json.Unmarshal(data, &sel); err != nil {
		t.Fatalf("decode persisted selection: %v", err)
	}
	if sel.Email || sel.Workspace || !sel.Chosen {
		t.Fatalf("admin opt-out not persisted verbatim: %+v", sel)
	}
}
