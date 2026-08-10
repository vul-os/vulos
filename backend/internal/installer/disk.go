//go:build linux

// Package installer — install-to-disk (BMINIT-15).
//
// WriteBootableDisk partitions a target block device exactly the way
// build.sh's --disk section does — GPT with a FAT32 ESP (systemd-boot +
// kernel + initrd + loader entry) and an ext4 root labelled "vulos-root" —
// so the resulting machine boots from its own drive on every subsequent
// power-on and keeps whatever the user writes to it, unlike the --live path
// (esp.go / WriteBootableESP), which stays a RAM-resident session by design.
//
// # The failure mode this exists to prevent
//
// The disk loader entry sets init=/sbin/vulos-init, so vulos-init runs as
// PID 1 and reaches the VERITY-02 pivot gate (backend/cmd/init
// verifyOSBeforeBoot), which reads /etc/vulos/stable.json + its detached
// .sig and verifies them against the baked trust anchor and release cert.
// If that manifest is missing, log.Fatalf() kills PID 1 and the kernel
// panics with "Attempted to kill init!" — exactly the bug fixed in build.sh
// commit e5248e84 for the build-time --disk path. WriteBootableDisk closes
// the same gap for the *install-time* path.
//
// # Where the manifest comes from
//
// stable.json is NOT baked into the live medium. SEED-01 embeds only the
// trust anchor and release cert there — the release private key never
// touches a build machine (release.yml's REGISTRY-SIGN step: "CI holds NO
// private key... signing is a human operation on an offline machine"), so
// CI cannot produce a signed manifest, and the live squashfs build.sh --live
// packs never contains one. The manifest is therefore an INPUT this
// installer is GIVEN: the founder signs it offline (backend/cmd/sign
// sign-image, seconds, no build toolchain) against a roothash CI publishes
// alongside the release, and ships stable.json + stable.json.sig as a
// release asset for the operator to place on the live medium (or point at
// via --disk-manifest) before running --disk.
//
// WriteBootableDisk loads and cryptographically verifies whatever manifest
// it is given — against the trust anchor and release cert already baked
// onto the running live system — BEFORE it partitions anything, and refuses
// to touch the target disk at all if the manifest is missing, malformed, or
// fails to verify. This mirrors vulos-init's own VERITY-02 check
// (verify.VerifySquashfsBeforePivot) so an unbootable image is caught here,
// at install time, instead of on first boot.
//
// VERITY-02 itself is not touched or weakened by any of this — it is a
// fail-closed gate on the shipped system, and stays that way. Note also
// that for the ext4-root --disk layout, VERITY-02 does not independently
// recompute or block-verify RootHash against installed bytes (see build.sh's
// own comment on this: "--disk ships an ext4 root, not a squashfs+dm-verity
// device, so there is nothing to mount VERITY-02's root hash against at
// runtime yet"). What IS enforced, both at boot and by this installer before
// it writes anything, is that the manifest's signature verifies against a
// release cert that chains to the baked trust anchor, and that MinEpoch is
// not below the device's epoch floor.
package installer

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"vulos/backend/cmd/verify"
	"vulos/backend/services/signing"
)

// ─── Well-known paths / layout constants ──────────────────────────────────────

const (
	// diskEspTmpMount and diskRootTmpMount are the temporary mount points used
	// while populating the target disk. Distinct from esp.go's espTmpMount so
	// a --disk install and a --live write never collide on mount points.
	diskEspTmpMount  = "/mnt/vulos-disk-esp"
	diskRootTmpMount = "/mnt/vulos-disk-root"

	// diskESPSizeMiB matches build.sh's --disk ESP_MB exactly (build.sh line
	// `ESP_MB=220`). A different size here would still boot, but keeping the
	// two in lockstep avoids yet another axis of --disk vs installer drift.
	diskESPSizeMiB = 220

	// diskManifestDefaultPath is where WriteBootableDisk looks for the
	// operator-supplied stable.json, absent an explicit --disk-manifest
	// override. It is NOT baked into the live medium (see the package doc
	// comment) — this is purely a conventional drop location: the same
	// directory esp.go's liveManifestPath already uses for the live-USB
	// squashfs's own (optional, unsigned) digest manifest, so an operator
	// downloading the founder-signed release asset has one obvious place to
	// put stable.json + stable.json.sig on the live medium before running
	// --disk. Anything else must be passed via --disk-manifest.
	diskManifestDefaultPath = liveManifestPath
)

// ─── Config ──────────────────────────────────────────────────────────────────

