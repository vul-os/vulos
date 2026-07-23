package webhost

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// tarEntry is one record to pack into a test archive. Zero-value Typeflag means
// a regular file; set Linkname for symlink/hardlink entries.
type tarEntry struct {
	Typeflag byte
	Name     string
	Linkname string
	Body     string
}

// makeTarGz builds an in-memory tar.gz from entries. It writes the raw headers
// verbatim (including hostile names) so extraction sees exactly what a malicious
// bundle would carry.
func makeTarGz(t *testing.T, entries ...tarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		typ := e.Typeflag
		if typ == 0 {
			typ = tar.TypeReg
		}
		hdr := &tar.Header{
			Typeflag: typ,
			Name:     e.Name,
			Linkname: e.Linkname,
			Mode:     0o644,
			Size:     int64(len(e.Body)),
		}
		if typ == tar.TypeDir {
			hdr.Mode = 0o755
			hdr.Size = 0
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("write header %q: %v", e.Name, err)
		}
		if typ == tar.TypeReg && len(e.Body) > 0 {
			if _, err := tw.Write([]byte(e.Body)); err != nil {
				t.Fatalf("write body %q: %v", e.Name, err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
	return buf.Bytes()
}

// countTree returns every regular-file path found anywhere under dir. Used to
// prove a rejected deploy left no stray writes.
func countTree(t *testing.T, dir string) []string {
	t.Helper()
	var files []string
	_ = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.Mode().IsRegular() {
			files = append(files, p)
		}
		return nil
	})
	return files
}

// TestDeploy_ExtractsValidBundle is the happy path: a well-formed archive lands
// under <root>/<user>/<site>/htdocs and is reported by GetSite/ListSites.
func TestDeploy_ExtractsValidBundle(t *testing.T) {
	root := t.TempDir()
	svc := New(root)

	bundle := makeTarGz(t,
		tarEntry{Name: "index.html", Body: "<!doctype html><title>home</title>"},
		tarEntry{Typeflag: tar.TypeDir, Name: "assets"},
		tarEntry{Name: "assets/app.js", Body: "console.log(1)"},
		tarEntry{Name: "about/index.html", Body: "<h1>about</h1>"},
	)
	if err := svc.Deploy("userA", "blog", bytes.NewReader(bundle)); err != nil {
		t.Fatalf("Deploy: %v", err)
	}

	htdocs := filepath.Join(root, "userA", "blog", "htdocs")
	for _, rel := range []string{"index.html", "assets/app.js", "about/index.html"} {
		if _, err := os.Stat(filepath.Join(htdocs, filepath.FromSlash(rel))); err != nil {
			t.Errorf("expected %s extracted: %v", rel, err)
		}
	}

	site, err := svc.GetSite("userA", "blog")
	if err != nil {
		t.Fatalf("GetSite: %v", err)
	}
	if site.Bytes <= 0 {
		t.Errorf("expected non-zero Bytes, got %d", site.Bytes)
	}
	sites, err := svc.ListSites("userA")
	if err != nil {
		t.Fatalf("ListSites: %v", err)
	}
	if len(sites) != 1 || sites[0].Name != "blog" {
		t.Fatalf("expected one site 'blog', got %+v", sites)
	}

	// meta.json must live ABOVE htdocs (structurally unreachable by the server).
	if _, err := os.Stat(filepath.Join(root, "userA", "blog", "meta.json")); err != nil {
		t.Errorf("meta.json should sit beside htdocs: %v", err)
	}
}

// TestDeploy_RejectsTraversalEntry blocks a "../../etc/passwd" tar entry: the
// deploy fails with ErrUnsafeEntry and nothing is written outside the site root.
func TestDeploy_RejectsTraversalEntry(t *testing.T) {
	root := t.TempDir()
	// A canary tree outside root that an escape would clobber.
	outside := t.TempDir()
	svc := New(root)

	bundle := makeTarGz(t,
		tarEntry{Name: "index.html", Body: "ok"},
		tarEntry{Name: "../../../../../../../../etc/passwd", Body: "PWNED"},
	)
	err := svc.Deploy("userA", "blog", bytes.NewReader(bundle))
	if !errors.Is(err, ErrUnsafeEntry) {
		t.Fatalf("expected ErrUnsafeEntry, got %v", err)
	}

	// The live site must not exist (failed deploy is never swapped in).
	if _, err := os.Stat(filepath.Join(root, "userA", "blog", "htdocs")); !os.IsNotExist(err) {
		t.Errorf("failed deploy left an htdocs behind: %v", err)
	}
	// Nothing escaped into the sibling canary tree.
	if leaked := countTree(t, outside); len(leaked) != 0 {
		t.Errorf("traversal escaped into outside tree: %v", leaked)
	}
	// And no "PWNED" byte was written anywhere under the whole test root.
	for _, p := range countTree(t, root) {
		b, _ := os.ReadFile(p)
		if strings.Contains(string(b), "PWNED") {
			t.Errorf("hostile payload written at %s", p)
		}
	}
}

// TestDeploy_RejectsAbsolutePathEntry blocks a tar entry with a leading-slash
// absolute name.
func TestDeploy_RejectsAbsolutePathEntry(t *testing.T) {
	root := t.TempDir()
	svc := New(root)

	bundle := makeTarGz(t,
		tarEntry{Name: "/etc/cron.d/pwn", Body: "* * * * * root sh"},
	)
	err := svc.Deploy("userA", "blog", bytes.NewReader(bundle))
	if !errors.Is(err, ErrUnsafeEntry) {
		t.Fatalf("expected ErrUnsafeEntry for absolute path, got %v", err)
	}
	if _, statErr := os.Stat("/etc/cron.d/pwn"); statErr == nil {
		t.Fatal("absolute-path entry escaped to /etc/cron.d/pwn")
	}
	if _, err := os.Stat(filepath.Join(root, "userA", "blog", "htdocs")); !os.IsNotExist(err) {
		t.Errorf("failed deploy left an htdocs behind: %v", err)
	}
}

// TestDeploy_RejectsSymlinkEntry blocks a symlink tar entry outright. A symlink
// that points outside the root is the classic "extract then follow" escape; the
// extractor refuses the whole bundle rather than materializing the link.
func TestDeploy_RejectsSymlinkEntry(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("top-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	svc := New(root)

	bundle := makeTarGz(t,
		tarEntry{Name: "index.html", Body: "ok"},
		// Symlink named "leak" pointing at the outside secret (absolute escape).
		tarEntry{Typeflag: tar.TypeSymlink, Name: "leak", Linkname: secret},
	)
	err := svc.Deploy("userA", "blog", bytes.NewReader(bundle))
	if !errors.Is(err, ErrUnsafeEntry) {
		t.Fatalf("expected ErrUnsafeEntry for symlink, got %v", err)
	}
	// No symlink was materialized under the (never-committed) htdocs.
	if _, err := os.Lstat(filepath.Join(root, "userA", "blog", "htdocs", "leak")); !os.IsNotExist(err) {
		t.Errorf("symlink was materialized: %v", err)
	}
	// The outside secret is untouched.
	if b, _ := os.ReadFile(secret); string(b) != "top-secret" {
		t.Error("outside secret was modified")
	}
}

// TestDeploy_RejectsSymlinkTraversalDir also blocks a symlink-to-parent-dir,
// the "symlink a directory then write a child through it" variant.
func TestDeploy_RejectsSymlinkTraversalDir(t *testing.T) {
	root := t.TempDir()
	svc := New(root)

	bundle := makeTarGz(t,
		// "escape" -> the parent of the whole web root.
		tarEntry{Typeflag: tar.TypeSymlink, Name: "escape", Linkname: ".."},
		tarEntry{Name: "escape/pwn.txt", Body: "PWNED"},
	)
	err := svc.Deploy("userA", "blog", bytes.NewReader(bundle))
	if !errors.Is(err, ErrUnsafeEntry) {
		t.Fatalf("expected ErrUnsafeEntry for symlink dir, got %v", err)
	}
	for _, p := range countTree(t, root) {
		b, _ := os.ReadFile(p)
		if strings.Contains(string(b), "PWNED") {
			t.Errorf("hostile payload written at %s", p)
		}
	}
}

// TestDeploy_RejectsHardlinkEntry blocks a hardlink tar entry (another
// non-regular type that could point at an existing file outside the tree).
func TestDeploy_RejectsHardlinkEntry(t *testing.T) {
	root := t.TempDir()
	svc := New(root)

	bundle := makeTarGz(t,
		tarEntry{Typeflag: tar.TypeLink, Name: "hard", Linkname: "/etc/passwd"},
	)
	if err := svc.Deploy("userA", "blog", bytes.NewReader(bundle)); !errors.Is(err, ErrUnsafeEntry) {
		t.Fatalf("expected ErrUnsafeEntry for hardlink, got %v", err)
	}
}

// TestDeploy_EnforcesByteCap trips the decompressed-size guard: an archive whose
// contents exceed the configured byte ceiling is refused with ErrBundleTooLarge,
// bounding disk use regardless of the (untrusted) header Size.
func TestDeploy_EnforcesByteCap(t *testing.T) {
	root := t.TempDir()
	svc := New(root, WithMaxBundleBytes(1024)) // 1 KiB budget

	big := strings.Repeat("A", 4096) // 4 KiB payload > cap
	bundle := makeTarGz(t,
		tarEntry{Name: "index.html", Body: "ok"},
		tarEntry{Name: "huge.bin", Body: big},
	)
	if err := svc.Deploy("userA", "blog", bytes.NewReader(bundle)); !errors.Is(err, ErrBundleTooLarge) {
		t.Fatalf("expected ErrBundleTooLarge, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "userA", "blog", "htdocs")); !os.IsNotExist(err) {
		t.Errorf("over-cap deploy left an htdocs behind: %v", err)
	}
}

// TestDeploy_EnforcesFileCountCap trips the inode-exhaustion guard: an archive
// with more entries than the file-count ceiling is refused.
func TestDeploy_EnforcesFileCountCap(t *testing.T) {
	root := t.TempDir()
	svc := New(root, WithMaxFiles(3))

	entries := make([]tarEntry, 0, 10)
	for i := 0; i < 10; i++ {
		entries = append(entries, tarEntry{
			Name: filepath.Join("f", strings.Repeat("x", i+1)+".txt"),
			Body: "y",
		})
	}
	if err := svc.Deploy("userA", "blog", bytes.NewReader(makeTarGz(t, entries...))); !errors.Is(err, ErrBundleTooLarge) {
		t.Fatalf("expected ErrBundleTooLarge on file-count cap, got %v", err)
	}
}

// TestDeploy_RejectsNonGzip refuses a body that is not a gzip stream at all.
func TestDeploy_RejectsNonGzip(t *testing.T) {
	root := t.TempDir()
	svc := New(root)
	if err := svc.Deploy("userA", "blog", strings.NewReader("not a gzip")); err == nil {
		t.Fatal("expected error for non-gzip body")
	}
}

// TestDeploy_RejectsInvalidNames validates the identifier gate before anything
// touches the filesystem.
func TestDeploy_RejectsInvalidNames(t *testing.T) {
	root := t.TempDir()
	svc := New(root)
	good := makeTarGz(t, tarEntry{Name: "index.html", Body: "ok"})

	if err := svc.Deploy("bad user!", "blog", bytes.NewReader(good)); !errors.Is(err, ErrInvalidUser) {
		t.Errorf("expected ErrInvalidUser, got %v", err)
	}
	if err := svc.Deploy("../evil", "blog", bytes.NewReader(good)); !errors.Is(err, ErrInvalidUser) {
		t.Errorf("expected ErrInvalidUser for traversal id, got %v", err)
	}
	if err := svc.Deploy("userA", "../../etc", bytes.NewReader(good)); !errors.Is(err, ErrInvalidSite) {
		t.Errorf("expected ErrInvalidSite for traversal site, got %v", err)
	}
	if err := svc.Deploy("userA", "Bad_Site", bytes.NewReader(good)); !errors.Is(err, ErrInvalidSite) {
		t.Errorf("expected ErrInvalidSite for uppercase/underscore, got %v", err)
	}
}

// TestUserIsolation proves one owner cannot see or delete another owner's site:
// scoping is by the auth-stamped user id, never by client-supplied path.
func TestUserIsolation(t *testing.T) {
	root := t.TempDir()
	svc := New(root)

	bundle := makeTarGz(t, tarEntry{Name: "index.html", Body: "userB private"})
	if err := svc.Deploy("userB", "secret", bytes.NewReader(bundle)); err != nil {
		t.Fatalf("Deploy userB: %v", err)
	}

	// userA sees nothing.
	sites, err := svc.ListSites("userA")
	if err != nil {
		t.Fatalf("ListSites userA: %v", err)
	}
	if len(sites) != 0 {
		t.Errorf("userA saw userB's sites: %+v", sites)
	}

	// userA cannot GetSite userB's site.
	if _, err := svc.GetSite("userA", "secret"); !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound cross-user GetSite, got %v", err)
	}

	// userA cannot delete userB's site...
	if err := svc.DeleteSite("userA", "secret"); !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound cross-user DeleteSite, got %v", err)
	}
	// ...and userB's site is still intact on disk.
	if _, err := os.Stat(filepath.Join(root, "userB", "secret", "htdocs", "index.html")); err != nil {
		t.Errorf("userB's site was damaged by userA's delete attempt: %v", err)
	}

	// userB can, of course, delete its own.
	if err := svc.DeleteSite("userB", "secret"); err != nil {
		t.Errorf("owner delete failed: %v", err)
	}
}

// TestDeploy_PreservesDomainsAcrossRedeploy is a light functional check that a
// redeploy replaces files but keeps the metadata record present.
func TestDeploy_PreservesDomainsAcrossRedeploy(t *testing.T) {
	root := t.TempDir()
	svc := New(root)

	if err := svc.Deploy("userA", "blog", bytes.NewReader(makeTarGz(t,
		tarEntry{Name: "index.html", Body: "v1"}))); err != nil {
		t.Fatalf("first deploy: %v", err)
	}
	if err := svc.Deploy("userA", "blog", bytes.NewReader(makeTarGz(t,
		tarEntry{Name: "index.html", Body: "v2-longer-body"}))); err != nil {
		t.Fatalf("redeploy: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(root, "userA", "blog", "htdocs", "index.html"))
	if err != nil || string(b) != "v2-longer-body" {
		t.Fatalf("redeploy did not replace content: %q %v", b, err)
	}
}

// sanity: ensure the archive helper actually produces a gzip stream extractTarGz
// can read, so a bad helper never masquerades as a passing security test.
func TestMakeTarGz_IsReadable(t *testing.T) {
	data := makeTarGz(t, tarEntry{Name: "a.txt", Body: "hi"})
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("helper produced non-gzip: %v", err)
	}
	if _, err := io.Copy(io.Discard, gz); err != nil {
		t.Fatalf("helper produced unreadable stream: %v", err)
	}
}
