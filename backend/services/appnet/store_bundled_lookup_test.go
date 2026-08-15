package appnet

import (
	"os"
	"path/filepath"
	"testing"
)

// writeBundledApp lays down a minimal VALID app bundle at dir/<id>/.
func writeBundledApp(t *testing.T, dir, id string) {
	t.Helper()
	appDir := filepath.Join(dir, id)
	if err := os.MkdirAll(appDir, 0755); err != nil {
		t.Fatal(err)
	}
	manifest := `{
  "id": "` + id + `",
  "name": "Test App",
  "description": "fixture",
  "version": "0.1.0",
  "command": "python3 server.py",
  "port": 80,
  "category": "utilities"
}`
	if err := os.WriteFile(filepath.Join(appDir, "app.json"), []byte(manifest), 0644); err != nil {
		t.Fatal(err)
	}
}

// TestGetManifestFindsBundledApps pins the lookup that decides whether a
// SHIPPED app can be started at all.
//
// The box installs store apps into $HOME/.vulos/apps (AppStore.appsDir) but
// ships its own apps in /opt/vulos/apps (AppStore.bundledDirs, from
// discoverBundledAppDirs). POST /api/apps/launch resolves the command via
// GetManifest, and GetManifest read ONLY appsDir — so no bundled app could
// ever be launched: the request 404'd "app not found or invalid manifest",
// no namespace was registered, and the gateway then answered
// {"error":"app not running"}.
//
// That is the SAME failure the empty-/opt/vulos/apps build bug produced, so
// fixing the build alone would NOT have made a single app work. Installed()
// already merged both directories; GetManifest simply never did.
func TestGetManifestFindsBundledApps(t *testing.T) {
	bundled := t.TempDir()
	installed := t.TempDir()
	writeBundledApp(t, bundled, "calculator")

	s := &AppStore{appsDir: installed, bundledDirs: []string{bundled}}

	m, err := s.GetManifest("calculator")
	if err != nil {
		t.Fatalf("GetManifest could not resolve a BUNDLED app, so "+
			"/api/apps/launch would 404 and the gateway would answer "+
			"\"app not running\": %v", err)
	}
	if m.Command != "python3 server.py" {
		t.Errorf("command = %q, want the manifest's own command", m.Command)
	}
}

// TestGetManifestPrefersInstalledOverBundled keeps the precedence that
// Installed() already uses: a store-installed app of the same ID shadows the
// bundled copy, so an update is not silently ignored in favour of the
// shipped version.
func TestGetManifestPrefersInstalledOverBundled(t *testing.T) {
	bundled := t.TempDir()
	installed := t.TempDir()
	writeBundledApp(t, bundled, "notes")
	writeBundledApp(t, installed, "notes")

	// Make the installed copy distinguishable.
	p := filepath.Join(installed, "notes", "app.json")
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	updated := string(b[:len(b)-1]) + `,"author":"installed-copy"}`
	if err := os.WriteFile(p, []byte(updated), 0644); err != nil {
		t.Fatal(err)
	}

	s := &AppStore{appsDir: installed, bundledDirs: []string{bundled}}
	m, err := s.GetManifest("notes")
	if err != nil {
		t.Fatalf("GetManifest: %v", err)
	}
	if m.Author != "installed-copy" {
		t.Errorf("author = %q, want the INSTALLED copy to win over the bundled "+
			"one (matching AppStore.Installed precedence)", m.Author)
	}
}

// TestGetManifestMissingAppStillErrors makes sure the bundled fallback did not
// turn an unknown app into a success.
func TestGetManifestMissingAppStillErrors(t *testing.T) {
	s := &AppStore{appsDir: t.TempDir(), bundledDirs: []string{t.TempDir()}}
	if _, err := s.GetManifest("no-such-app"); err == nil {
		t.Fatal("GetManifest returned no error for an app that exists nowhere")
	}
}

// TestGetManifestRejectsInvalidBundledManifest makes sure the fallback still
// VALIDATES: an invalid bundled manifest must not become launchable just
// because it was found in a second directory.
func TestGetManifestRejectsInvalidBundledManifest(t *testing.T) {
	bundled := t.TempDir()
	appDir := filepath.Join(bundled, "broken")
	if err := os.MkdirAll(appDir, 0755); err != nil {
		t.Fatal(err)
	}
	// No command, and not a static web app → invalid.
	bad := `{"id":"broken","name":"Broken","description":"x","version":"0.1.0","port":80}`
	if err := os.WriteFile(filepath.Join(appDir, "app.json"), []byte(bad), 0644); err != nil {
		t.Fatal(err)
	}

	s := &AppStore{appsDir: t.TempDir(), bundledDirs: []string{bundled}}
	if _, err := s.GetManifest("broken"); err == nil {
		t.Fatal("an INVALID bundled manifest was accepted — validation must " +
			"still apply on the bundled path")
	}
}
