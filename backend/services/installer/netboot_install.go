package installer

// netboot_install.go — NETB-03: netboot-to-install pipeline.
//
// # Overview
//
// When the OS boots from network/RAM (live squashfs with writable overlay,
// started via the iPXE netboot path, NETB-01/NETB-04), the user can click
// "Install Vulos".  This file drives that transition from ephemeral network
// boot to a fully self-contained local installation.
//
// The pipeline writes two things to the local disk:
//
//  1. The **seed** — the irreducible set of files that makes the device
//     trustworthy and self-bootable without network:
//     - systemd-boot EFI binary (bootloader)
//     - Vulos verify-capable initramfs
//     - Trust-anchor public key (/etc/vulos/trust-anchor.pub, baked by SEED-01)
//     - OS bucket URL (/etc/vulos/os-bucket-url, from SEED-02)
//     - Signed bootloader entry pointing at the slot-a squashfs
//
//  2. The **first OS squashfs** — staged into slot-a of the OSDIST-02 A/B
//     slot layout (/var/cache/vulos/slot-a/os-core.squashfs on the target)
//     together with an initial boot-state.json (active=a, counter=0).
//
// After installation the machine boots locally.  Subsequent OS updates are
// fetched from the public OS bucket (OSDIST-04) — the device is NOT diskless.
//
// # Disk layout produced
//
//	/dev/sdX1  — ESP  (FAT32, 512 MiB, label EFI)
//	             └─ EFI/systemd/systemd-bootx64.efi  (bootloader)
//	             └─ EFI/vulos/initramfs.img            (verify-capable initramfs)
//	             └─ EFI/vulos/trust-anchor.pub         (SEED-01 baked key)
//	             └─ EFI/vulos/os-bucket-url            (SEED-02 bucket URL)
//	             └─ loader/entries/vulos-slot-a.conf   (systemd-boot entry)
//	/dev/sdX2  — root (ext4, label vulos-root, remainder of disk)
//	             └─ /var/cache/vulos/slot-a/os-core.squashfs
//	             └─ /var/cache/vulos/boot-state.json
//
// # Relationship to the existing BMINIT-12 installer
//
// The netboot install path is deliberately separate from the rsync-based live-USB
// installer (runInstall / BMINIT-12).  Both share partition/format/mount/unmount
// helpers via the same Service, but netboot install copies individual seed files
// and the squashfs image rather than rsyncing the running rootfs.
//
// # Source paths (on the running netboot/live-RAM system)
//
// All source files are read from the running system — they are already in RAM or
// on the live medium:
//
//	Squashfs      — NetbootInstallRequest.SquashfsPath
//	                (default: /run/live/medium/vulos/os-core.squashfs)
//	Trust anchor  — /etc/vulos/trust-anchor.pub  (baked by SEED-01)
//	Bucket URL    — /etc/vulos/os-bucket-url     (written by SEED-02)
//	Bootloader    — discovered via bootctl (systemd-boot) or a fixed path
//	Initramfs     — /boot/initramfs-vulos.img or kernel cmdline initrd= value

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"vulos/backend/services/osdist"
)

// ---------------------------------------------------------------------------
// Well-known source paths (on the running netboot/live-RAM system)
// ---------------------------------------------------------------------------

const (
	// defaultNetbootSquashfsPath is the standard location of the squashfs on
	// the netboot live medium.  Override via NetbootInstallRequest.SquashfsPath.
	defaultNetbootSquashfsPath = "/run/live/medium/vulos/os-core.squashfs"

	// seedAnchorSrc is where SEED-01 bakes the trust-anchor public key.
	seedAnchorSrc = "/etc/vulos/trust-anchor.pub"

	// seedBucketURLSrc is where the build embeds the OS bucket URL (SEED-02).
	seedBucketURLSrc = "/etc/vulos/os-bucket-url"

	// netbootInitramfsSrc is the verify-capable initramfs on the live system.
	netbootInitramfsSrc = "/boot/initramfs-vulos.img"

	// initramfsAltSrc is the fallback path used by many Debian live-boot setups.
	initramfsAltSrc = "/boot/initrd.img"

	// vulosCacheRelPath is the path (relative to the root mount) where the
	// OSDIST-02 A/B slot cache lives.  Matches vulosCacheDir in cmd/init/main.go.
	vulosCacheRelPath = "var/cache/vulos"

	// squashfsDestName is the filename stored inside each slot directory.
	squashfsDestName = "os-core.squashfs"

	// netbootInstallMount is the temporary mount point used during netboot install.
	// Kept separate from targetMount so both install paths can coexist.
	netbootInstallMount = "/mnt/vulos-netboot-install"
)

// ---------------------------------------------------------------------------
// Request / response types
// ---------------------------------------------------------------------------

