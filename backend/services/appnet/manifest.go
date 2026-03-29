package appnet

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Valid app categories.
var ValidCategories = []string{
	"core", "productivity", "media", "developer", "system", "network", "database", "other",
}

// Valid permission types apps can request.
var ValidPermissions = []string{
	"network",    // outbound network access (all apps get loopback)
	"filesystem", // read/write outside app data dir
	"camera",     // camera device access
	"microphone", // microphone device access
	"bluetooth",  // bluetooth access
	"usb",        // USB device access
	"gpu",        // GPU acceleration
	"background", // run when window is closed
}

var appIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9\-]{0,62}$`)

// AppManifest describes an installable app.
// Stored as app.json in each app's directory under /opt/vulos/apps/<id>/.
//
// Required bundle structure:
//
//	<app-id>/
//	├── app.json       (this manifest — required)
//	├── icon.svg       (app icon — required, path set in IconPath)
//	├── bin/           (executables)
//	│   └── server
//	├── static/        (optional web assets)
//	└── data/          (runtime data, symlinked to ~/.vulos/data/<app-id>)
type AppManifest struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	IconPath    string            `json:"icon_path"`    // relative path to icon file, e.g., "icon.svg"
	Icon        string            `json:"icon"`         // fallback unicode icon for dock/launchpad
	Description string            `json:"description"`
	Version     string            `json:"version"`
	Command     string            `json:"command"`      // relative to app dir: "bin/server" or "python3 server.py"
	Port        int               `json:"port"`         // port the app listens on inside namespace
	Category    string            `json:"category"`     // core, productivity, media, developer, system, network, database
	Keywords    []string          `json:"keywords"`
	Env         map[string]string `json:"env"`          // extra env vars
	Deps        []string          `json:"deps"`         // alpine packages needed
	WorkDir     string            `json:"work_dir"`     // defaults to app directory
	AutoStart   bool              `json:"auto_start"`   // start on boot
	Singleton   bool              `json:"singleton"`    // only one instance allowed
	Permissions []string          `json:"permissions"`  // requested permissions: "network", "filesystem", etc.
	Author      string            `json:"author"`       // app author/publisher
	License     string            `json:"license"`      // SPDX license identifier
	Homepage    string            `json:"homepage"`     // upstream project URL
}

// Validate checks that the manifest has all required fields and conforms
// to the strict bundle structure. appDir is the directory containing app.json.
func (m *AppManifest) Validate(appDir string) error {
	if !appIDPattern.MatchString(m.ID) {
		return fmt.Errorf("invalid app id %q: must be lowercase alphanumeric with hyphens, 1-63 chars", m.ID)
	}
	if m.Name == "" {
		return fmt.Errorf("app %s: name is required", m.ID)
	}
	if m.Version == "" {
		return fmt.Errorf("app %s: version is required", m.ID)
	}
	if m.Command == "" {
		return fmt.Errorf("app %s: command is required", m.ID)
	}
	if m.Port <= 0 || m.Port > 65535 {
		return fmt.Errorf("app %s: port must be between 1 and 65535", m.ID)
	}
	if m.Description == "" {
		return fmt.Errorf("app %s: description is required", m.ID)
	}

	// Validate category
	if m.Category != "" {
		valid := false
		for _, c := range ValidCategories {
			if m.Category == c {
				valid = true
				break
			}
		}
		if !valid {
			return fmt.Errorf("app %s: invalid category %q (valid: %s)", m.ID, m.Category, strings.Join(ValidCategories, ", "))
		}
	}

	// Validate permissions
	for _, p := range m.Permissions {
		valid := false
		for _, vp := range ValidPermissions {
			if p == vp {
				valid = true
				break
			}
		}
		if !valid {
			return fmt.Errorf("app %s: invalid permission %q (valid: %s)", m.ID, p, strings.Join(ValidPermissions, ", "))
		}
	}

	// Command must not escape the app directory
	if strings.Contains(m.Command, "..") {
		return fmt.Errorf("app %s: command must not contain '..'", m.ID)
	}

	// Icon path must exist if specified
	if m.IconPath != "" {
		if strings.Contains(m.IconPath, "..") {
			return fmt.Errorf("app %s: icon_path must not contain '..'", m.ID)
		}
		iconFull := filepath.Join(appDir, m.IconPath)
		if _, err := os.Stat(iconFull); err != nil {
			return fmt.Errorf("app %s: icon_path %q not found", m.ID, m.IconPath)
		}
	}

	return nil
}

// LoadManifest reads an app.json file.
func LoadManifest(path string) (*AppManifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m AppManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// LoadAndValidateManifest reads and validates an app.json file.
func LoadAndValidateManifest(path string) (*AppManifest, error) {
	m, err := LoadManifest(path)
	if err != nil {
		return nil, err
	}
	appDir := filepath.Dir(path)
	if err := m.Validate(appDir); err != nil {
		return nil, err
	}
	if m.WorkDir == "" {
		m.WorkDir = appDir
	}
	return m, nil
}

// ScanApps finds all app.json manifests in a directory.
// Expected layout: appsDir/<app-id>/app.json
func ScanApps(appsDir string) ([]*AppManifest, error) {
	entries, err := os.ReadDir(appsDir)
	if err != nil {
		return nil, err
	}

	var manifests []*AppManifest
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		manifestPath := filepath.Join(appsDir, e.Name(), "app.json")
		m, err := LoadManifest(manifestPath)
		if err != nil {
			continue
		}
		if m.WorkDir == "" {
			m.WorkDir = filepath.Join(appsDir, e.Name())
		}
		manifests = append(manifests, m)
	}
	return manifests, nil
}

// ScanAndValidateApps finds and validates all app.json manifests.
// Returns only apps that pass validation, logging errors for invalid ones.
func ScanAndValidateApps(appsDir string) ([]*AppManifest, []error) {
	entries, err := os.ReadDir(appsDir)
	if err != nil {
		return nil, []error{err}
	}

	var manifests []*AppManifest
	var errs []error
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		manifestPath := filepath.Join(appsDir, e.Name(), "app.json")
		m, err := LoadAndValidateManifest(manifestPath)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", e.Name(), err))
			continue
		}
		manifests = append(manifests, m)
	}
	return manifests, errs
}

// EnvSlice converts the manifest's env map to a slice for exec.
func (m *AppManifest) EnvSlice() []string {
	var env []string
	for k, v := range m.Env {
		env = append(env, k+"="+v)
	}
	return env
}
