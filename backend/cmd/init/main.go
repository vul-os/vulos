//go:build linux

package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
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

	// Phase 4: Start vulos server
	startServices()

	// Phase 5: Reap zombies (PID 1 duty)
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
