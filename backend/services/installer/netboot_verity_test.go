package installer

// netboot_verity_test.go — VERITY-03.
//
// These tests exist because the failure mode here is not "the feature does not
// work", it is "the machine does not boot".  scripts/initramfs/vulos-live
// PANICS when `veritysetup open` fails, so every assertion below is really
// about one of three things:
//
//   - a HALF set of artifacts is never assembled or staged,
//   - artifacts are never staged without having been verified first,
//   - a medium whose artifacts do not match aborts the install BEFORE the disk
//     is partitioned.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFile is a t.Fatal-on-error file writer for fixtures.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

const fixtureRootHash = "9f2b7c1d4e5a6b8c0d1e2f3a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e"

// ---------------------------------------------------------------------------
// Artifact resolution
// ---------------------------------------------------------------------------

func TestLocateVerityArtifacts_SiblingsNamedAfterTheImage(t *testing.T) {
	dir := t.TempDir()
	sq := filepath.Join(dir, "os-core.squashfs")
	writeFile(t, sq, "image")
	writeFile(t, filepath.Join(dir, "os-core.hashtree"), "tree")
	writeFile(t, filepath.Join(dir, "os-core.roothash"), fixtureRootHash)

	ht, rh, ok := locateVerityArtifacts(sq)
	if !ok {
		t.Fatal("expected the os-core.* pair beside os-core.squashfs to resolve")
	}
	if filepath.Base(ht) != "os-core.hashtree" || filepath.Base(rh) != "os-core.roothash" {
		t.Fatalf("resolved the wrong pair: %s / %s", ht, rh)
	}
}

// build.sh --live packs the image as image.squashfs but writes the verity
// siblings as os-core.hashtree/os-core.roothash beside it (build.sh VERITY-01).
// A purely base-derived lookup would find nothing there — which is precisely the
// medium the netboot smoke harness installs from.
func TestLocateVerityArtifacts_LiveBuildNamesImageSquashfsButOsCoreSiblings(t *testing.T) {
	dir := t.TempDir()
	sq := filepath.Join(dir, "image.squashfs")
	writeFile(t, sq, "image")
	writeFile(t, filepath.Join(dir, "os-core.hashtree"), "tree")
	writeFile(t, filepath.Join(dir, "os-core.roothash"), fixtureRootHash)

	ht, rh, ok := locateVerityArtifacts(sq)
	if !ok {
		t.Fatal("expected os-core.* siblings of image.squashfs to resolve")
	}
	if filepath.Base(ht) != "os-core.hashtree" || filepath.Base(rh) != "os-core.roothash" {
		t.Fatalf("resolved the wrong pair: %s / %s", ht, rh)
	}
}

// A half set must never resolve.  The hook gates dm-verity on BOTH files being
// present, so a lone hashtree is dead weight — but a lone ROOTHASH paired with
// some other family's hashtree would be an actively dangerous combination, and
// resolving the two files independently is how that would happen.
func TestLocateVerityArtifacts_HalfSetDoesNotResolve(t *testing.T) {
	for _, only := range []string{"os-core.hashtree", "os-core.roothash"} {
		t.Run(only, func(t *testing.T) {
			dir := t.TempDir()
			sq := filepath.Join(dir, "image.squashfs")
			writeFile(t, sq, "image")
			writeFile(t, filepath.Join(dir, only), "x")

			if _, _, ok := locateVerityArtifacts(sq); ok {
				t.Fatalf("a lone %s must not resolve as a verity artifact pair", only)
			}
		})
	}
}

func TestLocateVerityArtifacts_DirectoryIsNotAnArtifact(t *testing.T) {
	dir := t.TempDir()
	sq := filepath.Join(dir, "image.squashfs")
	writeFile(t, sq, "image")
	if err := os.MkdirAll(filepath.Join(dir, "os-core.hashtree"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "os-core.roothash"), fixtureRootHash)

	if _, _, ok := locateVerityArtifacts(sq); ok {
		t.Fatal("a DIRECTORY named os-core.hashtree must not satisfy the pair test")
	}
}

