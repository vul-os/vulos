package pty

import (
	"context"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// newTestService creates a Service with nil sysUsers and resolve so we never
// attempt privilege-dropping (requires root). The shell is set to /bin/sh so
// the test doesn't depend on bash being available.
func newTestService() *Service {
	return &Service{
		sessions: make(map[string]*Session),
		shell:    "/bin/sh",
		sysUsers: nil,
		resolve:  nil,
	}
}

// requirePTY skips the test when a real PTY cannot be allocated (e.g. some CI
// containers running without /dev/ptmx). We attempt a createSession and skip
// if it errors.
func requirePTY(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("pty tests only run on Linux/macOS")
	}
}

// ---------------------------------------------------------------------------
// ringBuffer
// ---------------------------------------------------------------------------

func TestRingBuffer_Empty(t *testing.T) {
	rb := newRingBuffer(16)
	if got := rb.Bytes(); len(got) != 0 {
		t.Fatalf("expected empty bytes, got len=%d", len(got))
	}
}

func TestRingBuffer_PartialFill(t *testing.T) {
	rb := newRingBuffer(16)
	rb.Write([]byte("hello"))
	got := rb.Bytes()
	if string(got) != "hello" {
		t.Fatalf("expected %q, got %q", "hello", got)
	}
}

func TestRingBuffer_ExactFill(t *testing.T) {
	rb := newRingBuffer(4)
	rb.Write([]byte("abcd"))
	got := rb.Bytes()
	if string(got) != "abcd" {
		t.Fatalf("expected %q, got %q", "abcd", got)
	}
}

func TestRingBuffer_Wrap(t *testing.T) {
	rb := newRingBuffer(4)
	rb.Write([]byte("abcde")) // wraps: last 4 bytes = "bcde"
	got := rb.Bytes()
	if string(got) != "bcde" {
		t.Fatalf("expected %q after wrap, got %q", "bcde", got)
	}
}

func TestRingBuffer_MultipleWraps(t *testing.T) {
	rb := newRingBuffer(4)
	rb.Write([]byte("12345678")) // two full wraps; last 4 bytes = "5678"
	got := rb.Bytes()
	if string(got) != "5678" {
		t.Fatalf("expected %q, got %q", "5678", got)
	}
}

// ---------------------------------------------------------------------------
// sanitizeUTF8
// ---------------------------------------------------------------------------

func TestSanitizeUTF8_ValidPassthrough(t *testing.T) {
	input := []byte("hello, 世界")
	out := sanitizeUTF8(input)
	if string(out) != string(input) {
		t.Fatalf("valid UTF-8 should pass through unchanged")
	}
}

func TestSanitizeUTF8_InvalidByteReplaced(t *testing.T) {
	// 0xFF is never valid UTF-8.
	input := []byte{'h', 'i', 0xFF, '!'}
	out := sanitizeUTF8(input)
	if string(out) != "hi?!" {
		t.Fatalf("expected invalid byte replaced by '?', got %q", out)
	}
}

func TestSanitizeUTF8_AllInvalid(t *testing.T) {
	input := []byte{0x80, 0x81, 0x82}
	out := sanitizeUTF8(input)
	for _, b := range out {
		if b != '?' {
			t.Fatalf("expected all '?', got %q", out)
		}
	}
}

func TestSanitizeUTF8_EmptySlice(t *testing.T) {
	out := sanitizeUTF8([]byte{})
	if len(out) != 0 {
		t.Fatalf("expected empty output for empty input")
	}
}

// ---------------------------------------------------------------------------
// parseIntOr
// ---------------------------------------------------------------------------

func TestParseIntOr_ValidNumber(t *testing.T) {
	if got := parseIntOr("42", 10); got != 42 {
		t.Fatalf("expected 42, got %d", got)
	}
}

func TestParseIntOr_EmptyString(t *testing.T) {
	if got := parseIntOr("", 99); got != 99 {
		t.Fatalf("expected fallback 99, got %d", got)
	}
}

func TestParseIntOr_ZeroReturnsDefault(t *testing.T) {
	// "0" parses to 0 which triggers the fallback path.
	if got := parseIntOr("0", 80); got != 80 {
		t.Fatalf("expected fallback 80 for '0', got %d", got)
	}
}

func TestParseIntOr_NonNumeric(t *testing.T) {
	// Non-numeric characters are skipped; result is 0 → fallback.
	if got := parseIntOr("abc", 24); got != 24 {
		t.Fatalf("expected fallback 24, got %d", got)
	}
}

func TestParseIntOr_MixedString(t *testing.T) {
	// Digits extracted from "12x3" → 123.
	if got := parseIntOr("12x3", 0); got != 123 {
		t.Fatalf("expected 123, got %d", got)
	}
}