// NetbootInstallRequest is the JSON body for POST /api/installer/netboot-install.
type NetbootInstallRequest struct {
	// Disk is the target block device name (e.g. "sda", "nvme0n1").
	Disk string `json:"disk"`
	// Confirm must be true; absent/false rejects the request so an accidental
	// POST never triggers a destructive wipe.
	Confirm bool `json:"confirm"`
	// SquashfsPath is the local path to the OS squashfs image to install.
	// Defaults to defaultNetbootSquashfsPath when empty.
	SquashfsPath string `json:"squashfs_path,omitempty"`
}

// ---------------------------------------------------------------------------
// Service extension — netboot hub + handler
// ---------------------------------------------------------------------------

// netbootMu mirrors the existing hub/mu pattern for progress tracking, keyed
// per Service to keep state for concurrent WS clients.
var (
	netbootMu   sync.Mutex
	netbootHubs = make(map[*Service]*progressHub)
)

// netbootProgressHub returns the current in-progress netboot hub for svc, or
// nil if no netboot install is running.
func netbootProgressHub(svc *Service) *progressHub {
	netbootMu.Lock()
	defer netbootMu.Unlock()
	return netbootHubs[svc]
}

// registerNetbootHub atomically stores the hub for svc, replacing any previous
// completed hub.  Returns an error if an install is already running.
func registerNetbootHub(svc *Service, h *progressHub) error {
	netbootMu.Lock()
	defer netbootMu.Unlock()
	if prev := netbootHubs[svc]; prev != nil {
		if done, _ := prev.isDone(); !done {
			return fmt.Errorf("netboot installation already in progress")
		}
	}
	netbootHubs[svc] = h
	return nil
}

// RegisterNetbootHandlers wires the netboot-install endpoints onto mux.
// Call this alongside RegisterHandlers so that both install paths are served.
func RegisterNetbootHandlers(mux *http.ServeMux, svc *Service) {
	mux.HandleFunc("/api/installer/netboot-install", svc.handleNetbootInstall)
	mux.HandleFunc("/api/installer/netboot-progress", svc.handleNetbootProgress)
}

// handleNetbootInstall serves POST /api/installer/netboot-install.
// It starts the async netboot-to-install pipeline and immediately returns 202.
func (s *Service) handleNetbootInstall(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// SEC: netboot-to-install wipes the target disk and writes a permanent OS —
	// a destructive privileged operation, identical in blast radius to the
	// live-USB /install endpoint.  Gate it on the same admin predicate so an
	// unauthenticated caller cannot trigger a wipe or point SquashfsPath at an
	// arbitrary local file.  When isAdmin is nil (tests) no check is performed.
	if s.isAdmin != nil && !s.isAdmin(r) {
		http.Error(w, `{"error":"admin only"}`, http.StatusForbidden)
		return
	}

	var req NetbootInstallRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if !req.Confirm {
		http.Error(w, "confirm must be true", http.StatusBadRequest)
		return
	}
	if !validDiskName(req.Disk) {
		http.Error(w, "invalid or missing disk name", http.StatusBadRequest)
		return
	}
	if req.SquashfsPath == "" {
		req.SquashfsPath = defaultNetbootSquashfsPath
	}

	hub := newProgressHub()
	if err := registerNetbootHub(s, hub); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}

	go s.runNetbootInstall(req, hub)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]string{"status": "started"})
}

// handleNetbootProgress serves GET /api/installer/netboot-progress (WebSocket).
// Streams progress messages for the netboot install pipeline.
func (s *Service) handleNetbootProgress(w http.ResponseWriter, r *http.Request) {
	// Reuse the existing handleProgress implementation after wiring up the
	// netboot hub: temporarily point s.hub at the netboot hub, stream, restore.
	// To avoid races we read the hub here and stream directly.
	hub := netbootProgressHub(s)
	if hub == nil {
		http.Error(w, "no netboot install in progress", http.StatusNotFound)
		return
	}

	// Delegate to the generic progress handler by temporarily setting s.hub.
	// This is safe because handleProgress only reads s.hub once.
	s.mu.Lock()
	prev := s.hub
	s.hub = hub
	s.mu.Unlock()

	s.handleProgress(w, r)

	s.mu.Lock()
	s.hub = prev
	s.mu.Unlock()
}

// ---------------------------------------------------------------------------
// Netboot install pipeline
// ---------------------------------------------------------------------------

