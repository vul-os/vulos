package appnet

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"vulos/backend/internal/datadir"
	"vulos/backend/services/packages"
)

// installIDPattern enforces a strict charset for app IDs used in Install.
// Must start with lowercase alphanumeric, then alphanumeric or hyphen, max 64 chars total.
var installIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

const (
	// maxExtractTotal is the maximum total bytes allowed across all extracted files
	// (zip-bomb / decompression-bomb defense).
	maxExtractTotal int64 = 200 * 1024 * 1024 // 200 MB
	// maxExtractEntry is the maximum size for a single extracted file.
	maxExtractEntry int64 = 100 * 1024 * 1024 // 100 MB
)

// ValidateInstalled runs validation on all installed app manifests.
func (s *AppStore) ValidateInstalled() ([]*AppManifest, []error) {
	return ScanAndValidateApps(s.appsDir)
}

// StoreEntry is an app listing in the app store.
type StoreEntry struct {
	AppManifest
	DownloadURL string `json:"download_url"`
	Checksum    string `json:"checksum"` // optional sha256 hex digest of the download archive
	Author      string `json:"author"`
	Size        string `json:"size"`
	Stars       int    `json:"stars"`
	Installed   bool   `json:"installed"`
}

// AppStore manages app discovery, install, and removal.
type AppStore struct {
	appsDir      string
	bundledDirs  []string
	catalogURL   string    // URL to fetch the app catalog (JSON)
	registry     *Registry // local vetted app registry
	registryPath string    // path to registry.json
	client       *http.Client
	installing   sync.Map // appID → true while install is in progress
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
		bundledDirs:  discoverBundledAppDirs(),
		catalogURL:   os.Getenv("VULOS_APP_CATALOG"),
		registry:     reg,
		registryPath: registryPath,
		client:       &http.Client{Timeout: 30 * time.Second},
	}
}

func discoverBundledAppDirs() []string {
	candidates := []string{}
	if p := os.Getenv("VULOS_BUNDLED_APPS"); p != "" {
		candidates = append(candidates, p)
	}
	candidates = append(candidates, "/opt/vulos/apps")
	if wd, err := os.Getwd(); err == nil {
		candidates = append(candidates,
			filepath.Join(wd, "apps"),
			filepath.Join(wd, "..", "apps"),
		)
	}

	seen := map[string]bool{}
	var dirs []string
	for _, c := range candidates {
		if c == "" {
			continue
		}
		abs, err := filepath.Abs(c)
		if err != nil || seen[abs] {
			continue
		}
		if st, err := os.Stat(abs); err == nil && st.IsDir() {
			seen[abs] = true
			dirs = append(dirs, abs)
		}
	}
	return dirs
}

// Registry returns the loaded registry.
func (s *AppStore) Registry() *Registry {
	return s.registry
}

// InstallFromRegistry installs an app from the vetted registry.
// Prevents duplicate concurrent installs of the same app.
func (s *AppStore) InstallFromRegistry(ctx context.Context, appID, version string) error {
	if _, loaded := s.installing.LoadOrStore(appID, true); loaded {
		return fmt.Errorf("%s is already being installed", appID)
	}
	defer s.installing.Delete(appID)
	return InstallFromRegistry(ctx, s.registry, appID, version, s.appsDir)
}

// IsInstalling returns true if an app install is currently in progress.
func (s *AppStore) IsInstalling(appID string) bool {
	_, ok := s.installing.Load(appID)
	return ok
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
		if s.hasApp(entries[i].ID) {
			entries[i].Installed = true
		}
	}

	return entries, nil
}

