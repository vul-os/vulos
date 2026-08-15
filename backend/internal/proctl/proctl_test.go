package proctl

import (
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"
)

// fakeProc builds a synthetic /proc tree. Every test drives real code against
// it: the parsing, the identity check, the protect list and the escalation are
// the SHIPPING functions, not re-implementations.
type fakeProc struct{ root string }

func newFakeProc(t *testing.T) *fakeProc {
	t.Helper()
	return &fakeProc{root: t.TempDir()}
}

// add writes /proc/<pid>/stat and cmdline. `comm` is written verbatim inside
// the parens so a test can use a name containing spaces and brackets.
func (f *fakeProc) add(t *testing.T, pid, ppid, pgid, sid int, comm, state string, start uint64, cmdline string) {
	t.Helper()
	dir := filepath.Join(f.root, strconv.Itoa(pid))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// proc(5) fields 1..24. Only 3,4,5,6 and 22 are read, but the line is
	// written full-length so a wrong index is caught rather than tolerated.
	fields := make([]string, 0, 24)
	fields = append(fields, strconv.Itoa(pid), "("+comm+")", state,
		strconv.Itoa(ppid), strconv.Itoa(pgid), strconv.Itoa(sid))
	for i := 7; i <= 24; i++ {
		if i == 22 {
			fields = append(fields, strconv.FormatUint(start, 10))
			continue
		}
		fields = append(fields, strconv.Itoa(i*1000))
	}
	line := ""
	for i, v := range fields {
		if i > 0 {
			line += " "
		}
		line += v
	}
	if err := os.WriteFile(filepath.Join(dir, "stat"), []byte(line+"\n"), 0o644); err != nil {
		t.Fatalf("write stat: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cmdline"), []byte(cmdline), 0o644); err != nil {
		t.Fatalf("write cmdline: %v", err)
	}
}

func (f *fakeProc) remove(t *testing.T, pid int) {
	t.Helper()
	if err := os.RemoveAll(filepath.Join(f.root, strconv.Itoa(pid))); err != nil {
		t.Fatalf("remove: %v", err)
	}
}

// ---------------------------------------------------------------------------
// ParseStat
// ---------------------------------------------------------------------------

// A process name may contain spaces AND parentheses — "(a b) c" is legal. A
// parser that splits on whitespace, or that anchors on the FIRST ')', reads
// every subsequent field one position off, which silently turns starttime into
// some other number and makes the identity check compare garbage to garbage.
func TestParseStat_NameWithSpacesAndParens(t *testing.T) {
	line := "42 (weird (name) here) S 7 9 11 " // fields 1..6
	for i := 7; i <= 24; i++ {
		if i == 22 {
			line += "555666 "
			continue
		}
		line += strconv.Itoa(i*1000) + " "
	}
	snap, ok := ParseStat(line, 42)
	if !ok {
		t.Fatalf("ParseStat returned !ok for a valid line")
	}
	if snap.Name != "weird (name) here" {
		t.Errorf("Name = %q, want %q", snap.Name, "weird (name) here")
	}
	if snap.State != "S" {
		t.Errorf("State = %q, want S", snap.State)
	}
	if snap.PPID != 7 || snap.PGID != 9 || snap.SID != 11 {
		t.Errorf("PPID/PGID/SID = %d/%d/%d, want 7/9/11", snap.PPID, snap.PGID, snap.SID)
	}
	if snap.Start != 555666 {
		t.Errorf("Start = %d, want 555666 (field 22 — a wrong index makes the identity check meaningless)", snap.Start)
	}
}

func TestRead_KernelThreadDetectedByEmptyCmdline(t *testing.T) {
	f := newFakeProc(t)
	f.add(t, 2, 0, 0, 0, "kthreadd", "S", 1, "")
	f.add(t, 300, 1, 300, 300, "bash", "S", 900, "/bin/bash\x00")

	kt, err := Read(f.root, 2)
	if err != nil {
		t.Fatalf("Read(2): %v", err)
	}
	if !kt.Kernel {
		t.Error("pid 2 with empty cmdline should be flagged as a kernel thread")
	}
	user, err := Read(f.root, 300)
	if err != nil {
		t.Fatalf("Read(300): %v", err)
	}
	if user.Kernel {
		t.Error("pid 300 has a cmdline and must not be flagged as a kernel thread")
	}
}

// ---------------------------------------------------------------------------
// Protect — the authorisation boundary
// ---------------------------------------------------------------------------