// runNetbootInstall orchestrates the full netboot-to-install sequence.
// It runs in a goroutine; progress is published to hub.
func (s *Service) runNetbootInstall(req NetbootInstallRequest, hub *progressHub) {
	dev := "/dev/" + req.Disk
	espPart := dev + partSuffix(req.Disk, 1)
	rootPart := dev + partSuffix(req.Disk, 2)
	ctx := context.Background()

	type step struct {
		name string
		pct  int
		fn   func() error
	}

	// VERITY-03: resolved by the verify-verity step below and consumed by
	// stage-squashfs.  A closure variable rather than a Service field so the
	// ordering guarantee is visible right here: the steps run sequentially and
	// staging cannot run before the set that feeds it has been validated.  nil
	// (no verity for this medium) is a legitimate value — see
	// netboot_verity.go.
	var verity *verityArtifacts

	steps := []step{
		// VERITY-03: resolve + VALIDATE the dm-verity siblings FIRST, before the
		// disk is partitioned.  A hash tree that does not describe the image
		// would panic the machine on every subsequent boot (the initramfs hook
		// panics when the verity device cannot be mounted, by design), so an
		// inconsistent medium must cost the operator nothing but a failed
		// install — not their existing disk and not a bricked boot.
		//
		// This step USED to run second, after verify-squashfs, and the order was
		// arbitrary because the two were independent: the signature covered the
		// raw image bytes and bound itself. It no longer does. The release key
		// signs an ImagePayload that NAMES a root hash, so the root hash this
		// step validates is the only thing tying that signature to the bytes on
		// the medium — verify-squashfs now consumes it. See netboot_verify.go.
		{name: "verify-verity", pct: 3, fn: func() error {
			v, err := s.resolveVerityArtifacts(ctx, req.SquashfsPath)
			if err != nil {
				return err
			}
			verity = v
			return nil
		}},
		// SECURITY (NETB-03, THREAT-MODEL #1/#3): verify the squashfs signature
		// chain — pinned anchor → release cert → release key → signed
		// ImagePayload → this image's root hash — BEFORE anything destructive
		// happens. Still before the disk is partitioned, because the image and
		// the pinned anchor are already in RAM on the live medium and depend on
		// nothing we write. Fail-closed: a tampered/substituted/downgraded image
		// aborts the install with the TARGET DISK STILL INTACT (no wipe, no
		// format), and of course before any byte reaches the permanent slot.
		{name: "verify-squashfs", pct: 4, fn: func() error {
			var rootHash string
			if verity != nil {
				rootHash = verity.rootHashHex
			}
			return s.verifyNetbootSquashfs(req.SquashfsPath, s.netbootVerifyConfig(), rootHash)
		}},
		{name: "partition", pct: 5, fn: func() error {
			return s.partition(ctx, dev)
		}},
		{name: "format-esp", pct: 8, fn: func() error {
			return s.formatESP(ctx, espPart)
		}},
		{name: "format-root", pct: 12, fn: func() error {
			return s.formatRoot(ctx, rootPart)
		}},
		{name: "mount", pct: 15, fn: func() error {
			return s.mountNetboot(ctx, espPart, rootPart)
		}},
		// OWNSTATE-01: create the directories the initramfs binds the owner's
		// state out of. This has to happen at install time and nowhere else —
		// see createOwnerStateDirs.
		{name: "state-dirs", pct: 17, fn: func() error {
			return s.createOwnerStateDirs(ctx, netbootInstallMount)
		}},
		{name: "write-seed", pct: 30, fn: func() error {
			return s.writeSeedFiles(ctx)
		}},
		{name: "stage-squashfs", pct: 85, fn: func() error {
			return s.stageFirstSquashfs(ctx, req.SquashfsPath, verity, hub)
		}},
		{name: "write-boot-state", pct: 88, fn: func() error {
			return s.writeInitialBootState(ctx)
		}},
		{name: "fstab", pct: 92, fn: func() error {
			return s.writeFstabNetboot(ctx, espPart, rootPart)
		}},
		{name: "bootloader", pct: 97, fn: func() error {
			return s.installNetbootLoader(ctx)
		}},
		{name: "unmount", pct: 99, fn: func() error {
			return s.unmountNetboot(ctx)
		}},
		{name: "reboot", pct: 100, fn: func() error {
			return s.rebootIntoLocal(ctx)
		}},
	}

	hub.publish(0)

	for i, st := range steps {
		log.Printf("[netboot-install] step %d/%d: %s", i+1, len(steps), st.name)
		if err := st.fn(); err != nil {
			log.Printf("[netboot-install] %s failed: %v", st.name, err)
			// Best-effort unmount before reporting failure (ignore error).
			s.unmountNetboot(context.Background()) //nolint:errcheck
			hub.setDone(fmt.Errorf("%s: %w", st.name, err))
			return
		}
		hub.publish(st.pct)
	}

	hub.setDone(nil)
}

