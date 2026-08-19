package pty

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"vulos/backend/internal/proctl"
)

// EXEC-PGID-01 regression gates.
//
// These assert the OBSERVED process group of a real child, not the presence of
// a line of source. A source-scanning test would pass against a Setpgid that
// had been set on the wrong Cmd, or set and then overwritten; these go red for
// any of that, because they read the number the kernel actually assigned.

// selfPGID is the process group of the test binary — i.e. the group an exec'd
// child inherits when SysProcAttr is left nil, which is exactly the defect.
func selfPGID(t *testing.T) int {
	t.Helper()
	pgid, err := syscall.Getpgid(os.Getpid())
	if err != nil {
		t.Fatalf("Getpgid(self): %v", err)
	}
	return pgid
}

// childPGID runs one command through Exec and returns the process group the
// kernel gave it. `$$` is the shell's own pid; ps reports that pid's pgid.
func childPGID(t *testing.T) int {
	t.Helper()
	res := Exec(context.Background(), "ps -o pgid= -p $$ 2>/dev/null")
	if res.ExitCode != 0 {
		t.Skipf("ps -o pgid= unavailable here (exit %d, output %q)", res.ExitCode, res.Output)
	}
	field := strings.TrimSpace(res.Output)
	pgid, err := strconv.Atoi(field)
	if err != nil {
		t.Skipf("could not parse pgid from ps output %q: %v", res.Output, err)
	}
	return pgid
}

// A command run through /api/exec must NOT share the server's process group.
//
// Delete `cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}` from Exec and
// the child inherits this test binary's group, the two numbers match, and this
// fails. That is the whole property: on a booted box the inherited group is the
// vulos-server's, so a group-directed signal aimed at a Portal command reaches
// the server.
func TestExec_ChildGetsItsOwnProcessGroup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("exec tests require a POSIX shell")
	}
	self := selfPGID(t)
	child := childPGID(t)
	if child == self {
		t.Fatalf("EXEC-PGID-01: /api/exec child shares the server's process group "+
			"(pgid=%d). A group-directed signal aimed at the child would hit the "+
			"server, and proctl.Protect refuses the child as \"self_group\". "+
			"Exec must set SysProcAttr.Setpgid.", child)
	}
}

// The consequence that made the shared group visible in the product: with the
// child in the server's group, proctl refuses to end it, so an /api/exec
// command could not be stopped from Activity Monitor at all.
//
// Asserted against the REAL observed pgid rather than an invented one, so it
// tracks whatever Exec actually does.
func TestExec_ChildIsNotProtectedAsServerGroup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("exec tests require a POSIX shell")
	}
	child := childPGID(t)
	self := proctl.Self{PID: os.Getpid(), PGID: selfPGID(t), SID: os.Getpid()}

	// A snapshot shaped like the exec'd shell: a live, non-kernel process in
	// the group Exec actually put it in.
	snap := proctl.Snapshot{PID: child + 1, PGID: child, SID: self.SID}
	if d := proctl.Protect(snap, self); d != nil {
		t.Fatalf("EXEC-PGID-01: an /api/exec child is refused by proctl.Protect as %q (%s). "+
			"Exec's child must not share the server's process group.", d.Code, d.Reason)
	}
}

// Cancelling the context must end the command's whole process group, not just
// the shell.
//
// The marker file is written by a BACKGROUNDED GRANDCHILD. exec.CommandContext's
// default cancel is cmd.Process.Kill(), which reaps `bash` and leaves that
// subshell running to completion — so with either the Setpgid or the Cancel
// hook removed from Exec, the sleep survives, the file appears, and this fails.
//
// (Without Setpgid the group kill is not merely ineffective, it is aimed at the
// wrong group: -pid names the group whose id is pid, and the child's group id
// would be the server's.)
func TestExec_CancelKillsTheWholeProcessGroup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("exec tests require a POSIX shell")
	}
	marker := filepath.Join(t.TempDir(), "grandchild-survived")

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	start := time.Now()
	Exec(ctx, "( sleep 2; touch "+marker+" ) & echo started; sleep 30")
	elapsed := time.Since(start)

	// Wait must not block behind the grandchild's inherited stdout pipe.
	if elapsed > 10*time.Second {
		t.Fatalf("EXEC-PGID-01: Exec blocked %v after a 300ms deadline — a "+
			"backgrounded grandchild is holding the output pipe open", elapsed)
	}

	// Give the grandchild's sleep well past its 2s deadline to prove it is gone.
	time.Sleep(3 * time.Second)
	if _, err := os.Stat(marker); err == nil {
		t.Fatalf("EXEC-PGID-01: a backgrounded grandchild of a cancelled /api/exec "+
			"command survived cancellation and wrote %s. Exec must kill the child's "+
			"process GROUP, and the child must be in its own group for that to mean "+
			"anything.", marker)
	}
}

// Exec must return within a bounded time even when a descendant ESCAPES the
// process group, because a group kill cannot reach such a process.
//
// `set -m` turns on job control, which makes bash put each job in a process
// group of its own — so both the backgrounded subshell and the foreground sleep
// leave the group Exec created, and SIGKILL to that group misses them. They
// still hold the write end of the stdout pipe that os/exec created (Stdout is a
// *bytes.Buffer, not a file, so a pipe and a copying goroutine are involved),
// and Wait does not return until that descriptor is closed by everyone.
//
// Measured on the code this replaced: 30.02s against a 300ms deadline. The
// duration is the descendant's, not ours — `sleep 9999 &` pinned the handler
// goroutine for hours, past the caller's deadline AND past Exec's own 10s
// timeout, neither of which can end a blocked Wait. cmd.WaitDelay is the only
// thing that bounds it: it force-closes the inherited descriptors and lets Wait
// return.
//
// Delete `cmd.WaitDelay = execWaitDelay` and this goes from ~2.3s to ~30s.
func TestExec_DoesNotHangOnADescendantThatEscapesTheGroup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("exec tests require a POSIX shell")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	start := time.Now()
	Exec(ctx, "set -m; ( sleep 25 ) & sleep 30")
	elapsed := time.Since(start)

	// Bound is the descendant's 25s vs our 300ms + 2s WaitDelay. Anything near
	// the former means Wait blocked on an inherited pipe.
	if elapsed > 8*time.Second {
		t.Fatalf("EXEC-PGID-01: Exec took %v against a 300ms deadline — a descendant "+
			"that escaped the process group is holding the output pipe open and "+
			"neither the caller's context nor Exec's own 10s timeout can end a "+
			"blocked Wait. cmd.WaitDelay must bound it.", elapsed)
	}
}
