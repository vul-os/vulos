// cgroups_test.go — per-app resource limits. This file was orphaned (zero
// callers) while apps launched with no memory/CPU/PID caps at all; it is now
// wired into Launcher.Launch/Stop, so these tests pin the contract that the
// wiring relies on — above all that an unsupported host is a clean no-op and
// never blocks an app from launching.
package appnet

import (
	"runtime"
	"testing"
)

func TestDefaultLimits_AreBounded(t *testing.T) {
	l := DefaultLimits()
	if l.MemoryMax <= 0 {
		t.Errorf("MemoryMax = %d, want a positive cap", l.MemoryMax)
	}
	if l.CPUQuota <= 0 {
		t.Errorf("CPUQuota = %d, want a positive cap", l.CPUQuota)
	}
	if l.PidsMax <= 0 {
		t.Errorf("PidsMax = %d, want a positive cap", l.PidsMax)
	}
}

// TestApplyCgroup_UnsupportedHostIsNoOp is the important one: on a host without
// cgroup v2 (every macOS dev build, and Linux without the v2 hierarchy)
// ApplyCgroup must return nil rather than an error, because Launcher.Launch
// logs-and-continues and must never fail an app launch over resource capping.
func TestApplyCgroup_UnsupportedHostIsNoOp(t *testing.T) {
	if supported() {
		t.Skip("host supports cgroup v2; this test covers the unsupported path")
	}
	if err := ApplyCgroup("test-app", 0, DefaultLimits()); err != nil {
		t.Errorf("ApplyCgroup on unsupported host = %v, want nil (must be a no-op)", err)
	}
	// Must not panic or block.
	RemoveCgroup("test-app")
	if stats := ReadCgroupStats("test-app"); len(stats) != 0 {
		t.Errorf("ReadCgroupStats on unsupported host = %v, want empty", stats)
	}
}

func TestSupported_FalseOffLinux(t *testing.T) {
	if runtime.GOOS != "linux" && supported() {
		t.Errorf("supported() = true on %s, want false — cgroups are Linux-only", runtime.GOOS)
	}
}

// TestReadCgroupStats_AlwaysReturnsNonNil guards callers from a nil map.
func TestReadCgroupStats_AlwaysReturnsNonNil(t *testing.T) {
	if ReadCgroupStats("no-such-app") == nil {
		t.Error("ReadCgroupStats returned a nil map")
	}
}
