package osdist

// slots_test.go — unit tests for the A/B slot manager (OSDIST-02).
//
// Test coverage:
//   - Initial state save/load round-trip.
//   - InactiveSlot returns the complement of Active.
//   - StageInto writes content into the inactive slot only.
//   - StageInto refuses to write into the active slot.
//   - SetPending records pending slot and resets boot counter.
//   - Flip promotes pending → active, records prior active as last_known_good.
//   - Rollback reverts active to last_known_good.
//   - Full state-machine walk: initial → stage → set-pending → flip → rollback.
//   - Atomic write: state file is always valid JSON (simulate crash check via
//     repeated save+load cycles).
//   - Active slot directory contents are untouched by staging.
//   - Data partition (separate path) is never accessed.

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ─── Helpers ──────────────────────────────────────────────────────────────────

// newTestManager creates a SlotManager in a fresh temp directory and returns
// both the manager and the cache root path.
func newTestManager(t *testing.T) (*SlotManager, string) {
	t.Helper()
	dir := t.TempDir()
	sm, err := NewSlotManager(dir)
	if err != nil {
		t.Fatalf("NewSlotManager: %v", err)
	}
	return sm, dir
}

// initialState returns a sensible starting BootState with slot-a active.
func initialState() *BootState {
	return &BootState{
		Active:        SlotA,
		Pending:       SlotNone,
		BootCounter:   0,
		LastKnownGood: SlotA,
	}
}

// stateFileIsValid checks that the state file exists and is valid JSON that
// round-trips to a BootState without error.
func stateFileIsValid(t *testing.T, cacheDir string) {
	t.Helper()
	path := filepath.Join(cacheDir, "boot-state.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("stateFileIsValid: read %s: %v", path, err)
	}
	var bs BootState
	if err := json.Unmarshal(data, &bs); err != nil {
		t.Fatalf("stateFileIsValid: unmarshal: %v\ncontent: %s", err, data)
	}
}

// ─── NewSlotManager ──────────────────────────────────────────────────────────

// OSDIST02_TestNewSlotManager_CreatesDirs verifies that NewSlotManager creates
// the slot-a and slot-b subdirectories.
func OSDIST02_TestNewSlotManager_CreatesDirs(t *testing.T) {
	_, cacheDir := newTestManager(t)
	for _, s := range []Slot{SlotA, SlotB} {
		d := filepath.Join(cacheDir, s.dir())
		info, err := os.Stat(d)
		if err != nil {
			t.Errorf("slot dir %s missing: %v", d, err)
			continue
		}
		if !info.IsDir() {
			t.Errorf("slot path %s is not a directory", d)
		}
	}
}

// ─── Save / Load ─────────────────────────────────────────────────────────────

// OSDIST02_TestSaveLoad_RoundTrip verifies that a BootState can be saved and
// loaded back with all fields intact.
func OSDIST02_TestSaveLoad_RoundTrip(t *testing.T) {
	sm, cacheDir := newTestManager(t)
	bs := initialState()

	if err := sm.Save(bs); err != nil {
		t.Fatalf("Save: %v", err)
	}
	stateFileIsValid(t, cacheDir)

	got, err := sm.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Active != bs.Active {
		t.Errorf("Active: got %q want %q", got.Active, bs.Active)
	}
	if got.Pending != bs.Pending {
		t.Errorf("Pending: got %q want %q", got.Pending, bs.Pending)
	}
	if got.BootCounter != bs.BootCounter {
		t.Errorf("BootCounter: got %d want %d", got.BootCounter, bs.BootCounter)
	}
	if got.LastKnownGood != bs.LastKnownGood {
		t.Errorf("LastKnownGood: got %q want %q", got.LastKnownGood, bs.LastKnownGood)
	}
}

// OSDIST02_TestSave_AtomicWrite verifies that repeated save operations always
// leave the state file in a valid, parseable state (simulates atomicity by
// checking after every write).
func OSDIST02_TestSave_AtomicWrite(t *testing.T) {
	sm, cacheDir := newTestManager(t)

	states := []*BootState{
		{Active: SlotA, Pending: SlotNone, BootCounter: 0, LastKnownGood: SlotA},
		{Active: SlotA, Pending: SlotB, BootCounter: 0, LastKnownGood: SlotA},
		{Active: SlotB, Pending: SlotNone, BootCounter: 0, LastKnownGood: SlotA},
		{Active: SlotA, Pending: SlotNone, BootCounter: 0, LastKnownGood: SlotB},
	}

	for i, s := range states {
		if err := sm.Save(s); err != nil {
			t.Fatalf("iteration %d: Save: %v", i, err)
		}
		stateFileIsValid(t, cacheDir)

		loaded, err := sm.Load()
		if err != nil {
			t.Fatalf("iteration %d: Load: %v", i, err)
		}
		if loaded.Active != s.Active || loaded.Pending != s.Pending {
			t.Errorf("iteration %d: state mismatch after save/load", i)
		}
	}
}