// ---------------------------------------------------------------------------
// Root-hash parsing
// ---------------------------------------------------------------------------

func TestParseRootHash_TrimsWhitespace(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "os-core.roothash")
	writeFile(t, p, "  "+strings.ToUpper(fixtureRootHash)+"\n")

	got, err := parseRootHash(p)
	if err != nil {
		t.Fatalf("parseRootHash: %v", err)
	}
	if got != fixtureRootHash {
		t.Fatalf("got %q, want %q", got, fixtureRootHash)
	}
}

// The exact shape this project already shipped once: gen-verity.sh's coloured
// progress transcript captured into the roothash field.  It must be rejected
// here rather than handed to veritysetup (or, worse, to the kernel).
func TestParseRootHash_RejectsAnsiTranscript(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "os-core.roothash")
	writeFile(t, p, "\033[0;34m  ▸ [gen-verity] Running veritysetup format ...\033[0m\n"+fixtureRootHash)

	if _, err := parseRootHash(p); err == nil {
		t.Fatal("a roothash file containing a build transcript must be rejected")
	}
}

func TestParseRootHash_RejectsEmpty(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "os-core.roothash")
	writeFile(t, p, "\n  \n")

	if _, err := parseRootHash(p); err == nil {
		t.Fatal("an empty roothash file must be rejected")
	}
}

// ---------------------------------------------------------------------------
// Resolution + validation
// ---------------------------------------------------------------------------

// verityFixture builds a medium: an image plus (optionally) its siblings.
type verityFixture struct {
	dir      string
	squashfs string
	hashtree string
	roothash string
}

func newVerityFixture(t *testing.T, withSiblings bool) verityFixture {
	t.Helper()
	dir := t.TempDir()
	f := verityFixture{
		dir:      dir,
		squashfs: filepath.Join(dir, "image.squashfs"),
		hashtree: filepath.Join(dir, "os-core.hashtree"),
		roothash: filepath.Join(dir, "os-core.roothash"),
	}
	writeFile(t, f.squashfs, "squashfs-bytes")
	if withSiblings {
		writeFile(t, f.hashtree, "merkle-tree-bytes")
		writeFile(t, f.roothash, fixtureRootHash)
	}
	return f
}

func TestResolveVerityArtifacts_NoSiblingsIsNotAnError(t *testing.T) {
	f := newVerityFixture(t, false)
	mc := newMockCmd()
	svc := newWithCommander(mc)

	v, err := svc.resolveVerityArtifacts(context.Background(), f.squashfs)
	if err != nil {
		t.Fatalf("a medium with no verity artifacts must install fine, got: %v", err)
	}
	if v != nil {
		t.Fatalf("expected nil artifact set, got %+v", v)
	}
	for _, c := range mc.calls {
		if strings.HasPrefix(c, "veritysetup") {
			t.Fatalf("veritysetup must not be probed when there is nothing to verify; called %q", c)
		}
	}
}

// If we cannot verify the artifacts we must not stage them.  Staging unverified
// dm-verity inputs is the one outcome that is strictly worse than no verity at
// all: it converts a working boot into a panic if they happen not to match.
func TestResolveVerityArtifacts_NoVeritysetupMeansNoStaging(t *testing.T) {
	f := newVerityFixture(t, true)
	mc := newMockCmd()
	mc.set("", fmt.Errorf("exec: \"veritysetup\": executable file not found in $PATH"),
		"veritysetup", "--version")
	svc := newWithCommander(mc)

	v, err := svc.resolveVerityArtifacts(context.Background(), f.squashfs)
	if err != nil {
		t.Fatalf("a missing veritysetup must not fail the install, got: %v", err)
	}
	if v != nil {
		t.Fatal("artifacts must NOT be staged when they cannot be verified here")
	}
	if mc.called("veritysetup", "verify", f.squashfs, f.hashtree, fixtureRootHash) {
		t.Fatal("verify must not be attempted after the tool probe failed")
	}
}

