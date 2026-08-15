package telephony

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// ─── a fake mmcli whose CALL answers the test can change between ticks ────────
//
// The inbound-call log is a state machine over what `--voice-list-calls` reports
// from one poll to the next (a path that was there and is now gone means the
// call ended). A static double cannot exercise that, so this one reads its
// answers from files the test rewrites between calls to pollCalls().
//
// Files:
//   $MMCLI_CALLS   — stdout for `--voice-list-calls` (empty file ⇒ no calls)
//   $MMCLI_PROPS   — stdout for `-o <path> -K`
//   $MMCLI_FAIL    — if it exists, `--voice-list-calls` exits 1 (transient error)
type callFake struct {
	calls, props, fail string
}

func newCallFake(t *testing.T) *callFake {
	t.Helper()
	dir := t.TempDir()
	f := &callFake{
		calls: filepath.Join(dir, "calls"),
		props: filepath.Join(dir, "props"),
		fail:  filepath.Join(dir, "fail"),
	}
	// t.Setenv("PATH", dir) below narrows PATH to the fake's own directory (that
	// is how mmcliPresent() is made deterministic), which also takes /bin off the
	// script's PATH — so `cat` is NOT FOUND and every branch below quietly emits
	// nothing while still exiting 0. That reads exactly like a modem with no
	// calls, which is the shape of double that makes a test pass for no reason.
	// Restore a real PATH inside the script; the Go-side gate is unaffected.
	script := "#!/bin/sh\n" +
		"PATH=/bin:/usr/bin\n" +
		"for a in \"$@\"; do\n" +
		"  case \"$a\" in\n" +
		"    -L) echo \"/org/freedesktop/ModemManager1/Modem/0 [Test] Modem\"; exit 0 ;;\n" +
		"    --voice-list-calls) [ -f \"$MMCLI_FAIL\" ] && exit 1; cat \"$MMCLI_CALLS\"; exit 0 ;;\n" +
		"    --voice-status) echo \"modem.voice.emergency-only: no\"; exit 0 ;;\n" +
		"    -K) cat \"$MMCLI_PROPS\"; exit 0 ;;\n" +
		"    --voice-create-call=*) echo \"/org/freedesktop/ModemManager1/Call/9\"; exit 0 ;;\n" +
		"  esac\n" +
		"done\n" +
		"exit 0\n"
	if err := os.WriteFile(filepath.Join(dir, "mmcli"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake mmcli: %v", err)
	}
	f.write(t, f.calls, "")
	f.write(t, f.props, "")
	t.Setenv("PATH", dir)
	t.Setenv("MMCLI_CALLS", f.calls)
	t.Setenv("MMCLI_PROPS", f.props)
	t.Setenv("MMCLI_FAIL", f.fail)
	return f
}

func (f *callFake) write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// setCall makes one call object visible with the given properties.
func (f *callFake) setCall(t *testing.T, path, number, direction, state string) {
	t.Helper()
	f.write(t, f.calls, "  "+path+"\n")
	f.write(t, f.props, "call.properties.number : "+number+"\n"+
		"call.properties.direction : "+direction+"\n"+
		"call.properties.state : "+state+"\n")
}

func (f *callFake) clearCalls(t *testing.T) { t.Helper(); f.write(t, f.calls, "") }

func (f *callFake) breakList(t *testing.T) {
	t.Helper()
	f.write(t, f.fail, "x")
}

// recNotifier captures sovereign notifications.
type recNotifier struct {
	mu   sync.Mutex
	sent []string
}

func (n *recNotifier) Send(title, body, source string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.sent = append(n.sent, title+"|"+body+"|"+source)
}

func (n *recNotifier) all() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]string(nil), n.sent...)
}

func ownerSvc(n Notifier) *Service { return New(n, func() string { return "owner" }) }

// ─── the properties under test ────────────────────────────────────────────────

// An INBOUND call that rings and then goes away UNANSWERED is a missed call, and
// the box must both log it and tell the user. Before this existed, CallLog()
// returned a hardcoded empty slice, so a real modem's Recents list was empty
// forever and a missed call left no trace anywhere on the box.
func TestPollCallsLogsMissedInboundCall(t *testing.T) {
	f := newCallFake(t)
	n := &recNotifier{}
	s := ownerSvc(n)

	f.setCall(t, "/org/freedesktop/ModemManager1/Call/3", "+27831112222", "incoming", "ringing")
	s.pollCalls() // observes the ringing call
	if got := s.CallLog(); len(got) != 0 {
		t.Fatalf("a still-ringing call must not be in the log yet, got %+v", got)
	}

	f.clearCalls(t)
	s.pollCalls() // the object vanished ⇒ the call ended

	log := s.CallLog()
	if len(log) != 1 {
		t.Fatalf("want 1 logged call, got %d (%+v)", len(log), log)
	}
	if log[0].Direction != "missed" {
		t.Errorf("unanswered inbound call must log as missed, got %q", log[0].Direction)
	}
	if log[0].Number != "+27831112222" {
		t.Errorf("number = %q, want +27831112222", log[0].Number)
	}
	notes := n.all()
	if len(notes) != 1 || !strings.HasPrefix(notes[0], "Missed call|") {
		t.Errorf("want one 'Missed call' notification, got %v", notes)
	}
}

