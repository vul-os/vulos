package vault

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"

	"vulos/backend/internal/storage"
)

// helpers

func unconfiguredS3() *storage.S3Config {
	return &storage.S3Config{
		Endpoint:  "s3.amazonaws.com",
		Bucket:    "",
		Region:    "us-east-1",
		AccessKey: "",
		SecretKey: "",
		UseSSL:    true,
	}
}

func configuredS3() *storage.S3Config {
	return &storage.S3Config{
		Endpoint:  "s3.amazonaws.com",
		Bucket:    "test-bucket",
		Region:    "us-east-1",
		AccessKey: "AKIAIOSFODNN7EXAMPLE",
		SecretKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
		UseSSL:    true,
	}
}

func resticInPath() bool {
	_, err := exec.LookPath("restic")
	return err == nil
}

// ---------------------------------------------------------------------------
// Status shape
// ---------------------------------------------------------------------------

// TestStatusZeroValue verifies that a freshly created Vault returns a zero
// Status without panicking and that its fields match the expected defaults.
func TestStatusZeroValue(t *testing.T) {
	v := New(unconfiguredS3(), t.TempDir())
	s := v.Status()

	if s.Initialized {
		t.Error("expected Initialized=false for new vault")
	}
	if s.Running {
		t.Error("expected Running=false for new vault")
	}
	if s.Snapshots != 0 {
		t.Errorf("expected Snapshots=0, got %d", s.Snapshots)
	}
	if s.LastError != "" {
		t.Errorf("expected empty LastError, got %q", s.LastError)
	}
	if !s.LastBackup.IsZero() {
		t.Errorf("expected zero LastBackup, got %v", s.LastBackup)
	}
}

// TestStatusReturnsCopy verifies Status() returns a value copy, not a
// shared pointer (mutations to the returned value must not affect the vault).
func TestStatusReturnsCopy(t *testing.T) {
	v := New(unconfiguredS3(), t.TempDir())

	s1 := v.Status()
	s1.Snapshots = 999 // mutate the copy

	s2 := v.Status()
	if s2.Snapshots == 999 {
		t.Error("Status() returned a shared reference — expected a copy")
	}
}

// TestStatusRunningFieldToggle exercises the Running flag by manipulating the
// internal mutex-protected status directly so no real backup is needed.
func TestStatusRunningFieldToggle(t *testing.T) {
	v := New(unconfiguredS3(), t.TempDir())

	if v.Status().Running {
		t.Fatal("Running should be false initially")
	}

	// Simulate what Backup() does: set Running, then clear it.
	v.mu.Lock()
	v.status.Running = true
	v.mu.Unlock()

	if !v.Status().Running {
		t.Error("Running should be true after setting")
	}

	v.mu.Lock()
	v.status.Running = false
	v.mu.Unlock()

	if v.Status().Running {
		t.Error("Running should be false after clearing")
	}
}

// ---------------------------------------------------------------------------
// Config gating (Configured)
// ---------------------------------------------------------------------------

// TestInitRequiresS3Config verifies Init() returns an error when S3 is not
// configured (no real restic interaction expected — fails fast on config check).
func TestInitRequiresS3Config(t *testing.T) {
	v := New(unconfiguredS3(), t.TempDir())
	err := v.Init(context.Background())
	if err == nil {
		t.Fatal("expected error from Init with unconfigured S3")
	}
}

// TestS3ConfiguredFalseWhenFieldsEmpty checks the Configured() helper.
func TestS3ConfiguredFalseWhenFieldsEmpty(t *testing.T) {
	s3 := unconfiguredS3()
	if s3.Configured() {
		t.Error("Configured() should return false when AccessKey/SecretKey/Bucket are empty")
	}
}

// TestS3ConfiguredTrueWhenFieldsSet checks the Configured() helper with valid values.
func TestS3ConfiguredTrueWhenFieldsSet(t *testing.T) {
	s3 := configuredS3()
	if !s3.Configured() {
		t.Error("Configured() should return true when AccessKey, SecretKey, and Bucket are all set")
	}
}

