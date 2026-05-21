//go:build linux

package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"vulos/backend/cmd/verify"
	"vulos/backend/services/gpu"
	"vulos/backend/services/hwdetect"
	"vulos/backend/services/osdist"
)

// slotMgrGlobal and bootStateGlobal hold the SlotManager and BootState after
// incrementBootCounter() runs.  They are set once in main() before any
// goroutines are launched, so no locking is needed.  markBootHealthy reads
// them in startKiosk() once the server is confirmed listening.
var (
	slotMgrGlobal   *osdist.SlotManager
	bootStateGlobal *osdist.BootState
)

// currentProfile holds the live boot-time host decision. Populated once by
// detectHost() before services/kiosk start; read by startKiosk + Chromium
// flag selection so they don't re-probe.
var currentProfile hostProfile

// hostProfile is the runtime decision made once at boot from the live probes
// (gpu/inputs/RAM). It picks compositor env + Chromium kiosk flags + whether
// to pre-warm the remote-browser stream pool. One image, one boot path, no
// "VM build" vs "bare-metal build" — same artifact on QEMU and real metal.
type hostProfile struct {
	HasHWGPU       bool   // real GPU (DRI + a non-llvmpipe driver)
	GPUTier        string // "nvenc"|"vaapi"|"software"
	HasInputs      bool   // ≥1 /dev/input/event*
	MemMB          int    // /proc/meminfo MemTotal
	LowMem         bool   // < 6 GB → skip prewarm, software path
	PrewarmBrowser bool
}

// detectHost runs the live probes and produces the runtime decision.
// Honors a one-shot kernel cmdline override (vulos.profile=software) for the
// QEMU smoke harness; otherwise everything is derived from probes.
func detectHost() hostProfile {
	g := gpu.Detect()
	hasHW := g.HasDRI && g.Tier != gpu.TierSoftware
	if cmd, _ := os.ReadFile("/proc/cmdline"); strings.Contains(string(cmd), "vulos.profile=software") {
		hasHW = false
	}
	// Input devices: count /dev/input/event*
	inputs, _ := filepath.Glob("/dev/input/event*")
	// Memory: parse MemTotal kB from /proc/meminfo
	memMB := 0
	if mb, err := os.ReadFile("/proc/meminfo"); err == nil {
		for _, line := range strings.Split(string(mb), "\n") {
			if strings.HasPrefix(line, "MemTotal:") {
				var kB int
				_, _ = fmt.Sscanf(line, "MemTotal: %d kB", &kB)
				memMB = kB / 1024
				break
			}
		}
	}
	p := hostProfile{
		HasHWGPU:  hasHW,
		GPUTier:   g.TierName,
		HasInputs: len(inputs) > 0,
		MemMB:     memMB,
		LowMem:    memMB > 0 && memMB < 6000,
	}
	// Pre-warm the remote-browser stream only when we can afford it:
	// real GPU + enough RAM. Low-RAM/software-GPU paths warm lazily.
	p.PrewarmBrowser = p.HasHWGPU && !p.LowMem
	log.Printf("[host] profile: gpu_hw=%v tier=%s inputs=%v mem=%dMB prewarm_browser=%v",
		p.HasHWGPU, p.GPUTier, p.HasInputs, p.MemMB, p.PrewarmBrowser)
	return p
}

// vulos-init: Custom PID 1 for Debian Linux.
// Mounts filesystems, starts networking, launches systemd, then hands off to the vulos server.
//
// Build: GOOS=linux GOARCH=amd64 go build -o vulos-init ./cmd/init
// Install: copy to /sbin/init (or set init=/sbin/vulos-init in kernel cmdline)

// ─── Boot-counter / A-B slot rollback (OSDIST-03) ────────────────────────────

// vulosCacheDir is where the A/B slot manager stores boot-state.json.
// The cache partition is mounted at /var/cache/vulos by convention; fall back
// to /run/vulos-cache (tmpfs) if the partition is not present — slots will not
// persist across reboots in that case, but the counter still protects
// against an immediate crash loop within a single session.
const vulosCacheDir = "/var/cache/vulos"

// slotManagerOrNil returns a SlotManager for vulosCacheDir, or nil with a
// warning if the directory cannot be prepared.  All callers treat nil as
// "slot management unavailable — continue without it".
func slotManagerOrNil() *osdist.SlotManager {
	sm, err := osdist.NewSlotManager(vulosCacheDir)
	if err != nil {
		log.Printf("[slots] slot manager unavailable (%v) — skipping boot-counter", err)
		return nil
	}
	return sm
}

