package core

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// ErrPinNotFound is returned by Store.Load when no pin is stored for a name.
var ErrPinNotFound = errors.New("clients/core: no stored pin for this box")

// FileStore is a reference Store implementation backed by a single 0600 file
// on disk (one "name base64(pin)" line per box), written via a temp-file
// rename so a crash mid-write can never leave a partially-written pin file.
//
// This exists so the package is usable and testable standalone. It is
// explicitly NOT what a production client should ship: a plain file is
// readable by anything with the same OS-user's filesystem access, which is a
// materially weaker guarantee than the platform keystore (Keychain, Android
// Keystore, DPAPI, libsecret) the Store interface doc comment calls for. Wails
// desktop / mobile shells should provide their own Store backed by the real
// keystore before shipping; FileStore is the fallback/dev/test seam.
type FileStore struct {
	// Path is the file pins are persisted to.
	Path string

	mu sync.Mutex
}

var _ Store = (*FileStore)(nil)

// NewFileStore returns a FileStore persisting to path. It does not touch the
// filesystem until Load/Save/Forget is called.
func NewFileStore(path string) *FileStore {
	return &FileStore{Path: path}
}

// Load returns the stored Fingerprint for name, or ErrPinNotFound if none is
// stored.
func (s *FileStore) Load(_ context.Context, name string) (Fingerprint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	entries, err := s.readAll()
	if err != nil {
		return Fingerprint{}, err
	}
	fp, ok := entries[name]
	if !ok {
		return Fingerprint{}, fmt.Errorf("%w: %q", ErrPinNotFound, name)
	}
	return fp, nil
}

// Save persists fp as the pin for name, overwriting any previous pin for that
// name. It writes to a temp file in the same directory and renames it over
// Path, so a concurrent reader (or a crash) never observes a half-written
// file, and sets file mode 0600 so only the owning OS user can read it.
func (s *FileStore) Save(_ context.Context, name string, fp Fingerprint) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("clients/core: FileStore.Save: empty name")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	entries, err := s.readAll()
	if err != nil {
		return err
	}
	entries[name] = fp
	return s.writeAll(entries)
}

// Forget deletes the stored pin for name, if any. Forgetting a name that has
// no stored pin is not an error.
func (s *FileStore) Forget(_ context.Context, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	entries, err := s.readAll()
	if err != nil {
		return err
	}
	delete(entries, name)
	return s.writeAll(entries)
}

func (s *FileStore) readAll() (map[string]Fingerprint, error) {
	entries := map[string]Fingerprint{}

	data, err := os.ReadFile(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		return entries, nil
	}
	if err != nil {
		return nil, fmt.Errorf("clients/core: FileStore: read %s: %w", s.Path, err)
	}

	for lineNo, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.SplitN(line, " ", 2)
		if len(fields) != 2 {
			return nil, fmt.Errorf("clients/core: FileStore: %s:%d: malformed line", s.Path, lineNo+1)
		}
		name, encoded := fields[0], fields[1]
		raw, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil || len(raw) != len(Fingerprint{}.SPKISHA256) {
			return nil, fmt.Errorf("clients/core: FileStore: %s:%d: malformed pin for %q", s.Path, lineNo+1, name)
		}
		var fp Fingerprint
		copy(fp.SPKISHA256[:], raw)
		entries[name] = fp
	}
	return entries, nil
}

func (s *FileStore) writeAll(entries map[string]Fingerprint) error {
	dir := filepath.Dir(s.Path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("clients/core: FileStore: mkdir %s: %w", dir, err)
	}

	var b strings.Builder
	for name, fp := range entries {
		b.WriteString(name)
		b.WriteByte(' ')
		b.WriteString(fp.String())
		b.WriteByte('\n')
	}

	tmp, err := os.CreateTemp(dir, filepath.Base(s.Path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("clients/core: FileStore: create temp file: %w", err)
	}
	tmpPath := tmp.Name()
	// If anything below fails before the rename, remove the leftover temp
	// file rather than abandoning it next to the real store.
	success := false
	defer func() {
		if !success {
			_ = os.Remove(tmpPath)
		}
	}()

	if _, err := tmp.WriteString(b.String()); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("clients/core: FileStore: write temp file: %w", err)
	}
	if err := tmp.Chmod(0600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("clients/core: FileStore: chmod temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("clients/core: FileStore: sync temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("clients/core: FileStore: close temp file: %w", err)
	}
	if err := os.Rename(tmpPath, s.Path); err != nil {
		return fmt.Errorf("clients/core: FileStore: rename into place: %w", err)
	}
	success = true
	return nil
}
