package stream

// Responsiveness for a streamed session.
//
// # Why the probe is in proctl and only the routing is here
//
// services/stream is the only package that knows a session's display number and
// whether that session is running on Xvfb or on cage, so the DECISION about
// which mechanism applies has to be made here. The probe itself is in
// internal/proctl, with the rest of the responsiveness model, for two reasons.
//
// First, proctl is where the vocabulary lives — Status, Method, and the rule
// that only a real measurement may produce a verdict. A second Responsiveness
// producer growing outside it is how the four categories start meaning
// different things in different files, which is the exact failure the type was
// introduced to prevent.
//
// Second, testability. proctl imports nothing from this repo and can be
// exercised against a scripted X server on any OS. This package cannot be
// imported without Xvfb, GStreamer, WebRTC and uinput in scope, and a probe
// that decides whether a user sees "Not responding" must be testable without
// any of them.
//
// What crosses the boundary is the smallest possible thing: a socket path.

import (
	"context"
	"fmt"

	"vulos/backend/internal/proctl"
)

// x11Socket is the display's unix socket path.
//
// ONE definition, used by the launch-time readiness wait, by Stop's cleanup and
// by the probe below. It was two copies of the same format string and the probe
// would have been a third; a probe dialling its own copy is how a check ends up
// watching something nothing creates, which has already happened in this repo —
// a placement gate read a window title that the shipping client never set. See
// TestX11SocketPathHasOneDefinition.
//
// A var, not a func, for the same reason lookPath is: a test needs to point it
// at a socket it can control without writing into /tmp/.X11-unix.
var x11Socket = func(displayNum int) string {
	return fmt.Sprintf("/tmp/.X11-unix/X%d", displayNum)
}

// Responsiveness reports whether the application in this session is still
// draining its event loop.
//
// Three routes, and which one applies is a property of how the session was
// launched, not a preference:
//
//   - Xvfb (X11): a real _NET_WM_PING, sent to the app's own window and
//     answered only by its event loop. See proctl.ProbeX11Ping.
//   - cage (Wayland): unmeasurable, and not for want of trying. The ping in
//     Wayland is xdg_wm_base.ping and only the COMPOSITOR may send it; cage has
//     no control interface to ask. proctl.StreamWayland says so.
//   - a session that is not running: nothing to ask.
//
// Worth knowing before calling this in a loop: a hung app costs the full ping
// budget plus a server check, so callers listing several sessions should probe
// them concurrently. cmd/server does.
func (s *Session) Responsiveness(ctx context.Context) proctl.Responsiveness {
	s.mu.Lock()
	running := s.Running
	displayNum := s.displayNum
	wayland := s.cage != nil || s.cageRTDir != ""
	haveXvfb := s.xvfb != nil
	s.mu.Unlock()

	switch {
	case !running:
		return proctl.StreamUnknown()
	case wayland:
		return proctl.StreamWayland()
	case !haveXvfb:
		// Neither compositor started. The session is in a state it should not
		// be in, and inventing a verdict about the app would be the worst
		// possible response to not understanding the situation.
		return proctl.StreamUnknown()
	}
	return proctl.ProbeX11Ping(ctx, x11Socket(displayNum), 0)
}
