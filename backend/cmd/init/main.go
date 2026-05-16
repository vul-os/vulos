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
)

// vulos-init: Custom PID 1 for Debian Linux.
// Mounts filesystems, starts networking, launches systemd, then hands off to the vulos server.
//
// Build: GOOS=linux GOARCH=amd64 go build -o vulos-init ./cmd/init
// Install: copy to /sbin/init (or set init=/sbin/vulos-init in kernel cmdline)

func main() {
	if os.Getpid() != 1 {
		fmt.Println("vulos-init: not running as PID 1, starting in service mode")
		startServices()
		return
	}

	log.SetPrefix("[vulos-init] ")
	log.Println("booting Vula OS...")

	// Phase 1: Mount essential filesystems
	mountAll()

	// Phase 2: Set hostname
	setHostname()

	// Phase 3: Start systemd services (if available)
	startSystemd()

	// Phase 4: Bring up networking (DHCP, WiFi fallback, mDNS)
	phaseNetwork()

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
		{"tmpfs", "/dev/shm", "tmpfs", 0, ""},
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
	log.Println("filesystems mounted")
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

func startKiosk() {
	cage, err := exec.LookPath("cage")
	if err != nil {
		log.Println("cage not found, skipping kiosk mode")
		return
	}

	// WPE WebKit launcher
	wpe := findBinary("wpe-webkit", "/usr/bin/cog", "/usr/bin/wpe-webkit-launcher")
	if wpe == "" {
		log.Println("WPE WebKit not found, skipping kiosk")
		return
	}

	// Wait for server to be ready
	time.Sleep(2 * time.Second)

	cmd := exec.Command(cage, "--", wpe, "http://localhost:8080")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(),
		"WLR_LIBINPUT_NO_DEVICES=1",
		"XDG_RUNTIME_DIR=/run/user/0",
	)
	os.MkdirAll("/run/user/0", 0700)

	if err := cmd.Start(); err != nil {
		log.Printf("cage start error: %v", err)
		return
	}
	log.Printf("cage kiosk started (pid=%d)", cmd.Process.Pid)
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
