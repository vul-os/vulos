// Package wltoplevel enumerates and controls native Wayland windows via the
// wlr-foreign-toplevel-management-v1 protocol.
//
// On a bare-metal Vula OS system running labwc the companion tool lswt(1) is
// used to snapshot the toplevel list and wlrctl(1) to activate/minimize/close
// individual handles. When neither tool is present, or when the process is not
// running inside a Wayland session, all list requests return an empty slice and
// action requests return a graceful "unavailable" error rather than a hard
// failure.
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
	"time"
)

// ---------------------------------------------------------------------------
// Domain types
// ---------------------------------------------------------------------------

// Window represents a single Wayland toplevel as reported by lswt.
type Window struct {
	// Handle is the opaque identifier used to focus/minimize/close the window.
	// It is the zero-based decimal index from the lswt enumeration order.
	Handle string `json:"handle"`

	Title string `json:"title"`
	AppID string `json:"app_id"`
	// State is a comma-separated list of active state flags, e.g.
	// "activated", "maximized", "minimized", "fullscreen".
	State string `json:"state"`
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
// It tries lswt first (Wayland-native); if unavailable it returns an empty
// slice with no error so callers (e.g. the dock) degrade gracefully.
func (s *Service) List(ctx context.Context) ([]Window, error) {
	// lswt -t prints one tab-separated line per toplevel:
	//   <title>\t<app_id>\t<state flags space-separated>
	// Handles are assigned as zero-based index strings ("0", "1", ...).
	out, err := s.exec.Output(ctx, "lswt", "-t")
	if err != nil {
		// Tool missing or no Wayland session — degrade to empty list.
		log.Printf("[wltoplevel] lswt unavailable (%v) — returning empty window list", err)
		return []Window{}, nil
	}
	return parseLswt(bytes.TrimSpace(out)), nil
}

// parseLswt converts raw lswt -t output into a slice of Windows.
// Expected format per line (tab-separated):
//
//	<title>  <app_id>  <state>
//
// State is a space-separated list of flags on a single field, e.g. "activated maximized".
func parseLswt(raw []byte) []Window {
	if len(raw) == 0 {
		return []Window{}
	}
	var windows []Window
	for i, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Split(line, "\t")
		w := Window{
			Handle: intToStr(i),
		}
		if len(parts) > 0 {
			w.Title = strings.TrimSpace(parts[0])
		}
		if len(parts) > 1 {
			w.AppID = strings.TrimSpace(parts[1])
		}
		if len(parts) > 2 {
			// State flags are space-separated; normalise to comma-separated for JSON.
			flags := strings.Fields(strings.TrimSpace(parts[2]))
			w.State = strings.Join(flags, ",")
		}
		windows = append(windows, w)
	}
	if windows == nil {
		return []Window{}
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
// The handle is used to look up the window's app_id + title from a fresh lswt
// snapshot; wlrctl is then called with an app_id match. This avoids wlrctl
// needing to understand numeric handles.
func (s *Service) Focus(ctx context.Context, handle string) error {
	return s.runAction(ctx, handle, "activate")
}

// Minimize iconifies the window identified by handle.
func (s *Service) Minimize(ctx context.Context, handle string) error {
	return s.runAction(ctx, handle, "set-minimized")
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
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return fmt.Errorf("wlrctl window %s %s: exit %d", verb, selector, exitErr.ExitCode())
		}
		log.Printf("[wltoplevel] wlrctl unavailable (%v)", err)
		return errUnavailable
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
		http.Error(w, "internal error", http.StatusInternalServerError)
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
