// Package telephony provides a ModemManager D-Bus client for enumerating
// modems and querying signal/SIM information via org.freedesktop.ModemManager1.
// When D-Bus is unavailable (e.g. Docker, CI) all calls return graceful empty
// results without panicking.
package telephony

import (
	"log"
	"os/exec"
	"strconv"
	"strings"
)

const (
	mmService = "org.freedesktop.ModemManager1"
	mmPath    = "/org/freedesktop/ModemManager1"
)

// Modem represents a single modem device exposed by ModemManager.
type Modem struct {
	Path          string `json:"path"`           // D-Bus object path, e.g. /org/freedesktop/ModemManager1/Modem/0
	Manufacturer  string `json:"manufacturer"`   // e.g. "Sierra Wireless"
	Model         string `json:"model"`          // e.g. "MC7455"
	State         string `json:"state"`          // e.g. "registered", "connected", "disabled"
	SignalQuality int    `json:"signal_quality"` // 0-100
	IMEI          string `json:"imei,omitempty"`
	IMSI          string `json:"imsi,omitempty"` // from SIM
	Operator      string `json:"operator,omitempty"`
	Technology    string `json:"technology,omitempty"` // "lte", "5g-nr", "umts", etc.
	PowerState    string `json:"power_state"`          // "on", "off", "low"
}

// mmClient is a thin wrapper around the gdbus CLI tool to avoid a cgo D-Bus
// dependency. When gdbus is absent or D-Bus is unreachable the client simply
// returns empty results.
type mmClient struct {
	available bool // false when gdbus is missing or bus is unreachable
}

// newMMClient probes whether D-Bus + ModemManager are accessible and returns
// a client. The client is always non-nil; callers check available.
func newMMClient() *mmClient {
	c := &mmClient{}
	if _, err := exec.LookPath("gdbus"); err != nil {
		log.Printf("[telephony] gdbus not found — modem enumeration disabled")
		return c
	}
	// Quick probe: introspect the manager object. If it fails, D-Bus/MM is absent.
	out, err := gdbusCall(mmService, mmPath, "org.freedesktop.DBus.Introspectable", "Introspect")
	if err != nil || !strings.Contains(out, "ModemManager") {
		log.Printf("[telephony] ModemManager not reachable via D-Bus — running in no-modem mode")
		return c
	}
	c.available = true
	return c
}

// ListModems enumerates all modems known to ModemManager. Returns an empty
// (non-nil) slice when the bus is unavailable.
func (c *mmClient) ListModems() []Modem {
	if !c.available {
		return []Modem{}
	}

	// Call org.freedesktop.ModemManager1.GetManagedObjects (standard ObjectManager)
	out, err := gdbusCall(mmService, mmPath, "org.freedesktop.DBus.ObjectManager", "GetManagedObjects")
	if err != nil {
		log.Printf("[telephony] GetManagedObjects: %v", err)
		return []Modem{}
	}

	paths := parseModemPaths(out)
	modems := make([]Modem, 0, len(paths))
	for _, p := range paths {
		m := c.fetchModem(p)
		modems = append(modems, m)
	}
	return modems
}

// fetchModem reads properties for a single modem object path.
func (c *mmClient) fetchModem(path string) Modem {
	m := Modem{Path: path, PowerState: "on"}

	// Read org.freedesktop.ModemManager1.Modem properties
	if out, err := gdbusGetAll(mmService, path, "org.freedesktop.ModemManager1.Modem"); err == nil {
		for _, line := range strings.Split(out, "\n") {
			line = strings.TrimSpace(line)
			switch {
			case strings.HasPrefix(line, "'Manufacturer':"):
				m.Manufacturer = extractStringVal(line)
			case strings.HasPrefix(line, "'Model':"):
				m.Model = extractStringVal(line)
			case strings.HasPrefix(line, "'EquipmentIdentifier':"):
				m.IMEI = extractStringVal(line)
			case strings.HasPrefix(line, "'State':"):
				m.State = modemStateString(extractIntVal(line))
			case strings.HasPrefix(line, "'PowerState':"):
				m.PowerState = modemPowerStateString(extractIntVal(line))
			case strings.HasPrefix(line, "'AccessTechnologies':"):
				m.Technology = accessTechString(extractIntVal(line))
			case strings.HasPrefix(line, "'SignalQuality':"):
				// Value is a tuple (uint32, boolean) → take first int
				if v := extractFirstInt(line); v >= 0 {
					m.SignalQuality = v
				}
			case strings.HasPrefix(line, "'OperatorName':"):
				m.Operator = extractStringVal(line)
			}
		}
	}

	// Read SIM properties (best-effort)
	if out, err := gdbusGetAll(mmService, path, "org.freedesktop.ModemManager1.Modem.Sim"); err == nil {
		for _, line := range strings.Split(out, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "'Imsi':") {
				m.IMSI = extractStringVal(line)
			}
		}
	}

	return m
}

