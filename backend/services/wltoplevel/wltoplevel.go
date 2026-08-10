// Package wltoplevel enumerates and controls native Wayland windows via the
// wlr-foreign-toplevel-management-v1 protocol.
//
// Both halves shell out to wlrctl(1): `wlrctl toplevel list` to snapshot the
// window list, `wlrctl window <verb> app_id:<id>` to act on one. It used to
// list via lswt(1) instead, which is not packaged in Debian at all — so on
// every shipped image the list call failed and this endpoint returned an empty
// array, indistinguishable from "the compositor has no windows open". wlrctl IS
// packaged (85 KB) and now ships in build.sh's rootfs.
//
// WHAT WAS MEASURED (2026-08-10, labwc headless under Docker/arm64, wlrctl
// 0.2.2, against a real foot(1) toplevel):
//
//	wlrctl toplevel list            -> "demo-app: Demo Window", exit 0
//	wlrctl toplevel list, 0 windows -> empty output, exit 0
//	wlrctl window activate app_id:demo-app -> exit 0
//	wlrctl window minimize app_id:demo-app -> exit 0
//	wlrctl window close    app_id:demo-app -> exit 0
//	wlrctl window set-minimized app_id:demo-app -> exit 1,
//	                                "Unknown toplevel action: 'set-minimized'"
//	under cage: "Foreign Toplevel Management interface not found!", exit 1
//
// Two consequences are baked into the code below. First, Minimize used to send
// `set-minimized`, which wlrctl rejects — the verb is `minimize`. Second, cage
// does not implement foreign-toplevel at all, so on a v1 (cage) box this
// service can never work; that is reported as 503 "unavailable", NOT as an
// empty window list, because the two mean different things to a dock.
//
// Window.State is not populated: `wlrctl toplevel list` prints only
// "app_id: title" and has no state flags to report. The field is omitted from
// the JSON rather than sent empty, so a client cannot mistake "unknown" for
// "no flags set". Recovering state needs a real wlr-foreign-toplevel client
// (see roadmap/DISPLAY-STACK.md R4), not another shell-out.
//
// The command layer is injected via the Executor interface so tests can supply
// a deterministic fake without spawning real processes.
package wltoplevel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Domain types
// ---------------------------------------------------------------------------

// Window represents a single Wayland toplevel as reported by wlrctl.
type Window struct {
	// Handle is the opaque identifier used to focus/minimize/close the window.
	// It is the zero-based decimal index from the enumeration order.
	Handle string `json:"handle"`

	Title string `json:"title"`
	AppID string `json:"app_id"`
	// State is a comma-separated list of active state flags, e.g.
	// "activated", "maximized", "minimized", "fullscreen".
	//
	// ALWAYS EMPTY with the wlrctl backend, which reports no state flags — see
	// the package doc. omitempty keeps it out of the JSON entirely so a client
	// reads "absent" (unknown) rather than "" (no flags active).
	State string `json:"state,omitempty"`
}

// actionRequest is the JSON body sent to POST /api/shell/windows/{action}.
type actionRequest struct {
	Handle string `json:"handle"`
}

// ---------------------------------------------------------------------------
// Executor — mockable command layer
// ---------------------------------------------------------------------------

// Executor abstracts running external commands so tests can supply a fake.
type Executor interface {
	// Output runs a command and returns its combined stdout, or an error.
	Output(ctx context.Context, name string, args ...string) ([]byte, error)
	// Run runs a fire-and-forget command.
	Run(ctx context.Context, name string, args ...string) error
}

// realExecutor shells out to real binaries.
type realExecutor struct{}

func (realExecutor) Output(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output()
}

func (realExecutor) Run(ctx context.Context, name string, args ...string) error {
	return exec.CommandContext(ctx, name, args...).Run()
}

// ---------------------------------------------------------------------------
// Service
// ---------------------------------------------------------------------------

// Service lists and controls Wayland toplevels.
type Service struct {
	exec Executor
}

// New returns a Service backed by real process execution.
func New() *Service {
	return &Service{exec: realExecutor{}}
}

// newWithExecutor is used by tests to inject a fake executor.
func newWithExecutor(e Executor) *Service {
	return &Service{exec: e}
}

