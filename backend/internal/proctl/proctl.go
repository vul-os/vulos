// Package proctl ends FOREIGN processes — ones this server did not spawn and
// holds no *exec.Cmd for — safely enough to expose over HTTP.
//
// # Why this is not internal/procgroup
//
// internal/procgroup solves the same hazard for CHILDREN: a pid is only a
// process while that process lives, and once it is reaped the kernel may hand
// the number to something else, so a signal sent by number afterwards lands on
// a stranger. procgroup's guard is `cmd.ProcessState == nil` — "Wait has not
// returned yet". That guard is unavailable here by construction. An Activity
// Monitor lists pids out of /proc; there is no exec.Cmd, no ProcessState, and
// nobody is waiting on them. The pid in a listing is a number the client read
// seconds ago and posted back, which is exactly the stale-pid shape.
//
// So this package uses the identity that the kernel itself keeps stable:
//
//	(pid, starttime)
//
// starttime is field 22 of /proc/<pid>/stat — the clock ticks since boot at
// which THAT process began. A recycled pid is a different process and therefore
// carries a different starttime. The pair is what /proc-reading tools have used
// as a process identity for decades, and it is monotonic per boot, so it cannot
// collide the way a bare pid does.
//
// Every entry point here therefore takes the starttime the caller observed and
// re-reads /proc to confirm it still holds. A mismatch is refused, never
// signalled. The check is repeated immediately before the SIGKILL of an
// escalation, because the whole point of an escalation is that time passed
// between the two signals — that gap is precisely where a pid gets recycled,
// and it is the window a naive implementation leaves wide open.
//
// # Why /proc is a parameter
//
// Root is injectable so the decisions above can be tested against a synthetic
// /proc tree, on any OS, deterministically. Whether a recycled pid ACTUALLY
// gets signalled depends on the kernel and on load and cannot be provoked in a
// unit test — the same reasoning that made procgroup.ShouldSignal exported. The
// decision can be asserted, so it is.
package proctl

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// DefaultRoot is the real procfs mount.
const DefaultRoot = "/proc"

// Errors returned by Controller. Callers map these to HTTP status codes.
var (
	// ErrNoSuchProcess — the pid has no /proc entry at all.
	ErrNoSuchProcess = errors.New("no such process")
	// ErrIdentityMismatch — the pid exists but is NOT the process the caller
	// observed. This is the recycled-pid case and is always a refusal.
	ErrIdentityMismatch = errors.New("pid no longer identifies the process you selected")
	// ErrProtected — the process is on the never-signal list. See Protect.
	ErrProtected = errors.New("protected process")
)

// Snapshot is the subset of /proc/<pid>/stat this package makes decisions on.
type Snapshot struct {
	PID   int    `json:"pid"`
	PPID  int    `json:"ppid"`
	PGID  int    `json:"pgid"`
	SID   int    `json:"sid"`
	Name  string `json:"name"`
	State string `json:"state"` // raw single-letter state: R S D Z T I
	// Start is field 22 of /proc/<pid>/stat, clock ticks since boot. It is the
	// half of the process identity that a pid alone cannot provide.
	Start uint64 `json:"start"`
	// Kernel reports a kernel thread — /proc/<pid>/cmdline is empty for these.
	// They cannot be killed (the kernel discards the signal) so offering it
	// would be a lie in the UI.
	Kernel bool `json:"kernel"`
}

// Self identifies the server process, so it cannot be talked into killing
// itself or the group it shares with its own supervisor.
type Self struct {
	PID  int
	PGID int
	SID  int
}

// CurrentSelf reads this process's own identity. Called once at startup: the
// values are fixed for the life of the process, and reading them per-request
// would let a setpgid race change the answer between check and signal.
func CurrentSelf() Self {
	pid := os.Getpid()
	s := Self{PID: pid, PGID: pid, SID: pid}
	if pgid, err := syscall.Getpgid(pid); err == nil {
		s.PGID = pgid
	}
	if sid, err := syscall.Getsid(pid); err == nil {
		s.SID = sid
	}
	return s
}

