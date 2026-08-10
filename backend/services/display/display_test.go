package display

import (
	"os"
	"os/exec"
	"testing"
)

// ---------------------------------------------------------------------------
// Fixtures — wlr-randr output
// ---------------------------------------------------------------------------

const fixtureWlr = `HDMI-A-1 (HDMI-A-1) 1920x1080 (0x47)
  Enabled: yes
  Modes:
    1920x1080 px, 60.000000 Hz (preferred, current)
    1280x720 px, 60.000000 Hz
eDP-1 (eDP-1) 2560x1600 (0x1)
  Enabled: yes
  Modes:
    2560x1600 px, 60.002441 Hz (preferred, current)
    1920x1200 px, 59.950199 Hz
`

// fixtureWlrHeadless is VERBATIM output from wlr-randr 0.4.1 against a live
// headless labwc session (arm64, 2026-08-10). The other fixtures in this file
// were written to match the parser; this one was captured from the tool, and
// the parser did not handle it: the header carries no parenthesised connector
// and the mode line carries no refresh rate, so the old checks
// (`strings.Contains(line, "(")` and `"px," && "Hz"`) skipped the output and
// all of its modes.
const fixtureWlrHeadless = `HEADLESS-1 "Headless output 1"
  Make: (null)
  Model: (null)
  Serial: (null)
  Enabled: yes
  Modes:
    1280x720 px (current)
  Position: 0,0
  Transform: normal
  Scale: 1.000000
`

const fixtureWlrDisabled = `HDMI-A-2 (HDMI-A-2) 0x0 (0x0)
  Enabled: no
  Modes:
`

// ---------------------------------------------------------------------------
// Fixtures — xrandr output
// ---------------------------------------------------------------------------

const fixtureXrandr = `Screen 0: minimum 8 x 8, current 1920 x 1080, maximum 32767 x 32767
HDMI-1 connected primary 1920x1080+0+0 (normal left inverted right x axis y axis) 527mm x 296mm
   1920x1080     60.00*+  50.00    59.94
   1280x720      60.00    50.00    59.94
   1024x768      75.08    70.07    60.00
VGA-1 disconnected (normal left inverted right x axis y axis)
   800x600       75.00    72.19    60.32    56.25
eDP-1 connected 2560x1600+1920+0 (normal left inverted right x axis y axis) 344mm x 215mm
   2560x1600     59.99*+
   1920x1200     59.95
`

const fixtureXrandrOnly = `HDMI-1 connected 1024x768+0+0 (normal left inverted right x axis y axis) 200mm x 150mm
   1024x768      75.08*   60.00
`

// ---------------------------------------------------------------------------
// parseWlrOutput
// ---------------------------------------------------------------------------

func TestParseWlrOutput_Count(t *testing.T) {
	outputs := parseWlrOutput(fixtureWlr)
	if len(outputs) != 2 {
		t.Errorf("expected 2 outputs, got %d", len(outputs))
	}
}

func TestParseWlrOutput_Names(t *testing.T) {
	outputs := parseWlrOutput(fixtureWlr)
	if outputs[0].Name != "HDMI-A-1" {
		t.Errorf("first output name: got %q", outputs[0].Name)
	}
	if outputs[1].Name != "eDP-1" {
		t.Errorf("second output name: got %q", outputs[1].Name)
	}
}

func TestParseWlrOutput_Enabled(t *testing.T) {
	outputs := parseWlrOutput(fixtureWlr)
	for _, o := range outputs {
		if !o.Enabled {
			t.Errorf("output %q should be enabled", o.Name)
		}
	}
}

func TestParseWlrOutput_Resolution(t *testing.T) {
	outputs := parseWlrOutput(fixtureWlr)
	if outputs[0].Resolution != "1920x1080" {
		t.Errorf("HDMI-A-1 resolution: got %q", outputs[0].Resolution)
	}
	if outputs[1].Resolution != "2560x1600" {
		t.Errorf("eDP-1 resolution: got %q", outputs[1].Resolution)
	}
}

