package wltoplevel

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// fakeExecutor — deterministic stand-in for external commands
// ---------------------------------------------------------------------------

// fakeExecutor records calls and returns canned responses keyed by command name.
type fakeExecutor struct {
	// outputs maps command name → stdout bytes to return.
	outputs map[string][]byte
	// errors maps command name → error to return from BOTH Output and Run.
	errors map[string]error
	// runErr, when set, fails only Run — letting a test exercise "the list
	// worked but the action failed", which sharing one map cannot express.
	runErr error
	// calls records every (name, args) pair that was invoked.
	calls [][]string
}

func newFake() *fakeExecutor {
	return &fakeExecutor{
		outputs: make(map[string][]byte),
		errors:  make(map[string]error),
	}
}

func (f *fakeExecutor) record(name string, args []string) {
	entry := append([]string{name}, args...)
	f.calls = append(f.calls, entry)
}

func (f *fakeExecutor) Output(_ context.Context, name string, args ...string) ([]byte, error) {
	f.record(name, args)
	if err, ok := f.errors[name]; ok {
		return nil, err
	}
	return f.outputs[name], nil
}

func (f *fakeExecutor) Run(_ context.Context, name string, args ...string) error {
	f.record(name, args)
	if f.runErr != nil {
		return f.runErr
	}
	if err, ok := f.errors[name]; ok {
		return err
	}
	return nil
}

// ---------------------------------------------------------------------------
// wlrctl output fixtures
// ---------------------------------------------------------------------------

// wlrctlOutput returns a `wlrctl toplevel list` fixture with two windows.
//
// The shape is copied from real output measured against a live labwc session
// on 2026-08-10 ("demo-app: Demo Window"), not invented: one line per
// toplevel, "app_id: title", no state flags and no trailing metadata.
func wlrctlOutput() []byte {
	return []byte(
		"org.mozilla.firefox: Firefox\n" +
			"org.wezfurlong.wezterm: Terminal\n",
	)
}

// ---------------------------------------------------------------------------
// parseWlrctlList unit tests
// ---------------------------------------------------------------------------

func TestParseWlrctlList_TwoWindows(t *testing.T) {
	windows := parseWlrctlList(wlrctlOutput())
	if len(windows) != 2 {
		t.Fatalf("expected 2 windows, got %d", len(windows))
	}

	// First window
	w := windows[0]
	if w.Handle != "0" {
		t.Errorf("window[0].Handle = %q, want %q", w.Handle, "0")
	}
	if w.Title != "Firefox" {
		t.Errorf("window[0].Title = %q, want %q", w.Title, "Firefox")
	}
	if w.AppID != "org.mozilla.firefox" {
		t.Errorf("window[0].AppID = %q, want %q", w.AppID, "org.mozilla.firefox")
	}
	// wlrctl reports no state flags at all — see the package doc.
	if w.State != "" {
		t.Errorf("window[0].State = %q, want empty (wlrctl reports no state)", w.State)
	}

	// Second window
	w = windows[1]
	if w.Handle != "1" {
		t.Errorf("window[1].Handle = %q, want %q", w.Handle, "1")
	}
	if w.AppID != "org.wezfurlong.wezterm" {
		t.Errorf("window[1].AppID = %q, want %q", w.AppID, "org.wezfurlong.wezterm")
	}
	if w.State != "" {
		t.Errorf("window[1].State = %q, want empty", w.State)
	}
}

func TestParseWlrctlList_Empty(t *testing.T) {
	windows := parseWlrctlList([]byte{})
	if len(windows) != 0 {
		t.Fatalf("expected 0 windows for empty input, got %d", len(windows))
	}
}

// A title containing ": " must not be split on its own colon — only the first
// separator divides app_id from title.
func TestParseWlrctlList_TitleContainsColon(t *testing.T) {
	raw := []byte("nvim: main.go: 3 unsaved changes\n")
	windows := parseWlrctlList(raw)
	if len(windows) != 1 {
		t.Fatalf("expected 1 window, got %d", len(windows))
	}
	if windows[0].AppID != "nvim" {
		t.Errorf("AppID = %q, want %q", windows[0].AppID, "nvim")
	}
	if windows[0].Title != "main.go: 3 unsaved changes" {
		t.Errorf("Title = %q, want %q", windows[0].Title, "main.go: 3 unsaved changes")
	}
}

