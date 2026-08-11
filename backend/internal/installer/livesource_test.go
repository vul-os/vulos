//go:build linux

package installer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The installer defaulted to /run/live/medium/vulos/os-core.squashfs — a path
// nothing on a Vulos live image creates. build.sh writes the OS to
// /image.squashfs on the partition labelled VULOS-LIVE-DATA, and that is the
// only file on it. So the route the README calls primary could not read the
// image it exists to copy, even once the binary shipped.
//
// These pin the two properties that matter, both of which are about what a
// person standing at a just-wiped machine is told:
//
//  1. a real image IS found, wherever it legitimately sits;
//  2. when it is not, the error says what was tried — "not found" with no list
//     is the least actionable message available.

// withCandidates swaps the search list for the duration of a test. The real
// paths are absolute and root-owned, so a test that used them would either need
// root or would silently assert nothing.
func withCandidates(t *testing.T, paths []string) {
	t.Helper()
	orig := squashfsCandidatesFn
	squashfsCandidatesFn = func() []string { return paths }
	t.Cleanup(func() { squashfsCandidatesFn = orig })
}

func TestResolveSquashfs_FindsARealImage(t *testing.T) {
	dir := t.TempDir()
	img := filepath.Join(dir, "image.squashfs")
	if err := os.WriteFile(img, []byte("not really a squashfs, but non-empty"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A path that does not exist comes FIRST, so this also proves the search
	// continues past a miss rather than stopping at the first candidate.
	withCandidates(t, []string{filepath.Join(dir, "nope.squashfs"), img})

	got, why, err := resolveSquashfs()
	if err != nil {
		t.Fatalf("resolveSquashfs() = %v, want the image at %s", err, img)
	}
	if got != img {
		t.Errorf("resolved %q, want %q", got, img)
	}
	if why == "" {
		t.Error("no reason given for the choice; the installer logs it, and a blank line helps nobody")
	}
}

func TestResolveSquashfs_IgnoresAnEmptyFile(t *testing.T) {
	// A zero-byte file at the right path is a truncated download or a failed
	// copy. Treating it as the image would fail later, during the install,
	// after the target disk has already been repartitioned.
	dir := t.TempDir()
	empty := filepath.Join(dir, "image.squashfs")
	if err := os.WriteFile(empty, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	withCandidates(t, []string{empty})

	if got, _, err := resolveSquashfs(); err == nil {
		t.Fatalf("resolveSquashfs() accepted a zero-byte file at %q — the install would fail after wiping the disk", got)
	}
}

func TestResolveSquashfs_IgnoresADirectory(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "image.squashfs")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	withCandidates(t, []string{sub})

	if _, _, err := resolveSquashfs(); err == nil {
		t.Fatal("resolveSquashfs() accepted a directory as the OS image")
	}
}

func TestResolveSquashfs_ErrorNamesEveryPathTried(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "one.squashfs")
	b := filepath.Join(dir, "two.squashfs")
	withCandidates(t, []string{a, b})

	_, _, err := resolveSquashfs()
	if err == nil {
		t.Fatal("expected an error when no candidate exists")
	}
	for _, want := range []string{a, b} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not name %q, so a user cannot tell where it looked:\n%v", want, err)
		}
	}
	if !strings.Contains(err.Error(), liveDataLabel) {
		t.Errorf("error does not mention the %s label, which is the one identifier that does not vary:\n%v", liveDataLabel, err)
	}
}
