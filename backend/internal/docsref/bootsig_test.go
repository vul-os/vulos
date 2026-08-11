package docsref

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The boot-time signature policy documented in ARCHITECTURE.md must still be
// the one scripts/initramfs/vulos-live implements.
//
// The policy has three outcomes and they are not symmetric:
//
//	verified              — signature present and checked against the pinned anchor
//	unsigned              — no signature: REFUSED on netboot, TOLERATED on local disk
//	present-but-invalid   — fatal on both paths, always
//
// That asymmetry is the single fact that decides what can be booted before any
// release key exists: a --disk image boots unsigned and can be tested end to
// end, a netboot image cannot. Someone planning a first-boot test reads the
// document, not the initramfs hook, so the document has to be right — and it
// documented the "unsigned on this path" half without ever stating which path
// refuses.
//
// Pinning it in both directions matters more than usual because the code is a
// shell script in an initramfs. It is not compiled, not vetted, and not covered
// by any Go test; nothing else in this repository would notice the netboot
// branch being softened to a warning.

const (
	archDoc    = "docs/ARCHITECTURE.md"
	initramfsH = "scripts/initramfs/vulos-live"
)

func readRepoFile(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRoot, rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	if len(b) < 500 {
		t.Fatalf("%s is %d bytes; too short to be the file this check describes", rel, len(b))
	}
	return string(b)
}

func TestNetbootRefusesAnUnsignedRoothash(t *testing.T) {
	hook := readRepoFile(t, initramfsH)

	// The netboot branch must PANIC. A panic in an initramfs halts the boot;
	// anything less (a warning, a log line, a fallback) is a fail-open on the
	// one path where the payload crosses a network an attacker may control.
	idx := strings.Index(hook, `elif [ "$NETBOOT" = "1" ]; then`)
	if idx < 0 {
		t.Fatalf("%s no longer has a netboot branch on the unsigned path; the policy "+
			"ARCHITECTURE.md describes cannot be in force", initramfsH)
	}
	// Look only at the branch body, not the whole file, so a panic elsewhere
	// cannot satisfy this.
	body := hook[idx:]
	if end := strings.Index(body[10:], "\nfi\n"); end > 0 {
		body = body[:end+10]
	}
	if !strings.Contains(body, "panic ") {
		t.Errorf("the netboot branch for an unsigned roothash does not panic. An "+
			"initramfs that continues here boots an unauthenticated image off the "+
			"network. Branch body:\n%s", body)
	}
}

func TestLocalDiskToleratesAnUnsignedRoothashAndRecordsIt(t *testing.T) {
	hook := readRepoFile(t, initramfsH)

	// The tolerated case must still be RECORDED rather than silently accepted —
	// an operator has to be able to tell "verified" from "nobody checked".
	if !strings.Contains(hook, `ROOTHASH_SIG_STATE="unsigned"`) {
		t.Errorf("%s no longer records sig=unsigned. Tolerating a missing signature is "+
			"defensible only while the boot says that is what happened", initramfsH)
	}
	if !strings.Contains(hook, `ROOTHASH_SIG_STATE="verified"`) {
		t.Errorf("%s no longer records sig=verified, so the two cases are "+
			"indistinguishable from outside", initramfsH)
	}
}

// The document must state the asymmetry, not merely that signatures exist. It
// previously said "the roothash is unsigned on this path" — true, and silent
// about which path refuses, which is the part a reader needs.
func TestArchitectureDocumentsTheBootSignatureAsymmetry(t *testing.T) {
	// Collapse whitespace before matching: markdown wraps, so the phrase this
	// check is looking for legitimately spans a line break in the source.
	doc := strings.Join(strings.Fields(readRepoFile(t, archDoc)), " ")

	for _, want := range []struct{ text, why string }{
		{"FATAL ON NETBOOT", "the netboot half of the policy"},
		{"TOLERATED ON A LOCAL DISK", "the local-disk half of the policy"},
		{"sig=unsigned", "the state a reader will actually see in a boot log"},
	} {
		if !strings.Contains(doc, want.text) {
			t.Errorf("%s does not mention %q — %s is undocumented, and it is what "+
				"decides whether a build can be booted before a release key exists",
				archDoc, want.text, want.why)
		}
	}
}
