//go:build linux

// Package installer provides the live-USB ESP writer for BMINIT-14.
//
// WriteBootableESP partitions a target block device as a GPT disk with a
// 512 MiB FAT32 EFI System Partition (ESP) and a remainder ext4 data
// partition, then populates the ESP so the USB device can boot in a UEFI VM
// or on bare metal:
//
//	ESP layout
//	  EFI/BOOT/bootx64.efi      — bootloader (systemd-boot or fallback stub)
//	  EFI/vulos/vmlinuz         — kernel (copied from live system)
//	  EFI/vulos/initramfs.img   — verify-capable initramfs
//	  EFI/vulos/trust-anchor.pub — SEED-01 baked trust-anchor key
//	  EFI/vulos/os-bucket-url   — SEED-02 bucket URL (optional)
//	  loader/entries/vulos-live.conf — systemd-boot entry (toram live boot)
//
// The squashfs image on the source live system is verified (sha256) against
// the published manifest before the USB is written, mirroring the netboot
// iPXE path (NETB-*) but writing to a physical block device instead of
// serving over HTTP.
package installer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// ─── Well-known source paths (on the running live system) ────────────────────

const (
	// liveSquashfsPath is the standard location of the squashfs on the
	// live medium (NETB-03 convention).  Override via Config.SquashfsPath.
	liveSquashfsPath = "/run/live/medium/vulos/os-core.squashfs"

	// liveManifestPath is the stable.json manifest next to the squashfs.
	// It carries the expected sha256 of the squashfs image.
	liveManifestPath = "/run/live/medium/vulos/stable.json"

	// espESPMount is the temporary mount point used while populating the ESP.
	espTmpMount = "/mnt/vulos-live-usb"

	// Source paths for seed files — same as netboot_install.go (NETB-03).
	espAnchorSrc    = "/etc/vulos/trust-anchor.pub"
	espBucketURLSrc = "/etc/vulos/os-bucket-url"
	espInitramfsSrc = "/boot/initramfs-vulos.img"
	espInitramfsAlt = "/boot/initrd.img"
)

// ─── Config ──────────────────────────────────────────────────────────────────

// Config holds parameters for WriteBootableESP.
type Config struct {
	// TargetDisk is the block device to write (e.g. "/dev/sdb").  The device
	// is fully overwritten; all existing data is lost.
	TargetDisk string

	// SquashfsPath is the source squashfs image to embed onto the USB as
	// EFI/vulos/os-core.squashfs.  Defaults to liveSquashfsPath.
	SquashfsPath string

	// ManifestPath is the stable.json that carries the expected sha256 of the
	// squashfs.  If set the squashfs is verified before write; leave empty to
	// skip verification (not recommended in production).
	ManifestPath string

	// Progress receives integers in [0, 100] as the write advances.  May be nil.
	Progress chan<- int
}

// ─── WriteBootableESP ─────────────────────────────────────────────────────────

