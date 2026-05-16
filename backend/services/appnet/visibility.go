package appnet

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// Visibility represents the network exposure level of an app.
type Visibility string

const (
	// VisibilityPrivate is the default: app is only accessible on this device.
	VisibilityPrivate Visibility = "private"
	// VisibilityLocal makes the app accessible to peers on the same local network.
	VisibilityLocal Visibility = "local"
	// VisibilityPublic makes the app accessible to anyone with the URL.
	VisibilityPublic Visibility = "public"
)

// ValidateVisibility returns an error if v is not a recognised visibility value.
func ValidateVisibility(v Visibility) error {
	switch v {
	case VisibilityPrivate, VisibilityLocal, VisibilityPublic:
		return nil
	default:
		return fmt.Errorf("invalid visibility %q: must be one of private, local, public", v)
	}
}

// visibilityState is the on-disk format persisted to visibility.json.
type visibilityState struct {
	Apps map[string]Visibility `json:"apps"`
}

// VisibilityStore persists per-app visibility settings.
// All methods are safe for concurrent use.
type VisibilityStore struct {
	mu   sync.RWMutex
	path string
	data visibilityState
}

// NewVisibilityStore loads (or creates) the visibility store at path.
// If the file does not exist an empty store is returned without error.
func NewVisibilityStore(path string) (*VisibilityStore, error) {
	s := &VisibilityStore{
		path: path,
		data: visibilityState{Apps: make(map[string]Visibility)},
	}
	if err := s.load(); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("visibility store: %w", err)
	}
	return s, nil
}

// Get returns the visibility for an app, defaulting to VisibilityPrivate when
// no explicit setting has been stored.
func (s *VisibilityStore) Get(appID string) Visibility {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if v, ok := s.data.Apps[appID]; ok {
		return v
	}
	return VisibilityPrivate
}

// Set persists the visibility for an app and flushes to disk.
func (s *VisibilityStore) Set(appID string, v Visibility) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data.Apps[appID] = v
	return s.flush()
}

// Delete removes the explicit visibility setting for an app (reverts to default
// private) and flushes to disk.
func (s *VisibilityStore) Delete(appID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data.Apps, appID)
	return s.flush()
}

// All returns a copy of the stored visibility map. Apps with no explicit entry
// are not included; callers should treat missing entries as VisibilityPrivate.
func (s *VisibilityStore) All() map[string]Visibility {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]Visibility, len(s.data.Apps))
	for k, v := range s.data.Apps {
		out[k] = v
	}
	return out
}

// load reads the JSON file from disk. Must be called with no lock held.
func (s *VisibilityStore) load() error {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return err
	}
	var st visibilityState
	if err := json.Unmarshal(data, &st); err != nil {
		return err
	}
	if st.Apps == nil {
		st.Apps = make(map[string]Visibility)
	}
	s.data = st
	return nil
}

// flush writes the current state to disk. Must be called with the write lock held.
func (s *VisibilityStore) flush() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0644)
}