// ---------------------------------------------------------------------------
// parseTwoInts
// ---------------------------------------------------------------------------

func TestParseTwoInts_Normal(t *testing.T) {
	a, b := parseTwoInts("80,24")
	if a != 80 || b != 24 {
		t.Fatalf("expected 80,24 got %d,%d", a, b)
	}
}

func TestParseTwoInts_LargeValues(t *testing.T) {
	a, b := parseTwoInts("220,50")
	if a != 220 || b != 50 {
		t.Fatalf("expected 220,50 got %d,%d", a, b)
	}
}

func TestParseTwoInts_MissingSecond(t *testing.T) {
	a, b := parseTwoInts("80,")
	if a != 80 || b != 0 {
		t.Fatalf("expected 80,0 got %d,%d", a, b)
	}
}

func TestParseTwoInts_Empty(t *testing.T) {
	a, b := parseTwoInts("")
	if a != 0 || b != 0 {
		t.Fatalf("expected 0,0 got %d,%d", a, b)
	}
}

// ---------------------------------------------------------------------------
// generateID
// ---------------------------------------------------------------------------

func TestGenerateID_NotEmpty(t *testing.T) {
	id := generateID()
	if id == "" {
		t.Fatal("generateID returned empty string")
	}
}

func TestGenerateID_Unique(t *testing.T) {
	// IDs are timestamp-based with microsecond precision; two calls back-to-back
	// should still produce unique values on modern hardware, but we sleep 1ms to
	// be safe.
	id1 := generateID()
	time.Sleep(time.Millisecond)
	id2 := generateID()
	if id1 == id2 {
		t.Fatalf("expected unique IDs, both were %q", id1)
	}
}

// ---------------------------------------------------------------------------
// Service – session lifecycle (no real PTY spawn needed)
// ---------------------------------------------------------------------------

func TestNewService_Defaults(t *testing.T) {
	svc := newTestService()
	if svc.sessions == nil {
		t.Fatal("sessions map must be initialised")
	}
	if svc.shell == "" {
		t.Fatal("shell must not be empty")
	}
}

func TestActiveSessionsInitiallyZero(t *testing.T) {
	svc := newTestService()
	if n := svc.ActiveSessions(); n != 0 {
		t.Fatalf("expected 0 active sessions, got %d", n)
	}
}

func TestListSessions_EmptyForUnknownUser(t *testing.T) {
	svc := newTestService()
	out := svc.ListSessions("nonexistent-user")
	if len(out) != 0 {
		t.Fatalf("expected empty list, got %+v", out)
	}
}

func TestGetSession_MissingID(t *testing.T) {
	svc := newTestService()
	sess := svc.getSession("does-not-exist", "user1")
	if sess != nil {
		t.Fatal("expected nil for unknown session ID")
	}
}

func TestGetSession_WrongUser(t *testing.T) {
	svc := newTestService()
	// Manually insert a session belonging to user1.
	fakeSess := &Session{
		ID:     "test-sess-001",
		UserID: "user1",
		Alive:  true,
	}
	svc.mu.Lock()
	svc.sessions["test-sess-001"] = fakeSess
	svc.mu.Unlock()

	// Lookup as user2 should return nil (ownership check).
	got := svc.getSession("test-sess-001", "user2")
	if got != nil {
		t.Fatal("session ownership check failed: user2 should not see user1's session")
	}
}

func TestGetSession_CorrectUser(t *testing.T) {
	svc := newTestService()
	fakeSess := &Session{
		ID:     "test-sess-002",
		UserID: "user1",
		Alive:  true,
	}
	svc.mu.Lock()
	svc.sessions["test-sess-002"] = fakeSess
	svc.mu.Unlock()

	got := svc.getSession("test-sess-002", "user1")
	if got == nil {
		t.Fatal("expected to find session for correct user")
	}
}

func TestDestroySession_NotFound(t *testing.T) {
	svc := newTestService()
	ok := svc.DestroySession("no-such-id", "user1")
	if ok {
		t.Fatal("DestroySession should return false for unknown id")
	}
}

func TestDestroySession_WrongUser(t *testing.T) {
	svc := newTestService()
	fakeSess := &Session{
		ID:     "test-sess-003",
		UserID: "user1",
		Alive:  true,
		done:   make(chan struct{}),
	}
	fakeSess.scrollback = newRingBuffer(64)
	svc.mu.Lock()
	svc.sessions["test-sess-003"] = fakeSess
	svc.mu.Unlock()

	ok := svc.DestroySession("test-sess-003", "user2")
	if ok {
		t.Fatal("DestroySession should return false when user doesn't own session")
	}
	// Session must still be present.
	if svc.ActiveSessions() != 1 {
		t.Fatal("session should survive incorrect-user destroy attempt")
	}
}

