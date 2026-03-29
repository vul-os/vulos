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

	// Install Alpine dependencies if specified
	manifest, err := LoadManifest(filepath.Join(appDir, "app.json"))
	if err == nil && len(manifest.Deps) > 0 {
		args := append([]string{"add"}, manifest.Deps...)
		exec.CommandContext(ctx, "apk", args...).Run()
	}

	log.Printf("[appstore] installed %s", entry.ID)
	return nil
}

// Uninstall removes an app.
func (s *AppStore) Uninstall(appID string) error {
	appDir := filepath.Join(s.appsDir, appID)
	if _, err := os.Stat(appDir); os.IsNotExist(err) {
		return fmt.Errorf("app %s not installed", appID)
	}
	if err := os.RemoveAll(appDir); err != nil {
		return fmt.Errorf("remove %s: %w", appID, err)
	}
	log.Printf("[appstore] uninstalled %s", appID)
	return nil
}

// Installed lists all locally installed apps.
func (s *AppStore) Installed() ([]*AppManifest, error) {
	return ScanApps(s.appsDir)
}

// AppDir returns the base directory for apps.
func (s *AppStore) AppDir() string {
	return s.appsDir
}
