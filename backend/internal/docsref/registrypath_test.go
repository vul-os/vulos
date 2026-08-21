package docsref

import (
	"os"
	"path/filepath"
	"testing"
)

// shippedRegistryPath returns the path this package must use to read the
// registry.json this repo ships.
//
// registry.json lives at the repo root, OUTSIDE this Go module, and cmd/go
// records a test's file dependencies only for paths under the module root.
// A gate that reads `../../../registry.json` therefore declares no dependency
// on the registry at all and `go test` will re-serve its verdict, unchanged,
// over a registry that has since been rewritten. Measured 2026-08-19; see the
// header of services/appnet/registry_cachepath_test.go for the full table and
// for the demonstration (an arch ratchet printing "ok (cached)" against a
// registry that could not possibly satisfy it).
//
// testdata/registry.json is a symlink onto the repo-root file. Reached through
// that in-module path, the file's stat enters the cache key and an edit
// invalidates the result.
func shippedRegistryPath(t *testing.T) string {
	t.Helper()

	const link = "testdata/registry.json"
	info, err := os.Lstat(link)
	if err != nil {
		t.Fatalf("%s is missing: %v\n"+
			"It is the symlink that makes the shipped registry.json visible to "+
			"go's test cache. Recreate it:\n"+
			"  ln -s ../../../../registry.json backend/internal/docsref/%s",
			link, err, link)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("%s is a real file, not a symlink onto the repo-root registry.\n"+
			"A second copy of a signed release artifact goes stale silently, and "+
			"every gate reading it would then pass against the copy — the shape "+
			"TestNoStrayRegistryBesideTheSigner exists to keep out.", link)
	}
	got, err := filepath.EvalSymlinks(link)
	if err != nil {
		t.Fatalf("%s is a dangling symlink: %v", link, err)
	}
	want, err := filepath.EvalSymlinks(filepath.Join(repoRoot, "registry.json"))
	if err != nil {
		t.Fatalf("the canonical registry.json is not at the repo root: %v", err)
	}
	if got != want {
		t.Fatalf("%s resolves to %s, not to the shipped registry at %s", link, got, want)
	}
	return link
}