// bootThreshold returns the configured rollback threshold.
// Reads VULOS_BOOT_THRESHOLD; defaults to osdist.DefaultBootThreshold (3).
func bootThreshold() int {
	if v := os.Getenv("VULOS_BOOT_THRESHOLD"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return osdist.DefaultBootThreshold
}

// incrementBootCounter increments the persistent boot counter early in the
// init sequence.  If the counter now meets or exceeds the threshold, it
// immediately rolls back to last-known-good so the bootloader uses the safe
// slot on the next reboot, then continues the current boot (we are already
// running; a reboot is triggered by the caller if desired).
//
// Returns the SlotManager and the (possibly updated) BootState so that
// markBootHealthy can use them without re-reading the file.
func incrementBootCounter() (*osdist.SlotManager, *osdist.BootState) {
	sm := slotManagerOrNil()
	if sm == nil {
		return nil, nil
	}

	bs, err := sm.Load()
	if err != nil {
		// boot-state.json absent on first-ever boot — write a fresh one.
		log.Printf("[slots] no boot-state found (%v) — initialising", err)
		bs = &osdist.BootState{
			Active:        osdist.SlotA,
			Pending:       osdist.SlotNone,
			BootCounter:   0,
			LastKnownGood: osdist.SlotA,
		}
		if saveErr := sm.Save(bs); saveErr != nil {
			log.Printf("[slots] could not write initial boot-state: %v", saveErr)
			return nil, nil
		}
	}

	bs, err = sm.IncrementBootCounter(bs)
	if err != nil {
		log.Printf("[slots] could not increment boot counter: %v", err)
		return sm, nil
	}
	log.Printf("[slots] boot_counter=%d (threshold=%d, active=%s, last_known_good=%s)",
		bs.BootCounter, bootThreshold(), bs.Active, bs.LastKnownGood)

	if osdist.ShouldRollback(bs, bootThreshold()) {
		log.Printf("[slots] boot counter exceeded threshold — rolling back to last-known-good slot %s", bs.LastKnownGood)
		rolled, rbErr := sm.Rollback(bs)
		if rbErr != nil {
			log.Printf("[slots] rollback failed: %v", rbErr)
		} else {
			bs = rolled
			log.Printf("[slots] rollback written: active will be %s on next boot", bs.Active)
		}
	}

	return sm, bs
}

// ─── NETB-03: live/netboot boot detection ────────────────────────────────────

// isLiveBoot reports whether the system booted from a live/netboot medium
// (kernel cmdline contains vulos.live=1).  In live-boot mode the root
// filesystem is a squashfs+overlay loaded from the network; there is no
// local stable.json manifest and no slot layout yet.  VERITY-02 verification
// and OSDIST-03 boot-counter management are both skipped in this mode — they
// apply only to a locally-installed system.
func isLiveBoot() bool {
	data, err := os.ReadFile("/proc/cmdline")
	if err != nil {
		return false
	}
	return strings.Contains(string(data), "vulos.live=1")
}

// ─── VERITY-02: OS image verification before services start ──────────────────

// Well-known paths for the squashfs verification gate.
// All paths are absolute; they are accessible after mountAll() because the
// rootfs is already the squashfs (pivot happened in the early initramfs stage).
const (
	// verityManifestPath is where stable.json is expected on the live system.
	// The manifest carries the dm-verity root hash and the signed ImagePayload.
	verityManifestPath = "/etc/vulos/stable.json"

	// verityManifestSigPath is the detached .sig for stable.json.
	verityManifestSigPath = "/etc/vulos/stable.json.sig"

	// verityReleaseCertPath is the release-key certificate (ReleaseCert JSON).
	// Validated against the baked trust anchor before the image sig is trusted.
	verityReleaseCertPath = "/etc/vulos/release-cert.json"

	// verityEpochPath is the persistent epoch floor (signing.DefaultEpochPath).
	verityEpochPath = "/var/lib/vulos/epoch-floor.json"
)

// verityEpochFloor reads the epoch floor from the persistent store.
// Returns 0 on any error (conservative — new device has floor 0).
func verityEpochFloor() int64 {
	data, err := os.ReadFile(verityEpochPath)
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

// verifyOSBeforeBoot is the VERITY-02 pivot gate.
//
// It is called early in main() — after mountAll() (so /etc is readable) but
// before any OS services start.  If verification fails for any reason the
// function logs the error and calls os.Exit(1), halting boot.
//
// Verification steps:
//  1. Load the baked trust anchor (/etc/vulos/trust-anchor.pub).
//  2. Validate the release cert (/etc/vulos/release-cert.json) against the anchor.
//  3. Enforce cert.MinEpoch >= device epoch floor.
//  4. Verify the stable.json.sig over the ImagePayload (release key).
//  5. Check that ImagePayload.RootHash == manifest's roothash field.
//
// Any mismatch is fatal: the OS refuses to start with an unverified image.
// This implements the fail-closed guarantee from roadmap/SIGNING.md.
func verifyOSBeforeBoot() {
	log.Println("[verity] starting OS image verification (VERITY-02)")

	// Read the manifest (stable.json) to extract the expected root hash and
	// construct the ImagePayload that the release key signed.
	manifestData, err := os.ReadFile(verityManifestPath)
	if err != nil {
		log.Fatalf("[verity] HALT: cannot read manifest %q: %v", verityManifestPath, err)
	}

	// Parse the manifest into the ImagePayload structure.
	// We read the fields the release key signed (ImagePayload mirrors ManifestPayload
	// from cmd/sign/pki.go — path, roothash, size, min_epoch, released_at).
	var payload verify.ImagePayload
	if err := json.Unmarshal(manifestData, &payload); err != nil {
		log.Fatalf("[verity] HALT: cannot parse manifest %q: %v", verityManifestPath, err)
	}
	if payload.RootHash == "" || payload.Path == "" {
		log.Fatalf("[verity] HALT: manifest %q missing required fields (roothash/path)", verityManifestPath)
	}

	epochFloor := verityEpochFloor()

	cfg := verify.SquashfsVerifyConfig{
		// AnchorPath defaults to signing.DefaultAnchorPath (/etc/vulos/trust-anchor.pub)
		CertPath:           verityReleaseCertPath,
		SquashfsPath:       "", // not used for ImagePayload-sig verification
		SigPath:            verityManifestSigPath,
		ExpectedRootHash:   payload.RootHash,
		ImagePayloadForSig: payload,
		EpochFloor:         epochFloor,
	}

	if err := verify.VerifySquashfsBeforePivot(cfg); err != nil {
		log.Fatalf("[verity] HALT: OS image verification failed (fail-closed): %v", err)
	}

	log.Printf("[verity] OS image verified OK (roothash=%s, epoch_floor=%d)",
		payload.RootHash, epochFloor)
}

// ─────────────────────────────────────────────────────────────────────────────

// markBootHealthy resets the boot counter to 0 and promotes the active slot to
// last-known-good.  Call this after the OS services / desktop are confirmed
// healthy (e.g. after vulos-server starts listening).
func markBootHealthy(sm *osdist.SlotManager, bs *osdist.BootState) {
	if sm == nil || bs == nil {
		return
	}
	next, err := sm.MarkHealthy(bs)
	if err != nil {
		log.Printf("[slots] MarkHealthy failed: %v", err)
		return
	}
	log.Printf("[slots] boot marked healthy: counter reset, last_known_good=%s", next.LastKnownGood)
}

// ─────────────────────────────────────────────────────────────────────────────

// plymouthProgress sends a progress update to Plymouth (0–100).
// No-ops silently when the plymouth binary is not found — safe in containers.
func plymouthProgress(n int) {
	bin, err := exec.LookPath("plymouth")
	if err != nil {
		return
	}
	cmd := exec.Command(bin, "system-update", fmt.Sprintf("--progress=%d", n))
	_ = cmd.Run()
}

// plymouthQuitRetainSplash tears down the Plymouth splash while keeping
// the last frame visible until the compositor (labwc) draws its first frame.
// No-ops silently when the plymouth binary is not found.
func plymouthQuitRetainSplash() {
	bin, err := exec.LookPath("plymouth")
	if err != nil {
		return
	}
	cmd := exec.Command(bin, "quit", "--retain-splash")
	_ = cmd.Run()
}

func main() {
	if os.Getpid() != 1 {
		fmt.Println("vulos-init: not running as PID 1, starting in service mode")
		startSSH()
		startServices()
		return
	}

	log.SetPrefix("[vulos-init] ")
	log.Println("booting Vula OS...")
	plymouthProgress(0) // milestone: boot start

	// Phase 1: Mount essential filesystems
	mountAll()
	plymouthProgress(20) // milestone: mountAll done

	// NETB-03: In a live/netboot session the root is an in-RAM overlay; there
	// is no local slot layout and no stable.json manifest.  Skip the boot
	// counter and OS verification — they only apply to a locally-installed system.
	if isLiveBoot() {
		log.Println("[netboot] live-boot mode detected (vulos.live=1) — skipping OSDIST-03 counter and VERITY-02 verification")
	} else {
		// OSDIST-03: Increment the persistent boot counter immediately after mounts
		// (so /var/cache is accessible).  If the counter exceeds the threshold the
		// rollback is written here; we note it and continue the current boot.  The
		// healthy-mark is deferred to after the server is confirmed listening.
		slotMgrGlobal, bootStateGlobal = incrementBootCounter()

		// VERITY-02: Verify the OS image (squashfs + dm-verity root hash + release
		// cert chain) before any services start.  Halts boot on any mismatch.
		// This is the pivot gate: if verification fails we never reach startServices().
		verifyOSBeforeBoot()
	}

	// Phase 1.5: Load core kernel modules.
	// Without systemd-udevd running yet (we're PID 1, udev isn't), nothing
	// auto-loads modules on uevent. Real hardware AND QEMU both ship USB
	// HID as modules in the Debian generic kernel, so without this the
	// kernel never creates /dev/input/event* and the kiosk has no mouse
	// or keyboard. Best-effort: missing modules are not fatal (a few
	// arches/kernels build them in, in which case modprobe is a no-op).
	loadCoreModules()

	// Phase 2: Hardware detection (best-effort, non-fatal)
	detectHardware()

	// Phase 3: Set hostname
	setHostname()
	plymouthProgress(40) // milestone: hostname set / hardware detected

	// Phase 4: Start systemd services (if available)
	startSystemd()
	plymouthProgress(60) // milestone: systemd handed off / services starting

	// Phase 4: Bring up networking (DHCP, WiFi fallback, mDNS)
	phaseNetwork()
	// Phase 4: Generate SSH host keys + start sshd
	startSSH()

	// Phase 5: D-Bus system bus (before the browser, best-effort)
	startSystemBus()

	// Host profile: gpu / inputs / RAM → one decision, used below for
	// kiosk env, Chromium flags, and browser pre-warm.
	currentProfile = detectHost()
	if os.Getenv("VULOS_PREWARM_BROWSER") == "" {
		if currentProfile.PrewarmBrowser {
			os.Setenv("VULOS_PREWARM_BROWSER", "1")
		} else {
			os.Setenv("VULOS_PREWARM_BROWSER", "0")
		}
	}

	// Phase 5: Start vulos server
	startServices()

	// Phase 6: Reap zombies (PID 1 duty)
	reapLoop()
}

func mountAll() {
	mounts := []struct {
		source string
		target string
		fstype string
		flags  uintptr
		data   string
	}{
		{"proc", "/proc", "proc", 0, ""},
		{"sysfs", "/sys", "sysfs", 0, ""},
		{"devtmpfs", "/dev", "devtmpfs", 0, ""},
		{"devpts", "/dev/pts", "devpts", 0, ""},
		{"tmpfs", "/dev/shm", "tmpfs", 0, "size=2g"},
		{"tmpfs", "/run", "tmpfs", 0, ""},
		{"tmpfs", "/tmp", "tmpfs", 0, ""},
		{"cgroup2", "/sys/fs/cgroup", "cgroup2", 0, ""},
	}

	for _, m := range mounts {
		os.MkdirAll(m.target, 0755)
		err := syscall.Mount(m.source, m.target, m.fstype, m.flags, m.data)
		if err != nil {
			log.Printf("mount %s: %v (may already be mounted)", m.target, err)
		}
	}

	// efivarfs — only on UEFI systems; skip in containers/non-UEFI.
	efiDir := "/sys/firmware/efi"
	if _, err := os.Stat(efiDir); err == nil {
		efivarsTarget := "/sys/firmware/efi/efivars"
		os.MkdirAll(efivarsTarget, 0755)
		if err := syscall.Mount("efivarfs", efivarsTarget, "efivarfs", 0, ""); err != nil {
			log.Printf("mount %s: %v (may already be mounted)", efivarsTarget, err)
		}
	}

	// Data partition — mount LABEL=vulos-data into ~/.vulos when present.
	mountDataPartition()

	log.Println("filesystems mounted")
}

// detectHardware runs the hardware detection phase. It is best-effort: any
// panic or error inside hwdetect is already recovered there and only logged,
// so this wrapper never returns an error to the caller.
func detectHardware() {
	log.Println("phase 2: hardware detection")
	hwdetect.Run()
}

// mountDataPartition finds a block device with LABEL=vulos-data and mounts it
// into the home .vulos directory. Best-effort: logs on failure, never panics.
func mountDataPartition() {
	const label = "vulos-data"

	// blkid resolves LABEL → device path without pulling in any library.
	out, err := exec.Command("blkid", "-L", label).Output()
	if err != nil || len(strings.TrimSpace(string(out))) == 0 {
		// Partition not present — normal on non-data-disk systems.
		return
	}
	device := strings.TrimSpace(string(out))

	// Determine mount point: use the running user's home directory.
	home, err := os.UserHomeDir()
	if err != nil {
		home = "/root"
	}
	target := filepath.Join(home, ".vulos")

	if err := os.MkdirAll(target, 0755); err != nil {
		log.Printf("data partition: mkdir %s: %v", target, err)
		return
	}

	if err := syscall.Mount(device, target, "ext4", 0, ""); err != nil {
		log.Printf("data partition: mount %s -> %s: %v", device, target, err)
		return
	}
	log.Printf("data partition: %s mounted at %s", device, target)
}

func setHostname() {
	name := "vulos"
	if data, err := os.ReadFile("/etc/hostname"); err == nil {
		name = string(data)
	}
	syscall.Sethostname([]byte(name))
	log.Printf("hostname: %s", name)
}

func startSystemd() {
	// In a container, systemd won't be PID 1 so systemctl may not work.
	// On bare metal, systemd manages services via unit files.
	if _, err := exec.LookPath("systemctl"); err == nil {
		log.Println("systemd detected (services managed by systemd)")
		return
	}
	log.Println("systemctl not found, continuing without init system")
}

// phaseNetwork brings the machine online:
//  1. Wired DHCP — tries udhcpc then dhclient on every eth*/en* interface.
//  2. WiFi fallback — if wired fails and wpa_cli lists saved networks, connect
//     to each in turn via wpa_supplicant and run DHCP on the wireless interface.
//  3. resolv.conf — writes fallback nameservers (Cloudflare + Google) so DNS
//     works even when the DHCP server forgets to send option 6.
//  4. avahi-daemon — started in the background so hostname.local resolves on
//     the LAN via mDNS.
//
// All steps are best-effort: errors are logged, never fatal.
// BMINIT9_/initnet- phase.
func phaseNetwork() {
	log.Println("BMINIT9: starting network phase")

	// Bring up the loopback interface FIRST. Without this, 127.0.0.1 is
	// unroutable inside the VM — vulos-server binds fine (it's an INADDR_ANY
	// :8080 bind that doesn't need loopback up) but the kiosk Chromium's
	// http://localhost:8080 fetch and our wait-for-port both ECONNREFUSED.
	// The browser shows its blank error state and never retries — the
	// "white screen forever" symptom. `ip link set lo up` (or the syscall
	// equivalent here) costs ~0ms and is the actual cure for that class
	// of bug. We use `ip` if available, else fall back to `ifconfig`.
	if err := bringUpLoopback(); err != nil {
		log.Printf("BMINIT9: WARN bringing up loopback: %v (localhost may be unreachable)", err)
	} else {
		log.Println("BMINIT9: loopback (lo) up")
	}

	wiredOK := initnetWired()
	if !wiredOK {
		log.Println("BMINIT9: wired DHCP failed — trying WiFi fallback")
		initnetWifi()
	}

	initnetResolvConf()
	initnetAvahi()

	log.Println("BMINIT9: network phase complete")
}

// initnetWired enumerates eth*/en* interfaces from /sys/class/net and runs DHCP
// (udhcpc preferred, dhclient as fallback) on each until one succeeds.
func initnetWired() bool {
	ifaces := initnetEthernetIfaces()
	if len(ifaces) == 0 {
		log.Println("BMINIT9: no wired interfaces found in /sys/class/net")
		return false
	}

	dhcpBin, dhcpArgs := initnetDHCPCmd()
	if dhcpBin == "" {
		log.Println("BMINIT9: neither udhcpc nor dhclient found — skipping wired DHCP")
		return false
	}

	for _, iface := range ifaces {
		log.Printf("BMINIT9: bringing up %s, running %s", iface, dhcpBin)

		// Bring link up (ignore errors — may already be up)
		exec.Command("ip", "link", "set", iface, "up").Run()

		args := append(dhcpArgs, iface)
		cmd := exec.Command(dhcpBin, args...)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			log.Printf("BMINIT9: DHCP on %s failed: %v", iface, err)
			continue
		}
		log.Printf("BMINIT9: DHCP on %s succeeded", iface)
		return true
	}
	return false
}

// initnetEthernetIfaces reads /sys/class/net and returns physical wired
// interface names (eth*, en* — excluding lo, wl*, virtual, docker, etc.).
func initnetEthernetIfaces() []string {
	entries, err := os.ReadDir("/sys/class/net")
	if err != nil {
		log.Printf("BMINIT9: readdir /sys/class/net: %v", err)
		return nil
	}
	var result []string
	for _, e := range entries {
		n := e.Name()
		if n == "lo" {
			continue
		}
		if strings.HasPrefix(n, "eth") || strings.HasPrefix(n, "en") {
			// Skip virtual interfaces that show up as en* (e.g. enX0 on Xen).
			// A quick heuristic: skip if no /sys/class/net/<iface>/device symlink
			// (i.e. it has no physical device backing).
			devicePath := "/sys/class/net/" + n + "/device"
			if _, err := os.Lstat(devicePath); err != nil {
				// No physical device — likely a virtual NIC; still try it.
				log.Printf("BMINIT9: %s has no /device symlink, will still try DHCP", n)
			}
			result = append(result, n)
		}
	}
	return result
}

// initnetDHCPCmd returns the best available DHCP client binary and its
// arguments (everything except the interface name, which is appended last).
func initnetDHCPCmd() (string, []string) {
	// udhcpc (busybox) — lightweight, exits 0 on success
	if p, err := exec.LookPath("udhcpc"); err == nil {
		// -i <iface> -n (exit if lease not obtained) -q (quit after lease)
		return p, []string{"-i"}
	}
	// dhclient (isc-dhcp-client)
	if p, err := exec.LookPath("dhclient"); err == nil {
		return p, []string{"-v"}
	}
	// dhcpcd
	if p, err := exec.LookPath("dhcpcd"); err == nil {
		return p, []string{}
	}
	return "", nil
}

// initnetWifi tries each saved wpa_supplicant network in turn, bringing up
// the wireless interface with DHCP after a brief association wait.
func initnetWifi() {
	wifiIface := initnetWifiIface()
	if wifiIface == "" {
		log.Println("BMINIT9: no wireless interface found, skipping WiFi fallback")
		return
	}

	// Ask wpa_cli for saved networks.
	out, err := exec.Command("wpa_cli", "-i", wifiIface, "list_networks").Output()
	if err != nil {
		log.Printf("BMINIT9: wpa_cli list_networks: %v", err)
		return
	}

	type savedNet struct{ id, ssid string }
	var nets []savedNet
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Split(line, "\t")
		if len(fields) >= 2 {
			// First field is numeric network id; skip header line
			id := strings.TrimSpace(fields[0])
			if id == "" || id == "network" || strings.HasPrefix(id, "Selected") {
				continue
			}
			// Validate numeric id
			valid := true
			for _, c := range id {
				if c < '0' || c > '9' {
					valid = false
					break
				}
			}
			if valid {
				nets = append(nets, savedNet{id: id, ssid: strings.TrimSpace(fields[1])})
			}
		}
	}

	if len(nets) == 0 {
		log.Println("BMINIT9: no saved WiFi networks in wpa_supplicant")
		return
	}

	exec.Command("ip", "link", "set", wifiIface, "up").Run()

	dhcpBin, dhcpArgs := initnetDHCPCmd()

	for _, n := range nets {
		log.Printf("BMINIT9: trying WiFi SSID=%q (network id %s)", n.ssid, n.id)

		// Select the network and reassociate.
		exec.Command("wpa_cli", "-i", wifiIface, "select_network", n.id).Run()

		// Wait up to 8 seconds for association.
		associated := false
		for i := 0; i < 8; i++ {
			time.Sleep(1 * time.Second)
			statusOut, _ := exec.Command("wpa_cli", "-i", wifiIface, "status").Output()
			if strings.Contains(string(statusOut), "wpa_state=COMPLETED") {
				associated = true
				break
			}
		}
		if !associated {
			log.Printf("BMINIT9: association timed out for SSID=%q", n.ssid)
			continue
		}

		if dhcpBin != "" {
			args := append(dhcpArgs, wifiIface)
			cmd := exec.Command(dhcpBin, args...)
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			if err := cmd.Run(); err != nil {
				log.Printf("BMINIT9: DHCP on WiFi %s failed: %v", wifiIface, err)
				continue
			}
			log.Printf("BMINIT9: WiFi up on %s (SSID=%q)", wifiIface, n.ssid)
			return
		}
	}
	log.Println("BMINIT9: WiFi fallback exhausted all saved networks")
}

