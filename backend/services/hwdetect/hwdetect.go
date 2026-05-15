//go:build linux

// Package hwdetect probes hardware present at boot time and writes a structured
// report to /var/log/vulos-boot.log. All probes are best-effort: a failure in
// any individual probe is logged but does not abort the phase.
package hwdetect

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"vulos/backend/services/gpu"
)

const bootLogPath = "/var/log/vulos-boot.log"

// AudioInfo describes the audio subsystem found on the machine.
type AudioInfo struct {
	Cards   []string `json:"cards"`    // ALSA card names from /proc/asound/cards
	HasALSA bool     `json:"has_alsa"` // /proc/asound/ exists
	Backend string   `json:"backend"`  // "pipewire", "pulseaudio", or "alsa"
}

// InputDevice describes a single /dev/input/event* device.
type InputDevice struct {
	Path string `json:"path"`
	Name string `json:"name"` // from /sys/class/input/eventN/device/name
}

// InputInfo lists all detected input devices.
type InputInfo struct {
	Devices     []InputDevice `json:"devices"`
	HasKeyboard bool          `json:"has_keyboard"`
	HasMouse    bool          `json:"has_mouse"`
	HasTouch    bool          `json:"has_touch"`
	HasGamepad  bool          `json:"has_gamepad"`
}

// NetInterface describes a network interface from /sys/class/net/.
type NetInterface struct {
	Name      string `json:"name"`
	Type      string `json:"type"`      // "wired", "wireless", "loopback", "other"
	Operstate string `json:"operstate"` // "up", "down", "unknown"
}

// NetInfo lists all detected network interfaces.
type NetInfo struct {
	Interfaces  []NetInterface `json:"interfaces"`
	HasWired    bool           `json:"has_wired"`
	HasWireless bool           `json:"has_wireless"`
}

// StorageDisk describes a block device from /sys/block/.
type StorageDisk struct {
	Name      string `json:"name"`
	Type      string `json:"type"`       // "nvme", "sata", "mmc", "usb", "other"
	SizeBytes int64  `json:"size_bytes"` // from /sys/block/<name>/size (512-byte sectors)
	Removable bool   `json:"removable"`  // /sys/block/<name>/removable
}

// StorageInfo lists all detected block devices.
type StorageInfo struct {
	Disks []StorageDisk `json:"disks"`
}

// BatteryInfo describes the power supply / battery state.
type BatteryInfo struct {
	Present    bool   `json:"present"`    // any battery found
	Status     string `json:"status"`     // "Charging", "Discharging", "Full", "Unknown"
	Percentage int    `json:"percentage"` // 0-100, -1 if unknown
	IsLaptop   bool   `json:"is_laptop"`  // true when battery is present
}

// Report is the top-level hardware detection result written to the boot log.
type Report struct {
	Timestamp string      `json:"timestamp"`
	GPU       gpu.Info    `json:"gpu"`
	Audio     AudioInfo   `json:"audio"`
	Input     InputInfo   `json:"input"`
	Network   NetInfo     `json:"network"`
	Storage   StorageInfo `json:"storage"`
	Battery   BatteryInfo `json:"battery"`
}

// Run executes all hardware probes, logs each result, and writes the full
// report to /var/log/vulos-boot.log. Errors from individual probes are
// logged but do not propagate — this phase is always non-fatal.
func Run() {
	log.Println("[hwdetect] starting hardware detection phase")

	report := Report{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}

	// GPU — reuse existing gpu.Detect() which is already cached.
	report.GPU = probeGPU()

	// Audio
	report.Audio = probeAudio()

	// Input devices
	report.Input = probeInput()

	// Network interfaces
	report.Network = probeNetwork()

	// Block storage
	report.Storage = probeStorage()

	// Battery / power supply
	report.Battery = probeBattery()

	// Write structured report to boot log.
	writeBootLog(report)

	log.Printf("[hwdetect] detection complete: gpu=%s audio_backend=%s wired=%v wireless=%v laptop=%v",
		report.GPU.TierName, report.Audio.Backend, report.Network.HasWired,
		report.Network.HasWireless, report.Battery.IsLaptop)
}

