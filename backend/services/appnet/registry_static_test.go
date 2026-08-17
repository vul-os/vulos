package appnet

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestStaticInstall_Binary verifies that a plain binary download is placed at
// bin/<filename> and made executable.
func TestStaticInstall_Binary(t *testing.T) {
	content := []byte("#!/bin/sh\necho hello\n")
	sum := sha256.Sum256(content)
	checksum := hex.EncodeToString(sum[:])

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(content)
	}))
	defer srv.Close()

	appDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(appDir, "bin"), 0755); err != nil {
		t.Fatal(err)
	}

	recipe := &VersionRecipe{
		DownloadURL: srv.URL + "/myapp",
		Checksum:    checksum,
	}

	if err := staticInstall(context.Background(), recipe, appDir); err != nil {
		t.Fatalf("staticInstall returned error: %v", err)
	}

	dest := filepath.Join(appDir, "bin", "myapp")
	info, err := os.Stat(dest)
	if err != nil {
		t.Fatalf("binary not found at %s: %v", dest, err)
	}
	if info.Mode()&0111 == 0 {
		t.Errorf("binary at %s is not executable (mode=%s)", dest, info.Mode())
	}

	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(content) {
		t.Errorf("binary content mismatch: got %q, want %q", got, content)
	}
}

// TestStaticInstall_ChecksumMismatch verifies that a wrong checksum is rejected.
func TestStaticInstall_ChecksumMismatch(t *testing.T) {
	content := []byte("real content")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(content)
	}))
	defer srv.Close()

	appDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(appDir, "bin"), 0755); err != nil {
		t.Fatal(err)
	}

	recipe := &VersionRecipe{
		DownloadURL: srv.URL + "/myapp",
		Checksum:    "0000000000000000000000000000000000000000000000000000000000000000",
	}

	err := staticInstall(context.Background(), recipe, appDir)
	if err == nil {
		t.Fatal("expected checksum mismatch error, got nil")
	}
	if !strings.Contains(err.Error(), "checksum mismatch") {
		t.Errorf("unexpected error message: %v", err)
	}
}

// TestStaticInstall_NoChecksum verifies that omitting checksum still installs successfully.
func TestStaticInstall_NoChecksum(t *testing.T) {
	content := []byte("binary-no-checksum")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(content)
	}))
	defer srv.Close()

	appDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(appDir, "bin"), 0755); err != nil {
		t.Fatal(err)
	}

	recipe := &VersionRecipe{
		DownloadURL: srv.URL + "/server",
	}

	if err := staticInstall(context.Background(), recipe, appDir); err != nil {
		t.Fatalf("staticInstall returned error: %v", err)
	}

	if _, err := os.Stat(filepath.Join(appDir, "bin", "server")); err != nil {
		t.Fatalf("binary not installed: %v", err)
	}
}

// TestVersionRecipe_DownloadURLJSON verifies that DownloadURL and ArchiveStrip
// round-trip through JSON correctly.
func TestVersionRecipe_DownloadURLJSON(t *testing.T) {
	const raw = `{
		"download_url": "https://example.com/app.tar.gz",
		"archive_strip": 1,
		"command": "bin/app",
		"port": 8080,
		"checksum": "abc123",
		"singleton": true
	}`

	var recipe VersionRecipe
	if err := json.Unmarshal([]byte(raw), &recipe); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if recipe.DownloadURL != "https://example.com/app.tar.gz" {
		t.Errorf("DownloadURL: got %q, want https://example.com/app.tar.gz", recipe.DownloadURL)
	}
	if recipe.ArchiveStrip != 1 {
		t.Errorf("ArchiveStrip: got %d, want 1", recipe.ArchiveStrip)
	}
	if recipe.Checksum != "abc123" {
		t.Errorf("Checksum: got %q, want abc123", recipe.Checksum)
	}
	if !recipe.Singleton {
		t.Error("Singleton: expected true")
	}

	// Round-trip: marshal back and check keys present.
	data, err := json.Marshal(&recipe)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(data), `"download_url"`) {
		t.Errorf("marshalled JSON missing download_url: %s", data)
	}
	if !strings.Contains(string(data), `"archive_strip"`) {
		t.Errorf("marshalled JSON missing archive_strip: %s", data)
	}
}

// TestInstallFromRegistry_StaticPath verifies the full static install flow:
// download → checksum verify → binary installed → manifest written.
func TestInstallFromRegistry_StaticPath(t *testing.T) {
	// C1 fix: registry now requires a trust anchor. Use INSECURE mode in tests.
	withInsecureRegistry(t)
	content := []byte("#!/bin/sh\necho running\n")
	sum := sha256.Sum256(content)
	checksum := hex.EncodeToString(sum[:])

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(content)
	}))
	defer srv.Close()

	appsDir := t.TempDir()
	reg := &Registry{
		Apps: map[string]*RegistryEntry{
			"myapp": {
				Name:        "My App",
				Vetted:      true,
				Type:        "web",
				Description: "Test static app",
				Category:    "developer",
				Icon:        "M",
				Author:      "Test Author",
				License:     "MIT",
				Homepage:    "https://example.com",
				Versions: map[string]*VersionRecipe{
					"1.0": {
						Artifacts: map[string]*Artifact{
							"amd64": {DownloadURL: srv.URL + "/myapp", Checksum: checksum},
							"arm64": {DownloadURL: srv.URL + "/myapp", Checksum: checksum},
						},
						BinaryName: "myapp",
						Command:    "bin/myapp",
						Port:       8080,
					},
				},
			},
		},
	}

	if err := InstallFromRegistry(context.Background(), reg, "myapp", "1.0", appsDir); err != nil {
		t.Fatalf("InstallFromRegistry failed: %v", err)
	}

	// Binary must be installed.
	binPath := filepath.Join(appsDir, "myapp", "bin", "myapp")
	if _, err := os.Stat(binPath); err != nil {
		t.Errorf("binary not found at %s: %v", binPath, err)
	}

	// Manifest must exist and have correct fields.
	manifestPath := filepath.Join(appsDir, "myapp", "app.json")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("manifest not found at %s: %v", manifestPath, err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("manifest not valid JSON: %v", err)
	}
	if m["id"] != "myapp" {
		t.Errorf("manifest id: got %v, want myapp", m["id"])
	}
	if m["version"] != "1.0" {
		t.Errorf("manifest version: got %v, want 1.0", m["version"])
	}
	if m["command"] != "bin/myapp" {
		t.Errorf("manifest command: got %v, want bin/myapp", m["command"])
	}
}