func TestProtect_DeniesTheProcessesThatWouldBrickTheBox(t *testing.T) {
	self := Self{PID: 500, PGID: 500, SID: 500}
	cases := []struct {
		name string
		snap Snapshot
		code string
	}{
		{"pid 1 is init", Snapshot{PID: 1, PGID: 1}, "init"},
		{"the server itself", Snapshot{PID: 500, PGID: 500}, "self"},
		{"a sibling in the server's process group", Snapshot{PID: 777, PGID: 500}, "self_group"},
		{"a kernel thread", Snapshot{PID: 88, PGID: 0, Kernel: true}, "kernel_thread"},
		{"pid zero", Snapshot{PID: 0}, "invalid_pid"},
		{"a negative pid", Snapshot{PID: -1}, "invalid_pid"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := Protect(tc.snap, self)
			if d == nil {
				t.Fatalf("Protect allowed %+v — this process must never be signalled", tc.snap)
			}
			if d.Code != tc.code {
				t.Errorf("code = %q, want %q", d.Code, tc.code)
			}
		})
	}
}

func TestProtect_AllowsAnOrdinaryUserProcess(t *testing.T) {
	self := Self{PID: 500, PGID: 500, SID: 500}
	// Same SESSION as the server but a different process group. Sessions are
	// far broader than groups — on a login shell every job shares one — so
	// denying by session would refuse most of what a user wants to quit.
	snap := Snapshot{PID: 901, PGID: 901, SID: 500}
	if d := Protect(snap, self); d != nil {
		t.Fatalf("Protect denied an ordinary process: %v", d)
	}
}

// ---------------------------------------------------------------------------
// Identity — the recycled-pid defence
// ---------------------------------------------------------------------------

func TestVerify_RefusesARecycledPid(t *testing.T) {
	f := newFakeProc(t)
	self := Self{PID: 500, PGID: 500, SID: 500}
	c := New(f.root, self)

	// The client listed pid 4242 when it had starttime 111.
	f.add(t, 4242, 300, 4242, 300, "editor", "S", 111, "/usr/bin/editor\x00")
	if _, err := c.Verify(4242, 111); err != nil {
		t.Fatalf("Verify on the unchanged process: %v", err)
	}

	// It exited; the kernel handed 4242 to something else, which has a
	// different starttime because it began later.
	f.remove(t, 4242)
	f.add(t, 4242, 1, 4242, 1, "sshd", "S", 999, "/usr/sbin/sshd\x00")

	if _, err := c.Verify(4242, 111); err != ErrIdentityMismatch {
		t.Fatalf("Verify err = %v, want ErrIdentityMismatch — a bare pid check would have signalled sshd", err)
	}
}

func TestDo_RefusesToSignalARecycledPid(t *testing.T) {
	f := newFakeProc(t)
	c := New(f.root, Self{PID: 500, PGID: 500, SID: 500})
	var sent []int
	c.Signal = func(pid int, sig syscall.Signal) error { sent = append(sent, pid); return nil }
	c.Sleep = func(time.Duration) {}

	f.add(t, 4242, 1, 4242, 1, "sshd", "S", 999, "/usr/sbin/sshd\x00")

	_, err := c.Do(Request{PID: 4242, Start: 111, Mode: ModeQuit, Grace: DefaultGrace})
	if err != ErrIdentityMismatch {
		t.Fatalf("Do err = %v, want ErrIdentityMismatch", err)
	}
	if len(sent) != 0 {
		t.Fatalf("Do sent %d signal(s) to a recycled pid; it must send none", len(sent))
	}
}

// The escalation's own gap is the dangerous one: the grace period is time
// deliberately allowed to pass between SIGTERM and SIGKILL, which is exactly
// when a pid gets recycled. A check only at the top of Do would pass, then
// SIGKILL a stranger.
func TestDo_ReVerifiesIdentityBeforeTheSIGKILL(t *testing.T) {
	f := newFakeProc(t)
	c := New(f.root, Self{PID: 500, PGID: 500, SID: 500})

	f.add(t, 4242, 300, 4242, 300, "hung-app", "S", 111, "/usr/bin/hung-app\x00")

	var sent []syscall.Signal
	c.Signal = func(pid int, sig syscall.Signal) error {
		sent = append(sent, sig)
		if sig == syscall.SIGTERM {
			// It dies, and the pid is immediately reused by a new process.
			f.remove(t, 4242)
			f.add(t, 4242, 1, 4242, 1, "postgres", "S", 900, "/usr/bin/postgres\x00")
		}
		return nil
	}
	c.Sleep = func(time.Duration) {}

	res, err := c.Do(Request{PID: 4242, Start: 111, Mode: ModeQuit, Grace: DefaultGrace})
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	for _, s := range sent {
		if s == syscall.SIGKILL {
			t.Fatalf("Do sent SIGKILL after the pid was recycled — it landed on postgres")
		}
	}
	if res.Outcome != OutcomeTerminated {
		t.Errorf("Outcome = %q, want %q", res.Outcome, OutcomeTerminated)
	}
}

