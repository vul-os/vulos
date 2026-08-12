package stream

import (
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// signalGroup must not signal a pid that has already been reaped.
//
// cmd.Process.Pid keeps its value after the child exits, and these children are
// waited on, so the kernel is free to recycle that number. syscall.Kill(-pid, …)
// does not fail on a recycled pid — it terminates whatever process group now
// owns it. On CI that showed up as the backend job dying with exit 137
// mid-compile, no test having failed, memory at 1GB of 15GB, and `go` and `link`
// named among the runner's orphans.
//
// The guard is cmd.ProcessState != nil, which is set once Wait returns.

func TestSignalGroupSkipsAReapedProcess(t *testing.T) {
	cmd := exec.Command("/bin/sh", "-c", "exit 0")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	pid := cmd.Process.Pid
	if err := cmd.Wait(); err != nil {
		t.Fatalf("wait: %v", err)
	}
	if cmd.ProcessState == nil {
		t.Fatal("ProcessState is nil after Wait; the guard this test relies on cannot work")
	}

	// A LIVE process group standing in for whatever might inherit the pid. It is
	// not the same pid — the point is that signalGroup must not reach ANY group
	// once the command is reaped, and the only way it could is by using the
	// stale number.
	victim := exec.Command("/bin/sh", "-c", "sleep 30")
	victim.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := victim.Start(); err != nil {
		t.Fatalf("start victim: %v", err)
	}
	defer func() {
		_ = syscall.Kill(-victim.Process.Pid, syscall.SIGKILL)
		_ = victim.Wait()
	}()

	signalGroup(cmd, syscall.SIGKILL)

	// The reaped command's pid must not have been signalled. Verified by
	// asking the kernel whether that pid exists rather than by inspecting our
	// own code: signal 0 tests for existence.
	if err := syscall.Kill(pid, 0); err == nil {
		t.Logf("pid %d was already recycled by another process; the guard is what "+
			"stops us signalling it", pid)
	}

	// And the live group is untouched.
	if err := syscall.Kill(victim.Process.Pid, 0); err != nil {
		t.Errorf("the live process was killed: %v", err)
	}
}

func TestSignalGroupSignalsALiveProcess(t *testing.T) {
	cmd := exec.Command("/bin/sh", "-c", "sleep 30")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}

	signalGroup(cmd, syscall.SIGKILL)

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		t.Fatal("signalGroup did not kill a RUNNING child — the guard has been made " +
			"so strict that cleanup no longer cleans anything up")
	}
}

func TestSignalGroupToleratesNil(t *testing.T) {
	signalGroup(nil, syscall.SIGKILL)
	signalGroup(&exec.Cmd{}, syscall.SIGKILL) // never started: Process is nil
}

// The DECISION, asserted directly — see shouldSignal's comment for why the
// effect cannot be. This is the assertion that fails when the guard is removed;
// the behavioural tests above do not, because pid reuse is the kernel's call.
func TestShouldSignalRefusesAReapedCommand(t *testing.T) {
	live := exec.Command("/bin/sh", "-c", "sleep 30")
	live.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := live.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() {
		_ = syscall.Kill(-live.Process.Pid, syscall.SIGKILL)
		_ = live.Wait()
	}()

	if !shouldSignal(live) {
		t.Error("a RUNNING child is not signalled, so cleanup would leak every process")
	}

	reaped := exec.Command("/bin/sh", "-c", "exit 0")
	reaped.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := reaped.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := reaped.Wait(); err != nil {
		t.Fatalf("wait: %v", err)
	}
	if shouldSignal(reaped) {
		t.Error("a REAPED child is still signalled by pid. That pid belongs to the kernel " +
			"now and may already name another process group — killing it is how a build " +
			"job dies with exit 137 and no test having failed")
	}

	if shouldSignal(nil) || shouldSignal(&exec.Cmd{}) {
		t.Error("nil or never-started commands are signalled")
	}
}

// EVERY group kill in this package must go through signalGroup.
//
// Fixing Session.Close alone was not enough: pool.go still killed cage's group
// directly in the launch-failure path — which is the path CI takes, since no
// compositor comes up there — and stream.go still signalled gstVideo and the
// app supervisor's children the same way. A guard that one call site honours
// and five bypass is not a guard.
func TestNoDirectGroupKillsRemain(t *testing.T) {
	root := "."
	var offenders []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		if strings.HasSuffix(path, "_test.go") {
			return nil // tests may signal their own fixtures directly
		}
		src, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		for i, line := range strings.Split(string(src), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "//") {
				continue // a comment about the pattern is not the pattern
			}
			// signalGroup's own body is the sanctioned implementation — it is the
			// one place the guard has already been applied, two lines above.
			if strings.Contains(line, "syscall.Kill(-cmd.Process.Pid, sig)") {
				continue
			}
			if strings.Contains(line, "syscall.Kill(-") {
				offenders = append(offenders, fmt.Sprintf("%s:%d: %s", path, i+1, trimmed))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if len(offenders) > 0 {
		t.Errorf("these signal a process GROUP by pid without the reaped-process guard, "+
			"so a recycled pid can take an unrelated group with it — use signalGroup:\n%s",
			strings.Join(offenders, "\n"))
	}
}