// initnetWifiIface finds the first wl* interface in /sys/class/net.
func initnetWifiIface() string {
	entries, err := os.ReadDir("/sys/class/net")
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "wl") {
			return e.Name()
		}
	}
	return ""
}

// initnetResolvConf writes a minimal /etc/resolv.conf if DNS nameservers are
// missing, ensuring external names resolve even when DHCP doesn't supply them.
func initnetResolvConf() {
	const path = "/etc/resolv.conf"
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), "nameserver") {
		log.Println("BMINIT9: /etc/resolv.conf already has nameservers, leaving untouched")
		return
	}
	content := "# Written by vulos-init (BMINIT9)\nnameserver 1.1.1.1\nnameserver 8.8.8.8\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		log.Printf("BMINIT9: write /etc/resolv.conf: %v", err)
		return
	}
	log.Println("BMINIT9: wrote fallback nameservers to /etc/resolv.conf")
}

// initnetAvahi starts avahi-daemon in the background so that hostname.local
// resolves on the LAN via mDNS.  Errors are logged, not fatal.
func initnetAvahi() {
	avahi, err := exec.LookPath("avahi-daemon")
	if err != nil {
		log.Println("BMINIT9: avahi-daemon not found, mDNS unavailable")
		return
	}
	cmd := exec.Command(avahi, "--no-rlimits", "--no-drop-root")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		log.Printf("BMINIT9: avahi-daemon start error: %v", err)
		return
	}
	log.Printf("BMINIT9: avahi-daemon started (pid=%d) — hostname.local is resolvable", cmd.Process.Pid)
}