// ---------------------------------------------------------------------------
// Mount helpers (netboot paths)
// ---------------------------------------------------------------------------

// mountNetboot mounts root then ESP under netbootInstallMount.
func (s *Service) mountNetboot(ctx context.Context, espPart, rootPart string) error {
	if _, err := s.cmd.Output(ctx, "mkdir", "-p", netbootInstallMount); err != nil {
		return err
	}
	if _, err := s.cmd.Output(ctx, "mount", rootPart, netbootInstallMount); err != nil {
		return err
	}
	espTarget := netbootInstallMount + "/boot/efi"
	if _, err := s.cmd.Output(ctx, "mkdir", "-p", espTarget); err != nil {
		return err
	}
	if _, err := s.cmd.Output(ctx, "mount", espPart, espTarget); err != nil {
		return err
	}
	return nil
}

// unmountNetboot tears down the netboot install mount points.
func (s *Service) unmountNetboot(ctx context.Context) error {
	s.cmd.Output(ctx, "umount", netbootInstallMount+"/boot/efi") //nolint:errcheck
	s.cmd.Output(ctx, "umount", netbootInstallMount)             //nolint:errcheck
	return nil
}

// ---------------------------------------------------------------------------
// Seed file installation
// ---------------------------------------------------------------------------

// seedCopySpec describes one file to copy from the live system to the target ESP.
type seedCopySpec struct {
	// src is the source path on the running live system.
	src string
	// destRel is the destination path relative to the ESP mount (boot/efi).
	destRel string
	// optional — when true, absence of src is logged but not fatal.
	optional bool
}

// writeSeedFiles copies the seed artifacts (bootloader EFI, initramfs, trust-anchor
// public key, OS bucket URL) from the running live system into the target ESP,
// then writes a signed systemd-boot loader entry pointing at slot-a.
//
// Seed file layout on the ESP (relative to the ESP mount):
//
//	EFI/systemd/systemd-bootx64.efi   — bootloader (from bootctl --print-efi-path)
//	EFI/vulos/initramfs.img           — verify-capable initramfs
//	EFI/vulos/trust-anchor.pub        — SEED-01 baked trust-anchor key
//	EFI/vulos/os-bucket-url           — SEED-02 bucket URL
//	loader/entries/vulos-slot-a.conf  — boot entry for slot-a
func (s *Service) writeSeedFiles(ctx context.Context) error {
	espMount := netbootInstallMount + "/boot/efi"

	// Discover the source initramfs — try the Vulos-specific path first,
	// fall back to the standard Debian/live path.
	initramfsSrc := netbootInitramfsSrc
	if _, err := os.Stat(initramfsSrc); os.IsNotExist(err) {
		initramfsSrc = initramfsAltSrc
	}

	specs := []seedCopySpec{
		// Trust anchor — mandatory; absent means the build is broken (SEED-01).
		{src: seedAnchorSrc, destRel: "EFI/vulos/trust-anchor.pub"},
		// OS bucket URL — optional (device can still update if missing, but
		// the default embedded path must then be relied upon).
		{src: seedBucketURLSrc, destRel: "EFI/vulos/os-bucket-url", optional: true},
		// Verify-capable initramfs — mandatory.
		{src: initramfsSrc, destRel: "EFI/vulos/initramfs.img"},
	}

	for _, spec := range specs {
		destPath := filepath.Join(espMount, filepath.FromSlash(spec.destRel))
		if err := s.copySeedFile(ctx, spec.src, destPath); err != nil {
			if spec.optional {
				log.Printf("[netboot-install] optional seed file %s not copied: %v", spec.src, err)
				continue
			}
			return fmt.Errorf("seed file %s: %w", spec.src, err)
		}
		log.Printf("[netboot-install] seed file installed: %s → %s", spec.src, destPath)
	}

	// Install the systemd-boot EFI binary via bootctl so it lands at the
	// firmware-discoverable path (EFI/systemd/systemd-bootx64.efi) and the
	// NVRAM entry is updated.
	if err := s.installNetbootBootctl(ctx, espMount); err != nil {
		return fmt.Errorf("bootctl: %w", err)
	}

	// Write the boot entry for slot-a.
	if err := s.writeSlotABootEntry(ctx, espMount); err != nil {
		return fmt.Errorf("boot entry: %w", err)
	}

	return nil
}

