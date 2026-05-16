//go:build linux

package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"vulos/backend/services/hwdetect"
)

// vulos-init: Custom PID 1 for Debian Linux.
// Mounts filesystems, starts networking, launches systemd, then hands off to the vulos server.
//
// Build: GOOS=linux GOARCH=amd64 go build -o vulos-init ./cmd/init
// Install: copy to /sbin/init (or set init=/sbin/vulos-init in kernel cmdline)

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

	// Start in background
	cmd := exec.Command(serverBin, "-env", "main")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = os.Environ()

	if err := cmd.Start(); err != nil {
		log.Printf("failed to start vulos-server: %v", err)
		return
	}
	log.Printf("vulos-server started (pid=%d)", cmd.Process.Pid)

	// Start Cage/WPE WebKit kiosk if available
	startKiosk()
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
	if !displayConnected() {
		log.Println("no display connected, skipping kiosk (headless mode)")
		return
	}

	// Shared runtime dir for Wayland socket.
	os.MkdirAll("/run/user/0", 0700)
	baseEnv := append(os.Environ(),
		"XDG_RUNTIME_DIR=/run/user/0",
		"WAYLAND_DISPLAY=wayland-1",
		"WLR_LIBINPUT_NO_DEVICES=1",
	)

	// Wait for server to be ready before launching browser.
	time.Sleep(2 * time.Second)

	plymouthProgress(100)      // milestone: kiosk up
	plymouthQuitRetainSplash() // hand off splash to compositor (both labwc + cage paths)

	// Prefer labwc (multi-window, traffic-light SSD, browser-as-background).
	labwcBin, err := exec.LookPath("labwc")
	if err == nil {
		startLabwcKiosk(labwcBin, baseEnv)
		return
	}

	// labwc absent → cage fallback (single-window kiosk, existing behaviour).
	log.Println("labwc not found, falling back to cage kiosk")
	cage, err := exec.LookPath("cage")
	if err != nil {
		log.Println("cage not found, skipping kiosk mode")
		return
	}
	wpe := findBinary("cog", "/usr/bin/cog", "/usr/bin/wpe-webkit-launcher")
	if wpe == "" {
		log.Println("WPE WebKit not found, skipping kiosk")
		return
	}
	cmd := exec.Command(cage, "--", wpe, "http://localhost:8080")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = baseEnv
	if err := cmd.Start(); err != nil {
		log.Printf("cage start error: %v", err)
		return
	}
	log.Printf("cage kiosk started (pid=%d)", cmd.Process.Pid)
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

// startLabwcKiosk launches labwc with assets/labwc config then pins the
// browser (Cog/WPE preferred, Chromium fallback) to the background layer.
func startLabwcKiosk(labwcBin string, baseEnv []string) {
	// Point labwc at the shipped config (BMINIT-01 assets/labwc/).
	// XDG_CONFIG_DIRS tells labwc to look in assets/labwc for rc.xml etc.
	labwcConfigDir := findLabwcConfigDir()
	labwcEnv := append(baseEnv, "XDG_CONFIG_DIRS=/usr/share:/etc:/usr/local/share")
	if labwcConfigDir != "" {
		labwcEnv = append(labwcEnv, "LABWC_CONFIG_DIR="+labwcConfigDir)
	}

	labwcCmd := exec.Command(labwcBin)
	labwcCmd.Stdout = os.Stdout
	labwcCmd.Stderr = os.Stderr
	labwcCmd.Env = labwcEnv

	if err := labwcCmd.Start(); err != nil {
		log.Printf("labwc start error: %v", err)
		return
	}
	log.Printf("labwc started (pid=%d)", labwcCmd.Process.Pid)

	// Give labwc a moment to create the Wayland socket.
	time.Sleep(1 * time.Second)

	// Browser: Cog/WPE preferred (lightweight, no chrome); Chromium as fallback.
	browserBin, browserArgs := findKioskBrowser()
	if browserBin == "" {
		log.Println("no browser found (cog/chromium), labwc running without browser")
		return
	}

	browserCmd := exec.Command(browserBin, browserArgs...)
	browserCmd.Stdout = os.Stdout
	browserCmd.Stderr = os.Stderr
	browserCmd.Env = baseEnv

	if err := browserCmd.Start(); err != nil {
		log.Printf("browser start error: %v", err)
		return
	}
	log.Printf("kiosk browser started under labwc (bin=%s pid=%d)", browserBin, browserCmd.Process.Pid)
}

// findLabwcConfigDir returns the assets/labwc config directory if it exists,
// checking paths relative to the binary and well-known install locations.
func findLabwcConfigDir() string {
	candidates := []string{
		"/usr/share/vulos/assets/labwc",
		"/opt/vulos/assets/labwc",
		"assets/labwc",
	}
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// findKioskBrowser returns the browser binary + args for labwc kiosk mode.
// Cog (WPE WebKit) runs fullscreen on Wayland natively; the labwc window rule
// in rc.xml pins it to the background layer (BMINIT-01 config).
// Chromium is the fallback with equivalent --kiosk flags.
func findKioskBrowser() (string, []string) {
	cogBin := findBinary("cog", "/usr/bin/cog")
	if cogBin != "" {
		// --platform=wl → native Wayland, no cage wrapper needed.
		return cogBin, []string{"--platform=wl", "http://localhost:8080"}
	}
	chromiumBin := findBinary(
		"chromium", "chromium-browser",
		"/usr/bin/chromium", "/usr/bin/chromium-browser",
	)
	if chromiumBin != "" {
		return chromiumBin, []string{
			"--kiosk",
			"--ozone-platform=wayland",
			"--no-sandbox",
			"http://localhost:8080",
		}
	}
	return "", nil
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
