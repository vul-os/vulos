// Package desktop parses .desktop files (freedesktop.org Desktop Entry spec)
// and provides a unified app listing for the Vula OS launchpad.
//
// Sources:
//   - /usr/share/applications/*.desktop (apt-installed apps)
//   - ~/.local/share/applications/*.desktop (user apps)
//   - Web apps registered via the Vula registry
package desktop

import (
	"bufio"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Entry is a unified desktop entry — either from a .desktop file or a web app.
type Entry struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Comment     string   `json:"comment"`
	Exec        string   `json:"exec"`        // command to run (desktop apps)
	Icon        string   `json:"icon"`         // icon name or path
	Categories  []string `json:"categories"`
	Type        string   `json:"type"`         // "desktop" or "web"
	URL         string   `json:"url"`          // for web apps
	Terminal    bool     `json:"terminal"`     // run in terminal
	NoDisplay   bool     `json:"no_display"`   // hidden from menus
	MimeTypes   []string `json:"mime_types"`
}

// Service manages desktop entry discovery and caching.
type Service struct {
	mu      sync.RWMutex
	entries []Entry
	lastScan time.Time
	dirs    []string
}

// New creates a desktop entry service scanning the given directories.
func New() *Service {
	home, _ := os.UserHomeDir()
	return &Service{
		dirs: []string{
			"/usr/share/applications",
			"/usr/local/share/applications",
			filepath.Join(home, ".local", "share", "applications"),
			"/var/lib/flatpak/exports/share/applications",
			filepath.Join(home, ".local", "share", "flatpak", "exports", "share", "applications"),
		},
	}
}

// Scan reads all .desktop files and caches the results.
func (s *Service) Scan() []Entry {
	s.mu.Lock()
	defer s.mu.Unlock()

	var entries []Entry
	seen := map[string]bool{}

	for _, dir := range s.dirs {
		files, err := filepath.Glob(filepath.Join(dir, "*.desktop"))
		if err != nil {
			continue
		}
		for _, f := range files {
			e, err := parseDesktopFile(f)
			if err != nil || e.NoDisplay || e.Name == "" {
				continue
			}
			if seen[e.ID] {
				continue
			}
			seen[e.ID] = true
			entries = append(entries, e)
		}
	}

	s.entries = entries
	s.lastScan = time.Now()
	log.Printf("[desktop] scanned %d entries from %d dirs", len(entries), len(s.dirs))
	return entries
}

// List returns cached entries, rescanning if stale (>60s).
func (s *Service) List() []Entry {
	s.mu.RLock()
	if time.Since(s.lastScan) < 60*time.Second && s.entries != nil {
		entries := s.entries
		s.mu.RUnlock()
		return entries
	}
	s.mu.RUnlock()
	return s.Scan()
}

// RegisterHandlers registers the desktop entry API.
func (s *Service) RegisterHandlers(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/desktop/entries", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(s.List())
	})

	// Serve app icons from system icon themes (e.g. /usr/share/icons/hicolor/*/apps/)
	mux.HandleFunc("GET /api/desktop/icon/", func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/api/desktop/icon/")
		if name == "" || strings.Contains(name, "/") || strings.Contains(name, "..") {
			http.Error(w, "bad icon name", 400)
			return
		}
		iconPath := findSystemIcon(name)
		if iconPath == "" {
			http.Error(w, "not found", 404)
			return
		}
		http.ServeFile(w, r, iconPath)
	})

	// Force rescan (called after apt install/remove)
	mux.HandleFunc("POST /api/desktop/rescan", func(w http.ResponseWriter, r *http.Request) {
		entries := s.Scan()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]int{"count": len(entries)})
	})
}

// parseDesktopFile reads a .desktop file and returns an Entry.
func parseDesktopFile(path string) (Entry, error) {
	f, err := os.Open(path)
	if err != nil {
		return Entry{}, err
	}
	defer f.Close()

	e := Entry{
		ID:   strings.TrimSuffix(filepath.Base(path), ".desktop"),
		Type: "desktop",
	}

	scanner := bufio.NewScanner(f)
	inDesktopEntry := false

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		// Skip comments and empty lines
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Section headers
		if strings.HasPrefix(line, "[") {
			inDesktopEntry = line == "[Desktop Entry]"
			continue
		}

		if !inDesktopEntry {
			continue
		}

		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		val := strings.TrimSpace(parts[1])

		switch key {
		case "Name":
			e.Name = val
		case "Comment":
			e.Comment = val
		case "Exec":
			// Strip field codes (%f, %F, %u, %U, etc.)
			e.Exec = stripFieldCodes(val)
		case "Icon":
			e.Icon = val
		case "Categories":
			cats := strings.Split(val, ";")
			for _, c := range cats {
				c = strings.TrimSpace(c)
				if c != "" {
					e.Categories = append(e.Categories, c)
				}
			}
		case "Terminal":
			e.Terminal = val == "true"
		case "NoDisplay":
			e.NoDisplay = val == "true"
		case "MimeType":
			mimes := strings.Split(val, ";")
			for _, m := range mimes {
				m = strings.TrimSpace(m)
				if m != "" {
					e.MimeTypes = append(e.MimeTypes, m)
				}
			}
		case "Type":
			if val != "Application" {
				e.NoDisplay = true // Only show Application type
			}
		}
	}

	return e, scanner.Err()
}

// findSystemIcon searches for an icon by name in system icon theme directories.
// Prefers SVG > PNG, larger sizes first.
func findSystemIcon(name string) string {
	sizes := []string{"scalable", "256x256", "128x128", "64x64", "48x48", "32x32", "24x24", "16x16"}
	exts := []string{".svg", ".png", ".xpm"}
	dirs := []string{"/usr/share/icons/hicolor", "/usr/share/icons/Adwaita", "/usr/share/pixmaps"}

	for _, dir := range dirs {
		if dir == "/usr/share/pixmaps" {
			for _, ext := range exts {
				p := filepath.Join(dir, name+ext)
				if _, err := os.Stat(p); err == nil {
					return p
				}
			}
			continue
		}
		for _, sz := range sizes {
			for _, ext := range exts {
				p := filepath.Join(dir, sz, "apps", name+ext)
				if _, err := os.Stat(p); err == nil {
					return p
				}
			}
		}
	}
	return ""
}

// stripFieldCodes removes freedesktop field codes from Exec values.
func stripFieldCodes(exec string) string {
	fields := strings.Fields(exec)
	var clean []string
	for _, f := range fields {
		if len(f) == 2 && f[0] == '%' {
			continue // Skip %f, %F, %u, %U, %d, %D, %n, %N, %i, %c, %k
		}
		clean = append(clean, f)
	}
	return strings.Join(clean, " ")
}
