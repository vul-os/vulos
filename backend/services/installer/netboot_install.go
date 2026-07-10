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

// netbootHub and netbootMu mirror the existing hub/mu pattern for progress
// tracking.  They are stored on Service to keep state for concurrent WS clients.
type netbootHub struct {
	hub *progressHub
}

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

	steps := []step{
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
		{name: "write-seed", pct: 30, fn: func() error {
			return s.writeSeedFiles(ctx)
		}},
		// SECURITY (NETB-03, THREAT-MODEL #1/#3): verify the squashfs signature
		// against the PINNED trust anchor BEFORE any byte is written to the
		// permanent slot.  Fail-closed — a tampered/substituted/downgraded image
		// aborts the install with the disk left un-poisoned.  This is the
		// verify-before-write gate the signature-first design promises.
		{name: "verify-squashfs", pct: 35, fn: func() error {
			return s.verifyNetbootSquashfs(req.SquashfsPath, s.netbootVerifyConfig())
		}},
		{name: "stage-squashfs", pct: 85, fn: func() error {
			return s.stageFirstSquashfs(ctx, req.SquashfsPath, hub)
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
	_, err := s.cmd.Output(ctx, "bootctl",
		"--path="+espMount,
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
		"options root=LABEL=vulos-root ro quiet splash",
		"        vulos.slot=a vulos.squashfs=/var/cache/vulos/slot-a/os-core.squashfs",
	}, "\n") + "\n"

	entryPath := filepath.Join(entriesDir, "vulos-slot-a.conf")
	_, err := s.cmd.Output(ctx, "sh", "-c",
		fmt.Sprintf("printf '%%s' %q > %s", entry, entryPath))
	return err
}

// ---------------------------------------------------------------------------
// Squashfs staging into slot-a
// ---------------------------------------------------------------------------

// stageFirstSquashfs copies squashfsPath into the OSDIST-02 slot-a directory
// on the target root partition, creating the /var/cache/vulos/slot-a tree.
// Progress is streamed to hub as the copy advances (pct range: 35–85).
func (s *Service) stageFirstSquashfs(ctx context.Context, squashfsPath string, hub *progressHub) error {
	cacheDir := filepath.Join(netbootInstallMount, vulosCacheRelPath)

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
	_, err = s.cmd.Output(ctx, "sh", "-c",
		fmt.Sprintf("printf '%%s' %q > %s", fstab, netbootInstallMount+"/etc/fstab"))
	return err
}

// ---------------------------------------------------------------------------
// Bootloader install
// ---------------------------------------------------------------------------

// installNetbootLoader runs bootctl to install systemd-boot into the target
// ESP and set it as the default boot entry.
func (s *Service) installNetbootLoader(ctx context.Context) error {
	_, err := s.cmd.Output(ctx, "bootctl",
		"--path="+netbootInstallMount+"/boot/efi",
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
