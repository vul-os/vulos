package bluetooth

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// ErrUnavailable means this box could not ASK bluetoothctl — the binary is
// missing, the daemon is not running, or the call timed out. It is not an
// answer about the radio.
//
// It exists because the alternative, which this package shipped, is worse than
// no answer: GetStatus swallowed the error and returned a zero-valued Status,
// so a dead bluetoothd was served as HTTP 200 `{"powered":false,"devices":null}`
// — indistinguishable on the wire from a healthy adapter that is switched off
// with nothing paired. The Settings panel had already been hardened to disable
// its toggle when `powered` is absent, on the principle that a radio whose
// state is unknown is not a radio you can be asked to toggle; that state was
// unreachable from the real backend, because the box never said "I don't know".
//
// The idiom is wltoplevel's (services/wltoplevel/wltoplevel.go:126) and is used
// here for the same reason: the caller gets an error, and the HTTP layer turns
// it into 503, never 200-with-a-zero-value.
var ErrUnavailable = errors.New("bluetooth: bluetoothctl unavailable")

// Device is a discovered or paired Bluetooth device.
type Device struct {
	Address string `json:"address"`
	Name    string `json:"name"`

	// Paired, Connected, Trusted and Type come from "bluetoothctl info <addr>".
	// When Unmeasured is true that call FAILED and all four below are zero
	// values that were never observed — see Unmeasured.
	Paired    bool   `json:"paired"`
	Connected bool   `json:"connected"`
	Trusted   bool   `json:"trusted"`
	Type      string `json:"type"` // "audio", "input", "phone", "computer", "unknown"
	RSSI      int    `json:"rssi,omitempty"`

	// Unmeasured is true when "bluetoothctl info <addr>" could not be run for
	// this device, so its flags above are UNMEASURED rather than false. The
	// error from that call used to be discarded (`info, _ := btctl(...)`),
	// which reported a connected headset as `connected:false` and a paired
	// keyboard as `paired:false` — measured-looking answers nobody measured,
	// and the exact input the panel uses to decide whether to offer "Pair".
	//
	// Address and Name are still real: they come from "bluetoothctl devices",
	// which succeeded. Only the info-derived fields are unknown, so this is a
	// per-device flag rather than a failure of the whole listing — a device
	// drifting out of range between the two calls is a normal race and must not
	// blank the panel.
	//
	// `omitempty` keeps the success path byte-identical to the previous wire
	// shape, so existing callers are unaffected. Note the negative sense is
	// deliberate: absent must mean "measured", because that is what every
	// response from an older box means.
	Unmeasured bool `json:"unmeasured,omitempty"`
}

// Status is the Bluetooth adapter state.
type Status struct {
	Powered      bool     `json:"powered"`
	Discoverable bool     `json:"discoverable"`
	Discovering  bool     `json:"discovering"`
	Name         string   `json:"name"`
	Address      string   `json:"address"`
	Devices      []Device `json:"devices"`
}

// Service manages Bluetooth via bluetoothctl.
type Service struct {
	mu sync.Mutex

	// run invokes bluetoothctl. It is a field so tests can drive the error
	// paths without a bluetooth daemon; production always gets btctl. The
	// injection point mirrors wltoplevel's Executor, for the same reason —
	// the failure branches are the ones that were wrong, and they are
	// unreachable in a test that can only run the real binary.
	run func(args ...string) (string, error)
}

func New() *Service {
	return &Service{run: btctl}
}

// newWithRunner is used by tests to inject a fake bluetoothctl.
func newWithRunner(run func(args ...string) (string, error)) *Service {
	return &Service{run: run}
}

// Available reports whether bluetoothctl is installed AND answered. It cannot
// distinguish "no Bluetooth hardware" from "bluetoothd is dead", so a false
// here is not evidence about the radio; callers wanting that distinction must
// use GetStatus and check for ErrUnavailable.
func (s *Service) Available() bool {
	_, err := exec.LookPath("bluetoothctl")
	if err != nil {
		return false
	}
	out, err := s.run("show")
	return err == nil && strings.Contains(out, "Controller")
}