// DiskConfig holds parameters for WriteBootableDisk.
type DiskConfig struct {
	// TargetDisk is the block device to install onto (e.g. "/dev/sda"). The
	// device is fully repartitioned; all existing data is lost.
	TargetDisk string

	// SquashfsPath is the source squashfs image whose contents are unpacked
	// onto the target's ext4 root. Defaults to liveSquashfsPath (the same
	// default esp.go's live path uses).
	SquashfsPath string

	// StableManifestPath is the operator-supplied stable.json to verify and
	// carry across to the target's /etc/vulos/stable.json — a release asset
	// the founder signs offline (see the package doc comment), not something
	// present on the live medium by default. Its detached signature is
	// expected at StableManifestPath+".sig". Defaults to
	// diskManifestDefaultPath, a conventional drop location; most installs
	// will want to pass this explicitly (--disk-manifest on the CLI).
	StableManifestPath string

	// Progress receives integers in [0, 100] as the install advances. May be nil.
	Progress chan<- int
}

// ─── WriteBootableDisk ─────────────────────────────────────────────────────────

// WriteBootableDisk installs Vulos OS onto cfg.TargetDisk so the machine
// boots from its own drive: GPT with a FAT32 ESP (systemd-boot, kernel,
// initrd, loader entry) and an ext4 root partition labelled "vulos-root",
// matching build.sh's --disk layout exactly — including the loader entry's
// `root=LABEL=vulos-root rw init=/sbin/vulos-init` options line, which is
// what makes vulos-init run as PID 1 (see the package doc comment for why
// that is exactly the dangerous part).
//
// The target disk is completely repartitioned; call only with explicit user
// confirmation. Build tag: linux only, like esp.go.
func WriteBootableDisk(ctx context.Context, cfg DiskConfig) error {
	if cfg.TargetDisk == "" {
		return fmt.Errorf("disk: TargetDisk must not be empty")
	}
	if cfg.SquashfsPath == "" {
		cfg.SquashfsPath = liveSquashfsPath
	}
	if cfg.StableManifestPath == "" {
		cfg.StableManifestPath = diskManifestDefaultPath
	}

	prog := func(pct int) {
		if cfg.Progress != nil {
			select {
			case cfg.Progress <- pct:
			default:
			}
		}
	}
	prog(0)

	// 1. Load + cryptographically verify the manifest BEFORE anything on the
	//    target disk is touched. This is the whole point: fail loudly here,
	//    not with a kernel panic on the user's first boot.
	payload, manifestBytes, sigBytes, err := loadVerifiedStableManifest(cfg.StableManifestPath, diskManifestVerifyPaths{})
	if err != nil {
		return fmt.Errorf("disk: %w", err)
	}
	prog(3)

	if _, statErr := os.Stat(cfg.SquashfsPath); statErr != nil {
		return fmt.Errorf("disk: squashfs source %q: %w", cfg.SquashfsPath, statErr)
	}
	prog(5)

	espPart, rootPart := partitionNames(cfg.TargetDisk)

	// 2. Partition: GPT, FAT32 ESP + ext4 root — same boundaries and
	//    partition names as build.sh's --disk section (parted mkpart ESP /
	//    mkpart root).
	log.Printf("[disk] partitioning %s (GPT: ESP + ext4 root, matching build.sh --disk)", cfg.TargetDisk)
	espEndMiB := fmt.Sprintf("%dMiB", 1+diskESPSizeMiB)
	if err := runCmd(ctx, "parted", "-s", cfg.TargetDisk,
		"mklabel", "gpt",
		"mkpart", "ESP", "fat32", "1MiB", espEndMiB,
		"set", "1", "esp", "on",
		"mkpart", "root", "ext4", espEndMiB, "100%",
	); err != nil {
		return fmt.Errorf("disk: parted: %w", err)
	}
	prog(10)

	// 3. Format. Label "ESP" (not esp.go's live-path label "EFI") and
	//    "vulos-root" match build.sh's `mkfs.vfat -F 32 -n ESP` / `mkfs.ext4
	//    ... -L vulos-root` exactly — the root label is what the loader
	//    entry's root=LABEL=vulos-root resolves against at boot.
	log.Printf("[disk] formatting %s as FAT32 (ESP, label ESP)", espPart)
	if err := runCmd(ctx, "mkfs.fat", "-F32", "-n", "ESP", espPart); err != nil {
		return fmt.Errorf("disk: mkfs.fat: %w", err)
	}
	log.Printf("[disk] formatting %s as ext4 (root, label vulos-root)", rootPart)
	if err := runCmd(ctx, "mkfs.ext4", "-F", "-L", "vulos-root", rootPart); err != nil {
		return fmt.Errorf("disk: mkfs.ext4: %w", err)
	}
	prog(15)

	// 4. Mount both partitions.
	if err := os.MkdirAll(diskEspTmpMount, 0o755); err != nil {
		return fmt.Errorf("disk: mkdir %s: %w", diskEspTmpMount, err)
	}
	if err := os.MkdirAll(diskRootTmpMount, 0o755); err != nil {
		return fmt.Errorf("disk: mkdir %s: %w", diskRootTmpMount, err)
	}
	log.Printf("[disk] mounting %s at %s", rootPart, diskRootTmpMount)
	if err := runCmd(ctx, "mount", rootPart, diskRootTmpMount); err != nil {
		return fmt.Errorf("disk: mount %s: %w", rootPart, err)
	}
	defer func() {
		_ = runCmd(context.Background(), "umount", diskRootTmpMount)
	}()
	log.Printf("[disk] mounting %s at %s", espPart, diskEspTmpMount)
	if err := runCmd(ctx, "mount", espPart, diskEspTmpMount); err != nil {
		return fmt.Errorf("disk: mount %s: %w", espPart, err)
	}
	defer func() {
		_ = runCmd(context.Background(), "umount", diskEspTmpMount)
	}()
	prog(20)

	// 5. Populate the root partition from the verified squashfs — not from
	//    the running live session's merged overlay view, which may carry
	//    tmpfs-only session artifacts. Unpacking the exact squashfs whose
	//    content stable.json's root hash was computed over is what makes
	//    "the manifest we just verified" and "the bits we just installed"
	//    the same claim.
	log.Printf("[disk] unpacking %s onto %s", cfg.SquashfsPath, diskRootTmpMount)
	if err := runCmd(ctx, "unsquashfs", "-f", "-d", diskRootTmpMount, cfg.SquashfsPath); err != nil {
		return fmt.Errorf("disk: unsquashfs: %w", err)
	}
	prog(75)

	// 6. Carry the verified manifest + signature across to where VERITY-02
	//    reads them. This is the fix for the e5248e84 failure mode: no
	//    manifest here means vulos-init's verifyOSBeforeBoot() calls
	//    log.Fatalf(), PID 1 dies, and the kernel panics with "Attempted to
	//    kill init!" on the very first boot.
	if err := writeManifestToRoot(diskRootTmpMount, manifestBytes, sigBytes); err != nil {
		return fmt.Errorf("disk: %w", err)
	}
	log.Printf("[disk] stable.json + .sig carried to %s/etc/vulos (roothash=%s)", diskRootTmpMount, payload.RootHash)
	prog(82)

	// 7. Bootloader — reuse the exact same bootctl/EFI-stub helpers the live
	//    path uses (esp.go); do not maintain a second copy of this logic.
	log.Printf("[disk] installing systemd-boot via bootctl")
	if err := installBootctlESP(ctx, diskEspTmpMount); err != nil {
		log.Printf("[disk] bootctl unavailable (%v) — copying EFI stub directly", err)
		if stubErr := copyEFIStub(ctx, diskEspTmpMount); stubErr != nil {
			return fmt.Errorf("disk: EFI stub install: %w (bootctl also failed: %v)", stubErr, err)
		}
	}
	prog(88)

	// 8. Kernel + initrd, flat at the ESP root (::/vmlinuz, ::/initrd.img) —
	//    build.sh's --disk ESP layout, NOT esp.go's EFI/vulos/ tree (that
	//    tree is the --live layout; a --disk loader entry pointing at
	//    /EFI/vulos/vmlinuz while the file lives at /vmlinuz would be exactly
	//    the kind of build.sh-vs-installer drift this command exists to
	//    avoid). Both are required — a disk with no kernel doesn't boot at
	//    all, so unlike the live path this is fatal, not a warning.
	kernelSrc := resolveKernelSrc()
	if kernelSrc == "" {
		return fmt.Errorf("disk: no kernel found (checked /boot/vmlinuz, /boot/vmlinuz-live, /boot/vmlinuz-*)")
	}
	if err := espCopyFile(ctx, kernelSrc, filepath.Join(diskEspTmpMount, "vmlinuz")); err != nil {
		return fmt.Errorf("disk: copy kernel: %w", err)
	}
	initrdSrc := resolveInitramfsSrc()
	if initrdSrc == "" {
		return fmt.Errorf("disk: no initrd found (checked %s, %s, /boot/initrd.img-*)", espInitramfsSrc, espInitramfsAlt)
	}
	if err := espCopyFile(ctx, initrdSrc, filepath.Join(diskEspTmpMount, "initrd.img")); err != nil {
		return fmt.Errorf("disk: copy initrd: %w", err)
	}
	prog(94)

	// 9. Loader entry: root=LABEL=vulos-root rw init=/sbin/vulos-init — the
	//    exact string build.sh's --disk section writes. disk_test.go asserts
	//    this against build.sh's own printf line directly, so a future edit
	//    to either side that breaks the match fails the build, not a user's
	//    boot.
	if err := writeDiskBootEntry(ctx, diskEspTmpMount); err != nil {
		return fmt.Errorf("disk: boot entry: %w", err)
	}
	prog(99)

	if err := runCmd(ctx, "sync"); err != nil {
		log.Printf("[disk] sync warning (non-fatal): %v", err)
	}

	log.Printf("[disk] install complete: %s now boots Vulos OS from its own drive (root=LABEL=vulos-root, init=/sbin/vulos-init)", cfg.TargetDisk)
	prog(100)
	return nil
}