// Install downloads and installs an app from its download URL.
// Expects a tar.gz archive containing app.json + app files.
//
// Security measures (M3):
//   - entry.ID is validated against a strict allowlist pattern before use in paths.
//   - The tar.gz is extracted with pure Go (no shell) and each entry is containment-checked.
//   - If entry.Checksum is non-empty the archive sha256 is verified before extraction.
func (s *AppStore) Install(ctx context.Context, entry StoreEntry) error {
	// --- ID validation ---
	if !installIDPattern.MatchString(entry.ID) {
		return fmt.Errorf("invalid app id %q: must match ^[a-z0-9][a-z0-9-]{0,63}$", entry.ID)
	}
	if entry.DownloadURL == "" {
		return fmt.Errorf("no download URL for %s", entry.ID)
	}

	appDir := filepath.Join(s.appsDir, entry.ID)
	if err := os.MkdirAll(appDir, 0755); err != nil {
		return fmt.Errorf("create app dir: %w", err)
	}

	// --- Download to temp file ---
	req, err := http.NewRequestWithContext(ctx, "GET", entry.DownloadURL, nil)
	if err != nil {
		return fmt.Errorf("build request for %s: %w", entry.ID, err)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("download %s: %w", entry.ID, err)
	}
	defer resp.Body.Close()

	// Write to a temp file so we can (optionally) verify the checksum before
	// extraction and avoid holding the whole archive in memory.
	tmp, err := os.CreateTemp("", "vulos-install-*.tar.gz")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) // best-effort cleanup

	if _, err := io.Copy(tmp, resp.Body); err != nil {
		tmp.Close()
		return fmt.Errorf("download %s: %w", entry.ID, err)
	}
	tmp.Close()

	// --- Checksum verification ---
	if entry.Checksum != "" {
		if err := verifySHA256(tmpPath, entry.Checksum); err != nil {
			return fmt.Errorf("checksum mismatch for %s: %w", entry.ID, err)
		}
	}

	// --- Safe tar extraction ---
	if err := safeExtractTarGz(tmpPath, appDir); err != nil {
		return fmt.Errorf("extract %s: %w", entry.ID, err)
	}

	// --- Install OS dependencies if specified ---
	manifest, err := LoadManifest(filepath.Join(appDir, "app.json"))
	if err == nil && len(manifest.Deps) > 0 {
		packages.InstallDeps(ctx, manifest.Deps)
	}

	log.Printf("[appstore] installed %s", entry.ID)
	return nil
}

// verifySHA256 reads the file at path and compares its sha256 hex digest against want.
func verifySHA256(path, want string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	got := hex.EncodeToString(h.Sum(nil))
	if !strings.EqualFold(got, want) {
		return fmt.Errorf("got %s, want %s", got, want)
	}
	return nil
}

