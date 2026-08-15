package appnet

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// bundledAppsDir returns the repo's real frontend/apps directory — the apps
// build.sh copies into /opt/vulos/apps, which is the ONLY place the box's
// AppStore looks for bundled apps (discoverBundledAppDirs).
func bundledAppsDir(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot resolve this test's own path")
	}
	// backend/services/appnet/x_test.go → repo root is three levels up.
	root := filepath.Join(filepath.Dir(thisFile), "..", "..", "..")
	dir := filepath.Join(root, "frontend", "apps")
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("bundled apps dir %s not found: %v", dir, err)
	}
	return dir
}

// TestBundledManifestsAreValid loads EVERY manifest the image ships and runs
// the same validation the box runs at scan time.
//
// This matters because manifest validation is the gate between "the app is on
// disk" and "the app exists as far as the OS is concerned". A manifest that
// fails Validate is dropped by ScanApps, so GetManifest 404s, /api/apps/launch
// never starts anything, no namespace is registered, and the gateway answers
// {"error":"app not running"} — indistinguishable from the app not being
// shipped at all.
//
// It found site-template declaring icon_path "icon.svg" with no such file, and
// (once that was fixed) being rejected outright because Validate demanded a
// command from an app that by design has no process. Both made that manifest
// invalid in every image.
//
// CACHING HAZARD — read before trusting a local PASS: this test's inputs live
// OUTSIDE the package (frontend/apps/**), so `go test` will happily replay a
// cached PASS after those files change. Three mutations were each "killed"
// from cache before this was noticed. Always verify with -count=1:
//
//	go test -count=1 ./services/appnet/ -run TestBundledManifestsAreValid
//
// CI is unaffected (fresh checkout, cold cache).
func TestBundledManifestsAreValid(t *testing.T) {
	dir := bundledAppsDir(t)

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}

	checked := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		manifestPath := filepath.Join(dir, e.Name(), "app.json")
		if _, err := os.Stat(manifestPath); err != nil {
			continue // e.g. _shared/, which is assets not an app
		}
		checked++
		t.Run(e.Name(), func(t *testing.T) {
			m, err := LoadAndValidateManifest(manifestPath)
			if err != nil {
				t.Fatalf("bundled app %s has an INVALID manifest, so the box "+
					"would drop it and every request would 404 "+
					"\"app not running\": %v", e.Name(), err)
			}
			if m.ID != e.Name() {
				t.Errorf("manifest id %q does not match its directory %q — the "+
					"box keys apps by directory, so this app is unreachable "+
					"under the id it declares", m.ID, e.Name())
			}
			// A process-backed app must actually contain the script its
			// command runs, or it will start and immediately die on the box.
			for _, field := range strings.Fields(m.Command) {
				if filepath.Ext(field) != ".py" {
					continue
				}
				if _, err := os.Stat(filepath.Join(dir, e.Name(), field)); err != nil {
					t.Errorf("command %q references %s, which is not in the "+
						"app directory: %v", m.Command, field, err)
				}
			}
			// A static web app has no process, so what it serves must be there.
			if m.Command == "" {
				if _, err := os.Stat(filepath.Join(dir, e.Name(), "index.html")); err != nil {
					t.Errorf("static app has no index.html to serve: %v", err)
				}
			}
		})
	}

	// COVERAGE ASSERTION: this test is worthless if it silently finds nothing.
	// 16 app directories ship today (15 process-backed + site-template).
	if checked < 16 {
		t.Fatalf("only %d bundled manifests were examined, expected at least 16 "+
			"— apps went missing from %s, or this floor is stale", checked, dir)
	}
}