// ---------------------------------------------------------------------------
// Window enumeration
// ---------------------------------------------------------------------------

// List returns all currently visible Wayland toplevels.
//
// A non-zero exit or a missing binary returns errUnavailable — NOT an empty
// slice. Those two states look identical to a dock and mean opposite things:
// "the compositor reports no open windows" versus "this box cannot answer the
// question at all" (no wlrctl, no Wayland session, or a cage session, which
// does not implement foreign-toplevel). This function previously returned
// (empty, nil) for both, so a v1 box reported "no windows" forever.
//
// An empty list with exit 0 is a real answer and is returned as such.
func (s *Service) List(ctx context.Context) ([]Window, error) {
	out, err := s.exec.Output(ctx, "wlrctl", "toplevel", "list")
	if err != nil {
		if !isMissingOrRefused(err) {
			// A cancelled context or an I/O failure is not "this box has no
			// foreign-toplevel support" — do not launder it into a 503.
			return nil, fmt.Errorf("wlrctl toplevel list: %w", err)
		}
		// Polled every 2s by the shell, so log the reason once rather than
		// 30 times a minute.
		unavailableOnce.Do(func() {
			log.Printf("[wltoplevel] wlrctl toplevel list unavailable (%v) — "+
				"/api/shell/windows will report 503 until a foreign-toplevel-capable "+
				"compositor (labwc, not cage) and wlrctl are both present", err)
		})
		return nil, fmt.Errorf("%w: %v", errUnavailable, err)
	}
	return parseWlrctlList(bytes.TrimSpace(out)), nil
}

// unavailableOnce guards the one-time "no wlrctl" log line.
var unavailableOnce sync.Once

// exitCoder is anything that ran and exited non-zero: *exec.ExitError in
// production, a stub in tests. The classification below matches this interface
// rather than the concrete *exec.ExitError so a test can express "the tool ran
// and refused" at all — with the concrete type, every stubbed error fell into
// the catch-all branch and the tests silently agreed with whatever it did.
type exitCoder interface{ ExitCode() int }

// isMissingOrRefused reports whether err means "this box cannot answer" —
// either wlrctl is not installed, or it ran and refused (measured under cage:
// "Foreign Toplevel Management interface not found!", exit 1). Anything else
// — a context deadline, a pipe error — is a genuine failure of this request
// and must not be reported to the client as an unsupported compositor.
func isMissingOrRefused(err error) bool {
	if errors.Is(err, exec.ErrNotFound) {
		return true
	}
	var ec exitCoder
	return errors.As(err, &ec)
}

// parseWlrctlList converts `wlrctl toplevel list` output into Windows.
//
// Measured format, one toplevel per line:
//
//	app_id: title
//
// The separator is the FIRST ": " only — titles routinely contain colons
// ("vim: file.go — 3 changes"), app_ids do not. Handles are zero-based index
// strings assigned in enumeration order.
func parseWlrctlList(raw []byte) []Window {
	if len(raw) == 0 {
		return []Window{}
	}
	windows := []Window{}
	for i, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		w := Window{Handle: intToStr(i)}
		if appID, title, found := strings.Cut(line, ": "); found {
			w.AppID = strings.TrimSpace(appID)
			w.Title = strings.TrimSpace(title)
		} else {
			// No separator — treat the whole line as the app_id rather than
			// silently dropping a window we cannot fully describe.
			w.AppID = line
		}
		windows = append(windows, w)
	}
	return windows
}

// ---------------------------------------------------------------------------
// Window actions
// ---------------------------------------------------------------------------

// errUnavailable is returned when the necessary Wayland helper is absent.
var errUnavailable = errors.New("wlr-foreign-toplevel helper not available")

// Focus activates (raises and focuses) the window identified by handle.
//
// The handle is used to look up the window's app_id + title from a fresh
// snapshot; wlrctl is then called with an app_id match. This avoids wlrctl
// needing to understand numeric handles.
func (s *Service) Focus(ctx context.Context, handle string) error {
	return s.runAction(ctx, handle, "activate")
}