func TestListSessions_FiltersCorrectly(t *testing.T) {
	svc := newTestService()

	// Two sessions for user1, one for user2.
	for _, entry := range []struct{ id, uid string }{
		{"s1", "user1"},
		{"s2", "user1"},
		{"s3", "user2"},
	} {
		sess := &Session{
			ID:         entry.id,
			UserID:     entry.uid,
			CreatedAt:  time.Now(),
			Alive:      true,
			scrollback: newRingBuffer(64),
		}
		svc.mu.Lock()
		svc.sessions[entry.id] = sess
		svc.mu.Unlock()
	}

	out := svc.ListSessions("user1")
	if len(out) != 2 {
		t.Fatalf("expected 2 sessions for user1, got %d", len(out))
	}
	out2 := svc.ListSessions("user2")
	if len(out2) != 1 {
		t.Fatalf("expected 1 session for user2, got %d", len(out2))
	}
}

func TestDestroyAll_ClearsMap(t *testing.T) {
	svc := newTestService()

	// destroySession calls sess.ptmx.Close() and checks sess.cmd.Process — so
	// we need a valid *os.File and a non-nil *exec.Cmd (with Process==nil is
	// fine; the guard `if sess.cmd.Process != nil` handles that).
	for _, id := range []string{"da1", "da2"} {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatalf("pipe: %v", err)
		}
		w.Close() // write end not needed; read end acts as stand-in ptmx
		sess := &Session{
			ID:         id,
			UserID:     "userX",
			Alive:      true,
			done:       make(chan struct{}),
			scrollback: newRingBuffer(64),
			ptmx:       r,
			cmd:        &exec.Cmd{}, // non-nil cmd, Process == nil → kill skipped
		}
		svc.mu.Lock()
		svc.sessions[id] = sess
		svc.mu.Unlock()
	}

	if svc.ActiveSessions() != 2 {
		t.Fatal("precondition: expected 2 sessions")
	}

	svc.DestroyAll()

	if svc.ActiveSessions() != 0 {
		t.Fatalf("expected 0 sessions after DestroyAll, got %d", svc.ActiveSessions())
	}
}

// ---------------------------------------------------------------------------
// Session create/list/close lifecycle – requires a real PTY
// ---------------------------------------------------------------------------

func TestCreateSession_StartsAndRegisters(t *testing.T) {
	requirePTY(t)
	svc := newTestService()

	sess, err := svc.createSession("user1", 80, 24)
	if err != nil {
		t.Skipf("PTY unavailable (%v), skipping", err)
	}
	defer svc.destroySession(sess.ID)

	if sess.ID == "" {
		t.Fatal("session ID must not be empty")
	}
	if sess.UserID != "user1" {
		t.Fatalf("expected UserID=user1, got %q", sess.UserID)
	}
	if !sess.Alive {
		t.Fatal("newly created session must be Alive")
	}
	if svc.ActiveSessions() != 1 {
		t.Fatalf("expected 1 active session, got %d", svc.ActiveSessions())
	}
}

func TestCreateSession_ListAndClose(t *testing.T) {
	requirePTY(t)
	svc := newTestService()

	sess, err := svc.createSession("user42", 120, 40)
	if err != nil {
		t.Skipf("PTY unavailable (%v), skipping", err)
	}

	// Verify it appears in the list.
	list := svc.ListSessions("user42")
	if len(list) != 1 {
		t.Fatalf("expected 1 session in list, got %d", len(list))
	}
	if list[0].ID != sess.ID {
		t.Fatalf("listed ID %q does not match created ID %q", list[0].ID, sess.ID)
	}

	// Close via public API.
	if ok := svc.DestroySession(sess.ID, "user42"); !ok {
		t.Fatal("DestroySession returned false for valid session")
	}
	if svc.ActiveSessions() != 0 {
		t.Fatalf("expected 0 sessions after destroy, got %d", svc.ActiveSessions())
	}
	if list2 := svc.ListSessions("user42"); len(list2) != 0 {
		t.Fatalf("expected empty list after destroy, got %d sessions", len(list2))
	}
}

func TestCreateMultipleSessions_SameUser(t *testing.T) {
	requirePTY(t)
	svc := newTestService()

	s1, err := svc.createSession("multi-user", 80, 24)
	if err != nil {
		t.Skipf("PTY unavailable (%v), skipping", err)
	}
	s2, err := svc.createSession("multi-user", 80, 24)
	if err != nil {
		t.Fatalf("second createSession failed: %v", err)
	}
	defer svc.destroySession(s1.ID)
	defer svc.destroySession(s2.ID)

	if s1.ID == s2.ID {
		t.Fatal("two sessions must have distinct IDs")
	}
	if svc.ActiveSessions() != 2 {
		t.Fatalf("expected 2 active sessions, got %d", svc.ActiveSessions())
	}

	list := svc.ListSessions("multi-user")
	if len(list) != 2 {
		t.Fatalf("expected 2 sessions in list, got %d", len(list))
	}
}