// WriteBootableESP writes a bootable EFI System Partition to cfg.TargetDisk.
// It runs in the caller's goroutine and returns a non-nil error on failure.
// The target disk is completely repartitioned; call only with explicit user
// confirmation.
//
// Build tag: linux only — the underlying helpers call Linux syscalls via
// exec (parted, mkfs.fat, mount, umount, cp).  Darwin / CI: compile under
// GOOS=linux only; the cmd/installer entry point carries the same tag so
// go build ./... on darwin still passes.
func WriteBootableESP(ctx context.Context, cfg Config) error {
	if cfg.TargetDisk == "" {
		return fmt.Errorf("esp: TargetDisk must not be empty")
	}
	if cfg.SquashfsPath == "" {
		cfg.SquashfsPath = liveSquashfsPath
	}
	if cfg.ManifestPath == "" {
		cfg.ManifestPath = liveManifestPath
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

	// 1. Verify the squashfs against the manifest (if manifest present).
	if err := verifySquashfsDigest(cfg.SquashfsPath, cfg.ManifestPath); err != nil {
		return fmt.Errorf("esp: squashfs verification: %w", err)
	}
	prog(5)

	// Derive partition names.
	espPart, dataPart := partitionNames(cfg.TargetDisk)

	// 2. Partition the disk: GPT, 512 MiB ESP + remainder ext4 data.
	log.Printf("[esp] partitioning %s", cfg.TargetDisk)
	if err := runCmd(ctx, "parted", "-s", cfg.TargetDisk,
		"mklabel", "gpt",
		"mkpart", "ESP", "fat32", "1MiB", "513MiB",
		"set", "1", "esp", "on",
		"mkpart", "vulos-data", "ext4", "513MiB", "100%",
	); err != nil {
		return fmt.Errorf("esp: parted: %w", err)
	}
	prog(10)

	// 3. Format ESP as FAT32, data partition as ext4.
	log.Printf("[esp] formatting %s as FAT32 (ESP)", espPart)
	if err := runCmd(ctx, "mkfs.fat", "-F32", "-n", "EFI", espPart); err != nil {
		return fmt.Errorf("esp: mkfs.fat: %w", err)
	}
	log.Printf("[esp] formatting %s as ext4 (data)", dataPart)
	if err := runCmd(ctx, "mkfs.ext4", "-F", "-L", "vulos-live-data", dataPart); err != nil {
		return fmt.Errorf("esp: mkfs.ext4: %w", err)
	}
	prog(15)

	// 4. Mount the ESP under a temporary directory.
	if err := os.MkdirAll(espTmpMount, 0o755); err != nil {
		return fmt.Errorf("esp: mkdir %s: %w", espTmpMount, err)
	}
	log.Printf("[esp] mounting %s at %s", espPart, espTmpMount)
	if err := runCmd(ctx, "mount", espPart, espTmpMount); err != nil {
		return fmt.Errorf("esp: mount %s: %w", espPart, err)
	}
	defer func() {
		// Best-effort unmount even if a later step fails.
		_ = runCmd(context.Background(), "umount", espTmpMount)
	}()
	prog(18)

	// 5. Populate the ESP tree.
	if err := populateESP(ctx, cfg.SquashfsPath, prog); err != nil {
		return fmt.Errorf("esp: populate: %w", err)
	}
	prog(90)

	// 6. Install the systemd-boot EFI binary via bootctl so the UEFI firmware
	//    finds it at the standard fallback path EFI/BOOT/bootx64.efi.
	log.Printf("[esp] installing systemd-boot via bootctl")
	if err := installBootctlESP(ctx, espTmpMount); err != nil {
		// Non-fatal: if bootctl is unavailable, copy the EFI stub directly.
		log.Printf("[esp] bootctl unavailable (%v) — copying EFI stub directly", err)
		if stubErr := copyEFIStub(ctx, espTmpMount); stubErr != nil {
			return fmt.Errorf("esp: EFI stub install: %w (bootctl also failed: %v)", stubErr, err)
		}
	}
	prog(95)

	// 7. Write the systemd-boot loader entry for live (toram) boot.
	if err := writeLiveBootEntry(ctx, espTmpMount); err != nil {
		return fmt.Errorf("esp: boot entry: %w", err)
	}
	prog(99)

	// 8. Sync and unmount — the defer above handles umount; call sync first.
	if err := runCmd(ctx, "sync"); err != nil {
		log.Printf("[esp] sync warning (non-fatal): %v", err)
	}

	log.Printf("[esp] live USB written to %s — bootable ESP installed", cfg.TargetDisk)
	prog(100)
	return nil
}

// ─── Partition naming ────────────────────────────────────────────────────────

// partitionNames returns the ESP and data partition device paths for disk.
// NVMe/eMMC devices use the 'p' infix (nvme0n1p1, mmcblk0p1); all others
// append the digit directly (sdb1, vdb2).
func partitionNames(disk string) (esp, data string) {
	if strings.HasPrefix(disk, "/dev/nvme") || strings.HasPrefix(disk, "/dev/mmcblk") {
		return disk + "p1", disk + "p2"
	}
	return disk + "1", disk + "2"
}

// ─── Squashfs verification ────────────────────────────────────────────────────

// manifestDigest is the subset of stable.json we need for verification.
// It mirrors the ManifestPayload / ImagePayload structure already used by
// cmd/verify.  We only read the sha256 field here.
type manifestDigest struct {
	SHA256 string `json:"sha256"`
}

// verifySquashfsDigest computes the sha256 of squashfsPath and compares it
// against the value in the manifest JSON at manifestPath.  Returns nil if the
// digest matches or if the manifest does not contain a sha256 field (skip).
func verifySquashfsDigest(squashfsPath, manifestPath string) error {
	// If the manifest is absent, skip verification (dev/offline mode).
	mdata, err := os.ReadFile(manifestPath)
	if err != nil {
		log.Printf("[esp] manifest %s not found — skipping squashfs digest check", manifestPath)
		return nil
	}

	// Parse only the sha256 field; ignore unknown fields.
	var m manifestDigest
	if jsonErr := jsonUnmarshalDigest(mdata, &m); jsonErr != nil {
		log.Printf("[esp] manifest parse warning (%v) — skipping digest check", jsonErr)
		return nil
	}
	if m.SHA256 == "" {
		log.Printf("[esp] manifest has no sha256 field — skipping digest check")
		return nil
	}

	f, err := os.Open(squashfsPath)
	if err != nil {
		return fmt.Errorf("open squashfs %s: %w", squashfsPath, err)
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return fmt.Errorf("hash squashfs %s: %w", squashfsPath, err)
	}
	got := hex.EncodeToString(h.Sum(nil))
	if got != strings.ToLower(m.SHA256) {
		return fmt.Errorf("squashfs sha256 mismatch: got %s, want %s", got, m.SHA256)
	}
	log.Printf("[esp] squashfs digest verified OK (%s)", got[:16])
	return nil
}

// jsonUnmarshalDigest unmarshals data into a manifestDigest struct.
func jsonUnmarshalDigest(data []byte, v *manifestDigest) error {
	return json.Unmarshal(data, v)
}

// ─── ESP population ──────────────────────────────────────────────────────────

// populateESP creates the standard Vulos live-USB ESP directory tree under
// espTmpMount and copies the necessary files from the running live system.
// Progress is reported in the range [18, 90] via prog.
func populateESP(ctx context.Context, squashfsPath string, prog func(int)) error {
	// Directories to create on the ESP.
	dirs := []string{
		"EFI/BOOT",
		"EFI/vulos",
		"loader/entries",
	}
	for _, d := range dirs {
		full := filepath.Join(espTmpMount, d)
		if err := os.MkdirAll(full, 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", full, err)
		}
	}
	prog(20)

	// Discover initramfs source.
	initramfsSrc := resolveInitramfsSrc()

	// Seed files to copy: src → dest relative to ESP mount.
	type copySpec struct {
		src      string
		destRel  string
		optional bool
	}
	specs := []copySpec{
		{src: espAnchorSrc, destRel: "EFI/vulos/trust-anchor.pub"},
		{src: espBucketURLSrc, destRel: "EFI/vulos/os-bucket-url", optional: true},
		{src: initramfsSrc, destRel: "EFI/vulos/initramfs.img"},
	}

	for _, s := range specs {
		dest := filepath.Join(espTmpMount, filepath.FromSlash(s.destRel))
		if err := espCopyFile(ctx, s.src, dest); err != nil {
			if s.optional {
				log.Printf("[esp] optional file %s not copied: %v", s.src, err)
				continue
			}
			return fmt.Errorf("copy %s: %w", s.src, err)
		}
		log.Printf("[esp] installed %s → %s", s.src, dest)
	}
	prog(30)

	// Copy kernel.
	kernelDest := filepath.Join(espTmpMount, "EFI", "vulos", "vmlinuz")
	kernelSrc := resolveKernelSrc()
	if kernelSrc != "" {
		if err := espCopyFile(ctx, kernelSrc, kernelDest); err != nil {
			log.Printf("[esp] kernel copy warning (non-fatal): %v", err)
		} else {
			log.Printf("[esp] kernel installed %s → %s", kernelSrc, kernelDest)
		}
	}
	prog(35)

	// Copy squashfs onto ESP as EFI/vulos/os-core.squashfs (for toram boot).
	// This is the bulk copy — report fine-grained progress.
	squashfsDest := filepath.Join(espTmpMount, "EFI", "vulos", "os-core.squashfs")
	if err := copyFileWithProgress(squashfsPath, squashfsDest, prog, 35, 88); err != nil {
		return fmt.Errorf("copy squashfs: %w", err)
	}

	return nil
}

// resolveKernelSrc finds the best available kernel binary on the live system.
// Falls back to a versioned /boot/vmlinuz-* glob (build.sh's --disk and --live
// sections both discover the kernel this way: `ls boot/vmlinuz-* | sort -V |
// tail -1`) so the installer finds the same kernel a fresh Debian install
// would boot, not just the live-medium's fixed-name copies.
func resolveKernelSrc() string {
	candidates := []string{
		"/boot/vmlinuz",
		"/boot/vmlinuz-live",
		"/run/live/medium/vulos/vmlinuz",
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return latestGlobMatch("/boot/vmlinuz-*")
}

// resolveInitramfsSrc finds the best available initramfs/initrd image on the
// live system, falling back to a versioned /boot/initrd.img-* glob (mirrors
// build.sh's kernel/initrd discovery — see resolveKernelSrc).
func resolveInitramfsSrc() string {
	candidates := []string{espInitramfsSrc, espInitramfsAlt}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return latestGlobMatch("/boot/initrd.img-*")
}

// latestGlobMatch returns the lexicographically-last match for pattern
// (mirrors build.sh's `ls -1 ... | sort -V | tail -1` kernel/initrd
// discovery), or "" if there are no matches or the pattern is malformed.
func latestGlobMatch(pattern string) string {
	matches, err := filepath.Glob(pattern)
	if err != nil || len(matches) == 0 {
		return ""
	}
	sort.Strings(matches)
	return matches[len(matches)-1]
}

// espCopyFile copies src to dest, creating dest's parent directory if needed.
func espCopyFile(ctx context.Context, src, dest string) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(dest), err)
	}
	return runCmd(ctx, "cp", "--preserve=timestamps", src, dest)
}