// ---------------------------------------------------------------------------
// Escalation
// ---------------------------------------------------------------------------

func TestDo_QuitEscalatesToSIGKILLOnlyAfterTheGracePeriod(t *testing.T) {
	f := newFakeProc(t)
	c := New(f.root, Self{PID: 500, PGID: 500, SID: 500})
	f.add(t, 4242, 300, 4242, 300, "wedged", "S", 111, "/usr/bin/wedged\x00")

	// A clock the test advances, so the grace period is exercised for real
	// rather than skipped by a zero-duration Sleep.
	now := time.Unix(0, 0)
	c.Now = func() time.Time { return now }
	c.Sleep = func(d time.Duration) { now = now.Add(d) }

	var sent []syscall.Signal
	c.Signal = func(pid int, sig syscall.Signal) error {
		sent = append(sent, sig)
		if sig == syscall.SIGKILL {
			f.remove(t, 4242) // SIGKILL always works on this process
		}
		return nil
	}

	res, err := c.Do(Request{PID: 4242, Start: 111, Mode: ModeQuit, Grace: DefaultGrace})
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if len(sent) != 2 || sent[0] != syscall.SIGTERM || sent[1] != syscall.SIGKILL {
		t.Fatalf("signals = %v, want [SIGTERM SIGKILL] in that order", sent)
	}
	if res.Outcome != OutcomeKilled {
		t.Errorf("Outcome = %q, want %q", res.Outcome, OutcomeKilled)
	}
	// The whole point of a grace period is that SIGKILL is not immediate.
	if res.ElapsedMS < DefaultGrace.Milliseconds() {
		t.Errorf("elapsed %dms — SIGKILL arrived before the %s grace period expired",
			res.ElapsedMS, DefaultGrace)
	}
}

func TestDo_QuitReportsTerminatedWhenSIGTERMIsEnough(t *testing.T) {
	f := newFakeProc(t)
	c := New(f.root, Self{PID: 500, PGID: 500, SID: 500})
	f.add(t, 4242, 300, 4242, 300, "polite", "S", 111, "/usr/bin/polite\x00")
	c.Sleep = func(time.Duration) {}
	var sent []syscall.Signal
	c.Signal = func(pid int, sig syscall.Signal) error {
		sent = append(sent, sig)
		f.remove(t, 4242)
		return nil
	}
	res, err := c.Do(Request{PID: 4242, Start: 111, Mode: ModeQuit, Grace: DefaultGrace})
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if len(sent) != 1 || sent[0] != syscall.SIGTERM {
		t.Fatalf("signals = %v, want [SIGTERM] only", sent)
	}
	if res.Outcome != OutcomeTerminated {
		t.Errorf("Outcome = %q, want %q", res.Outcome, OutcomeTerminated)
	}
}

func TestDo_ForceSkipsSIGTERMEntirely(t *testing.T) {
	f := newFakeProc(t)
	c := New(f.root, Self{PID: 500, PGID: 500, SID: 500})
	f.add(t, 4242, 300, 4242, 300, "frozen", "S", 111, "/usr/bin/frozen\x00")
	c.Sleep = func(time.Duration) {}
	var sent []syscall.Signal
	c.Signal = func(pid int, sig syscall.Signal) error {
		sent = append(sent, sig)
		f.remove(t, 4242)
		return nil
	}
	// Grace is set to a non-zero value to prove ModeForce OVERRIDES it rather
	// than merely defaulting to zero.
	res, err := c.Do(Request{PID: 4242, Start: 111, Mode: ModeForce, Grace: DefaultGrace})
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if len(sent) != 1 || sent[0] != syscall.SIGKILL {
		t.Fatalf("signals = %v, want [SIGKILL] only", sent)
	}
	if res.Outcome != OutcomeKilled {
		t.Errorf("Outcome = %q, want %q", res.Outcome, OutcomeKilled)
	}
}