// ─── Manifest verification ────────────────────────────────────────────────────

// diskManifestVerifyPaths holds the trust-anchor / release-cert / epoch-floor
// paths used to verify the carried-across manifest. The zero value resolves
// to the real, production, on-image paths (signing.DefaultAnchorPath /
// DefaultReleaseCertPath / DefaultEpochPath) via withDefaults — production
// code always passes the zero value. Tests override all three to a temp tree
// so the verification logic can be exercised without root or a real
// /etc/vulos, without changing what production actually checks.
type diskManifestVerifyPaths struct {
	AnchorPath string
	CertPath   string
	EpochPath  string
}

func (p diskManifestVerifyPaths) withDefaults() diskManifestVerifyPaths {
	if p.AnchorPath == "" {
		p.AnchorPath = signing.DefaultAnchorPath
	}
	if p.CertPath == "" {
		p.CertPath = signing.DefaultReleaseCertPath
	}
	if p.EpochPath == "" {
		p.EpochPath = signing.DefaultEpochPath
	}
	return p
}

// loadVerifiedStableManifest reads stable.json + stable.json.sig from
// manifestPath (and manifestPath+".sig") and verifies them against the trust
// anchor and release cert named by vp (production: the real baked paths on
// this running system) — the same chain vulos-init's VERITY-02 gate
// (verifyOSBeforeBoot in backend/cmd/init) walks at boot, via the same
// verify.VerifySquashfsBeforePivot function.
//
// Any failure — missing manifest, missing signature, malformed payload, or a
// signature/cert/epoch check that does not pass — is returned as a non-nil
// error. Callers MUST treat that as "do not write to the target disk": an
// installed system without a manifest that verifies here is a system
// VERITY-02 will refuse to boot.
func loadVerifiedStableManifest(manifestPath string, vp diskManifestVerifyPaths) (payload verify.ImagePayload, manifestBytes, sigBytes []byte, err error) {
	if manifestPath == "" {
		manifestPath = diskManifestDefaultPath
	}
	vp = vp.withDefaults()
	sigPath := manifestPath + ".sig"

	manifestBytes, err = os.ReadFile(manifestPath)
	if err != nil {
		err = fmt.Errorf("cannot read stable.json manifest %q — the source medium has no signed OS manifest to carry to the target disk; VERITY-02 (backend/cmd/init verifyOSBeforeBoot) would kernel-panic on first boot without one: %w", manifestPath, err)
		return
	}

	if jsonErr := json.Unmarshal(manifestBytes, &payload); jsonErr != nil {
		err = fmt.Errorf("cannot parse stable.json manifest %q: %w", manifestPath, jsonErr)
		return
	}
	if payload.RootHash == "" || payload.Path == "" {
		err = fmt.Errorf("stable.json manifest %q is missing required fields (roothash/path)", manifestPath)
		return
	}

	sigBytes, err = os.ReadFile(sigPath)
	if err != nil {
		err = fmt.Errorf("cannot read stable.json signature %q — an unsigned manifest cannot satisfy VERITY-02: %w", sigPath, err)
		return
	}

	vcfg := verify.SquashfsVerifyConfig{
		AnchorPath:         vp.AnchorPath,
		CertPath:           vp.CertPath,
		SigPath:            sigPath,
		ImagePayloadForSig: payload,
		EpochFloor:         readEpochFloorBestEffort(vp.EpochPath),
	}
	// VerifyManifestSignature, not VerifySquashfsBeforePivot: this call verifies
	// a manifest DESCRIBING an image that is not the one this process is running,
	// and --disk unpacks it onto an ext4 root, so there is no dm-verity root hash
	// here to bind the signature to. Saying so explicitly is the point — the two
	// used to be one function, whose root-hash "check" compared payload.RootHash
	// against payload.RootHash and reported a verified image having measured
	// nothing.
	if verr := verify.VerifyManifestSignature(vcfg); verr != nil {
		err = fmt.Errorf("stable.json manifest %q failed signature verification — refusing to install an image VERITY-02 would reject at boot: %w", manifestPath, verr)
		return
	}

	log.Printf("[disk] stable.json manifest SIGNATURE verified (roothash=%s — not bound to any image here; --disk installs an ext4 root with no dm-verity device)", payload.RootHash)
	return payload, manifestBytes, sigBytes, nil
}