// writeFileViaCommander writes content to path through s.cmd, so callers stay
// testable with a mock Commander exactly like every other step in this
// package (never real filesystem I/O against hardcoded absolute paths like
// /mnt/vulos-netboot-install under test).
//
// It does NOT do what the previous shape at all three of this package's
// content-writing call sites did:
//
//	s.cmd.Output(ctx, "sh", "-c", fmt.Sprintf("printf '%%s' %q > %s", content, path))
//
// Go's %q verb renders a real newline byte as the TWO CHARACTERS '\' 'n'
// (Go-string-literal escaping) when it interpolates content into the "-c"
// script text, and `printf '%s'` never re-interprets backslash escapes — so
// every multi-line entry.conf/fstab this package ever wrote landed on the
// target disk as ONE line with literal "\n" text instead of real newlines.
// systemd-boot silently ignores a boot entry it can't parse that way: the
// ESP came out with a seed, a kernel, an initramfs and a bootloader binary
// that all checked out, and NOTHING to boot. Found by actually booting a
// netboot-installed disk in QEMU (scripts/netboot-install-smoke.sh) — no
// mocked unit test exercises real shell quoting, only the in-memory string.
//
// The fix passes content as a shell POSITIONAL PARAMETER ($1) rather than
// interpolating it into the script text at all, so no Go-vs-shell escaping
// translation ever happens: printf '%s' "$1" emits $1's bytes verbatim,
// newlines included. "sh" as $0 is a placeholder positional parameters start
// at $1.
func (s *Service) writeFileViaCommander(ctx context.Context, content, path string) error {
	_, err := s.cmd.Output(ctx, "sh", "-c", `printf '%s' "$1" > "$2"`, "sh", content, path)
	if err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// copySeedFile copies src to dest, creating parent directories as needed.
// The copy uses the Commander so it is testable with a mock.
func (s *Service) copySeedFile(ctx context.Context, src, dest string) error {
	// Ensure parent directory exists.
	if _, err := s.cmd.Output(ctx, "mkdir", "-p", filepath.Dir(dest)); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(dest), err)
	}
	if _, err := s.cmd.Output(ctx, "cp", "--preserve=timestamps", src, dest); err != nil {
		return fmt.Errorf("cp %s → %s: %w", src, dest, err)
	}
	return nil
}

// installNetbootBootctl runs bootctl to install the systemd-boot EFI binary
// into the target ESP and register an NVRAM boot entry.
func (s *Service) installNetbootBootctl(ctx context.Context, espMount string) error {
	// --path must be given RELATIVE TO --root, not as an absolute host path
	// that already contains the --root prefix. bootctl silently concatenates
	// the two ("<root><path>"), so passing the absolute espMount here
	// produced "<netbootInstallMount><netbootInstallMount>/boot/efi" — a path
	// that never exists — and bootctl failed with "Failed to open parent
	// directory" on EVERY real invocation, aborting the install pipeline at
	// its very first bootctl call. Confirmed against real bootctl (systemd
	// 257): "--path=/boot/efi --root=X" succeeds, "--path=X/boot/efi
	// --root=X" fails. Deriving the relative form from espMount (rather than
	// hardcoding "/boot/efi") keeps this correct if writeSeedFiles' espMount
	// construction ever changes.
	relPath := strings.TrimPrefix(espMount, netbootInstallMount)
	_, err := s.cmd.Output(ctx, "bootctl",
		"--path="+relPath,
		"--root="+netbootInstallMount,
		"install",
		"--no-variables", // avoid writing NVRAM from the live session
	)
	return err
}

