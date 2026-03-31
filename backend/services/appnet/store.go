package appnet

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"vulos/backend/services/packages"
	"time"
)

// ValidateInstalled runs validation on all installed app manifests.
func (s *AppStore) ValidateInstalled() ([]*AppManifest, []error) {
	return ScanAndValidateApps(s.appsDir)
}

// StoreEntry is an app listing in the app store.
type StoreEntry struct {
	AppManifest
	DownloadURL string `json:"download_url"`
	Author      string `json:"author"`
	Size        string `json:"size"`
	Stars       int    `json:"stars"`
	Installed   bool   `json:"installed"`
}

// AppStore manages app discovery, install, and removal.
type AppStore struct {
	appsDir      string
	catalogURL   string    // URL to fetch the app catalog (JSON)
	registry     *Registry // local vetted app registry
	registryPath string    // path to registry.json
	client       *http.Client
}

func NewAppStore(appsDir string) *AppStore {
	os.MkdirAll(appsDir, 0755)

	// Load local registry if it exists
	registryPath := filepath.Join(appsDir, "..", "registry.json")
	if p := os.Getenv("VULOS_REGISTRY"); p != "" {
		registryPath = p
	}
	var reg *Registry
	if r, err := LoadRegistry(registryPath); err == nil {
		reg = r
		log.Printf("[appstore] loaded registry with %d apps", len(reg.Apps))
	} else {
		reg = &Registry{Apps: make(map[string]*RegistryEntry)}
	}

	return &AppStore{
		appsDir:      appsDir,
		catalogURL:   os.Getenv("VULOS_APP_CATALOG"),
		registry:     reg,
		registryPath: registryPath,
		client:       &http.Client{Timeout: 30 * time.Second},
	}
}

// Registry returns the loaded registry.
func (s *AppStore) Registry() *Registry {
	return s.registry
}

// InstallFromRegistry installs an app from the vetted registry.
func (s *AppStore) InstallFromRegistry(ctx context.Context, appID, version string) error {
	return InstallFromRegistry(ctx, s.registry, appID, version, s.appsDir)
}

// Catalog fetches the app catalog from the remote store.
func (s *AppStore) Catalog(ctx context.Context) ([]StoreEntry, error) {
	if s.catalogURL == "" {
		return nil, fmt.Errorf("no app catalog configured (set VULOS_APP_CATALOG)")
	}

	req, _ := http.NewRequestWithContext(ctx, "GET", s.catalogURL, nil)
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch catalog: %w", err)
	}
	defer resp.Body.Close()

	var entries []StoreEntry
	if err := json.NewDecoder(resp.Body).Decode(&entries); err != nil {
		return nil, fmt.Errorf("parse catalog: %w", err)
	}

	// Mark installed apps
	for i := range entries {
		if _, err := os.Stat(filepath.Join(s.appsDir, entries[i].ID, "app.json")); err == nil {
			entries[i].Installed = true
		}
	}

	return entries, nil
}

// Install downloads and installs an app from its download URL.
// Expects a tar.gz archive containing app.json + app files.
func (s *AppStore) Install(ctx context.Context, entry StoreEntry) error {
	if entry.DownloadURL == "" {
		return fmt.Errorf("no download URL for %s", entry.ID)
	}

	appDir := filepath.Join(s.appsDir, entry.ID)
	os.MkdirAll(appDir, 0755)

	// Download
	req, _ := http.NewRequestWithContext(ctx, "GET", entry.DownloadURL, nil)
	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("download %s: %w", entry.ID, err)
	}
	defer resp.Body.Close()

	// Save tarball
	tarPath := filepath.Join(appDir, "app.tar.gz")
	f, err := os.Create(tarPath)
	if err != nil {
		return err
	}
	io.Copy(f, resp.Body)
	f.Close()

	// Extract
	cmd := exec.CommandContext(ctx, "tar", "xzf", tarPath, "-C", appDir)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("extract: %s", out)
	}
	os.Remove(tarPath)

	// Install OS dependencies if specified
	manifest, err := LoadManifest(filepath.Join(appDir, "app.json"))
	if err == nil && len(manifest.Deps) > 0 {
		packages.InstallDeps(ctx, manifest.Deps)
	}

	log.Printf("[appstore] installed %s", entry.ID)
	return nil
}

// Uninstall removes an app, its apt packages (for desktop apps), and cleans up.
func (s *AppStore) Uninstall(appID string) error {
	appDir := filepath.Join(s.appsDir, appID)
	if _, err := os.Stat(appDir); os.IsNotExist(err) {
		return fmt.Errorf("app %s not installed", appID)
	}

	log.Printf("[appstore] uninstalling %s from %s", appID, appDir)

	// Read manifest to check app type
	manifest, _ := LoadManifest(filepath.Join(appDir, "app.json"))

	// For desktop apps installed via apt, remove the packages
	if manifest != nil && manifest.Type == "desktop" && s.registry != nil {
		if entry, ok := s.registry.Apps[appID]; ok {
			ver := entry.LatestVersion()
			if recipe := entry.GetRecipe(ver); recipe != nil && recipe.Install != "" {
				pkgs := extractAptPackages(recipe.Install)
				if len(pkgs) > 0 {
					log.Printf("[appstore] apt-get remove for %s: %v", appID, pkgs)
					args := append([]string{"remove", "-y"}, pkgs...)
					cmd := exec.Command("apt-get", args...)
					cmd.Stdout = os.Stdout
					cmd.Stderr = os.Stderr
					if err := cmd.Run(); err != nil {
						log.Printf("[appstore] apt-get remove warning for %s: %v", appID, err)
					}
					exec.Command("apt-get", "autoremove", "-y", "-qq").Run()
				}
			}
		}
	}

	// Remove symlinked data directory
	dataDir := filepath.Join(os.Getenv("HOME"), ".vulos", "data", appID)
	if info, err := os.Lstat(dataDir); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			os.Remove(dataDir)
		} else {
			os.RemoveAll(dataDir)
		}
	}

	// Remove app directory (app.json, icon.svg, bin/, static/, data/)
	if err := os.RemoveAll(appDir); err != nil {
		log.Printf("[appstore] failed to remove app dir %s: %v", appDir, err)
		return fmt.Errorf("remove %s: %w", appID, err)
	}

	log.Printf("[appstore] uninstalled %s", appID)
	return nil
}

// extractAptPackages parses package names from an install command like
// "apt-get install -y --no-install-recommends pkg1 pkg2 && other stuff"
func extractAptPackages(installCmd string) []string {
	var pkgs []string
	parts := strings.Fields(installCmd)
	pastInstall := false
	for _, p := range parts {
		if p == "install" {
			pastInstall = true
			continue
		}
		if !pastInstall {
			continue
		}
		if strings.HasPrefix(p, "-") {
			continue
		}
		if p == "&&" {
			break
		}
		pkgs = append(pkgs, p)
	}
	return pkgs
}

// Installed lists all locally installed apps.
func (s *AppStore) Installed() ([]*AppManifest, error) {
	return ScanApps(s.appsDir)
}

// AppDir returns the base directory for apps.
func (s *AppStore) AppDir() string {
	return s.appsDir
}
