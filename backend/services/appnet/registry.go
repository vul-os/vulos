package appnet

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// Registry holds the catalog of vetted apps with versioned install recipes.
// The registry defines *what* can be installed and *how* — the AppStore handles
// the actual lifecycle (download, install, uninstall).
//
// Registry file format (registry.json):
//
//	{
//	  "apps": {
//	    "postgres": {
//	      "name": "PostgreSQL",
//	      "vetted": true,
//	      "description": "Relational database",
//	      "category": "database",
//	      "author": "PostgreSQL Global Development Group",
//	      "homepage": "https://postgresql.org",
//	      "versions": {
//	        "16": { "install": "apk add postgresql16", ... },
//	        "15": { "install": "apk add postgresql15", ... }
//	      }
//	    }
//	  }
//	}
type Registry struct {
	Apps map[string]*RegistryEntry `json:"apps"`
}

// RegistryEntry is a single app in the registry.
type RegistryEntry struct {
	Name        string                    `json:"name"`
	Vetted      bool                      `json:"vetted"`       // true = reviewed and approved by Vula OS team
	Type        string                    `json:"type"`         // "web" (serves HTTP, accessible via gateway) or "service" (background daemon, no web UI)
	Description string                    `json:"description"`
	Category    string                    `json:"category"`
	Author      string                    `json:"author"`
	Homepage    string                    `json:"homepage"`
	Icon        string                    `json:"icon"`         // unicode fallback icon
	IconURL     string                    `json:"icon_url"`     // URL to download icon from
	Keywords    []string                  `json:"keywords"`
	License     string                    `json:"license"`
	Versions    map[string]*VersionRecipe `json:"versions"`
}

// VersionRecipe defines how to install and run a specific version of an app.
type VersionRecipe struct {
	Install     string            `json:"install"`      // shell command to install (e.g., "apk add postgresql16")
	Command     string            `json:"command"`      // how to run it (e.g., "bin/postgres -D data/")
	Port        int               `json:"port"`         // default port the app listens on
	PostInstall string            `json:"post_install"` // one-time setup command (e.g., "bin/initdb -D data/")
	Deps        []string          `json:"deps"`         // additional alpine package dependencies
	Env         map[string]string `json:"env"`          // default environment variables
	Permissions []string          `json:"permissions"`  // required permissions
	Checksum    string            `json:"checksum"`     // sha256 checksum of download (if applicable)
	Singleton   bool              `json:"singleton"`    // only one instance allowed
	AutoStart   bool              `json:"auto_start"`   // start on boot
}

// LatestVersion returns the highest version string for an entry.
// Uses simple lexicographic sorting — versions should use sortable format (e.g., "16.3").
func (e *RegistryEntry) LatestVersion() string {
	if len(e.Versions) == 0 {
		return ""
	}
	versions := make([]string, 0, len(e.Versions))
	for v := range e.Versions {
		versions = append(versions, v)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(versions)))
	return versions[0]
}

// GetRecipe returns the recipe for a specific version, or nil.
func (e *RegistryEntry) GetRecipe(version string) *VersionRecipe {
	if version == "" || version == "latest" {
		version = e.LatestVersion()
	}
	return e.Versions[version]
}

// LoadRegistry reads a registry.json file.
func LoadRegistry(path string) (*Registry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var r Registry
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, err
	}
	if r.Apps == nil {
		r.Apps = make(map[string]*RegistryEntry)
	}
	return &r, nil
}

