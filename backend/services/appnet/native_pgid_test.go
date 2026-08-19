package appnet

import (
	"os"
	"runtime"
	"syscall"
	"testing"
	"time"

	"vulos/backend/internal/proctl"
)

// A native app must not land in the server's process group.
//
// This reads the pgid the kernel actually assigned to a real spawned process,
// not the presence of a line of source: remove the Setpgid from LaunchNative
// and the number it returns becomes this test binary's own group, which on a
// booted box is vulos-server's.
func TestLaunchNative_GetsItsOwnProcessGroup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires POSIX process groups")
	}
	pid, err := LaunchNative(NativeLaunchSpec{Binary: "sleep", Args: []string{"5"}})
	if err != nil {
		t.Skipf("could not launch sleep here: %v", err)
	}

	pgid, err := syscall.Getpgid(pid)
	if err != nil {
		t.Fatalf("Getpgid(%d): %v", pid, err)
	}
	selfPgid, err := syscall.Getpgid(os.Getpid())
	if err != nil {
		t.Fatalf("Getpgid(self): %v", err)
	}

	// Cleanup has to be safe on the FAILING code path, not just the passing one.
	// Group-killing unconditionally was wrong for exactly the reason this test
	// exists: when the guard is broken the child's group IS this test binary's
	// group, so `kill -KILL -pgid` killed the test runner. The mutation run
	// produced no output at all and exited 1 — a broken guard that destroys the
	// evidence of its own breakage is worse than no guard.
	t.Cleanup(func() {
		if pgid != selfPgid {
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
		} else if p, err := os.FindProcess(pid); err == nil {
			_ = p.Kill()
		}
		time.Sleep(50 * time.Millisecond)
	})

	if pgid == selfPgid {
		t.Fatalf("a native app was launched into the server's own process group "+
			"(pgid=%d). LaunchNative must set SysProcAttr.Setpgid.", pgid)
	}

	// The consequence that made this visible: LaunchNative hands back a pid and
	// never stops the process, so proctl is the only control the product offers
	// for a native app. In the server's group, proctl refuses it as
	// "self_group" and the End-process button cannot work.
	self := proctl.Self{PID: os.Getpid(), PGID: selfPgid, SID: os.Getpid()}
	snap := proctl.Snapshot{PID: pid, PGID: pgid, SID: self.SID}
	if d := proctl.Protect(snap, self); d != nil {
		t.Fatalf("a native app is refused by proctl.Protect as %q (%s) — it is the "+
			"only way to stop one, so it must not be in the server's group",
			d.Code, d.Reason)
	}
}
