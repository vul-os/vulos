package appnet

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ─────────────────────────────────────────────────────────────────────────────
// PAYLOAD-01 — "unpacked nothing, reported success" was drawio's shipped
// behaviour, and adding `.war` to the zip list fixed drawio without fixing the
// SHAPE: any archive whose extension the installer does not recognise still
// falls through to the single-binary branch. These tests exercise the shape,
// not the extension.
//
// Every expected value below is transcribed from the format's own specification
// (RFC 1952 §2.3.1 for gzip, APPNOTE.TXT §4.3.16 for zip, POSIX.1-1988 for
// tar's `ustar` at offset 257), never read back from archiveMagics — pinning a
// table to itself proves the table equals the table.
// ─────────────────────────────────────────────────────────────────────────────

// gzipBytes returns a real gzip stream, produced by the standard library rather
// than by hand-writing a magic number.
func gzipBytes(t *testing.T, payload string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write([]byte(payload)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// plainTarBytes returns an UNCOMPRESSED tar, whose magic sits 257 bytes in —
// the case a sniffer that only reads a 4-byte prefix silently misses.
func plainTarBytes(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	body := []byte("hello")
	if err := tw.WriteHeader(&tar.Header{Name: "index.html", Mode: 0o644, Size: int64(len(body))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// installPayload serves body at a URL with the given path and installs it.
func installPayload(t *testing.T, urlPath string, body []byte, binaryName string) (string, error) {
	t.Helper()
	withInsecureRegistry(t)
	t.Setenv("VULOS_DATA_DIR", t.TempDir())

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(body)
	}))
	defer srv.Close()

	appsDir := t.TempDir()
	reg := &Registry{Apps: map[string]*RegistryEntry{
		"sniffed": {
			Name: "Sniffed", Vetted: true, Type: "web",
			Description: "payload sniff fixture", Category: "developer",
			Icon: "S", Author: "Test", License: "MIT", Homepage: "https://example.com",
			Versions: map[string]*VersionRecipe{
				"1.0": {
					Artifacts: map[string]*Artifact{
						"any": {DownloadURL: srv.URL + urlPath, Checksum: sha256Hex(body)},
					},
					BinaryName: binaryName,
					Command:    "bin/" + binaryName,
					Port:       8080,
				},
			},
		},
	}}
	err := InstallFromRegistry(context.Background(), reg, "sniffed", "1.0", appsDir)
	return filepath.Join(appsDir, "sniffed"), err
}

// TestArchiveWithAnUnknownExtensionIsRefused_NotInstalledAsABinary is drawio's
// defect in its general form: the payload is an archive, the URL extension is
// one the installer does not unpack, and the old behaviour was chmod 0755 plus
// a success report.
func TestArchiveWithAnUnknownExtensionIsRefused_NotInstalledAsABinary(t *testing.T) {
	// .tar.zst is not in tarExtensions and not in zipExtensions today. The
	// bytes here are gzip, so the test does not depend on zstd support existing;
	// what it depends on is the EXTENSION being unrecognised, which is the
	// condition that routes a payload to the single-binary branch.
	appDir, err := installPayload(t, "/payload.tar.zst", gzipBytes(t, "site files"), "payload")
	if err == nil {
		t.Fatal("install of an ARCHIVE through the single-binary branch reported SUCCESS — " +
			"that is drawio's shipped defect: bin/<file> chmod 0755, nothing unpacked, " +
			"and an app with no files (PAYLOAD-01)")
	}
	if !strings.Contains(err.Error(), "PAYLOAD-01") {
		t.Errorf("install failed for some other reason than the payload sniff: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(appDir, "bin", "payload")); statErr == nil {
		t.Error("the archive was still installed into bin/ — the refusal must happen before " +
			"the file is placed, or the app directory holds a 0755 archive")
	}
}

// TestUncompressedTarIsRefused covers the format whose magic is 257 bytes in.
// A sniffer that reads only a short prefix returns "not an archive" here and
// installs a tar as an executable, which is the same silent success wearing a
// different extension.
func TestUncompressedTarIsRefused(t *testing.T) {
	_, err := installPayload(t, "/bundle.tar", plainTarBytes(t), "bundle")
	if err == nil {
		t.Fatal("an uncompressed .tar was installed as a single executable (PAYLOAD-01)")
	}
	if !strings.Contains(err.Error(), "PAYLOAD-01") {
		t.Errorf("refused, but not by the payload sniff: %v", err)
	}
}

// TestRealBinaryStillInstalls is the CONTROL, and without it every test above
// would also pass against a guard that refused every single-binary install —
// which is how a "correct" rule and a paranoid one are told apart. conduit,
// gitea and minio are single-binary recipes; breaking them to close drawio's
// hole would be a worse trade than the hole.
func TestRealBinaryStillInstalls(t *testing.T) {
	// An ELF header: 0x7f "ELF", 64-bit, little-endian, ET_EXEC, EM_X86_64.
	// Transcribed from the ELF specification, not produced by this package.
	elf := append([]byte{0x7f, 'E', 'L', 'F', 2, 1, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0,
		2, 0, 0x3e, 0}, bytes.Repeat([]byte{0}, 300)...)

	appDir, err := installPayload(t, "/server-linux", elf, "server")
	if err != nil {
		t.Fatalf("a real binary was refused: %v", err)
	}
	st, statErr := os.Stat(filepath.Join(appDir, "bin", "server"))
	if statErr != nil {
		t.Fatalf("binary not installed at bin/server: %v", statErr)
	}
	if st.Mode().Perm()&0o111 == 0 {
		t.Errorf("bin/server is not executable (mode %v)", st.Mode().Perm())
	}
}

// TestSniffKnowsEachContainerFormat pins the table itself against literals
// transcribed from the format specifications. A magic number that drifts by one
// byte would otherwise show up only as an install that quietly succeeded.
func TestSniffKnowsEachContainerFormat(t *testing.T) {
	cases := []struct {
		what  string
		bytes []byte
	}{
		{"gzip", []byte{0x1f, 0x8b, 0x08, 0x00}},
		{"zip", []byte{'P', 'K', 0x03, 0x04}},
		{"xz", []byte{0xfd, '7', 'z', 'X', 'Z', 0x00}},
		{"bzip2", []byte{'B', 'Z', 'h', '9'}},
		{"zstd", []byte{0x28, 0xb5, 0x2f, 0xfd}},
		{"7z", []byte{'7', 'z', 0xbc, 0xaf, 0x27, 0x1c}},
		{"deb-ar", []byte("!<arch>\ndebian-binary")},
	}
	dir := t.TempDir()
	for _, c := range cases {
		p := filepath.Join(dir, c.what)
		if err := os.WriteFile(p, c.bytes, 0o644); err != nil {
			t.Fatal(err)
		}
		if got := sniffArchiveFormat(p); got == "" {
			t.Errorf("%s payload was not recognised as an archive — it would be installed as an "+
				"executable and unpack nothing", c.what)
		}
	}
	// The negative half: bytes that are NOT a container must stay installable.
	notArchive := filepath.Join(dir, "script")
	if err := os.WriteFile(notArchive, []byte("#!/bin/sh\nexec ./thing\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := sniffArchiveFormat(notArchive); got != "" {
		t.Errorf("a shell script was classified as %q — every single-binary recipe would break", got)
	}
}
