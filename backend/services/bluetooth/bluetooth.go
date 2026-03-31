package bluetooth

import (
	"context"
	"fmt"
	"log"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Device is a discovered or paired Bluetooth device.
type Device struct {
	Address   string `json:"address"`
	Name      string `json:"name"`
	Paired    bool   `json:"paired"`
	Connected bool   `json:"connected"`
	Trusted   bool   `json:"trusted"`
	Type      string `json:"type"` // "audio", "input", "phone", "computer", "unknown"
	RSSI      int    `json:"rssi,omitempty"`
}

// Status is the Bluetooth adapter state.
type Status struct {
	Powered     bool     `json:"powered"`
	Discoverable bool   `json:"discoverable"`
	Discovering  bool   `json:"discovering"`
	Name        string   `json:"name"`
	Address     string   `json:"address"`
	Devices     []Device `json:"devices"`
}

// Service manages Bluetooth via bluetoothctl.
type Service struct {
	mu sync.Mutex
}

func New() *Service {
	return &Service{}
}

// Available checks if Bluetooth hardware is present.
func (s *Service) Available() bool {
	_, err := exec.LookPath("bluetoothctl")
	if err != nil {
		return false
	}
	out, err := btctl("show")
	return err == nil && strings.Contains(out, "Controller")
}

// GetStatus returns the adapter and device state.
func (s *Service) GetStatus(ctx context.Context) Status {
	st := Status{}

	out, err := btctl("show")
	if err != nil {
		return st
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Name:") {
			st.Name = strings.TrimSpace(strings.TrimPrefix(line, "Name:"))
		}
		if strings.HasPrefix(line, "Controller") {
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				st.Address = parts[1]
			}
		}
		if strings.Contains(line, "Powered: yes") {
			st.Powered = true
		}
		if strings.Contains(line, "Discoverable: yes") {
			st.Discoverable = true
		}
		if strings.Contains(line, "Discovering: yes") {
			st.Discovering = true
		}
	}

	st.Devices = s.listDevices(ctx)
	return st
}

// SetPower turns Bluetooth on or off.
func (s *Service) SetPower(ctx context.Context, on bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	val := "off"
	if on {
		val = "on"
	}
	_, err := btctl("power", val)
	log.Printf("[bluetooth] power %s", val)
	return err
}

// StartDiscovery scans for nearby devices.
func (s *Service) StartDiscovery(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := btctl("scan", "on")
	return err
}

// StopDiscovery stops scanning.
func (s *Service) StopDiscovery(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := btctl("scan", "off")
	return err
}

// Pair initiates pairing with a device.
func (s *Service) Pair(ctx context.Context, address string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := btctl("pair", address)
	if err != nil {
		return fmt.Errorf("pair %s: %w", address, err)
	}
	btctl("trust", address)
	log.Printf("[bluetooth] paired %s", address)
	return nil
}

// Connect connects to a paired device.
func (s *Service) Connect(ctx context.Context, address string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := btctl("connect", address)
	if err != nil {
		return fmt.Errorf("connect %s: %w", address, err)
	}
	log.Printf("[bluetooth] connected %s", address)
	return nil
}

// Disconnect drops a device connection.
func (s *Service) Disconnect(ctx context.Context, address string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := btctl("disconnect", address)
	return err
}

// Remove unpairs and forgets a device.
func (s *Service) Remove(ctx context.Context, address string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := btctl("remove", address)
	log.Printf("[bluetooth] removed %s", address)
	return err
}

func (s *Service) listDevices(ctx context.Context) []Device {
	out, err := btctl("devices")
	if err != nil {
		return nil
	}

	var devices []Device
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "Device ") {
			continue
		}
		parts := strings.SplitN(line, " ", 3)
		if len(parts) < 3 {
			continue
		}
		addr := parts[1]
		name := parts[2]

		dev := Device{Address: addr, Name: name}

		// Get detailed info
		info, _ := btctl("info", addr)
		for _, l := range strings.Split(info, "\n") {
			l = strings.TrimSpace(l)
			if strings.Contains(l, "Paired: yes") {
				dev.Paired = true
			}
			if strings.Contains(l, "Connected: yes") {
				dev.Connected = true
			}
			if strings.Contains(l, "Trusted: yes") {
				dev.Trusted = true
			}
			if strings.Contains(l, "Icon: audio") {
				dev.Type = "audio"
			} else if strings.Contains(l, "Icon: input") {
				dev.Type = "input"
			} else if strings.Contains(l, "Icon: phone") {
				dev.Type = "phone"
			} else if strings.Contains(l, "Icon: computer") {
				dev.Type = "computer"
			}
		}
		if dev.Type == "" {
			dev.Type = "unknown"
		}

		devices = append(devices, dev)
	}
	return devices
}

func btctl(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bluetoothctl", args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}