// Minimize iconifies the window identified by handle.
//
// The verb is `minimize`. It used to be `set-minimized`, which wlrctl rejects
// outright: "Unknown toplevel action: 'set-minimized'", exit 1 — measured
// against a live labwc toplevel, 2026-08-10. Every minimize request from the
// dock was failing on any box that had wlrctl at all.
func (s *Service) Minimize(ctx context.Context, handle string) error {
	return s.runAction(ctx, handle, "minimize")
}

// Close requests the window identified by handle to close.
func (s *Service) Close(ctx context.Context, handle string) error {
	return s.runAction(ctx, handle, "close")
}

// runAction resolves a handle to a window and calls wlrctl window <verb> app_id:<id>.
func (s *Service) runAction(ctx context.Context, handle, verb string) error {
	windows, err := s.List(ctx)
	if err != nil {
		return fmt.Errorf("list windows: %w", err)
	}

	var target *Window
	for i := range windows {
		if windows[i].Handle == handle {
			target = &windows[i]
			break
		}
	}
	if target == nil {
		return fmt.Errorf("window handle %q not found", handle)
	}

	// Build wlrctl selector. Prefer app_id, fall back to title.
	selector := ""
	if target.AppID != "" {
		selector = "app_id:" + target.AppID
	} else if target.Title != "" {
		selector = "title:" + target.Title
	} else {
		return fmt.Errorf("window handle %q has no app_id or title to match", handle)
	}

	if err := s.exec.Run(ctx, "wlrctl", "window", verb, selector); err != nil {
		var ec exitCoder
		if errors.As(err, &ec) {
			// The tool ran and rejected the request. That is a failed action,
			// not an unavailable service — a 500, so the caller sees that the
			// window did not move rather than "your compositor is unsupported".
			return fmt.Errorf("wlrctl window %s %s: exit %d", verb, selector, ec.ExitCode())
		}
		if errors.Is(err, exec.ErrNotFound) {
			log.Printf("[wltoplevel] wlrctl not installed (%v)", err)
			return errUnavailable
		}
		return fmt.Errorf("wlrctl window %s %s: %w", verb, selector, err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// HTTP handlers
// ---------------------------------------------------------------------------

// RegisterHandlers mounts the window list and action endpoints on mux.
//
// Routes:
//
//	GET  /api/shell/windows            — list all Wayland toplevels
//	POST /api/shell/windows/focus      — activate window (body: {"handle":"N"})
//	POST /api/shell/windows/minimize   — iconify window
//	POST /api/shell/windows/close      — close window
func (s *Service) RegisterHandlers(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/shell/windows", s.handleList)
	mux.HandleFunc("POST /api/shell/windows/focus", s.handleAction(s.Focus))
	mux.HandleFunc("POST /api/shell/windows/minimize", s.handleAction(s.Minimize))
	mux.HandleFunc("POST /api/shell/windows/close", s.handleAction(s.Close))
}

func (s *Service) handleList(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	windows, err := s.List(ctx)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		if errors.Is(err, errUnavailable) {
			// 503, not 200-with-[]. "I cannot see the windows" must not be
			// served as "there are no windows" — the dock draws those two
			// states identically and one of them is a lie.
			w.WriteHeader(http.StatusServiceUnavailable)
			json.NewEncoder(w).Encode(map[string]string{
				"error": "wlr-foreign-toplevel unavailable",
				"detail": "no wlrctl on PATH, or the compositor does not implement " +
					"wlr-foreign-toplevel-management-v1 (cage does not; labwc does)",
			})
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "internal error"})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(windows)
}

func (s *Service) handleAction(fn func(context.Context, string) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req actionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Handle == "" {
			http.Error(w, "bad request: handle required", http.StatusBadRequest)
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()

		if err := fn(ctx, req.Handle); err != nil {
			if errors.Is(err, errUnavailable) {
				// Not running under a compatible compositor — tell the client
				// but don't treat it as a server error.
				http.Error(w, "wayland compositor unavailable", http.StatusServiceUnavailable)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// intToStr converts a non-negative integer to its decimal string representation
// without importing strconv (keeping the dependency footprint minimal).
func intToStr(n int) string {
	if n == 0 {
		return "0"
	}
	buf := make([]byte, 0, 10)
	for n > 0 {
		buf = append([]byte{byte('0' + n%10)}, buf...)
		n /= 10
	}
	return string(buf)
}
