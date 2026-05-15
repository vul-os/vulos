package profiles

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"vulos/backend/services/naming"
)

// BrowserProfile is an isolated browsing context — its own cookies, storage,
// and session state. Like Firefox containers but at the OS level.
//
// How isolation works:
//   - Local (WebKit/Cage): Each profile maps to a separate WebKitWebsiteDataManager
//     with its own data directory. Cog supports --data-dir for this.
//   - Remote (browser over network): The gateway injects a profile-specific cookie
//     prefix and the proxy maintains separate cookie jars per profile.
//   - Apps: Each app launched through the gateway gets its own profile by default
//     (app ID = profile ID). Users can also assign apps to named profiles.
type BrowserProfile struct {
	ID          string            `json:"id"`
	UserID      string            `json:"user_id"`
	Name        string            `json:"name"`
	Color       string            `json:"color"` // hex color for visual identification
	Icon        string            `json:"icon"`
	Isolated    bool              `json:"isolated"`     // if true, strict isolation (no shared state)
	AppBindings []string          `json:"app_bindings"` // app IDs bound to this profile
	Cookies     map[string]string `json:"cookies"`      // domain → cookie jar path (managed by gateway)
	DataDir     string            `json:"data_dir"`     // filesystem path for this profile's data
	CreatedAt   time.Time         `json:"created_at"`
	UpdatedAt   time.Time         `json:"updated_at"`
}

// Store manages browser profiles per user.
type Store struct {
	mu       sync.RWMutex
	profiles map[string]*BrowserProfile // profileID → profile
	baseDir  string
	path     string
}

func NewStore(dataDir string) *Store {
	baseDir := filepath.Join(dataDir, "browser-profiles")
	os.MkdirAll(baseDir, 0755)
	storePath := filepath.Join(dataDir, "browser-profiles.json")

	s := &Store{
		profiles: make(map[string]*BrowserProfile),
		baseDir:  baseDir,
		path:     storePath,
	}

	if data, err := os.ReadFile(storePath); err == nil {
		var list []*BrowserProfile
		json.Unmarshal(data, &list)
		for _, p := range list {
			s.profiles[p.ID] = p
		}
	}
	return s
}

// Create makes a new browser profile.
// name must satisfy the Vulos naming rules: lowercase alphanumeric with
// optional single hyphens, no leading/trailing or consecutive hyphens,
// 2–32 characters. This prevents "--" from appearing in DNS subdomains.
func (s *Store) Create(userID, name, color, icon string) (*BrowserProfile, error) {
	if err := naming.ValidateIdent(name, "profile name", 2, 32); err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	id := fmt.Sprintf("bp-%d", time.Now().UnixMilli())
	dataDir := filepath.Join(s.baseDir, id)
	os.MkdirAll(dataDir, 0755)

	p := &BrowserProfile{
		ID:        id,
		UserID:    userID,
		Name:      name,
		Color:     color,
		Icon:      icon,
		Isolated:  true,
		Cookies:   make(map[string]string),
		DataDir:   dataDir,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	s.profiles[id] = p
	log.Printf("[profiles] created browser profile %s (%s) for user %s", id, name, userID)
	return p, nil
}

// EnsureDefaults creates the standard profiles for a user if they don't exist.
func (s *Store) EnsureDefaults(userID string) {
	existing := s.ListForUser(userID)
	if len(existing) > 0 {
		return
	}
	// Names are lowercase to comply with the Vulos naming rules (DNS-safe).
	// These literals are always valid, so errors are intentionally ignored.
	s.Create(userID, "personal", "#3b82f6", "👤") //nolint:errcheck
	s.Create(userID, "work", "#22c55e", "💼")     //nolint:errcheck
	s.Create(userID, "private", "#a855f7", "🔒")  //nolint:errcheck
}

// Get returns a profile by ID.
func (s *Store) Get(id string) (*BrowserProfile, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.profiles[id]
	return p, ok
}

// ListForUser returns all profiles for a user.
func (s *Store) ListForUser(userID string) []*BrowserProfile {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var result []*BrowserProfile
	for _, p := range s.profiles {
		if p.UserID == userID {
			result = append(result, p)
		}
	}
	return result
}

// Update modifies a profile.
// If name is non-empty it must satisfy the Vulos naming rules (see Create).
func (s *Store) Update(id, name, color, icon string) error {
	if name != "" {
		if err := naming.ValidateIdent(name, "profile name", 2, 32); err != nil {
			return err
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.profiles[id]
	if !ok {
		return fmt.Errorf("profile not found")
	}
	if name != "" {
		p.Name = name
	}
	if color != "" {
		p.Color = color
	}
	if icon != "" {
		p.Icon = icon
	}
	p.UpdatedAt = time.Now()
	return nil
}

// BindApp assigns an app to always use this profile.
func (s *Store) BindApp(profileID, appID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.profiles[profileID]
	if !ok {
		return fmt.Errorf("profile not found")
	}
	// Remove from any existing profile
	for _, other := range s.profiles {
		filtered := other.AppBindings[:0]
		for _, a := range other.AppBindings {
			if a != appID {
				filtered = append(filtered, a)
			}
		}
		other.AppBindings = filtered
	}
	p.AppBindings = append(p.AppBindings, appID)
	p.UpdatedAt = time.Now()
	return nil
}

// ProfileForApp returns the profile bound to an app, or empty string for default.
func (s *Store) ProfileForApp(userID, appID string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, p := range s.profiles {
		if p.UserID != userID {
			continue
		}
		for _, a := range p.AppBindings {
			if a == appID {
				return p.ID
			}
		}
	}
	return ""
}

// Delete removes a profile and its data.
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.profiles[id]
	if !ok {
		return fmt.Errorf("profile not found")
	}
	os.RemoveAll(p.DataDir)
	delete(s.profiles, id)
	log.Printf("[profiles] deleted browser profile %s", id)
	return nil
}

// ClearData wipes all cookies/storage for a profile without deleting it.
func (s *Store) ClearData(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.profiles[id]
	if !ok {
		return fmt.Errorf("profile not found")
	}
	os.RemoveAll(p.DataDir)
	os.MkdirAll(p.DataDir, 0755)
	p.Cookies = make(map[string]string)
	p.UpdatedAt = time.Now()
	log.Printf("[profiles] cleared data for profile %s", id)
	return nil
}

// Flush persists to disk.
func (s *Store) Flush() error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var list []*BrowserProfile
	for _, p := range s.profiles {
		list = append(list, p)
	}
	data, _ := json.MarshalIndent(list, "", "  ")
	tmp := s.path + ".tmp"
	os.WriteFile(tmp, data, 0600)
	return os.Rename(tmp, s.path)
}