// writeSlotABootEntry writes a systemd-boot loader entry that boots slot-a.
// The entry passes vulos.live=0 so the initramfs mounts the squashfs from the
// local slot rather than the live medium.
//
// Entry file: <espMount>/loader/entries/vulos-slot-a.conf
func (s *Service) writeSlotABootEntry(ctx context.Context, espMount string) error {
	entriesDir := filepath.Join(espMount, "loader", "entries")
	if _, err := s.cmd.Output(ctx, "mkdir", "-p", entriesDir); err != nil {
		return fmt.Errorf("mkdir entries: %w", err)
	}

	// Kernel path inside the ESP (systemd-boot requires a path relative to the
	// ESP root, prefixed with '/').  The kernel binary is expected at the same
	// location as on the live ESP — copy it if present.
	kernelRelPath := "/EFI/vulos/vmlinuz"
	kernelSrc := "/boot/vmlinuz"
	if _, err := os.Stat(kernelSrc); os.IsNotExist(err) {
		// Try the live-boot kernel path.
		kernelSrc = ""
		for _, candidate := range []string{
			"/boot/vmlinuz-live",
			"/run/live/medium/vulos/vmlinuz",
		} {
			if _, err := os.Stat(candidate); err == nil {
				kernelSrc = candidate
				break
			}
		}
	}
	if kernelSrc != "" {
		kernelDest := filepath.Join(espMount, "EFI", "vulos", "vmlinuz")
		if _, err := s.cmd.Output(ctx, "mkdir", "-p", filepath.Dir(kernelDest)); err != nil {
			return err
		}
		if _, err := s.cmd.Output(ctx, "cp", "--preserve=timestamps", kernelSrc, kernelDest); err != nil {
			log.Printf("[netboot-install] kernel copy warning (non-fatal): %v", err)
		}
	}

	entry := strings.Join([]string{
		"# Vulos OS — slot-a boot entry (written by NETB-03 installer)",
		"title   Vulos OS (slot-a)",
		"linux   " + kernelRelPath,
		"initrd  /EFI/vulos/initramfs.img",
		// vulos.live=0 is REQUIRED, not decorative. The root partition holds
		// only /var/cache/vulos/slot-a/os-core.squashfs — the OS itself is
		// inside that squashfs — and the only thing that mounts it is the
		// scripts/initramfs/vulos-live hook. That hook is gated on
		// `cmdline_has vulos.live`, which matches a bare token OR a key=value,
		// so "=0" activates it while reading as "not a live-USB session". It
		// also consumes vulos.squashfs=, so without the token that parameter is
		// inert and the machine boots a partition with no OS on it.
		//
		// This line previously omitted the token while the doc comment above
		// claimed it was passed. Nothing caught it because no test tied the
		// entry to the hook's requirement and nothing exercises a real netboot;
		// TestWriteSlotABootEntry_CarriesTokenInitramfsHookRequires does the
		// first half.
		//
		// ALL of it belongs on ONE "options" line. systemd-boot's Boot Loader
		// Specification does allow the options value to be split across
		// multiple lines — but only when EVERY line starts with the literal
		// keyword "options" again; a bare continuation line (leading
		// whitespace, no keyword) is not part of the spec and is silently
		// dropped. This previously split vulos.live=0/vulos.slot=a/
		// vulos.squashfs= onto exactly such a bare continuation line, so —
		// even with the token spelled correctly, the vulos.squashfs=
		// consumption fix in scripts/initramfs/vulos-live, and the bootctl
		// --path fix below — the kernel actually booted with cmdline
		// "initrd=... root=LABEL=vulos-root ro" and NOTHING after it. Found
		// by reading the real `Kernel command line:` line QEMU printed after
		// booting the disk this pipeline produced (scripts/netboot-install-
		// smoke.sh) — no mocked unit test parses a kernel command line, so
		// nothing before this caught it. TestWriteSlotABootEntry_
		// OptionsIsOneLine pins this.
		"options root=LABEL=vulos-root ro quiet splash vulos.live=0 vulos.slot=a vulos.squashfs=/var/cache/vulos/slot-a/os-core.squashfs",
	}, "\n") + "\n"

	entryPath := filepath.Join(entriesDir, "vulos-slot-a.conf")
	return s.writeFileViaCommander(ctx, entry, entryPath)
}

// ---------------------------------------------------------------------------
// Squashfs staging into slot-a
// ---------------------------------------------------------------------------

// stageFirstSquashfs copies squashfsPath into the OSDIST-02 slot-a directory
// on the target root partition, creating the /var/cache/vulos/slot-a tree, and
// stages the validated dm-verity siblings beside it (VERITY-03).
// Progress is streamed to hub as the copy advances (pct range: 35–85).
//
// To be unambiguous, because this comment has been misread as saying the
// opposite: os-core.hashtree, os-core.roothash and (when the medium ships one)
// os-core.roothash.sig ARE staged into the slot, by stageVerityArtifacts at the
// bottom of stageFirstSquashfsInto.  A netboot-installed disk carries them, and
// scripts/netboot-install-smoke.sh Phase 5 re-runs `veritysetup verify` against
// the staged trio on the disk to prove the copy survived.
//
// The paragraph below is about the ONE case where nothing is staged:
//
// verity may be nil — no verity artifacts on this medium, or none that could be
// validated here.  Only then does the disk boot exactly as it did before
// VERITY-03: the initramfs finds no hashtree/roothash and takes its documented
// unverified loop-mount fallback.
func (s *Service) stageFirstSquashfs(ctx context.Context, squashfsPath string, verity *verityArtifacts, hub *progressHub) error {
	return s.stageFirstSquashfsInto(ctx, netbootInstallMount, squashfsPath, verity, hub)
}