// --- gdbus helpers ----------------------------------------------------------

func gdbusCall(service, path, iface, method string) (string, error) {
	out, err := exec.Command("gdbus", "call",
		"--system",
		"--dest", service,
		"--object-path", path,
		"--method", iface+"."+method,
	).Output()
	return string(out), err
}

func gdbusGetAll(service, path, iface string) (string, error) {
	out, err := exec.Command("gdbus", "call",
		"--system",
		"--dest", service,
		"--object-path", path,
		"--method", "org.freedesktop.DBus.Properties.GetAll",
		iface,
	).Output()
	return string(out), err
}

// parseModemPaths extracts object paths matching the Modem pattern from
// GetManagedObjects output. The output is a GLib variant dict; we look for
// lines containing the path string directly.
func parseModemPaths(out string) []string {
	var paths []string
	seen := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		// Object paths appear like: '/org/freedesktop/ModemManager1/Modem/0',
		if strings.Contains(line, "/org/freedesktop/ModemManager1/Modem/") {
			// Extract the path token
			start := strings.Index(line, "/org/freedesktop/ModemManager1/Modem/")
			if start < 0 {
				continue
			}
			rest := line[start:]
			// Trim trailing quote/comma/paren
			for _, ch := range []string{"'", "\"", ",", ")", " "} {
				if idx := strings.Index(rest, ch); idx > 0 {
					rest = rest[:idx]
				}
			}
			// Ensure it ends with a digit (actual modem index)
			if len(rest) > 0 && seen[rest] == false {
				parts := strings.Split(rest, "/")
				last := parts[len(parts)-1]
				if _, err := strconv.Atoi(last); err == nil {
					paths = append(paths, rest)
					seen[rest] = true
				}
			}
		}
	}
	return paths
}

// --- value extraction helpers -----------------------------------------------

// extractStringVal pulls 'key': <'value'> → value.
func extractStringVal(line string) string {
	idx := strings.Index(line, "<'")
	if idx < 0 {
		return ""
	}
	rest := line[idx+2:]
	end := strings.Index(rest, "'")
	if end < 0 {
		return ""
	}
	return rest[:end]
}

// extractIntVal pulls the integer out of 'key': <uint32 N> or <int32 N>.
func extractIntVal(line string) int {
	for _, marker := range []string{"<uint32 ", "<int32 ", "<uint64 ", "<int64 "} {
		idx := strings.Index(line, marker)
		if idx < 0 {
			continue
		}
		rest := line[idx+len(marker):]
		end := strings.IndexAny(rest, "> ,")
		if end < 0 {
			end = len(rest)
		}
		if v, err := strconv.Atoi(strings.TrimSpace(rest[:end])); err == nil {
			return v
		}
	}
	return -1
}

// extractFirstInt finds the first integer value in a line (for tuples).
func extractFirstInt(line string) int {
	return extractIntVal(line)
}

// --- enum string conversions ------------------------------------------------

// modemStateString converts MMModemState enum to a readable string.
// Values from ModemManager headers (MM_MODEM_STATE_*).
func modemStateString(state int) string {
	switch state {
	case -1:
		return "failed"
	case 0:
		return "unknown"
	case 1:
		return "initializing"
	case 2:
		return "locked"
	case 3:
		return "disabled"
	case 4:
		return "disabling"
	case 5:
		return "enabling"
	case 6:
		return "enabled"
	case 7:
		return "searching"
	case 8:
		return "registered"
	case 9:
		return "disconnecting"
	case 10:
		return "connecting"
	case 11:
		return "connected"
	default:
		return "unknown"
	}
}

// modemPowerStateString converts MMModemPowerState.
func modemPowerStateString(ps int) string {
	switch ps {
	case 1:
		return "off"
	case 2:
		return "low"
	case 3:
		return "on"
	default:
		return "unknown"
	}
}

// accessTechString converts MMModemAccessTechnology bitmask to a short label.
// We report the highest-priority technology present.
func accessTechString(mask int) string {
	switch {
	case mask&(1<<15) != 0:
		return "5g-nr"
	case mask&(1<<14) != 0:
		return "lte"
	case mask&(1<<8) != 0, mask&(1<<9) != 0:
		return "hspa+"
	case mask&(1<<7) != 0:
		return "hspa"
	case mask&(1<<6) != 0:
		return "hsdpa"
	case mask&(1<<5) != 0:
		return "umts"
	case mask&(1<<4) != 0:
		return "edge"
	case mask&(1<<1) != 0, mask&(1<<2) != 0, mask&(1<<3) != 0:
		return "gprs"
	case mask&(1<<0) != 0:
		return "gsm"
	default:
		return "unknown"
	}
}
