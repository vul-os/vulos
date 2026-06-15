package drivers

import (
	"context"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// classifyPCI — PCI class description string → category
// ---------------------------------------------------------------------------

func TestClassifyPCI_Display(t *testing.T) {
	for _, desc := range []string{
		"VGA compatible controller",
		"Display controller",
		"3D controller",
		"3D Controller (NVIDIA GeForce RTX 4090)",
	} {
		if got := classifyPCI(desc); got != "display" {
			t.Errorf("classifyPCI(%q) = %q, want display", desc, got)
		}
	}
}

func TestClassifyPCI_Network(t *testing.T) {
	for _, desc := range []string{
		"Ethernet controller",
		"Network controller",
		"Wireless Network Adapter",
	} {
		if got := classifyPCI(desc); got != "network" {
			t.Errorf("classifyPCI(%q) = %q, want network", desc, got)
		}
	}
}

func TestClassifyPCI_Audio(t *testing.T) {
	for _, desc := range []string{
		"Audio device",
		"Multimedia audio controller",
		"High Definition Audio Controller",
		"Sound card",
	} {
		if got := classifyPCI(desc); got != "audio" {
			t.Errorf("classifyPCI(%q) = %q, want audio", desc, got)
		}
	}
}

func TestClassifyPCI_Storage(t *testing.T) {
	for _, desc := range []string{
		"SATA controller",
		"NVMe Solid State Drive",
		"IDE controller",
		"Mass storage controller",
	} {
		if got := classifyPCI(desc); got != "storage" {
			t.Errorf("classifyPCI(%q) = %q, want storage", desc, got)
		}
	}
}

func TestClassifyPCI_USB(t *testing.T) {
	for _, desc := range []string{
		"USB controller",
		"USB 3.0 Host Controller",
	} {
		if got := classifyPCI(desc); got != "usb" {
			t.Errorf("classifyPCI(%q) = %q, want usb", desc, got)
		}
	}
}

func TestClassifyPCI_Bridge(t *testing.T) {
	for _, desc := range []string{
		"PCI bridge",
		"Host bridge",
		"ISA bridge",
	} {
		if got := classifyPCI(desc); got != "bridge" {
			t.Errorf("classifyPCI(%q) = %q, want bridge", desc, got)
		}
	}
}

func TestClassifyPCI_Serial(t *testing.T) {
	for _, desc := range []string{
		"Serial controller",
		"Communication controller",
	} {
		if got := classifyPCI(desc); got != "serial" {
			t.Errorf("classifyPCI(%q) = %q, want serial", desc, got)
		}
	}
}

func TestClassifyPCI_Other(t *testing.T) {
	for _, desc := range []string{
		"",
		"Unknown device",
		"Signal processing controller",
		"System peripheral",
	} {
		if got := classifyPCI(desc); got != "other" {
			t.Errorf("classifyPCI(%q) = %q, want other", desc, got)
		}
	}
}

func TestClassifyPCI_CaseInsensitive(t *testing.T) {
	// Verify lower/upper/mixed all map the same.
	cases := []struct{ in, want string }{
		{"VGA Compatible Controller", "display"},
		{"vga compatible controller", "display"},
		{"ETHERNET CONTROLLER", "network"},
		{"ethernet controller", "network"},
	}
	for _, c := range cases {
		if got := classifyPCI(c.in); got != c.want {
			t.Errorf("classifyPCI(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// ---------------------------------------------------------------------------
// classifyPCICode — raw hex PCI class code → category
// ---------------------------------------------------------------------------

func TestClassifyPCICode_Display(t *testing.T) {
	for _, code := range []string{"0x030000", "0x030200", "0x0300ff"} {
		if got := classifyPCICode(code); got != "display" {
			t.Errorf("classifyPCICode(%q) = %q, want display", code, got)
		}
	}
}

func TestClassifyPCICode_Network(t *testing.T) {
	for _, code := range []string{"0x020000", "0x0200ff"} {
		if got := classifyPCICode(code); got != "network" {
			t.Errorf("classifyPCICode(%q) = %q, want network", code, got)
		}
	}
}

func TestClassifyPCICode_Audio(t *testing.T) {
	for _, code := range []string{"0x040100", "0x040300"} {
		if got := classifyPCICode(code); got != "audio" {
			t.Errorf("classifyPCICode(%q) = %q, want audio", code, got)
		}
	}
}

func TestClassifyPCICode_Storage(t *testing.T) {
	for _, code := range []string{"0x010000", "0x010601", "0x018000"} {
		if got := classifyPCICode(code); got != "storage" {
			t.Errorf("classifyPCICode(%q) = %q, want storage", code, got)
		}
	}
}

func TestClassifyPCICode_USB(t *testing.T) {
	for _, code := range []string{"0x0c0330", "0x0c03fe", "0x0c0310"} {
		if got := classifyPCICode(code); got != "usb" {
			t.Errorf("classifyPCICode(%q) = %q, want usb", code, got)
		}
	}
}

func TestClassifyPCICode_Bridge(t *testing.T) {
	for _, code := range []string{"0x060000", "0x060400"} {
		if got := classifyPCICode(code); got != "bridge" {
			t.Errorf("classifyPCICode(%q) = %q, want bridge", code, got)
		}
	}
}

func TestClassifyPCICode_Other(t *testing.T) {
	for _, code := range []string{"0x110000", "0x0b0000", ""} {
		if got := classifyPCICode(code); got != "other" {
			t.Errorf("classifyPCICode(%q) = %q, want other", code, got)
		}
	}
}

func TestClassifyPCICode_TooShort(t *testing.T) {
	// Strings shorter than 4 hex digits after stripping "0x" → other
	for _, code := range []string{"0x03", "0x", "03"} {
		if got := classifyPCICode(code); got != "other" {
			t.Errorf("classifyPCICode(%q) = %q, want other (too short)", code, got)
		}
	}
}

func TestClassifyPCICode_WithoutPrefix(t *testing.T) {
	// Code without 0x prefix — first two digits used
	if got := classifyPCICode("030000"); got != "display" {
		t.Errorf("classifyPCICode(030000) = %q, want display", got)
	}
}

// ---------------------------------------------------------------------------
// lspci -vmm -k block parser
// ---------------------------------------------------------------------------

// parseLspciBlocks mirrors detectPCI's block parsing logic, without exec.
func parseLspciBlocks(raw string) []Device {
	var devices []Device
	blocks := strings.Split(raw, "\n\n")
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

// lsusb line parser mirrors detectUSB loop, without exec.
func parseLsusbLines(raw string) []Device {
	var devices []Device
	for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
		if line == "" {
			continue
		}
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

// /proc/modules line parser mirrors listModules, without filesystem access.
func parseProcModules(raw string) []Module {
	var modules []Module
	for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		modules = append(modules, Module{
			Name:   fields[0],
			Size:   fields[1],
			UsedBy: fields[2],
			Loaded: true,
		})
	}
	return modules
}

// ---------------------------------------------------------------------------
// lspci fixture tests
// ---------------------------------------------------------------------------

const lspciFixture = `Slot:	00:02.0
Class:	VGA compatible controller
Vendor:	Intel Corporation
Device:	Alder Lake-P GT2 [Iris Xe Graphics]
Driver:	i915
Module:	i915

Slot:	00:1f.3
Class:	Audio device
Vendor:	Intel Corporation
Device:	Alder Lake PCH-P High Definition Audio Controller
Module:	snd_hda_intel

Slot:	01:00.0
Class:	Network controller
Vendor:	Realtek Semiconductor Co., Ltd.
Device:	RTL8852BE PCIe 802.11ax Wireless Network Controller
Driver:	rtw89_8852be
Module:	rtw89_8852be

`

func TestParseLspci_Count(t *testing.T) {
	devices := parseLspciBlocks(lspciFixture)
	if len(devices) != 3 {
		t.Fatalf("expected 3 devices, got %d", len(devices))
	}
}

func TestParseLspci_DisplayDevice(t *testing.T) {
	d := parseLspciBlocks(lspciFixture)[0]
	if d.Class != "display" {
		t.Errorf("class: want display, got %q", d.Class)
	}
	if d.Vendor != "Intel Corporation" {
		t.Errorf("vendor: want Intel Corporation, got %q", d.Vendor)
	}
	if d.Driver != "i915" {
		t.Errorf("driver: want i915, got %q", d.Driver)
	}
	if d.DriverState != "active" {
		t.Errorf("driver state: want active, got %q", d.DriverState)
	}
	if d.Module != "i915" {
		t.Errorf("module: want i915, got %q", d.Module)
	}
}

func TestParseLspci_MissingDriver(t *testing.T) {
	// Audio device has no Driver line → state should be "missing"
	d := parseLspciBlocks(lspciFixture)[1]
	if d.DriverState != "missing" {
		t.Errorf("expected missing driver state, got %q", d.DriverState)
	}
	if d.Driver != "" {
		t.Errorf("expected empty driver, got %q", d.Driver)
	}
}

func TestParseLspci_NetworkDevice(t *testing.T) {
	d := parseLspciBlocks(lspciFixture)[2]
	if d.Class != "network" {
		t.Errorf("class: want network, got %q", d.Class)
	}
}

func TestParseLspci_SlotInID(t *testing.T) {
	d := parseLspciBlocks(lspciFixture)[0]
	if d.ID != "00:02.0" {
		t.Errorf("ID: want 00:02.0, got %q", d.ID)
	}
	if d.Bus != "pci" {
		t.Errorf("bus: want pci, got %q", d.Bus)
	}
}

func TestParseLspci_EmptyBlock(t *testing.T) {
	// Blocks with no Device field should not appear in results.
	raw := `Slot:	00:00.0
Vendor:	Intel Corporation

`
	devices := parseLspciBlocks(raw)
	if len(devices) != 0 {
		t.Errorf("block with no Device field should be skipped, got %d devices", len(devices))
	}
}

// ---------------------------------------------------------------------------
// lsusb fixture tests
// ---------------------------------------------------------------------------

const lsusbFixture = `Bus 001 Device 001: ID 1d6b:0002 Linux Foundation 2.0 root hub
Bus 002 Device 001: ID 1d6b:0003 Linux Foundation 3.0 root hub
Bus 001 Device 002: ID 8087:0026 Intel Corp. AX201 Bluetooth
`

func TestParseLsusb_Count(t *testing.T) {
	devices := parseLsusbLines(lsusbFixture)
	if len(devices) != 3 {
		t.Fatalf("expected 3 devices, got %d", len(devices))
	}
}

func TestParseLsusb_IDAndName(t *testing.T) {
	devices := parseLsusbLines(lsusbFixture)
	if devices[0].ID != "1d6b:0002" {
		t.Errorf("ID: want 1d6b:0002, got %q", devices[0].ID)
	}
	if devices[0].Name != "Linux Foundation 2.0 root hub" {
		t.Errorf("name: want 'Linux Foundation 2.0 root hub', got %q", devices[0].Name)
	}
}

func TestParseLsusb_BusAndClass(t *testing.T) {
	for _, d := range parseLsusbLines(lsusbFixture) {
		if d.Bus != "usb" {
			t.Errorf("bus: want usb, got %q", d.Bus)
		}
		if d.Class != "usb" {
			t.Errorf("class: want usb, got %q", d.Class)
		}
		if d.DriverState != "active" {
			t.Errorf("driver_state: want active, got %q", d.DriverState)
		}
	}
}

func TestParseLsusb_EmptyInput(t *testing.T) {
	devices := parseLsusbLines("")
	if len(devices) != 0 {
		t.Errorf("expected 0 devices for empty input, got %d", len(devices))
	}
}

// ---------------------------------------------------------------------------
// /proc/modules fixture tests
// ---------------------------------------------------------------------------

const procModulesFixture = `i915 2482176 4 - Live 0xffffffffc0800000
snd_hda_intel 57344 2 - Live 0xffffffffc07a0000
rtw89_8852be 32768 0 - Live 0xffffffffc0790000
`

func TestParseProcModules_Count(t *testing.T) {
	modules := parseProcModules(procModulesFixture)
	if len(modules) != 3 {
		t.Fatalf("expected 3 modules, got %d", len(modules))
	}
}

func TestParseProcModules_Fields(t *testing.T) {
	modules := parseProcModules(procModulesFixture)
	if modules[0].Name != "i915" {
		t.Errorf("name: want i915, got %q", modules[0].Name)
	}
	if modules[0].Size != "2482176" {
		t.Errorf("size: want 2482176, got %q", modules[0].Size)
	}
	if modules[0].UsedBy != "4" {
		t.Errorf("used_by: want 4, got %q", modules[0].UsedBy)
	}
}

func TestParseProcModules_LoadedTrue(t *testing.T) {
	for _, m := range parseProcModules(procModulesFixture) {
		if !m.Loaded {
			t.Errorf("module %q: Loaded should be true", m.Name)
		}
	}
}

func TestParseProcModules_TooFewFields(t *testing.T) {
	// Lines with fewer than 3 fields are skipped.
	raw := "mod1 1024\nmod2 2048 0 - Live 0x0\n"
	modules := parseProcModules(raw)
	if len(modules) != 1 {
		t.Errorf("expected 1 module (2-field line skipped), got %d", len(modules))
	}
}

// ---------------------------------------------------------------------------
// SEC-DRV-01: Module name validation — flag injection prevention
// ---------------------------------------------------------------------------

func TestValidateModuleName_AcceptsValid(t *testing.T) {
	valid := []string{
		"i915",
		"rtw89-8852be",
		"nvidia",
		"snd-hda-intel",
		"usbhid",
		"v4l2loopback",
	}
	for _, name := range valid {
		if err := validateModuleName(name); err != nil {
			t.Errorf("validateModuleName(%q) returned unexpected error: %v", name, err)
		}
	}
}

func TestValidateModuleName_RejectsFlagInjection(t *testing.T) {
	// A module name starting with '-' would be interpreted as a modprobe flag.
	dangerous := []string{
		"--remove-all",
		"-r",
		"--dry-run",
		"-v",
	}
	for _, name := range dangerous {
		if err := validateModuleName(name); err == nil {
			t.Errorf("validateModuleName(%q) should return error (flag injection)", name)
		}
	}
}

func TestValidateModuleName_RejectsPathTraversal(t *testing.T) {
	// modprobe can load modules by path — traversal attempts must be blocked.
	dangerous := []string{
		"../../etc/evil",
		"/path/to/evil.ko",
		"evil module",
	}
	for _, name := range dangerous {
		if err := validateModuleName(name); err == nil {
			t.Errorf("validateModuleName(%q) should return error (path/meta char)", name)
		}
	}
}

func TestLoadModule_ValidatesName(t *testing.T) {
	ctx := context.Background()
	if err := LoadModule(ctx, "--remove-all"); err == nil {
		t.Error("LoadModule should reject --remove-all (flag injection)")
	}
	if err := LoadModule(ctx, "../../etc/evil"); err == nil {
		t.Error("LoadModule should reject path traversal")
	}
}

func TestUnloadModule_ValidatesName(t *testing.T) {
	ctx := context.Background()
	if err := UnloadModule(ctx, "-v"); err == nil {
		t.Error("UnloadModule should reject -v (flag injection)")
	}
}
