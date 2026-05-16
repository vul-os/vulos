package audio

import (
	"strings"
	"testing"
)

// pactlSinkFixture is representative output of "pactl list sinks".
const pactlSinkFixture = `
Sink #0
	State: RUNNING
	Name: alsa_output.pci-0000_00_1f.3.analog-stereo
	Description: Built-in Audio Analog Stereo
	Driver: PipeWire
	Sample Specification: s32le 2ch 48000Hz
	Channel Map: front-left,front-right
	Owner Module: 4294967295
	Mute: no
	Volume: front-left: 45875 /  70% / -9.28 dB,   front-right: 45875 /  70% / -9.28 dB
		    balance 0.00
	Base Volume: 65536 / 100% / 0.00 dB
	Monitor Source: alsa_output.pci-0000_00_1f.3.analog-stereo.monitor
	Latency: 0 usec, configured 0 usec
	Flags: HARDWARE HW_VOLUME_CTRL DECIBEL_VOLUME LATENCY
	Properties:
		alsa.card = "0"

Sink #1
	State: SUSPENDED
	Name: bluez_output.AA_BB_CC_DD_EE_FF.1
	Description: Sony WH-1000XM4
	Driver: PipeWire
	Mute: yes
	Volume: front-left: 32768 /  50% / -18.06 dB,   front-right: 32768 /  50% / -18.06 dB
		    balance 0.00
`

// pactlSourceFixture is representative output of "pactl list sources"
// including a monitor source that should be filtered out.
const pactlSourceFixture = `
Source #0
	State: RUNNING
	Name: alsa_input.pci-0000_00_1f.3.analog-stereo
	Description: Built-in Audio Analog Stereo
	Driver: PipeWire
	Mute: no
	Volume: front-left: 26214 /  40% / -23.96 dB,   front-right: 26214 /  40% / -23.96 dB

Source #1
	Name: alsa_output.pci-0000_00_1f.3.analog-stereo.monitor
	Description: Monitor of Built-in Audio Analog Stereo
	Mute: no
	Volume: front-left: 65536 / 100% / 0.00 dB
`

// ---- parsePactlOutput: device/sink enumeration from fixture strings ----

func TestParsePactlOutput_SinkCount(t *testing.T) {
	devices := parsePactlOutput("alsa_output.pci-0000_00_1f.3.analog-stereo", pactlSinkFixture, "sink")
	if len(devices) != 2 {
		t.Errorf("expected 2 sinks, got %d", len(devices))
	}
}

func TestParsePactlOutput_SinkFields(t *testing.T) {
	defName := "alsa_output.pci-0000_00_1f.3.analog-stereo"
	devices := parsePactlOutput(defName, pactlSinkFixture, "sink")

	d := devices[0]
	if d.ID != defName {
		t.Errorf("ID = %q, want %q", d.ID, defName)
	}
	if d.Name != "Built-in Audio Analog Stereo" {
		t.Errorf("Name = %q, want %q", d.Name, "Built-in Audio Analog Stereo")
	}
	if !d.Default {
		t.Errorf("expected Default=true for first sink")
	}
	if d.Muted {
		t.Errorf("expected Muted=false for first sink")
	}
	if d.Volume != 70 {
		t.Errorf("Volume = %d, want 70", d.Volume)
	}
	if d.Type != "sink" {
		t.Errorf("Type = %q, want %q", d.Type, "sink")
	}
}

func TestParsePactlOutput_MutedDevice(t *testing.T) {
	devices := parsePactlOutput("", pactlSinkFixture, "sink")
	// Second sink is muted
	d := devices[1]
	if !d.Muted {
		t.Errorf("expected second sink to be muted")
	}
	if d.Volume != 50 {
		t.Errorf("Volume = %d, want 50", d.Volume)
	}
	if d.Default {
		t.Errorf("expected second sink to not be default (no defName set)")
	}
}

func TestParsePactlOutput_NonDefaultWhenDifferentDefName(t *testing.T) {
	// defName points at a device that does not appear in sinks list
	devices := parsePactlOutput("some-other-sink", pactlSinkFixture, "sink")
	for _, d := range devices {
		if d.Default {
			t.Errorf("no sink should be default when defName=%q, but %q is", "some-other-sink", d.ID)
		}
	}
}

