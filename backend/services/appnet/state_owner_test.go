package appnet

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// ─────────────────────────────────────────────────────────────────────────────
// STATE-01 — the installer builds the app directory as root and the launcher
// runs the app as uid 65534, so SOMETHING has to hand the state directory over.
// Seven migrated recipes did it in post_install, once each. These tests pin the
// central answer instead, and they pin it by OBSERVING THE CHOWNS, because no
// unprivileged test process can perform one — a test that skipped the
// assertion when it could not chown would assert nothing at all, which is this
// repo's dominant defect.
//
// The uid/gid below are transcribed from `launcher.go`'s documented account
// (nobody/nogroup on every Debian and Ubuntu image) and from the `setpriv
// --reuid=65534 --regid=65534` line the launcher builds — NOT read back from
// appUID/appGID, which are the symbols under test.
// ─────────────────────────────────────────────────────────────────────────────

const (
	wantStateUID = 65534
	wantStateGID = 65534
)

// chownRecord is one observed chown call.
type chownRecord struct {
	path string
	uid  int
	gid  int
}

// captureChowns replaces the installer's chown with a recorder and claims to be
// root, so the test sees exactly what a real box would do.
func captureChowns(t *testing.T) *[]chownRecord {
	t.Helper()
	var got []chownRecord
	origChown, origEuid := chownFn, geteuidFn
	chownFn = func(path string, uid, gid int) error {
		got = append(got, chownRecord{path: path, uid: uid, gid: gid})
		return nil
	}
	geteuidFn = func() int { return 0 }
	t.Cleanup(func() { chownFn, geteuidFn = origChown, origEuid })
	return &got
}

func chownedPaths(records []chownRecord) []string {
	out := make([]string, 0, len(records))
	for _, r := range records {
		out = append(out, r.path)
	}
	sort.Strings(out)
	return out
}

// installStateFixture installs a one-binary app whose post_install builds a
// nested state tree, and returns the app dir plus the chowns that were made.
func installStateFixture(t *testing.T, postInstall string) (string, []chownRecord) {
	t.Helper()
	withInsecureRegistry(t)
	// Keep the data-dir symlink step inside the test's own tree.
	t.Setenv("VULOS_DATA_DIR", t.TempDir())

	body := []byte("#!/bin/sh\necho hi\n")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(body)
	}))
	defer srv.Close()

	appsDir := t.TempDir()
	reg := &Registry{Apps: map[string]*RegistryEntry{
		"stateowner": {
			Name: "State Owner", Vetted: true, Type: "web",
			Description: "state ownership fixture", Category: "developer",
			Icon: "S", Author: "Test", License: "MIT", Homepage: "https://example.com",
			Versions: map[string]*VersionRecipe{
				"1.0": {
					Artifacts: map[string]*Artifact{
						"amd64": {DownloadURL: srv.URL + "/app", Checksum: sha256Hex(body)},
						"arm64": {DownloadURL: srv.URL + "/app", Checksum: sha256Hex(body)},
					},
					BinaryName:  "stateowner",
					Command:     "bin/stateowner",
					Port:        8080,
					PostInstall: postInstall,
				},
			},
		},
	}}

	records := captureChowns(t)
	if err := InstallFromRegistry(context.Background(), reg, "stateowner", "1.0", appsDir); err != nil {
		t.Fatalf("InstallFromRegistry: %v", err)
	}
	return filepath.Join(appsDir, "stateowner"), *records
}

