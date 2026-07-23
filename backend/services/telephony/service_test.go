package telephony

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ─── shared test doubles (unique names; parse_test.go owns its own) ─────────────

// teleFakeMmcli installs a fake `mmcli` on PATH that logs every argv element (one
// per invocation-line, delimited by "---") to a log file and answers the handful
// of subcommands the service issues. This lets us drive Send/allSMS end-to-end and
// INSPECT THE EXACT ARGUMENT the service handed the CLI — the only way to assert
// the SMS quote/escape injection guard without live hardware. Returns the log path.
//
// The double reports a modem at index 0, an empty SMS inbox, and echoes an SMS
// object path for a create — enough for the guard/dedup properties under test.
func teleFakeMmcli(t *testing.T) (logPath string) {
	t.Helper()
	dir := t.TempDir()
	logPath = filepath.Join(dir, "mmcli.log")
	script := "#!/bin/sh\n" +
		"for a in \"$@\"; do printf '%s\\n' \"$a\" >> \"$MMCLI_LOG\"; done\n" +
		"printf -- '---\\n' >> \"$MMCLI_LOG\"\n" +
		"for a in \"$@\"; do\n" +
		"  case \"$a\" in\n" +
		"    -L) echo \"/org/freedesktop/ModemManager1/Modem/0 [Test] Modem\"; exit 0 ;;\n" +
		"    --messaging-list-sms) exit 0 ;;\n" +
		"    --messaging-create-sms=*) echo \"/org/freedesktop/ModemManager1/SMS/5\"; exit 0 ;;\n" +
		"    --send) exit 0 ;;\n" +
		"  esac\n" +
		"done\n" +
		"exit 0\n"
	if err := os.WriteFile(filepath.Join(dir, "mmcli"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake mmcli: %v", err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("MMCLI_LOG", logPath)
	return logPath
}

// teleNoMmcli points PATH at an empty dir so `mmcli` is NOT found — the state a box
// with no modem/ModemManager is in. Deterministic across dev machines that happen
// to have a real mmcli installed.
func teleNoMmcli(t *testing.T) {
	t.Helper()
	t.Setenv("PATH", t.TempDir())
}

// teleLogLines returns the fake-mmcli log split into non-empty lines.
func teleLogLines(t *testing.T, logPath string) []string {
	t.Helper()
	b, err := os.ReadFile(logPath)
	if err != nil {
		return nil // no invocation recorded yet
	}
	var out []string
	for _, l := range strings.Split(string(b), "\n") {
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

// teleFindLine returns the first log line with the given prefix (and whether found).
func teleFindLine(lines []string, prefix string) (string, bool) {
	for _, l := range lines {
		if strings.HasPrefix(l, prefix) {
			return l, true
		}
	}
	return "", false
}

// newTeleService builds a Service whose owner resolves to ownerID (may be "").
func newTeleService(ownerID string) *Service {
	return New(nil, func() string { return ownerID })
}

// ─── owner resolution (fail-closed) ─────────────────────────────────────────────

func TestIsOwner_Resolution(t *testing.T) {
	current := "u1"
	s := New(nil, func() string { return current })

	if !s.isOwner("u1") {
		t.Error("the resolved owner must be recognized")
	}
	if s.isOwner("u2") {
		t.Error("a different account must NOT be treated as owner")
	}
	if s.isOwner("") {
		t.Error("an empty X-User-ID must never be the owner")
	}

	// Owner is resolved dynamically per call (it can change / not exist yet). When
	// unresolved, telephony is denied to EVERYONE — including whoever would later
	// become owner. This is the fail-closed property the whole surface relies on.
	current = ""
	if s.isOwner("u1") {
		t.Error("unresolved owner ('') must deny even the former owner (fail-closed)")
	}
	if s.isOwner("") {
		t.Error("unresolved owner ('') must deny an empty id (fail-closed)")
	}
}

func TestNew_NilOwner_DeniesEveryone(t *testing.T) {
	// nil ownerID is normalized to "no owner" — never a wildcard.
	s := New(nil, nil)
	if s.isOwner("anyone") || s.isOwner("") {
		t.Error("New(_, nil) must resolve to no-owner and deny all callers")
	}
}

// ─── transient-failure vs empty-inbox: the seenSMS non-wipe invariant ───────────

// The inbound-SMS poll loop MUST NOT prune its seen-set on a transient mmcli error,
// or every already-delivered message would look "new" next tick and re-fire a
// notification (an OTP/bank-alert re-spam). The mechanism guarding that is allSMS's
// second return value: false == transient failure (skip prune), true == authoritative
// snapshot (even if empty). This test locks that these two are DISTINGUISHABLE.
func TestAllSMS_TransientFailureIsDistinctFromEmptyInbox(t *testing.T) {
	t.Run("transient failure (no modem) => ok=false", func(t *testing.T) {
		teleNoMmcli(t)
		s := newTeleService("u1")
		msgs, ok := s.allSMS()
		if ok {
			t.Error("a failed list must report ok=false so the poll loop keeps seenSMS")
		}
		if msgs != nil {
			t.Errorf("expected nil messages on failure, got %v", msgs)
		}
	})

	t.Run("genuinely empty inbox => ok=true, len 0", func(t *testing.T) {
		teleFakeMmcli(t)
		s := newTeleService("u1")
		msgs, ok := s.allSMS()
		if !ok {
			t.Error("an empty-but-successful list must report ok=true (not conflated with failure)")
		}
		if len(msgs) != 0 {
			t.Errorf("expected 0 messages, got %d", len(msgs))
		}
	})
}

// ─── SMS body quote/escape injection guard ──────────────────────────────────────

// mmcli's --messaging-create-sms takes a comma-joined last-value-wins dictionary
// (number=…,text=…). An attacker-influenced body containing a comma + "number="
// (or a stray quote) must stay INSIDE the quoted text value and must not add a
// second top-level key that could redirect the SMS. We drive Send through the fake
// mmcli and assert the EXACT argument it constructed.
func TestSend_BodyInjection_StaysQuoted(t *testing.T) {
	logPath := teleFakeMmcli(t)
	s := newTeleService("u1")

	// Body tries to open a second dictionary key.
	if err := s.Send("+15551234567", "hi there, number=+19998887777"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	lines := teleLogLines(t, logPath)
	arg, ok := teleFindLine(lines, "--messaging-create-sms=")
	if !ok {
		t.Fatalf("no create-sms invocation recorded; log=%v", lines)
	}

	want := `--messaging-create-sms=number=+15551234567,text="hi there, number=+19998887777"`
	if arg != want {
		t.Fatalf("injection breakout possible.\n got: %q\nwant: %q", arg, want)
	}
	// Structural guard: the recipient is immediately followed by a QUOTED text value,
	// so the body's own "number=" cannot become a top-level key.
	payload := strings.TrimPrefix(arg, "--messaging-create-sms=")
	if !strings.HasPrefix(payload, `number=+15551234567,text="`) || !strings.HasSuffix(payload, `"`) {
		t.Errorf("body escaped the quoted value: %q", payload)
	}
}

func TestSend_BodyQuotesAreEscaped(t *testing.T) {
	logPath := teleFakeMmcli(t)
	s := newTeleService("u1")

	// A double-quote in the body must be backslash-escaped so it can't close the
	// text value early and let trailing chars become new dictionary keys.
	if err := s.Send("+15551234567", `pay "now" or,else`); err != nil {
		t.Fatalf("Send: %v", err)
	}
	lines := teleLogLines(t, logPath)
	arg, ok := teleFindLine(lines, "--messaging-create-sms=")
	if !ok {
		t.Fatalf("no create-sms invocation recorded; log=%v", lines)
	}
	want := `--messaging-create-sms=number=+15551234567,text="pay \"now\" or,else"`
	if arg != want {
		t.Fatalf("quote not escaped inside value.\n got: %q\nwant: %q", arg, want)
	}
}

// The recipient number itself is interpolated UNQUOTED, so it is validated hard: a
// comma (or any non-phone char) that could inject number=/text= keys must be
// rejected BEFORE any CLI call.
func TestSend_MaliciousRecipientRejected(t *testing.T) {
	logPath := teleFakeMmcli(t)
	s := newTeleService("u1")

	err := s.Send("+1,text=redirected", "hello")
	if err == nil {
		t.Fatal("a recipient with an embedded ,text= must be rejected")
	}
	if !strings.Contains(err.Error(), "invalid recipient number") {
		t.Errorf("unexpected error: %v", err)
	}
	if _, ok := teleFindLine(teleLogLines(t, logPath), "--messaging-create-sms="); ok {
		t.Error("no SMS must be created for an invalid recipient")
	}
}

func TestValidNumber_InjectionCharsRejected(t *testing.T) {
	good := []string{"+15551234567", "5551234567", "+1 (555) 123-4567", "+27831234567"}
	for _, n := range good {
		if !validNumber(n) {
			t.Errorf("legitimate number rejected: %q", n)
		}
	}
	bad := []string{
		"",
		"+1,text=evil",   // comma → dictionary-key injection
		`+1",text="evil`, // quote breakout
		"+1;rm -rf",      // shell-ish junk
		"number=1",       // key injection
		"+1\ntext=evil",  // newline
		"abc",            // letters
	}
	for _, n := range bad {
		if validNumber(n) {
			t.Errorf("malicious/invalid number accepted: %q", n)
		}
	}
}