// GetStatus returns the adapter and device state.
//
// A failure to run "bluetoothctl show" returns ErrUnavailable and a zero
// Status which the caller MUST NOT serve — see ErrUnavailable. Returning
// (zero, nil) here was the bug: it asserted powered:false about a radio nobody
// had managed to look at.
func (s *Service) GetStatus(ctx context.Context) (Status, error) {
	st := Status{}

	out, err := s.run("show")
	if err != nil {
		return Status{}, fmt.Errorf("%w: bluetoothctl show: %v", ErrUnavailable, err)
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

	devs, err := s.listDevices(ctx)
	if err != nil {
		// "show" answered but "devices" did not. An empty device list because
		// the daemon just died and an empty device list because nothing is
		// paired or nearby are different facts, and the panel draws them
		// identically, so this is unavailable too — not a status with no
		// devices in it.
		return Status{}, err
	}
	st.Devices = devs
	return st, nil
}

// SetPower turns Bluetooth on or off.
func (s *Service) SetPower(ctx context.Context, on bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	val := "off"
	if on {
		val = "on"
	}
	_, err := s.run("power", val)
	log.Printf("[bluetooth] power %s", val)
	return err
}

// StartDiscovery scans for nearby devices.
func (s *Service) StartDiscovery(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.run("scan", "on")
	return err
}

// StopDiscovery stops scanning.
func (s *Service) StopDiscovery(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.run("scan", "off")
	return err
}

// Pair initiates pairing with a device.
func (s *Service) Pair(ctx context.Context, address string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.run("pair", address)
	if err != nil {
		return fmt.Errorf("pair %s: %w", address, err)
	}
	// Trusting is a follow-on convenience (it stops bluetoothd re-asking on
	// every reconnect), so a failure here does not undo the pairing and is not
	// returned as a failure to pair. It is no longer thrown away either: the
	// device really is paired-but-untrusted, and that is the difference between
	// a headset that reconnects by itself and one that does not.
	if _, err := s.run("trust", address); err != nil {
		log.Printf("[bluetooth] paired %s but trust failed: %v — device will not auto-reconnect", address, err)
	}
	log.Printf("[bluetooth] paired %s", address)
	return nil
}

// Connect connects to a paired device.
func (s *Service) Connect(ctx context.Context, address string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.run("connect", address)
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
	_, err := s.run("disconnect", address)
	return err
}

// Remove unpairs and forgets a device.
func (s *Service) Remove(ctx context.Context, address string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.run("remove", address)
	log.Printf("[bluetooth] removed %s", address)
	return err
}

// listDevices enumerates known devices.
//
// A failure of "bluetoothctl devices" returns ErrUnavailable — NOT nil. Those
// two used to be the same value, so "I could not ask" was served as "there is
// nothing here".
func (s *Service) listDevices(ctx context.Context) ([]Device, error) {
	out, err := s.run("devices")
	if err != nil {
		return nil, fmt.Errorf("%w: bluetoothctl devices: %v", ErrUnavailable, err)
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

		// Get detailed info. A failure here leaves Paired/Connected/Trusted/Type
		// UNSET and says so, rather than reporting the zero values as
		// observations — the error used to be discarded outright.
		info, infoErr := s.run("info", addr)
		if infoErr != nil {
			dev.Unmeasured = true
			devices = append(devices, dev)
			continue
		}
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
	return devices, nil
}

// parseStatus parses the text output of "bluetoothctl show" into a Status.
// Exposed for testing; does not populate Devices.
func parseStatus(out string) Status {
	st := Status{}
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
	return st
}

// parseDeviceList parses the text output of "bluetoothctl devices" into a slice of
// partial Device values (Address and Name only). Exposed for testing.
func parseDeviceList(out string) []Device {
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
		devices = append(devices, Device{Address: parts[1], Name: parts[2]})
	}
	return devices
}

// parseDeviceInfo merges the text output of "bluetoothctl info <addr>" into dev.
// Exposed for testing.
func parseDeviceInfo(info string, dev *Device) {
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
}

// ValidMAC returns true if s looks like a valid Bluetooth MAC address
// (six colon-separated hex octets, case-insensitive).
func ValidMAC(s string) bool {
	parts := strings.Split(s, ":")
	if len(parts) != 6 {
		return false
	}
	const hex = "0123456789abcdefABCDEF"
	for _, p := range parts {
		if len(p) != 2 {
			return false
		}
		for _, c := range p {
			if !strings.ContainsRune(hex, c) {
				return false
			}
		}
	}
	return true
}

func btctl(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bluetoothctl", args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}