func TestParseWlrOutput_Modes(t *testing.T) {
	outputs := parseWlrOutput(fixtureWlr)
	if len(outputs[0].Modes) != 2 {
		t.Errorf("HDMI-A-1 modes: got %d, want 2", len(outputs[0].Modes))
	}
}

func TestParseWlrOutput_Refresh(t *testing.T) {
	outputs := parseWlrOutput(fixtureWlr)
	if outputs[0].Refresh != "60.000000" {
		t.Errorf("HDMI-A-1 refresh: got %q", outputs[0].Refresh)
	}
}

func TestParseWlrOutput_Disabled(t *testing.T) {
	outputs := parseWlrOutput(fixtureWlrDisabled)
	if len(outputs) != 1 {
		t.Fatalf("expected 1 output, got %d", len(outputs))
	}
	if outputs[0].Enabled {
		t.Error("output should be disabled")
	}
}

// The measured headless form must parse: one output, enabled, with its mode.
func TestParseWlrOutput_HeadlessNoParensNoRefresh(t *testing.T) {
	outputs := parseWlrOutput(fixtureWlrHeadless)
	if len(outputs) != 1 {
		t.Fatalf("expected 1 output from real wlr-randr output, got %d", len(outputs))
	}
	o := outputs[0]
	if o.Name != "HEADLESS-1" {
		t.Errorf("Name = %q, want %q", o.Name, "HEADLESS-1")
	}
	if !o.Enabled {
		t.Error("Enabled = false, want true")
	}
	if o.Resolution != "1280x720" {
		t.Errorf("Resolution = %q, want %q", o.Resolution, "1280x720")
	}
	if len(o.Modes) != 1 || o.Modes[0] != "1280x720" {
		t.Errorf("Modes = %v, want [1280x720]", o.Modes)
	}
	// No refresh rate is reported for this output; the parser must leave the
	// field empty rather than picking up "(current)" as a rate.
	if o.Refresh != "" {
		t.Errorf("Refresh = %q, want empty (the tool reported none)", o.Refresh)
	}
}

// The Make/Model/Serial detail lines must not be mistaken for outputs.
func TestParseWlrOutput_DetailLinesAreNotOutputs(t *testing.T) {
	for _, o := range parseWlrOutput(fixtureWlrHeadless) {
		if o.Name == "Make:" || o.Name == "Model:" || o.Name == "Serial:" {
			t.Fatalf("detail line parsed as an output: %q", o.Name)
		}
	}
}

func TestParseWlrOutput_Empty(t *testing.T) {
	outputs := parseWlrOutput("")
	if len(outputs) != 0 {
		t.Errorf("expected 0 outputs for empty input, got %d", len(outputs))
	}
}

func TestParseWlrOutput_Connected(t *testing.T) {
	outputs := parseWlrOutput(fixtureWlr)
	for _, o := range outputs {
		if !o.Connected {
			t.Errorf("wlr outputs should always be Connected=true, got false for %q", o.Name)
		}
	}
}

// ---------------------------------------------------------------------------
// parseXrandrOutput
// ---------------------------------------------------------------------------

func TestParseXrandrOutput_Count(t *testing.T) {
	// 3 output blocks: HDMI-1 connected, VGA-1 disconnected, eDP-1 connected
	outputs := parseXrandrOutput(fixtureXrandr)
	if len(outputs) != 3 {
		t.Errorf("expected 3 outputs, got %d", len(outputs))
	}
}

func TestParseXrandrOutput_Names(t *testing.T) {
	outputs := parseXrandrOutput(fixtureXrandr)
	names := []string{"HDMI-1", "VGA-1", "eDP-1"}
	for i, want := range names {
		if outputs[i].Name != want {
			t.Errorf("outputs[%d].Name: got %q want %q", i, outputs[i].Name, want)
		}
	}
}

func TestParseXrandrOutput_Connected(t *testing.T) {
	outputs := parseXrandrOutput(fixtureXrandr)
	if !outputs[0].Connected {
		t.Error("HDMI-1 should be connected")
	}
	if outputs[1].Connected {
		t.Error("VGA-1 should be disconnected")
	}
	if !outputs[2].Connected {
		t.Error("eDP-1 should be connected")
	}
}