// A line with no ": " separator keeps the window rather than dropping it.
func TestParseWlrctlList_NoSeparator(t *testing.T) {
	windows := parseWlrctlList([]byte("bare-app-id\n"))
	if len(windows) != 1 {
		t.Fatalf("expected 1 window, got %d", len(windows))
	}
	if windows[0].AppID != "bare-app-id" {
		t.Errorf("AppID = %q, want %q", windows[0].AppID, "bare-app-id")
	}
}

// State must be omitted from the wire entirely, not sent as "": a client that
// sees "state": "" can reasonably conclude no flags are set, which is a claim
// this backend cannot make.
func TestWindowJSON_OmitsUnknownState(t *testing.T) {
	b, err := json.Marshal(parseWlrctlList(wlrctlOutput())[0])
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(b), "state") {
		t.Errorf("JSON must omit the unknown state field, got: %s", b)
	}
}

// ---------------------------------------------------------------------------
// Service.List unit tests
// ---------------------------------------------------------------------------

func TestList_WithWlrctl(t *testing.T) {
	fake := newFake()
	fake.outputs["wlrctl"] = wlrctlOutput()

	svc := newWithExecutor(fake)
	windows, err := svc.List(context.Background())
	if err != nil {
		t.Fatalf("List returned error: %v", err)
	}
	if len(windows) != 2 {
		t.Fatalf("expected 2 windows, got %d", len(windows))
	}
}

// wlrctl absent => unavailable, NOT an empty window list.
func TestList_WlrctlMissing_ReturnsUnavailable(t *testing.T) {
	fake := newFake()
	fake.errors["wlrctl"] = &notFoundError{"wlrctl"}

	svc := newWithExecutor(fake)
	windows, err := svc.List(context.Background())
	if !isUnavailable(err) {
		t.Fatalf("expected errUnavailable, got: %v", err)
	}
	if windows != nil {
		t.Fatalf("expected nil windows alongside the error, got %d", len(windows))
	}
}

// Under cage, wlrctl exits 1 with "Foreign Toplevel Management interface not
// found!" — measured. That is unavailable, not "no windows open".
func TestList_UnsupportedCompositor_ReturnsUnavailable(t *testing.T) {
	fake := newFake()
	fake.errors["wlrctl"] = &exitError{code: 1}

	svc := newWithExecutor(fake)
	if _, err := svc.List(context.Background()); !isUnavailable(err) {
		t.Fatalf("expected errUnavailable, got: %v", err)
	}
}

// Exit 0 with no output IS a real answer: the compositor has no toplevels.
// This must stay distinguishable from the unavailable case above.
func TestList_NoWindows_ReturnsEmptyNoError(t *testing.T) {
	fake := newFake()
	fake.outputs["wlrctl"] = []byte("")

	svc := newWithExecutor(fake)
	windows, err := svc.List(context.Background())
	if err != nil {
		t.Fatalf("expected no error for an empty-but-successful list, got: %v", err)
	}
	if len(windows) != 0 {
		t.Fatalf("expected 0 windows, got %d", len(windows))
	}
}

// ---------------------------------------------------------------------------
// Service action tests (Focus / Minimize / Close)
// ---------------------------------------------------------------------------

func TestFocus_CallsWlrctl(t *testing.T) {
	fake := newFake()
	fake.outputs["wlrctl"] = wlrctlOutput()

	svc := newWithExecutor(fake)
	if err := svc.Focus(context.Background(), "0"); err != nil {
		t.Fatalf("Focus returned error: %v", err)
	}

	// Verify wlrctl was called with the right arguments.
	assertWlrctlCall(t, fake.calls, "window", "activate", "app_id:org.mozilla.firefox")
}

func TestMinimize_CallsWlrctl(t *testing.T) {
	fake := newFake()
	fake.outputs["wlrctl"] = wlrctlOutput()

	svc := newWithExecutor(fake)
	if err := svc.Minimize(context.Background(), "1"); err != nil {
		t.Fatalf("Minimize returned error: %v", err)
	}

	// `minimize`, not `set-minimized` — wlrctl rejects the latter (measured).
	assertWlrctlCall(t, fake.calls, "window", "minimize", "app_id:org.wezfurlong.wezterm")
}