// Read returns the snapshot for pid under root.
func Read(root string, pid int) (Snapshot, error) {
	if pid <= 0 {
		return Snapshot{}, ErrNoSuchProcess
	}
	dir := filepath.Join(root, strconv.Itoa(pid))
	data, err := os.ReadFile(filepath.Join(dir, "stat"))
	if err != nil {
		return Snapshot{}, ErrNoSuchProcess
	}
	snap, ok := ParseStat(string(data), pid)
	if !ok {
		return Snapshot{}, ErrNoSuchProcess
	}
	// A kernel thread has an empty cmdline. Read errors are treated as "not a
	// kernel thread" rather than "is one": guessing the restrictive answer on
	// an unreadable file would silently make ordinary processes unkillable and
	// look like the feature is broken, whereas guessing the permissive answer
	// still runs into every other check below.
	if cmdline, err := os.ReadFile(filepath.Join(dir, "cmdline")); err == nil {
		snap.Kernel = len(strings.Trim(string(cmdline), "\x00")) == 0
	}
	return snap, nil
}

// ParseStat parses one /proc/<pid>/stat line.
//
// The comm field is parenthesised and may itself contain spaces and
// parentheses — "(a b) c" is a legal process name — so the split is anchored on
// the LAST ')' rather than on whitespace. Field numbers below are the 1-based
// proc(5) numbering; `rest[i]` is field i+3.
func ParseStat(data string, pid int) (Snapshot, bool) {
	s := Snapshot{PID: pid}
	lastParen := strings.LastIndex(data, ")")
	firstParen := strings.Index(data, "(")
	if lastParen < 0 || firstParen < 0 || lastParen <= firstParen || lastParen+2 >= len(data) {
		return s, false
	}
	s.Name = data[firstParen+1 : lastParen]
	rest := strings.Fields(data[lastParen+2:])
	if len(rest) < 20 {
		return s, false
	}
	s.State = rest[0]                                // field 3
	s.PPID, _ = strconv.Atoi(rest[1])                // field 4
	s.PGID, _ = strconv.Atoi(rest[2])                // field 5
	s.SID, _ = strconv.Atoi(rest[3])                 // field 6
	s.Start, _ = strconv.ParseUint(rest[19], 10, 64) // field 22
	return s, true
}

// Denial is a refusal to signal, with a stable machine-readable code so the UI
// can explain WHY a row's Quit button is disabled instead of failing on click.
type Denial struct {
	Code   string `json:"code"`
	Reason string `json:"reason"`
}

func (d *Denial) Error() string { return "protected: " + d.Code + ": " + d.Reason }

// Protect is the whole authorisation boundary below the role check: the set of
// processes NOBODY may signal through this API, admin included.
//
// It is a deny list rather than an allow list on purpose, and that is the one
// place this design is deliberately permissive: a box exists to run the owner's
// software, and an allow list would have to enumerate every program a user
// might ever install to be usable, which in practice means it gets widened
// until it allows everything. The role gate above it is what makes the
// capability rare; this list is what stops the rare capability being
// self-destructive.
//
// Each entry is a case where the signal either bricks the box or does nothing:
//
//   - pid 1. On the bare-metal path cmd/init is PID 1 with no credential drop,
//     so "kill 1" is "power off the box from a web page". Even on a hosted
//     path pid 1 is the container's init and killing it ends everything.
//   - the server itself, and anything sharing its process GROUP. The group
//     covers the supervisor/session the server was started from: killing it is
//     indistinguishable to the user from the box crashing, and it takes the
//     audit log's own writer down with it.
//   - kernel threads. The kernel discards signals to them, so a Quit button
//     over a kthread row is a control that cannot work.
//
// Deliberately NOT here: uid. On the netboot path the server runs as root and
// so does everything else, so a "no root processes" rule would refuse every
// process on exactly the deployment where the feature is most needed, which is
// the shape of a rule that gets deleted rather than obeyed.
func Protect(snap Snapshot, self Self) *Denial {
	switch {
	case snap.PID <= 0:
		return &Denial{"invalid_pid", "not a valid pid"}
	case snap.PID == 1:
		return &Denial{"init", "pid 1 is the system init; ending it stops the box"}
	case snap.PID == self.PID:
		return &Denial{"self", "this is the Vulos server process"}
	case self.PGID != 0 && snap.PGID == self.PGID:
		return &Denial{"self_group", "shares a process group with the Vulos server"}
	case snap.Kernel:
		return &Denial{"kernel_thread", "kernel threads do not receive signals"}
	}
	return nil
}

