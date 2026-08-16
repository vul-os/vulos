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
	// StatusDisplayNotResponding — the app's DISPLAY stopped answering, so no
	// question about the app could be put to it.
	//
	// This is its own status rather than a flavour of not_responding because it
	// is a different fault with a different remedy. When an X server stalls,
	// every application on it goes silent at once; they are all fine and the
	// display is not. Folding it into not_responding would put a "Not
	// responding" badge on a healthy app, and the user's obvious response —
	// force-quit it — destroys work and does not fix the frozen picture. The
	// session is what needs restarting.
	//
	// It IS a measurement, unlike unknown: something was probed and something
	// definite came back, or definitely did not. What it is a measurement OF is
	// the display.
	StatusDisplayNotResponding Status = "display_not_responding"
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
	// MethodX11Ping — a WM_PROTOCOLS/_NET_WM_PING ClientMessage sent to an X
	// window, and the echo the application's own event loop sends back. This is
	// the real event-loop test for a streamed desktop app; see xping.go for
	// what it proves and for why every X TOOL on the box cannot do it.
	MethodX11Ping Method = "x11_ping"
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

// StreamUnknown is the answer for a streamed session that offers nothing to
// ask.
//
// # This used to be the answer for ALL streamed apps, and was wrong about why
//
// The original text said Vulos could not implement _NET_WM_PING because there
// was no X client on the box — "no xdotool, no xprop, no Go X binding anywhere
// in the tree, checked, not assumed". Two thirds of that was false when it was
// written: scripts/build-sh-packages.txt ships xdotool in the rootfs and the
// Dockerfile installs it in the image.
//
// The conclusion happened to survive the correction, but not for the stated
// reason. xdotool, xprop and xwininfo all query the X SERVER's copy of the
// window tree, which answers just as promptly for an application that has been
// wedged for an hour. They cannot send a ping. What was actually missing was an
// X client that speaks the protocol, and proctl now contains one: see
// ProbeX11Ping, and x11.go for why the tools were the wrong shape rather than
// merely absent.
//
// What remains genuinely unaskable, and why:
//
//   - The Wayland path. When the box has a GPU, services/stream runs `cage`
//     instead of Xvfb and the app is a Wayland client. Wayland's equivalent is
//     xdg_wm_base.ping, and it is the COMPOSITOR's to send; cage exposes no
//     control interface, and a second client cannot ping another client's
//     surface. See StreamWayland.
//   - A session with no X window that implements _NET_WM_PING — a raw-Xlib or
//     SDL1 game, or an app in the first moments before it maps anything.
//     ProbeX11Ping reports that case as unknown with the window count, because
//     "nobody to ask" and "asked and got no answer" are different facts.
//   - A session that is not running, which is this constructor's remaining use.
func StreamUnknown() Responsiveness {
	return Responsiveness{
		Status: StatusUnknown, Method: MethodNone,
		Detail: "this session has no display to ask",
	}
}

// StreamWayland is the answer for a session running on cage rather than Xvfb.
//
// Not a stopgap and not laziness: on Wayland there is no client-to-client
// protocol for this at all. xdg_wm_base.ping travels from the COMPOSITOR to the
// surface's owner, and a Wayland client cannot address another client's
// surface — the design goal that removes X's global window tree also removes
// the ability to ping across it. cage is the compositor here and it is a
// deliberately minimal kiosk shell with no control socket, no IPC and no
// unresponsive-client reporting, so there is nothing to ask it either.
//
// Closing this gap means a compositor that reports pings (labwc, or cage with a
// patch) and a client that can read that report — a substantially different
// piece of work from the X path, and not one to be faked in the meantime by
// reusing a signal that measures something else.
func StreamWayland() Responsiveness {
	return Responsiveness{
		Status: StatusUnknown, Method: MethodNone,
		Detail: "this session runs on the Wayland compositor (cage), where a liveness ping " +
			"is the compositor's to send and no client can send one; only the X11 path can be probed",
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
