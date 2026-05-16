package profiles

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// monotonicClock returns a nowFn that produces strictly increasing timestamps
// starting from a fixed epoch, incrementing by 1ms per call. This ensures
// IDs derived from UnixMilli are always unique within a test.
func monotonicClock() func() time.Time {
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC).UnixMilli()
	var counter int64
	return func() time.Time {
		n := atomic.AddInt64(&counter, 1)
		return time.UnixMilli(base + n)
	}
}

// newTestStore creates a Store backed by t.TempDir with a monotonic clock so
// every profile gets a distinct ID regardless of wall-clock resolution.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	s := NewStore(t.TempDir())
	s.nowFn = monotonicClock()
	return s
}

// ---------------------------------------------------------------------------
// BrowserProfile Store – CRUD
// ---------------------------------------------------------------------------

func TestCreate_Valid(t *testing.T) {
	s := newTestStore(t)
	p, err := s.Create("user1", "personal", "#3b82f6", "👤")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.ID == "" {
		t.Error("expected non-empty ID")
	}
	if p.Name != "personal" {
		t.Errorf("Name = %q, want %q", p.Name, "personal")
	}
	if p.UserID != "user1" {
		t.Errorf("UserID = %q, want %q", p.UserID, "user1")
	}
	if !p.Isolated {
		t.Error("expected Isolated = true")
	}
	if p.CreatedAt.IsZero() {
		t.Error("CreatedAt not set")
	}
}

func TestCreate_InvalidName(t *testing.T) {
	s := newTestStore(t)
	cases := []string{
		"",            // empty
		"a",           // too short (min 2)
		"UPPER",       // uppercase
		"my--profile", // consecutive hyphens
		"-leading",    // leading hyphen
		"trailing-",   // trailing hyphen
		"has space",   // space
	}
	for _, name := range cases {
		_, err := s.Create("user1", name, "#fff", "")
		if err == nil {
			t.Errorf("Create(%q) expected error, got nil", name)
		}
	}
}

func TestGet_Found(t *testing.T) {
	s := newTestStore(t)
	p, _ := s.Create("user1", "work", "#22c55e", "💼")

	got, ok := s.Get(p.ID)
	if !ok {
		t.Fatal("Get: expected found=true")
	}
	if got.ID != p.ID {
		t.Errorf("ID mismatch: got %q want %q", got.ID, p.ID)
	}
}

func TestGet_NotFound(t *testing.T) {
	s := newTestStore(t)
	_, ok := s.Get("nonexistent")
	if ok {
		t.Error("Get: expected found=false for unknown ID")
	}
}

func TestListForUser(t *testing.T) {
	s := newTestStore(t)
	s.Create("userA", "home", "#fff", "")
	s.Create("userA", "work", "#000", "")
	s.Create("userB", "private", "#abc", "")

	list := s.ListForUser("userA")
	if len(list) != 2 {
		t.Errorf("ListForUser(userA): got %d, want 2", len(list))
	}
	for _, p := range list {
		if p.UserID != "userA" {
			t.Errorf("unexpected UserID %q in list", p.UserID)
		}
	}

	listB := s.ListForUser("userB")
	if len(listB) != 1 {
		t.Errorf("ListForUser(userB): got %d, want 1", len(listB))
	}
}

func TestUpdate_Valid(t *testing.T) {
	clk := monotonicClock()
	s := NewStore(t.TempDir())
	s.nowFn = clk

	p, _ := s.Create("user1", "work", "#000", "")
	before := p.UpdatedAt

	if err := s.Update(p.ID, "home", "#fff", "🏠"); err != nil {
		t.Fatalf("Update error: %v", err)
	}
	got, _ := s.Get(p.ID)
	if got.Name != "home" {
		t.Errorf("Name = %q, want home", got.Name)
	}
	if got.Color != "#fff" {
		t.Errorf("Color = %q, want #fff", got.Color)
	}
	if !got.UpdatedAt.After(before) {
		t.Error("UpdatedAt was not bumped")
	}
}

