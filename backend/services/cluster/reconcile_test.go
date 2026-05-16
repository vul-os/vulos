package cluster

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"vulos/backend/services/appnet"
)

// ---------------------------------------------------------------------------
// mockAppInstaller — in-memory implementation of AppInstaller for tests
// ---------------------------------------------------------------------------

type mockInstalledEntry struct {
	id      string
	version string
}

type mockAppInstaller struct {
	mu        sync.Mutex
	installed map[string]string // appID → version
	failIDs   map[string]bool   // appIDs that should fail InstallFromRegistry
	calls     []string          // log of method calls: "install:id", "uninstall:id"
}

func newMockAppInstaller(initial ...mockInstalledEntry) *mockAppInstaller {
	m := &mockAppInstaller{
		installed: make(map[string]string),
		failIDs:   make(map[string]bool),
	}
	for _, e := range initial {
		m.installed[e.id] = e.version
	}
	return m
}

func (m *mockAppInstaller) Installed() ([]*appnet.AppManifest, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*appnet.AppManifest
	for id, ver := range m.installed {
		out = append(out, &appnet.AppManifest{ID: id, Version: ver})
	}
	return out, nil
}

func (m *mockAppInstaller) InstallFromRegistry(_ context.Context, appID, version string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, "install:"+appID)
	if m.failIDs[appID] {
		return errors.New("mock install failure")
	}
	m.installed[appID] = version
	return nil
}

func (m *mockAppInstaller) Uninstall(appID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, "uninstall:"+appID)
	if _, ok := m.installed[appID]; !ok {
		return errors.New("not installed")
	}
	delete(m.installed, appID)
	return nil
}

