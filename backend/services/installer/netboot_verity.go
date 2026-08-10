package installer

// netboot_verity.go — VERITY-03: stage the dm-verity sibling artifacts alongside
// the OS squashfs so dm-verity is actually ACTIVE on an installed disk.
//
// # The defect this closes
//
// docs/ARCHITECTURE.md claims "dm-verity enforces block-level integrity at
// runtime via the initramfs".  That was true of the live-USB medium and FALSE of
// a netboot-installed disk, for two independent reasons.  This file closes the
// first one:
//
//	stageFirstSquashfs copied ONLY os-core.squashfs into the slot.  It never
//	staged the sibling os-core.hashtree / os-core.roothash (/.sig) that
//	scripts/verity/gen-verity.sh produces and that
//	scripts/initramfs/vulos-live looks for BESIDE the image.
//
// Read from a real boot's serial console, the symptom was:
//
//	vulos-live: veritysetup or hash files absent — mounting squashfs without dm-verity
//	  hashtree:    /root/var/cache/vulos/slot-a/os-core.hashtree MISSING
//	  roothash:    /root/var/cache/vulos/slot-a/os-core.roothash MISSING
//
// (that headline now reads "a dm-verity prerequisite is missing" — it was
// renamed once it could also fire with veritysetup and both files present.)
//
// The second reason (veritysetup + the dm-verity kernel module are not IN the
// initramfs) is a build.sh / rootfs-package change and is NOT fixed here.  Until
// it lands the hook keeps taking its documented unverified-loop fallback — which
// is exactly why staging these files is safe to turn on now (see "Why staging
// alone cannot brick a machine" below).
//
// # Why this is boot-critical, and what that forces
//
// scripts/initramfs/vulos-live PANICS when `veritysetup open` fails — by design,
// because a root-hash mismatch is indistinguishable from tampering.  So a
// hashtree/roothash staged next to a squashfs they do NOT describe converts a
// working boot into an unbootable machine.  Staging them unverified would be
// strictly WORSE than staging nothing.
//
// Therefore the artifacts are validated with the real tool, against the real
// image, BEFORE anything destructive happens:
//
//  1. Resolution + validation runs as its own pipeline step positioned BEFORE
//     `partition` — so a bad or inconsistent medium aborts with the target disk
//     still intact, the same discipline verifyNetbootSquashfs already follows.
//  2. Validation is `veritysetup verify <squashfs> <hashtree> <roothash>`, which
//     re-reads every data block, rebuilds the Merkle tree and asserts the stored
//     root matches.  This is the same primitive the initramfs will use, not a
//     stand-in for it: if it passes here, `veritysetup open` cannot fail for a
//     hash reason there.
//  3. If veritysetup is NOT available on the running live system, the artifacts
//     are NOT staged.  We never stage inputs we could not check.  That keeps the
//     result identical to today's behaviour (unverified loop mount) rather than
//     gambling the boot on unvalidated files.
//
// # Why staging alone cannot brick a machine
//
//   - Artifacts absent from the medium        → nothing staged, hook falls back
//     (this is what a pre-verity medium does).
//   - veritysetup absent on the live system   → nothing staged, hook falls back.
//   - Artifacts present but inconsistent      → install ABORTS before the disk is
//     touched; the operator still has their data and a working machine.
//   - Artifacts present and verified          → staged.  The initramfs then either
//     has veritysetup+dm-verity (verity active) or does not (documented fallback,
//     unchanged).  A mismatch at boot is impossible because the same tool already
//     verified the same three files.
//
// # A pre-existing installed disk
//
// A slot laid down by an older installer holds a squashfs and no verity
// siblings.  Nothing here touches it; the hook's `-f "$HASHTREE_ABS"` /
// `-f "$ROOTHASH_ABS"` guards are false, and it takes the same unverified
// loop-mount path it takes today.  Such a disk keeps booting unchanged.
//
// # Source-side naming
//
// gen-verity.sh's callers name the artifact family "os-core.*", but build.sh
// --live packs the image itself as image.squashfs and writes os-core.hashtree /
// os-core.roothash BESIDE it (build.sh VERITY-01).  A real netboot medium serves
// os-core.squashfs, where both conventions coincide.  Both are therefore
// accepted, resolved as a PAIR so a half-matching set is never assembled from
// two different families:
//
//	1. <image-base>.hashtree + <image-base>.roothash   (os-core.squashfs → os-core.*)
//	2. <dir>/os-core.hashtree + <dir>/os-core.roothash (image.squashfs   → os-core.*)
//
// Whatever the source is called, the STAGED names are always os-core.* — that is
// what the hook derives from the vulos.squashfs= path it is given.