// probeGPU wraps gpu.Detect so that any panic is caught and a zero Info
// returned rather than crashing the init process.
func probeGPU() (info gpu.Info) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[hwdetect] gpu probe panic: %v", r)
		}
	}()
	info = gpu.Detect()
	log.Printf("[hwdetect] gpu: tier=%s vendor=%s device=%q", info.TierName, info.Vendor, info.Device)
	return info
}

// probeAudio reads /proc/asound/cards for ALSA cards, then tries to detect
// PipeWire or PulseAudio as the active audio backend.
func probeAudio() AudioInfo {
	info := AudioInfo{}

	cards, err := os.ReadFile("/proc/asound/cards")
	if err != nil {
		log.Printf("[hwdetect] audio: /proc/asound/cards unavailable: %v", err)
	} else {
		info.HasALSA = true
		// Each card occupies two lines; odd lines have the long description.
		// We just collect non-empty lines that start with a digit (card index lines).
		for _, line := range strings.Split(string(cards), "\n") {
			line = strings.TrimSpace(line)
			if len(line) == 0 {
				continue
			}
			// Index lines look like " 0 [HDMI           ]: HDA-Intel - HDA Intel PCH"
			if len(line) > 0 && (line[0] >= '0' && line[0] <= '9') {
				// Extract the name between brackets.
				start := strings.Index(line, "[")
				end := strings.Index(line, "]")
				if start >= 0 && end > start {
					name := strings.TrimSpace(line[start+1 : end])
					info.Cards = append(info.Cards, name)
				}
			}
		}
		log.Printf("[hwdetect] audio: %d ALSA card(s): %v", len(info.Cards), info.Cards)
	}

	// Detect backend by checking process / socket presence.
	info.Backend = detectAudioBackend()
	log.Printf("[hwdetect] audio: backend=%s", info.Backend)
	return info
}

// detectAudioBackend checks for PipeWire or PulseAudio sockets / binaries.
func detectAudioBackend() string {
	// PipeWire runtime socket (XDG_RUNTIME_DIR / run/user/0)
	for _, dir := range []string{
		os.Getenv("XDG_RUNTIME_DIR"),
		"/run/user/0",
		"/run/user/1000",
	} {
		if dir == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, "pipewire-0")); err == nil {
			return "pipewire"
		}
	}

	// PulseAudio socket
	for _, dir := range []string{
		os.Getenv("XDG_RUNTIME_DIR"),
		"/run/user/0",
		"/run/user/1000",
		"/tmp",
	} {
		if dir == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, "pulse/native")); err == nil {
			return "pulseaudio"
		}
	}

	if _, err := os.Stat("/proc/asound"); err == nil {
		return "alsa"
	}
	return "none"
}

// probeInput enumerates /dev/input/event* and reads device names from sysfs.
func probeInput() InputInfo {
	info := InputInfo{}

	matches, err := filepath.Glob("/dev/input/event*")
	if err != nil || len(matches) == 0 {
		log.Println("[hwdetect] input: no /dev/input/event* devices found")
		return info
	}

	for _, evPath := range matches {
		devName := filepath.Base(evPath) // e.g. "event0"
		name := readSysInputName(devName)

		dev := InputDevice{Path: evPath, Name: name}
		info.Devices = append(info.Devices, dev)

		lower := strings.ToLower(name)
		switch {
		case strings.Contains(lower, "keyboard") || strings.Contains(lower, "kbd"):
			info.HasKeyboard = true
		case strings.Contains(lower, "mouse") || strings.Contains(lower, "trackpad") || strings.Contains(lower, "touchpad"):
			info.HasMouse = true
		case strings.Contains(lower, "touch") && !strings.Contains(lower, "touchpad"):
			info.HasTouch = true
		case strings.Contains(lower, "gamepad") || strings.Contains(lower, "joystick") || strings.Contains(lower, "controller"):
			info.HasGamepad = true
		}
	}

	log.Printf("[hwdetect] input: %d device(s) (keyboard=%v mouse=%v touch=%v gamepad=%v)",
		len(info.Devices), info.HasKeyboard, info.HasMouse, info.HasTouch, info.HasGamepad)
	return info
}

