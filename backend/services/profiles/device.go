package profiles

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// FormFactor is one of the supported device profile types.
type FormFactor string

const (
	FormFactorPC    FormFactor = "pc"
	FormFactorTV    FormFactor = "tv"
	FormFactorCar   FormFactor = "car"
	FormFactorWatch FormFactor = "watch"
)

// ValidFormFactor returns true if f is one of the known form factors.
func ValidFormFactor(f FormFactor) bool {
	switch f {
	case FormFactorPC, FormFactorTV, FormFactorCar, FormFactorWatch:
		return true
	}
	return false
}

// deviceProfileData is the on-disk structure.
type deviceProfileData struct {
	Profile FormFactor `json:"profile"`
}

// DeviceProfileStore holds the persisted form-factor selection and provides
// heuristic detection of the likely form factor.
type DeviceProfileStore struct {
	mu        sync.RWMutex
	path      string
	profile   FormFactor
	suggested FormFactor
}

// NewDeviceProfileStore initialises the store, loading any previously persisted
// choice from dbDir/device-profile.json. Detection runs once at startup.
func NewDeviceProfileStore(dbDir string) *DeviceProfileStore {
	path := filepath.Join(dbDir, "device-profile.json")
	s := &DeviceProfileStore{
		path:      path,
		suggested: detectFormFactor(),
	}

	// Load persisted profile (best-effort).
	if data, err := os.ReadFile(path); err == nil {
		var d deviceProfileData
		if json.Unmarshal(data, &d) == nil && ValidFormFactor(d.Profile) {
			s.profile = d.Profile
		}
	}

	// If no persisted value, fall back to the detected suggestion.
	if s.profile == "" {
		s.profile = s.suggested
	}

	log.Printf("[device-profile] profile=%s suggested=%s", s.profile, s.suggested)
	return s
}

// Get returns the current profile and the auto-detected suggestion.
func (s *DeviceProfileStore) Get() (profile, suggested FormFactor) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.profile, s.suggested
}

// Set persists a new profile choice to disk.
func (s *DeviceProfileStore) Set(f FormFactor) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.profile = f
	return s.flush()
}

// flush writes the current profile to disk (caller holds lock).
func (s *DeviceProfileStore) flush() error {
	data, _ := json.MarshalIndent(deviceProfileData{Profile: s.profile}, "", "  ")
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// detectFormFactor tries several heuristics to guess the device type.
// It must never crash; unknown / inaccessible sources are silently skipped.
func detectFormFactor() FormFactor {
	// 1. Explicit override via environment variable (useful for embedded builds).
	if env := os.Getenv("VULOS_FORM_FACTOR"); env != "" {
		ff := FormFactor(strings.ToLower(env))
		if ValidFormFactor(ff) {
			return ff
		}
	}

	// 2. DMI chassis type — available on most x86 machines via sysfs.
	//    Chassis type codes are defined by SMBIOS spec:
	//    https://www.dmtf.org/sites/default/files/standards/documents/DSP0134_3.7.0.pdf
	//    Table 16 — System Enclosure or Chassis Types
	//
	//    We read one or both of the common sysfs paths and interpret the value.
	chassis := readDMIFile("/sys/class/dmi/id/chassis_type")
	if chassis == "" {
		// Some embedded boards expose it here instead.
		chassis = readDMIFile("/sys/firmware/dmi/tables/DMITable")
	}
	if chassis != "" {
		switch strings.TrimSpace(chassis) {
		case "3": // Desktop
			return FormFactorPC
		case "6", "7": // Mini Tower, Tower
			return FormFactorPC
		case "8", "9", "10", "14": // Portable, Laptop, Notebook, Sub Notebook
			return FormFactorPC
		case "30": // Tablet
			return FormFactorPC
		case "31", "32": // Convertible, Detachable
			return FormFactorPC
		}
	}

	// 3. Car/AAOS hint: Android Automotive sets VULOS_DEVICE=car or a known env.
	if strings.EqualFold(os.Getenv("VULOS_DEVICE"), "car") {
		return FormFactorCar
	}

	// 4. TV hint: some SoC environments set this.
	if strings.EqualFold(os.Getenv("VULOS_DEVICE"), "tv") {
		return FormFactorTV
	}

	// 5. Watch hint.
	if strings.EqualFold(os.Getenv("VULOS_DEVICE"), "watch") {
		return FormFactorWatch
	}

	// Default: PC is the safest assumption for any desktop/server Linux install.
	return FormFactorPC
}

// readDMIFile reads a sysfs DMI file and returns its trimmed content,
// or empty string on any error (file missing, permission denied, etc.).
func readDMIFile(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}
