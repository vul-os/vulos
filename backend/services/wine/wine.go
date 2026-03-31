// Package wine manages Wine prefixes for running Windows applications.
// Each user gets isolated prefixes under ~/.vulos/wine/<prefix-name>/.
// DXVK is auto-installed when a GPU with Vulkan support is detected.
package wine

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"vulos/backend/services/gpu"
)

var validPrefixName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,62}$`)

// Prefix is a Wine prefix (a self-contained Windows environment).
type Prefix struct {
	Name      string `json:"name"`
	Path      string `json:"path"`
	Arch      string `json:"arch"`      // win64 or win32
	DXVK      bool   `json:"dxvk"`      // DXVK installed
	Created   int64  `json:"created"`   // unix timestamp
	SizeMB    int    `json:"size_mb"`   // approximate disk usage
	WindowsVer string `json:"windows_ver"` // e.g. "win10", "win7"
}

// Service manages Wine prefixes.
type Service struct {
	mu      sync.Mutex
	baseDir string // e.g. /root/.vulos/wine
	gpuInfo gpu.Info
}

// New creates a Wine service rooted at baseDir (e.g. ~/.vulos/wine).
func New(baseDir string) *Service {
	os.MkdirAll(baseDir, 0755)
	return &Service{
		baseDir: baseDir,
		gpuInfo: gpu.Detect(),
	}
}

// List returns all prefixes.
func (s *Service) List() []Prefix {
	s.mu.Lock()
	defer s.mu.Unlock()

	entries, err := os.ReadDir(s.baseDir)
	if err != nil {
		return nil
	}
	var prefixes []Prefix
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		pfxDir := filepath.Join(s.baseDir, e.Name())
		// Must contain system.reg to be a valid Wine prefix
		if _, err := os.Stat(filepath.Join(pfxDir, "system.reg")); err != nil {
			continue
		}
		p := Prefix{
			Name: e.Name(),
			Path: pfxDir,
			Arch: s.readArch(pfxDir),
		}
		if info, err := e.Info(); err == nil {
			p.Created = info.ModTime().Unix()
		}
		p.DXVK = s.hasDXVK(pfxDir)
		p.SizeMB = dirSizeMB(pfxDir)
		p.WindowsVer = s.readWindowsVersion(pfxDir)
		prefixes = append(prefixes, p)
	}
	return prefixes
}

// Create initializes a new Wine prefix.
func (s *Service) Create(name, arch string) (*Prefix, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if name == "" {
		return nil, fmt.Errorf("prefix name required")
	}
	if !validPrefixName.MatchString(name) {
		return nil, fmt.Errorf("invalid prefix name: use alphanumeric, dash, underscore only")
	}
	if arch == "" {
		arch = "win64"
	}
	if arch != "win64" && arch != "win32" {
		return nil, fmt.Errorf("arch must be win64 or win32")
	}

	pfxDir := filepath.Join(s.baseDir, name)
	if _, err := os.Stat(pfxDir); err == nil {
		return nil, fmt.Errorf("prefix %q already exists", name)
	}

	winearch := "win64"
	if arch == "win32" {
		winearch = "win32"
	}

	// Create the prefix via wineboot
	cmd := exec.Command("wineboot", "--init")
	cmd.Env = append(os.Environ(),
		"WINEPREFIX="+pfxDir,
		"WINEARCH="+winearch,
		"WINEDEBUG=-all",
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		os.RemoveAll(pfxDir)
		return nil, fmt.Errorf("wineboot init: %w", err)
	}

	// Set Windows version to 10 by default
	s.setWindowsVersion(pfxDir, "win10")

	// Auto-install DXVK if GPU supports Vulkan (Tier 1+ = VA-API or NVENC)
	dxvk := false
	if s.gpuInfo.Tier >= gpu.TierVAAPI {
		if err := s.installDXVK(pfxDir); err != nil {
			log.Printf("[wine] DXVK install warning for %q: %v", name, err)
		} else {
			dxvk = true
		}
	}

	p := &Prefix{
		Name:       name,
		Path:       pfxDir,
		Arch:       arch,
		DXVK:       dxvk,
		Created:    time.Now().Unix(),
		WindowsVer: "win10",
	}
	log.Printf("[wine] created prefix %q (arch=%s, dxvk=%v)", name, arch, dxvk)
	return p, nil
}

// Delete removes a Wine prefix entirely.
func (s *Service) Delete(name string) error {
	if !validPrefixName.MatchString(name) {
		return fmt.Errorf("invalid prefix name")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	pfxDir := filepath.Join(s.baseDir, name)
	if _, err := os.Stat(pfxDir); err != nil {
		return fmt.Errorf("prefix %q not found", name)
	}
	if err := os.RemoveAll(pfxDir); err != nil {
		return fmt.Errorf("delete prefix: %w", err)
	}
	log.Printf("[wine] deleted prefix %q", name)
	return nil
}

// Run executes a Windows .exe inside a prefix.
func (s *Service) Run(name, exe string, args []string) error {
	if !validPrefixName.MatchString(name) {
		return fmt.Errorf("invalid prefix name")
	}
	pfxDir := filepath.Join(s.baseDir, name)
	if _, err := os.Stat(pfxDir); err != nil {
		return fmt.Errorf("prefix %q not found", name)
	}

	cmdArgs := append([]string{exe}, args...)
	cmd := exec.Command("wine", cmdArgs...)
	cmd.Env = append(os.Environ(),
		"WINEPREFIX="+pfxDir,
		"WINEDEBUG=-all",
		"DISPLAY="+os.Getenv("DISPLAY"),
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Start()
}

// InstallDXVK installs DXVK into an existing prefix.
func (s *Service) InstallDXVK(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	pfxDir := filepath.Join(s.baseDir, name)
	if _, err := os.Stat(pfxDir); err != nil {
		return fmt.Errorf("prefix %q not found", name)
	}
	return s.installDXVK(pfxDir)
}

func (s *Service) installDXVK(pfxDir string) error {
	// Check if setup_dxvk is available (installed with DXVK package)
	bin, err := exec.LookPath("setup_dxvk")
	if err != nil {
		// Try Lutris's bundled DXVK
		bin, err = exec.LookPath("winetricks")
		if err != nil {
			return fmt.Errorf("neither setup_dxvk nor winetricks found")
		}
		cmd := exec.Command(bin, "dxvk")
		cmd.Env = append(os.Environ(),
			"WINEPREFIX="+pfxDir,
			"WINEDEBUG=-all",
		)
		return cmd.Run()
	}
	cmd := exec.Command(bin, "install")
	cmd.Env = append(os.Environ(),
		"WINEPREFIX="+pfxDir,
		"WINEDEBUG=-all",
	)
	return cmd.Run()
}

func (s *Service) hasDXVK(pfxDir string) bool {
	// DXVK installs d3d11.dll override in system32
	dll := filepath.Join(pfxDir, "drive_c", "windows", "system32", "d3d11.dll")
	info, err := os.Lstat(dll)
	if err != nil {
		return false
	}
	// DXVK replaces with a symlink or small shim; native Wine d3d11.dll is much larger
	return info.Size() < 500*1024
}

func (s *Service) readArch(pfxDir string) string {
	data, err := os.ReadFile(filepath.Join(pfxDir, "system.reg"))
	if err != nil {
		return "win64"
	}
	if strings.Contains(string(data), "#arch=win32") {
		return "win32"
	}
	return "win64"
}

func (s *Service) readWindowsVersion(pfxDir string) string {
	data, err := os.ReadFile(filepath.Join(pfxDir, "user.reg"))
	if err != nil {
		return ""
	}
	content := string(data)
	// Look for CurrentVersion in registry
	if strings.Contains(content, `"CurrentBuildNumber"="19041"`) {
		return "win10"
	}
	if strings.Contains(content, `"CurrentBuildNumber"="7601"`) {
		return "win7"
	}
	if strings.Contains(content, `"CurrentBuildNumber"="9600"`) {
		return "win81"
	}
	return "win10"
}

func (s *Service) setWindowsVersion(pfxDir, version string) {
	cmd := exec.Command("wine", "reg", "add",
		`HKEY_LOCAL_MACHINE\Software\Microsoft\Windows NT\CurrentVersion`,
		"/v", "CurrentBuildNumber", "/t", "REG_SZ", "/d", winBuild(version), "/f")
	cmd.Env = append(os.Environ(),
		"WINEPREFIX="+pfxDir,
		"WINEDEBUG=-all",
	)
	cmd.Run()
}

func winBuild(ver string) string {
	switch ver {
	case "win10":
		return "19041"
	case "win7":
		return "7601"
	case "win81":
		return "9600"
	default:
		return "19041"
	}
}

func dirSizeMB(path string) int {
	var total int64
	filepath.Walk(path, func(_ string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			total += info.Size()
		}
		return nil
	})
	return int(total / (1024 * 1024))
}

// RegisterHandlers registers Wine prefix API endpoints.
func (s *Service) RegisterHandlers(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/wine/prefixes", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(s.List())
	})

	mux.HandleFunc("POST /api/wine/prefixes", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Name string `json:"name"`
			Arch string `json:"arch"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error":"bad request"}`, 400)
			return
		}
		p, err := s.Create(req.Name, req.Arch)
		if err != nil {
			http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(p)
	})

	mux.HandleFunc("DELETE /api/wine/prefixes", func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("name")
		if err := s.Delete(name); err != nil {
			http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), 404)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"deleted"}`))
	})

	mux.HandleFunc("POST /api/wine/run", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Prefix string   `json:"prefix"`
			Exe    string   `json:"exe"`
			Args   []string `json:"args"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error":"bad request"}`, 400)
			return
		}
		if req.Prefix == "" || req.Exe == "" {
			http.Error(w, `{"error":"prefix and exe required"}`, 400)
			return
		}
		if err := s.Run(req.Prefix, req.Exe, req.Args); err != nil {
			http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"started"}`))
	})

	mux.HandleFunc("POST /api/wine/dxvk", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Prefix string `json:"prefix"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error":"bad request"}`, 400)
			return
		}
		if err := s.InstallDXVK(req.Prefix); err != nil {
			http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"installed"}`))
	})

	mux.HandleFunc("GET /api/wine/status", func(w http.ResponseWriter, r *http.Request) {
		g := gpu.Detect()
		hasBin := false
		if _, err := exec.LookPath("wine"); err == nil {
			hasBin = true
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"available":   hasBin,
			"gpu_tier":    g.TierName,
			"dxvk_auto":   g.Tier >= gpu.TierVAAPI,
			"prefix_count": len(s.List()),
		})
	})
}
