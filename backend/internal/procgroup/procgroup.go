// Package procgroup sends signals to a child's PROCESS GROUP without the hazard
// that comes with doing it by pid.
//
// syscall.Kill(-pid, sig) signals the whole process group whose id is pid. That
// is correct while the child is alive and was started with Setpgid. It is not
// correct afterwards: exec.Cmd keeps Process.Pid set after the child exits, and
// once the child has been waited on the kernel is free to hand that number to
// something else. The call does not fail — it terminates whatever process group
// now owns the number.
//
// This was found while investigating a CI job that died with exit 137
// mid-compile, no test having failed, and the runner naming `go` and `link`
// among the orphaned processes: a build spawning hundreds of short-lived
// compiler processes is the workload that recycles pids fastest. Memory was
// ruled out by measurement — the job's cgroup reported oom 0, oom_kill 0,
// oom_group_kill 0 with no limit set — so a signal from inside the run was what
// remained.
package procgroup

import (
	"os/exec"
	"syscall"
)

// ShouldSignal reports whether cmd's group can still be signalled safely.
//
// ProcessState is non-nil only after Wait has returned, which is the cheapest
// available "already reaped" marker. It does not close the window entirely — a
// concurrent Wait can still land between this check and the kill — but it
// removes the large one, where a process reaped seconds or minutes ago is
// signalled by number.
//
// Exported so it can be asserted directly: whether a recycled pid actually gets
// signalled depends on the kernel and the machine's load, so the EFFECT cannot
// be tested deterministically. The decision can.
func ShouldSignal(cmd *exec.Cmd) bool {
	return cmd != nil && cmd.Process != nil && cmd.ProcessState == nil
}

// Signal sends sig to cmd's process group, and only while cmd is still running.
func Signal(cmd *exec.Cmd, sig syscall.Signal) {
	if !ShouldSignal(cmd) {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, sig)
}