// readSysInputName returns the human-readable name for an input event device
// (e.g. "event0") from /sys/class/input/<eventN>/device/name.
func readSysInputName(eventN string) string {
	nameFile := filepath.Join("/sys/class/input", eventN, "device/name")
	data, err := os.ReadFile(nameFile)
	if err != nil {
		return eventN
	}
	return strings.TrimSpace(string(data))
}

// probeNetwork reads /sys/class/net/ to list interfaces and their types.
func probeNetwork() NetInfo {
	info := NetInfo{}

	entries, err := os.ReadDir("/sys/class/net")
	if err != nil {
		log.Printf("[hwdetect] network: /sys/class/net unavailable: %v", err)
		return info
	}

	for _, e := range entries {
		name := e.Name()
		iface := NetInterface{
			Name:      name,
			Type:      classifyNetInterface(name),
			Operstate: readSysNetFile(name, "operstate"),
		}
		info.Interfaces = append(info.Interfaces, iface)

		switch iface.Type {
		case "wired":
			info.HasWired = true
		case "wireless":
			info.HasWireless = true
		}
	}

	log.Printf("[hwdetect] network: %d interface(s) (wired=%v wireless=%v)",
		len(info.Interfaces), info.HasWired, info.HasWireless)
	return info
}

// classifyNetInterface guesses interface type from its name and sysfs.
func classifyNetInterface(name string) string {
	if name == "lo" {
		return "loopback"
	}
	// Wireless: /sys/class/net/<name>/wireless/ exists or name prefix
	if _, err := os.Stat(filepath.Join("/sys/class/net", name, "wireless")); err == nil {
		return "wireless"
	}
	lname := strings.ToLower(name)
	if strings.HasPrefix(lname, "wl") || strings.HasPrefix(lname, "wlan") {
		return "wireless"
	}
	if strings.HasPrefix(lname, "eth") || strings.HasPrefix(lname, "en") ||
		strings.HasPrefix(lname, "eno") || strings.HasPrefix(lname, "enp") ||
		strings.HasPrefix(lname, "ens") {
		return "wired"
	}
	return "other"
}

// readSysNetFile reads a single-value file under /sys/class/net/<iface>/<file>.
func readSysNetFile(iface, file string) string {
	data, err := os.ReadFile(filepath.Join("/sys/class/net", iface, file))
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(data))
}

// probeStorage reads /sys/block/ to enumerate block devices.
func probeStorage() StorageInfo {
	info := StorageInfo{}

	entries, err := os.ReadDir("/sys/block")
	if err != nil {
		log.Printf("[hwdetect] storage: /sys/block unavailable: %v", err)
		return info
	}

	for _, e := range entries {
		name := e.Name()
		// Skip loop devices and ram disks.
		if strings.HasPrefix(name, "loop") || strings.HasPrefix(name, "ram") {
			continue
		}

		disk := StorageDisk{
			Name:      name,
			Type:      classifyBlockDevice(name),
			SizeBytes: readBlockSizeBytes(name),
			Removable: readSysBlockBool(name, "removable"),
		}
		info.Disks = append(info.Disks, disk)
	}

	log.Printf("[hwdetect] storage: %d disk(s)", len(info.Disks))
	for _, d := range info.Disks {
		log.Printf("[hwdetect]   disk %s type=%s size=%s removable=%v",
			d.Name, d.Type, formatBytes(d.SizeBytes), d.Removable)
	}
	return info
}

