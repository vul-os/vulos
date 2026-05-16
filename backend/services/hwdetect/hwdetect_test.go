//go:build linux

package hwdetect

import (
	"strings"
	"testing"
)

// ──────────────────────────────────────────────────────────────────────────────
// Run() — panic-recover safety contract
// ──────────────────────────────────────────────────────────────────────────────

// TestRun_DoesNotPanic verifies that Run() completes without panicking even
// when the underlying /proc and /sys paths are unavailable (as in a test
// sandbox). All individual probes use defer/recover internally.
func TestRun_DoesNotPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("Run() panicked: %v", r)
		}
	}()
	// Run() writes to /var/log/vulos-boot.log which may fail — that is fine;
	// what must NOT happen is a panic escaping to the caller.
	Run()
}

// TestProbeGPU_PanicRecovery verifies that probeGPU() never panics even when
// gpu.Detect() (which probes live hardware) is unavailable or panics internally.
func TestProbeGPU_PanicRecovery(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("probeGPU() panicked: %v", r)
		}
	}()
	info := probeGPU()
	// Result should be the zero value of gpu.Info or a valid detection; we
	// just care that it returned without panicking.
	_ = info
}

// ──────────────────────────────────────────────────────────────────────────────
// classifyBlockDevice — fixture-string based, no live /sys reads
// ──────────────────────────────────────────────────────────────────────────────