// OSDIST02_TestLoad_MissingFile verifies that Load returns an error when
// boot-state.json does not exist.
func OSDIST02_TestLoad_MissingFile(t *testing.T) {
	sm, _ := newTestManager(t)
	_, err := sm.Load()
	if err == nil {
		t.Fatal("expected error when boot-state.json is absent")
	}
}

// OSDIST02_TestSave_InvalidState verifies that Save rejects a BootState with
// invalid field values.
func OSDIST02_TestSave_InvalidState(t *testing.T) {
	sm, _ := newTestManager(t)

	cases := []struct {
		name string
		bs   *BootState
	}{
		{"empty active", &BootState{Active: SlotNone, Pending: SlotNone, BootCounter: 0, LastKnownGood: SlotA}},
		{"bad active", &BootState{Active: "c", Pending: SlotNone, BootCounter: 0, LastKnownGood: SlotA}},
		{"bad pending", &BootState{Active: SlotA, Pending: "x", BootCounter: 0, LastKnownGood: SlotA}},
		{"negative counter", &BootState{Active: SlotA, Pending: SlotNone, BootCounter: -1, LastKnownGood: SlotA}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := sm.Save(tc.bs); err == nil {
				t.Fatalf("expected error for invalid state %+v", tc.bs)
			}
		})
	}
}

// ─── InactiveSlot ─────────────────────────────────────────────────────────────

// OSDIST02_TestInactiveSlot verifies that InactiveSlot returns the complement
// of the current active slot.
func OSDIST02_TestInactiveSlot(t *testing.T) {
	sm, _ := newTestManager(t)

	cases := []struct {
		active Slot
		want   Slot
	}{
		{SlotA, SlotB},
		{SlotB, SlotA},
	}
	for _, tc := range cases {
		bs := &BootState{Active: tc.active, LastKnownGood: tc.active}
		got, err := sm.InactiveSlot(bs)
		if err != nil {
			t.Errorf("InactiveSlot(active=%q): %v", tc.active, err)
			continue
		}
		if got != tc.want {
			t.Errorf("InactiveSlot(active=%q) = %q, want %q", tc.active, got, tc.want)
		}
	}
}

// ─── StageInto ───────────────────────────────────────────────────────────────

// OSDIST02_TestStageInto_WritesToInactiveSlot verifies that StageInto creates
// the destination file in the inactive slot with the correct content.
func OSDIST02_TestStageInto_WritesToInactiveSlot(t *testing.T) {
	sm, cacheDir := newTestManager(t)

	content := []byte("fake squashfs image bytes")
	bs := initialState() // active = a  →  inactive = b

	if err := sm.StageInto(bs, SlotB, "os-core.squashfs", bytes.NewReader(content)); err != nil {
		t.Fatalf("StageInto: %v", err)
	}

	dest := filepath.Join(cacheDir, "slot-b", "os-core.squashfs")
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read staged file: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Errorf("staged content mismatch: got %q want %q", got, content)
	}
}

// OSDIST02_TestStageInto_RefusesActiveSlot verifies that StageInto returns
// ErrStagingActiveSlot when the caller passes the active slot as the target.
func OSDIST02_TestStageInto_RefusesActiveSlot(t *testing.T) {
	sm, _ := newTestManager(t)
	bs := initialState() // active = a

	err := sm.StageInto(bs, SlotA, "os-core.squashfs", strings.NewReader("data"))
	if !errors.Is(err, ErrStagingActiveSlot) {
		t.Fatalf("expected ErrStagingActiveSlot, got: %v", err)
	}
}

// OSDIST02_TestStageInto_ActiveSlotUntouched verifies that the active slot
// directory contains no new files after staging into the inactive slot.
func OSDIST02_TestStageInto_ActiveSlotUntouched(t *testing.T) {
	sm, cacheDir := newTestManager(t)
	bs := initialState() // active = a  →  inactive = b

	// Record the initial contents of the active slot directory.
	entriesBefore, err := os.ReadDir(filepath.Join(cacheDir, "slot-a"))
	if err != nil {
		t.Fatalf("ReadDir slot-a before: %v", err)
	}

	if err := sm.StageInto(bs, SlotB, "os-core.squashfs", strings.NewReader("img")); err != nil {
		t.Fatalf("StageInto: %v", err)
	}

	entriesAfter, err := os.ReadDir(filepath.Join(cacheDir, "slot-a"))
	if err != nil {
		t.Fatalf("ReadDir slot-a after: %v", err)
	}

	if len(entriesBefore) != len(entriesAfter) {
		t.Errorf("active slot-a modified: before=%d entries after=%d entries",
			len(entriesBefore), len(entriesAfter))
	}
}