// Mode selects the escalation.
type Mode string

const (
	// ModeQuit is the polite path: SIGTERM, wait, then SIGKILL if still there.
	ModeQuit Mode = "quit"
	// ModeForce is Force Quit: SIGKILL with no grace period.
	ModeForce Mode = "force"
)

// DefaultGrace is how long ModeQuit waits between SIGTERM and SIGKILL.
//
// This is a decision, not a leftover. Five seconds is chosen against three
// reference points: macOS shows "Force Quit" after roughly four seconds of an
// unanswered quit, this repo's own appnet.Launcher.Stop already uses three, and
// an editor or database flushing to disk on SIGTERM wants more than one. Below
// about two seconds a healthy program that is merely mid-fsync gets SIGKILLed
// and the user loses data they never agreed to lose; above about ten the UI has
// to sit on a spinner long enough that the user assumes the button is broken
// and clicks it again. Five is inside both bounds with room on each side.
//
// It is per-request overridable within [0, MaxGrace] because the right answer
// genuinely differs — a hung game wants 0, a database wants 30.
const DefaultGrace = 5 * time.Second

// MaxGrace bounds the client-supplied grace period. An unbounded value would
// let one request pin a server goroutine indefinitely.
const MaxGrace = 30 * time.Second

// pollInterval is how often the escalation re-checks whether the process went
// away. Fine enough that a fast exit is reported as SIGTERM-terminated rather
// than escalated, coarse enough not to spin.
const pollInterval = 100 * time.Millisecond

// Outcome describes how a request ended.
type Outcome string

const (
	// OutcomeAlreadyGone — the process had exited before the first signal.
	OutcomeAlreadyGone Outcome = "already_gone"
	// OutcomeTerminated — the process exited after SIGTERM, within grace.
	OutcomeTerminated Outcome = "terminated"
	// OutcomeKilled — the process required SIGKILL.
	OutcomeKilled Outcome = "killed"
	// OutcomeSurvived — SIGKILL was delivered and the process is STILL there.
	// Real and not an error path: a task in uninterruptible sleep (state D,
	// almost always blocked storage or a wedged NFS mount) cannot be killed
	// until the I/O returns, and a zombie is already dead and merely unreaped.
	// Reporting this honestly is the point; claiming success would leave the
	// user believing they fixed something they did not.
	OutcomeSurvived Outcome = "survived"
)

// Result is the report of one signal request.
type Result struct {
	PID     int      `json:"pid"`
	Name    string   `json:"name"`
	Outcome Outcome  `json:"outcome"`
	Signals []string `json:"signals"`
	// State is the process state at the end, when it is still present. "D"
	// here is the honest explanation for OutcomeSurvived.
	State     string `json:"state,omitempty"`
	ElapsedMS int64  `json:"elapsed_ms"`
}

// Controller ends processes under Root.
type Controller struct {
	Root string
	Self Self

	// Signal and Sleep are injectable so the escalation ORDER and the
	// re-verification before SIGKILL can be asserted without a real process
	// and without a real wall-clock wait.
	Signal func(pid int, sig syscall.Signal) error
	Sleep  func(d time.Duration)
	Now    func() time.Time
}

// New returns a Controller wired to the real kernel.
func New(root string, self Self) *Controller {
	return &Controller{
		Root:   root,
		Self:   self,
		Signal: func(pid int, sig syscall.Signal) error { return syscall.Kill(pid, sig) },
		Sleep:  time.Sleep,
		Now:    time.Now,
	}
}

// Request is one signal request. Start is REQUIRED and carries the starttime
// the caller observed when it listed the process.
//
// Making it required rather than optional is the entire recycled-pid defence.
// An optional field would be omitted by every caller that "just has a pid" —
// which is every caller, since a pid is what a listing shows — and the guard
// would then be present in the code and absent from every call. A request
// without Start is rejected, so the check cannot be skipped by accident.
type Request struct {
	PID   int           `json:"pid"`
	Start uint64        `json:"start"`
	Mode  Mode          `json:"mode"`
	Grace time.Duration `json:"-"`
}

// Verify re-reads pid and confirms it is still the process the caller meant.
func (c *Controller) Verify(pid int, start uint64) (Snapshot, error) {
	snap, err := Read(c.Root, pid)
	if err != nil {
		return Snapshot{}, err
	}
	if snap.Start != start {
		return snap, ErrIdentityMismatch
	}
	return snap, nil
}

