package osdist

// slots.go — A/B slot manager + atomic active-slot flip (OSDIST-02).
//
// Design borrows from RAUC / Mender / ostree / Android A/B / Flatcar:
//   - Two identical slot directories (slot-a, slot-b) on the local cache partition.
//   - A single boot-state.json records which slot is active, which is pending, the
//     boot counter, and the last-known-good slot.
//   - All state mutations go through an atomic write (write temp file → rename).
//   - New images are staged into the *inactive* slot only; the active slot is never
//     touched by SlotManager.
//   - The writable overlay/data partition is entirely separate and is never read or
//     modified here.
//
// boot-state.json on-disk schema:
//
//	{
//	  "active":           "a",
//	  "pending":          "b",   // empty string when no update is pending
//	  "boot_counter":     0,
//	  "last_known_good":  "a"
//	}

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// ─── Sentinel errors ─────────────────────────────────────────────────────────

// ErrInvalidSlot is returned when a slot identifier other than "a" or "b" is used.
var ErrInvalidSlot = errors.New("osdist/slots: invalid slot (must be \"a\" or \"b\")")

// ErrStagingActiveSlot is returned when a caller attempts to stage data into
// the currently-active slot.
var ErrStagingActiveSlot = errors.New("osdist/slots: refusing to stage into the active slot")

// ErrNoPending is returned when Flip is called but no pending slot is set.
var ErrNoPending = errors.New("osdist/slots: no pending slot set; call SetPending first")

// ErrNoLastKnownGood is returned when Rollback is called but last_known_good is empty.
var ErrNoLastKnownGood = errors.New("osdist/slots: no last_known_good slot recorded")

// ─── Slot identifier ─────────────────────────────────────────────────────────

// Slot identifies one of the two OS image slots.
type Slot string

const (
	SlotA Slot = "a"
	SlotB Slot = "b"
	// SlotNone represents an absent/unset slot field.
	SlotNone Slot = ""
)

// valid reports whether s is a legal, non-empty slot identifier.
func (s Slot) valid() bool { return s == SlotA || s == SlotB }

// dir returns the subdirectory name for the slot inside the cache root.
func (s Slot) dir() string { return "slot-" + string(s) }

// ─── BootState ───────────────────────────────────────────────────────────────

// BootState is the in-memory representation of boot-state.json.
//
// JSON field names are fixed by the on-disk schema; do not rename them.
type BootState struct {
	// Active is the slot the running OS was loaded from ("a" or "b").
	Active Slot `json:"active"`

	// Pending is the slot the bootloader should try next ("a", "b", or "").
	// An empty string means no update is pending; the active slot will be
	// re-used on the next boot.
	Pending Slot `json:"pending"`

	// BootCounter counts consecutive boot attempts of the pending/active slot
	// without a healthy-mark.  vulos-init resets this to 0 after a clean boot.
	// The bootloader increments it before handoff; if it exceeds a threshold the
	// bootloader falls back to LastKnownGood automatically.
	BootCounter int `json:"boot_counter"`

	// LastKnownGood is the most-recently confirmed healthy slot.  On Flip it is
	// set to the prior Active so that Rollback can return to it.
	LastKnownGood Slot `json:"last_known_good"`
}

// validate returns an error if the state contains logically impossible values.
func (bs *BootState) validate() error {
	if !bs.Active.valid() {
		return fmt.Errorf("osdist/slots: invalid active slot %q", bs.Active)
	}
	if bs.Pending != SlotNone && !bs.Pending.valid() {
		return fmt.Errorf("osdist/slots: invalid pending slot %q", bs.Pending)
	}
	if bs.LastKnownGood != SlotNone && !bs.LastKnownGood.valid() {
		return fmt.Errorf("osdist/slots: invalid last_known_good slot %q", bs.LastKnownGood)
	}
	if bs.BootCounter < 0 {
		return fmt.Errorf("osdist/slots: boot_counter must be ≥ 0, got %d", bs.BootCounter)
	}
	return nil
}

