package appnet

import (
	"os"
	"path/filepath"
	"testing"
)

// ─── Reading a release artifact so that `go test` can see you read it ────────
//
// registry.json and registry.d/ live at the REPO ROOT, outside this Go module.
// `go test` caches a package's result keyed on, among other things, the files
// the test binary opened — but cmd/go records only files under the module root
// and discards every path outside it. A test that reads
// `../../../registry.json` therefore declares NO dependency on the registry at
// all: change the registry, re-run, and go prints "ok (cached)" over data it
// never looked at.
//
// MEASURED 2026-08-19 with five one-test packages, each reading the registry a
// different way, against a warm cache and a single-entry edit to registry.json:
//
//	read path                                     registry.json edit
//	../../../registry.json                        STALE — "(cached)", old size
//	<module>/…/other-pkg/testdata/registry.json   re-ran, new size
//	testdata/registry.json (local symlink)        re-ran, new size
//
//	read path                          fragment ADDED   fragment EDITED
//	../../../registry.d                STALE            STALE
//	testdata/registry.d (symlink)      re-ran           re-ran
//
// So the READ PATH is the whole fix, and it costs one symlink: reached through
// a path inside the module, the file's size and mtime enter the cache key and
// an edit invalidates it. A directory symlink covers the fragments both ways —
// go hashes a directory's entry list on ReadDir and each fragment's stat on
// open, so an added file and an edited file both register.
//
// The demonstration that made this urgent: with the ratchet in arch_test.go
// warm in the cache, adding one arch-less entry to registry.json (7 undeclared
// against a ceiling of 6, a test that CANNOT pass) still printed
// "ok  vulos/backend/services/appnet  (cached)". The same run with -count=1
// failed. The catalogue had just gone 74 → 142 entries.
//
// Every registry read in this package goes through the two helpers below.
// TestRegistryReadsAreCacheVisible in internal/docsref enforces that, module
// wide, and enforces that these symlinks still point at the shipped files.

// shippedRegistryPath returns the path this package's tests must use to read
// the registry.json this repo ships.  The returned path is inside the module,
// so go's test cache sees the dependency.
func shippedRegistryPath(t *testing.T) string {
	t.Helper()
	return inModuleArtifact(t, "registry.json")
}

// shippedFragmentDir returns the path this package's tests must use to read
// the registry.d/ fragment directory.  Same reason, same mechanism.
func shippedFragmentDir(t *testing.T) string {
	t.Helper()
	return inModuleArtifact(t, "registry.d")
}

// inModuleArtifact resolves testdata/<name> and refuses to hand back a path
// that is not a symlink onto the canonical repo-root artifact.
//
// The symlink-ness is checked, not assumed. A REAL registry.json committed at
// testdata/ would satisfy every read in this package and every gate would go
// green against a second, divergent copy of a signed release artifact — the
// same shape as the stray backend/registry.json found on 2026-08-17, which
// TestNoStrayRegistryBesideTheSigner exists to keep out.
func inModuleArtifact(t *testing.T, name string) string {
	t.Helper()

	link := filepath.Join("testdata", name)
	info, err := os.Lstat(link)
	if err != nil {
		t.Fatalf("%s is missing: %v\n"+
			"It is the symlink that makes the shipped %s visible to go's test "+
			"cache. Recreate it:\n"+
			"  ln -s ../../../../%s backend/services/appnet/%s",
			link, err, name, name, link)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("%s is a real %s, not a symlink onto the repo-root one.\n"+
			"A second copy of a signed release artifact goes stale silently and "+
			"every gate in this package would then pass against the copy.",
			link, map[bool]string{true: "directory", false: "file"}[info.IsDir()])
	}

	got, err := filepath.EvalSymlinks(link)
	if err != nil {
		t.Fatalf("%s is a dangling symlink: %v", link, err)
	}
	want, err := filepath.EvalSymlinks(filepath.Join(repoRoot, name))
	if err != nil {
		t.Fatalf("the canonical %s is not at the repo root: %v", name, err)
	}
	if got != want {
		t.Fatalf("%s resolves to %s, not to the shipped %s at %s", link, got, name, want)
	}
	return link
}