func TestParseXrandrOutput_Primary(t *testing.T) {
	outputs := parseXrandrOutput(fixtureXrandr)
	if !outputs[0].Primary {
		t.Error("HDMI-1 should be primary")
	}
	if outputs[1].Primary || outputs[2].Primary {
		t.Error("VGA-1 and eDP-1 should not be primary")
	}
}

func TestParseXrandrOutput_Resolution(t *testing.T) {
	outputs := parseXrandrOutput(fixtureXrandr)
	if outputs[0].Resolution != "1920x1080" {
		t.Errorf("HDMI-1 resolution: got %q", outputs[0].Resolution)
	}
	if outputs[2].Resolution != "2560x1600" {
		t.Errorf("eDP-1 resolution: got %q", outputs[2].Resolution)
	}
}

func TestParseXrandrOutput_Enabled(t *testing.T) {
	outputs := parseXrandrOutput(fixtureXrandr)
	// Active outputs have a "*" marker in mode line.
	if !outputs[0].Enabled {
		t.Error("HDMI-1 should be enabled (has active mode)")
	}
	if !outputs[2].Enabled {
		t.Error("eDP-1 should be enabled")
	}
}

func TestParseXrandrOutput_Modes(t *testing.T) {
	outputs := parseXrandrOutput(fixtureXrandr)
	// HDMI-1 has 3 mode lines (1920x1080, 1280x720, 1024x768)
	if len(outputs[0].Modes) != 3 {
		t.Errorf("HDMI-1 modes count: got %d want 3", len(outputs[0].Modes))
	}
}

func TestParseXrandrOutput_Refresh(t *testing.T) {
	outputs := parseXrandrOutput(fixtureXrandrOnly)
	if len(outputs) == 0 {
		t.Fatal("expected at least one output")
	}
	if outputs[0].Refresh != "75.08" {
		t.Errorf("refresh: got %q want %q", outputs[0].Refresh, "75.08")
	}
}

func TestParseXrandrOutput_Empty(t *testing.T) {
	outputs := parseXrandrOutput("")
	if len(outputs) != 0 {
		t.Errorf("expected 0 outputs for empty input, got %d", len(outputs))
	}
}

// ---------------------------------------------------------------------------
// clampBrightness
// ---------------------------------------------------------------------------

func TestClampBrightness_InRange(t *testing.T) {
	cases := []struct{ in, want int }{
		{0, 0},
		{50, 50},
		{100, 100},
		{75, 75},
	}
	for _, c := range cases {
		got := clampBrightness(c.in)
		if got != c.want {
			t.Errorf("clampBrightness(%d)=%d, want %d", c.in, got, c.want)
		}
	}
}

func TestClampBrightness_BelowZero(t *testing.T) {
	cases := []int{-1, -50, -100, -9999}
	for _, in := range cases {
		if got := clampBrightness(in); got != 0 {
			t.Errorf("clampBrightness(%d)=%d, want 0", in, got)
		}
	}
}

func TestClampBrightness_AboveHundred(t *testing.T) {
	cases := []int{101, 150, 200, 9999}
	for _, in := range cases {
		if got := clampBrightness(in); got != 100 {
			t.Errorf("clampBrightness(%d)=%d, want 100", in, got)
		}
	}
}

// ---------------------------------------------------------------------------
// Live-hardware tests (skipped unless display tools present)
// ---------------------------------------------------------------------------

func TestGetStatus_WlrSkip(t *testing.T) {
	if os.Getenv("WAYLAND_DISPLAY") == "" {
		t.Skip("no WAYLAND_DISPLAY; skipping live wlr-randr test")
	}
	if _, err := exec.LookPath("wlr-randr"); err != nil {
		t.Skip("wlr-randr not in PATH")
	}
	svc := New()
	_ = svc.GetStatus(t.Context())
}

func TestGetStatus_XrandrSkip(t *testing.T) {
	if os.Getenv("DISPLAY") == "" {
		t.Skip("no DISPLAY; skipping live xrandr test")
	}
	if _, err := exec.LookPath("xrandr"); err != nil {
		t.Skip("xrandr not in PATH")
	}
	svc := &Service{compositor: "x11"}
	_ = svc.GetStatus(t.Context())
}
