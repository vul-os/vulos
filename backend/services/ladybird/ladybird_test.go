package ladybird

import (
	"context"
	"os"
	"testing"
	"time"

	"vulos/backend/services/stream"
)

// newTestPool creates a real stream.Pool.
// Tests that exercise the no-binary fallback never call pool.Launch,
// so no Xvfb / GStreamer processes are spawned.
func newTestPool() *stream.Pool {
	return stream.NewPool()
}

// TestFlagOff_NoOp verifies the default behaviour: when the feature flag is
// unset (or explicitly empty), Start must return nil immediately and
// EngineStatus must report EngineDisabled without touching the stream pool.
func TestFlagOff_NoOp(t *testing.T) {
	os.Unsetenv(EnvFeatureFlag)

	svc := New(newTestPool())
	err := svc.Start(context.Background(), 0)
	if err != nil {
		t.Fatalf("Start() with flag off returned error: %v", err)
	}

	st := svc.EngineStatus()
	if st.Engine != "ladybird" {
		t.Errorf("EngineStatus().Engine = %q, want %q", st.Engine, "ladybird")
	}
	if st.State != EngineDisabled {
		t.Errorf("EngineStatus().State = %q, want %q", st.State, EngineDisabled)
	}
	if svc.Running() {
		t.Error("Running() should be false when flag is off")
	}
}

// TestFlagOff_ExplicitZero verifies that VULOS_LADYBIRD_ENGINE=0 is also
// treated as disabled (only "1" and "true" enable the feature).
func TestFlagOff_ExplicitZero(t *testing.T) {
	t.Setenv(EnvFeatureFlag, "0")

	svc := New(newTestPool())
	if err := svc.Start(context.Background(), 0); err != nil {
		t.Fatalf("Start() with flag=0 returned error: %v", err)
	}
	if svc.EngineStatus().State != EngineDisabled {
		t.Errorf("expected EngineDisabled for flag=0, got %q", svc.EngineStatus().State)
	}
}

// TestNoBinary_CleanFallback verifies that when the feature flag is on but no
// ladybird binary exists, Start returns nil (clean fallback, not an error) and
// EngineStatus reports EngineMissing.
func TestNoBinary_CleanFallback(t *testing.T) {
	t.Setenv(EnvFeatureFlag, "1")
	// Point LADYBIRD_BIN at a path that does not exist.
	t.Setenv(EnvBinaryPath, "/nonexistent/path/to/ladybird-does-not-exist")

	svc := New(newTestPool())
	err := svc.Start(context.Background(), 0)
	if err != nil {
		t.Fatalf("Start() with missing binary should return nil (clean fallback), got: %v", err)
	}

	st := svc.EngineStatus()
	if st.State != EngineMissing {
		t.Errorf("EngineStatus().State = %q, want %q", st.State, EngineMissing)
	}
	if st.BinPath != "" {
		t.Errorf("BinPath should be empty when binary is absent, got %q", st.BinPath)
	}
	if svc.Running() {
		t.Error("Running() should be false when binary is absent")
	}
}

// TestNoBinary_NoPathOverride verifies the same clean-fallback behaviour when
// LADYBIRD_BIN is not set and "ladybird" / "WebContent" are not on $PATH.
func TestNoBinary_NoPathOverride(t *testing.T) {
	t.Setenv(EnvFeatureFlag, "1")
	os.Unsetenv(EnvBinaryPath)

	// Narrow PATH to an empty temp dir so we can be sure neither "ladybird"
	// nor "WebContent" resolve.
	tmp := t.TempDir()
	t.Setenv("PATH", tmp)

	svc := New(newTestPool())
	err := svc.Start(context.Background(), 0)
	if err != nil {
		t.Fatalf("Start() with no binary on PATH should return nil, got: %v", err)
	}
	if svc.EngineStatus().State != EngineMissing {
		t.Errorf("expected EngineMissing, got %q", svc.EngineStatus().State)
	}
}