import (
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
)

const (
	// hashtreeDestName / roothashDestName / roothashSigDestName are the names
	// the artifacts take INSIDE a slot directory.  scripts/initramfs/vulos-live
	// derives them from the squashfs path it is booted with
	// ("<base>.squashfs" → "<base>.hashtree"/"<base>.roothash", plus
	// "<roothash>.sig"), so they must track squashfsDestName.
	hashtreeDestName    = "os-core.hashtree"
	roothashDestName    = "os-core.roothash"
	roothashSigDestName = "os-core.roothash.sig"

	// verityArtifactBaseName is the conventional artifact-family name used by
	// gen-verity.sh's callers when the image itself is named something else
	// (build.sh --live packs image.squashfs but writes os-core.hashtree).
	verityArtifactBaseName = "os-core"
)

// verityArtifacts is a RESOLVED and VALIDATED dm-verity artifact set: the
// hashtree and roothash files have been proven by veritysetup to describe the
// squashfs they ship beside.  A nil *verityArtifacts means "no verity for this
// install" — a legitimate, non-fatal outcome (see the file comment).
type verityArtifacts struct {
	// hashtree is the source path of the Merkle tree file.
	hashtree string
	// roothash is the source path of the hex root-hash file.
	roothash string
	// sig is the source path of the detached roothash signature, or "" when the
	// medium ships none.  The initramfs treats a PRESENT-but-invalid signature
	// as fatal, so this is copied only when it genuinely exists.
	sig string
	// rootHashHex is the parsed root hash, lowercase, no whitespace.
	rootHashHex string
}

// locateVerityArtifacts resolves the sibling hashtree/roothash for squashfsPath.
// It returns ("", "", false) when no complete PAIR exists under either naming
// convention — never a half-set drawn from two different families.
func locateVerityArtifacts(squashfsPath string) (hashtree, roothash string, ok bool) {
	dir := filepath.Dir(squashfsPath)
	base := strings.TrimSuffix(filepath.Base(squashfsPath), ".squashfs")

	candidates := [][2]string{
		{filepath.Join(dir, base+".hashtree"), filepath.Join(dir, base+".roothash")},
		{filepath.Join(dir, verityArtifactBaseName+".hashtree"), filepath.Join(dir, verityArtifactBaseName+".roothash")},
	}
	for _, c := range candidates {
		if isRegularFile(c[0]) && isRegularFile(c[1]) {
			return c[0], c[1], true
		}
	}
	return "", "", false
}

// isRegularFile reports whether path exists and is a regular file.  A directory
// named os-core.hashtree must not satisfy the pair test.
func isRegularFile(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.Mode().IsRegular()
}

// parseRootHash reads a gen-verity.sh roothash file and returns the hash in
// lowercase hex.  gen-verity.sh's output contract is "one line: 64-char
// lowercase hex string"; anything that is not pure hex is rejected rather than
// passed on to veritysetup, because a garbled roothash is exactly the failure
// this project already hit once (an ANSI-coloured build transcript landing in
// stable.json's roothash field, which then panicked the kernel at boot).
func parseRootHash(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read roothash %s: %w", path, err)
	}
	h := strings.ToLower(strings.TrimSpace(string(raw)))
	if h == "" {
		return "", fmt.Errorf("roothash %s is empty", path)
	}
	if _, err := hex.DecodeString(h); err != nil || len(h)%2 != 0 {
		return "", fmt.Errorf("roothash %s is not hex (%d bytes read)", path, len(raw))
	}
	return h, nil
}

// veritysetupAvailable reports whether the running system has a usable
// veritysetup.  Probed through the Service's Commander so tests can drive it.
func (s *Service) veritysetupAvailable(ctx context.Context) bool {
	_, err := s.cmd.Output(ctx, "veritysetup", "--version")
	return err == nil
}