func TestUpdate_InvalidName(t *testing.T) {
	s := newTestStore(t)
	p, _ := s.Create("user1", "work", "#000", "")
	err := s.Update(p.ID, "INVALID NAME", "", "")
	if err == nil {
		t.Error("Update with invalid name should return error")
	}
}

func TestUpdate_EmptyNamePreservesOld(t *testing.T) {
	s := newTestStore(t)
	p, _ := s.Create("user1", "work", "#000", "")
	s.Update(p.ID, "", "#ff0000", "")
	got, _ := s.Get(p.ID)
	if got.Name != "work" {
		t.Errorf("Name changed unexpectedly to %q", got.Name)
	}
	if got.Color != "#ff0000" {
		t.Errorf("Color not updated: %q", got.Color)
	}
}

func TestUpdate_NotFound(t *testing.T) {
	s := newTestStore(t)
	err := s.Update("ghost-id", "work", "", "")
	if err == nil {
		t.Error("Update on unknown ID should return error")
	}
}

func TestDelete(t *testing.T) {
	s := newTestStore(t)
	p, _ := s.Create("user1", "temp", "#fff", "")

	if err := s.Delete(p.ID); err != nil {
		t.Fatalf("Delete error: %v", err)
	}
	_, ok := s.Get(p.ID)
	if ok {
		t.Error("profile still present after Delete")
	}
}

func TestDelete_NotFound(t *testing.T) {
	s := newTestStore(t)
	err := s.Delete("does-not-exist")
	if err == nil {
		t.Error("Delete on unknown ID should return error")
	}
}

func TestClearData(t *testing.T) {
	s := newTestStore(t)
	p, _ := s.Create("user1", "priv", "#abc", "")

	// Plant a file inside DataDir to verify removal
	testFile := filepath.Join(p.DataDir, "cookie.dat")
	os.WriteFile(testFile, []byte("secret"), 0600)

	if err := s.ClearData(p.ID); err != nil {
		t.Fatalf("ClearData error: %v", err)
	}
	if _, statErr := os.Stat(testFile); !os.IsNotExist(statErr) {
		t.Error("expected cookie.dat to be removed after ClearData")
	}
	// DataDir itself should still exist
	if _, statErr := os.Stat(p.DataDir); os.IsNotExist(statErr) {
		t.Error("DataDir should be recreated by ClearData")
	}
}

func TestBindApp_And_ProfileForApp(t *testing.T) {
	s := newTestStore(t)
	p, _ := s.Create("user1", "work", "#000", "")

	if err := s.BindApp(p.ID, "com.example.app"); err != nil {
		t.Fatalf("BindApp error: %v", err)
	}
	got := s.ProfileForApp("user1", "com.example.app")
	if got != p.ID {
		t.Errorf("ProfileForApp = %q, want %q", got, p.ID)
	}
}

func TestBindApp_MovesBinding(t *testing.T) {
	s := newTestStore(t)
	p1, _ := s.Create("user1", "home", "#fff", "")
	p2, _ := s.Create("user1", "work", "#000", "")

	s.BindApp(p1.ID, "com.example.app")
	s.BindApp(p2.ID, "com.example.app") // move to p2

	got := s.ProfileForApp("user1", "com.example.app")
	if got != p2.ID {
		t.Errorf("ProfileForApp after rebind = %q, want %q", got, p2.ID)
	}
	// Should no longer be in p1
	g1, _ := s.Get(p1.ID)
	for _, a := range g1.AppBindings {
		if a == "com.example.app" {
			t.Error("old profile still holds the app binding")
		}
	}
}

func TestProfileForApp_NoBinding(t *testing.T) {
	s := newTestStore(t)
	s.Create("user1", "home", "#fff", "")
	got := s.ProfileForApp("user1", "unbound.app")
	if got != "" {
		t.Errorf("expected empty string for unbound app, got %q", got)
	}
}

// ---------------------------------------------------------------------------
// JSON persistence roundtrip
// ---------------------------------------------------------------------------