// TestInstall_HandsTheWholeStateTreeToTheAppUID is the assertion the seven
// per-recipe `chown -R 65534:65534 data` lines were standing in for: every path
// under data/ — including what post_install created two levels down — belongs
// to the uid the app runs as when the install returns.
func TestInstall_HandsTheWholeStateTreeToTheAppUID(t *testing.T) {
	appDir, records := installStateFixture(t, "mkdir -p data/db && printf 'x' > data/db/index")

	// Resolved the way the installer resolves it: on macOS /var is itself a
	// symlink to /private/var, so an unresolved literal would compare two
	// spellings of one directory and fail for a reason that is not the subject.
	dataDir, err := filepath.EvalSymlinks(filepath.Join(appDir, "data"))
	if err != nil {
		t.Fatalf("state dir missing after install: %v", err)
	}
	want := []string{
		dataDir,
		filepath.Join(dataDir, "db"),
		filepath.Join(dataDir, "db", "index"),
	}
	seen := map[string]chownRecord{}
	for _, r := range records {
		seen[r.path] = r
	}
	for _, p := range want {
		r, ok := seen[p]
		if !ok {
			t.Errorf("%s was never chowned — the app runs as uid %d and cannot write it "+
				"(STATE-01). chowned: %v", p, wantStateUID, chownedPaths(records))
			continue
		}
		if r.uid != wantStateUID || r.gid != wantStateGID {
			t.Errorf("%s handed to %d:%d, want %d:%d — the launcher execs the app as "+
				"`setpriv --reuid=%d --regid=%d`", p, r.uid, r.gid, wantStateUID, wantStateGID,
				wantStateUID, wantStateGID)
		}
	}
}

// TestInstall_LeavesTheCodeDirectoriesRootOwned pins the OTHER half of the
// model, and it is not decoration. A blanket `chown -R 65534 <appdir>` would
// pass the test above and hand the app write access to its own binary, which
// turns any bug in the app into persistence across restarts. Code is read-only
// to the process that runs it.
func TestInstall_LeavesTheCodeDirectoriesRootOwned(t *testing.T) {
	rawAppDir, records := installStateFixture(t, "mkdir -p data/db")
	// Resolved, because the installer chowns resolved paths: comparing an
	// unresolved /var/... spelling against a recorded /private/var/... one
	// matches nothing, and a check that can never match is not a check. A
	// mutation that chowned the WHOLE app dir was caught by another test while
	// this one stayed green, which is how that was found.
	appDir, err := filepath.EvalSymlinks(rawAppDir)
	if err != nil {
		t.Fatalf("app dir missing after install: %v", err)
	}

	forbidden := []string{
		filepath.Join(appDir, "bin"),
		filepath.Join(appDir, "bin", "stateowner"),
		filepath.Join(appDir, "static"),
		filepath.Join(appDir, "app.json"),
		appDir,
	}
	for _, r := range records {
		for _, f := range forbidden {
			if r.path == f {
				t.Errorf("installer chowned %s to %d:%d — bin/ and static/ are CODE and stay "+
					"root-owned; an app that can rewrite its own binary keeps any compromise "+
					"across a restart", r.path, r.uid, r.gid)
			}
		}
	}
	if len(records) == 0 {
		t.Fatal("no chowns at all — this test would pass vacuously; the state handoff is missing")
	}
}

// TestInstall_HandsOverTheSymlinkTARGET covers the shape the per-recipe stopgap
// could not: with no post_install, data/ is empty, so InstallFromRegistry
// REPLACES it with a symlink into the owner's data directory. Chowning the link
// itself would change nothing on disk (POSIX ignores a symlink's mode), and the
// app would still be unable to write. The target is what must change hands.
func TestInstall_HandsOverTheSymlinkTARGET(t *testing.T) {
	dataRoot := t.TempDir()
	withInsecureRegistry(t)
	t.Setenv("VULOS_DATA_DIR", dataRoot)

	body := []byte("#!/bin/sh\nexit 0\n")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(body)
	}))
	defer srv.Close()

	appsDir := t.TempDir()
	reg := &Registry{Apps: map[string]*RegistryEntry{
		"linkowner": {
			Name: "Link Owner", Vetted: true, Type: "web",
			Description: "symlinked state dir", Category: "developer",
			Icon: "L", Author: "Test", License: "MIT", Homepage: "https://example.com",
			Versions: map[string]*VersionRecipe{
				"1.0": {
					Artifacts: map[string]*Artifact{
						"amd64": {DownloadURL: srv.URL + "/app", Checksum: sha256Hex(body)},
						"arm64": {DownloadURL: srv.URL + "/app", Checksum: sha256Hex(body)},
					},
					BinaryName: "linkowner",
					Command:    "bin/linkowner",
					Port:       8080,
				},
			},
		},
	}}

	records := captureChowns(t)
	if err := InstallFromRegistry(context.Background(), reg, "linkowner", "1.0", appsDir); err != nil {
		t.Fatalf("InstallFromRegistry: %v", err)
	}

	appDir := filepath.Join(appsDir, "linkowner")
	if fi, err := os.Lstat(filepath.Join(appDir, "data")); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Skipf("this install did not produce a data symlink (%v) — the case under test did not arise", err)
	}
	// The owner's data directory, resolved the way the OS resolves it.
	userData, err := filepath.EvalSymlinks(filepath.Join(dataRoot, "data", "linkowner"))
	if err != nil {
		t.Fatalf("owner data dir missing: %v", err)
	}

	var hit *chownRecord
	for i := range *records {
		if (*records)[i].path == userData {
			hit = &(*records)[i]
		}
	}
	if hit == nil {
		t.Fatalf("the symlink TARGET %s was never chowned — chowning the link would be a no-op "+
			"and the app could not write its own data (STATE-01). chowned: %v",
			userData, chownedPaths(*records))
	}
	if hit.uid != wantStateUID || hit.gid != wantStateGID {
		t.Errorf("%s handed to %d:%d, want %d:%d", userData, hit.uid, hit.gid, wantStateUID, wantStateGID)
	}
}