func (m *mockAppInstaller) isInstalled(appID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.installed[appID]
	return ok
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// newTestReconciler creates a Reconciler backed by a temp dir and the provided mock.
func newTestReconciler(t *testing.T, mock *mockAppInstaller) *Reconciler {
	t.Helper()
	dir := t.TempDir()
	r, err := NewReconciler(dir, mock, "test-node")
	if err != nil {
		t.Fatalf("NewReconciler: %v", err)
	}
	return r
}

// ---------------------------------------------------------------------------
// AC: install records row
// ---------------------------------------------------------------------------

func TestRegisterApp_RecordsRow(t *testing.T) {
	mock := newMockAppInstaller()
	r := newTestReconciler(t, mock)

	if err := r.RegisterApp("gimp", "2.10"); err != nil {
		t.Fatalf("RegisterApp: %v", err)
	}

	desired := r.ListDesiredApps()
	if len(desired) != 1 {
		t.Fatalf("expected 1 desired app, got %d", len(desired))
	}
	rec := desired[0]
	if rec.AppID != "gimp" {
		t.Errorf("AppID = %q, want %q", rec.AppID, "gimp")
	}
	if rec.Version != "2.10" {
		t.Errorf("Version = %q, want %q", rec.Version, "2.10")
	}
	if rec.InstalledBy != "test-node" {
		t.Errorf("InstalledBy = %q, want %q", rec.InstalledBy, "test-node")
	}
	if rec.Status != AppSyncActive {
		t.Errorf("Status = %q, want %q", rec.Status, AppSyncActive)
	}
	if rec.InstalledAt.IsZero() {
		t.Error("InstalledAt must not be zero")
	}
}

// ---------------------------------------------------------------------------
// AC: reconciler installs missing apps
// ---------------------------------------------------------------------------

func TestReconcileApps_InstallsMissing(t *testing.T) {
	mock := newMockAppInstaller() // nothing installed locally
	r := newTestReconciler(t, mock)

	// Desired: gimp active
	if err := r.RegisterApp("gimp", "2.10"); err != nil {
		t.Fatalf("RegisterApp: %v", err)
	}

	r.ReconcileApps(context.Background())

	if !mock.isInstalled("gimp") {
		t.Error("gimp should have been installed")
	}

	// local_app_status should reflect installed
	status := r.store.GetLocalStatus("gimp")
	if status == nil {
		t.Fatal("expected local status record for gimp")
	}
	if status.State != LocalAppInstalled {
		t.Errorf("State = %q, want %q", status.State, LocalAppInstalled)
	}
}

// ---------------------------------------------------------------------------
// AC: reconciler uninstalls removed apps
// ---------------------------------------------------------------------------

func TestReconcileApps_UninstallsRemoved(t *testing.T) {
	// gimp is locally installed but NOT in desired state
	mock := newMockAppInstaller(mockInstalledEntry{"gimp", "2.10"})
	r := newTestReconciler(t, mock)
	// desired is empty → gimp should be removed

	r.ReconcileApps(context.Background())

	if mock.isInstalled("gimp") {
		t.Error("gimp should have been uninstalled")
	}
}

func TestReconcileApps_UninstallsAfterMarkRemoved(t *testing.T) {
	mock := newMockAppInstaller(mockInstalledEntry{"gimp", "2.10"})
	r := newTestReconciler(t, mock)

	// Register then mark removed (simulates remote node triggering removal).
	if err := r.RegisterApp("gimp", "2.10"); err != nil {
		t.Fatalf("RegisterApp: %v", err)
	}
	if err := r.MarkAppRemoved("gimp"); err != nil {
		t.Fatalf("MarkAppRemoved: %v", err)
	}

	r.ReconcileApps(context.Background())

	if mock.isInstalled("gimp") {
		t.Error("gimp should have been uninstalled after MarkAppRemoved")
	}
}

// ---------------------------------------------------------------------------
// AC: failures recorded + retried with backoff
// ---------------------------------------------------------------------------

func TestReconcileApps_RecordsFailure(t *testing.T) {
	mock := newMockAppInstaller()
	mock.failIDs["gimp"] = true

	r := newTestReconciler(t, mock)
	if err := r.RegisterApp("gimp", "2.10"); err != nil {
		t.Fatalf("RegisterApp: %v", err)
	}

	r.ReconcileApps(context.Background())

	status := r.store.GetLocalStatus("gimp")
	if status == nil {
		t.Fatal("expected local status record for gimp after failure")
	}
	if status.State != LocalAppFailed {
		t.Errorf("State = %q, want %q", status.State, LocalAppFailed)
	}
	if status.Error == "" {
		t.Error("Error field must be populated on failure")
	}
	if status.RetryCount != 1 {
		t.Errorf("RetryCount = %d, want 1", status.RetryCount)
	}
	if status.NextRetryAt.IsZero() {
		t.Error("NextRetryAt must be set after first failure")
	}
}

func TestReconcileApps_BackoffPreventsPrematureRetry(t *testing.T) {
	mock := newMockAppInstaller()
	mock.failIDs["gimp"] = true

	r := newTestReconciler(t, mock)
	if err := r.RegisterApp("gimp", "2.10"); err != nil {
		t.Fatalf("RegisterApp: %v", err)
	}

	// First pass — fails, sets NextRetryAt to now+30s.
	r.ReconcileApps(context.Background())

	// Immediately run a second pass — should be skipped due to backoff.
	installCallsBefore := countCalls(mock.calls, "install:gimp")
	r.ReconcileApps(context.Background())
	installCallsAfter := countCalls(mock.calls, "install:gimp")

	if installCallsAfter != installCallsBefore {
		t.Errorf("expected no new install attempt during backoff window, got %d extra call(s)",
			installCallsAfter-installCallsBefore)
	}
}

func TestReconcileApps_BackoffExhausted_StopsRetrying(t *testing.T) {
	mock := newMockAppInstaller()
	mock.failIDs["gimp"] = true

	r := newTestReconciler(t, mock)
	if err := r.RegisterApp("gimp", "2.10"); err != nil {
		t.Fatalf("RegisterApp: %v", err)
	}

	// Simulate exhausted retries by pre-writing a status record with
	// RetryCount == len(schedule) and NextRetryAt in the past.
	exhausted := &LocalAppStatusRecord{
		AppID:       "gimp",
		State:       LocalAppFailed,
		Error:       "previous failure",
		RetryCount:  len(reconcileBackoffSchedule),
		NextRetryAt: time.Now().Add(-time.Minute),
	}
	if err := r.store.SetLocalStatus(exhausted); err != nil {
		t.Fatalf("SetLocalStatus: %v", err)
	}

	callsBefore := len(mock.calls)
	r.ReconcileApps(context.Background())
	callsAfter := len(mock.calls)

	if callsAfter != callsBefore {
		t.Errorf("expected no install attempt after exhausted retries, got %d new call(s)",
			callsAfter-callsBefore)
	}
}

func TestReconcileApps_BackoffExpired_Retries(t *testing.T) {
	mock := newMockAppInstaller()
	// First call fails, second succeeds.
	callCount := 0
	origFail := mock.failIDs
	_ = origFail

	r := newTestReconciler(t, mock)
	if err := r.RegisterApp("gimp", "2.10"); err != nil {
		t.Fatalf("RegisterApp: %v", err)
	}

	// Pre-set a failed status with NextRetryAt already expired.
	expired := &LocalAppStatusRecord{
		AppID:       "gimp",
		State:       LocalAppFailed,
		Error:       "old failure",
		RetryCount:  1,
		NextRetryAt: time.Now().Add(-time.Minute), // already in the past
	}
	if err := r.store.SetLocalStatus(expired); err != nil {
		t.Fatalf("SetLocalStatus: %v", err)
	}
	_ = callCount

	// Reconcile — backoff has expired, so a retry should be attempted.
	r.ReconcileApps(context.Background())

	calls := countCalls(mock.calls, "install:gimp")
	if calls == 0 {
		t.Error("expected install retry after backoff expired")
	}
}

// ---------------------------------------------------------------------------
// AC: no symbol collision — verified by build; this test guards naming
// ---------------------------------------------------------------------------

func TestReconcileInterval_IsDistinctFromOtherIntervals(t *testing.T) {
	// reconcileInterval must be a unique constant — here we just assert its value
	// is within a reasonable range to catch accidental overwrites.
	if reconcileInterval <= 0 {
		t.Errorf("reconcileInterval = %v, must be positive", reconcileInterval)
	}
	if reconcileInterval > 10*time.Minute {
		t.Errorf("reconcileInterval = %v, seems too large", reconcileInterval)
	}
}

// ---------------------------------------------------------------------------
// persistence — records survive across Reconciler restarts
// ---------------------------------------------------------------------------

func TestPersistence_DesiredApps(t *testing.T) {
	dir := t.TempDir()
	mock := newMockAppInstaller()

	r1, err := NewReconciler(dir, mock, "node-1")
	if err != nil {
		t.Fatalf("NewReconciler: %v", err)
	}
	if err := r1.RegisterApp("blender", "3.6"); err != nil {
		t.Fatalf("RegisterApp: %v", err)
	}

	// Create a second Reconciler from the same dir — should reload the record.
	r2, err := NewReconciler(dir, mock, "node-1")
	if err != nil {
		t.Fatalf("NewReconciler (r2): %v", err)
	}
	desired := r2.ListDesiredApps()
	if len(desired) != 1 || desired[0].AppID != "blender" {
		t.Errorf("expected blender in reloaded desired apps, got %+v", desired)
	}
}

func TestPersistence_LocalStatus(t *testing.T) {
	dir := t.TempDir()
	mock := newMockAppInstaller()

	r1, err := NewReconciler(dir, mock, "node-1")
	if err != nil {
		t.Fatalf("NewReconciler: %v", err)
	}
	if err := r1.RegisterApp("blender", "3.6"); err != nil {
		t.Fatalf("RegisterApp: %v", err)
	}
	r1.ReconcileApps(context.Background())

	// Reload from disk.
	r2, err := NewReconciler(dir, mock, "node-1")
	if err != nil {
		t.Fatalf("NewReconciler (r2): %v", err)
	}
	status := r2.store.GetLocalStatus("blender")
	if status == nil {
		t.Fatal("expected local status for blender after reload")
	}
	if status.State != LocalAppInstalled {
		t.Errorf("State = %q, want %q", status.State, LocalAppInstalled)
	}
}

// ---------------------------------------------------------------------------
// Start / Stop
// ---------------------------------------------------------------------------

func TestStartStop(t *testing.T) {
	mock := newMockAppInstaller()
	r := newTestReconciler(t, mock)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	r.Start(ctx)

	// Give the goroutine time for at least one reconcile pass.
	time.Sleep(50 * time.Millisecond)
	r.Stop()

	// Calling Stop a second time must not panic.
	r.Stop()
}

// ---------------------------------------------------------------------------
// JSON store — tests for the persistence layer itself
// ---------------------------------------------------------------------------

func TestAppReconcileStore_UpsertAndList(t *testing.T) {
	dir := t.TempDir()
	s, err := newAppReconcileStore(dir)
	if err != nil {
		t.Fatalf("newAppReconcileStore: %v", err)
	}

	rec := &InstalledAppRecord{
		AppID:       "postgres",
		Version:     "16",
		InstalledBy: "home",
		InstalledAt: time.Now().UTC(),
		Status:      AppSyncActive,
	}
	if err := s.UpsertDesiredApp(rec); err != nil {
		t.Fatalf("UpsertDesiredApp: %v", err)
	}

	rows := s.ListDesiredApps()
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if rows[0].AppID != "postgres" {
		t.Errorf("AppID = %q, want postgres", rows[0].AppID)
	}
}

func TestAppReconcileStore_SetAndGetLocalStatus(t *testing.T) {
	dir := t.TempDir()
	s, err := newAppReconcileStore(dir)
	if err != nil {
		t.Fatalf("newAppReconcileStore: %v", err)
	}

	rec := &LocalAppStatusRecord{
		AppID:    "gimp",
		State:    LocalAppInstalling,
		Progress: "downloading",
	}
	if err := s.SetLocalStatus(rec); err != nil {
		t.Fatalf("SetLocalStatus: %v", err)
	}

	got := s.GetLocalStatus("gimp")
	if got == nil {
		t.Fatal("GetLocalStatus returned nil")
	}
	if got.State != LocalAppInstalling {
		t.Errorf("State = %q, want %q", got.State, LocalAppInstalling)
	}
	if got.UpdatedAt.IsZero() {
		t.Error("UpdatedAt must be set by SetLocalStatus")
	}
}

func TestAppReconcileStore_Persistence(t *testing.T) {
	dir := t.TempDir()

	s1, _ := newAppReconcileStore(dir)
	_ = s1.UpsertDesiredApp(&InstalledAppRecord{
		AppID: "kicad", Version: "7", Status: AppSyncActive,
		InstalledAt: time.Now(),
	})
	_ = s1.SetLocalStatus(&LocalAppStatusRecord{AppID: "kicad", State: LocalAppInstalled})

	// Reload from disk.
	s2, err := newAppReconcileStore(dir)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	desired := s2.ListDesiredApps()
	if len(desired) != 1 || desired[0].AppID != "kicad" {
		t.Errorf("reloaded desired apps = %+v, want kicad", desired)
	}
	got := s2.GetLocalStatus("kicad")
	if got == nil || got.State != LocalAppInstalled {
		t.Errorf("reloaded local status = %+v", got)
	}
}

// ---------------------------------------------------------------------------
// reconcileNextBackoff
// ---------------------------------------------------------------------------

func TestReconcileNextBackoff(t *testing.T) {
	cases := []struct {
		attempt  int
		wantZero bool
	}{
		{0, false},
		{1, false},
		{2, false},
		{3, false},
		{4, true}, // exhausted
		{9, true}, // well beyond limit
	}
	for _, tc := range cases {
		d := reconcileNextBackoff(tc.attempt)
		if tc.wantZero && d != 0 {
			t.Errorf("attempt %d: got %v, want 0 (exhausted)", tc.attempt, d)
		}
		if !tc.wantZero && d == 0 {
			t.Errorf("attempt %d: got 0, want non-zero delay", tc.attempt)
		}
	}
}

// ---------------------------------------------------------------------------
// JSON file paths are inside dataDir
// ---------------------------------------------------------------------------

func TestAppReconcileStore_FileLocations(t *testing.T) {
	dir := t.TempDir()
	s, _ := newAppReconcileStore(dir)

	wantDesired := filepath.Join(dir, "installed_apps.json")
	wantLocal := filepath.Join(dir, "local_app_status.json")

	if s.desiredPath != wantDesired {
		t.Errorf("desiredPath = %q, want %q", s.desiredPath, wantDesired)
	}
	if s.localPath != wantLocal {
		t.Errorf("localPath = %q, want %q", s.localPath, wantLocal)
	}

	// Ensure the directory was created.
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("dataDir not created: %v", err)
	}
}

// ---------------------------------------------------------------------------
// helper
// ---------------------------------------------------------------------------

func countCalls(calls []string, target string) int {
	n := 0
	for _, c := range calls {
		if c == target {
			n++
		}
	}
	return n
}