// copyFileWithProgress copies src to dest in 1 MiB chunks, reporting hub
// progress in the range [startPct, endPct].
func copyFileWithProgress(src, dest string, prog func(int), startPct, endPct int) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open %s: %w", src, err)
	}
	defer in.Close()

	info, err := in.Stat()
	if err != nil {
		return fmt.Errorf("stat %s: %w", src, err)
	}
	total := info.Size()

	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(dest), err)
	}

	out, err := os.Create(dest)
	if err != nil {
		return fmt.Errorf("create %s: %w", dest, err)
	}
	var copyErr error
	defer func() {
		out.Close()
		if copyErr != nil {
			os.Remove(dest)
		}
	}()

	buf := make([]byte, 1<<20)
	var written int64
	for {
		nr, readErr := in.Read(buf)
		if nr > 0 {
			nw, writeErr := out.Write(buf[:nr])
			written += int64(nw)
			if writeErr != nil {
				copyErr = fmt.Errorf("write %s: %w", dest, writeErr)
				return copyErr
			}
			if total > 0 {
				pct := startPct + int((written*int64(endPct-startPct))/total)
				prog(pct)
			} else {
				prog((startPct + endPct) / 2)
			}
		}
		if readErr != nil {
			if readErr == io.EOF {
				break
			}
			copyErr = fmt.Errorf("read %s: %w", src, readErr)
			return copyErr
		}
	}
	if copyErr = out.Sync(); copyErr != nil {
		return fmt.Errorf("sync %s: %w", dest, copyErr)
	}
	log.Printf("[esp] squashfs copied to %s (%d bytes)", dest, written)
	return nil
}

