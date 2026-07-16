package appnet

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// writeTestManifest writes a minimal app.json for id under appsDir/id/app.json.
func writeTestManifest(t *testing.T, appsDir, id, name string) {
	t.Helper()
	dir := filepath.Join(appsDir, id)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	m := AppManifest{
		ID:          id,
		Name:        name,
		Description: name + " description",
		Version:     "1.0.0",
		Command:     "bin/server",
		Port:        8080,
		Category:    "productivity",
		Icon:        "🧩",
	}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "app.json"), raw, 0644); err != nil {
		t.Fatal(err)
	}
}

// TestAppStore_Installed_ReturnsTileShape verifies GET /api/store/installed's
// backing call (AppStore.Installed) returns the fields the App Hub
// needs to render a tile: id, name, description, category, icon — the pinned
// cross-repo contract (two-class app model plan) for OS-box app inventory.
func TestAppStore_Installed_ReturnsTileShape(t *testing.T) {
	appsDir := t.TempDir()
	writeTestManifest(t, appsDir, "office", "Office")
	writeTestManifest(t, appsDir, "talk", "Talk")

	store := NewAppStore(appsDir)
	apps, err := store.Installed()
	if err != nil {
		t.Fatalf("Installed() error: %v", err)
	}
	if len(apps) != 2 {
		t.Fatalf("Installed() returned %d apps, want 2", len(apps))
	}

	byID := map[string]*AppManifest{}
	for _, a := range apps {
		byID[a.ID] = a
	}
	for _, id := range []string{"office", "talk"} {
		a, ok := byID[id]
		if !ok {
			t.Fatalf("expected app %q in Installed() result", id)
		}
		if a.Name == "" || a.Description == "" || a.Category == "" || a.Icon == "" {
			t.Fatalf("app %q missing tile fields: %+v", id, a)
		}
	}
}

// TestAppStore_Installed_EmptyDirIsEmptyNotError verifies a box with no apps
// installed returns an empty (not nil-error) list — the cockpit should render
// zero tiles, not an error state.
func TestAppStore_Installed_EmptyDirIsEmptyNotError(t *testing.T) {
	appsDir := t.TempDir()
	store := NewAppStore(appsDir)
	apps, err := store.Installed()
	if err != nil {
		t.Fatalf("Installed() error on empty dir: %v", err)
	}
	if len(apps) != 0 {
		t.Fatalf("expected 0 apps, got %d", len(apps))
	}
}

// TestAppStore_Installed_NoDuplicatesAcrossBundledAndLocal verifies that when
// the same app ID exists in both a bundled dir and the local apps dir, it is
// reported exactly once (the local copy wins), so the cockpit never renders a
// duplicate tile for one app.
func TestAppStore_Installed_NoDuplicatesAcrossBundledAndLocal(t *testing.T) {
	bundledDir := t.TempDir()
	appsDir := t.TempDir()
	writeTestManifest(t, bundledDir, "office", "Office (bundled)")
	writeTestManifest(t, appsDir, "office", "Office (local)")

	store := NewAppStore(appsDir)
	store.bundledDirs = []string{bundledDir}

	apps, err := store.Installed()
	if err != nil {
		t.Fatalf("Installed() error: %v", err)
	}
	count := 0
	var got *AppManifest
	for _, a := range apps {
		if a.ID == "office" {
			count++
			got = a
		}
	}
	if count != 1 {
		t.Fatalf("expected exactly one \"office\" entry, got %d", count)
	}
	if got.Name != "Office (local)" {
		t.Fatalf("expected the local manifest to win, got name %q", got.Name)
	}
}