// stageFirstSquashfsInto is stageFirstSquashfs against an explicit root mount
// point.  Split out so the staging layout — which files land in a slot under
// which names — is testable without a real disk; netbootInstallMount is a
// package const and nothing could otherwise observe what this writes.
func (s *Service) stageFirstSquashfsInto(ctx context.Context, root, squashfsPath string, verity *verityArtifacts, hub *progressHub) error {
	cacheDir := filepath.Join(root, vulosCacheRelPath)

	// NewSlotManager creates slot-a / slot-b directories.
	sm, err := osdist.NewSlotManager(cacheDir)
	if err != nil {
		return fmt.Errorf("create slot manager: %w", err)
	}

	// Slot-a is used for the initial install; there is no prior active slot
	// so we stage directly without the "don't overwrite active" guard.
	slotADir, err := sm.SlotDir(osdist.SlotA)
	if err != nil {
		return fmt.Errorf("slot-a dir: %w", err)
	}

	destPath := filepath.Join(slotADir, squashfsDestName)
	if err := copyWithProgress(squashfsPath, destPath, hub, 35, 85); err != nil {
		return fmt.Errorf("copy squashfs: %w", err)
	}
	log.Printf("[netboot-install] squashfs staged to %s", destPath)

	// The image is on disk; now its verity siblings, under the os-core.* names
	// the initramfs derives from the boot entry's vulos.squashfs= path.
	if err := stageVerityArtifacts(verity, slotADir); err != nil {
		return err
	}
	return nil
}

