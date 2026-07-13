package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestDetectGamingLaunch_Command verifies the launch-app auto-detect engages
// gaming mode for real game/compat-layer launchers and NOT for plain desktop or
// GPU-accelerated non-game commands.
func TestDetectGamingLaunch_Command(t *testing.T) {
	cases := []struct {
		cmd  string
		want bool
	}{
		// Real games / Windows compat layers → gaming mode ON.
		{"steam", true},
		{"lutris", true},
		{"wine", true},
		{"wine64", true},
		{"steam-runtime", true},
		{"/usr/bin/lutris", true},
		{"steam -applaunch 42", true},
		// Plain desktop apps and GPU-accelerated non-games → gaming mode OFF.
		{"kicad", false},
		{"audacity", false},
		{"blender", false},
		{"kdenlive", false},
		{"firefox", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := detectGamingLaunch(tc.cmd, "", ""); got != tc.want {
			t.Errorf("detectGamingLaunch(%q) = %v, want %v", tc.cmd, got, tc.want)
		}
	}
}

// TestDetectGamingLaunch_ManifestCategory verifies the manifest-category branch:
// a non-launcher command still engages gaming mode when the app manifest
// declares category=="gaming", and does not for any other category.
func TestDetectGamingLaunch_ManifestCategory(t *testing.T) {
	appsDir := t.TempDir()

	write := func(appID, category string) {
		dir := filepath.Join(appsDir, appID)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		body := `{"id":"` + appID + `","name":"` + appID + `","category":"` + category + `"}`
		if err := os.WriteFile(filepath.Join(dir, "app.json"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	write("mygame", "gaming")
	write("myeditor", "productivity")

	// A non-launcher command whose manifest says gaming → ON.
	if !detectGamingLaunch("/opt/mygame/run.sh", "mygame", appsDir) {
		t.Errorf("manifest category=gaming should engage gaming mode")
	}
	// A non-launcher command whose manifest is not gaming → OFF.
	if detectGamingLaunch("/opt/myeditor/run.sh", "myeditor", appsDir) {
		t.Errorf("manifest category=productivity should NOT engage gaming mode")
	}
	// Missing manifest, non-launcher command → OFF.
	if detectGamingLaunch("/opt/unknown/run.sh", "unknown", appsDir) {
		t.Errorf("missing manifest + non-launcher command should NOT engage gaming mode")
	}
}