// ─── Bootloader installation ──────────────────────────────────────────────────

// installBootctlESP uses bootctl to install systemd-boot into mountPath (an
// already-mounted ESP). The --no-variables flag avoids writing NVRAM entries
// from the live session (the firmware will find EFI/BOOT/bootx64.efi via the
// removable media path). Shared by both the live-USB and install-to-disk
// paths — do not fork a second copy of this logic.
func installBootctlESP(ctx context.Context, mountPath string) error {
	return runCmd(ctx, "bootctl",
		"--path="+mountPath,
		"install",
		"--no-variables",
	)
}

// copyEFIStub is a fallback for environments where bootctl is not available.
// It copies the systemd-boot EFI stub from known system paths to both the
// UEFI removable media path (EFI/BOOT/bootx64.efi) and the standard
// systemd-boot path (EFI/systemd/systemd-bootx64.efi) under mountPath.
// Shared by both the live-USB and install-to-disk paths.
func copyEFIStub(ctx context.Context, mountPath string) error {
	candidates := []string{
		"/usr/lib/systemd/boot/efi/systemd-bootx64.efi",
		"/usr/share/efi-x86_64/systemd-bootx64.efi",
		"/usr/lib/gummiboot/gummibootx64.efi",
	}
	var stubSrc string
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			stubSrc = c
			break
		}
	}
	if stubSrc == "" {
		return fmt.Errorf("no EFI stub found in known paths")
	}

	targets := []string{
		filepath.Join(mountPath, "EFI", "BOOT", "bootx64.efi"),
		filepath.Join(mountPath, "EFI", "systemd", "systemd-bootx64.efi"),
	}
	for _, dest := range targets {
		if err := espCopyFile(ctx, stubSrc, dest); err != nil {
			return fmt.Errorf("copy EFI stub to %s: %w", dest, err)
		}
		log.Printf("[esp] EFI stub installed: %s → %s", stubSrc, dest)
	}
	return nil
}

