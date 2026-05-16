package appnet

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// buildTarGz creates an in-memory .tar.gz from the given entries.
// Each entry is (name, content, typeflag, linkname).
func buildTarGz(t *testing.T, entries []tarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gzw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gzw)

	for _, e := range entries {
		hdr := &tar.Header{
			Name:     e.name,
			Typeflag: e.typeflag,
			Linkname: e.linkname,
		}
		if e.typeflag == tar.TypeReg || e.typeflag == tar.TypeRegA {
			hdr.Size = int64(len(e.content))
			hdr.Mode = 0644
		} else if e.typeflag == tar.TypeDir {
			hdr.Mode = 0755
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("write tar header: %v", err)
		}
		if len(e.content) > 0 {
			if _, err := tw.Write(e.content); err != nil {
				t.Fatalf("write tar content: %v", err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := gzw.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
	return buf.Bytes()
}

type tarEntry struct {
	name     string
	typeflag byte
	content  []byte
	linkname string
}

func writeTempTarGz(t *testing.T, data []byte) string {
	t.Helper()
	f, err := os.CreateTemp("", "test-*.tar.gz")
	if err != nil {
		t.Fatalf("create temp: %v", err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		t.Fatalf("write temp: %v", err)
	}
	f.Close()
	t.Cleanup(func() { os.Remove(f.Name()) })
	return f.Name()
}

// ---------------------------------------------------------------------------
// ID validation tests
// ---------------------------------------------------------------------------

func TestInstallIDValidation(t *testing.T) {
	cases := []struct {
		id   string
		want bool // true = valid
	}{
		{"my-app", true},
		{"myapp123", true},
		{"a", true},
		{"a" + strings.Repeat("b", 63), true},  // 64 chars total
		{"a" + strings.Repeat("b", 64), false}, // 65 chars — too long
		{"", false},                            // empty
		{"My-App", false},                      // uppercase
		{"-myapp", false},                      // leading dash
		{"../etc/passwd", false},               // path traversal
		{"../../etc", false},                   // path traversal
		{"app.zip", false},                     // dot in name
		{"app/name", false},                    // slash
		{"app name", false},                    // space
		{"app\x00name", false},                 // null byte
	}
	for _, tc := range cases {
		got := installIDPattern.MatchString(tc.id)
		if got != tc.want {
			t.Errorf("installIDPattern.MatchString(%q) = %v, want %v", tc.id, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// safeExtractTarGz — malicious archive tests
// ---------------------------------------------------------------------------

func TestSafeExtractTarGz_PathTraversal(t *testing.T) {
	destDir := t.TempDir()

	data := buildTarGz(t, []tarEntry{
		{name: "../../etc/passwd", typeflag: tar.TypeReg, content: []byte("evil")},
	})
	archivePath := writeTempTarGz(t, data)

	err := safeExtractTarGz(archivePath, destDir)
	if err == nil {
		t.Fatal("expected error for '../..' path traversal, got nil")
	}
}

func TestSafeExtractTarGz_AbsolutePath(t *testing.T) {
	destDir := t.TempDir()

	data := buildTarGz(t, []tarEntry{
		{name: "/etc/passwd", typeflag: tar.TypeReg, content: []byte("evil")},
	})
	archivePath := writeTempTarGz(t, data)

	err := safeExtractTarGz(archivePath, destDir)
	if err == nil {
		t.Fatal("expected error for absolute path /etc/passwd, got nil")
	}
}

func TestSafeExtractTarGz_SymlinkEscape(t *testing.T) {
	destDir := t.TempDir()

	// A symlink that points outside the extraction root.
	data := buildTarGz(t, []tarEntry{
		{name: "evil-link", typeflag: tar.TypeSymlink, linkname: "../../etc/shadow"},
	})
	archivePath := writeTempTarGz(t, data)

	err := safeExtractTarGz(archivePath, destDir)
	if err == nil {
		t.Fatal("expected error for symlink escape, got nil")
	}
}

func TestSafeExtractTarGz_HardlinkRejected(t *testing.T) {
	destDir := t.TempDir()

	data := buildTarGz(t, []tarEntry{
		{name: "hardlink", typeflag: tar.TypeLink, linkname: "/etc/passwd"},
	})
	archivePath := writeTempTarGz(t, data)

	err := safeExtractTarGz(archivePath, destDir)
	if err == nil {
		t.Fatal("expected error for hardlink, got nil")
	}
}

func TestSafeExtractTarGz_DotDotInSubdir(t *testing.T) {
	destDir := t.TempDir()

	// Disguised traversal: subdir/../../etc/passwd
	data := buildTarGz(t, []tarEntry{
		{name: "subdir/../../etc/passwd", typeflag: tar.TypeReg, content: []byte("evil")},
	})
	archivePath := writeTempTarGz(t, data)

	err := safeExtractTarGz(archivePath, destDir)
	if err == nil {
		t.Fatal("expected error for 'subdir/../../etc/passwd' traversal, got nil")
	}
}

// ---------------------------------------------------------------------------
// safeExtractTarGz — happy path
// ---------------------------------------------------------------------------

func TestSafeExtractTarGz_HappyPath(t *testing.T) {
	destDir := t.TempDir()

	const fileContent = "hello vulos"
	data := buildTarGz(t, []tarEntry{
		{name: "subdir/", typeflag: tar.TypeDir},
		{name: "subdir/hello.txt", typeflag: tar.TypeReg, content: []byte(fileContent)},
		{name: "app.json", typeflag: tar.TypeReg, content: []byte(`{"id":"my-app"}`)},
	})
	archivePath := writeTempTarGz(t, data)

	if err := safeExtractTarGz(archivePath, destDir); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify extracted files exist and contain the right data.
	gotBytes, err := os.ReadFile(filepath.Join(destDir, "subdir", "hello.txt"))
	if err != nil {
		t.Fatalf("read extracted file: %v", err)
	}
	if string(gotBytes) != fileContent {
		t.Errorf("extracted content = %q, want %q", gotBytes, fileContent)
	}
	if _, err := os.Stat(filepath.Join(destDir, "app.json")); err != nil {
		t.Errorf("app.json not extracted: %v", err)
	}

	// Verify no files escaped the destDir.
	if _, err := os.Stat("/etc/passwd_evil"); err == nil {
		t.Error("evil file appeared at /etc/passwd_evil — containment failed!")
	}
}

// ---------------------------------------------------------------------------
// verifySHA256 tests
// ---------------------------------------------------------------------------

func TestVerifySHA256_Match(t *testing.T) {
	data := []byte("vulos security test data")

	f, err := os.CreateTemp("", "sha256test-*")
	if err != nil {
		t.Fatal(err)
	}
	f.Write(data)
	f.Close()
	t.Cleanup(func() { os.Remove(f.Name()) })

	h := sha256.Sum256(data)
	want := hex.EncodeToString(h[:])

	if err := verifySHA256(f.Name(), want); err != nil {
		t.Errorf("expected match, got: %v", err)
	}
}

func TestVerifySHA256_Mismatch(t *testing.T) {
	data := []byte("vulos security test data")

	f, err := os.CreateTemp("", "sha256test-*")
	if err != nil {
		t.Fatal(err)
	}
	f.Write(data)
	f.Close()
	t.Cleanup(func() { os.Remove(f.Name()) })

	wrongChecksum := strings.Repeat("de", 32) // 64 hex chars, wrong value

	err = verifySHA256(f.Name(), wrongChecksum)
	if err == nil {
		t.Error("expected checksum mismatch error, got nil")
	}
}

func TestVerifySHA256_UppercaseHex(t *testing.T) {
	data := []byte("case insensitive checksum")

	f, err := os.CreateTemp("", "sha256test-*")
	if err != nil {
		t.Fatal(err)
	}
	f.Write(data)
	f.Close()
	t.Cleanup(func() { os.Remove(f.Name()) })

	h := sha256.Sum256(data)
	want := strings.ToUpper(hex.EncodeToString(h[:]))

	if err := verifySHA256(f.Name(), want); err != nil {
		t.Errorf("expected uppercase hex to match, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Size cap tests
// ---------------------------------------------------------------------------

func TestSafeExtractTarGz_PerEntrySizeCap(t *testing.T) {
	destDir := t.TempDir()

	// Build an entry claiming to be just over the per-entry cap.
	// We craft the header to declare a size just above the cap, but only write
	// 1 byte of actual data — the LimitedReader + size check guards both cases.
	//
	// To actually trigger the cap without writing 100 MB in a test, we use a
	// temporarily lowered cap via a custom size. Since the constants are package-level,
	// we instead write a genuinely large (but small-enough-for-test) payload.
	//
	// Strategy: write a file of exactly maxExtractEntry+1 bytes to a real tar.
	// That would be 100 MB+1 — too slow for a unit test. Instead, test the
	// header-based check: set hdr.Size > maxExtractEntry and 0 bytes of body.
	// The extractor reads hdr.Size before copying and rejects it.
	bigSize := maxExtractEntry + 1
	var buf bytes.Buffer
	gzw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gzw)
	hdr := &tar.Header{
		Name:     "bigfile",
		Typeflag: tar.TypeReg,
		Size:     bigSize,
		Mode:     0644,
	}
	tw.WriteHeader(hdr)
	// Write zeroes equal to what the header claims.
	zeros := make([]byte, bigSize)
	tw.Write(zeros)
	tw.Close()
	gzw.Close()

	archivePath := writeTempTarGz(t, buf.Bytes())

	err := safeExtractTarGz(archivePath, destDir)
	if err == nil {
		t.Fatal("expected per-entry size cap error, got nil")
	}
	if !strings.Contains(err.Error(), "too large") && !strings.Contains(err.Error(), "exceeded") {
		t.Errorf("error message should mention size cap, got: %v", err)
	}
}

func TestSafeExtractTarGz_NoEscapeAfterExtract(t *testing.T) {
	// Confirm that after a successful extraction, no files exist outside destDir.
	destDir := t.TempDir()

	data := buildTarGz(t, []tarEntry{
		{name: "safe.txt", typeflag: tar.TypeReg, content: []byte("safe content")},
	})
	archivePath := writeTempTarGz(t, data)

	if err := safeExtractTarGz(archivePath, destDir); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Only safe.txt should exist under destDir.
	var found []string
	filepath.Walk(destDir, func(p string, _ os.FileInfo, _ error) error {
		if p != destDir {
			found = append(found, p)
		}
		return nil
	})
	if len(found) != 1 || !strings.HasSuffix(found[0], "safe.txt") {
		t.Errorf("unexpected files after extraction: %v", found)
	}

	// Ensure no file was placed at the io.LimitedReader sentinel path.
	if _, err := os.Stat(filepath.Join(destDir, "..", "escaped")); err == nil {
		t.Error("a file escaped the extraction root")
	}
}

// ---------------------------------------------------------------------------
// Helper: read the extracted bytes through io.ReadFull to confirm LimitedReader
// ---------------------------------------------------------------------------

func TestSafeExtractTarGz_LimitedReaderActualData(t *testing.T) {
	// If the archive's actual body is larger than the declared Size in the header,
	// the LimitedReader must still catch it. We can't test the body > header case
	// directly because archive/tar itself limits reads to header.Size.
	// So we verify the normal path works and the cap does not incorrectly truncate.
	destDir := t.TempDir()

	content := bytes.Repeat([]byte("x"), 1024) // 1 KiB — well within cap
	data := buildTarGz(t, []tarEntry{
		{name: "data.bin", typeflag: tar.TypeReg, content: content},
	})
	archivePath := writeTempTarGz(t, data)

	if err := safeExtractTarGz(archivePath, destDir); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(destDir, "data.bin"))
	if err != nil {
		t.Fatalf("read extracted: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Errorf("extracted content mismatch: got %d bytes, want %d", len(got), len(content))
	}
}

// Ensure io is imported (used in buildTarGz indirectly via archive/tar internals).
var _ = io.EOF