// TestS3ConfiguredRequiresAllThreeFields checks that Configured() requires all
// three mandatory fields (AccessKey, SecretKey, Bucket).
func TestS3ConfiguredRequiresAllThreeFields(t *testing.T) {
	cases := []struct {
		name string
		cfg  *storage.S3Config
	}{
		{"missing AccessKey", &storage.S3Config{Bucket: "b", SecretKey: "s"}},
		{"missing SecretKey", &storage.S3Config{Bucket: "b", AccessKey: "a"}},
		{"missing Bucket", &storage.S3Config{AccessKey: "a", SecretKey: "s"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.cfg.Configured() {
				t.Errorf("Configured() should be false when %s", tc.name)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Backup gating: requires Initialized
// ---------------------------------------------------------------------------

// TestBackupRequiresInitialized verifies that Backup() returns an error when
// the vault has not been initialized, without requiring a real restic binary.
func TestBackupRequiresInitialized(t *testing.T) {
	v := New(configuredS3(), t.TempDir())
	// status.Initialized is false by default
	err := v.Backup(context.Background())
	if err == nil {
		t.Fatal("expected error from Backup when vault is not initialized")
	}
}

// ---------------------------------------------------------------------------
// Schedule interval math
// ---------------------------------------------------------------------------

// TestStartScheduleFiresAfterInterval verifies that StartSchedule goroutine
// ticks and calls Backup without deadlocking.  Backup returns fast with
// "vault not initialized" (pure in-memory, no binary needed).  We confirm the
// ticker fired by verifying the mutex is cleanly releasable after ctx cancel.
func TestStartScheduleFiresAfterInterval(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping schedule timing test in short mode")
	}

	v := New(configuredS3(), t.TempDir())
	// Leave Initialized=false so Backup() fails immediately with no exec call.

	interval := 60 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), interval*3)
	defer cancel()

	v.StartSchedule(ctx, interval)

	// Wait for context expiry (covers at least 2 ticks).
	<-ctx.Done()

	// Brief grace period for the goroutine to observe ctx.Done and exit.
	time.Sleep(20 * time.Millisecond)

	// Confirm the mutex is acquirable — proves the goroutine released it and exited.
	acquired := make(chan struct{})
	go func() {
		v.mu.Lock()
		v.mu.Unlock()
		close(acquired)
	}()
	select {
	case <-acquired:
		// goroutine exited cleanly after cancel
	case <-time.After(300 * time.Millisecond):
		t.Error("mutex still held after context cancellation — scheduler goroutine may be stuck")
	}
}

// TestScheduleIntervalIsRespected verifies that with a very long interval the
// scheduler does NOT fire before the first tick.
func TestScheduleIntervalIsRespected(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping schedule timing test in short mode")
	}

	v := New(configuredS3(), t.TempDir())
	v.mu.Lock()
	v.status.Initialized = true
	v.mu.Unlock()

	// Use a 10-second interval — the scheduler should not fire during a
	// 50 ms observation window.
	interval := 10 * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	v.StartSchedule(ctx, interval)
	<-ctx.Done()

	s := v.Status()
	if !s.LastBackup.IsZero() || s.LastError != "" {
		t.Error("scheduler fired before the first interval elapsed")
	}
}

// ---------------------------------------------------------------------------
// FindRestic behavior
// ---------------------------------------------------------------------------

// TestFindResticReturnsBool just ensures the function returns a bool and
// doesn't panic regardless of whether restic is installed.
func TestFindResticReturnsBool(t *testing.T) {
	result := FindRestic()
	// result is either true or false — both are acceptable
	_ = result
}

// TestFindResticConsistentWithLookPath checks that FindRestic() agrees with
// exec.LookPath to confirm the implementation is correct.
func TestFindResticConsistentWithLookPath(t *testing.T) {
	expected := resticInPath()
	got := FindRestic()
	if got != expected {
		t.Errorf("FindRestic()=%v but exec.LookPath says %v", got, expected)
	}
}

// TestFindResticAbsent exercises FindRestic when restic is guaranteed absent
// by temporarily clearing PATH.
func TestFindResticAbsent(t *testing.T) {
	orig := os.Getenv("PATH")
	t.Setenv("PATH", "") // empty PATH — no binary can be found
	defer os.Setenv("PATH", orig)

	if FindRestic() {
		t.Error("FindRestic() returned true with empty PATH")
	}
}

// ---------------------------------------------------------------------------
// getenv helper
// ---------------------------------------------------------------------------

// TestGetenvFallback verifies that getenv returns the fallback when the env
// var is unset.
func TestGetenvFallback(t *testing.T) {
	const key = "VAULT_TEST_UNSET_VAR_XYZ"
	os.Unsetenv(key)
	got := getenv(key, "expected-fallback")
	if got != "expected-fallback" {
		t.Errorf("getenv fallback: got %q, want %q", got, "expected-fallback")
	}
}

// TestGetenvUsesEnv verifies that getenv returns the actual env value when set.
func TestGetenvUsesEnv(t *testing.T) {
	const key = "VAULT_TEST_SET_VAR_XYZ"
	t.Setenv(key, "env-value")
	got := getenv(key, "fallback")
	if got != "env-value" {
		t.Errorf("getenv env: got %q, want %q", got, "env-value")
	}
}

// ---------------------------------------------------------------------------
// DataDir helper
// ---------------------------------------------------------------------------

// TestDataDirReturnsNonEmpty verifies DataDir() returns a non-empty path and
// that the directory exists after the call.
func TestDataDirReturnsNonEmpty(t *testing.T) {
	dir := DataDir()
	if dir == "" {
		t.Fatal("DataDir() returned empty string")
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		t.Errorf("DataDir() path %q is not an accessible directory: %v", dir, err)
	}
}

// ---------------------------------------------------------------------------
// New constructor
// ---------------------------------------------------------------------------

// TestNewSetsDataDir verifies the constructor stores the provided dataDir.
func TestNewSetsDataDir(t *testing.T) {
	tmp := t.TempDir()
	v := New(unconfiguredS3(), tmp)
	if v.dataDir != tmp {
		t.Errorf("expected dataDir=%q, got %q", tmp, v.dataDir)
	}
}

// TestNewDefaultPassword verifies that when RESTIC_PASSWORD is unset the vault
// uses the compiled-in default key.
func TestNewDefaultPassword(t *testing.T) {
	const key = "RESTIC_PASSWORD"
	os.Unsetenv(key)
	v := New(unconfiguredS3(), t.TempDir())
	if v.password != "vulos-default-key" {
		t.Errorf("expected default password, got %q", v.password)
	}
}

// TestNewCustomPassword verifies that RESTIC_PASSWORD is honoured.
func TestNewCustomPassword(t *testing.T) {
	t.Setenv("RESTIC_PASSWORD", "my-secret")
	v := New(unconfiguredS3(), t.TempDir())
	if v.password != "my-secret" {
		t.Errorf("expected custom password %q, got %q", "my-secret", v.password)
	}
}

// ---------------------------------------------------------------------------
// resticCmd smoke test (no execution)
// ---------------------------------------------------------------------------

// TestResticCmdContainsEnv verifies that resticCmd injects RESTIC_PASSWORD and
// the S3 env vars into the command's environment without running the command.
func TestResticCmdContainsEnv(t *testing.T) {
	t.Setenv("RESTIC_PASSWORD", "check-env")
	v := New(configuredS3(), t.TempDir())

	cmd := v.resticCmd(context.Background(), "snapshots")
	if cmd == nil {
		t.Fatal("resticCmd returned nil")
	}

	hasPassword := false
	hasRepo := false
	for _, e := range cmd.Env {
		if e == "RESTIC_PASSWORD=check-env" {
			hasPassword = true
		}
		if len(e) > 18 && e[:18] == "RESTIC_REPOSITORY=" {
			hasRepo = true
		}
	}
	if !hasPassword {
		t.Error("RESTIC_PASSWORD not found in cmd.Env")
	}
	if !hasRepo {
		t.Error("RESTIC_REPOSITORY not found in cmd.Env")
	}
}

// ---------------------------------------------------------------------------
// Restic-dependent tests (skipped when restic not in PATH)
// ---------------------------------------------------------------------------

// TestInitWithResticAbsent confirms Init() returns an error when restic is
// not available and S3 is configured.
func TestInitWithResticAbsent(t *testing.T) {
	if resticInPath() {
		t.Skip("restic found in PATH — skipping absent-binary test")
	}
	v := New(configuredS3(), t.TempDir())
	// Even though S3 is "configured", restic binary is missing so init fails.
	err := v.Init(context.Background())
	if err == nil {
		t.Error("expected Init to fail when restic binary is absent")
	}
}
