// Package drivers detects hardware and manages kernel modules (like Ubuntu's Additional Drivers).
package drivers

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Device represents a detected hardware device.
type Device struct {
	Bus         string `json:"bus"`          // pci, usb, platform
	ID          string `json:"id"`           // vendor:product
	Vendor      string `json:"vendor"`
	Name        string `json:"name"`
	Class       string `json:"class"`        // display, network, sound, etc.
	Driver      string `json:"driver"`       // currently bound driver
	Module      string `json:"module"`       // kernel module name
	DriverState string `json:"driver_state"` // active, available, missing
}

// Module represents a loaded or available kernel module.
type Module struct {
	Name    string `json:"name"`
	Size    string `json:"size"`
	UsedBy  string `json:"used_by"`
	Loaded  bool   `json:"loaded"`
	Builtin bool   `json:"builtin"`
}

// Status is the full driver status payload.
type Status struct {
	Devices  []Device `json:"devices"`
	Modules  []Module `json:"modules"`
	Kernel   string   `json:"kernel"`
}

// Detect scans the system for hardware and driver status.
func Detect(ctx context.Context) Status {
	s := Status{}

	// Kernel version
	if data, err := os.ReadFile("/proc/version"); err == nil {
		parts := strings.Fields(string(data))
		if len(parts) >= 3 {
			s.Kernel = parts[2]
		}
	}

	s.Devices = detectPCI(ctx)
	s.Devices = append(s.Devices, detectUSB(ctx)...)
	s.Modules = listModules()

	return s
}

func detectPCI(ctx context.Context) []Device {
	var devices []Device
	out, err := exec.CommandContext(ctx, "lspci", "-vmm", "-k").Output()
	if err != nil {
		// Fallback: scan /sys/bus/pci/devices
		return detectPCISys()
	}

	// Parse lspci -vmm -k output (blocks separated by blank lines)
	blocks := strings.Split(string(out), "\n\n")
	for _, block := range blocks {
		d := Device{Bus: "pci"}
		lines := strings.Split(strings.TrimSpace(block), "\n")
		for _, line := range lines {
			parts := strings.SplitN(line, "\t", 2)
			if len(parts) != 2 {
				parts = strings.SplitN(line, ":\t", 2)
			}
			if len(parts) != 2 {
				continue
			}
			key := strings.TrimSuffix(strings.TrimSpace(parts[0]), ":")
			val := strings.TrimSpace(parts[1])
			switch key {
			case "Slot":
				d.ID = val
			case "Class":
				d.Class = classifyPCI(val)
			case "Vendor":
				d.Vendor = val
			case "Device":
				d.Name = val
			case "Driver":
				d.Driver = val
				d.DriverState = "active"
			case "Module":
				d.Module = val
			}
		}
		if d.Name != "" {
			if d.Driver == "" {
				d.DriverState = "missing"
			}
			devices = append(devices, d)
		}
	}
	return devices
}

func detectPCISys() []Device {
	var devices []Device
	base := "/sys/bus/pci/devices"
	entries, err := os.ReadDir(base)
	if err != nil {
		return nil
	}
	for _, e := range entries {
		d := Device{Bus: "pci", ID: e.Name()}
		dpath := filepath.Join(base, e.Name())

		if data, err := os.ReadFile(filepath.Join(dpath, "vendor")); err == nil {
			d.Vendor = strings.TrimSpace(string(data))
		}
		if data, err := os.ReadFile(filepath.Join(dpath, "device")); err == nil {
			d.Name = strings.TrimSpace(string(data))
		}
		if data, err := os.ReadFile(filepath.Join(dpath, "class")); err == nil {
			d.Class = classifyPCICode(strings.TrimSpace(string(data)))
		}
		// Check bound driver
		driverLink, err := os.Readlink(filepath.Join(dpath, "driver"))
		if err == nil {
			d.Driver = filepath.Base(driverLink)
			d.DriverState = "active"
		} else {
			d.DriverState = "missing"
		}

		if d.Vendor != "" || d.Name != "" {
			devices = append(devices, d)
		}
	}
	return devices
}