// SaveRegistry writes a registry.json file.
func SaveRegistry(path string, r *Registry) error {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

// InstallFromRegistry installs an app from the registry into appsDir.
// It runs the install command, generates a validated app.json manifest,
// downloads the icon, and runs post_install if present.
func InstallFromRegistry(ctx context.Context, reg *Registry, appID, version, appsDir string) error {
	entry, ok := reg.Apps[appID]
	if !ok {
		return fmt.Errorf("app %q not found in registry", appID)
	}

	if version == "" || version == "latest" {
		version = entry.LatestVersion()
	}
	recipe, ok := entry.Versions[version]
	if !ok {
		return fmt.Errorf("version %q not found for app %q (available: %s)",
			version, appID, strings.Join(availableVersions(entry), ", "))
	}

	appDir := filepath.Join(appsDir, appID)

	// Create strict directory structure
	for _, dir := range []string{"bin", "static", "data"} {
		os.MkdirAll(filepath.Join(appDir, dir), 0755)
	}

	// Run install command
	if recipe.Install != "" {
		log.Printf("[registry] installing %s@%s: %s", appID, version, recipe.Install)
		cmd := exec.CommandContext(ctx, "sh", "-c", recipe.Install)
		cmd.Dir = appDir
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		cmd.Env = append(os.Environ(), fmt.Sprintf("APP_DIR=%s", appDir))
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("install command failed: %w", err)
		}
	}

	// Install additional deps
	if len(recipe.Deps) > 0 {
		args := append([]string{"add", "--no-cache"}, recipe.Deps...)
		exec.CommandContext(ctx, "apk", args...).Run()
	}

	// Generate the app.json manifest
	manifest := &AppManifest{
		ID:          appID,
		Name:        entry.Name,
		Icon:        entry.Icon,
		IconPath:    "icon.svg",
		Description: entry.Description,
		Version:     version,
		Command:     recipe.Command,
		Port:        recipe.Port,
		Category:    entry.Category,
		Keywords:    entry.Keywords,
		Env:         recipe.Env,
		Deps:        recipe.Deps,
		WorkDir:     appDir,
		AutoStart:   recipe.AutoStart,
		Singleton:   recipe.Singleton,
		Permissions: recipe.Permissions,
		Author:      entry.Author,
		License:     entry.License,
		Homepage:    entry.Homepage,
	}

	// Write manifest
	manifestData, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal manifest: %w", err)
	}
	if err := os.WriteFile(filepath.Join(appDir, "app.json"), manifestData, 0644); err != nil {
		return fmt.Errorf("write manifest: %w", err)
	}

	// Create a placeholder icon if none exists
	iconPath := filepath.Join(appDir, "icon.svg")
	if _, err := os.Stat(iconPath); os.IsNotExist(err) {
		placeholderIcon := generatePlaceholderIcon(entry.Name, entry.Icon)
		os.WriteFile(iconPath, []byte(placeholderIcon), 0644)
	}

	// Run post-install command
	if recipe.PostInstall != "" {
		log.Printf("[registry] post-install %s: %s", appID, recipe.PostInstall)
		cmd := exec.CommandContext(ctx, "sh", "-c", recipe.PostInstall)
		cmd.Dir = appDir
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		cmd.Env = append(os.Environ(),
			fmt.Sprintf("APP_DIR=%s", appDir),
			fmt.Sprintf("DATA_DIR=%s", filepath.Join(appDir, "data")),
		)
		if err := cmd.Run(); err != nil {
			log.Printf("[registry] post-install warning for %s: %v", appID, err)
		}
	}

	// Symlink data dir to user data directory
	userDataDir := filepath.Join(os.Getenv("HOME"), ".vulos", "data", appID)
	appDataDir := filepath.Join(appDir, "data")
	if _, err := os.Stat(userDataDir); os.IsNotExist(err) {
		os.MkdirAll(filepath.Dir(userDataDir), 0755)
		// Only symlink if the data dir is empty (fresh install)
		entries, _ := os.ReadDir(appDataDir)
		if len(entries) == 0 {
			os.Remove(appDataDir)
			os.Symlink(userDataDir, appDataDir)
			os.MkdirAll(userDataDir, 0755)
		}
	}

	log.Printf("[registry] installed %s@%s → %s", appID, version, appDir)
	return nil
}

// RegistryList returns a flat list of all registry entries with their IDs,
// suitable for API responses.
type RegistryListEntry struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Type        string   `json:"type"` // "web" or "service"
	Description string   `json:"description"`
	Category    string   `json:"category"`
	Author      string   `json:"author"`
	Icon        string   `json:"icon"`
	Vetted      bool     `json:"vetted"`
	Versions    []string `json:"versions"`
	Latest      string   `json:"latest"`
	Installed   bool     `json:"installed"`
	Homepage    string   `json:"homepage"`
	License     string   `json:"license"`
}

// ListEntries returns a flat list of all registry apps, marking which are installed.
func (r *Registry) ListEntries(appsDir string) []RegistryListEntry {
	var entries []RegistryListEntry
	for id, entry := range r.Apps {
		versions := availableVersions(entry)
		installed := false
		if _, err := os.Stat(filepath.Join(appsDir, id, "app.json")); err == nil {
			installed = true
		}
		appType := entry.Type
		if appType == "" {
			appType = "web" // default to web
		}
		entries = append(entries, RegistryListEntry{
			ID:          id,
			Name:        entry.Name,
			Type:        appType,
			Description: entry.Description,
			Category:    entry.Category,
			Author:      entry.Author,
			Icon:        entry.Icon,
			Vetted:      entry.Vetted,
			Versions:    versions,
			Latest:      entry.LatestVersion(),
			Installed:   installed,
			Homepage:    entry.Homepage,
			License:     entry.License,
		})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	return entries
}

func availableVersions(entry *RegistryEntry) []string {
	versions := make([]string, 0, len(entry.Versions))
	for v := range entry.Versions {
		versions = append(versions, v)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(versions)))
	return versions
}

// generatePlaceholderIcon creates a simple SVG icon with the app's unicode icon or first letter.
func generatePlaceholderIcon(name, icon string) string {
	display := icon
	if display == "" && name != "" {
		display = strings.ToUpper(name[:1])
	}
	if display == "" {
		display = "?"
	}
	return fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64">
  <rect width="64" height="64" rx="14" fill="#1a1a1a"/>
  <text x="32" y="40" text-anchor="middle" font-size="28" fill="#e5e5e5" font-family="system-ui">%s</text>
</svg>`, display)
}
