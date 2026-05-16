package network

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// newServiceForModeTest returns a bare Service suitable for exercising the
// NET-09 mode methods without exercising the full New() identity wiring.
// We construct the Service directly to keep the test focused on the additive
// surface and to avoid coupling to config.Config in the test path.
func newServiceForModeTest(t *testing.T) *Service {
	t.Helper()
	return &Service{}
}

// modePath returns the canonical persistence location for a given home root.
func modePathFor(home string) string {
	return filepath.Join(home, ".vulos", "db", "network-mode.json")
}

func TestLoadMode_DefaultsToFabricAndPersistsAt0600(t *testing.T) {
	home := t.TempDir()
	s := newServiceForModeTest(t)

	s.LoadMode(home)

	if got := s.GetMode(); got != ModeFabric {
		t.Fatalf("default mode: got %q, want %q", got, ModeFabric)
	}

	path := modePathFor(home)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("expected mode file at %s: %v", path, err)
	}

	// File-mode permission bits — skip on Windows where 0600 semantics differ.
	if runtime.GOOS != "windows" {
		if perm := info.Mode().Perm(); perm != 0600 {
			t.Errorf("mode file perm: got %#o, want 0600", perm)
		}
	}

	// Sanity: file contains a recognisable {"mode":"fabric"} payload.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var f net9ModeFile
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatalf("mode file json: %v", err)
	}
	if f.Mode != ModeFabric {
		t.Errorf("persisted mode: got %q, want %q", f.Mode, ModeFabric)
	}
}

func TestSetMode_RoundTrip(t *testing.T) {
	home := t.TempDir()
	s := newServiceForModeTest(t)
	s.LoadMode(home)

	for _, m := range []ConnectionMode{ModeDirect, ModeOwn, ModeLocal, ModeFabric} {
		if err := s.SetMode(m); err != nil {
			t.Fatalf("SetMode(%q): %v", m, err)
		}
		if got := s.GetMode(); got != m {
			t.Errorf("GetMode after SetMode(%q): got %q", m, got)
		}

		// Verify on-disk state matches.
		raw, err := os.ReadFile(modePathFor(home))
		if err != nil {
			t.Fatal(err)
		}
		var f net9ModeFile
		if err := json.Unmarshal(raw, &f); err != nil {
			t.Fatal(err)
		}
		if f.Mode != m {
			t.Errorf("on-disk mode after SetMode(%q): got %q", m, f.Mode)
		}
	}
}

func TestSetMode_RejectsInvalid(t *testing.T) {
	home := t.TempDir()
	s := newServiceForModeTest(t)
	s.LoadMode(home)

	for _, bad := range []ConnectionMode{"", "FABRIC", "fabric ", "wan", "p2p", "fAbRiC"} {
		if err := s.SetMode(bad); err == nil {
			t.Errorf("SetMode(%q): expected error, got nil", bad)
		}
	}

	// Mode should remain the default after the failed sets.
	if got := s.GetMode(); got != ModeFabric {
		t.Errorf("mode after invalid SetMode calls: got %q, want %q", got, ModeFabric)
	}
}

func TestLocalMode_BlocksExternalListener(t *testing.T) {
	home := t.TempDir()
	s := newServiceForModeTest(t)
	s.LoadMode(home)

	if s.ExternalListenerBlocked() {
		t.Fatalf("ExternalListenerBlocked should be false in default (fabric) mode")
	}

	if err := s.SetMode(ModeLocal); err != nil {
		t.Fatalf("SetMode(local): %v", err)
	}
	if !s.ExternalListenerBlocked() {
		t.Errorf("ExternalListenerBlocked should be true after switching to local mode")
	}

	// Switching away from local clears the flag.
	if err := s.SetMode(ModeFabric); err != nil {
		t.Fatalf("SetMode(fabric): %v", err)
	}
	if s.ExternalListenerBlocked() {
		t.Errorf("ExternalListenerBlocked should be false after switching back to fabric mode")
	}
}

// TestModeSwitchDoesNotTouchInstanceJSON guarantees that ULID/identity state
// persisted at instance.json is never written to or modified by the NET-09
// code path. We pre-populate an instance.json with a known payload and a
// fixed mtime, exercise the full mode-switch surface, and assert the file
// is byte-for-byte identical.
func TestModeSwitchDoesNotTouchInstanceJSON(t *testing.T) {
	home := t.TempDir()
	dbDir := filepath.Join(home, ".vulos", "db")
	if err := os.MkdirAll(dbDir, 0700); err != nil {
		t.Fatal(err)
	}
	instancePath := filepath.Join(dbDir, "instance.json")
	original := []byte(`{"instance_id":"01HXXXXXXXXXXXXXXXXXXXXXXX","hostname":"vulos-test"}`)
	if err := os.WriteFile(instancePath, original, 0600); err != nil {
		t.Fatal(err)
	}
	beforeStat, err := os.Stat(instancePath)
	if err != nil {
		t.Fatal(err)
	}

	s := newServiceForModeTest(t)
	s.LoadMode(home)
	for _, m := range []ConnectionMode{ModeDirect, ModeOwn, ModeLocal, ModeFabric} {
		if err := s.SetMode(m); err != nil {
			t.Fatalf("SetMode(%q): %v", m, err)
		}
	}

	after, err := os.ReadFile(instancePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(original) {
		t.Errorf("instance.json contents changed by NET-09 code path\nbefore: %s\nafter:  %s", original, after)
	}
	afterStat, err := os.Stat(instancePath)
	if err != nil {
		t.Fatal(err)
	}
	if !afterStat.ModTime().Equal(beforeStat.ModTime()) {
		t.Errorf("instance.json mtime changed: before=%v after=%v", beforeStat.ModTime(), afterStat.ModTime())
	}
}

func TestValidConnectionModes_CanonicalOrder(t *testing.T) {
	got := ValidConnectionModes()
	want := []ConnectionMode{ModeFabric, ModeDirect, ModeOwn, ModeLocal}
	if len(got) != len(want) {
		t.Fatalf("len: got %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("index %d: got %q, want %q", i, got[i], want[i])
		}
	}
	// Returned slice must be a copy — mutating it must not affect subsequent calls.
	got[0] = "tampered"
	again := ValidConnectionModes()
	if again[0] != ModeFabric {
		t.Errorf("ValidConnectionModes must return a fresh copy; got %q after tamper", again[0])
	}
}

func TestTriggerDirectReenroll_WritesSignalFile(t *testing.T) {
	home := t.TempDir()
	s := newServiceForModeTest(t)
	s.LoadMode(home)

	// Switching to direct should write a re-enroll signal file.
	if err := s.SetMode(ModeDirect); err != nil {
		t.Fatalf("SetMode(direct): %v", err)
	}
	signal := filepath.Join(home, ".vulos", "db", "network-reenroll.signal")
	if _, err := os.Stat(signal); err != nil {
		t.Fatalf("expected re-enroll signal file at %s: %v", signal, err)
	}

	// Explicit TriggerDirectReenroll call is idempotent (no error, file remains).
	s.TriggerDirectReenroll()
	if _, err := os.Stat(signal); err != nil {
		t.Errorf("signal file should still exist after explicit TriggerDirectReenroll: %v", err)
	}
}