func detectUSB(ctx context.Context) []Device {
	var devices []Device
	out, err := exec.CommandContext(ctx, "lsusb").Output()
	if err != nil {
		return detectUSBSys()
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		// Bus 001 Device 002: ID 1d6b:0003 Linux Foundation 3.0 root hub
		d := Device{Bus: "usb", DriverState: "active"}
		if idx := strings.Index(line, "ID "); idx >= 0 {
			rest := line[idx+3:]
			parts := strings.SplitN(rest, " ", 2)
			d.ID = parts[0]
			if len(parts) > 1 {
				d.Name = parts[1]
			}
		}
		d.Class = "usb"
		devices = append(devices, d)
	}
	return devices
}

func detectUSBSys() []Device {
	var devices []Device
	base := "/sys/bus/usb/devices"
	entries, err := os.ReadDir(base)
	if err != nil {
		return nil
	}
	for _, e := range entries {
		dpath := filepath.Join(base, e.Name())
		d := Device{Bus: "usb", ID: e.Name(), Class: "usb"}

		if data, err := os.ReadFile(filepath.Join(dpath, "manufacturer")); err == nil {
			d.Vendor = strings.TrimSpace(string(data))
		}
		if data, err := os.ReadFile(filepath.Join(dpath, "product")); err == nil {
			d.Name = strings.TrimSpace(string(data))
		}
		driverLink, _ := os.Readlink(filepath.Join(dpath, "driver"))
		if driverLink != "" {
			d.Driver = filepath.Base(driverLink)
			d.DriverState = "active"
		} else {
			d.DriverState = "missing"
		}

		if d.Name != "" || d.Vendor != "" {
			devices = append(devices, d)
		}
	}
	return devices
}

func listModules() []Module {
	var modules []Module
	data, err := os.ReadFile("/proc/modules")
	if err != nil {
		return nil
	}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		m := Module{
			Name:   fields[0],
			Size:   fields[1],
			UsedBy: fields[2],
			Loaded: true,
		}
		modules = append(modules, m)
	}
	return modules
}

// LoadModule loads a kernel module via modprobe.
func LoadModule(ctx context.Context, name string) error {
	return exec.CommandContext(ctx, "modprobe", name).Run()
}

// UnloadModule unloads a kernel module via modprobe -r.
func UnloadModule(ctx context.Context, name string) error {
	return exec.CommandContext(ctx, "modprobe", "-r", name).Run()
}

func classifyPCI(desc string) string {
	d := strings.ToLower(desc)
	switch {
	case strings.Contains(d, "display"), strings.Contains(d, "vga"), strings.Contains(d, "3d"):
		return "display"
	case strings.Contains(d, "network"), strings.Contains(d, "ethernet"), strings.Contains(d, "wireless"):
		return "network"
	case strings.Contains(d, "audio"), strings.Contains(d, "sound"), strings.Contains(d, "multimedia"):
		return "audio"
	case strings.Contains(d, "storage"), strings.Contains(d, "sata"), strings.Contains(d, "nvme"), strings.Contains(d, "ide"):
		return "storage"
	case strings.Contains(d, "usb"):
		return "usb"
	case strings.Contains(d, "bridge"):
		return "bridge"
	case strings.Contains(d, "serial"), strings.Contains(d, "communication"):
		return "serial"
	default:
		return "other"
	}
}

func classifyPCICode(code string) string {
	// PCI class codes: 0x0300xx = display, 0x0200xx = network, etc.
	code = strings.TrimPrefix(code, "0x")
	if len(code) < 4 {
		return "other"
	}
	switch code[:2] {
	case "03":
		return "display"
	case "02":
		return "network"
	case "04":
		return "audio"
	case "01":
		return "storage"
	case "0c":
		return "usb"
	case "06":
		return "bridge"
	default:
		return "other"
	}
}