// safeExtractTarGz extracts a .tar.gz archive into destDir with full containment checks.
//
//   - Rejects absolute paths.
//   - Rejects any path component equal to "..".
//   - Realpath-contains every output path inside destDir.
//   - Rejects symlinks.
//   - Enforces per-entry (maxExtractEntry) and total (maxExtractTotal) size caps.
func safeExtractTarGz(archivePath, destDir string) error {
	// Resolve the canonical destination so we can do prefix-containment checks.
	absDestDir, err := filepath.Abs(destDir)
	if err != nil {
		return fmt.Errorf("abs destDir: %w", err)
	}
	// Ensure trailing separator for reliable prefix check.
	containPrefix := absDestDir + string(os.PathSeparator)

	f, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer f.Close()

	gzr, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("gzip: %w", err)
	}
	defer gzr.Close()

	tr := tar.NewReader(gzr)

	var totalExtracted int64

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read tar: %w", err)
		}

		name := hdr.Name

		// --- Reject absolute paths ---
		if strings.HasPrefix(name, "/") {
			return fmt.Errorf("tar entry has absolute path: %q", name)
		}

		// --- Reject ".." in any path component ---
		clean := filepath.Clean(name)
		for _, part := range strings.Split(clean, string(os.PathSeparator)) {
			if part == ".." {
				return fmt.Errorf("tar entry path traversal rejected: %q", name)
			}
		}

		// --- Build target path and realpath-contain ---
		targetPath := filepath.Join(absDestDir, clean)
		// targetPath must be strictly inside absDestDir (not equal to it for files).
		if targetPath != absDestDir && !strings.HasPrefix(targetPath, containPrefix) {
			return fmt.Errorf("tar entry escapes destination: %q → %q", name, targetPath)
		}

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(targetPath, 0755); err != nil {
				return fmt.Errorf("mkdir %q: %w", targetPath, err)
			}

		case tar.TypeReg, tar.TypeRegA:
			// --- Per-entry size cap ---
			if hdr.Size > maxExtractEntry {
				return fmt.Errorf("tar entry %q too large (%d bytes, max %d)", name, hdr.Size, maxExtractEntry)
			}

			// Ensure parent directory exists.
			if err := os.MkdirAll(filepath.Dir(targetPath), 0755); err != nil {
				return fmt.Errorf("mkdir parent for %q: %w", targetPath, err)
			}

			out, err := os.OpenFile(targetPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, hdr.FileInfo().Mode()&0755)
			if err != nil {
				return fmt.Errorf("create %q: %w", targetPath, err)
			}

			// Use a LimitReader to enforce per-entry cap during actual copy
			// (protects against header.Size lying in malicious archives).
			limited := &io.LimitedReader{R: tr, N: maxExtractEntry + 1}
			written, copyErr := io.Copy(out, limited)
			out.Close()
			if copyErr != nil {
				return fmt.Errorf("write %q: %w", targetPath, copyErr)
			}
			if written > maxExtractEntry {
				os.Remove(targetPath)
				return fmt.Errorf("tar entry %q exceeded size cap during extraction", name)
			}

			// --- Total size cap (zip-bomb defense) ---
			totalExtracted += written
			if totalExtracted > maxExtractTotal {
				os.Remove(targetPath)
				return fmt.Errorf("tar archive total extraction size exceeded %d bytes", maxExtractTotal)
			}

		case tar.TypeSymlink, tar.TypeLink:
			// Symlinks and hardlinks are rejected outright to prevent escape via
			// link targets that point outside the extraction root.
			return fmt.Errorf("tar entry %q is a symlink/hardlink — rejected for security", name)

		default:
			// Device nodes, fifos, etc. are also rejected.
			return fmt.Errorf("tar entry %q has unsupported type %d — rejected", name, hdr.Typeflag)
		}
	}

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

	// Remove the actual app: Flatpak or apt
	if s.registry != nil {
		if entry, ok := s.registry.Apps[appID]; ok {
			ver := entry.LatestVersion()
			if recipe := entry.GetRecipe(ver); recipe != nil {
				// Flatpak app
				if recipe.FlatpakID != "" {
					log.Printf("[appstore] flatpak uninstall for %s: %s", appID, recipe.FlatpakID)
					if err := FlatpakUninstall(context.Background(), recipe.FlatpakID); err != nil {
						log.Printf("[appstore] flatpak uninstall warning for %s: %v", appID, err)
					}
				} else if manifest != nil && manifest.Type == "desktop" && recipe.Install != "" {
					// Apt-installed desktop app
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
	}

	// Remove symlinked data directory
	dataDir := datadir.Join("data", appID)
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
	merged := map[string]*AppManifest{}
	order := []string{}
	add := func(dir string) error {
		apps, err := ScanApps(dir)
		if err != nil {
			return err
		}
		for _, app := range apps {
			if app == nil || app.ID == "" {
				continue
			}
			if _, ok := merged[app.ID]; !ok {
				order = append(order, app.ID)
			}
			merged[app.ID] = app
		}
		return nil
	}

	for _, dir := range s.bundledDirs {
		_ = add(dir)
	}
	if err := add(s.appsDir); err != nil && len(merged) == 0 {
		return nil, err
	}

	apps := make([]*AppManifest, 0, len(order))
	for _, id := range order {
		apps = append(apps, merged[id])
	}
	return apps, nil
}

// AppDir returns the base directory for apps.
func (s *AppStore) AppDir() string {
	return s.appsDir
}

func (s *AppStore) hasApp(appID string) bool {
	if _, err := os.Stat(filepath.Join(s.appsDir, appID, "app.json")); err == nil {
		return true
	}
	for _, dir := range s.bundledDirs {
		if _, err := os.Stat(filepath.Join(dir, appID, "app.json")); err == nil {
			return true
		}
	}
	return false
}

// GetManifest loads and validates the manifest for an installed app by ID.
// Returns an error if the app is not installed or the manifest is invalid.
func (s *AppStore) GetManifest(appID string) (*AppManifest, error) {
	manifestPath := filepath.Join(s.appsDir, appID, "app.json")
	return LoadAndValidateManifest(manifestPath)
}