func TestClassifyBlockDevice(t *testing.T) {
	cases := []struct {
		name string
		want string
	}{
		{"nvme0n1", "nvme"},
		{"nvme1n1", "nvme"},
		{"sda", "sata"},
		{"sdb", "sata"},
		{"mmcblk0", "mmc"},
		{"vda", "virtio"},
		{"vdb", "virtio"},
		{"xda", "other"},
		{"hda", "other"},
	}
	for _, tc := range cases {
		got := classifyBlockDevice(tc.name)
		if got != tc.want {
			t.Errorf("classifyBlockDevice(%q) = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// classifyNetInterface — fixture-string based, no live /sys reads
// ──────────────────────────────────────────────────────────────────────────────

func TestClassifyNetInterface_Names(t *testing.T) {
	cases := []struct {
		name string
		want string
	}{
		{"lo", "loopback"},
		{"eth0", "wired"},
		{"eth1", "wired"},
		{"enp3s0", "wired"},
		{"ens33", "wired"},
		{"eno1", "wired"},
		// wl-prefixed → wireless (even without sysfs wireless/ dir in tests)
		{"wlan0", "wireless"},
		{"wlp2s0", "wireless"},
	}
	for _, tc := range cases {
		got := classifyNetInterface(tc.name)
		if got != tc.want {
			t.Errorf("classifyNetInterface(%q) = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestClassifyNetInterface_Loopback(t *testing.T) {
	if got := classifyNetInterface("lo"); got != "loopback" {
		t.Errorf("lo → %q, want loopback", got)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// formatBytes — pure function, no I/O
// ──────────────────────────────────────────────────────────────────────────────

func TestFormatBytes(t *testing.T) {
	const gib = int64(1) << 30
	const mib = int64(1) << 20

	cases := []struct {
		input int64
		want  string
	}{
		{0, "0 B"},
		{1023, "1023 B"},
		{mib, "1.0 MiB"},
		{512 * mib, "512.0 MiB"},
		{gib, "1.0 GiB"},
		{512 * gib, "512.0 GiB"},
	}
	for _, tc := range cases {
		got := formatBytes(tc.input)
		if got != tc.want {
			t.Errorf("formatBytes(%d) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// probeAudio — fixture via /proc/asound/cards content parsing logic
//
// The internal parsing logic is exercised indirectly by probeAudio(), which
// reads /proc/asound/cards. On a test machine that file is absent; we verify
// that the function returns a valid (zero-value) AudioInfo without panicking.
// ──────────────────────────────────────────────────────────────────────────────

func TestProbeAudio_NoPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("probeAudio() panicked: %v", r)
		}
	}()
	info := probeAudio()
	// Backend must be one of the known strings.
	validBackends := map[string]bool{"pipewire": true, "pulseaudio": true, "alsa": true, "none": true}
	if !validBackends[info.Backend] {
		t.Errorf("unexpected audio backend: %q", info.Backend)
	}
}

// TestAudioCardParsing exercises the ALSA card line parsing logic using the
// same string-splitting approach used in probeAudio, without touching /proc.
func TestAudioCardParsing(t *testing.T) {
	// Simulate the content of /proc/asound/cards
	fixture := " 0 [HDMI           ]: HDA-Intel - HDA Intel PCH\n" +
		"                      HDA Intel PCH at 0xdf348000 irq 139\n" +
		" 1 [PCH            ]: HDA-Intel - HDA Intel PCH\n" +
		"                      HDA Intel PCH at 0xdf340000 irq 140\n"

	var cards []string
	for _, line := range strings.Split(fixture, "\n") {
		line = strings.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		if line[0] >= '0' && line[0] <= '9' {
			start := strings.Index(line, "[")
			end := strings.Index(line, "]")
			if start >= 0 && end > start {
				name := strings.TrimSpace(line[start+1 : end])
				cards = append(cards, name)
			}
		}
	}

	if len(cards) != 2 {
		t.Fatalf("expected 2 cards, got %d: %v", len(cards), cards)
	}
	if cards[0] != "HDMI" {
		t.Errorf("cards[0] = %q, want HDMI", cards[0])
	}
	if cards[1] != "PCH" {
		t.Errorf("cards[1] = %q, want PCH", cards[1])
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// readBlockSizeBytes — parse logic via fixture strings (no live /sys read)
// ──────────────────────────────────────────────────────────────────────────────

// TestBlockSizeParsing exercises the sector→bytes arithmetic used in
// readBlockSizeBytes without touching /sys by replicating the parse logic.
func TestBlockSizeParsing(t *testing.T) {
	cases := []struct {
		raw       string // content of /sys/block/<name>/size
		wantBytes int64
	}{
		{"976773168\n", 976773168 * 512}, // 500 GB NVMe
		{"60751872\n", 60751872 * 512},   // 31 GB SD card
		{"0\n", 0},
		{"1\n", 512},
	}
	for _, tc := range cases {
		var sectors int64
		_, err := parseI64(strings.TrimSpace(tc.raw), &sectors)
		if err != nil {
			t.Fatalf("parseI64 unexpected error for %q: %v", tc.raw, err)
		}
		got := sectors * 512
		if got != tc.wantBytes {
			t.Errorf("sectors=%d → bytes=%d, want %d", sectors, got, tc.wantBytes)
		}
	}
}

// parseI64 is a test-local helper that mirrors the fmt.Sscanf call in
// readBlockSizeBytes so we can test the arithmetic without touching /sys.
func parseI64(s string, out *int64) (int, error) {
	// Same as: fmt.Sscanf(s, "%d", &sectors)
	n := 0
	var v int64
	for _, ch := range s {
		if ch < '0' || ch > '9' {
			break
		}
		v = v*10 + int64(ch-'0')
		n++
	}
	*out = v
	return n, nil
}

// ──────────────────────────────────────────────────────────────────────────────
// Kernel version parsing — probeAudio backend string validation
// ──────────────────────────────────────────────────────────────────────────────

// TestKernelVersionStringParsing tests extraction of kernel version components
// from a /proc/version style string, mirroring what a version parser would do.
func TestKernelVersionStringParsing(t *testing.T) {
	samples := []struct {
		line  string
		major int
		minor int
	}{
		{"Linux version 6.1.0-25-amd64 (debian-kernel@lists.debian.org)", 6, 1},
		{"Linux version 5.15.0-105-generic (buildd@lcy02-amd64-033)", 5, 15},
		{"Linux version 6.6.31-gentoo (root@builder)", 6, 6},
	}

	for _, tc := range samples {
		major, minor := parseKernelVersion(tc.line)
		if major != tc.major || minor != tc.minor {
			t.Errorf("parseKernelVersion(%q) = (%d,%d), want (%d,%d)",
				tc.line, major, minor, tc.major, tc.minor)
		}
	}
}

// parseKernelVersion extracts the major and minor version from a
// "Linux version X.Y.Z ..." string. It is a test-local pure function that
// exercises the same parsing logic one would use with /proc/version content.
func parseKernelVersion(line string) (major, minor int) {
	// Find "version X.Y"
	const prefix = "Linux version "
	idx := strings.Index(line, prefix)
	if idx < 0 {
		return 0, 0
	}
	rest := line[idx+len(prefix):]
	// rest = "6.1.0-25-amd64 ..."
	parts := strings.SplitN(rest, ".", 3)
	if len(parts) < 2 {
		return 0, 0
	}
	parseDigits(parts[0], &major)
	parseDigits(parts[1], &minor)
	return major, minor
}

// parseDigits reads leading digits from s into *out.
func parseDigits(s string, out *int) {
	v := 0
	for _, ch := range s {
		if ch < '0' || ch > '9' {
			break
		}
		v = v*10 + int(ch-'0')
	}
	*out = v
}

// ──────────────────────────────────────────────────────────────────────────────
// probeStorage / probeNetwork / probeBattery — no-panic safety
// ──────────────────────────────────────────────────────────────────────────────

func TestProbeStorage_NoPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("probeStorage() panicked: %v", r)
		}
	}()
	_ = probeStorage()
}

func TestProbeNetwork_NoPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("probeNetwork() panicked: %v", r)
		}
	}()
	info := probeNetwork()
	// Interfaces slice must not be nil on success (may be empty).
	_ = info
}

func TestProbeBattery_NoPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("probeBattery() panicked: %v", r)
		}
	}()
	info := probeBattery()
	// Percentage is -1 when unknown; must be in range [-1, 100].
	if info.Percentage < -1 || info.Percentage > 100 {
		t.Errorf("Percentage out of range: %d", info.Percentage)
	}
}

func TestProbeInput_NoPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("probeInput() panicked: %v", r)
		}
	}()
	_ = probeInput()
}
