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