func startServices() {
	plymouthProgress(80) // milestone: server starting

	// Locate vulos server binary
	serverBin := findBinary("vulos-server", "/usr/local/bin/vulos-server", "/opt/vulos/server")
	if serverBin == "" {
		log.Println("vulos-server not found, skipping")
		return
	}

	// Supervise vulos-server: if it dies the whole OS goes blank (the kiosk
	// Chromium starts getting ECONNREFUSED on every fetch). Same pattern as
	// superviseKiosk — Wait() for efficient liveness, restart with backoff,
	// reset on any healthy run.
	go superviseServer(serverBin, "-env", "main")

	// Start Cage/WPE WebKit kiosk if available (also supervised).
	startKiosk()
}

// superviseServer keeps vulos-server alive. cmd.Wait() blocks until exit
// (no polling overhead), then restart with exponential backoff 1s→30s,
// reset after any run ≥ 30s so a one-off panic retries fast but a crash
// loop doesn't burn CPU. Logs every transition for postmortem.
func superviseServer(bin string, args ...string) {
	const (
		minBackoff = 1 * time.Second
		maxBackoff = 30 * time.Second
		healthyRun = 30 * time.Second
	)
	backoff := minBackoff
	for attempt := 1; ; attempt++ {
		cmd := exec.Command(bin, args...)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		cmd.Env = os.Environ()
		start := time.Now()
		if err := cmd.Start(); err != nil {
			log.Printf("[server] start error (attempt %d): %v — retrying in %s", attempt, err, backoff)
			time.Sleep(backoff)
			if backoff < maxBackoff {
				backoff *= 2
				if backoff > maxBackoff {
					backoff = maxBackoff
				}
			}
			continue
		}
		log.Printf("[server] vulos-server started (pid=%d, attempt=%d)", cmd.Process.Pid, attempt)
		err := cmd.Wait()
		dur := time.Since(start)
		exit := 0
		if ee, ok := err.(*exec.ExitError); ok {
			exit = ee.ExitCode()
		} else if err != nil {
			exit = -1
		}
		log.Printf("[server] vulos-server exited after %s (exit=%d) — restarting", dur.Round(time.Millisecond), exit)
		if dur >= healthyRun {
			backoff = minBackoff // healthy run → reset
		} else {
			time.Sleep(backoff)
			if backoff < maxBackoff {
				backoff *= 2
				if backoff > maxBackoff {
					backoff = maxBackoff
				}
			}
		}
	}
}

