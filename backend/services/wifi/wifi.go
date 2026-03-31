package wifi

import (
	"context"
	"fmt"
	"log"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Network is a visible WiFi network.
type Network struct {
	SSID     string `json:"ssid"`
	BSSID    string `json:"bssid"`
	Signal   int    `json:"signal"`   // dBm
	Security string `json:"security"` // "open", "WPA2", "WPA3", etc.
	Freq     int    `json:"freq"`     // MHz
	Band     string `json:"band"`     // "2.4GHz" or "5GHz"
}

// Connection is the current WiFi state.
type Connection struct {
	Connected bool   `json:"connected"`
	SSID      string `json:"ssid,omitempty"`
	BSSID     string `json:"bssid,omitempty"`
	IP        string `json:"ip,omitempty"`
	Signal    int    `json:"signal,omitempty"`
	Freq      int    `json:"freq,omitempty"`
	TxRate    string `json:"tx_rate,omitempty"`
}

// SavedNetwork is a remembered WiFi credential.
type SavedNetwork struct {
	SSID     string `json:"ssid"`
	Priority int    `json:"priority"`
}

// Service manages WiFi via iw/wpa_supplicant.
type Service struct {
	mu        sync.Mutex
	iface     string
}

func New() *Service {
	iface := detectInterface()
	return &Service{iface: iface}
}

// Interface returns the WiFi interface name.
func (s *Service) Interface() string { return s.iface }

// Scan returns visible WiFi networks.
func (s *Service) Scan(ctx context.Context) ([]Network, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Trigger scan
	run(ctx, "iw", "dev", s.iface, "scan", "trigger")
	time.Sleep(2 * time.Second)

	out, err := output(ctx, "iw", "dev", s.iface, "scan", "dump")
	if err != nil {
		return nil, fmt.Errorf("scan failed: %w", err)
	}

	networks := parseIWScan(string(out))
	sort.Slice(networks, func(i, j int) bool { return networks[i].Signal > networks[j].Signal })
	return networks, nil
}

// Status returns the current connection.
func (s *Service) Status(ctx context.Context) Connection {
	out, err := output(ctx, "iw", "dev", s.iface, "link")
	if err != nil {
		return Connection{Connected: false}
	}

	text := string(out)
	if strings.Contains(text, "Not connected") {
		return Connection{Connected: false}
	}

	conn := Connection{Connected: true}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "SSID:") {
			conn.SSID = strings.TrimSpace(strings.TrimPrefix(line, "SSID:"))
		}
		if strings.HasPrefix(line, "signal:") {
			fmt.Sscanf(line, "signal: %d", &conn.Signal)
		}
		if strings.HasPrefix(line, "freq:") {
			fmt.Sscanf(line, "freq: %d", &conn.Freq)
		}
		if strings.HasPrefix(line, "tx bitrate:") {
			conn.TxRate = strings.TrimSpace(strings.TrimPrefix(line, "tx bitrate:"))
		}
	}

	// Get IP
	if ipOut, err := output(ctx, "ip", "-4", "addr", "show", s.iface); err == nil {
		for _, line := range strings.Split(string(ipOut), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "inet ") {
				parts := strings.Fields(line)
				if len(parts) >= 2 {
					conn.IP = strings.Split(parts[1], "/")[0]
				}
			}
		}
	}

	return conn
}

// Connect joins a WiFi network via wpa_supplicant.
func (s *Service) Connect(ctx context.Context, ssid, password string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Add network to wpa_supplicant
	var netBlock string
	if password == "" {
		netBlock = fmt.Sprintf("network={\n\tssid=\"%s\"\n\tkey_mgmt=NONE\n}\n", ssid)
	} else {
		netBlock = fmt.Sprintf("network={\n\tssid=\"%s\"\n\tpsk=\"%s\"\n}\n", ssid, password)
	}

	// Append to wpa_supplicant.conf
	confPath := "/etc/wpa_supplicant/wpa_supplicant.conf"
	cmd := fmt.Sprintf("echo '%s' >> %s", netBlock, confPath)
	if err := run(ctx, "sh", "-c", cmd); err != nil {
		return fmt.Errorf("write config: %w", err)
	}

	// Reconfigure
	if err := run(ctx, "wpa_cli", "-i", s.iface, "reconfigure"); err != nil {
		return fmt.Errorf("reconfigure: %w", err)
	}

	log.Printf("[wifi] connecting to %s", ssid)
	return nil
}

// Disconnect drops the current WiFi connection.
func (s *Service) Disconnect(ctx context.Context) error {
	return run(ctx, "wpa_cli", "-i", s.iface, "disconnect")
}

// SavedNetworks lists remembered networks from wpa_supplicant.
func (s *Service) SavedNetworks(ctx context.Context) []SavedNetwork {
	out, err := output(ctx, "wpa_cli", "-i", s.iface, "list_networks")
	if err != nil {
		return nil
	}
	var result []SavedNetwork
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Split(line, "\t")
		if len(fields) >= 2 {
			if _, err := strconv.Atoi(fields[0]); err == nil {
				result = append(result, SavedNetwork{SSID: fields[1]})
			}
		}
	}
	return result
}

// ForgetNetwork removes a saved network.
func (s *Service) ForgetNetwork(ctx context.Context, ssid string) error {
	networks := s.SavedNetworks(ctx)
	out, _ := output(ctx, "wpa_cli", "-i", s.iface, "list_networks")
	for i, line := range strings.Split(string(out), "\n") {
		if i == 0 { continue }
		fields := strings.Split(line, "\t")
		if len(fields) >= 2 && fields[1] == ssid {
			run(ctx, "wpa_cli", "-i", s.iface, "remove_network", fields[0])
			run(ctx, "wpa_cli", "-i", s.iface, "save_config")
			return nil
		}
	}
	_ = networks
	return fmt.Errorf("network %s not found", ssid)
}

func detectInterface() string {
	out, err := exec.Command("iw", "dev").Output()
	if err != nil {
		return "wlan0"
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Interface ") {
			return strings.TrimPrefix(line, "Interface ")
		}
	}
	return "wlan0"
}

func parseIWScan(data string) []Network {
	var networks []Network
	var cur *Network

	for _, line := range strings.Split(data, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "BSS ") {
			if cur != nil {
				networks = append(networks, *cur)
			}
			cur = &Network{}
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				cur.BSSID = strings.TrimSuffix(parts[1], "(on")
			}
		}
		if cur == nil { continue }
		if strings.HasPrefix(line, "SSID:") {
			cur.SSID = strings.TrimSpace(strings.TrimPrefix(line, "SSID:"))
		}
		if strings.HasPrefix(line, "signal:") {
			fmt.Sscanf(line, "signal: %d", &cur.Signal)
		}
		if strings.HasPrefix(line, "freq:") {
			fmt.Sscanf(line, "freq: %d", &cur.Freq)
			if cur.Freq > 4000 {
				cur.Band = "5GHz"
			} else {
				cur.Band = "2.4GHz"
			}
		}
		if strings.Contains(line, "WPA") || strings.Contains(line, "RSN") {
			if strings.Contains(line, "WPA3") || strings.Contains(line, "SAE") {
				cur.Security = "WPA3"
			} else {
				cur.Security = "WPA2"
			}
		}
	}
	if cur != nil && cur.SSID != "" {
		networks = append(networks, *cur)
	}
	return networks
}

func run(ctx context.Context, name string, args ...string) error {
	return exec.CommandContext(ctx, name, args...).Run()
}

func output(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output()
}
