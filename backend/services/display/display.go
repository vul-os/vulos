package display

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// Output is a display/monitor.
type Output struct {
	Name       string   `json:"name"` // e.g., "HDMI-1", "eDP-1"
	Connected  bool     `json:"connected"`
	Enabled    bool     `json:"enabled"`
	Primary    bool     `json:"primary"`
	Resolution string   `json:"resolution"` // e.g., "1920x1080"
	Refresh    string   `json:"refresh"`    // e.g., "60.00"
	Position   string   `json:"position"`   // e.g., "0x0"
	Modes      []string `json:"modes"`      // available resolutions
}

// Brightness holds backlight info.
type Brightness struct {
	Current int    `json:"current"` // 0-100
	Max     int    `json:"max"`
	Device  string `json:"device"`
}

// Status is the full display state.
type Status struct {
	Outputs    []Output   `json:"outputs"`
	Brightness Brightness `json:"brightness"`
	Compositor string     `json:"compositor"` // "wlroots", "cage", "x11", "unknown"
}

// Service manages displays via wlr-randr (Wayland) or xrandr (X11).
type Service struct {
	mu         sync.Mutex
	compositor string
}

func New() *Service {
	return &Service{compositor: detectCompositor()}
}

// GetStatus returns all outputs and brightness.
func (s *Service) GetStatus(ctx context.Context) Status {
	st := Status{Compositor: s.compositor}
	st.Outputs = s.listOutputs(ctx)
	st.Brightness = s.getBrightness()
	return st
}

// SetBrightness sets screen brightness (0-100).
func (s *Service) SetBrightness(ctx context.Context, percent int) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if percent < 0 {
		percent = 0
	}
	if percent > 100 {
		percent = 100
	}

	// Try brightnessctl first
	if _, err := exec.LookPath("brightnessctl"); err == nil {
		return run(ctx, "brightnessctl", "set", fmt.Sprintf("%d%%", percent))
	}

	// Direct sysfs
	matches, _ := filepath.Glob("/sys/class/backlight/*/max_brightness")
	for _, maxPath := range matches {
		data, err := os.ReadFile(maxPath)
		if err != nil {
			continue
		}
		maxVal, _ := strconv.Atoi(strings.TrimSpace(string(data)))
		if maxVal == 0 {
			continue
		}
		target := maxVal * percent / 100
		brightPath := filepath.Join(filepath.Dir(maxPath), "brightness")
		os.WriteFile(brightPath, []byte(strconv.Itoa(target)), 0644)
		log.Printf("[display] brightness set to %d%%", percent)
		return nil
	}

	return fmt.Errorf("no backlight device found")
}

// SetResolution changes the resolution of an output.
func (s *Service) SetResolution(ctx context.Context, outputName, resolution string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	switch s.compositor {
	case "wlroots", "cage":
		return run(ctx, "wlr-randr", "--output", outputName, "--mode", resolution)
	case "x11":
		return run(ctx, "xrandr", "--output", outputName, "--mode", resolution)
	}
	return fmt.Errorf("unsupported compositor: %s", s.compositor)
}

// EnableOutput turns a display on or off.
func (s *Service) EnableOutput(ctx context.Context, outputName string, enable bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	switch s.compositor {
	case "wlroots", "cage":
		flag := "--on"
		if !enable {
			flag = "--off"
		}
		return run(ctx, "wlr-randr", "--output", outputName, flag)
	case "x11":
		flag := "--auto"
		if !enable {
			flag = "--off"
		}
		return run(ctx, "xrandr", "--output", outputName, flag)
	}
	return fmt.Errorf("unsupported compositor: %s", s.compositor)
}

func (s *Service) listOutputs(ctx context.Context) []Output {
	switch s.compositor {
	case "wlroots", "cage":
		return s.listWlr(ctx)
	case "x11":
		return s.listXrandr(ctx)
	}
	return nil
}

func (s *Service) listWlr(ctx context.Context) []Output {
	out, err := output(ctx, "wlr-randr")
	if err != nil {
		return nil
	}

	var outputs []Output
	var cur *Output

	for _, line := range strings.Split(string(out), "\n") {
		if !strings.HasPrefix(line, " ") && strings.Contains(line, "(") {
			if cur != nil {
				outputs = append(outputs, *cur)
			}
			name := strings.Fields(line)[0]
			cur = &Output{Name: name, Connected: true}
		}
		if cur == nil {
			continue
		}
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "Enabled:") {
			cur.Enabled = strings.Contains(trimmed, "yes")
		}
		if strings.Contains(trimmed, "px,") && strings.Contains(trimmed, "Hz") {
			// Mode line: "1920x1080 px, 60.000000 Hz (preferred, current)"
			fields := strings.Fields(trimmed)
			if len(fields) >= 1 {
				cur.Modes = append(cur.Modes, fields[0])
				if strings.Contains(trimmed, "current") {
					cur.Resolution = fields[0]
					if len(fields) >= 3 {
						cur.Refresh = strings.TrimSuffix(fields[2], ",")
					}
				}
			}
		}
	}
	if cur != nil {
		outputs = append(outputs, *cur)
	}
	return outputs
}