func TestResolveVerityArtifacts_VerifyFailureAbortsTheInstall(t *testing.T) {
	f := newVerityFixture(t, true)
	mc := newMockCmd()
	mc.set("Verification of data area failed.", fmt.Errorf("exit status 1"),
		"veritysetup", "verify", f.squashfs, f.hashtree, fixtureRootHash)
	svc := newWithCommander(mc)

	v, err := svc.resolveVerityArtifacts(context.Background(), f.squashfs)
	if err == nil {
		t.Fatal("a hash tree that does not describe the image must abort the install")
	}
	if v != nil {
		t.Fatal("no artifact set may be returned alongside a verification failure")
	}
	if !strings.Contains(err.Error(), "does not verify") {
		t.Errorf("error should say what went wrong, got: %v", err)
	}
}

func TestResolveVerityArtifacts_SuccessPassesTheParsedHash(t *testing.T) {
	f := newVerityFixture(t, true)
	// Written with surrounding whitespace on purpose: veritysetup must receive
	// the PARSED hash, never the raw file contents.
	writeFile(t, f.roothash, fixtureRootHash+"\n")
	mc := newMockCmd()
	svc := newWithCommander(mc)

	v, err := svc.resolveVerityArtifacts(context.Background(), f.squashfs)
	if err != nil {
		t.Fatalf("resolveVerityArtifacts: %v", err)
	}
	if v == nil {
		t.Fatal("expected a validated artifact set")
	}
	if v.rootHashHex != fixtureRootHash {
		t.Errorf("rootHashHex = %q, want %q", v.rootHashHex, fixtureRootHash)
	}
	if v.sig != "" {
		t.Errorf("no .sig ships on this medium, got %q", v.sig)
	}
	if !mc.called("veritysetup", "verify", f.squashfs, f.hashtree, fixtureRootHash) {
		t.Fatalf("veritysetup verify was not called with the parsed hash; calls: %v", mc.calls)
	}
}

func TestResolveVerityArtifacts_PicksUpDetachedSignature(t *testing.T) {
	f := newVerityFixture(t, true)
	writeFile(t, f.roothash+".sig", "detached-sig")
	svc := newWithCommander(newMockCmd())

	v, err := svc.resolveVerityArtifacts(context.Background(), f.squashfs)
	if err != nil {
		t.Fatalf("resolveVerityArtifacts: %v", err)
	}
	if v == nil || v.sig == "" {
		t.Fatal("os-core.roothash.sig ships beside the roothash and must be picked up")
	}
}

// ---------------------------------------------------------------------------
// Staging
// ---------------------------------------------------------------------------

func TestStageFirstSquashfsInto_StagesTheVeritySiblings(t *testing.T) {
	f := newVerityFixture(t, true)
	writeFile(t, f.roothash+".sig", "detached-sig")
	svc := newWithCommander(newMockCmd())
	ctx := context.Background()

	v, err := svc.resolveVerityArtifacts(ctx, f.squashfs)
	if err != nil || v == nil {
		t.Fatalf("fixture should resolve: v=%v err=%v", v, err)
	}

	root := t.TempDir()
	if err := svc.stageFirstSquashfsInto(ctx, root, f.squashfs, v, newProgressHub()); err != nil {
		t.Fatalf("stageFirstSquashfsInto: %v", err)
	}

	slotA := filepath.Join(root, vulosCacheRelPath, "slot-a")
	// The names are not cosmetic: scripts/initramfs/vulos-live derives them from
	// the vulos.squashfs= path in the boot entry, so a rename here silently
	// disables dm-verity on the installed disk.
	for name, want := range map[string]string{
		squashfsDestName:    "squashfs-bytes",
		hashtreeDestName:    "merkle-tree-bytes",
		roothashDestName:    fixtureRootHash,
		roothashSigDestName: "detached-sig",
	} {
		got, err := os.ReadFile(filepath.Join(slotA, name))
		if err != nil {
			t.Fatalf("%s was not staged into slot-a: %v", name, err)
		}
		if string(got) != want {
			t.Errorf("%s content = %q, want %q", name, got, want)
		}
	}
}

