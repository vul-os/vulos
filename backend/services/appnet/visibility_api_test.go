package appnet

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// newTestVisStore creates a VisibilityStore backed by a temp file.
func newTestVisStore(t *testing.T) *VisibilityStore {
	t.Helper()
	dir := t.TempDir()
	s, err := NewVisibilityStore(filepath.Join(dir, "visibility.json"))
	if err != nil {
		t.Fatalf("NewVisibilityStore: %v", err)
	}
	return s
}

// newTestAppStore creates a minimal AppStore backed by a temp apps directory.
func newTestAppStore(t *testing.T) *AppStore {
	t.Helper()
	dir := t.TempDir()
	return NewAppStore(filepath.Join(dir, "apps"))
}

func TestVisibilityStore_GetDefault(t *testing.T) {
	s := newTestVisStore(t)
	if got := s.Get("myapp"); got != VisibilityPrivate {
		t.Errorf("default visibility = %q, want %q", got, VisibilityPrivate)
	}
}

func TestVisibilityStore_SetAndGet(t *testing.T) {
	s := newTestVisStore(t)
	if err := s.Set("myapp", VisibilityPublic); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if got := s.Get("myapp"); got != VisibilityPublic {
		t.Errorf("Get after Set = %q, want %q", got, VisibilityPublic)
	}
}

func TestVisibilityStore_Delete(t *testing.T) {
	s := newTestVisStore(t)
	s.Set("myapp", VisibilityLocal)
	if err := s.Delete("myapp"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if got := s.Get("myapp"); got != VisibilityPrivate {
		t.Errorf("after Delete Get = %q, want %q", got, VisibilityPrivate)
	}
}

func TestVisibilityStore_All(t *testing.T) {
	s := newTestVisStore(t)
	s.Set("a", VisibilityPublic)
	s.Set("b", VisibilityLocal)
	all := s.All()
	if len(all) != 2 {
		t.Errorf("All len = %d, want 2", len(all))
	}
	if all["a"] != VisibilityPublic {
		t.Errorf("all[a] = %q, want public", all["a"])
	}
}

func TestValidateVisibility(t *testing.T) {
	for _, v := range []Visibility{VisibilityPrivate, VisibilityLocal, VisibilityPublic} {
		if err := ValidateVisibility(v); err != nil {
			t.Errorf("ValidateVisibility(%q) unexpected error: %v", v, err)
		}
	}
	if err := ValidateVisibility("bogus"); err == nil {
		t.Error("ValidateVisibility(bogus) expected error, got nil")
	}
}

func TestVisibilityStore_Persistence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vis.json")

	s1, _ := NewVisibilityStore(path)
	s1.Set("app1", VisibilityPublic)

	// Reload from the same file.
	s2, err := NewVisibilityStore(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got := s2.Get("app1"); got != VisibilityPublic {
		t.Errorf("after reload Get = %q, want public", got)
	}
}

// registerHandlers is a test helper that wires up the handlers and returns the mux.
func registerHandlers(t *testing.T) (*http.ServeMux, *VisibilityStore) {
	t.Helper()
	store := newTestAppStore(t)
	vis := newTestVisStore(t)
	mux := http.NewServeMux()
	RegisterVisibilityHandlers(mux, store, vis)
	return mux, vis
}

func TestGETVisibility_EmptyStore(t *testing.T) {
	mux, _ := registerHandlers(t)
	req := httptest.NewRequest("GET", "/api/apps/visibility", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	var resp []visAppVisibility
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// No apps installed → empty slice.
	if len(resp) != 0 {
		t.Errorf("expected 0 entries, got %d", len(resp))
	}
}

func TestGETVisibility_WithExplicitSetting(t *testing.T) {
	mux, vis := registerHandlers(t)
	vis.Set("myapp", VisibilityPublic)

	req := httptest.NewRequest("GET", "/api/apps/visibility", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	var resp []visAppVisibility
	json.NewDecoder(w.Body).Decode(&resp)

	if len(resp) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(resp))
	}
	if resp[0].AppID != "myapp" || resp[0].Visibility != VisibilityPublic {
		t.Errorf("unexpected entry: %+v", resp[0])
	}
}

func TestPOSTVisibility_Valid(t *testing.T) {
	mux, vis := registerHandlers(t)

	body, _ := json.Marshal(map[string]string{"visibility": "local"})
	req := httptest.NewRequest("POST", "/api/apps/calculator/visibility", bytes.NewReader(body))
	req.SetPathValue("id", "calculator")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	if got := vis.Get("calculator"); got != VisibilityLocal {
		t.Errorf("stored visibility = %q, want local", got)
	}
}

func TestPOSTVisibility_InvalidValue(t *testing.T) {
	mux, _ := registerHandlers(t)

	body, _ := json.Marshal(map[string]string{"visibility": "internet"})
	req := httptest.NewRequest("POST", "/api/apps/calculator/visibility", bytes.NewReader(body))
	req.SetPathValue("id", "calculator")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestPOSTVisibility_InvalidJSON(t *testing.T) {
	mux, _ := registerHandlers(t)

	req := httptest.NewRequest("POST", "/api/apps/calculator/visibility", bytes.NewReader([]byte("not-json")))
	req.SetPathValue("id", "calculator")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestGETVisibility_InstalledApp(t *testing.T) {
	dir := t.TempDir()
	appsDir := filepath.Join(dir, "apps")
	os.MkdirAll(filepath.Join(appsDir, "calc"), 0755)

	// Write a minimal app.json so ScanApps picks it up.
	manifest := `{"id":"calc","name":"Calc","version":"1.0","command":"bin/server","port":8080,"description":"A calculator"}`
	os.WriteFile(filepath.Join(appsDir, "calc", "app.json"), []byte(manifest), 0644)

	store := NewAppStore(appsDir)
	vis, _ := NewVisibilityStore(filepath.Join(dir, "vis.json"))
	mux := http.NewServeMux()
	RegisterVisibilityHandlers(mux, store, vis)

	req := httptest.NewRequest("GET", "/api/apps/visibility", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	var resp []visAppVisibility
	json.NewDecoder(w.Body).Decode(&resp)

	if len(resp) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(resp))
	}
	if resp[0].AppID != "calc" {
		t.Errorf("app_id = %q, want calc", resp[0].AppID)
	}
	// No explicit setting → default private.
	if resp[0].Visibility != VisibilityPrivate {
		t.Errorf("visibility = %q, want private", resp[0].Visibility)
	}
}