// startSystemBus brings up a D-Bus system bus (best-effort, non-fatal).
// There is no systemd socket activation under vulos-init, so without this
// Chromium/Cog and assorted desktop bits log "Failed to contact D-Bus" on
// every launch. Ensures /etc/machine-id (dbus refuses to start without it)
// and /run/dbus first.
func startSystemBus() {
	bin := findBinary("dbus-daemon", "/usr/bin/dbus-daemon")
	if bin == "" {
		return
	}
	if _, err := os.Stat("/run/dbus/system_bus_socket"); err == nil {
		return // already running
	}
	// dbus-daemon aborts without a machine-id.
	if b, _ := os.ReadFile("/etc/machine-id"); len(b) < 32 {
		if uuidgen := findBinary("dbus-uuidgen", "/usr/bin/dbus-uuidgen"); uuidgen != "" {
			_ = exec.Command(uuidgen, "--ensure=/etc/machine-id").Run()
		}
	}
	os.MkdirAll("/run/dbus", 0o755)
	if err := exec.Command(bin, "--system", "--fork", "--nopidfile").Run(); err != nil {
		log.Printf("dbus-daemon start warning (non-fatal): %v", err)
	} else {
		log.Println("dbus system bus started")
	}
}

// startSSH generates SSH host keys on first boot (idempotent) and starts sshd.
func startSSH() {
	// Generate all host key types if the ed25519 key is missing (first boot only).
	if _, err := os.Stat("/etc/ssh/ssh_host_ed25519_key"); os.IsNotExist(err) {
		log.Println("generating SSH host keys (first boot)...")
		if out, err := exec.Command("ssh-keygen", "-A").CombinedOutput(); err != nil {
			log.Printf("ssh-keygen -A failed: %v: %s", err, out)
		} else {
			log.Println("SSH host keys generated")
		}
	} else {
		log.Println("SSH host keys already present")
	}

	// sshd needs its privilege-separation directory; /run is a fresh tmpfs
	// every boot, so create it or sshd exits 255 ("Missing privilege
	// separation directory: /run/sshd").
	os.MkdirAll("/run/sshd", 0o755)

	// Start sshd in daemon mode (idempotent — sshd refuses to start if already running).
	sshdBin := findBinary("/usr/sbin/sshd", "sshd")
	if sshdBin == "" {
		log.Println("sshd not found, skipping SSH daemon")
		return
	}
	if out, err := exec.Command(sshdBin).CombinedOutput(); err != nil {
		log.Printf("sshd start warning: %v: %s", err, out)
	} else {
		log.Println("sshd started")
	}
}