func TestClose_CallsWlrctl(t *testing.T) {
	fake := newFake()
	fake.outputs["wlrctl"] = wlrctlOutput()

	svc := newWithExecutor(fake)
	if err := svc.Close(context.Background(), "0"); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}

	assertWlrctlCall(t, fake.calls, "window", "close", "app_id:org.mozilla.firefox")
}

func TestAction_UnknownHandle_ReturnsError(t *testing.T) {
	fake := newFake()
	fake.outputs["wlrctl"] = wlrctlOutput()

	svc := newWithExecutor(fake)
	err := svc.Focus(context.Background(), "99")
	if err == nil {
		t.Fatal("expected error for unknown handle, got nil")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected 'not found' in error, got: %v", err)
	}
}

// Both the list and the action shell out to wlrctl, so a missing binary must
// surface as unavailable from whichever call reaches it first.
func TestAction_WlrctlUnavailable_ReturnsUnavailable(t *testing.T) {
	fake := newFake()
	fake.errors["wlrctl"] = &notFoundError{"wlrctl"}

	svc := newWithExecutor(fake)
	err := svc.Focus(context.Background(), "0")
	if !isUnavailable(err) {
		t.Fatalf("expected errUnavailable, got: %v", err)
	}
}

// The action path must still reach wlrctl with the right verb when the list
// succeeds — i.e. the fake's Run half, not just Output.
func TestAction_RunFails_PropagatesExitCode(t *testing.T) {
	fake := newFake()
	fake.outputs["wlrctl"] = wlrctlOutput()
	fake.runErr = &exitError{code: 1}

	svc := newWithExecutor(fake)
	err := svc.Focus(context.Background(), "0")
	if err == nil {
		t.Fatal("expected an error when wlrctl exits non-zero")
	}
	if isUnavailable(err) {
		t.Fatalf("a non-zero exit is a failed action, not an unavailable service: %v", err)
	}
}

// ---------------------------------------------------------------------------
// HTTP handler tests
// ---------------------------------------------------------------------------

func TestHTTP_GetWindows_JSON(t *testing.T) {
	fake := newFake()
	fake.outputs["wlrctl"] = wlrctlOutput()

	svc := newWithExecutor(fake)
	mux := http.NewServeMux()
	svc.RegisterHandlers(mux)

	req := httptest.NewRequest("GET", "/api/shell/windows", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/shell/windows: status %d, want 200", rec.Code)
	}

	var windows []Window
	if err := json.NewDecoder(rec.Body).Decode(&windows); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(windows) != 2 {
		t.Fatalf("expected 2 windows in response, got %d", len(windows))
	}
	if windows[0].Title != "Firefox" {
		t.Errorf("windows[0].Title = %q, want %q", windows[0].Title, "Firefox")
	}
}

// THE REGRESSION THIS FILE EXISTS TO PREVENT: outside a foreign-toplevel
// session the endpoint must answer 503, not 200 with []. It used to answer
// 200 [] on every shipped image, because lswt was never packaged.
func TestHTTP_GetWindows_UnavailableIs503NotEmptyArray(t *testing.T) {
	fake := newFake()
	fake.errors["wlrctl"] = &exitError{code: 1}

	svc := newWithExecutor(fake)
	mux := http.NewServeMux()
	svc.RegisterHandlers(mux)

	req := httptest.NewRequest("GET", "/api/shell/windows", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 when foreign-toplevel is unavailable, got %d", rec.Code)
	}
	if strings.TrimSpace(rec.Body.String()) == "[]" {
		t.Fatal("body must not be an empty window array — that is the lie being fixed")
	}
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body["error"] == "" {
		t.Errorf("expected an error field explaining the unavailability, got %v", body)
	}
}

// Exit 0 with no toplevels stays a 200 with an empty array.
func TestHTTP_GetWindows_NoWindowsIs200EmptyArray(t *testing.T) {
	fake := newFake()
	fake.outputs["wlrctl"] = []byte("")

	svc := newWithExecutor(fake)
	mux := http.NewServeMux()
	svc.RegisterHandlers(mux)

	req := httptest.NewRequest("GET", "/api/shell/windows", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for an empty-but-successful list, got %d", rec.Code)
	}
	var windows []Window
	if err := json.NewDecoder(rec.Body).Decode(&windows); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(windows) != 0 {
		t.Fatalf("expected empty array, got %d", len(windows))
	}
}