// TestEngineStatus_FieldsWhenDisabled checks that EngineStatus always
// populates Engine correctly regardless of state.
func TestEngineStatus_FieldsWhenDisabled(t *testing.T) {
	os.Unsetenv(EnvFeatureFlag)
	svc := New(newTestPool())
	_ = svc.Start(context.Background(), 0)

	st := svc.EngineStatus()
	if st.Engine != "ladybird" {
		t.Errorf("Engine field = %q, want \"ladybird\"", st.Engine)
	}
	if st.Message == "" {
		t.Error("Message should explain the disabled state")
	}
}

// TestEngineStatus_FieldsWhenMissing checks that EngineMissing status
// carries a helpful message referencing the binary env var.
func TestEngineStatus_FieldsWhenMissing(t *testing.T) {
	t.Setenv(EnvFeatureFlag, "1")
	t.Setenv(EnvBinaryPath, "/no/such/binary")

	svc := New(newTestPool())
	_ = svc.Start(context.Background(), 0)

	st := svc.EngineStatus()
	if st.State != EngineMissing {
		t.Fatalf("expected EngineMissing, got %q", st.State)
	}
	if st.Message == "" {
		t.Error("EngineMissing status should carry a non-empty Message")
	}
}

// TestWaitReady_ReturnsFalseWhenDisabled ensures WaitReady does not block
// forever when the engine is disabled; it should time out quickly.
func TestWaitReady_ReturnsFalseWhenDisabled(t *testing.T) {
	os.Unsetenv(EnvFeatureFlag)
	svc := New(newTestPool())
	_ = svc.Start(context.Background(), 0)

	start := time.Now()
	ready := svc.WaitReady(200 * time.Millisecond)
	elapsed := time.Since(start)

	if ready {
		t.Error("WaitReady should return false when engine is disabled")
	}
	// Should have waited at most ~300ms (200ms deadline + small slop).
	if elapsed > 500*time.Millisecond {
		t.Errorf("WaitReady took too long: %v", elapsed)
	}
}

// TestStop_Idempotent verifies that Stop() on a non-running service is safe.
func TestStop_Idempotent(t *testing.T) {
	os.Unsetenv(EnvFeatureFlag)
	svc := New(newTestPool())
	// Should not panic.
	svc.Stop()
	svc.Stop()
}

// TestFeatureFlagEnabled covers the flag parsing helper directly.
func TestFeatureFlagEnabled(t *testing.T) {
	cases := []struct {
		val  string
		want bool
	}{
		{"", false},
		{"0", false},
		{"false", false},
		{"no", false},
		{"1", true},
		{"true", true},
	}
	for _, c := range cases {
		t.Run("flag="+c.val, func(t *testing.T) {
			if c.val == "" {
				os.Unsetenv(EnvFeatureFlag)
			} else {
				t.Setenv(EnvFeatureFlag, c.val)
			}
			got := featureFlagEnabled()
			if got != c.want {
				t.Errorf("featureFlagEnabled() = %v, want %v (val=%q)", got, c.want, c.val)
			}
		})
	}
}

// TestResolveBinary_ExplicitBinNotFound verifies that a non-existent LADYBIRD_BIN
// is ignored and resolveBinary falls back to PATH search (returning "" when PATH
// is also empty).
func TestResolveBinary_ExplicitBinNotFound(t *testing.T) {
	t.Setenv(EnvBinaryPath, "/does/not/exist/ladybird")
	t.Setenv("PATH", t.TempDir()) // ensure nothing on PATH either

	got := resolveBinary()
	if got != "" {
		t.Errorf("resolveBinary() = %q, want empty string for missing binary", got)
	}
}

// TestResolveBinary_ExplicitBinFound verifies that a valid LADYBIRD_BIN path
// is returned directly without a PATH search.
func TestResolveBinary_ExplicitBinFound(t *testing.T) {
	// Create a real executable in a temp dir.
	tmp := t.TempDir()
	fakeBin := tmp + "/ladybird"
	if err := os.WriteFile(fakeBin, []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatalf("creating fake binary: %v", err)
	}
	t.Setenv(EnvBinaryPath, fakeBin)

	got := resolveBinary()
	if got != fakeBin {
		t.Errorf("resolveBinary() = %q, want %q", got, fakeBin)
	}
}