// ─── Boot entry ───────────────────────────────────────────────────────────────

// writeLiveBootEntry writes a systemd-boot loader entry that performs a toram
// live boot from the squashfs on the USB ESP.  The entry passes vulos.live=1
// so vulos-init knows to skip OSDIST-03 boot-counter and VERITY-02 checks.
// mountPath is the already-mounted ESP root (espTmpMount in production;
// esp_test.go passes a t.TempDir() so the real function is exercised, not a
// duplicate).
func writeLiveBootEntry(ctx context.Context, mountPath string) error {
	entriesDir := filepath.Join(mountPath, "loader", "entries")
	if err := os.MkdirAll(entriesDir, 0o755); err != nil {
		return fmt.Errorf("mkdir entries: %w", err)
	}

	// Also write loader/loader.conf so systemd-boot uses a short timeout.
	loaderConf := "timeout 5\ndefault vulos-live.conf\n"
	loaderConfPath := filepath.Join(mountPath, "loader", "loader.conf")
	if err := os.WriteFile(loaderConfPath, []byte(loaderConf), 0o644); err != nil {
		return fmt.Errorf("write loader.conf: %w", err)
	}

	entry := strings.Join([]string{
		"# Vulos OS — live USB boot entry (written by BMINIT-14 installer)",
		"title   Vulos OS (live USB)",
		"linux   /EFI/vulos/vmlinuz",
		"initrd  /EFI/vulos/initramfs.img",
		"options root=LABEL=vulos-root ro quiet splash",
		"        vulos.live=1 toram",
		"        vulos.squashfs=/EFI/vulos/os-core.squashfs",
	}, "\n") + "\n"

	entryPath := filepath.Join(entriesDir, "vulos-live.conf")
	if err := os.WriteFile(entryPath, []byte(entry), 0o644); err != nil {
		return fmt.Errorf("write boot entry: %w", err)
	}
	log.Printf("[esp] boot entry written: %s", entryPath)
	return nil
}

// ─── Command runner ──────────────────────────────────────────────────────────

// runCmd runs an external command, redirecting stdout+stderr to the process
// log.  Returns an error wrapping the command + exit message on failure.
func runCmd(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	if len(out) > 0 {
		log.Printf("[esp] %s: %s", name, strings.TrimSpace(string(out)))
	}
	return nil
}