// TestRegistryJSON_NavidromeEntry validates that the navidrome static entry in
// registry.json parses correctly and has all required fields.
func TestRegistryJSON_NavidromeEntry(t *testing.T) {
	// Test file lives at backend/services/appnet/, registry.json is 3 dirs up.
	regPath := filepath.Join("..", "..", "..", "registry.json")
	if _, err := os.Stat(regPath); err != nil {
		t.Skipf("registry.json not found at %s: %v", regPath, err)
	}

	reg, err := LoadRegistry(regPath)
	if err != nil {
		t.Fatalf("LoadRegistry: %v", err)
	}

	entry, ok := reg.Apps["navidrome"]
	if !ok {
		t.Fatal("navidrome not found in registry.json")
	}
	if entry.Name == "" {
		t.Error("navidrome: name is empty")
	}
	if entry.Type != "web" {
		t.Errorf("navidrome: type = %q, want web", entry.Type)
	}

	// MIGRATED to the per-architecture vehicle (roadmap/INSTALL-METHODOLOGY.md).
	// The old loop keyed everything off `recipe.DownloadURL != ""` — the
	// retired field — so once navidrome migrated, `found` stayed false AND the
	// command/port checks inside the branch stopped running entirely. A test
	// that goes quiet when its subject changes shape is the failure this suite
	// keeps finding, so the replacement asserts the vehicle that exists now and
	// checks command/port UNCONDITIONALLY rather than inside a branch.
	found := false
	for ver, recipe := range entry.Versions {
		if recipe.DownloadURL != "" {
			t.Errorf("navidrome version %s: still carries a top-level download_url — the retired vehicle", ver)
		}
		if !recipe.HasArtifacts() {
			continue
		}
		found = true
		for arch, a := range recipe.Artifacts {
			if !strings.HasPrefix(a.DownloadURL, "https://") {
				t.Errorf("navidrome %s[%s]: download_url must be https, got %q", ver, arch, a.DownloadURL)
			}
			if len(a.Checksum) != 64 {
				t.Errorf("navidrome %s[%s]: checksum %q is not a bare 64-char sha256 hex digest", ver, arch, a.Checksum)
			}
		}
		if recipe.Command == "" {
			t.Errorf("navidrome version %s: command is empty", ver)
		}
		if recipe.Port == 0 {
			t.Errorf("navidrome version %s: port is 0", ver)
		}
	}
	if !found {
		t.Error("navidrome: no version declares per-architecture artifacts")
	}
}

// TestRegistryJSON_AptFlatpakUnchanged confirms that existing apt and Flatpak entries
// still parse and have their expected fields after our changes.
func TestRegistryJSON_AptFlatpakUnchanged(t *testing.T) {
	regPath := filepath.Join("..", "..", "..", "registry.json")
	if _, err := os.Stat(regPath); err != nil {
		t.Skipf("registry.json not found: %v", err)
	}

	reg, err := LoadRegistry(regPath)
	if err != nil {
		t.Fatalf("LoadRegistry: %v", err)
	}

	// cockpit → apt-based
	cockpit, ok := reg.Apps["cockpit"]
	if !ok {
		t.Fatal("cockpit not found in registry.json")
	}
	recipe := cockpit.GetRecipe("latest")
	if recipe == nil {
		t.Fatal("cockpit: no recipe")
	}
	if !strings.Contains(recipe.Install, "apt-get") {
		t.Errorf("cockpit: expected apt-get in Install, got %q", recipe.Install)
	}
	if recipe.FlatpakID != "" {
		t.Errorf("cockpit: expected empty FlatpakID, got %q", recipe.FlatpakID)
	}
	if recipe.DownloadURL != "" {
		t.Errorf("cockpit: expected empty DownloadURL, got %q", recipe.DownloadURL)
	}

	// gimp → Flatpak-based
	gimp, ok := reg.Apps["gimp"]
	if !ok {
		t.Fatal("gimp not found in registry.json")
	}
	gimpRecipe := gimp.GetRecipe("latest")
	if gimpRecipe == nil {
		t.Fatal("gimp: no recipe")
	}
	if gimpRecipe.FlatpakID == "" {
		t.Error("gimp: expected non-empty FlatpakID")
	}
	if gimpRecipe.DownloadURL != "" {
		t.Errorf("gimp: expected empty DownloadURL, got %q", gimpRecipe.DownloadURL)
	}
}