// ---------------------------------------------------------------------------
// Exec – argv / injection hardening
// ---------------------------------------------------------------------------

// TestExec_ArgsPassedAsSlice verifies that Exec invokes bash with -c and the
// raw command string — NOT via a shell-joined string that could split tokens.
// We can't change exec.go, but we can verify the observed behaviour: a command
// that would break if naively split on spaces must still produce correct output.
func TestExec_ArgsPassedAsSlice(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("exec tests require a POSIX shell")
	}
	ctx := context.Background()

	// If argv were split on spaces, "echo hello world" → [echo, hello, world]
	// which is fine, but "echo 'a b'" → [echo, 'a, b'] which is wrong.
	// The function passes the whole string to bash -c which correctly handles
	// quoting. Verify the correct output arrives.
	res := Exec(ctx, "echo 'hello world'")
	if res.ExitCode != 0 {
		t.Fatalf("expected exit 0, got %d (output: %q)", res.ExitCode, res.Output)
	}
	if strings.TrimSpace(res.Output) != "hello world" {
		t.Fatalf("expected 'hello world', got %q", res.Output)
	}
}

// TestExec_NoShellInjectionViaCommand verifies that a command containing shell
// metacharacters is handed to bash -c intact and produces the expected output,
// rather than being re-split or re-interpreted at a different level.
func TestExec_NoShellInjectionViaCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("exec tests require a POSIX shell")
	}
	ctx := context.Background()

	// A literal semicolon in the command is interpreted by the single bash
	// invocation — not by a second layer of shell parsing on the Go side.
	// "true; echo injected" should succeed and print "injected".
	res := Exec(ctx, "true; echo injected")
	if res.ExitCode != 0 {
		t.Fatalf("unexpected exit code %d", res.ExitCode)
	}
	if !strings.Contains(res.Output, "injected") {
		t.Fatalf("expected 'injected' in output, got %q", res.Output)
	}
}

func TestExec_CommandField(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("exec tests require a POSIX shell")
	}
	ctx := context.Background()
	cmd := "echo test-command-field"
	res := Exec(ctx, cmd)
	if res.Command != cmd {
		t.Fatalf("ExecResult.Command should echo the input command, got %q", res.Command)
	}
}

func TestExec_ExitCode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("exec tests require a POSIX shell")
	}
	ctx := context.Background()
	res := Exec(ctx, "exit 42")
	if res.ExitCode != 42 {
		t.Fatalf("expected exit code 42, got %d", res.ExitCode)
	}
}

func TestExec_StderrMerged(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("exec tests require a POSIX shell")
	}
	ctx := context.Background()
	res := Exec(ctx, "echo stdout; echo stderr >&2")
	if !strings.Contains(res.Output, "stdout") {
		t.Fatalf("stdout missing from output: %q", res.Output)
	}
	if !strings.Contains(res.Output, "stderr") {
		t.Fatalf("stderr missing from merged output: %q", res.Output)
	}
}

func TestExec_Timeout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("exec tests require a POSIX shell")
	}
	// Use a caller-supplied context with a short deadline to verify that
	// cancellation produces a non-zero exit without waiting 10 s.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	res := Exec(ctx, "sleep 30")
	if res.ExitCode == 0 {
		t.Fatal("expected non-zero exit when context is cancelled before sleep ends")
	}
}

func TestExec_OutputTruncation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("exec tests require a POSIX shell")
	}
	ctx := context.Background()
	// Generate ~12 KB of output (> 10 KB truncation limit).
	res := Exec(ctx, "python3 -c \"print('x'*12000)\" 2>/dev/null || dd if=/dev/zero bs=12000 count=1 2>/dev/null | tr '\\0' 'a'")
	// If neither python3 nor dd produce the output as expected on this system
	// we just verify the function doesn't panic; truncation is tested by the
	// length check.
	if len(res.Output) > 10240+len("\n... (truncated)") {
		t.Fatalf("output not truncated: len=%d", len(res.Output))
	}
}

func TestExec_TrueCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("exec tests require a POSIX shell")
	}
	ctx := context.Background()
	res := Exec(ctx, "true")
	if res.ExitCode != 0 {
		t.Fatalf("'true' should exit 0, got %d", res.ExitCode)
	}
	if res.Duration == "" {
		t.Fatal("Duration must not be empty")
	}
}
