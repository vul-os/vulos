package pty

import (
	"bytes"
	"context"
	"os/exec"
	"syscall"
	"time"

	"vulos/backend/internal/procgroup"
)

// ExecResult is the output of a one-shot command.
type ExecResult struct {
	Command  string `json:"command"`
	Output   string `json:"output"`
	ExitCode int    `json:"exit_code"`
	Duration string `json:"duration"`
}

// execWaitDelay bounds how long Wait may block after the context is cancelled
// and the group has been killed.
//
// Stdout/Stderr here are *bytes.Buffer, not *os.File, so os/exec creates a pipe
// and copies in a goroutine — and Wait does not return until the WRITE end is
// closed by every process holding it. A backgrounded grandchild inherits that
// descriptor, so `sleep 300 &` inside the command kept Wait blocked long after
// the 10 s deadline had passed and long after the shell itself was dead. The
// group kill below closes that in the normal case; WaitDelay is what makes the
// pathological case (a process that survives SIGKILL, e.g. uninterruptible I/O)
// bounded rather than permanent.
const execWaitDelay = 2 * time.Second

// Exec runs a single command and returns its output.
// Used by the Portal for /commands without needing a full PTY session.
// Timeout: 10 seconds.
//
// # Why the child gets its own process group
//
// EXEC-PGID-01. This used to leave SysProcAttr nil, so the shell and everything
// it spawned inherited the SERVER's process group. Three consequences, all real
// and all measured on a booted box where vulos-server ran as root with pgid 491:
//
//  1. Any group-directed signal aimed at an exec'd child — `kill -TERM -491`,
//     or the same thing from a supervisor or a cleanup script — landed on the
//     Vulos server itself. The blast radius of ending one Portal command was
//     the whole box.
//
//  2. The reverse: proctl.Protect refuses any pid whose PGID equals the
//     server's, with the denial code "self_group". Every /api/exec child
//     therefore reported "shares a process group with the Vulos server" in
//     Activity Monitor and could not be ended from the UI at all. The
//     protection that exists to keep the server alive was, correctly by its own
//     rule, shielding processes it was never meant to shield.
//
//  3. Cleanup was impossible to express. There was no group that named "this
//     command and its children" and nothing else, so a timed-out command's
//     grandchildren simply leaked.
//
// Launched apps already ran in their own group (appnet's newAppSysProcAttr and
// every exec site in services/stream set Setpgid), which is why apps were
// unaffected and only this path was exposed. Setpgid here makes /api/exec agree
// with them.
//
// Setpgid and the Cancel hook are ONE change, not two. syscall.Kill(-pid, …)
// names the group whose id is pid; without Setpgid the child's group id is the
// SERVER's, not the child's, so a negative-pid kill would either hit nothing
// (ESRCH) or — if the number ever collided — the server. Adding the group kill
// without the group is the bug this is fixing, pointed the other way.
func Exec(ctx context.Context, command string) ExecResult {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	start := time.Now()
	cmd := exec.CommandContext(ctx, "bash", "-c", command)

	// Own process group — see the doc comment above.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	// Kill the GROUP on cancellation, not just the shell. The default
	// CommandContext cancel is cmd.Process.Kill(), which reaps `bash` and
	// leaves everything it spawned running. procgroup.Signal carries the
	// reaped-pid guard (a signal by number after Wait has returned can land on
	// whatever inherited the pid); returning nil keeps Wait reporting the
	// process's own exit status, which is what the default cancel did too, so
	// the ExitCode this function returns is unchanged.
	cmd.Cancel = func() error {
		procgroup.Signal(cmd, syscall.SIGKILL)
		return nil
	}
	cmd.WaitDelay = execWaitDelay

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = -1
		}
	}

	output := stdout.String()
	if stderr.Len() > 0 {
		if output != "" {
			output += "\n"
		}
		output += stderr.String()
	}

	// Truncate to 10KB
	if len(output) > 10240 {
		output = output[:10240] + "\n... (truncated)"
	}

	return ExecResult{
		Command:  command,
		Output:   output,
		ExitCode: exitCode,
		Duration: time.Since(start).Truncate(time.Millisecond).String(),
	}
}