func (s *Service) listXrandr(ctx context.Context) []Output {
	out, err := output(ctx, "xrandr", "--query")
	if err != nil {
		return nil
	}

	var outputs []Output
	var cur *Output

	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, " connected") || strings.Contains(line, " disconnected") {
			if cur != nil {
				outputs = append(outputs, *cur)
			}
			fields := strings.Fields(line)
			cur = &Output{
				Name:      fields[0],
				Connected: strings.Contains(line, " connected"),
				Primary:   strings.Contains(line, "primary"),
			}
		}
		if cur == nil {
			continue
		}
		trimmed := strings.TrimSpace(line)
		if len(trimmed) > 0 && (trimmed[0] >= '0' && trimmed[0] <= '9') {
			fields := strings.Fields(trimmed)
			if len(fields) >= 1 {
				cur.Modes = append(cur.Modes, fields[0])
				if strings.Contains(trimmed, "*") {
					cur.Resolution = fields[0]
					cur.Enabled = true
					for _, f := range fields[1:] {
						if strings.Contains(f, "*") {
							cur.Refresh = strings.TrimRight(f, "*+ ")
						}
					}
				}
			}
		}
	}
	if cur != nil {
		outputs = append(outputs, *cur)
	}
	return outputs
}

func (s *Service) getBrightness() Brightness {
	matches, _ := filepath.Glob("/sys/class/backlight/*/brightness")
	for _, bPath := range matches {
		dir := filepath.Dir(bPath)
		device := filepath.Base(dir)
		curData, err := os.ReadFile(bPath)
		if err != nil {
			continue
		}
		maxData, err := os.ReadFile(filepath.Join(dir, "max_brightness"))
		if err != nil {
			continue
		}
		cur, _ := strconv.Atoi(strings.TrimSpace(string(curData)))
		max, _ := strconv.Atoi(strings.TrimSpace(string(maxData)))
		pct := 0
		if max > 0 {
			pct = cur * 100 / max
		}
		return Brightness{Current: pct, Max: max, Device: device}
	}
	return Brightness{Current: 100, Device: "none"}
}

// parseWlrOutput parses the text output of "wlr-randr" into Output structs.
// Exposed for testing.
func parseWlrOutput(out string) []Output {
	var outputs []Output
	var cur *Output

	for _, line := range strings.Split(out, "\n") {
		if !strings.HasPrefix(line, " ") && strings.Contains(line, "(") {
			if cur != nil {
				outputs = append(outputs, *cur)
			}
			name := strings.Fields(line)[0]
			cur = &Output{Name: name, Connected: true}
		}
		if cur == nil {
			continue
		}
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "Enabled:") {
			cur.Enabled = strings.Contains(trimmed, "yes")
		}
		if strings.Contains(trimmed, "px,") && strings.Contains(trimmed, "Hz") {
			fields := strings.Fields(trimmed)
			if len(fields) >= 1 {
				cur.Modes = append(cur.Modes, fields[0])
				if strings.Contains(trimmed, "current") {
					cur.Resolution = fields[0]
					if len(fields) >= 3 {
						cur.Refresh = strings.TrimSuffix(fields[2], ",")
					}
				}
			}
		}
	}
	if cur != nil {
		outputs = append(outputs, *cur)
	}
	return outputs
}

// parseXrandrOutput parses the text output of "xrandr --query" into Output structs.
// Exposed for testing.
func parseXrandrOutput(out string) []Output {
	var outputs []Output
	var cur *Output

	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, " connected") || strings.Contains(line, " disconnected") {
			if cur != nil {
				outputs = append(outputs, *cur)
			}
			fields := strings.Fields(line)
			cur = &Output{
				Name:      fields[0],
				Connected: strings.Contains(line, " connected"),
				Primary:   strings.Contains(line, "primary"),
			}
		}
		if cur == nil {
			continue
		}
		trimmed := strings.TrimSpace(line)
		if len(trimmed) > 0 && (trimmed[0] >= '0' && trimmed[0] <= '9') {
			fields := strings.Fields(trimmed)
			if len(fields) >= 1 {
				cur.Modes = append(cur.Modes, fields[0])
				if strings.Contains(trimmed, "*") {
					cur.Resolution = fields[0]
					cur.Enabled = true
					for _, f := range fields[1:] {
						if strings.Contains(f, "*") {
							cur.Refresh = strings.TrimRight(f, "*+ ")
						}
					}
				}
			}
		}
	}
	if cur != nil {
		outputs = append(outputs, *cur)
	}
	return outputs
}

// clampBrightness clamps percent to [0, 100]. Exposed for testing.
func clampBrightness(percent int) int {
	if percent < 0 {
		return 0
	}
	if percent > 100 {
		return 100
	}
	return percent
}

func detectCompositor() string {
	if os.Getenv("WAYLAND_DISPLAY") != "" {
		if _, err := exec.LookPath("wlr-randr"); err == nil {
			return "wlroots"
		}
		return "cage"
	}
	if os.Getenv("DISPLAY") != "" {
		return "x11"
	}
	return "unknown"
}

func run(ctx context.Context, name string, args ...string) error {
	return exec.CommandContext(ctx, name, args...).Run()
}

func output(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output()
}