// OSDIST02_TestStageInto_NoTempFilesLeft verifies that no .tmp files are left
// in the slot directory after a successful stage.
func OSDIST02_TestStageInto_NoTempFilesLeft(t *testing.T) {
	sm, cacheDir := newTestManager(t)
	bs := initialState()

	if err := sm.StageInto(bs, SlotB, "os-core.squashfs", strings.NewReader("img")); err != nil {
		t.Fatalf("StageInto: %v", err)
	}

	entries, err := os.ReadDir(filepath.Join(cacheDir, "slot-b"))
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("temp file left in slot-b: %s", e.Name())
		}
	}
}

// ─── SetPending ──────────────────────────────────────────────────────────────

// OSDIST02_TestSetPending_RecordsPendingSlot verifies that SetPending saves the
// correct pending slot and resets the boot counter.
func OSDIST02_TestSetPending_RecordsPendingSlot(t *testing.T) {
	sm, cacheDir := newTestManager(t)
	bs := initialState()
	if err := sm.Save(bs); err != nil {
		t.Fatalf("Save: %v", err)
	}

	next, err := sm.SetPending(bs, SlotB)
	if err != nil {
		t.Fatalf("SetPending: %v", err)
	}
	stateFileIsValid(t, cacheDir)

	if next.Pending != SlotB {
		t.Errorf("Pending: got %q want %q", next.Pending, SlotB)
	}
	if next.BootCounter != 0 {
		t.Errorf("BootCounter: got %d want 0", next.BootCounter)
	}
	if next.Active != SlotA {
		t.Errorf("Active should still be %q, got %q", SlotA, next.Active)
	}
}

// OSDIST02_TestSetPending_RefusesActiveSlot verifies that SetPending rejects
// the currently-active slot.
func OSDIST02_TestSetPending_RefusesActiveSlot(t *testing.T) {
	sm, _ := newTestManager(t)
	bs := initialState()

	_, err := sm.SetPending(bs, SlotA)
	if !errors.Is(err, ErrStagingActiveSlot) {
		t.Fatalf("expected ErrStagingActiveSlot, got: %v", err)
	}
}

// ─── Flip ─────────────────────────────────────────────────────────────────────

// OSDIST02_TestFlip_PromotesPending verifies that Flip atomically swaps active
// and pending, recording the prior active as last_known_good.
func OSDIST02_TestFlip_PromotesPending(t *testing.T) {
	sm, cacheDir := newTestManager(t)

	bs := &BootState{
		Active:        SlotA,
		Pending:       SlotB,
		BootCounter:   0,
		LastKnownGood: SlotA,
	}
	if err := sm.Save(bs); err != nil {
		t.Fatalf("Save: %v", err)
	}

	next, err := sm.Flip(bs)
	if err != nil {
		t.Fatalf("Flip: %v", err)
	}
	stateFileIsValid(t, cacheDir)

	if next.Active != SlotB {
		t.Errorf("Active after flip: got %q want %q", next.Active, SlotB)
	}
	if next.Pending != SlotNone {
		t.Errorf("Pending after flip: got %q want %q", next.Pending, SlotNone)
	}
	if next.LastKnownGood != SlotA {
		t.Errorf("LastKnownGood after flip: got %q want %q", next.LastKnownGood, SlotA)
	}
	if next.BootCounter != 0 {
		t.Errorf("BootCounter after flip: got %d want 0", next.BootCounter)
	}
}

// OSDIST02_TestFlip_NoPending verifies that Flip returns ErrNoPending when no
// pending slot is set.
func OSDIST02_TestFlip_NoPending(t *testing.T) {
	sm, _ := newTestManager(t)
	bs := initialState() // Pending = ""

	_, err := sm.Flip(bs)
	if !errors.Is(err, ErrNoPending) {
		t.Fatalf("expected ErrNoPending, got: %v", err)
	}
}

// OSDIST02_TestFlip_IsReversible verifies that after a Flip, Rollback restores
// the original active slot.
func OSDIST02_TestFlip_IsReversible(t *testing.T) {
	sm, cacheDir := newTestManager(t)

	bs := &BootState{
		Active:        SlotA,
		Pending:       SlotB,
		BootCounter:   0,
		LastKnownGood: SlotA,
	}
	if err := sm.Save(bs); err != nil {
		t.Fatalf("Save: %v", err)
	}

	flipped, err := sm.Flip(bs)
	if err != nil {
		t.Fatalf("Flip: %v", err)
	}
	stateFileIsValid(t, cacheDir)

	reverted, err := sm.Rollback(flipped)
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	stateFileIsValid(t, cacheDir)

	if reverted.Active != SlotA {
		t.Errorf("Active after rollback: got %q want %q", reverted.Active, SlotA)
	}
}

// ─── Rollback ────────────────────────────────────────────────────────────────

