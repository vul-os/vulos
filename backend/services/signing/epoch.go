package signing

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// DefaultEpochPath is the default path for the persistent epoch floor file.
// Callers may override by passing a custom path to NewEpochStore.
const DefaultEpochPath = "/var/lib/vulos/epoch-floor.json"

// epochRecord is the on-disk JSON format for the persisted epoch floor.
type epochRecord struct {
	Floor int64 `json:"floor"`
}

// EpochStore persists the minimum trusted epoch floor.
//
// The floor is monotonically increasing: it can only rise, never fall.
// It is used to enforce rollback and downgrade protection — any manifest
// or epoch claim below the floor is refused outright.
//
// v1 persistence is file-based (atomic write via rename).  TPM-sealed
// storage is a planned future upgrade noted in SIGNING.md.
//
// EpochStore is safe for concurrent use.
type EpochStore struct {
	mu   sync.RWMutex
	path string
	// current is the in-memory cached floor; always consistent with the file.
	current int64
}

// NewEpochStore opens (or creates) the epoch floor file at path.
//
// If the file does not exist it is created with floor = 0.
// Any pre-existing floor value is loaded into memory immediately so that
// Current() never requires disk I/O after construction.
func NewEpochStore(path string) (*EpochStore, error) {
	if path == "" {
		path = DefaultEpochPath
	}

	// Ensure parent directory exists.
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, fmt.Errorf("signing: epoch store: mkdir %q: %w", filepath.Dir(path), err)
	}

	s := &EpochStore{path: path}

	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		// Brand-new device: initialise at floor 0 and persist.
		if werr := s.writeFloor(0); werr != nil {
			return nil, fmt.Errorf("signing: epoch store: init write: %w", werr)
		}
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("signing: epoch store: read %q: %w", path, err)
	}

	var rec epochRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil, fmt.Errorf("signing: epoch store: parse %q: %w", path, err)
	}
	s.current = rec.Floor
	return s, nil
}

// Current returns the current minimum trusted epoch floor.
// This is the highest epoch the device has ever accepted via RaiseTo.
func (s *EpochStore) Current() int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.current
}

// RaiseTo raises the epoch floor to n if n is strictly greater than the
// current floor, then persists the new value.  If n <= current this is a
// no-op (monotonicity guarantee — the floor never decreases).
//
// Returns an error only on persistence failures; a no-op raise returns nil.
func (s *EpochStore) RaiseTo(n int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if n <= s.current {
		// No-op: already at or above n; monotonicity is preserved.
		return nil
	}

	if err := s.writeFloor(n); err != nil {
		return fmt.Errorf("signing: epoch store: raise to %d: %w", n, err)
	}
	s.current = n
	return nil
}

// AcceptEpoch checks whether a claimed epoch value is acceptable given the
// current floor.  It returns a non-nil error if claimed < Current(), which
// indicates a rollback or downgrade attempt.
//
// AcceptEpoch does NOT raise the floor — raising is an explicit, root-signed
// operation performed via BumpFloor (or RaiseTo after external verification).
// The distinction keeps rollback protection separate from the floor-raising
// policy.
func (s *EpochStore) AcceptEpoch(claimed int64) error {
	floor := s.Current()
	if claimed < floor {
		return fmt.Errorf("signing: epoch rejected: claimed %d is below floor %d", claimed, floor)
	}
	return nil
}

// EpochBumpMessage is the canonical structure for a root-signed floor-bump
// request.  The root key signs the canonical JSON of this struct; the
// signature is verified before the floor is raised.
type EpochBumpMessage struct {
	// Type must be "epoch-bump" to prevent cross-context signature reuse.
	Type string `json:"type"`
	// NewFloor is the floor value the device should raise to.
	NewFloor int64 `json:"new_floor"`
}

// BumpFloor raises the epoch floor to msg.NewFloor after verifying that sig
// is a valid root signature over the canonical JSON of msg.
//
// The root key is loaded from anchorPath via VerifyWithAnchor (SIGN-02).
// The bump is applied only when:
//   - the signature verifies against the baked trust anchor, AND
//   - msg.Type == "epoch-bump", AND
//   - msg.NewFloor > current floor (monotonicity; a signed bump to a lower
//     value is a no-op, not an error, consistent with RaiseTo semantics)
//
// Returns an error if signature verification fails or the message is
// structurally invalid.  A no-op bump (new_floor <= current) returns nil.
func (s *EpochStore) BumpFloor(anchorPath string, msg EpochBumpMessage, sig []byte) error {
	if msg.Type != "epoch-bump" {
		return fmt.Errorf("signing: epoch bump: invalid message type %q (want \"epoch-bump\")", msg.Type)
	}

	canonical, err := Canonical(msg)
	if err != nil {
		return fmt.Errorf("signing: epoch bump: canonical: %w", err)
	}

	if err := VerifyWithAnchor(anchorPath, canonical, sig); err != nil {
		return fmt.Errorf("signing: epoch bump: %w", err)
	}

	// Signature is valid; raise the floor (no-op if already at or above).
	return s.RaiseTo(msg.NewFloor)
}

// writeFloor atomically persists floor to s.path via a temp-file rename.
// Must be called with s.mu held (write lock).
func (s *EpochStore) writeFloor(floor int64) error {
	rec := epochRecord{Floor: floor}
	data, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}

	dir := filepath.Dir(s.path)
	tmp, err := os.CreateTemp(dir, ".epoch-floor-*.json.tmp")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	tmpName := tmp.Name()

	// Always clean up the temp file on any failure path.
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(tmpName)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp: %w", err)
	}

	if err := os.Rename(tmpName, s.path); err != nil {
		return fmt.Errorf("rename: %w", err)
	}
	ok = true
	return nil
}