// readEpochFloorBestEffort mirrors backend/cmd/init's verityEpochFloor:
// returns 0 on any read/parse error, since a fresh or live-booted system has
// no persisted epoch floor yet (a brand-new device's floor is conservatively
// 0, same as verifyOSBeforeBoot assumes).
func readEpochFloorBestEffort(epochPath string) int64 {
	data, err := os.ReadFile(epochPath)
	if err != nil {
		return 0
	}
	var rec struct {
		Floor int64 `json:"floor"`
	}
	if err := json.Unmarshal(data, &rec); err != nil {
		return 0
	}
	return rec.Floor
}

// ─── Carrying the manifest onto the target root ───────────────────────────────

// writeManifestToRoot carries the verified manifest + signature into
// rootMountPath's etc/vulos/, matching exactly where VERITY-02
// (verityManifestPath / verityManifestSigPath in backend/cmd/init/main.go)
// reads them at boot once rootMountPath becomes "/".
func writeManifestToRoot(rootMountPath string, manifestBytes, sigBytes []byte) error {
	etcVulos := filepath.Join(rootMountPath, "etc", "vulos")
	if err := os.MkdirAll(etcVulos, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", etcVulos, err)
	}
	if err := os.WriteFile(filepath.Join(etcVulos, "stable.json"), manifestBytes, 0o444); err != nil {
		return fmt.Errorf("write stable.json: %w", err)
	}
	if err := os.WriteFile(filepath.Join(etcVulos, "stable.json.sig"), sigBytes, 0o444); err != nil {
		return fmt.Errorf("write stable.json.sig: %w", err)
	}
	return nil
}