// OSDIST02_TestRollback_RevertsToLastKnownGood verifies that Rollback sets
// Active = LastKnownGood and clears any pending slot.
func OSDIST02_TestRollback_RevertsToLastKnownGood(t *testing.T) {
	sm, cacheDir := newTestManager(t)

	bs := &BootState{
		Active:        SlotB,
		Pending:       SlotNone,
		BootCounter:   2,
		LastKnownGood: SlotA,
	}
	if err := sm.Save(bs); err != nil {
		t.Fatalf("Save: %v", err)
	}

	next, err := sm.Rollback(bs)
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	stateFileIsValid(t, cacheDir)

	if next.Active != SlotA {
		t.Errorf("Active after rollback: got %q want %q", next.Active, SlotA)
	}
	if next.Pending != SlotNone {
		t.Errorf("Pending after rollback: got %q want empty", next.Pending)
	}
	if next.BootCounter != 0 {
		t.Errorf("BootCounter after rollback: got %d want 0", next.BootCounter)
	}
}

// OSDIST02_TestRollback_NoLastKnownGood verifies that Rollback returns
// ErrNoLastKnownGood when last_known_good is unset.
func OSDIST02_TestRollback_NoLastKnownGood(t *testing.T) {
	sm, _ := newTestManager(t)

	bs := &BootState{
		Active:        SlotB,
		Pending:       SlotNone,
		BootCounter:   0,
		LastKnownGood: SlotNone,
	}

	_, err := sm.Rollback(bs)
	if !errors.Is(err, ErrNoLastKnownGood) {
		t.Fatalf("expected ErrNoLastKnownGood, got: %v", err)
	}
}

// ─── Full state-machine walk ──────────────────────────────────────────────────

// OSDIST02_TestStateMachine_FullWalk exercises the complete update flow:
// initial state → stage image → set-pending → flip → rollback.
//
// Assertions:
//   - New image lands in the inactive slot only.
//   - Active slot directory content is untouched throughout.
//   - State file is always valid JSON after every operation.
//   - Flip is atomic and reversible via Rollback.
//   - Data partition path (separate dir) is never accessed.
func OSDIST02_TestStateMachine_FullWalk(t *testing.T) {
	sm, cacheDir := newTestManager(t)

	// 1. Initialise: slot-a is active and last-known-good.
	bs := initialState()
	if err := sm.Save(bs); err != nil {
		t.Fatalf("initial Save: %v", err)
	}
	stateFileIsValid(t, cacheDir)

	// Place a sentinel file in the active slot to prove it is never touched.
	sentinelPath := filepath.Join(cacheDir, "slot-a", "os-core.squashfs")
	sentinelContent := []byte("ACTIVE-SLOT-SENTINEL")
	if err := os.WriteFile(sentinelPath, sentinelContent, 0o644); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}

	// 2. Determine inactive slot and stage new image there.
	inactive, err := sm.InactiveSlot(bs)
	if err != nil {
		t.Fatalf("InactiveSlot: %v", err)
	}
	if inactive != SlotB {
		t.Fatalf("expected inactive=b, got %q", inactive)
	}

	newImage := []byte("NEW-OS-IMAGE-v08")
	if err := sm.StageInto(bs, inactive, "os-core.squashfs", bytes.NewReader(newImage)); err != nil {
		t.Fatalf("StageInto: %v", err)
	}
	stateFileIsValid(t, cacheDir)

	// Verify the new image is in slot-b.
	stagedPath := filepath.Join(cacheDir, "slot-b", "os-core.squashfs")
	got, err := os.ReadFile(stagedPath)
	if err != nil {
		t.Fatalf("read staged: %v", err)
	}
	if !bytes.Equal(got, newImage) {
		t.Errorf("staged content: got %q want %q", got, newImage)
	}

	// Verify the active slot sentinel is untouched.
	sentinelGot, err := os.ReadFile(sentinelPath)
	if err != nil {
		t.Fatalf("read sentinel: %v", err)
	}
	if !bytes.Equal(sentinelGot, sentinelContent) {
		t.Errorf("active slot sentinel modified: got %q want %q", sentinelGot, sentinelContent)
	}

	// 3. Set pending.
	bs, err = sm.SetPending(bs, inactive)
	if err != nil {
		t.Fatalf("SetPending: %v", err)
	}
	stateFileIsValid(t, cacheDir)
	if bs.Pending != SlotB {
		t.Errorf("Pending after SetPending: got %q want %q", bs.Pending, SlotB)
	}
	if bs.BootCounter != 0 {
		t.Errorf("BootCounter after SetPending: got %d want 0", bs.BootCounter)
	}

	// 4. Flip: slot-b becomes active, slot-a becomes last-known-good.
	bs, err = sm.Flip(bs)
	if err != nil {
		t.Fatalf("Flip: %v", err)
	}
	stateFileIsValid(t, cacheDir)
	if bs.Active != SlotB {
		t.Errorf("Active after flip: got %q want %q", bs.Active, SlotB)
	}
	if bs.LastKnownGood != SlotA {
		t.Errorf("LastKnownGood after flip: got %q want %q", bs.LastKnownGood, SlotA)
	}

	// The slot-a sentinel must still be untouched — flip is a pointer swap only.
	sentinelGot, err = os.ReadFile(sentinelPath)
	if err != nil {
		t.Fatalf("read sentinel after flip: %v", err)
	}
	if !bytes.Equal(sentinelGot, sentinelContent) {
		t.Errorf("active slot sentinel modified by flip: got %q want %q", sentinelGot, sentinelContent)
	}

	// 5. Rollback: revert to slot-a.
	bs, err = sm.Rollback(bs)
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	stateFileIsValid(t, cacheDir)
	if bs.Active != SlotA {
		t.Errorf("Active after rollback: got %q want %q", bs.Active, SlotA)
	}
	if bs.Pending != SlotNone {
		t.Errorf("Pending after rollback: got %q want empty", bs.Pending)
	}

	// 6. Data partition untouched: a separate "data" directory must not exist.
	dataDir := filepath.Join(cacheDir, "data")
	if _, err := os.Stat(dataDir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("data partition directory %s was created — should never be touched", dataDir)
	}
}