// TestHandOffStateDir_NotRootIsASkipNotAFailure pins the one case that may be
// silent. `go test` and `make dev` are not root; an unprivileged installer also
// cannot setpriv, so it runs the app as ITSELF and owner and app already match.
// Refusing there would break every developer install to fix nothing.
func TestHandOffStateDir_NotRootIsASkipNotAFailure(t *testing.T) {
	appDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(appDir, "data"), 0o755); err != nil {
		t.Fatal(err)
	}
	called := 0
	origChown, origEuid := chownFn, geteuidFn
	chownFn = func(string, int, int) error { called++; return nil }
	geteuidFn = func() int { return 1000 }
	t.Cleanup(func() { chownFn, geteuidFn = origChown, origEuid })

	if err := handOffStateDir(appDir); err != nil {
		t.Errorf("handOffStateDir refused an unprivileged install: %v", err)
	}
	if called != 0 {
		t.Errorf("attempted %d chown(s) as a non-root installer — the call cannot succeed and "+
			"its failure would be reported as an install failure", called)
	}
}

// TestInstall_FailsWhenTheStateHandoffFails is the assertion that keeps this
// from becoming decoration. POSTINSTALL-01 exists because a step that logs a
// warning and carries on shipped an app that died on every launch; a state
// handoff that logged and carried on would ship exactly the same app.
func TestInstall_FailsWhenTheStateHandoffFails(t *testing.T) {
	withInsecureRegistry(t)
	t.Setenv("VULOS_DATA_DIR", t.TempDir())

	body := []byte("#!/bin/sh\nexit 0\n")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(body)
	}))
	defer srv.Close()

	origChown, origEuid := chownFn, geteuidFn
	chownFn = func(string, int, int) error { return os.ErrPermission }
	geteuidFn = func() int { return 0 }
	t.Cleanup(func() { chownFn, geteuidFn = origChown, origEuid })

	appsDir := t.TempDir()
	reg := &Registry{Apps: map[string]*RegistryEntry{
		"failowner": {
			Name: "Fail Owner", Vetted: true, Type: "web",
			Description: "chown failure", Category: "developer",
			Icon: "F", Author: "Test", License: "MIT", Homepage: "https://example.com",
			Versions: map[string]*VersionRecipe{
				"1.0": {
					Artifacts: map[string]*Artifact{
						"amd64": {DownloadURL: srv.URL + "/app", Checksum: sha256Hex(body)},
						"arm64": {DownloadURL: srv.URL + "/app", Checksum: sha256Hex(body)},
					},
					BinaryName: "failowner",
					Command:    "bin/failowner",
					Port:       8080,
				},
			},
		},
	}}

	err := InstallFromRegistry(context.Background(), reg, "failowner", "1.0", appsDir)
	if err == nil {
		t.Fatal("install reported SUCCESS after the state handoff failed — the app would have " +
			"installed and then died on its first write (STATE-01)")
	}
	if !strings.Contains(err.Error(), "STATE-01") {
		t.Errorf("install failed, but not with the state-handoff error: %v", err)
	}
}
