package wltoplevel

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
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
	// errors maps command name → error to return.
	errors map[string]error
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
	if err, ok := f.errors[name]; ok {
		return err
	}
	return nil
}

// ---------------------------------------------------------------------------
// lswt output fixtures
// ---------------------------------------------------------------------------

// lswtOutput returns a valid lswt -t fixture with two windows.
func lswtOutput() []byte {
	return []byte(
		"Firefox\torg.mozilla.firefox\tactivated\n" +
			"Terminal\torg.wezfurlong.wezterm\t\n",
	)
}

// ---------------------------------------------------------------------------
// parseLswt unit tests
// ---------------------------------------------------------------------------

func TestParseLswt_TwoWindows(t *testing.T) {
	windows := parseLswt(lswtOutput())
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
	if w.State != "activated" {
		t.Errorf("window[0].State = %q, want %q", w.State, "activated")
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

func TestParseLswt_Empty(t *testing.T) {
	windows := parseLswt([]byte{})
	if len(windows) != 0 {
		t.Fatalf("expected 0 windows for empty input, got %d", len(windows))
	}
}

func TestParseLswt_MultipleStateFlags(t *testing.T) {
	raw := []byte("GIMP\torg.gimp.GIMP\tactivated maximized\n")
	windows := parseLswt(raw)
	if len(windows) != 1 {
		t.Fatalf("expected 1 window, got %d", len(windows))
	}
	if windows[0].State != "activated,maximized" {
		t.Errorf("State = %q, want %q", windows[0].State, "activated,maximized")
	}
}

// ---------------------------------------------------------------------------
// Service.List unit tests
// ---------------------------------------------------------------------------

func TestList_WithLswt(t *testing.T) {
	fake := newFake()
	fake.outputs["lswt"] = lswtOutput()

	svc := newWithExecutor(fake)
	windows, err := svc.List(context.Background())
	if err != nil {
		t.Fatalf("List returned error: %v", err)
	}
	if len(windows) != 2 {
		t.Fatalf("expected 2 windows, got %d", len(windows))
	}
}

func TestList_LswtUnavailable_ReturnsEmptyNoError(t *testing.T) {
	fake := newFake()
	// Simulate lswt not being installed (exec.ErrNotFound in practice).
	fake.errors["lswt"] = &notFoundError{"lswt"}

	svc := newWithExecutor(fake)
	windows, err := svc.List(context.Background())
	if err != nil {
		t.Fatalf("expected no error when lswt is unavailable, got: %v", err)
	}
	if len(windows) != 0 {
		t.Fatalf("expected empty slice, got %d windows", len(windows))
	}
}

func TestList_OutsideWayland_ReturnsEmptyNoError(t *testing.T) {
	// Simulate running outside a Wayland session — lswt exits non-zero.
	fake := newFake()
	fake.errors["lswt"] = &exitError{code: 1}

	svc := newWithExecutor(fake)
	windows, err := svc.List(context.Background())
	if err != nil {
		t.Fatalf("expected no error outside Wayland, got: %v", err)
	}
	if len(windows) != 0 {
		t.Fatalf("expected empty slice, got %d windows", len(windows))
	}
}

// ---------------------------------------------------------------------------
// Service action tests (Focus / Minimize / Close)
// ---------------------------------------------------------------------------

func TestFocus_CallsWlrctl(t *testing.T) {
	fake := newFake()
	fake.outputs["lswt"] = lswtOutput()

	svc := newWithExecutor(fake)
	if err := svc.Focus(context.Background(), "0"); err != nil {
		t.Fatalf("Focus returned error: %v", err)
	}

	// Verify wlrctl was called with the right arguments.
	assertWlrctlCall(t, fake.calls, "window", "activate", "app_id:org.mozilla.firefox")
}

func TestMinimize_CallsWlrctl(t *testing.T) {
	fake := newFake()
	fake.outputs["lswt"] = lswtOutput()

	svc := newWithExecutor(fake)
	if err := svc.Minimize(context.Background(), "1"); err != nil {
		t.Fatalf("Minimize returned error: %v", err)
	}

	assertWlrctlCall(t, fake.calls, "window", "set-minimized", "app_id:org.wezfurlong.wezterm")
}

func TestClose_CallsWlrctl(t *testing.T) {
	fake := newFake()
	fake.outputs["lswt"] = lswtOutput()

	svc := newWithExecutor(fake)
	if err := svc.Close(context.Background(), "0"); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}

	assertWlrctlCall(t, fake.calls, "window", "close", "app_id:org.mozilla.firefox")
}

func TestAction_UnknownHandle_ReturnsError(t *testing.T) {
	fake := newFake()
	fake.outputs["lswt"] = lswtOutput()

	svc := newWithExecutor(fake)
	err := svc.Focus(context.Background(), "99")
	if err == nil {
		t.Fatal("expected error for unknown handle, got nil")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected 'not found' in error, got: %v", err)
	}
}

func TestAction_WlrctlUnavailable_ReturnsUnavailable(t *testing.T) {
	fake := newFake()
	fake.outputs["lswt"] = lswtOutput()
	fake.errors["wlrctl"] = &notFoundError{"wlrctl"}

	svc := newWithExecutor(fake)
	err := svc.Focus(context.Background(), "0")
	if !isUnavailable(err) {
		t.Fatalf("expected errUnavailable, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// HTTP handler tests
// ---------------------------------------------------------------------------

func TestHTTP_GetWindows_JSON(t *testing.T) {
	fake := newFake()
	fake.outputs["lswt"] = lswtOutput()

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

func TestHTTP_GetWindows_EmptyWhenNoWayland(t *testing.T) {
	fake := newFake()
	fake.errors["lswt"] = &exitError{code: 1}

	svc := newWithExecutor(fake)
	mux := http.NewServeMux()
	svc.RegisterHandlers(mux)

	req := httptest.NewRequest("GET", "/api/shell/windows", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 even outside Wayland, got %d", rec.Code)
	}

	var windows []Window
	if err := json.NewDecoder(rec.Body).Decode(&windows); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(windows) != 0 {
		t.Fatalf("expected empty array, got %d windows", len(windows))
	}
}

func TestHTTP_Focus_OK(t *testing.T) {
	fake := newFake()
	fake.outputs["lswt"] = lswtOutput()

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
	fake.outputs["lswt"] = lswtOutput()
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
	fake.outputs["lswt"] = lswtOutput()

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
	fake.outputs["lswt"] = lswtOutput()

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
// Minimal error stubs (avoid importing os/exec in the test)
// ---------------------------------------------------------------------------

// notFoundError mimics exec.ErrNotFound for tools that are not installed.
type notFoundError struct{ name string }

func (e *notFoundError) Error() string { return e.name + ": executable file not found in $PATH" }

// exitError mimics *exec.ExitError for non-zero exits.
type exitError struct{ code int }

func (e *exitError) Error() string { return "exit status " + intToStr(e.code) }

// exitError must satisfy the errors.As target for *exec.ExitError in
// Service.runAction. We implement ExitCode() so we can check the interface.
func (e *exitError) ExitCode() int { return e.code }