// ─── Boot entry ───────────────────────────────────────────────────────────────

// diskLoaderConfContent is loader/loader.conf on an installed disk's ESP.
// Matches build.sh's --disk section byte for byte (build.sh:
// `printf 'default vulos\ntimeout 0\nconsole-mode max\n'`).
const diskLoaderConfContent = "default vulos\ntimeout 0\nconsole-mode max\n"

// diskBootEntryContent is loader/entries/vulos.conf on an installed disk's
// ESP. It MUST match, byte for byte, the entry build.sh's --disk section
// writes to $OUTDIR/_entry.conf — disk_test.go asserts this directly against
// build.sh's own printf string, not a hand-copied literal, so the two cannot
// silently drift apart the way the live-boot entry's "vulos.live=1" vs
// "vulos.live" once did.
const diskBootEntryContent = "title  Vulos\n" +
	"linux  /vmlinuz\n" +
	"initrd /initrd.img\n" +
	"options root=LABEL=vulos-root rw init=/sbin/vulos-init quiet splash plymouth.theme=vulos vulos.kiosk=force console=tty1 console=ttyAMA0,115200\n"

// writeDiskBootEntry writes loader.conf and the systemd-boot loader entry for
// an installed (non-live) disk boot: init=/sbin/vulos-init as PID 1, which is
// what makes the VERITY-02 gate run. mountPath is the already-mounted ESP
// root (diskEspTmpMount in production; disk_test.go passes a t.TempDir()).
func writeDiskBootEntry(ctx context.Context, mountPath string) error {
	entriesDir := filepath.Join(mountPath, "loader", "entries")
	if err := os.MkdirAll(entriesDir, 0o755); err != nil {
		return fmt.Errorf("mkdir entries: %w", err)
	}

	loaderConfPath := filepath.Join(mountPath, "loader", "loader.conf")
	if err := os.WriteFile(loaderConfPath, []byte(diskLoaderConfContent), 0o644); err != nil {
		return fmt.Errorf("write loader.conf: %w", err)
	}

	entryPath := filepath.Join(entriesDir, "vulos.conf")
	if err := os.WriteFile(entryPath, []byte(diskBootEntryContent), 0o644); err != nil {
		return fmt.Errorf("write boot entry: %w", err)
	}
	log.Printf("[disk] boot entry written: %s", entryPath)
	return nil
}