// The pre-VERITY-03 layout, which every already-installed disk has: a squashfs
// and nothing else.  It must remain reachable, because that is what happens on
// a medium with no verity artifacts and on one where they cannot be validated.
func TestStageFirstSquashfsInto_NilSetStagesOnlyTheSquashfs(t *testing.T) {
	f := newVerityFixture(t, false)
	svc := newWithCommander(newMockCmd())
	root := t.TempDir()

	if err := svc.stageFirstSquashfsInto(context.Background(), root, f.squashfs, nil, newProgressHub()); err != nil {
		t.Fatalf("staging without verity must succeed: %v", err)
	}
	slotA := filepath.Join(root, vulosCacheRelPath, "slot-a")
	if _, err := os.Stat(filepath.Join(slotA, squashfsDestName)); err != nil {
		t.Fatalf("squashfs not staged: %v", err)
	}
	for _, name := range []string{hashtreeDestName, roothashDestName, roothashSigDestName} {
		if _, err := os.Stat(filepath.Join(slotA, name)); err == nil {
			t.Errorf("%s must not exist when there is no verity artifact set", name)
		}
	}
}

// Ordering invariant: the roothash is what the hook gates the whole dm-verity
// branch on, so it is written LAST.  If the hashtree copy fails (or the machine
// dies) part-way, the slot must not be left advertising verity it cannot honour.
func TestStageVerityArtifacts_RoothashIsNeverLeftWithoutItsHashtree(t *testing.T) {
	dir := t.TempDir()
	slot := t.TempDir()
	roothash := filepath.Join(dir, "os-core.roothash")
	writeFile(t, roothash, fixtureRootHash)

	v := &verityArtifacts{
		hashtree:    filepath.Join(dir, "does-not-exist.hashtree"),
		roothash:    roothash,
		rootHashHex: fixtureRootHash,
	}
	if err := stageVerityArtifacts(v, slot); err == nil {
		t.Fatal("staging a missing hashtree must fail")
	}
	if _, err := os.Stat(filepath.Join(slot, roothashDestName)); err == nil {
		t.Fatal("the roothash was staged even though the hashtree copy failed — " +
			"that slot would panic the machine at boot")
	}
}

// ---------------------------------------------------------------------------
// Pipeline placement
// ---------------------------------------------------------------------------

// The whole point of running verity validation as its own step before
// `partition`: an inconsistent medium must cost the operator a failed install,
// not their existing disk.  Asserting the ERROR alone would pass even if the
// check ran after mkfs.
func TestRunNetbootInstall_BadVerityArtifactsAbortBeforeThediskIsTouched(t *testing.T) {
	f := newVerifyFixture(t)
	f.writeSig(t, f.squashfsPath+".sig", f.image)
	c := f.cfg()

	dir := filepath.Dir(f.squashfsPath)
	hashtree := filepath.Join(dir, "os-core.hashtree")
	writeFile(t, hashtree, "merkle-tree-bytes")
	writeFile(t, filepath.Join(dir, "os-core.roothash"), fixtureRootHash)

	mc := newMockCmd()
	mc.set("Verification of data area failed.", fmt.Errorf("exit status 1"),
		"veritysetup", "verify", f.squashfsPath, hashtree, fixtureRootHash)

	svc := newWithCommander(mc)
	svc.verifyCfg = &c
	hub := newProgressHub()

	svc.runNetbootInstall(NetbootInstallRequest{
		Disk:         "sdc",
		Confirm:      true,
		SquashfsPath: f.squashfsPath,
	}, hub)

	done, err := hub.isDone()
	if !done {
		t.Fatal("hub not done")
	}
	if err == nil || !strings.Contains(err.Error(), "verify-verity") {
		t.Fatalf("expected the verify-verity step to fail the install, got: %v", err)
	}
	for _, call := range mc.calls {
		if strings.HasPrefix(call, "parted") || strings.HasPrefix(call, "mkfs") {
			t.Fatalf("the disk was modified (%q) despite unusable verity artifacts — "+
				"this check must run BEFORE anything destructive", call)
		}
	}
}
