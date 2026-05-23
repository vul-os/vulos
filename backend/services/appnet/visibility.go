package appnet

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// Visibility constants for app accessibility.
const (
	VisibilityPrivate = "private" // only accessible on this device (default)
	VisibilityLocal   = "local"   // accessible to peers on the local network
	VisibilityPublic  = "public"  // accessible to anyone with the URL
)

// ValidVisibilities lists the accepted visibility values.
var ValidVisibilities = []string{VisibilityPrivate, VisibilityLocal, VisibilityPublic}

// SystemAppIDs contains the IDs of built-in OS apps that must never be
// published to the public web.  Attempting to set their visibility to
// anything other than "private" returns an error.
var SystemAppIDs = map[string]struct{}{
	"settings": {},
	"files":    {},
	"terminal": {},
}

// IsSystemApp reports whether appID belongs to the set of protected system
// apps that may not be published.
func IsSystemApp(appID string) bool {
	_, ok := SystemAppIDs[appID]
	return ok
}

// ValidateVisibility returns an error when v is not a recognised value.
// An empty string is NOT accepted here; callers that want the empty→private
// defaulting behaviour should apply it before calling this function.
func ValidateVisibility(v string) error {
	for _, valid := range ValidVisibilities {
		if v == valid {
			return nil
		}
	}
	return fmt.Errorf("invalid visibility %q (valid: private, local, public)", v)
}

// visibilityDB is the on-disk representation stored as JSON.
type visibilityDB struct {
	// Apps maps app ID → visibility value.
	Apps map[string]string `json:"apps"`
}

// VisibilityStore persists per-app visibility settings to
// ~/.vulos/db/visibility.json (or the path set via VULOS_VISIBILITY_DB).
// All methods are safe for concurrent use.
type VisibilityStore struct {
	mu   sync.RWMutex
	path string
	db   visibilityDB
}

// dbPath returns the resolved path for the visibility database file.
// Order of precedence:
//  1. VULOS_VISIBILITY_DB environment variable
//  2. $HOME/.vulos/db/visibility.json
func dbPath() string {
	if p := os.Getenv("VULOS_VISIBILITY_DB"); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, ".vulos", "db", "visibility.json")
}

// NewVisibilityStore creates a VisibilityStore backed by the default path and
// loads any existing data from disk.  It is not an error if the file does not
// yet exist — the store starts empty in that case.
func NewVisibilityStore() (*VisibilityStore, error) {
	return NewVisibilityStoreAt(dbPath())
}

// NewVisibilityStoreAt creates a VisibilityStore backed by path and loads any
// existing data.  Useful for testing with a temporary directory.
func NewVisibilityStoreAt(path string) (*VisibilityStore, error) {
	vs := &VisibilityStore{
		path: path,
		db:   visibilityDB{Apps: make(map[string]string)},
	}
	if err := vs.load(); err != nil {
		return nil, err
	}
	return vs, nil
}

// load reads the JSON file from disk.  A missing file is silently ignored.
func (vs *VisibilityStore) load() error {
	data, err := os.ReadFile(vs.path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("visibility store: read %s: %w", vs.path, err)
	}
	var db visibilityDB
	if err := json.Unmarshal(data, &db); err != nil {
		return fmt.Errorf("visibility store: parse %s: %w", vs.path, err)
	}
	if db.Apps == nil {
		db.Apps = make(map[string]string)
	}
	vs.db = db
	return nil
}

// save writes the in-memory state to disk atomically (write-to-temp + rename).
// Callers must hold vs.mu (write lock).
func (vs *VisibilityStore) save() error {
	if err := os.MkdirAll(filepath.Dir(vs.path), 0700); err != nil {
		return fmt.Errorf("visibility store: mkdir: %w", err)
	}
	data, err := json.MarshalIndent(vs.db, "", "  ")
	if err != nil {
		return fmt.Errorf("visibility store: marshal: %w", err)
	}
	tmp := vs.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return fmt.Errorf("visibility store: write tmp: %w", err)
	}
	if err := os.Rename(tmp, vs.path); err != nil {
		return fmt.Errorf("visibility store: rename: %w", err)
	}
	return nil
}

// Get returns the visibility for appID.  If no value has been stored the
// default VisibilityPrivate is returned.
func (vs *VisibilityStore) Get(appID string) string {
	vs.mu.RLock()
	defer vs.mu.RUnlock()
	if v, ok := vs.db.Apps[appID]; ok {
		return v
	}
	return VisibilityPrivate
}

// Set stores visibility for appID and persists to disk.
// Returns an error if visibility is not one of the three valid values.
func (vs *VisibilityStore) Set(appID, visibility string) error {
	if visibility == "" {
		visibility = VisibilityPrivate
	}
	if err := ValidateVisibility(visibility); err != nil {
		return err
	}
	vs.mu.Lock()
	defer vs.mu.Unlock()
	vs.db.Apps[appID] = visibility
	return vs.save()
}

// Delete removes the stored visibility for appID (it will revert to the
// default VisibilityPrivate on the next Get call) and persists to disk.
func (vs *VisibilityStore) Delete(appID string) error {
	vs.mu.Lock()
	defer vs.mu.Unlock()
	delete(vs.db.Apps, appID)
	return vs.save()
}

// All returns a copy of the full appID → visibility map.
func (vs *VisibilityStore) All() map[string]string {
	vs.mu.RLock()
	defer vs.mu.RUnlock()
	out := make(map[string]string, len(vs.db.Apps))
	for k, v := range vs.db.Apps {
		out[k] = v
	}
	return out
}
