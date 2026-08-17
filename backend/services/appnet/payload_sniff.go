package appnet

import (
	"bytes"
	"fmt"
	"os"
)

// ─── PAYLOAD-01: the payload must be the KIND of thing the URL claims ────────
//
// THE DEFECT THIS CLOSES, and it is the one that motivated the whole standard.
// staticInstall classifies a download by URL EXTENSION: tarExtensions → tar,
// zipExtensions → extractZip, and everything else falls through to "it must be
// a single binary, install it at bin/<name> and chmod 0755". That fallthrough
// has no way to be wrong out loud. drawio's `draw.war` matched neither list, so
// a 52 MB ZIP was copied to bin/draw.war, marked executable, and the installer
// REPORTED SUCCESS while static/ stayed empty and the app served nothing.
//
// `.war` is now a zip extension, which fixes drawio. It does NOT fix the shape:
// `.tar.zst`, `.7z`, `.tar`, `.deb`, `.jar`, `.tar.lz4` and any extension
// upstream invents next all still land in the same branch, and each of them
// would produce the same silent success. Extending the list one extension per
// incident is how this defect survived twice already.
//
// So the check is on the BYTES, which cannot be spelled wrongly: if the payload
// that arrived is a known archive container and the installer is about to treat
// it as an executable, the install fails and names both facts. A real binary
// (ELF, a script, a Mach-O) is unaffected, because no archive magic matches it.
//
// It runs after the checksum check on purpose. The bytes are already proven to
// be the bytes the signed entry pinned; this asks a different question — whether
// the recipe's SHAPE matches them — and answering it before verifying integrity
// would mean reasoning about unverified input.

// archiveMagic is one container format's identifying prefix.
type archiveMagic struct {
	name   string
	offset int
	magic  []byte
}

// archiveMagics are the container formats that must never be installed as a
// single executable. Each is the format's own documented signature, transcribed
// from the format specification rather than derived from any list in this repo:
// a sniffer built out of tarExtensions would go blind at exactly the moment
// tarExtensions was wrong, which is the case it exists to catch.
var archiveMagics = []archiveMagic{
	{"gzip (.tar.gz/.tgz)", 0, []byte{0x1f, 0x8b}},
	{"zip (.zip/.war/.jar)", 0, []byte("PK\x03\x04")},
	{"zip, empty archive", 0, []byte("PK\x05\x06")},
	{"zip, spanned archive", 0, []byte("PK\x07\x08")},
	{"xz (.tar.xz)", 0, []byte{0xfd, '7', 'z', 'X', 'Z', 0x00}},
	{"bzip2 (.tar.bz2)", 0, []byte("BZh")},
	{"zstd (.tar.zst)", 0, []byte{0x28, 0xb5, 0x2f, 0xfd}},
	{"7-zip (.7z)", 0, []byte{'7', 'z', 0xbc, 0xaf, 0x27, 0x1c}},
	{"ar archive (.deb)", 0, []byte("!<arch>\n")},
	{"rar", 0, []byte("Rar!\x1a\x07")},
	// POSIX tar keeps its magic 257 bytes in, which is why an uncompressed
	// `.tar` looks like arbitrary bytes to anything that only reads a prefix.
	{"tar (uncompressed)", 257, []byte("ustar")},
}

// sniffArchiveFormat returns the name of the archive container the file holds,
// or "" when the bytes are not a container this knows.
//
// A short or unreadable file is NOT an archive as far as this is concerned:
// the caller is deciding whether to refuse, and inventing a refusal out of a
// read error would fail installs for a reason that is not the subject.
func sniffArchiveFormat(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	head := make([]byte, 512)
	n, _ := f.Read(head)
	head = head[:n]
	for _, m := range archiveMagics {
		end := m.offset + len(m.magic)
		if len(head) < end {
			continue
		}
		if bytes.Equal(head[m.offset:end], m.magic) {
			return m.name
		}
	}
	return ""
}

// refuseArchiveInstalledAsBinary is the guard staticInstall's single-binary
// branch calls before it renames a download into bin/.
func refuseArchiveInstalledAsBinary(tmpPath, url, dest string) error {
	format := sniffArchiveFormat(tmpPath)
	if format == "" {
		return nil
	}
	return fmt.Errorf("refusing to install %s as a single executable: its bytes are a %s, "+
		"but its URL names no archive extension this installer unpacks, so it would land at %s "+
		"with mode 0755 and NOTHING would be unpacked — the install would report success and the "+
		"app would have no files (PAYLOAD-01, roadmap/INSTALL-METHODOLOGY.md). That is exactly what "+
		"drawio's draw.war did. Publish the artefact under an extension the installer unpacks "+
		"(.tar.gz, .tgz, .tar.bz2, .tar.xz, .zip, .war), or pin the single binary the app really ships",
		url, format, dest)
}