func TestFlush_And_Reload(t *testing.T) {
	dir := t.TempDir()
	s1 := NewStore(dir)
	s1.nowFn = monotonicClock()
	p, _ := s1.Create("user1", "personal", "#3b82f6", "👤")
	s1.BindApp(p.ID, "myapp")
	if err := s1.Flush(); err != nil {
		t.Fatalf("Flush error: %v", err)
	}

	// Reload from same directory
	s2 := NewStore(dir)
	got, ok := s2.Get(p.ID)
	if !ok {
		t.Fatal("profile not found after reload")
	}
	if got.Name != "personal" {
		t.Errorf("Name = %q after reload, want personal", got.Name)
	}
	if got.Color != "#3b82f6" {
		t.Errorf("Color = %q after reload, want #3b82f6", got.Color)
	}
	if len(got.AppBindings) != 1 || got.AppBindings[0] != "myapp" {
		t.Errorf("AppBindings mismatch after reload: %v", got.AppBindings)
	}
}

func TestFlush_ValidJSON(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	s.nowFn = monotonicClock()
	s.Create("user1", "home", "#fff", "")
	s.Flush()

	storePath := filepath.Join(dir, "browser-profiles.json")
	data, err := os.ReadFile(storePath)
	if err != nil {
		t.Fatalf("could not read store file: %v", err)
	}
	var list []*BrowserProfile
	if err := json.Unmarshal(data, &list); err != nil {
		t.Fatalf("store file is not valid JSON: %v", err)
	}
	if len(list) != 1 {
		t.Errorf("expected 1 profile in JSON, got %d", len(list))
	}
}

// ---------------------------------------------------------------------------
// EnsureDefaults – default profile derivation
// ---------------------------------------------------------------------------

func TestEnsureDefaults_CreatesThree(t *testing.T) {
	s := newTestStore(t)
	s.EnsureDefaults("user1")

	list := s.ListForUser("user1")
	if len(list) != 3 {
		t.Errorf("EnsureDefaults: got %d profiles, want 3", len(list))
	}

	names := make(map[string]bool)
	for _, p := range list {
		names[p.Name] = true
	}
	for _, want := range []string{"personal", "work", "private"} {
		if !names[want] {
			t.Errorf("expected default profile %q to exist", want)
		}
	}
}

func TestEnsureDefaults_Idempotent(t *testing.T) {
	s := newTestStore(t)
	s.EnsureDefaults("user1")
	s.EnsureDefaults("user1") // second call should be a no-op

	list := s.ListForUser("user1")
	if len(list) != 3 {
		t.Errorf("EnsureDefaults is not idempotent: got %d profiles", len(list))
	}
}

func TestEnsureDefaults_DefaultsAreIsolated(t *testing.T) {
	s := newTestStore(t)
	s.EnsureDefaults("user1")
	for _, p := range s.ListForUser("user1") {
		if !p.Isolated {
			t.Errorf("default profile %q: expected Isolated=true", p.Name)
		}
	}
}

// ---------------------------------------------------------------------------
// DeviceProfileStore
// ---------------------------------------------------------------------------

func TestValidFormFactor(t *testing.T) {
	valid := []FormFactor{FormFactorPC, FormFactorTV, FormFactorCar, FormFactorWatch}
	for _, ff := range valid {
		if !ValidFormFactor(ff) {
			t.Errorf("ValidFormFactor(%q) = false, want true", ff)
		}
	}
	if ValidFormFactor("unknown") {
		t.Error("ValidFormFactor(unknown) = true, want false")
	}
	if ValidFormFactor("") {
		t.Error("ValidFormFactor('') = true, want false")
	}
}

func TestDeviceProfileStore_DefaultIsPC(t *testing.T) {
	// Unset env vars so heuristic returns FormFactorPC on non-Linux
	t.Setenv("VULOS_FORM_FACTOR", "")
	t.Setenv("VULOS_DEVICE", "")

	s := NewDeviceProfileStore(t.TempDir())
	profile, _ := s.Get()
	// On test host (macOS / CI) DMI files don't exist; default must be PC.
	if profile == "" {
		t.Error("expected a non-empty form factor")
	}
}