func startKiosk() {
	// Headless path: no display connected → skip compositor entirely.
	// vulos.kiosk=force on the kernel cmdline overrides this — QEMU's
	// virtio-gpu reports no "connected" DRM output yet still renders a
	// compositor, so the bare-metal smoke harness sets it to see the desktop.
	if !displayConnected() && !kioskForced() {
		log.Println("no display connected, skipping kiosk (headless mode)")
		return
	}
	if !displayConnected() {
		log.Println("vulos.kiosk=force set — starting compositor despite no connected DRM output")
	}

	// Shared runtime dir for Wayland socket.
	os.MkdirAll("/run/user/0", 0700)
	// Bare-metal compositor env. Deliberately do NOT set WAYLAND_DISPLAY:
	// the compositor *creates* that socket; presetting it forces wlroots into
	// nested-Wayland mode ("Could not connect to remote display") instead of
	// the DRM backend on the real GPU. LIBSEAT_BACKEND=builtin lets the
	// compositor become DRM master directly as root — there is no
	// seatd/logind/polkit under vulos-init (without it wlroots aborts: "a
	// seat could not be created"). pixman renderer because QEMU virtio-gpu /
	// software GPUs have no usable GL here.
	// Compositor env, derived from the live host profile. One artifact,
	// detected at boot. LIBSEAT_BACKEND=builtin lets wlroots become DRM
	// master without seatd/logind; the renderer + libinput knob are picked
	// from the live probe.
	baseEnv := append(os.Environ(),
		"XDG_RUNTIME_DIR=/run/user/0",
		"LIBSEAT_BACKEND=builtin",
	)
	if currentProfile.HasHWGPU {
		// Real GPU: let wlroots pick the GL renderer.
		log.Println("[kiosk] hardware GPU detected — using GL renderer")
	} else {
		// No real GPU (QEMU virtio-gpu, llvmpipe, headless): software path.
		// Three things wlroots' DRM backend can choke on under virtio-gpu
		// without working GL, all required for cage to actually paint:
		//   WLR_DRM_NO_ATOMIC=1       — fall back to legacy KMS modeset
		//   WLR_DRM_NO_MODIFIERS=1    — don't require GBM modifier negotiation
		//   WLR_RENDERER_ALLOW_SOFTWARE=1 — accept the pixman renderer
		// Without these cage starts, opens /dev/dri/card0, but silently
		// fails to put a framebuffer on screen (no logs, no exit) — the
		// "QEMU window shows kernel console under its own chrome" symptom.
		// DBUS_SESSION_BUS_ADDRESS=unix:path=/dev/null gives Chromium one
		// "Connection refused" on the session bus and lets it proceed to
		// navigation instead of looping on NameHasOwner.
		baseEnv = append(baseEnv,
			"WLR_RENDERER=pixman",
			"WLR_RENDERER_ALLOW_SOFTWARE=1",
			"WLR_DRM_NO_ATOMIC=1",
			"WLR_DRM_NO_MODIFIERS=1",
			"DBUS_SESSION_BUS_ADDRESS=unix:path=/dev/null",
		)
		log.Println("[kiosk] software path — pixman, DRM legacy/no-modifiers, dbus session bus disabled")
	}
	// Brief wait for udev's coldplug to surface /dev/input/event* (USB
	// HID enumeration can take a second or two). Informational only —
	// inputs that arrive later are picked up via libinput hotplug.
	if waitForInputDevices(3 * time.Second) {
		log.Println("[kiosk] input devices present (libinput will use hotplug)")
	} else {
		log.Println("[kiosk] no input devices yet — relying on libinput hotplug")
	}
	// ALWAYS set WLR_LIBINPUT_NO_DEVICES=1. This is "don't abort the
	// compositor at init time if libinput sees zero devices right now"
	// — NOT "disable inputs". With it unset, cage's libinput backend
	// aborts startup ("Unable to start the wlroots backend") when udev
	// coldplug hasn't finished, even on machines where inputs are about
	// to appear. Hotplugged devices still arrive via libinput.
	baseEnv = append(baseEnv, "WLR_LIBINPUT_NO_DEVICES=1")

	// Wait for vulos-server to actually be LISTENING on :8080 before
	// launching the kiosk browser. A fixed 2-second sleep races: the
	// server's full init (peering stores, sqlite migrations, etc.) takes
	// 20-40s on a software-CPU VM, and a too-early browser launch hits
	// ECONNREFUSED, paints its blank error state (looks white in kiosk
	// mode with --noerrdialogs), and never retries — the "white screen
	// forever" symptom. Poll the loopback socket instead, up to 2 minutes.
	waitForLocalPort("127.0.0.1:8080", 120*time.Second)

	// OSDIST-03: Server is confirmed listening → this is the healthy-boot
	// point.  Reset the boot counter and promote the active slot to
	// last-known-good so future failed boots can roll back to this one.
	markBootHealthy(slotMgrGlobal, bootStateGlobal)

	plymouthProgress(100)      // milestone: kiosk up
	plymouthQuitRetainSplash() // hand off splash to compositor (both labwc + cage paths)

	// Single mode (decisions.md D93): the OS *is* the React shell at
	// localhost:8080. cage runs the browser fullscreen — one window, no
	// labwc / native-window / window-manager path. Native Linux apps stream
	// into the React shell (same pipeline as remote). The installer is just
	// another React app inside this shell.
	cageBin, err := exec.LookPath("cage")
	if err != nil {
		log.Println("cage not found, skipping kiosk")
		return
	}
	browserBin, browserArgs := findKioskBrowser()
	if browserBin == "" {
		log.Println("no browser (cog/chromium) found, skipping kiosk")
		return
	}
	// Chromium (under cage) IS the shell — if it dies, the user sees a
	// dead screen. Supervise it: launch, Wait, on exit restart with
	// exponential backoff (1s → 30s, capped). Runs forever in a goroutine.
	go superviseKiosk(cageBin, browserBin, browserArgs, baseEnv)
}