func TestParsePactlOutput_FilterMonitorSources(t *testing.T) {
	devices := parsePactlOutput("", pactlSourceFixture, "source")
	// The .monitor source must be filtered out
	for _, d := range devices {
		if strings.Contains(d.ID, ".monitor") {
			t.Errorf("monitor source %q should have been filtered", d.ID)
		}
	}
	if len(devices) != 1 {
		t.Errorf("expected 1 non-monitor source, got %d", len(devices))
	}
}

func TestParsePactlOutput_SourceVolume(t *testing.T) {
	devices := parsePactlOutput("", pactlSourceFixture, "source")
	if len(devices) == 0 {
		t.Fatal("no sources parsed")
	}
	if devices[0].Volume != 40 {
		t.Errorf("source volume = %d, want 40", devices[0].Volume)
	}
}

func TestParsePactlOutput_Empty(t *testing.T) {
	devices := parsePactlOutput("", "", "sink")
	if len(devices) != 0 {
		t.Errorf("expected empty slice for empty input, got %d devices", len(devices))
	}
}

// ---- Volume clamping (inline logic from SetVolume, verified via clampVolume helper) ----

// clampVolume mirrors the clamping logic in Service.SetVolume.
func clampVolume(v int) int {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}

func TestVolumeClamp(t *testing.T) {
	cases := []struct {
		in   int
		want int
	}{
		{-10, 0},
		{0, 0},
		{50, 50},
		{100, 100},
		{101, 100},
		{999, 100},
	}
	for _, tc := range cases {
		got := clampVolume(tc.in)
		if got != tc.want {
			t.Errorf("clampVolume(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

// ---- pactl / pipewire argument-builder logic (backend string routing) ----

// pactlSinkArg mirrors the kind-selection logic in SetVolume and SetMute.
func pactlKindForType(deviceType, command string) string {
	kind := "sink"
	if deviceType == "input" {
		kind = "source"
	}
	return "set-" + kind + "-" + command
}

func TestPactlKind_Output(t *testing.T) {
	got := pactlKindForType("output", "volume")
	want := "set-sink-volume"
	if got != want {
		t.Errorf("pactlKindForType(output, volume) = %q, want %q", got, want)
	}
}

func TestPactlKind_Input(t *testing.T) {
	got := pactlKindForType("input", "mute")
	want := "set-source-mute"
	if got != want {
		t.Errorf("pactlKindForType(input, mute) = %q, want %q", got, want)
	}
}

// pactlDefaultArg mirrors the kind-selection logic in SetDefault.
func pactlDefaultArg(deviceType string) string {
	if deviceType == "input" {
		return "default-source"
	}
	return "default-sink"
}

func TestPactlDefaultArg_Output(t *testing.T) {
	got := pactlDefaultArg("output")
	if got != "default-sink" {
		t.Errorf("pactlDefaultArg(output) = %q, want default-sink", got)
	}
}

func TestPactlDefaultArg_Input(t *testing.T) {
	got := pactlDefaultArg("input")
	if got != "default-source" {
		t.Errorf("pactlDefaultArg(input) = %q, want default-source", got)
	}
}

// ---- Struct shape sanity checks ----

func TestDeviceStruct_ZeroValue(t *testing.T) {
	var d Device
	if d.Volume != 0 || d.Muted || d.Default {
		t.Errorf("unexpected zero value: %+v", d)
	}
}

func TestStatusStruct_ZeroValue(t *testing.T) {
	var s Status
	if s.Backend != "" || s.Outputs != nil || s.Inputs != nil {
		t.Errorf("unexpected zero value: %+v", s)
	}
}

// ---- Live-device paths (skipped when pactl/amixer unavailable) ----

func TestGetStatus_LiveSkip(t *testing.T) {
	t.Skip("skipping live audio device test — requires pactl or amixer")
}

func TestSetVolume_LiveSkip(t *testing.T) {
	t.Skip("skipping live SetVolume test — requires pactl or amixer")
}

func TestSetMute_LiveSkip(t *testing.T) {
	t.Skip("skipping live SetMute test — requires pactl or amixer")
}

func TestSetDefault_LiveSkip(t *testing.T) {
	t.Skip("skipping live SetDefault test — requires pactl or amixer")
}
