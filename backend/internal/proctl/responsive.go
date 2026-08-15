package proctl

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

// Responsiveness answers "is this thing frozen?" — and, just as importantly,
// records HOW it answered and refuses to answer when it cannot.
//
// # Why this type exists instead of a boolean
//
// macOS shows "(Not Responding)" when an app stops draining its event queue.
// That is a specific measurement against a specific mechanism, not a synonym
// for busy: a process pinning a core is responsive, and a process at 0% CPU
// blocked on a lock is not.
//
// Vulos runs four different kinds of thing, and the mechanism available for
// each is genuinely different. A single `frozen bool` on a row would force one
// of them to be presented as if it meant the same as the others, and three of
// the four would then be a guess wearing the clothes of a measurement. So the
// answer carries its Method, and Status has an explicit "unknown" that is
// rendered as "unknown" rather than quietly folded into "responding".
type Responsiveness struct {
	Status Status `json:"status"`
	Method Method `json:"method"`
	// Detail is the human-readable evidence: the HTTP code and latency, the
	// process state letter, or the reason no measurement is possible.
	Detail string `json:"detail"`
	// CheckedMS is how long the probe took. Zero when no probe was made.
	CheckedMS int64 `json:"checked_ms"`
}

// Status is the four-valued answer. Not a boolean, on purpose.
type Status string

const (
	// StatusResponding — a probe was made and the subject answered it.
	StatusResponding Status = "responding"
	// StatusNotResponding — a probe was made and the subject did not answer
	// within the budget. This is the only value that may be shown to a user as
	// "Not responding", and it is set only by an actual measurement.
	StatusNotResponding Status = "not_responding"
	// StatusUnknown — no mechanism is available to ask. Vulos does not know.
	// Distinct from StatusNotApplicable: unknown means the question is
	// meaningful and unanswerable, which is a gap, not a category error.
	StatusUnknown Status = "unknown"
	// StatusNotApplicable — the subject is not a thing that can be asked.
	StatusNotApplicable Status = "not_applicable"
)

// Method names the mechanism behind a Status, so that a UI (or a reader of the
// JSON) can tell a measurement from an absence of one.
type Method string

const (
	// MethodHTTPProbe — an HTTP request through the app's namespace. This is
	// the real event-loop test for a process-backed app: a server that has
	// stopped accepting or stopped replying is exactly the "stopped servicing
	// its loop" condition macOS reports.
	MethodHTTPProbe Method = "http_probe"
	// MethodProcState — /proc/<pid>/stat state letter. This is NOT a
	// responsiveness probe and must never be labelled one; see StateNote.
	MethodProcState Method = "proc_state"
	// MethodNone — nothing available. Detail says why.
	MethodNone Method = "none"
	// MethodClientSide — the subject is a browser surface; only the client can
	// measure it, and the backend declines rather than guessing.
	MethodClientSide Method = "client_side"
)

// probeBudget bounds an HTTP liveness probe.
//
// Three seconds matches the existing gateway.HealthCheck timeout, which is the
// same probe against the same apps — a different number here would mean the
// Activity Monitor and /api/apps/health disagree about the same app, and the
// disagreement would be an artefact of two constants rather than of anything
// happening on the box.
const probeBudget = 3 * time.Second

// ProbeHTTP asks an app's own HTTP surface whether it is still serving.
//
// Any complete response counts as responding, INCLUDING a 5xx: a 500 means the
// server accepted the connection, routed the request and generated a reply,
// which is precisely the evidence that its loop is running. Treating 5xx as
// frozen would label a working-but-erroring app as hung, and the user would
// force-quit something that was about to log a stack trace.
func ProbeHTTP(ctx context.Context, client *http.Client, url string) Responsiveness {
	start := time.Now()
	ctx, cancel := context.WithTimeout(ctx, probeBudget)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Responsiveness{
			Status: StatusUnknown, Method: MethodNone,
			Detail: "cannot build probe request: " + err.Error(),
		}
	}
	resp, err := client.Do(req)
	elapsed := time.Since(start).Milliseconds()
	if err != nil {
		return Responsiveness{
			Status: StatusNotResponding, Method: MethodHTTPProbe,
			Detail:    fmt.Sprintf("no reply within %s", probeBudget),
			CheckedMS: elapsed,
		}
	}
	resp.Body.Close()
	return Responsiveness{
		Status: StatusResponding, Method: MethodHTTPProbe,
		Detail:    fmt.Sprintf("HTTP %d in %dms", resp.StatusCode, elapsed),
		CheckedMS: elapsed,
	}
}

// StreamUnknown is the answer for a streamed X11 desktop app.
//
// X HAS the right mechanism — _NET_WM_PING, the EWMH protocol a window manager
// uses to decide a client is hung, which is the direct analogue of what macOS
// reports. Vulos does not implement it: services/stream runs Xvfb, a WM and
// gstreamer, and there is no X client in this codebase that could send a ping
// and time the reply (no xdotool, no xprop, no Go X binding anywhere in the
// tree — checked, not assumed).
//
// Returning "unknown" with that reason is the honest position. The available
// substitutes are all wrong in the same way: the app's process state is S while
// it waits for X, whether it is healthy or wedged; its CPU is near zero in both
// cases; and the video pipeline keeps producing frames of a frozen window, so
// "the stream is flowing" is not evidence about the app at all. Every one of
// those would produce a confident badge that is uncorrelated with the thing it
// claims to measure.
func StreamUnknown() Responsiveness {
	return Responsiveness{
		Status: StatusUnknown, Method: MethodNone,
		Detail: "streamed X11 apps need _NET_WM_PING, which this box does not implement yet",
	}
}

// BuiltinNotApplicable is the answer for a built-in surface such as this
// monitor itself. These are React views inside the shell's own browser tab —
// no process, no port, nothing for the server to probe. If the shell's main
// thread is blocked, the server cannot observe it and the UI that would display
// the badge is blocked too.
func BuiltinNotApplicable() Responsiveness {
	return Responsiveness{
		Status: StatusNotApplicable, Method: MethodClientSide,
		Detail: "built-in views run in the browser; only the shell can measure its own main thread",
	}
}

// StateNote explains a /proc state letter WITHOUT claiming it is a
// responsiveness verdict.
//
// This is the one that is easiest to get wrong. D — uninterruptible sleep — is
// the state that most looks like "frozen" and most often is not an application
// fault at all: it is a task blocked inside a syscall that cannot be
// interrupted, overwhelmingly storage (a slow disk, a stalled USB device, a
// hung NFS or SMB mount). The application is fine; the block device is not. It
// is also the one state where a Force Quit genuinely cannot work, because the
// signal is not delivered until the I/O returns.
//
// So a D-state row gets a note, not a "Not Responding" badge, and the returned
// Status is deliberately StatusUnknown for every state: no state letter is
// evidence about an event loop.
func StateNote(state string) Responsiveness {
	detail := ""
	switch state {
	case "D":
		detail = "uninterruptible sleep — blocked in the kernel, usually waiting on storage; signals are not delivered until it returns"
	case "Z":
		detail = "zombie — already exited, waiting for its parent to collect it; it cannot be killed again"
	case "T", "t":
		detail = "stopped — suspended by a signal or a debugger, not hung"
	case "R":
		detail = "running on a CPU"
	case "S":
		detail = "sleeping — waiting on an event; this is the normal state for an idle program"
	case "I":
		detail = "idle kernel task"
	default:
		detail = "state " + state
	}
	return Responsiveness{
		Status: StatusUnknown, Method: MethodProcState,
		Detail: detail,
	}
}