// Do runs the request: verify identity, check the protect list, then signal.
func (c *Controller) Do(req Request) (Result, error) {
	started := c.Now()
	res := Result{PID: req.PID}

	snap, err := c.Verify(req.PID, req.Start)
	if err != nil {
		return res, err
	}
	res.Name = snap.Name
	if d := Protect(snap, c.Self); d != nil {
		return res, d
	}

	grace := req.Grace
	if grace < 0 || grace > MaxGrace {
		grace = MaxGrace
	}
	if req.Mode == ModeForce {
		grace = 0
	}

	if grace > 0 {
		res.Signals = append(res.Signals, "SIGTERM")
		if err := c.Signal(req.PID, syscall.SIGTERM); err != nil {
			// ESRCH here means it exited between Verify and the signal. That is
			// the benign end of the race and is reported, not failed.
			res.Outcome = OutcomeAlreadyGone
			res.ElapsedMS = c.elapsed(started)
			return res, nil
		}
		if gone, _ := c.waitGone(req.PID, req.Start, grace); gone {
			res.Outcome = OutcomeTerminated
			res.ElapsedMS = c.elapsed(started)
			return res, nil
		}
	}

	// WHERE THE PRE-SIGKILL IDENTITY GUARD ACTUALLY LIVES: waitGone, above.
	//
	// This is the dangerous moment — a whole grace period has passed since the
	// check at the top of this function, by design, and that gap is exactly
	// when a pid gets recycled onto something else. The first draft put an
	// explicit second c.Verify here to cover it. Mutation testing killed that
	// line: replacing it with an always-false condition changed nothing,
	// because waitGone re-verifies the FULL (pid, starttime) identity on every
	// poll including the one at its deadline, and returns "gone" the instant
	// the identity stops holding. Control only reaches this line after a
	// verification that just succeeded.
	//
	// The redundant check was deleted rather than kept "for safety". A guard
	// that cannot be shown to do anything is not safety, it is a second place
	// a future reader must reason about, and — worse — reassurance that makes
	// the real guard in waitGone look optional. Weakening waitGone's Verify to
	// a bare existence check is the mutation that matters here, and it is
	// killed by TestDo_ReVerifiesIdentityBeforeTheSIGKILL.
	res.Signals = append(res.Signals, "SIGKILL")
	if err := c.Signal(req.PID, syscall.SIGKILL); err != nil {
		res.Outcome = OutcomeAlreadyGone
		res.ElapsedMS = c.elapsed(started)
		return res, nil
	}
	if gone, _ := c.waitGone(req.PID, req.Start, 2*time.Second); gone {
		res.Outcome = OutcomeKilled
		res.ElapsedMS = c.elapsed(started)
		return res, nil
	}

	res.Outcome = OutcomeSurvived
	if s, err := Read(c.Root, req.PID); err == nil {
		res.State = s.State
	}
	res.ElapsedMS = c.elapsed(started)
	return res, nil
}

// waitGone polls until (pid, start) no longer identifies a live process.
//
// "Gone" deliberately includes "the pid is now a DIFFERENT process": the
// process we were asked about has ended either way, and treating a recycled pid
// as still-alive would escalate to SIGKILL against the new occupant.
func (c *Controller) waitGone(pid int, start uint64, budget time.Duration) (bool, Snapshot) {
	deadline := c.Now().Add(budget)
	for {
		snap, err := c.Verify(pid, start)
		if err != nil {
			return true, snap
		}
		// A zombie has already exited; it lingers only until its parent reaps
		// it, and no further signal changes that.
		if snap.State == "Z" {
			return true, snap
		}
		if !c.Now().Before(deadline) {
			return false, snap
		}
		c.Sleep(pollInterval)
	}
}

func (c *Controller) elapsed(from time.Time) int64 {
	return c.Now().Sub(from).Milliseconds()
}

// DenialFor reports why pid may not be signalled, or nil if it may be. Used by
// the process LISTING so the UI can render a disabled control with a reason,
// rather than an enabled control that fails.
func DenialFor(root string, pid int, self Self) *Denial {
	snap, err := Read(root, pid)
	if err != nil {
		return &Denial{"gone", "process no longer exists"}
	}
	return Protect(snap, self)
}