// A task in uninterruptible sleep does not die on SIGKILL until its I/O
// returns. Reporting "killed" there would tell the user they fixed a stuck
// disk, which they did not.
func TestDo_ReportsSurvivedForAnUnkillableDStateTask(t *testing.T) {
	f := newFakeProc(t)
	c := New(f.root, Self{PID: 500, PGID: 500, SID: 500})
	f.add(t, 4242, 300, 4242, 300, "nfs-reader", "D", 111, "/usr/bin/cp\x00")
	now := time.Unix(0, 0)
	c.Now = func() time.Time { return now }
	c.Sleep = func(d time.Duration) { now = now.Add(d) }
	c.Signal = func(pid int, sig syscall.Signal) error { return nil } // nothing dies

	res, err := c.Do(Request{PID: 4242, Start: 111, Mode: ModeForce})
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if res.Outcome != OutcomeSurvived {
		t.Fatalf("Outcome = %q, want %q", res.Outcome, OutcomeSurvived)
	}
	if res.State != "D" {
		t.Errorf("State = %q, want D — the state IS the explanation", res.State)
	}
}

func TestDo_ZombieCountsAsGoneRatherThanSurviving(t *testing.T) {
	f := newFakeProc(t)
	c := New(f.root, Self{PID: 500, PGID: 500, SID: 500})
	f.add(t, 4242, 300, 4242, 300, "defunct", "Z", 111, "/usr/bin/defunct\x00")
	c.Sleep = func(time.Duration) {}
	c.Signal = func(pid int, sig syscall.Signal) error { return nil }

	res, err := c.Do(Request{PID: 4242, Start: 111, Mode: ModeQuit, Grace: DefaultGrace})
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if res.Outcome != OutcomeTerminated {
		t.Errorf("Outcome = %q, want %q — a zombie has already exited", res.Outcome, OutcomeTerminated)
	}
}

func TestDo_DeniesAProtectedProcessWithoutSignalling(t *testing.T) {
	f := newFakeProc(t)
	c := New(f.root, Self{PID: 500, PGID: 500, SID: 500})
	f.add(t, 1, 0, 1, 1, "init", "S", 1, "/sbin/init\x00")
	var sent int
	c.Signal = func(pid int, sig syscall.Signal) error { sent++; return nil }
	c.Sleep = func(time.Duration) {}

	_, err := c.Do(Request{PID: 1, Start: 1, Mode: ModeForce})
	d, ok := err.(*Denial)
	if !ok {
		t.Fatalf("err = %v (%T), want *Denial", err, err)
	}
	if d.Code != "init" {
		t.Errorf("code = %q, want init", d.Code)
	}
	if sent != 0 {
		t.Fatalf("sent %d signal(s) to pid 1", sent)
	}
}

func TestDo_ClampsAnAbsurdGracePeriod(t *testing.T) {
	f := newFakeProc(t)
	c := New(f.root, Self{PID: 500, PGID: 500, SID: 500})
	f.add(t, 4242, 300, 4242, 300, "wedged", "S", 111, "/usr/bin/wedged\x00")
	now := time.Unix(0, 0)
	c.Now = func() time.Time { return now }
	c.Sleep = func(d time.Duration) { now = now.Add(d) }
	c.Signal = func(pid int, sig syscall.Signal) error {
		if sig == syscall.SIGKILL {
			f.remove(t, 4242)
		}
		return nil
	}
	res, err := c.Do(Request{PID: 4242, Start: 111, Mode: ModeQuit, Grace: 24 * time.Hour})
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	// Bounded by MaxGrace, with the 2s post-SIGKILL confirmation window on top.
	if res.ElapsedMS > (MaxGrace + 3*time.Second).Milliseconds() {
		t.Errorf("elapsed %dms — a client-supplied grace of 24h was not clamped to %s",
			res.ElapsedMS, MaxGrace)
	}
}

func TestDo_MissingProcessIsNoSuchProcess(t *testing.T) {
	f := newFakeProc(t)
	c := New(f.root, Self{PID: 500, PGID: 500, SID: 500})
	if _, err := c.Do(Request{PID: 999, Start: 5, Mode: ModeForce}); err != ErrNoSuchProcess {
		t.Fatalf("err = %v, want ErrNoSuchProcess", err)
	}
}

func TestDenialFor_ReportsGoneForAVanishedPid(t *testing.T) {
	f := newFakeProc(t)
	d := DenialFor(f.root, 12345, Self{PID: 500, PGID: 500, SID: 500})
	if d == nil || d.Code != "gone" {
		t.Fatalf("DenialFor = %v, want code gone", d)
	}
}

func TestCurrentSelf_ReportsThisProcess(t *testing.T) {
	s := CurrentSelf()
	if s.PID != os.Getpid() {
		t.Errorf("Self.PID = %d, want %d", s.PID, os.Getpid())
	}
	// PGID must be a real group, never left at zero: Protect skips the
	// self_group rule when PGID is 0, so a zero here silently removes a rule.
	if s.PGID <= 0 {
		t.Errorf("Self.PGID = %d — Protect's self_group rule is inert at zero", s.PGID)
	}
}