// resolveVerityArtifacts finds and VALIDATES the dm-verity artifacts that ship
// beside squashfsPath.
//
// Returns (nil, nil) — stage nothing, boot unverified, exactly as today — when:
//   - no complete hashtree+roothash pair ships beside the image, or
//   - veritysetup is unavailable on this system, so the pair cannot be checked.
//
// Returns an error — ABORT the install, before the disk is touched — when:
//   - the roothash file is unreadable/empty/not hex, or
//   - veritysetup verify says the tree does not describe this image.
//
// The asymmetry is deliberate.  "No verity available" is the status quo and must
// not break installs.  "Verity artifacts that do not match" means the medium is
// corrupt or tampered with, and staging them would panic the machine at every
// subsequent boot.
func (s *Service) resolveVerityArtifacts(ctx context.Context, squashfsPath string) (*verityArtifacts, error) {
	hashtree, roothash, ok := locateVerityArtifacts(squashfsPath)
	if !ok {
		log.Printf("[netboot-install] no dm-verity artifacts beside %s "+
			"(looked for <base>.hashtree/.roothash and %s.hashtree/.roothash) — "+
			"the installed disk will mount its squashfs WITHOUT dm-verity",
			squashfsPath, verityArtifactBaseName)
		return nil, nil
	}

	hashHex, err := parseRootHash(roothash)
	if err != nil {
		return nil, fmt.Errorf("dm-verity: %w", err)
	}

	if !s.veritysetupAvailable(ctx) {
		log.Printf("[netboot-install] dm-verity artifacts found (%s, %s) but veritysetup is "+
			"not available on this system — refusing to stage inputs that cannot be "+
			"verified here; the installed disk will mount its squashfs WITHOUT dm-verity",
			hashtree, roothash)
		return nil, nil
	}

	// The real check.  Re-reads every data block, rebuilds the tree, asserts the
	// root matches.  Because this is the same tool and the same three files the
	// initramfs will use, passing here means `veritysetup open` at boot cannot
	// fail for a hash reason.
	if out, err := s.cmd.Output(ctx, "veritysetup", "verify", squashfsPath, hashtree, hashHex); err != nil {
		return nil, fmt.Errorf("dm-verity: %s does not verify against %s with root hash %s "+
			"— refusing to install (staging a mismatched hash tree turns every subsequent "+
			"boot into a panic): %v: %s",
			squashfsPath, hashtree, hashHex, err, strings.TrimSpace(string(out)))
	}

	v := &verityArtifacts{hashtree: hashtree, roothash: roothash, rootHashHex: hashHex}
	if sig := roothash + ".sig"; isRegularFile(sig) {
		v.sig = sig
	}
	log.Printf("[netboot-install] dm-verity artifacts verified against %s (roothash %s, sig=%v)",
		squashfsPath, hashHex, v.sig != "")
	return v, nil
}

// stageVerityArtifacts copies a validated artifact set into slotDir under the
// os-core.* names the initramfs derives from the boot entry's vulos.squashfs=
// path.  A nil set is a no-op.
//
// The roothash is written LAST.  scripts/initramfs/vulos-live gates the whole
// dm-verity branch on the roothash file existing, so if the machine loses power
// mid-staging the worst intermediate state on disk is "hashtree present,
// roothash absent" — which the hook reads as "no verity", not as "verity with a
// truncated hash".  The reverse order would leave a window in which a boot
// panics on a hashtree that is still being written.
func stageVerityArtifacts(v *verityArtifacts, slotDir string) error {
	if v == nil {
		return nil
	}
	if err := copyFileSimple(v.hashtree, filepath.Join(slotDir, hashtreeDestName)); err != nil {
		return fmt.Errorf("stage hashtree: %w", err)
	}
	if v.sig != "" {
		if err := copyFileSimple(v.sig, filepath.Join(slotDir, roothashSigDestName)); err != nil {
			return fmt.Errorf("stage roothash signature: %w", err)
		}
	}
	if err := copyFileSimple(v.roothash, filepath.Join(slotDir, roothashDestName)); err != nil {
		return fmt.Errorf("stage roothash: %w", err)
	}
	log.Printf("[netboot-install] dm-verity artifacts staged into %s (hashtree, roothash%s)",
		slotDir, map[bool]string{true: ", roothash.sig"}[v.sig != ""])
	return nil
}

// copyFileSimple copies src to dest, syncing before close so the bytes are on
// the medium and not merely in the page cache — the machine reboots moments
// after this returns.  Progress reporting is deliberately absent: these files
// are a few MiB next to a ~600 MiB squashfs.
func copyFileSimple(src, dest string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open %s: %w", src, err)
	}
	defer in.Close()

	out, err := os.Create(dest)
	if err != nil {
		return fmt.Errorf("create %s: %w", dest, err)
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(dest)
		return fmt.Errorf("copy %s → %s: %w", src, dest, err)
	}
	if err := out.Sync(); err != nil {
		out.Close()
		os.Remove(dest)
		return fmt.Errorf("sync %s: %w", dest, err)
	}
	return out.Close()
}