// The SAME sequence, but the call reaches `active` before it ends, is a received
// call — not a missed one. This is the branch that distinguishes the two, and it
// is the reason the poll tracks state rather than just presence.
func TestPollCallsLogsAnsweredInboundCall(t *testing.T) {
	f := newCallFake(t)
	n := &recNotifier{}
	s := ownerSvc(n)

	path := "/org/freedesktop/ModemManager1/Call/4"
	f.setCall(t, path, "+27835554444", "incoming", "ringing")
	s.pollCalls()
	f.setCall(t, path, "+27835554444", "incoming", "active")
	s.pollCalls()
	f.clearCalls(t)
	s.pollCalls()

	log := s.CallLog()
	if len(log) != 1 {
		t.Fatalf("want 1 logged call, got %d (%+v)", len(log), log)
	}
	if log[0].Direction != "incoming" {
		t.Errorf("an ANSWERED inbound call must log as incoming, got %q", log[0].Direction)
	}
	if notes := n.all(); len(notes) != 0 {
		t.Errorf("an answered call must not raise a missed-call notification, got %v", notes)
	}
}

// A transient mmcli failure must NOT be read as "every call ended". Without the
// guard, one hiccup mid-call flushes the in-flight call into the log as missed
// while the user is still talking — the same class of bug the SMS poll's
// seen-set guard already documents.
func TestPollCallsIgnoresTransientListFailure(t *testing.T) {
	f := newCallFake(t)
	s := ownerSvc(nil)

	f.setCall(t, "/org/freedesktop/ModemManager1/Call/5", "+27830001111", "incoming", "active")
	s.pollCalls()

	f.breakList(t)
	s.pollCalls()

	if got := s.CallLog(); len(got) != 0 {
		t.Fatalf("a failed --voice-list-calls must not end the in-flight call, got %+v", got)
	}
}

// PlaceCall logs the outgoing call itself, and the poll must not log it a SECOND
// time when it later observes that same call object ending. Two independent
// writers to one log is exactly how duplicate rows appear.
//
// The single guard that makes this true is the direction check in pollCalls'
// "ended" branch. An earlier version ALSO pre-marked the dialled call's object
// path as seen; mutation testing showed that second guard was dead — removing it
// changed nothing, because the direction check had already covered the case.
func TestPlaceCallLogsOnceNotTwice(t *testing.T) {
	f := newCallFake(t)
	s := ownerSvc(nil)

	if err := s.PlaceCall("+27829998888"); err != nil {
		t.Fatalf("PlaceCall: %v", err)
	}
	log := s.CallLog()
	if len(log) != 1 || log[0].Direction != "outgoing" {
		t.Fatalf("want 1 outgoing entry immediately after dialling, got %+v", log)
	}

	// The modem now shows that call, then drops it when it ends.
	f.setCall(t, "/org/freedesktop/ModemManager1/Call/9", "+27829998888", "outgoing", "active")
	s.pollCalls()
	f.clearCalls(t)
	s.pollCalls()

	if got := s.CallLog(); len(got) != 1 {
		t.Fatalf("the placed call must appear exactly once, got %d entries: %+v", len(got), got)
	}
}

// The log is bounded, and reads come back newest-first — Recents shows the most
// recent call at the top, and a chatty line cannot grow the process forever.
func TestCallLogIsNewestFirstAndBounded(t *testing.T) {
	s := ownerSvc(nil)
	for i := 0; i < maxCallLog+25; i++ {
		s.recordCall(Call{Number: "+100", Direction: "outgoing", TS: int64(i)})
	}
	log := s.CallLog()
	if len(log) != maxCallLog {
		t.Fatalf("log length = %d, want it bounded at %d", len(log), maxCallLog)
	}
	if log[0].TS <= log[1].TS {
		t.Errorf("CallLog must be newest-first, got TS %d then %d", log[0].TS, log[1].TS)
	}
	if log[0].TS != int64(maxCallLog+24) {
		t.Errorf("newest entry TS = %d, want %d", log[0].TS, maxCallLog+24)
	}
}

// Status must carry the modem's VOICE capability. A data/SMS-only USB stick is a
// completely normal thing to plug into a box, and without this field the phone
// app cannot tell one from a voice-capable modem — so it offers a dial pad whose
// every press fails.
func TestStatusReportsVoiceCapability(t *testing.T) {
	newCallFake(t)
	s := ownerSvc(nil)
	st := s.Status()
	if !st.Available {
		t.Fatalf("fake modem should be available, got %+v", st)
	}
	if !st.Voice {
		t.Error("Status.Voice must be true when the modem reports voice support")
	}
}

// With no mmcli at all, every read is a clean empty — and specifically a JSON
// LIST, never null. A client that narrows "is this an array?" turns a null into
// its unrecognised-shape branch, which is how a dead service renders as a
// designed empty state.
func TestNoModemReadsAreEmptyListsNotNull(t *testing.T) {
	teleNoMmcli(t)
	s := ownerSvc(nil)

	if st := s.Status(); st.Available || st.Voice {
		t.Errorf("no modem must report unavailable and no voice, got %+v", st)
	}
	if got := s.CallLog(); got == nil {
		t.Error("CallLog() must be an empty slice, not nil (nil marshals to null)")
	}
	if got := s.ThreadFor("+27830000000"); got == nil {
		t.Error("ThreadFor() must be an empty slice, not nil (nil marshals to null)")
	}
}
