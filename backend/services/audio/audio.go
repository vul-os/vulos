package audio

import (
	"context"
	"fmt"
	"log"
	"os/exec"
	"strconv"
	"strings"
	"sync"
)

// Device is an audio input or output.
type Device struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Type    string `json:"type"` // "sink" (output) or "source" (input)
	Default bool   `json:"default"`
	Muted   bool   `json:"muted"`
	Volume  int    `json:"volume"` // 0-100
}

// Status is the full audio state.
type Status struct {
	Backend string   `json:"backend"` // "pipewire", "pulseaudio", "alsa"
	Outputs []Device `json:"outputs"`
	Inputs  []Device `json:"inputs"`
}

// Service manages audio via PipeWire/PulseAudio (pactl) or ALSA (amixer).
type Service struct {
	mu      sync.Mutex
	backend string
}

func New() *Service {
	backend := detectBackend()
	return &Service{backend: backend}
}

// GetStatus returns all audio devices and their state.
func (s *Service) GetStatus(ctx context.Context) Status {
	st := Status{Backend: s.backend}

	switch s.backend {
	case "pipewire", "pulseaudio":
		st.Outputs = s.pactlList(ctx, "sink")
		st.Inputs = s.pactlList(ctx, "source")
	case "alsa":
		st.Outputs = s.alsaList(ctx, "playback")
		st.Inputs = s.alsaList(ctx, "capture")
	}

	return st
}

// SetVolume sets volume for a device (0-100).
func (s *Service) SetVolume(ctx context.Context, deviceID string, deviceType string, volume int) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if volume < 0 {
		volume = 0
	}
	if volume > 100 {
		volume = 100
	}

	switch s.backend {
	case "pipewire", "pulseaudio":
		kind := "sink"
		if deviceType == "input" {
			kind = "source"
		}
		return run(ctx, "pactl", "set-"+kind+"-volume", deviceID, fmt.Sprintf("%d%%", volume))
	case "alsa":
		return run(ctx, "amixer", "sset", deviceID, fmt.Sprintf("%d%%", volume))
	}
	return fmt.Errorf("unsupported backend: %s", s.backend)
}

// SetMute mutes or unmutes a device.
func (s *Service) SetMute(ctx context.Context, deviceID string, deviceType string, muted bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	val := "0"
	if muted {
		val = "1"
	}

	switch s.backend {
	case "pipewire", "pulseaudio":
		kind := "sink"
		if deviceType == "input" {
			kind = "source"
		}
		return run(ctx, "pactl", "set-"+kind+"-mute", deviceID, val)
	case "alsa":
		toggle := "unmute"
		if muted {
			toggle = "mute"
		}
		return run(ctx, "amixer", "sset", deviceID, toggle)
	}
	return fmt.Errorf("unsupported backend: %s", s.backend)
}

// SetDefault sets the default output or input device.
func (s *Service) SetDefault(ctx context.Context, deviceID string, deviceType string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	switch s.backend {
	case "pipewire", "pulseaudio":
		kind := "default-sink"
		if deviceType == "input" {
			kind = "default-source"
		}
		return run(ctx, "pactl", "set-"+kind, deviceID)
	}
	log.Printf("[audio] set default %s=%s", deviceType, deviceID)
	return nil
}

func (s *Service) pactlList(ctx context.Context, kind string) []Device {
	// Get default
	defOut, _ := output(ctx, "pactl", "get-default-"+kind)
	defName := strings.TrimSpace(string(defOut))

	// List devices
	out, err := output(ctx, "pactl", "list", kind+"s")
	if err != nil {
		return nil
	}

	return parsePactlOutput(defName, string(out), kind)
}

// parsePactlOutput parses the text output of "pactl list sinks|sources" and
// returns a Device slice. defName is the current default device name (from
// "pactl get-default-sink|source"). This function is pure (no exec) and is
// package-accessible for testing.
func parsePactlOutput(defName, out, kind string) []Device {
	var devices []Device
	var cur *Device

	for _, line := range strings.Split(out, "\n") {
		trimmed := strings.TrimSpace(line)

		if strings.HasPrefix(trimmed, "Name:") {
			if cur != nil {
				devices = append(devices, *cur)
			}
			name := strings.TrimSpace(strings.TrimPrefix(trimmed, "Name:"))
			cur = &Device{
				ID:      name,
				Type:    kind,
				Default: name == defName,
			}
		}
		if cur == nil {
			continue
		}

		if strings.HasPrefix(trimmed, "Description:") {
			cur.Name = strings.TrimSpace(strings.TrimPrefix(trimmed, "Description:"))
		}
		if strings.Contains(trimmed, "Mute:") {
			cur.Muted = strings.Contains(trimmed, "yes")
		}
		if strings.HasPrefix(trimmed, "Volume:") && cur.Volume == 0 {
			// Parse "Volume: front-left: 42000 / 64% / -11.78 dB"
			if idx := strings.Index(trimmed, "/"); idx > 0 {
				rest := strings.TrimSpace(trimmed[idx+1:])
				if pctIdx := strings.Index(rest, "%"); pctIdx > 0 {
					pct := strings.TrimSpace(rest[:pctIdx])
					cur.Volume, _ = strconv.Atoi(pct)
				}
			}
		}
	}
	if cur != nil {
		devices = append(devices, *cur)
	}

	// Filter out monitor sources
	var filtered []Device
	for _, d := range devices {
		if !strings.Contains(d.ID, ".monitor") {
			filtered = append(filtered, d)
		}
	}
	return filtered
}

func (s *Service) alsaList(ctx context.Context, kind string) []Device {
	flag := "-l"
	if kind == "capture" {
		flag = "-L"
	}
	out, err := output(ctx, "aplay", flag)
	if err != nil {
		// Fallback: return a single Master control
		vol := 50
		if v, err := output(ctx, "amixer", "get", "Master"); err == nil {
			for _, line := range strings.Split(string(v), "\n") {
				if strings.Contains(line, "[") {
					if idx := strings.Index(line, "["); idx > 0 {
						rest := line[idx+1:]
						if pct := strings.Index(rest, "%]"); pct > 0 {
							vol, _ = strconv.Atoi(rest[:pct])
						}
					}
				}
			}
		}
		return []Device{{ID: "Master", Name: "Master", Type: kind, Default: true, Volume: vol}}
	}
	_ = out
	return []Device{{ID: "default", Name: "Default", Type: kind, Default: true, Volume: 50}}
}

func detectBackend() string {
	if _, err := exec.LookPath("pactl"); err == nil {
		out, _ := exec.Command("pactl", "info").Output()
		if strings.Contains(string(out), "PipeWire") {
			return "pipewire"
		}
		return "pulseaudio"
	}
	if _, err := exec.LookPath("amixer"); err == nil {
		return "alsa"
	}
	return "none"
}

func run(ctx context.Context, name string, args ...string) error {
	return exec.CommandContext(ctx, name, args...).Run()
}

func output(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output()
}