// ─── SlotManager ─────────────────────────────────────────────────────────────

// SlotManager manages the two-slot OS image cache on the local cache partition.
//
// Layout inside cacheDir:
//
//	cacheDir/
//	├── slot-a/          — OS image files for slot A
//	├── slot-b/          — OS image files for slot B
//	└── boot-state.json  — current BootState (always atomically written)
//
// The writable overlay/data partition is completely separate and is never
// accessed by SlotManager.
type SlotManager struct {
	cacheDir string
}

// NewSlotManager creates a SlotManager rooted at cacheDir and ensures the
// slot-a, slot-b subdirectories exist.  It does NOT create or modify
// boot-state.json; call Load (or initialise and Save) explicitly.
func NewSlotManager(cacheDir string) (*SlotManager, error) {
	for _, s := range []Slot{SlotA, SlotB} {
		if err := os.MkdirAll(filepath.Join(cacheDir, s.dir()), 0o755); err != nil {
			return nil, fmt.Errorf("osdist/slots: mkdir %s: %w", s.dir(), err)
		}
	}
	return &SlotManager{cacheDir: cacheDir}, nil
}

// stateFile returns the absolute path to boot-state.json.
func (sm *SlotManager) stateFile() string {
	return filepath.Join(sm.cacheDir, "boot-state.json")
}

// SlotDir returns the absolute path to the directory for slot s.
func (sm *SlotManager) SlotDir(s Slot) (string, error) {
	if !s.valid() {
		return "", ErrInvalidSlot
	}
	return filepath.Join(sm.cacheDir, s.dir()), nil
}

// ─── Load / Save ─────────────────────────────────────────────────────────────

// Load reads and deserialises boot-state.json from the cache directory.
// Returns an error if the file does not exist or is corrupt.
func (sm *SlotManager) Load() (*BootState, error) {
	data, err := os.ReadFile(sm.stateFile())
	if err != nil {
		return nil, fmt.Errorf("osdist/slots: load boot-state: %w", err)
	}
	var bs BootState
	if err := json.Unmarshal(data, &bs); err != nil {
		return nil, fmt.Errorf("osdist/slots: parse boot-state: %w", err)
	}
	if err := bs.validate(); err != nil {
		return nil, err
	}
	return &bs, nil
}

// Save atomically writes bs to boot-state.json via a temp-file-then-rename
// strategy so that the file is never partially written.  Callers receive an
// error if the state is logically invalid.
func (sm *SlotManager) Save(bs *BootState) error {
	if err := bs.validate(); err != nil {
		return err
	}

	data, err := json.MarshalIndent(bs, "", "  ")
	if err != nil {
		return fmt.Errorf("osdist/slots: marshal boot-state: %w", err)
	}
	// Append a trailing newline — cosmetic but consistent with standard tooling.
	data = append(data, '\n')

	// Write to a temp file in the same directory so rename is atomic on the
	// same filesystem (POSIX guarantee).
	tmp, err := os.CreateTemp(sm.cacheDir, "boot-state-*.json.tmp")
	if err != nil {
		return fmt.Errorf("osdist/slots: create temp: %w", err)
	}
	tmpPath := tmp.Name()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("osdist/slots: write temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("osdist/slots: sync temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("osdist/slots: close temp: %w", err)
	}

	if err := os.Rename(tmpPath, sm.stateFile()); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("osdist/slots: rename boot-state: %w", err)
	}
	return nil
}

// ─── Slot helpers ─────────────────────────────────────────────────────────────

// InactiveSlot returns the slot that is NOT currently active.
func (sm *SlotManager) InactiveSlot(bs *BootState) (Slot, error) {
	switch bs.Active {
	case SlotA:
		return SlotB, nil
	case SlotB:
		return SlotA, nil
	default:
		return SlotNone, fmt.Errorf("osdist/slots: cannot determine inactive slot: active=%q", bs.Active)
	}
}

// ─── StageInto ───────────────────────────────────────────────────────────────

