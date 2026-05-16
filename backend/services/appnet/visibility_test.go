package appnet

import (
	"os"
	"path/filepath"
	"testing"
)

// ---------------------------------------------------------------------------
// ValidateVisibility
// ---------------------------------------------------------------------------

func TestValidateVisibility(t *testing.T) {
	valid := []string{"private", "local", "public"}
	for _, v := range valid {
		if err := ValidateVisibility(v); err != nil {
			t.Errorf("ValidateVisibility(%q) unexpected error: %v", v, err)
		}
	}

	invalid := []string{"", "all", "PRIVATE", "none", "internet"}
	for _, v := range invalid {
		if err := ValidateVisibility(v); err == nil {
			t.Errorf("ValidateVisibility(%q) expected error, got nil", v)
		}
	}
}

// ---------------------------------------------------------------------------
// VisibilityStore
// ---------------------------------------------------------------------------

func newTestStore(t *testing.T) *VisibilityStore {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "visibility.json")
	vs, err := NewVisibilityStoreAt(path)
	if err != nil {
		t.Fatalf("NewVisibilityStoreAt: %v", err)
	}
	return vs
}

func TestVisibilityStore_DefaultsToPrivate(t *testing.T) {
	vs := newTestStore(t)
	if got := vs.Get("unknown-app"); got != VisibilityPrivate {
		t.Errorf("Get(unknown) = %q, want %q", got, VisibilityPrivate)
	}
}

func TestVisibilityStore_SetAndGet(t *testing.T) {
	vs := newTestStore(t)

	cases := []struct {
		appID string
		vis   string
	}{
		{"my-app", VisibilityPrivate},
		{"my-app", VisibilityLocal},
		{"my-app", VisibilityPublic},
		{"other-app", VisibilityLocal},
	}

	for _, c := range cases {
		if err := vs.Set(c.appID, c.vis); err != nil {
			t.Fatalf("Set(%q, %q): %v", c.appID, c.vis, err)
		}
		if got := vs.Get(c.appID); got != c.vis {
			t.Errorf("Get(%q) = %q, want %q", c.appID, got, c.vis)
		}
	}
}

func TestVisibilityStore_SetEmptyDefaultsToPrivate(t *testing.T) {
	vs := newTestStore(t)
	if err := vs.Set("app", ""); err != nil {
		t.Fatalf("Set with empty string: %v", err)
	}
	if got := vs.Get("app"); got != VisibilityPrivate {
		t.Errorf("Get after Set(\"\") = %q, want %q", got, VisibilityPrivate)
	}
}

func TestVisibilityStore_SetInvalidReturnsError(t *testing.T) {
	vs := newTestStore(t)
	if err := vs.Set("app", "world"); err == nil {
		t.Error("Set with invalid value expected error, got nil")
	}
}

func TestVisibilityStore_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "visibility.json")

	// Write some entries.
	vs1, err := NewVisibilityStoreAt(path)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	if err := vs1.Set("app-a", VisibilityPublic); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := vs1.Set("app-b", VisibilityLocal); err != nil {
		t.Fatalf("Set: %v", err)
	}

	// Re-open from the same file and verify persistence.
	vs2, err := NewVisibilityStoreAt(path)
	if err != nil {
		t.Fatalf("second open: %v", err)
	}
	if got := vs2.Get("app-a"); got != VisibilityPublic {
		t.Errorf("round-trip app-a = %q, want %q", got, VisibilityPublic)
	}
	if got := vs2.Get("app-b"); got != VisibilityLocal {
		t.Errorf("round-trip app-b = %q, want %q", got, VisibilityLocal)
	}
	// An app not set should still be private.
	if got := vs2.Get("app-c"); got != VisibilityPrivate {
		t.Errorf("round-trip app-c = %q, want %q", got, VisibilityPrivate)
	}
}

func TestVisibilityStore_Delete(t *testing.T) {
	vs := newTestStore(t)
	if err := vs.Set("app", VisibilityPublic); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := vs.Delete("app"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if got := vs.Get("app"); got != VisibilityPrivate {
		t.Errorf("Get after Delete = %q, want %q", got, VisibilityPrivate)
	}
}

func TestVisibilityStore_All(t *testing.T) {
	vs := newTestStore(t)
	if err := vs.Set("a", VisibilityLocal); err != nil {
		t.Fatalf("Set a: %v", err)
	}
	if err := vs.Set("b", VisibilityPublic); err != nil {
		t.Fatalf("Set b: %v", err)
	}

	all := vs.All()
	if len(all) != 2 {
		t.Fatalf("All() returned %d entries, want 2", len(all))
	}
	if all["a"] != VisibilityLocal {
		t.Errorf("All[a] = %q, want %q", all["a"], VisibilityLocal)
	}
	if all["b"] != VisibilityPublic {
		t.Errorf("All[b] = %q, want %q", all["b"], VisibilityPublic)
	}

	// Returned map must be a copy — mutations must not affect the store.
	all["a"] = VisibilityPublic
	if got := vs.Get("a"); got != VisibilityLocal {
		t.Errorf("store mutated via All() map: got %q, want %q", got, VisibilityLocal)
	}
}

func TestVisibilityStore_MissingFileIsOK(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nonexistent", "visibility.json")
	// File (and its parent directory) does not exist yet — load must succeed.
	_, err := NewVisibilityStoreAt(path)
	if err != nil {
		t.Fatalf("NewVisibilityStoreAt on missing file: %v", err)
	}
}

func TestVisibilityStore_CreatesParentDirs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "subdir", "deep", "visibility.json")
	vs, err := NewVisibilityStoreAt(path)
	if err != nil {
		t.Fatalf("NewVisibilityStoreAt: %v", err)
	}
	if err := vs.Set("x", VisibilityPublic); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("expected file at %s: %v", path, err)
	}
}

// ---------------------------------------------------------------------------
// AppManifest.Validate — Visibility field
// ---------------------------------------------------------------------------

// minimalManifest returns a Validate-ready manifest with the bare minimum
// fields set.  appDir is ignored for these tests (IconPath is empty).
func minimalManifest(id, visibility string) *AppManifest {
	return &AppManifest{
		ID:          id,
		Name:        "Test App",
		Version:     "1.0.0",
		Command:     "bin/server",
		Port:        8080,
		Description: "a test app",
		Visibility:  visibility,
	}
}

func TestManifestValidate_VisibilityDefaults(t *testing.T) {
	m := minimalManifest("test-app", "")
	if err := m.Validate("/tmp"); err != nil {
		t.Fatalf("Validate with empty visibility: %v", err)
	}
	if m.Visibility != VisibilityPrivate {
		t.Errorf("Visibility after Validate = %q, want %q", m.Visibility, VisibilityPrivate)
	}
}

func TestManifestValidate_VisibilityValid(t *testing.T) {
	for _, v := range ValidVisibilities {
		m := minimalManifest("test-app", v)
		if err := m.Validate("/tmp"); err != nil {
			t.Errorf("Validate(visibility=%q) unexpected error: %v", v, err)
		}
	}
}

func TestManifestValidate_VisibilityInvalid(t *testing.T) {
	m := minimalManifest("test-app", "internet")
	if err := m.Validate("/tmp"); err == nil {
		t.Error("Validate with invalid visibility expected error, got nil")
	}
}