// superviseKiosk is the keep-alive loop for the cage+browser kiosk. It is
// efficient: there is no polling — Wait() blocks until the process exits,
// at which point exit code + duration are logged and a backoff kicks in
// only if the process died quickly (a fast-failing crash loop). A normal
// long-lived run resets the backoff. Plymouth progress stays at 100 once
// the first launch succeeds, so subsequent restarts are silent.
func superviseKiosk(cageBin, browserBin string, browserArgs, baseEnv []string) {
	const (
		minBackoff = 1 * time.Second
		maxBackoff = 30 * time.Second
		// A run shorter than this is considered a "fast crash" and the
		// backoff increases; longer runs are healthy and reset it.
		healthyRun = 30 * time.Second
	)
	backoff := minBackoff
	for attempt := 1; ; attempt++ {
		cmd := exec.Command(cageBin, append([]string{"--", browserBin}, browserArgs...)...)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		cmd.Env = baseEnv
		start := time.Now()
		if err := cmd.Start(); err != nil {
			log.Printf("[kiosk] start error (attempt %d): %v — retrying in %s", attempt, err, backoff)
			time.Sleep(backoff)
			if backoff < maxBackoff {
				backoff *= 2
				if backoff > maxBackoff {
					backoff = maxBackoff
				}
			}
			continue
		}
		log.Printf("[kiosk] cage kiosk started: %s (pid=%d, attempt=%d)", browserBin, cmd.Process.Pid, attempt)
		err := cmd.Wait()
		dur := time.Since(start)
		exit := 0
		if ee, ok := err.(*exec.ExitError); ok {
			exit = ee.ExitCode()
		} else if err != nil {
			exit = -1
		}
		log.Printf("[kiosk] cage exited after %s (exit=%d) — restarting", dur.Round(time.Millisecond), exit)
		if dur >= healthyRun {
			backoff = minBackoff // healthy run → reset
		} else {
			time.Sleep(backoff)
			if backoff < maxBackoff {
				backoff *= 2
				if backoff > maxBackoff {
					backoff = maxBackoff
				}
			}
		}
	}
}

// kioskForced reports whether the kernel cmdline requested the compositor be
// started unconditionally (vulos.kiosk=force) — for VMs where DRM connector
// status is unreliable (QEMU virtio-gpu).
func kioskForced() bool {
	data, err := os.ReadFile("/proc/cmdline")
	if err != nil {
		return false
	}
	return strings.Contains(string(data), "vulos.kiosk=force")
}

// displayConnected reports whether at least one DRM output is connected.
// Checks /sys/class/drm/card*/status for the string "connected".
func displayConnected() bool {
	matches, err := filepath.Glob("/sys/class/drm/card*/status")
	if err != nil || len(matches) == 0 {
		return false
	}
	for _, path := range matches {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		// Exact value is "connected\n"; avoid matching "disconnected".
		if string(data) == "connected\n" || string(data) == "connected" {
			return true
		}
	}
	return false
}

// findKioskBrowser returns the browser binary + args run (under cage)
// fullscreen as the single kiosk window showing the React shell.
// Cog (WPE WebKit) is preferred (lightweight, no browser chrome);
// Chromium is the fallback with equivalent --kiosk flags.
func findKioskBrowser() (string, []string) {
	cogBin := findBinary("cog", "/usr/bin/cog")
	if cogBin != "" {
		return cogBin, []string{"--platform=wl", "http://localhost:8080"}
	}
	chromiumBin := findBinary(
		"chromium", "chromium-browser",
		"/usr/bin/chromium", "/usr/bin/chromium-browser",
	)
	if chromiumBin != "" {
		// Baseline kiosk flags (always-on, brand-correct first-run UX).
		args := []string{
			"--kiosk",
			"--ozone-platform=wayland",
			"--no-sandbox",
			"--no-first-run",
			"--no-default-browser-check",
			"--noerrdialogs",
			"--password-store=basic",
			"--disable-component-update",
			"--disable-background-networking",
			"--disable-sync",
			"--disable-default-apps",
			"--disable-extensions",
			"--disable-notifications",
			"--disable-translate",
		}
		if currentProfile.HasHWGPU {
			// Hardware GPU: native GL + hardware video decode. Lets the
			// shell + streamed app pixels render fast and correctly.
			args = append(args,
				"--use-gl=egl",
				"--enable-features=VaapiVideoDecoder,UseOzonePlatform",
			)
		} else {
			// Software path (QEMU virtio-gpu, llvmpipe, no GPU): Chromium
			// otherwise stays pre-navigation for the entire boot — kiosk
			// shows blank white because:
			//   1) the session bus probe loops (handled via env above),
			//   2) GCM (Google Cloud Messaging) tries to register against
			//      Google's endpoint on first launch even with
			//      --disable-background-networking, retries on QUOTA_*/
			//      PHONE_REGISTRATION_ERROR, and blocks first navigation
			//      on a VM without real internet. We disable GCM + push
			//      + proxy-resolver outright, and point the proxy at
			//      "direct" with bypass="*" so connection attempts fail
			//      fast instead of retrying.
			args = append(args,
				"--disable-gpu",
				"--use-gl=swiftshader",
				"--enable-features=UseOzonePlatform",
				"--disable-features=Translate,MediaRouter,InterestFeedContentSuggestions,DBus,GCMRegistration,PushMessaging,OptimizationHints",
				"--proxy-server=direct://",
				"--proxy-bypass-list=*",
				"--disable-background-networking",
				"--disable-dev-shm-usage",
			)
		}
		args = append(args, "http://localhost:8080")
		return chromiumBin, args
	}
	return "", nil
}