func TestHTTP_Focus_OK(t *testing.T) {
	fake := newFake()
	fake.outputs["wlrctl"] = wlrctlOutput()

	svc := newWithExecutor(fake)
	mux := http.NewServeMux()
	svc.RegisterHandlers(mux)

	body := strings.NewReader(`{"handle":"0"}`)
	req := httptest.NewRequest("POST", "/api/shell/windows/focus", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/shell/windows/focus: status %d, want 200; body: %s",
			rec.Code, rec.Body.String())
	}
}

func TestHTTP_Focus_MissingHandle_BadRequest(t *testing.T) {
	fake := newFake()
	svc := newWithExecutor(fake)
	mux := http.NewServeMux()
	svc.RegisterHandlers(mux)

	body := strings.NewReader(`{}`)
	req := httptest.NewRequest("POST", "/api/shell/windows/focus", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing handle, got %d", rec.Code)
	}
}

func TestHTTP_Focus_WlrctlUnavailable_503(t *testing.T) {
	fake := newFake()
	fake.errors["wlrctl"] = &notFoundError{"wlrctl"}

	svc := newWithExecutor(fake)
	mux := http.NewServeMux()
	svc.RegisterHandlers(mux)

	body := strings.NewReader(`{"handle":"0"}`)
	req := httptest.NewRequest("POST", "/api/shell/windows/focus", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 when wlrctl unavailable, got %d", rec.Code)
	}
}

func TestHTTP_Minimize_OK(t *testing.T) {
	fake := newFake()
	fake.outputs["wlrctl"] = wlrctlOutput()

	svc := newWithExecutor(fake)
	mux := http.NewServeMux()
	svc.RegisterHandlers(mux)

	body := strings.NewReader(`{"handle":"1"}`)
	req := httptest.NewRequest("POST", "/api/shell/windows/minimize", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/shell/windows/minimize: status %d; body: %s",
			rec.Code, rec.Body.String())
	}
}

func TestHTTP_Close_OK(t *testing.T) {
	fake := newFake()
	fake.outputs["wlrctl"] = wlrctlOutput()

	svc := newWithExecutor(fake)
	mux := http.NewServeMux()
	svc.RegisterHandlers(mux)

	body := strings.NewReader(`{"handle":"0"}`)
	req := httptest.NewRequest("POST", "/api/shell/windows/close", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/shell/windows/close: status %d; body: %s",
			rec.Code, rec.Body.String())
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// assertWlrctlCall checks that at least one recorded call matches the given arguments.
func assertWlrctlCall(t *testing.T, calls [][]string, args ...string) {
	t.Helper()
	want := append([]string{"wlrctl"}, args...)
	for _, call := range calls {
		if len(call) != len(want) {
			continue
		}
		match := true
		for i := range want {
			if call[i] != want[i] {
				match = false
				break
			}
		}
		if match {
			return
		}
	}
	t.Errorf("no wlrctl call matching %v found in recorded calls %v", want, calls)
}

// isUnavailable reports whether err wraps errUnavailable.
func isUnavailable(err error) bool {
	return errors.Is(err, errUnavailable)
}

// ---------------------------------------------------------------------------
// Error stubs
// ---------------------------------------------------------------------------

// notFoundError mimics what exec returns for a tool that is not installed. It
// WRAPS the real exec.ErrNotFound: a stub that only looked like the real error
// in its message would be classified by the fallback branch instead of the
// not-found branch, and the test would then be agreeing with the wrong code
// path while looking green.
type notFoundError struct{ name string }

func (e *notFoundError) Error() string { return e.name + ": executable file not found in $PATH" }

func (e *notFoundError) Unwrap() error { return exec.ErrNotFound }

// exitError mimics *exec.ExitError for non-zero exits.
type exitError struct{ code int }

func (e *exitError) Error() string { return "exit status " + intToStr(e.code) }

// exitError must satisfy the errors.As target for *exec.ExitError in
// Service.runAction. We implement ExitCode() so we can check the interface.
func (e *exitError) ExitCode() int { return e.code }