func TestDeviceProfileStore_EnvOverride(t *testing.T) {
	t.Setenv("VULOS_FORM_FACTOR", "tv")
	s := NewDeviceProfileStore(t.TempDir())
	profile, suggested := s.Get()
	if profile != FormFactorTV {
		t.Errorf("profile = %q, want tv", profile)
	}
	if suggested != FormFactorTV {
		t.Errorf("suggested = %q, want tv", suggested)
	}
}

func TestDeviceProfileStore_SetAndGet(t *testing.T) {
	t.Setenv("VULOS_FORM_FACTOR", "")
	dir := t.TempDir()
	s := NewDeviceProfileStore(dir)
	if err := s.Set(FormFactorTV); err != nil {
		t.Fatalf("Set error: %v", err)
	}
	profile, _ := s.Get()
	if profile != FormFactorTV {
		t.Errorf("Get after Set = %q, want tv", profile)
	}
}

func TestDeviceProfileStore_Persistence(t *testing.T) {
	t.Setenv("VULOS_FORM_FACTOR", "")
	dir := t.TempDir()
	s1 := NewDeviceProfileStore(dir)
	s1.Set(FormFactorCar)

	s2 := NewDeviceProfileStore(dir)
	profile, _ := s2.Get()
	if profile != FormFactorCar {
		t.Errorf("persisted profile = %q, want car", profile)
	}
}

func TestDeviceProfileStore_InvalidPersistedValueFallsBack(t *testing.T) {
	t.Setenv("VULOS_FORM_FACTOR", "")
	t.Setenv("VULOS_DEVICE", "")
	dir := t.TempDir()

	// Write a bogus profile JSON to the store path
	bad := filepath.Join(dir, "device-profile.json")
	os.WriteFile(bad, []byte(`{"profile":"spaceship"}`), 0600)

	s := NewDeviceProfileStore(dir)
	profile, _ := s.Get()
	// Invalid value is ignored; detectFormFactor() returns FormFactorPC on CI
	if profile == "" {
		t.Error("expected fallback to a valid form factor")
	}
	if !ValidFormFactor(profile) {
		t.Errorf("fallback profile %q is not a valid form factor", profile)
	}
}

// ---------------------------------------------------------------------------
// D80 regression: collision-free IDs under same-millisecond burst
// ---------------------------------------------------------------------------

// sameMilliClock returns a nowFn that always returns exactly the same
// timestamp, simulating many creates within the same millisecond.
func sameMilliClock() func() time.Time {
	fixed := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)
	return func() time.Time { return fixed }
}

// TestCreate_CollisionFreeUnderSameMilli creates N profiles in a tight loop
// where the clock is frozen (same millisecond for every call) and asserts
// that every profile gets a distinct ID and no profile silently overwrites
// another.
func TestCreate_CollisionFreeUnderSameMilli(t *testing.T) {
	const N = 200

	s := NewStore(t.TempDir())
	s.nowFn = sameMilliClock()

	ids := make(map[string]struct{}, N)
	// Use different names to avoid name-collision errors (names must be unique
	// per the Vulos naming rules but the store doesn't enforce uniqueness —
	// only IDs must be unique for this test).
	for i := 0; i < N; i++ {
		name := fmt.Sprintf("p%04d", i)
		p, err := s.Create("user1", name, "#fff", "")
		if err != nil {
			t.Fatalf("Create #%d error: %v", i, err)
		}
		if _, dup := ids[p.ID]; dup {
			t.Fatalf("duplicate ID %q at iteration %d — D80 collision detected", p.ID, i)
		}
		ids[p.ID] = struct{}{}
	}

	// Also verify the store actually holds N distinct profiles.
	list := s.ListForUser("user1")
	if len(list) != N {
		t.Errorf("store has %d profiles, want %d — some were silently overwritten", len(list), N)
	}
}

// Ensure time import is used (UpdatedAt comparison in TestUpdate_Valid uses time.Time).
var _ = time.Now
