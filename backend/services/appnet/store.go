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
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"vulos/backend/internal/datadir"
	"vulos/backend/internal/safedial"
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
	Checksum    string `json:"checksum"` // REQUIRED sha256 hex digest of the download archive (CATALOG-01)
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
		client:       newSSRFGuardedStoreClient(),
	}
}

// newSSRFGuardedStoreClient builds the *http.Client used for both the
// catalog fetch (s.catalogURL, operator-configured via VULOS_APP_CATALOG)
// and, more importantly, Install's download of entry.DownloadURL — a field
// decoded directly from an admin's POST /api/store/install request body
// (cmd/server/main.go), with no registry signature and no mandatory
// checksum. Unlike InstallFromRegistry (registry.go), which only ever
// downloads a URL from an Ed25519-signed, vetted registry entry, this path
// previously had NO SSRF guard at all: an admin session (or a CSRF'd one)
// could point Install at http://169.254.169.254/..., a loopback admin API,
// or any LAN host, and the box would fetch, tar-extract, and install
// whatever came back. The dial-time Control hook re-validates the resolved
// IP at every connect(2) (including across a redirect), closing the
// DNS-rebind gap a registration-time-only check would leave open.
func newSSRFGuardedStoreClient() *http.Client {
	return &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			DialContext: (&net.Dialer{
				Timeout: 30 * time.Second,
				Control: safedial.ControlFunc(false),
			}).DialContext,
		},
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
//   - The archive sha256 is verified before extraction, and a MISSING checksum
//     is refused rather than skipped (CATALOG-01).
func (s *AppStore) Install(ctx context.Context, entry StoreEntry) error {
	// --- ID validation ---
	if !installIDPattern.MatchString(entry.ID) {
		return fmt.Errorf("invalid app id %q: must match ^[a-z0-9][a-z0-9-]{0,63}$", entry.ID)
	}
	if entry.DownloadURL == "" {
		return fmt.Errorf("no download URL for %s", entry.ID)
	}
	// SSRF guard (registration-time half; newSSRFGuardedStoreClient's dial-time
	// Control hook is the second, DNS-rebind-safe half). entry.DownloadURL is
	// attacker-shaped input (an admin-supplied request body, not a vetted
	// registry entry — see newSSRFGuardedStoreClient's doc comment), so it is
	// screened exactly like any other user-supplied outbound URL in this OS.
	if err := validateStoreDownloadURL(entry.DownloadURL); err != nil {
		return fmt.Errorf("refusing download URL for %s: %w", entry.ID, err)
	}

	// CATALOG-01: a checksum is MANDATORY here, exactly as it is on every
	// artefact in the registry (roadmap/INSTALL-METHODOLOGY.md §4.3).
	//
	// This is the weakest install path in the OS and it was the only one with an
	// optional integrity check: `entry` is decoded straight from a request body,
	// it carries no publisher signature, and the checksum below used to be
	// verified only `if entry.Checksum != ""` — so omitting the field skipped
	// verification entirely rather than failing. An admin (or a CSRF'd session)
	// could install an unverified tarball from any public host.
	//
	// Ordered AFTER the SSRF guard so that guard still answers for the inputs it
	// was written for; a URL that is refused for pointing at 169.254.169.254
	// must keep saying so rather than complaining about a missing digest.
	//
	// Note what this does NOT do: it does not make this path equivalent to the
	// registry one. There is still no signature, so the checksum only proves the
	// bytes match what the REQUEST asked for. The methodology's answer is that
	// signed registry entries are the way apps are installed; this endpoint is
	// the vestigial external-catalog path (VULOS_APP_CATALOG) and the shipped
	// front end never calls it — it uses POST /api/store/registry/install.
	if strings.TrimSpace(entry.Checksum) == "" {
		return fmt.Errorf("refusing to install %s: the catalog entry has no checksum, and an "+
			"unsigned download with no integrity check is the one shape this OS does not install "+
			"(CATALOG-01, roadmap/INSTALL-METHODOLOGY.md)", entry.ID)
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
	// Unconditional: CATALOG-01 above has already refused an empty one, so this
	// runs for every download rather than for the ones that opted in.
	if err := verifySHA256(tmpPath, entry.Checksum); err != nil {
		return fmt.Errorf("checksum mismatch for %s: %w", entry.ID, err)
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

// validateStoreDownloadURL rejects a download URL whose scheme is not
// http(s) or whose host is (or resolves to) a loopback/private/link-local/
// metadata address, before Install ever dials it. This is the pre-dial half
// of the SSRF guard; newSSRFGuardedStoreClient's Control hook re-validates
// the actually-resolved IP at connect(2) time (DNS-rebind + redirect safe).
func validateStoreDownloadURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("scheme must be http or https, got %q", u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("URL has no host")
	}
	if _, err := safedial.ValidateHost(host, false); err != nil {
		return err
	}
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

		// TypeRegA (old-GNU '\x00') is not listed: archive/tar normalizes it to
		// TypeReg when reading, so an old-GNU tarball still lands here. The
		// security test builds one with TypeRegA to prove that normalization.
		case tar.TypeReg:
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

// ── SYNC-APPS-01: the box's half of desire → realisation ─────────────────────
//
// These three methods make *AppStore satisfy multiinstance.Realiser, which is
// how the fleet's DESIRED set (one entry per app, what the user asked for)
// becomes apps on THIS box's disk.
//
// The interface is satisfied STRUCTURALLY. multiinstance declares it in
// primitive types and neither package imports the other, so there is no adapter
// type to drift and no import edge between the installer and the replicator.
// That is deliberate: an adapter is where a signature change becomes a silent
// behaviour change, and this is the seam whose absence was the whole defect —
// AppStore.Install has always been a MkdirAll that writes no row anywhere, so
// nothing produced the events the replicator existed to carry.
//
// The direction is one-way and the type says so. Nothing here can tell the
// replicator what the fleet wants; a box can only be asked what it HAS and told
// to change it. A box that cannot install something reports a reason and the
// desire stands.

// RealisedVersions reports what is actually installed on this box, appID →
// version.
//
// It reads the FILESYSTEM (Installed()), which is this OS's definition of
// installed, and not the app_registry table. Reconciling against the table would
// mean reconciling against a report of the disk rather than the disk: a row that
// went stale — an install that half-failed, a directory removed by hand — would
// make the box believe its own bookkeeping and never repair itself. The whole
// point of a realised set is that it is checkable against ground truth.
//
// Bundled apps (/opt/vulos/apps) are included, because they ARE installed here:
// omitting them would make every box try to install what it already ships.
//
// SYNC-APPS-02 — what "ground truth" means on a volatile root. The paragraph
// above is right that the filesystem is the truth, and wrong about how long that
// truth lasts. On the three overlay boot paths (live-USB, live-ESP,
// netboot-installed) the whole of / is a squashfs lower plus a tmpfs upper in
// RAM (scripts/initramfs/vulos-live), and appsDir is datadir.Join("apps") =
// /root/.vulos/apps, inside it. There, this method reports the truth about THIS
// BOOT, which is a different claim from the truth about this box: after a reboot
// it reads an empty directory for apps the box really did install.
//
// That is still the right answer to give — a box must not claim to have an app
// it cannot launch — but it is not, on its own, enough for the reconciler to act
// on, which is why StorageVolatility exists next to it and why PlanReconcileFor
// classifies an absence rather than just counting it.
func (s *AppStore) RealisedVersions() (map[string]string, error) {
	apps, err := s.Installed()
	if err != nil {
		return nil, fmt.Errorf("appstore: RealisedVersions: %w", err)
	}
	out := make(map[string]string, len(apps))
	for _, a := range apps {
		if a == nil || a.ID == "" {
			continue
		}
		out[a.ID] = a.Version
	}
	return out, nil
}

// mountsPath is the kernel's mount table. A variable so the classifier can be
// tested against captured tables instead of the host's own.
var mountsPath = "/proc/self/mounts"

// volatileFSTypes are the filesystems that exist only in RAM. A file written to
// one of these is gone at the next boot, with no failure at the time of writing.
var volatileFSTypes = map[string]bool{"tmpfs": true, "ramfs": true}

// StorageVolatility reports whether the directory this box installs apps into
// lives on storage that does NOT survive a reboot, with a human-readable detail
// naming the mount it decided on.
//
// This is the second input the reconciler needs and the one nothing produced
// before. RealisedVersions can say "the app is not here"; only this can say
// whether "not here" means "never arrived" or "arrived and evaporated with the
// tmpfs". Without it a fleet-driven reconciler on a live-USB box re-downloads
// every desired app on every boot, forever, and the only symptom is a slow boot.
//
// It is measured, not inferred, and measured from the kernel's own mount table
// rather than from the boot mode: an operator who points VULOS_DATA_DIR at a
// mounted volume, or an initramfs that later binds the data dir onto real
// storage, flips this to durable with no code change here and no policy to
// update. That is deliberate — the day the app dir becomes persistent, every
// behaviour keyed on this answer stops firing by itself rather than becoming
// wrong.
//
// Unknown is reported as NOT volatile (false, ""). There is no mount table on
// darwin (developer machines) and there may be none in a stripped container, and
// claiming "your storage is volatile" without evidence would put a false reason
// on a replicated row that a user reads at another box.
func (s *AppStore) StorageVolatility() (bool, string) {
	data, err := os.ReadFile(mountsPath)
	if err != nil {
		return false, ""
	}
	return classifyMountVolatility(string(data), s.appsDir)
}

// classifyMountVolatility decides whether path sits on RAM-backed storage, given
// the contents of a /proc/self/mounts-style table.
//
// Two hops, because one is not enough on this OS. The mount covering
// /root/.vulos/apps on every overlay boot is "/" with fstype `overlay`, and
// overlay is neither durable nor volatile in itself: it inherits the answer from
// wherever its WRITABLE upper layer lives. vulos-live puts that upper in a tmpfs
// at /run/vulos/rw, so the classifier follows upperdir= and asks the same
// question of it. Stopping at "overlay" would report every overlay box as
// durable — which is the mistake that makes this whole defect invisible.
func classifyMountVolatility(mounts, path string) (bool, string) {
	return classifyMountVolatilityDepth(mounts, path, 0)
}

func classifyMountVolatilityDepth(mounts, path string, depth int) (bool, string) {
	if depth > 4 {
		// An overlay stacked on an overlay stacked on … — refuse to loop and
		// refuse to guess.
		return false, ""
	}
	fstype, mountPoint, opts := mountFor(mounts, path)
	switch {
	case fstype == "":
		return false, ""
	case volatileFSTypes[fstype]:
		return true, fmt.Sprintf("%s at %s (RAM-backed)", fstype, mountPoint)
	case fstype == "overlay":
		upper := mountOption(opts, "upperdir")
		if upper == "" {
			// A lower-only overlay is read-only: an install cannot land at all,
			// which is not the same failure but is equally not durable.
			return true, fmt.Sprintf("overlay at %s with no writable upper layer", mountPoint)
		}
		volatile, detail := classifyMountVolatilityDepth(mounts, upper, depth+1)
		if !volatile {
			return false, ""
		}
		return true, fmt.Sprintf("overlay at %s whose upper layer %s is %s", mountPoint, upper, detail)
	default:
		return false, ""
	}
}

// mountFor returns the fstype, mount point and options of the most specific
// mount covering path. Longest-prefix, because "/" covers everything and the
// answer for /root/.vulos/apps is whichever mount is nearest it.
func mountFor(mounts, path string) (fstype, mountPoint, opts string) {
	path = filepath.Clean(path)
	best := -1
	for _, line := range strings.Split(mounts, "\n") {
		f := strings.Fields(line)
		if len(f) < 4 {
			continue
		}
		mp := unescapeMountField(f[1])
		if !pathWithin(path, mp) {
			continue
		}
		if len(mp) > best {
			best, fstype, mountPoint, opts = len(mp), f[2], mp, f[3]
		}
	}
	return fstype, mountPoint, opts
}

// pathWithin reports whether path is mp or lives beneath it. Prefix comparison
// alone would put /rootfs-backup under /root.
func pathWithin(path, mp string) bool {
	mp = filepath.Clean(mp)
	if mp == "/" || mp == path {
		return true
	}
	return strings.HasPrefix(path, mp+string(filepath.Separator))
}

// mountOption returns the value of key= in a comma-separated mount option list.
func mountOption(opts, key string) string {
	for _, o := range strings.Split(opts, ",") {
		if v, ok := strings.CutPrefix(o, key+"="); ok {
			return unescapeMountField(v)
		}
	}
	return ""
}

// unescapeMountField undoes the octal escaping the kernel applies to space, tab,
// newline and backslash in mount table paths.
func unescapeMountField(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) &&
			s[i+1] >= '0' && s[i+1] <= '7' &&
			s[i+2] >= '0' && s[i+2] <= '7' &&
			s[i+3] >= '0' && s[i+3] <= '7' {
			b.WriteByte((s[i+1]-'0')<<6 | (s[i+2]-'0')<<3 | (s[i+3] - '0'))
			i += 3
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// Realise installs appID at version ("" = latest) on this box.
//
// It routes through InstallFromRegistry, NOT Install. That is a security
// decision and not an implementation detail: Install takes a DownloadURL from an
// admin's request body with no registry signature and no mandatory checksum (see
// newSSRFGuardedStoreClient), so wiring a REPLICATED intent to it would let any
// box that can write the fleet desired set name an arbitrary URL for every other
// box to fetch and extract. InstallFromRegistry only ever downloads a URL from
// an Ed25519-signed, vetted registry entry, verifies the publisher signature
// before touching the filesystem, and refuses a disabled entry.
//
// It is also where the architecture check already lives, which is why arch needs
// no special handling anywhere in the sync layer: an arm64 box asked to install
// an amd64-only app gets back "requires amd64; this box is arm64" before
// anything is downloaded, and that string travels to the fleet as the reason.
func (s *AppStore) Realise(ctx context.Context, appID, version string) error {
	return s.InstallFromRegistry(ctx, appID, version)
}

// Unrealise removes appID from this box. Errors — including "app X not
// installed" for an app that only ever shipped bundled in the image — are
// reported as realisation failures rather than swallowed, because a removal that
// silently did not happen is exactly the class of silence this split exists to
// end.
func (s *AppStore) Unrealise(_ context.Context, appID string) error {
	return s.Uninstall(appID)
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

// GetManifest loads and validates the manifest for an app by ID, searching the
// SAME set of directories the box considers "installed": the local install dir
// first, then the bundled dirs the image ships (/opt/vulos/apps).
//
// The bundled fallback is not optional. POST /api/apps/launch resolves an app's
// command through this method, and it used to read s.appsDir
// ($HOME/.vulos/apps) alone. Bundled apps are never "installed" into that
// directory — they ship in /opt/vulos/apps — so launching ANY app the OS ships
// failed with "app not found or invalid manifest", no namespace was registered,
// and the gateway then answered {"error":"app not running"}.
//
// Installed() and hasApp() already merged both locations; this method was the
// one that did not, which is why the box could LIST every bundled app while
// being unable to START any of them.
//
// Precedence matches Installed(): a store-installed app of the same ID shadows
// the bundled copy, so an update is never ignored in favour of the shipped
// version. Validation applies identically on both paths — being bundled does
// not exempt a manifest from Validate.
func (s *AppStore) GetManifest(appID string) (*AppManifest, error) {
	manifestPath := filepath.Join(s.appsDir, appID, "app.json")
	m, err := LoadAndValidateManifest(manifestPath)
	if err == nil {
		return m, nil
	}
	// Only fall back when the app is genuinely absent here. An app that IS
	// installed locally but has a broken manifest must keep reporting its own
	// error rather than silently resolving to the bundled copy.
	if !os.IsNotExist(err) {
		return nil, err
	}
	for _, dir := range s.bundledDirs {
		if bm, bErr := LoadAndValidateManifest(filepath.Join(dir, appID, "app.json")); bErr == nil {
			return bm, nil
		} else if !os.IsNotExist(bErr) {
			// A present-but-invalid bundled manifest is the most useful error
			// to surface: the app exists on disk and still cannot start.
			err = bErr
		}
	}
	return nil, err
}