// classifyBlockDevice guesses disk type from its name prefix.
func classifyBlockDevice(name string) string {
	switch {
	case strings.HasPrefix(name, "nvme"):
		return "nvme"
	case strings.HasPrefix(name, "sd"):
		return "sata" // may be USB; removable flag distinguishes
	case strings.HasPrefix(name, "mmc"):
		return "mmc"
	case strings.HasPrefix(name, "vd"):
		return "virtio"
	default:
		return "other"
	}
}

// readBlockSizeBytes returns disk capacity in bytes (sectors × 512).
func readBlockSizeBytes(name string) int64 {
	data, err := os.ReadFile(filepath.Join("/sys/block", name, "size"))
	if err != nil {
		return 0
	}
	var sectors int64
	fmt.Sscanf(strings.TrimSpace(string(data)), "%d", &sectors)
	return sectors * 512
}

// readSysBlockBool reads a "0" or "1" flag from /sys/block/<name>/<file>.
func readSysBlockBool(name, file string) bool {
	data, err := os.ReadFile(filepath.Join("/sys/block", name, file))
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(data)) == "1"
}

// formatBytes renders a byte count as a human-readable string (GiB/MiB).
func formatBytes(b int64) string {
	const gib = 1 << 30
	const mib = 1 << 20
	switch {
	case b >= gib:
		return fmt.Sprintf("%.1f GiB", float64(b)/gib)
	case b >= mib:
		return fmt.Sprintf("%.1f MiB", float64(b)/mib)
	default:
		return fmt.Sprintf("%d B", b)
	}
}

// probeBattery reads /sys/class/power_supply/ to detect laptop batteries.
func probeBattery() BatteryInfo {
	info := BatteryInfo{Percentage: -1}

	entries, err := os.ReadDir("/sys/class/power_supply")
	if err != nil {
		log.Printf("[hwdetect] battery: /sys/class/power_supply unavailable: %v", err)
		return info
	}

	for _, e := range entries {
		name := e.Name()
		psType := readPowerSupplyFile(name, "type")

		if strings.EqualFold(psType, "Battery") {
			info.Present = true
			info.IsLaptop = true
			info.Status = readPowerSupplyFile(name, "status")

			// Read charge percentage (may be in "capacity" or computed from charge_now/charge_full).
			if cap := readPowerSupplyFile(name, "capacity"); cap != "" {
				var pct int
				fmt.Sscanf(cap, "%d", &pct)
				info.Percentage = pct
			}

			log.Printf("[hwdetect] battery: %s status=%s pct=%d%%", name, info.Status, info.Percentage)
			break // report first battery found
		}
	}

	if !info.Present {
		log.Println("[hwdetect] battery: none (desktop or no battery detected)")
	}
	return info
}

// readPowerSupplyFile reads a single-value file from /sys/class/power_supply/<name>/<file>.
func readPowerSupplyFile(name, file string) string {
	data, err := os.ReadFile(filepath.Join("/sys/class/power_supply", name, file))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// writeBootLog serialises the Report as pretty-printed JSON and appends it
// (with a separator) to /var/log/vulos-boot.log. Non-fatal on any error.
func writeBootLog(report Report) {
	// Ensure the log directory exists.
	if err := os.MkdirAll(filepath.Dir(bootLogPath), 0755); err != nil {
		log.Printf("[hwdetect] boot log: cannot create dir: %v", err)
		return
	}

	f, err := os.OpenFile(bootLogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		log.Printf("[hwdetect] boot log: cannot open %s: %v", bootLogPath, err)
		return
	}
	defer f.Close()

	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")

	// Write a separator so multiple boot entries are distinguishable.
	if _, err := fmt.Fprintf(f, "--- vulos-boot %s ---\n", report.Timestamp); err != nil {
		log.Printf("[hwdetect] boot log: write separator: %v", err)
		return
	}

	if err := enc.Encode(report); err != nil {
		log.Printf("[hwdetect] boot log: encode error: %v", err)
		return
	}

	log.Printf("[hwdetect] boot log written to %s", bootLogPath)
}