// StageInto copies the content of src into the inactive slot directory as the
// file named destName (e.g. "os-core.squashfs").  It refuses to write into the
// currently-active slot, ensuring the running OS image is never overwritten.
//
// The write is performed as: write temp file in the slot dir → sync → rename,
// so the destination file is either fully present or absent; never partial.
//
// Parameters:
//   - bs       current BootState (used to identify the inactive slot)
//   - slot     the target slot — must equal InactiveSlot(bs); provided
//     explicitly so callers cannot accidentally stage into the active slot
//   - destName filename within the slot directory (e.g. "os-core.squashfs")
//   - src      source reader whose content will be streamed into the slot
func (sm *SlotManager) StageInto(bs *BootState, slot Slot, destName string, src io.Reader) error {
	if !slot.valid() {
		return ErrInvalidSlot
	}
	if slot == bs.Active {
		return ErrStagingActiveSlot
	}

	slotDir := filepath.Join(sm.cacheDir, slot.dir())
	dest := filepath.Join(slotDir, destName)

	tmp, err := os.CreateTemp(slotDir, destName+".*.tmp")
	if err != nil {
		return fmt.Errorf("osdist/slots: stage create temp in %s: %w", slot.dir(), err)
	}
	tmpPath := tmp.Name()

	if _, err := io.Copy(tmp, src); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("osdist/slots: stage copy into %s: %w", slot.dir(), err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("osdist/slots: stage sync %s: %w", slot.dir(), err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("osdist/slots: stage close %s: %w", slot.dir(), err)
	}
	if err := os.Rename(tmpPath, dest); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("osdist/slots: stage rename in %s: %w", slot.dir(), err)
	}
	return nil
}

// ─── SetPending ──────────────────────────────────────────────────────────────

// SetPending marks slot as the next boot target, resets BootCounter to 0, and
// atomically saves the updated BootState.  slot must be the inactive slot.
func (sm *SlotManager) SetPending(bs *BootState, slot Slot) (*BootState, error) {
	if !slot.valid() {
		return nil, ErrInvalidSlot
	}
	if slot == bs.Active {
		return nil, ErrStagingActiveSlot
	}

	next := &BootState{
		Active:        bs.Active,
		Pending:       slot,
		BootCounter:   0,
		LastKnownGood: bs.LastKnownGood,
	}
	if err := sm.Save(next); err != nil {
		return nil, err
	}
	return next, nil
}

// ─── Flip ─────────────────────────────────────────────────────────────────────

// Flip atomically promotes the pending slot to active, preserving the prior
// active as LastKnownGood for Rollback.  BootCounter is reset to 0.
//
// Flip is a pure pointer swap — no files inside either slot directory are
// touched.  It fails if no Pending slot has been set.
func (sm *SlotManager) Flip(bs *BootState) (*BootState, error) {
	if bs.Pending == SlotNone {
		return nil, ErrNoPending
	}

	next := &BootState{
		Active:        bs.Pending,
		Pending:       SlotNone,
		BootCounter:   0,
		LastKnownGood: bs.Active, // prior active becomes last-known-good
	}
	if err := sm.Save(next); err != nil {
		return nil, err
	}
	return next, nil
}

// ─── Rollback ────────────────────────────────────────────────────────────────

// Rollback reverts the active slot to LastKnownGood and clears any pending
// slot.  It fails if LastKnownGood is unset.
//
// Like Flip, Rollback is a pure pointer swap — no files inside either slot
// directory are modified.
func (sm *SlotManager) Rollback(bs *BootState) (*BootState, error) {
	if bs.LastKnownGood == SlotNone {
		return nil, ErrNoLastKnownGood
	}

	next := &BootState{
		Active:        bs.LastKnownGood,
		Pending:       SlotNone,
		BootCounter:   0,
		LastKnownGood: bs.LastKnownGood, // retain; no new confirmed-good slot yet
	}
	if err := sm.Save(next); err != nil {
		return nil, err
	}
	return next, nil
}