// OSDIST02_TestStateMachine_LoadAfterFlip verifies that the state file loaded
// after a Flip reflects the new active slot.
func OSDIST02_TestStateMachine_LoadAfterFlip(t *testing.T) {
	sm, cacheDir := newTestManager(t)

	bs := &BootState{
		Active:        SlotA,
		Pending:       SlotB,
		BootCounter:   0,
		LastKnownGood: SlotA,
	}
	if err := sm.Save(bs); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if _, err := sm.Flip(bs); err != nil {
		t.Fatalf("Flip: %v", err)
	}
	stateFileIsValid(t, cacheDir)

	loaded, err := sm.Load()
	if err != nil {
		t.Fatalf("Load after flip: %v", err)
	}
	if loaded.Active != SlotB {
		t.Errorf("loaded.Active after flip: got %q want %q", loaded.Active, SlotB)
	}
	if loaded.LastKnownGood != SlotA {
		t.Errorf("loaded.LastKnownGood after flip: got %q want %q", loaded.LastKnownGood, SlotA)
	}
}

// OSDIST02_TestStageInto_LargePayload verifies that StageInto correctly handles
// a multi-chunk reader (simulating a real squashfs download).
func OSDIST02_TestStageInto_LargePayload(t *testing.T) {
	sm, cacheDir := newTestManager(t)
	bs := initialState()

	// Build a 256 KiB payload.
	payload := bytes.Repeat([]byte("VULOS"), 256*1024/5+1)
	payload = payload[:256*1024]

	if err := sm.StageInto(bs, SlotB, "os-core.squashfs", bytes.NewReader(payload)); err != nil {
		t.Fatalf("StageInto large payload: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(cacheDir, "slot-b", "os-core.squashfs"))
	if err != nil {
		t.Fatalf("read large staged: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("large payload content mismatch (lengths: got %d want %d)", len(got), len(payload))
	}
}

// OSDIST02_TestSlotDir_Valid verifies SlotDir returns the correct paths.
func OSDIST02_TestSlotDir_Valid(t *testing.T) {
	sm, cacheDir := newTestManager(t)

	for _, s := range []Slot{SlotA, SlotB} {
		got, err := sm.SlotDir(s)
		if err != nil {
			t.Errorf("SlotDir(%q): %v", s, err)
			continue
		}
		want := filepath.Join(cacheDir, s.dir())
		if got != want {
			t.Errorf("SlotDir(%q) = %q, want %q", s, got, want)
		}
	}
}

// OSDIST02_TestSlotDir_Invalid verifies SlotDir returns ErrInvalidSlot for
// unknown slot identifiers.
func OSDIST02_TestSlotDir_Invalid(t *testing.T) {
	sm, _ := newTestManager(t)

	_, err := sm.SlotDir("c")
	if !errors.Is(err, ErrInvalidSlot) {
		t.Fatalf("expected ErrInvalidSlot for slot \"c\", got: %v", err)
	}

	_, err = sm.SlotDir(SlotNone)
	if !errors.Is(err, ErrInvalidSlot) {
		t.Fatalf("expected ErrInvalidSlot for empty slot, got: %v", err)
	}
}

// ─── OSDIST-03: boot-counter helpers ─────────────────────────────────────────

// OSDIST03_TestIncrementBootCounter verifies that IncrementBootCounter adds 1
// to the counter each call and persists the change atomically.
func OSDIST03_TestIncrementBootCounter(t *testing.T) {
	sm, cacheDir := newTestManager(t)
	bs := initialState()
	if err := sm.Save(bs); err != nil {
		t.Fatalf("Save: %v", err)
	}

	for i := 1; i <= 5; i++ {
		next, err := sm.IncrementBootCounter(bs)
		if err != nil {
			t.Fatalf("IncrementBootCounter (iter %d): %v", i, err)
		}
		if next.BootCounter != i {
			t.Errorf("iter %d: BootCounter = %d, want %d", i, next.BootCounter, i)
		}
		stateFileIsValid(t, cacheDir)

		// Reload from disk to confirm persistence.
		loaded, err := sm.Load()
		if err != nil {
			t.Fatalf("iter %d: Load: %v", i, err)
		}
		if loaded.BootCounter != i {
			t.Errorf("iter %d: persisted BootCounter = %d, want %d", i, loaded.BootCounter, i)
		}
		bs = next
	}
}

// OSDIST03_TestIncrementBootCounter_PreservesOtherFields confirms that
// IncrementBootCounter does not alter Active, Pending, or LastKnownGood.
func OSDIST03_TestIncrementBootCounter_PreservesOtherFields(t *testing.T) {
	sm, _ := newTestManager(t)
	bs := &BootState{
		Active:        SlotB,
		Pending:       SlotNone,
		BootCounter:   2,
		LastKnownGood: SlotA,
	}
	if err := sm.Save(bs); err != nil {
		t.Fatalf("Save: %v", err)
	}

	next, err := sm.IncrementBootCounter(bs)
	if err != nil {
		t.Fatalf("IncrementBootCounter: %v", err)
	}
	if next.Active != SlotB {
		t.Errorf("Active changed: got %q want %q", next.Active, SlotB)
	}
	if next.Pending != SlotNone {
		t.Errorf("Pending changed: got %q want %q", next.Pending, SlotNone)
	}
	if next.LastKnownGood != SlotA {
		t.Errorf("LastKnownGood changed: got %q want %q", next.LastKnownGood, SlotA)
	}
	if next.BootCounter != 3 {
		t.Errorf("BootCounter: got %d want 3", next.BootCounter)
	}
}

// OSDIST03_TestShouldRollback_BelowThreshold verifies that ShouldRollback
// returns false when the counter is below the threshold.
func OSDIST03_TestShouldRollback_BelowThreshold(t *testing.T) {
	bs := &BootState{
		Active:        SlotB,
		BootCounter:   2,
		LastKnownGood: SlotA,
	}
	if ShouldRollback(bs, 3) {
		t.Errorf("ShouldRollback should be false when counter(%d) < threshold(%d)", bs.BootCounter, 3)
	}
}

// OSDIST03_TestShouldRollback_AtThreshold verifies that ShouldRollback returns
// true when the counter equals the threshold.
func OSDIST03_TestShouldRollback_AtThreshold(t *testing.T) {
	bs := &BootState{
		Active:        SlotB,
		BootCounter:   3,
		LastKnownGood: SlotA,
	}
	if !ShouldRollback(bs, 3) {
		t.Errorf("ShouldRollback should be true when counter(%d) == threshold(%d)", bs.BootCounter, 3)
	}
}

// OSDIST03_TestShouldRollback_AboveThreshold verifies that ShouldRollback
// returns true when the counter exceeds the threshold.
func OSDIST03_TestShouldRollback_AboveThreshold(t *testing.T) {
	bs := &BootState{
		Active:        SlotB,
		BootCounter:   5,
		LastKnownGood: SlotA,
	}
	if !ShouldRollback(bs, 3) {
		t.Errorf("ShouldRollback should be true when counter(%d) > threshold(%d)", bs.BootCounter, 3)
	}
}

// OSDIST03_TestShouldRollback_NoLastKnownGood verifies that ShouldRollback
// returns false when there is no last-known-good slot, regardless of the
// counter — there is nowhere safe to fall back to.
func OSDIST03_TestShouldRollback_NoLastKnownGood(t *testing.T) {
	bs := &BootState{
		Active:        SlotA,
		BootCounter:   10,
		LastKnownGood: SlotNone,
	}
	if ShouldRollback(bs, 1) {
		t.Errorf("ShouldRollback should be false when LastKnownGood is unset")
	}
}

// OSDIST03_TestShouldRollback_DefaultThreshold verifies that a threshold ≤ 0
// falls back to DefaultBootThreshold.
func OSDIST03_TestShouldRollback_DefaultThreshold(t *testing.T) {
	bs := &BootState{
		Active:        SlotB,
		BootCounter:   DefaultBootThreshold,
		LastKnownGood: SlotA,
	}
	if !ShouldRollback(bs, 0) {
		t.Errorf("ShouldRollback(threshold=0) should use DefaultBootThreshold=%d and return true for counter=%d",
			DefaultBootThreshold, bs.BootCounter)
	}
	bsBelow := &BootState{
		Active:        SlotB,
		BootCounter:   DefaultBootThreshold - 1,
		LastKnownGood: SlotA,
	}
	if ShouldRollback(bsBelow, -1) {
		t.Errorf("ShouldRollback(threshold=-1) should use DefaultBootThreshold=%d and return false for counter=%d",
			DefaultBootThreshold, bsBelow.BootCounter)
	}
}

// OSDIST03_TestMarkHealthy_ResetsCounterAndPromotesSlot verifies that
// MarkHealthy resets BootCounter to 0 and sets LastKnownGood = Active.
func OSDIST03_TestMarkHealthy_ResetsCounterAndPromotesSlot(t *testing.T) {
	sm, cacheDir := newTestManager(t)
	bs := &BootState{
		Active:        SlotB,
		Pending:       SlotNone,
		BootCounter:   2,
		LastKnownGood: SlotA, // prior good slot
	}
	if err := sm.Save(bs); err != nil {
		t.Fatalf("Save: %v", err)
	}

	next, err := sm.MarkHealthy(bs)
	if err != nil {
		t.Fatalf("MarkHealthy: %v", err)
	}
	stateFileIsValid(t, cacheDir)

	if next.BootCounter != 0 {
		t.Errorf("BootCounter after MarkHealthy: got %d want 0", next.BootCounter)
	}
	if next.LastKnownGood != SlotB {
		t.Errorf("LastKnownGood after MarkHealthy: got %q want %q (active slot)", next.LastKnownGood, SlotB)
	}
	if next.Active != SlotB {
		t.Errorf("Active changed unexpectedly: got %q want %q", next.Active, SlotB)
	}
	if next.Pending != SlotNone {
		t.Errorf("Pending changed unexpectedly: got %q want %q", next.Pending, SlotNone)
	}
}

// OSDIST03_TestMarkHealthy_Persisted verifies that the healthy state is
// actually written to disk.
func OSDIST03_TestMarkHealthy_Persisted(t *testing.T) {
	sm, _ := newTestManager(t)
	bs := &BootState{
		Active:        SlotA,
		Pending:       SlotNone,
		BootCounter:   1,
		LastKnownGood: SlotB,
	}
	if err := sm.Save(bs); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if _, err := sm.MarkHealthy(bs); err != nil {
		t.Fatalf("MarkHealthy: %v", err)
	}

	loaded, err := sm.Load()
	if err != nil {
		t.Fatalf("Load after MarkHealthy: %v", err)
	}
	if loaded.BootCounter != 0 {
		t.Errorf("persisted BootCounter: got %d want 0", loaded.BootCounter)
	}
	if loaded.LastKnownGood != SlotA {
		t.Errorf("persisted LastKnownGood: got %q want %q", loaded.LastKnownGood, SlotA)
	}
}

// OSDIST03_TestBootCounterRollbackFlow exercises the full unhealthy-boot flow:
// increment counter N times past threshold → ShouldRollback → Rollback.
func OSDIST03_TestBootCounterRollbackFlow(t *testing.T) {
	sm, cacheDir := newTestManager(t)

	bs := &BootState{
		Active:        SlotB,
		Pending:       SlotNone,
		BootCounter:   0,
		LastKnownGood: SlotA,
	}
	if err := sm.Save(bs); err != nil {
		t.Fatalf("Save: %v", err)
	}

	const threshold = 3

	// Simulate 3 failed boots (counter reaches threshold).
	for i := 1; i <= threshold; i++ {
		next, err := sm.IncrementBootCounter(bs)
		if err != nil {
			t.Fatalf("IncrementBootCounter (boot %d): %v", i, err)
		}
		bs = next

		if i < threshold && ShouldRollback(bs, threshold) {
			t.Errorf("boot %d: ShouldRollback returned true before reaching threshold %d", i, threshold)
		}
	}

	// Now the counter equals threshold → should rollback.
	if !ShouldRollback(bs, threshold) {
		t.Fatalf("ShouldRollback: expected true at counter=%d threshold=%d", bs.BootCounter, threshold)
	}

	rolled, err := sm.Rollback(bs)
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	stateFileIsValid(t, cacheDir)

	if rolled.Active != SlotA {
		t.Errorf("Active after rollback: got %q want %q (last_known_good)", rolled.Active, SlotA)
	}
	if rolled.BootCounter != 0 {
		t.Errorf("BootCounter after rollback: got %d want 0", rolled.BootCounter)
	}
}

// OSDIST03_TestHealthyBootFlow exercises the full healthy-boot flow:
// increment counter → server up → MarkHealthy → counter reset.
func OSDIST03_TestHealthyBootFlow(t *testing.T) {
	sm, cacheDir := newTestManager(t)

	bs := &BootState{
		Active:        SlotB,
		Pending:       SlotNone,
		BootCounter:   0,
		LastKnownGood: SlotA,
	}
	if err := sm.Save(bs); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Increment (simulate boot started).
	bs, err := sm.IncrementBootCounter(bs)
	if err != nil {
		t.Fatalf("IncrementBootCounter: %v", err)
	}
	if bs.BootCounter != 1 {
		t.Fatalf("expected BootCounter=1, got %d", bs.BootCounter)
	}
	stateFileIsValid(t, cacheDir)

	// Services came up → mark healthy.
	healthy, err := sm.MarkHealthy(bs)
	if err != nil {
		t.Fatalf("MarkHealthy: %v", err)
	}
	stateFileIsValid(t, cacheDir)

	if healthy.BootCounter != 0 {
		t.Errorf("BootCounter after MarkHealthy: got %d want 0", healthy.BootCounter)
	}
	if healthy.LastKnownGood != SlotB {
		t.Errorf("LastKnownGood after MarkHealthy: got %q want %q", healthy.LastKnownGood, SlotB)
	}
	// ShouldRollback must now be false.
	if ShouldRollback(healthy, DefaultBootThreshold) {
		t.Errorf("ShouldRollback should be false after MarkHealthy")
	}
}

// ─── Standard test entry points ──────────────────────────────────────────────

func TestNewSlotManager_CreatesDirs(t *testing.T) { OSDIST02_TestNewSlotManager_CreatesDirs(t) }
func TestSaveLoad_RoundTrip(t *testing.T)         { OSDIST02_TestSaveLoad_RoundTrip(t) }
func TestSave_AtomicWrite(t *testing.T)           { OSDIST02_TestSave_AtomicWrite(t) }
func TestLoad_MissingFile(t *testing.T)           { OSDIST02_TestLoad_MissingFile(t) }
func TestSave_InvalidState(t *testing.T)          { OSDIST02_TestSave_InvalidState(t) }
func TestInactiveSlot(t *testing.T)               { OSDIST02_TestInactiveSlot(t) }
func TestStageInto_WritesToInactiveSlot(t *testing.T) {
	OSDIST02_TestStageInto_WritesToInactiveSlot(t)
}
func TestStageInto_RefusesActiveSlot(t *testing.T) { OSDIST02_TestStageInto_RefusesActiveSlot(t) }
func TestStageInto_ActiveSlotUntouched(t *testing.T) {
	OSDIST02_TestStageInto_ActiveSlotUntouched(t)
}
func TestStageInto_NoTempFilesLeft(t *testing.T)     { OSDIST02_TestStageInto_NoTempFilesLeft(t) }
func TestSetPending_RecordsPendingSlot(t *testing.T) { OSDIST02_TestSetPending_RecordsPendingSlot(t) }
func TestSetPending_RefusesActiveSlot(t *testing.T)  { OSDIST02_TestSetPending_RefusesActiveSlot(t) }
func TestFlip_PromotesPending(t *testing.T)          { OSDIST02_TestFlip_PromotesPending(t) }
func TestFlip_NoPending(t *testing.T)                { OSDIST02_TestFlip_NoPending(t) }
func TestFlip_IsReversible(t *testing.T)             { OSDIST02_TestFlip_IsReversible(t) }
func TestRollback_RevertsToLastKnownGood(t *testing.T) {
	OSDIST02_TestRollback_RevertsToLastKnownGood(t)
}
func TestRollback_NoLastKnownGood(t *testing.T) { OSDIST02_TestRollback_NoLastKnownGood(t) }
func TestStateMachine_FullWalk(t *testing.T)    { OSDIST02_TestStateMachine_FullWalk(t) }
func TestStateMachine_LoadAfterFlip(t *testing.T) {
	OSDIST02_TestStateMachine_LoadAfterFlip(t)
}
func TestStageInto_LargePayload(t *testing.T) { OSDIST02_TestStageInto_LargePayload(t) }
func TestSlotDir_Valid(t *testing.T)          { OSDIST02_TestSlotDir_Valid(t) }
func TestSlotDir_Invalid(t *testing.T)        { OSDIST02_TestSlotDir_Invalid(t) }

// OSDIST-03 entry points
func TestIncrementBootCounter(t *testing.T) { OSDIST03_TestIncrementBootCounter(t) }
func TestIncrementBootCounter_PreservesOtherFields(t *testing.T) {
	OSDIST03_TestIncrementBootCounter_PreservesOtherFields(t)
}
func TestShouldRollback_BelowThreshold(t *testing.T) {
	OSDIST03_TestShouldRollback_BelowThreshold(t)
}
func TestShouldRollback_AtThreshold(t *testing.T) { OSDIST03_TestShouldRollback_AtThreshold(t) }
func TestShouldRollback_AboveThreshold(t *testing.T) {
	OSDIST03_TestShouldRollback_AboveThreshold(t)
}
func TestShouldRollback_NoLastKnownGood(t *testing.T) {
	OSDIST03_TestShouldRollback_NoLastKnownGood(t)
}
func TestShouldRollback_DefaultThreshold(t *testing.T) {
	OSDIST03_TestShouldRollback_DefaultThreshold(t)
}
func TestMarkHealthy_ResetsCounterAndPromotesSlot(t *testing.T) {
	OSDIST03_TestMarkHealthy_ResetsCounterAndPromotesSlot(t)
}
func TestMarkHealthy_Persisted(t *testing.T)   { OSDIST03_TestMarkHealthy_Persisted(t) }
func TestBootCounterRollbackFlow(t *testing.T) { OSDIST03_TestBootCounterRollbackFlow(t) }
func TestHealthyBootFlow(t *testing.T)         { OSDIST03_TestHealthyBootFlow(t) }

// ─── Unused import guard ──────────────────────────────────────────────────────
// io is used by StageInto via io.Reader in the API; confirm it's importable.
var _ io.Reader
