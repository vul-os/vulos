package location

// OPTIONAL modem-GPS fallback for boxes with a GNSS-capable USB LTE modem
// attached, spoken through ModemManager's `mmcli` (same "shell out to a CLI"
// approach as services/telephony — a tiny exec+parseKV helper is duplicated
// here rather than importing telephony, per the location package's isolation
// requirement).
//
// HARDWARE-GATED: most LTE modems have no GNSS receiver at all, and even a
// GNSS-capable one only has a fix after `--location-enable-gps-raw` (or
// `--location-enable-gps-nmea`) has been run and the antenna has a sky view.
// ModemSource.ModemPosition() returns (Position{}, false) for every one of
// those "no" cases — this code cannot be exercised without real GNSS hardware
// and has NOT been verified against a live modem; correctness of the mmcli
// key names is best-effort from ModemManager's documented `-K` schema.

import (
	"context"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// modemExecTimeout bounds a wedged modem the same way services/telephony does.
const modemExecTimeout = 5 * time.Second

// runMMCLI runs `mmcli <args...>` and returns trimmed stdout, or an error.
// Duplicated (not imported) from services/telephony/telephony.go by design —
// this package must not depend on telephony.
func runMMCLI(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), modemExecTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "mmcli", args...).Output()
	return strings.TrimSpace(string(out)), err
}

// mmcliAvailable reports whether the ModemManager CLI is installed.
func mmcliAvailable() bool {
	_, err := exec.LookPath("mmcli")
	return err == nil
}

var reModemPathLoc = regexp.MustCompile(`/org/freedesktop/ModemManager1/Modem/(\d+)`)

// parseModemKV turns mmcli `-K` output ("a.b.c : value" lines) into a flat
// map. Sentinel "--" (mmcli's "unset") becomes "". Pure function — tested
// directly on sample `-K` text without any hardware.
func parseModemKV(out string) map[string]string {
	m := make(map[string]string)
	for _, line := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key := strings.TrimSpace(k)
		val := strings.TrimSpace(v)
		if val == "--" {
			val = ""
		}
		if key != "" {
			m[key] = val
		}
	}
	return m
}

// firstModemIndex resolves the first modem's index from `mmcli -L`, or ""
// when no modem is present.
func firstModemIndex() string {
	out, err := runMMCLI("-L")
	if err != nil {
		return ""
	}
	match := reModemPathLoc.FindStringSubmatch(out)
	if match == nil {
		return ""
	}
	return match[1]
}

// gpsLatLng extracts a GPS latitude/longitude from a `--location-get -K`
// key-value map. ModemManager's documented key names are
// "location.gps.latitude" / "location.gps.longitude"; some builds have been
// observed to prefix modem-scoped keys with "modem." (mirroring
// "modem.generic.*" elsewhere), so both are checked. ok is false when neither
// is present (no GNSS fix — the overwhelmingly common case: most LTE sticks
// have no GNSS receiver, or one that has never obtained a fix).
func gpsLatLng(kv map[string]string) (lat, lng, alt float64, ok bool) {
	latStr := firstNonEmptyKV(kv, "location.gps.latitude", "modem.location.gps.latitude")
	lngStr := firstNonEmptyKV(kv, "location.gps.longitude", "modem.location.gps.longitude")
	if latStr == "" || lngStr == "" {
		return 0, 0, 0, false
	}
	la, err1 := strconv.ParseFloat(latStr, 64)
	lo, err2 := strconv.ParseFloat(lngStr, 64)
	if err1 != nil || err2 != nil {
		return 0, 0, 0, false
	}
	altStr := firstNonEmptyKV(kv, "location.gps.altitude", "modem.location.gps.altitude")
	al, _ := strconv.ParseFloat(altStr, 64) // altitude is optional/best-effort
	return la, lo, al, true
}

func firstNonEmptyKV(kv map[string]string, keys ...string) string {
	for _, k := range keys {
		if v := kv[k]; v != "" {
			return v
		}
	}
	return ""
}

// ModemSource is the concrete ModemFallback backed by a real (or absent)
// modem. Zero value is ready to use.
type ModemSource struct{}

// NewModemSource returns a ModemFallback that reads GNSS fixes from an
// attached ModemManager modem, or degrades to "unavailable" cleanly when
// there is no mmcli / no modem / no GNSS fix.
func NewModemSource() *ModemSource { return &ModemSource{} }

// ModemPosition implements ModemFallback. Returns (Position{}, false) unless
// mmcli is installed, a modem is present, GPS has been enabled on it, AND it
// currently has a fix.
func (m *ModemSource) ModemPosition() (Position, bool) {
	if !mmcliAvailable() {
		return Position{}, false
	}
	id := firstModemIndex()
	if id == "" {
		return Position{}, false
	}
	out, err := runMMCLI("-m", id, "--location-get", "-K")
	if err != nil {
		return Position{}, false
	}
	kv := parseModemKV(out)
	lat, lng, alt, ok := gpsLatLng(kv)
	if !ok {
		return Position{}, false
	}
	if err := validatePosition(Position{Lat: lat, Lng: lng}); err != nil {
		return Position{}, false
	}
	return Position{
		Lat:      lat,
		Lng:      lng,
		Altitude: alt,
		TS:       time.Now().Unix(),
		Source:   "modem",
	}, true
}