// copyWithProgress streams src to dest, publishing hub progress in the range
// [startPct, endPct] as the copy advances.  If the source size is unknown (0)
// progress is pulsed to midpoint.
func copyWithProgress(src, dest string, hub *progressHub, startPct, endPct int) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open %s: %w", src, err)
	}
	defer in.Close()

	// Get file size for progress calculation.
	info, err := in.Stat()
	if err != nil {
		return fmt.Errorf("stat %s: %w", src, err)
	}
	total := info.Size()

	// Ensure parent directory.
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(dest), err)
	}

	out, err := os.Create(dest)
	if err != nil {
		return fmt.Errorf("create %s: %w", dest, err)
	}
	defer func() {
		out.Close()
		if err != nil {
			os.Remove(dest)
		}
	}()

	const bufSize = 1 << 20 // 1 MiB
	buf := make([]byte, bufSize)
	var written int64

	for {
		nr, readErr := in.Read(buf)
		if nr > 0 {
			nw, writeErr := out.Write(buf[:nr])
			written += int64(nw)
			if writeErr != nil {
				err = fmt.Errorf("write %s: %w", dest, writeErr)
				return err
			}
			// Publish progress.
			if total > 0 {
				pct := startPct + int((written*int64(endPct-startPct))/total)
				hub.publish(pct)
			} else {
				// Unknown size: pulse to midpoint.
				hub.publish((startPct + endPct) / 2)
			}
		}
		if readErr != nil {
			if readErr == io.EOF {
				break
			}
			err = fmt.Errorf("read %s: %w", src, readErr)
			return err
		}
	}

	if err = out.Sync(); err != nil {
		return fmt.Errorf("sync %s: %w", dest, err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Initial boot-state.json
// ---------------------------------------------------------------------------

// writeInitialBootState creates /var/cache/vulos/boot-state.json on the target
// with active=slot-a, counter=0, last_known_good=a.  This ensures the first
// local boot picks slot-a and the OSDIST-03 counter starts clean.
func (s *Service) writeInitialBootState(ctx context.Context) error {
	cacheDir := filepath.Join(netbootInstallMount, vulosCacheRelPath)

	sm, err := osdist.NewSlotManager(cacheDir)
	if err != nil {
		return fmt.Errorf("slot manager for boot-state: %w", err)
	}

	bs := &osdist.BootState{
		Active:        osdist.SlotA,
		Pending:       osdist.SlotNone,
		BootCounter:   0,
		LastKnownGood: osdist.SlotA,
	}
	if err := sm.Save(bs); err != nil {
		return fmt.Errorf("save boot-state: %w", err)
	}
	log.Printf("[netboot-install] boot-state.json written (active=a, counter=0, last_known_good=a)")
	return nil
}

// ---------------------------------------------------------------------------
// OWNSTATE-01 — the owner's state directories
// ---------------------------------------------------------------------------

// ownerStateDirs are the directories that must exist on the installed root
// partition for scripts/initramfs/vulos-live to be able to keep the owner's
// account on the disk. Each is (path relative to the partition root, mode).
//
// Why a mode matters here and not for /var/cache/vulos: what lands under
// /root/.vulos is not a cache. It is auth.db (the owner's password hash, live
// sessions, the encrypted recovery and master-key blobs), auth.key (the secret
// every session cookie is signed with), the box's Ed25519 peering private key,
// the software device key, and the credential/TOTP vaults. None of it is
// encrypted at rest — this repository ships no LUKS on any path, deliberately
// (build.sh installs cryptsetup-bin, not cryptsetup) — so 0700 is the only
// access control there is once the machine is running.
var ownerStateDirs = []struct {
	rel  string
	mode string
}{
	// The data directory backend/internal/datadir resolves to on a shipped box:
	// the server unit sets HOME=/root and never sets VULOS_DATA_DIR.
	{"root/.vulos", "0700"},
	// A mountpoint, not a store. The initramfs mounts a tmpfs back over this so
	// installed-app manifests keep dying with the Flatpak/apt payload they point
	// at (roadmap/APP-DIR-PERSISTENCE.md). Creating it here means the hook never
	// has to mkdir into $rootmnt to get a mountpoint.
	{"root/.vulos/apps", "0755"},
	// NOT under the data dir: cmd/server hardcodes /var/lib/vulos for the
	// .setup-complete marker, the LAN TLS material and the signing epoch floor.
	{"var/lib/vulos", "0755"},
}

// createOwnerStateDirs lays down the owner-state directories on the freshly
// formatted root partition.
//
// It has to be the installer that does this. The initramfs cannot: until its
// final rebind, that partition is $rootmnt mounted read-only, and a mkdir into
// $rootmnt is exactly the failure roadmap/BOOT-FOUR-ERRORS.md is about. The
// running OS cannot either: once the hook binds the overlay over $rootmnt the
// partition is unreachable by path for the life of the machine. So a disk whose
// installer did not run this step keeps its owner state in RAM, and the hook
// says so on the console rather than pretending otherwise.
func (s *Service) createOwnerStateDirs(ctx context.Context, root string) error {
	for _, d := range ownerStateDirs {
		target := filepath.Join(root, d.rel)
		if _, err := s.cmd.Output(ctx, "mkdir", "-p", "-m", d.mode, target); err != nil {
			return fmt.Errorf("mkdir %s: %w", target, err)
		}
		// -m applies to the final component only; a parent created along the way
		// (/root, /var/lib) keeps the umask default, which is correct — the
		// secrecy that matters is on .vulos itself.
		if _, err := s.cmd.Output(ctx, "chmod", d.mode, target); err != nil {
			return fmt.Errorf("chmod %s %s: %w", d.mode, target, err)
		}
	}
	log.Printf("[netboot-install] owner-state dirs created: /root/.vulos (0700), /root/.vulos/apps, /var/lib/vulos")
	return nil
}

// ---------------------------------------------------------------------------
// fstab
// ---------------------------------------------------------------------------

// writeFstabNetboot writes /etc/fstab on the target root partition.
// It mirrors writeFstab but uses netbootInstallMount.
func (s *Service) writeFstabNetboot(ctx context.Context, espPart, rootPart string) error {
	rootUUID, err := s.partUUID(ctx, rootPart)
	if err != nil {
		return fmt.Errorf("root UUID: %w", err)
	}
	espUUID, err := s.partUUID(ctx, espPart)
	if err != nil {
		return fmt.Errorf("esp UUID: %w", err)
	}

	fstab := fmt.Sprintf(
		"# Generated by Vulos OS netboot installer (NETB-03)\n"+
			"UUID=%s  /          ext4  defaults,noatime  0 1\n"+
			"UUID=%s  /boot/efi  vfat  umask=0077        0 2\n",
		rootUUID, espUUID,
	)

	etcDir := netbootInstallMount + "/etc"
	if _, err := s.cmd.Output(ctx, "mkdir", "-p", etcDir); err != nil {
		return fmt.Errorf("mkdir etc: %w", err)
	}
	return s.writeFileViaCommander(ctx, fstab, netbootInstallMount+"/etc/fstab")
}

// ---------------------------------------------------------------------------
// Bootloader install
// ---------------------------------------------------------------------------

// installNetbootLoader runs bootctl to install systemd-boot into the target
// ESP and set it as the default boot entry.
func (s *Service) installNetbootLoader(ctx context.Context) error {
	// See installNetbootBootctl's comment: --path must be relative to --root,
	// not the absolute (and therefore double-prefixed) espMount.
	_, err := s.cmd.Output(ctx, "bootctl",
		"--path=/boot/efi",
		"--root="+netbootInstallMount,
		"install",
	)
	return err
}

// ---------------------------------------------------------------------------
// Reboot into local installation
// ---------------------------------------------------------------------------

// rebootIntoLocal triggers a system reboot so the machine boots from the
// freshly installed local disk rather than the network.
// It prefers `systemctl reboot` (if systemd is running) and falls back to
// the `reboot` binary.  This call does NOT return on success.
func (s *Service) rebootIntoLocal(ctx context.Context) error {
	log.Println("[netboot-install] installation complete — rebooting into local disk")

	// Try systemctl reboot first (respects running services).
	if _, err := s.cmd.Output(ctx, "systemctl", "reboot"); err == nil {
		return nil
	}
	// Fall back to the reboot binary.
	_, err := s.cmd.Output(ctx, "reboot")
	return err
}