// loadCoreModules modprobes the kernel modules the Debian generic kernel
// keeps as loadable: USB HID stack (keyboard/mouse/tablet), evdev (creates
// /dev/input/event*), USB host controllers, and the virtio DRM driver
// (required for cage's wlroots DRM backend to find /dev/dri/card*). Without
// these and without udev running, the kernel never even *enumerates* the
// devices QEMU/firmware exposes — so the screen comes up blank and the
// kiosk has no inputs. Each modprobe is best-effort: built-in modules
// fail-silent, and a missing module on one platform is fine on another.
func loadCoreModules() {
	modprobe := findBinary("modprobe", "/usr/sbin/modprobe", "/sbin/modprobe")
	if modprobe == "" {
		log.Println("[modules] modprobe not found, skipping (kernel may have everything built in)")
	} else {
		modules := []string{
			// USB host controllers
			"xhci_pci", "xhci_hcd", "ehci_pci", "ohci_pci",
			// Input stack
			"usbhid", "hid_generic", "evdev",
			// virtio + DRM (QEMU + cloud)
			"virtio_pci", "virtio_blk", "virtio_net", "virtio_gpu",
		}
		loaded := 0
		for _, m := range modules {
			if err := exec.Command(modprobe, "-q", m).Run(); err == nil {
				loaded++
			}
		}
		log.Printf("[modules] modprobed %d/%d core modules (best-effort)", loaded, len(modules))
	}
	startUdev()
}

// startUdev brings up systemd-udevd (or a udevd binary) and triggers
// coldplug. Without this, the kernel creates devices in /sys but no
// device-add uevents are processed → /dev/input/event* may exist but
// wlroots' libinput backend uses libinput_udev_create_context() which
// REQUIRES udev (it enumerates and watches via libudev). Without a
// running udevd, libinput sees zero inputs even with event* files.
// Best-effort: if no udevd is installed, callers must rely on
// WLR_LIBINPUT_NO_DEVICES=1 so cage can still start.
func startUdev() {
	udevd := findBinary(
		"systemd-udevd",
		"/lib/systemd/systemd-udevd",
		"/usr/lib/systemd/systemd-udevd",
		"udevd",
		"/sbin/udevd",
		"/usr/sbin/udevd",
	)
	if udevd == "" {
		log.Println("[udev] no udevd found — input/output devices will not be enumerated for libinput")
		return
	}
	cmd := exec.Command(udevd, "--daemon")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		log.Printf("[udev] %s --daemon: %v (best-effort, continuing)", udevd, err)
		return
	}
	log.Printf("[udev] %s started (pid=%d)", udevd, cmd.Process.Pid)
	// Coldplug: replay add events for every device already in /sys so udev
	// notices the modules we just loaded. settle waits for the queue to
	// drain (bounded — never blocks forever).
	udevadm := findBinary("udevadm", "/sbin/udevadm", "/usr/sbin/udevadm", "/bin/udevadm")
	if udevadm == "" {
		return
	}
	_ = exec.Command(udevadm, "trigger", "--action=add", "--type=devices").Run()
	_ = exec.Command(udevadm, "trigger", "--action=add", "--type=subsystems").Run()
	_ = exec.Command(udevadm, "settle", "--timeout=5").Run()
	log.Println("[udev] coldplug complete")
}

// waitForInputDevices polls /dev/input/event* every 100ms until at least
// one device exists or timeout elapses. Returns true if any device was
// found. Boot-time `detectHost()` runs before udev finishes USB enum, so a
// one-shot probe is a false negative on every real machine with USB
// peripherals. This is the gate that prevents accidentally setting
// WLR_LIBINPUT_NO_DEVICES on a real laptop.
func waitForInputDevices(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if matches, _ := filepath.Glob("/dev/input/event*"); len(matches) > 0 {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	matches, _ := filepath.Glob("/dev/input/event*")
	return len(matches) > 0
}

// bringUpLoopback brings the lo interface UP. Tries `ip link set lo up`
// first (always present on Debian), falls back to `ifconfig lo up`. Either
// is a fork+exec but it's a one-time syscall at boot — measured ~3-5ms.
// Returns nil iff something marked lo as up successfully.
func bringUpLoopback() error {
	// Idempotent: if it's already UP, we're done.
	if data, err := os.ReadFile("/sys/class/net/lo/operstate"); err == nil {
		state := strings.TrimSpace(string(data))
		// "unknown" is what Linux reports for loopback when it's up
		// (loopback doesn't have a real carrier signal) — accept it.
		if state == "up" || state == "unknown" {
			// But also verify the IFF_UP flag is set, since operstate
			// can be "unknown" before the admin brings it up too.
			if flags, err := os.ReadFile("/sys/class/net/lo/flags"); err == nil {
				var f uint64
				_, _ = fmt.Sscanf(strings.TrimSpace(string(flags)), "0x%x", &f)
				if f&0x1 != 0 { // IFF_UP
					return nil
				}
			}
		}
	}
	if ipBin, err := exec.LookPath("ip"); err == nil {
		if out, err := exec.Command(ipBin, "link", "set", "lo", "up").CombinedOutput(); err == nil {
			return nil
		} else {
			log.Printf("BMINIT9: ip link set lo up failed: %v: %s", err, out)
		}
	}
	if ifcBin, err := exec.LookPath("ifconfig"); err == nil {
		if out, err := exec.Command(ifcBin, "lo", "up").CombinedOutput(); err == nil {
			return nil
		} else {
			log.Printf("BMINIT9: ifconfig lo up failed: %v: %s", err, out)
		}
	}
	return fmt.Errorf("no ip/ifconfig tool found")
}

// waitForLocalPort blocks until something is listening on addr or timeout
// elapses. Polls every 250ms; logs progress every 5s so a slow boot is
// visible in the serial log. Returns true on success, false on timeout —
// caller continues either way (the kiosk launches; if the server never
// came up Chromium will at least show its standard error page).
func waitForLocalPort(addr string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	lastLog := time.Now()
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
		if err == nil {
			_ = c.Close()
			log.Printf("[kiosk] %s is listening — launching browser", addr)
			return true
		}
		if time.Since(lastLog) >= 5*time.Second {
			log.Printf("[kiosk] waiting for %s (still not listening)…", addr)
			lastLog = time.Now()
		}
		time.Sleep(250 * time.Millisecond)
	}
	log.Printf("[kiosk] WARNING: %s not listening after %s — launching browser anyway", addr, timeout)
	return false
}

// reapLoop harvests zombie processes — required duty for PID 1.
func reapLoop() {
	for {
		var status syscall.WaitStatus
		pid, err := syscall.Wait4(-1, &status, 0, nil)
		if err != nil {
			time.Sleep(1 * time.Second)
			continue
		}
		if pid > 0 {
			log.Printf("reaped pid %d (status=%d)", pid, status.ExitStatus())
		}
	}
}

func findBinary(names ...string) string {
	for _, name := range names {
		if filepath.IsAbs(name) {
			if _, err := os.Stat(name); err == nil {
				return name
			}
			continue
		}
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	return ""
}
