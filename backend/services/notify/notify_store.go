package notify

// notify_store.go — NOTIF-02: persistent notification store.
//
// Persists notifications to ~/.vulos/db/notifications.json (0600) using an
// atomic temp-file + rename. A missing or corrupt file yields a fresh empty
// store with no error (best-effort durability, never blocks the in-memory
// path). Retention (cap + max age) is applied on every Append.
//
// All methods are safe on a nil *Store receiver so callers never need to
// guard the pointer.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	// storeSchemaVersion is the on-disk schema version.
	storeSchemaVersion = 1

	// defaultMaxN is the default retention cap (max notifications kept).
	defaultMaxN = 500

	// defaultMaxAge is the default retention age (older entries pruned).
	defaultMaxAge = 30 * 24 * time.Hour
)

// storeFile is the on-disk JSON schema:
// {"version":1,"notifications":[...],"saved_at":"..."}.
type storeFile struct {
	Version       int            `json:"version"`
	Notifications []Notification `json:"notifications"`
	SavedAt       string         `json:"saved_at"`
}

// Store is a file-backed notification store with retention.
type Store struct {
	mu     sync.Mutex
	path   string
	items  []Notification
	maxN   int
	maxAge time.Duration
}

// OpenStore opens (or initialises) the notification store at path.
//
// If the file is missing or corrupt, a fresh empty store is returned with no
// error — persistence is best-effort and must never fail notification
// delivery. The parent directory is created if absent.
func OpenStore(path string) (*Store, error) {
	s := &Store{
		path:   path,
		maxN:   defaultMaxN,
		maxAge: defaultMaxAge,
	}

	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
	}

	data, err := os.ReadFile(path)
	if err != nil {
		// Missing file (or unreadable) → fresh empty store, no error.
		return s, nil
	}

	var sf storeFile
	if err := json.Unmarshal(data, &sf); err != nil {
		// Corrupt file → fresh empty store, no error.
		return s, nil
	}
	s.items = sf.Notifications
	return s, nil
}

// Close flushes any pending state to disk. Safe on a nil receiver.
func (s *Store) Close() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.flushLocked()
}

// Append adds a notification, applies retention, and persists.
// Safe on a nil receiver and a nil notification.
func (s *Store) Append(n *Notification) {
	if s == nil || n == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	s.items = append(s.items, *n)
	s.applyRetentionLocked()
	_ = s.flushLocked()
}

// List returns up to limit notifications, newest first. A limit <= 0 (or
// larger than the store) returns everything. Safe on a nil receiver.
func (s *Store) List(limit int) []Notification {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	n := len(s.items)
	if limit <= 0 || limit > n {
		limit = n
	}
	out := make([]Notification, limit)
	for i := 0; i < limit; i++ {
		out[i] = s.items[n-1-i]
	}
	return out
}

// MarkRead marks the notification with the given id as read and persists.
// Safe on a nil receiver.
func (s *Store) MarkRead(id string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.items {
		if s.items[i].ID == id {
			s.items[i].Read = true
			_ = s.flushLocked()
			return
		}
	}
}

// MarkAllRead marks every stored notification as read and persists.
// Safe on a nil receiver.
func (s *Store) MarkAllRead() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.items {
		s.items[i].Read = true
	}
	_ = s.flushLocked()
}

// Prune trims the store to at most maxN entries and drops entries older than
// maxAge, then persists. A non-positive maxN or maxAge disables that bound.
// Safe on a nil receiver.
func (s *Store) Prune(maxN int, maxAge time.Duration) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(maxN, maxAge)
	_ = s.flushLocked()
}

// Len returns the number of stored notifications. Safe on a nil receiver.
func (s *Store) Len() int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.items)
}

// applyRetentionLocked enforces the store's configured defaults. Caller holds s.mu.
func (s *Store) applyRetentionLocked() {
	s.pruneLocked(s.maxN, s.maxAge)
}

// pruneLocked applies cap + age bounds. Caller holds s.mu.
func (s *Store) pruneLocked(maxN int, maxAge time.Duration) {
	if maxAge > 0 {
		cutoff := time.Now().Add(-maxAge)
		kept := s.items[:0]
		for _, it := range s.items {
			if it.CreatedAt.IsZero() || it.CreatedAt.After(cutoff) {
				kept = append(kept, it)
			}
		}
		s.items = kept
	}
	if maxN > 0 && len(s.items) > maxN {
		s.items = append([]Notification(nil), s.items[len(s.items)-maxN:]...)
	}
}

// flushLocked atomically writes the store to disk (temp + rename, 0600).
// Caller holds s.mu. A best-effort no-op when no path is configured.
func (s *Store) flushLocked() error {
	if s.path == "" {
		return nil
	}

	sf := storeFile{
		Version:       storeSchemaVersion,
		Notifications: s.items,
		SavedAt:       time.Now().UTC().Format(time.RFC3339),
	}
	if sf.Notifications == nil {
		sf.Notifications = []Notification{}
	}

	data, err := json.MarshalIndent(sf, "", "  ")
	if err != nil {
		return err
	}

	dir := filepath.Dir(s.path)
	tmp, err := os.CreateTemp(dir, ".notifications-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once renamed

	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return err
	}
	// Belt-and-braces: ensure final perms even if umask altered CreateTemp.
	return os.Chmod(s.path, 0o600)
}
