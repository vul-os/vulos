package proctl

// _NET_WM_PING: the one X exchange that an application, and not the X server,
// has to answer.
//
// # What this measures, exactly
//
// The window manager sends a client a WM_PROTOCOLS/_NET_WM_PING ClientMessage.
// A live application dequeues it in its event loop and sends the same message
// back to the root window. That is the whole protocol, and it is what GNOME's
// and KDE's "Application is not responding — Force Quit?" dialogs are built on.
//
// The echo can only be produced by code running inside the application's event
// loop. It therefore measures the same property the HTTP probe measures for a
// process-backed app, and the same property macOS reports as "(Not
// Responding)": is this program still draining the queue it is supposed to
// drain?
//
// # Hung versus idle — the distinction the whole feature turns on
//
// An idle app answers. A GTK, Qt or SDL toolkit sitting with nothing to draw is
// blocked in poll() on its X connection; the ping wakes it, the handler runs,
// the echo goes out in microseconds. Idleness is not quietness at the protocol
// level — it is a loop waiting for work, and a ping IS work.
//
// A hung app does not answer, whether it is spinning in a callback, deadlocked
// on a mutex, stopped by a signal or blocked in a syscall. In every one of those
// cases the ping sits in the queue undelivered to the handler.
//
// This is exactly why the substitutes were rejected. CPU is near zero for both
// idle and deadlocked. The /proc state letter is S for both. The video pipeline
// happily encodes 60 frames a second of a frozen window, so "the stream is
// flowing" says nothing about the app. And every X TOOL on the box —
// xdotool, xprop, xwininfo — queries the SERVER's copy of the window tree,
// which answers identically for a healthy app and a wedged one. See x11.go.
//
// # The failure this design refuses to make
//
// If the X server itself stops answering, every app on that display goes silent
// too. Reporting that as "the app is not responding" would be a confident,
// wrong, actionable badge: the user force-quits an application that was fine,
// and the display is still broken afterwards. So an unanswered ping is followed
// by a round trip to the server, and only a server that ANSWERS lets this
// return not_responding. A server that does not gets its own status,
// StatusDisplayNotResponding, which names the display and not the app.

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"time"
)

// DefaultXPingBudget is how long an app has to echo a ping before it is called
// not responding.
//
// Five seconds, matching what desktop window managers wait before offering to
// kill a client — mutter and KWin are both in this range, and they are the
// implementations users' expectations are actually calibrated against. The
// tension is real in both directions: too short and an app doing a legitimate
// synchronous four-second load (opening a large project file, decompressing a
// save) gets a "Not responding" badge and a user who force-quits it mid-write;
// too long and a genuinely dead app reads as fine while the user stares at a
// frozen picture. It is longer than the 3s HTTP probe budget on purpose — an
// HTTP app is asked on a fresh connection with nothing queued ahead of the
// request, while a ping sits behind whatever else is in the client's queue,
// including a full redraw.
//
// The cost of the wait is bounded by probing sessions concurrently, so a box
// with several sessions waits the maximum rather than the sum.
const DefaultXPingBudget = 5 * time.Second

// xServerCheckBudget bounds the "is the server still there?" round trip made
// after an unanswered ping. It is short because it is a one-request exchange
// with a server that, if healthy, is not doing anything else for this
// connection.
const xServerCheckBudget = time.Second

// Window-tree walk bounds. A stream display holds one app, a window manager and
// their handful of frames and icon windows; these caps exist so that a display
// with a pathological tree cannot turn a status poll into a long scan.
const (
	xWalkMaxDepth   = 6
	xWalkMaxWindows = 512
)

// ProbeX11Ping asks every pingable window on an X display to echo, and reports
// what came back.
//
// socket is the server's unix socket path (/tmp/.X11-unix/X<n>). budget of 0
// means DefaultXPingBudget.
//
// The four answers it can give, and what each one is evidence of:
//
//	responding             — an application echoed. Its event loop is running.
//	not_responding         — nothing echoed AND the X server still answers.
//	                         The app, specifically, is not draining its queue.
//	display_not_responding — the X server did not answer. Says nothing about
//	                         any application on it, and must not be rendered
//	                         as though it did.
//	unknown                — no window on the display implements _NET_WM_PING,
//	                         so there was nothing to ask. NOT a verdict.
func ProbeX11Ping(ctx context.Context, socket string, budget time.Duration) Responsiveness {
	if budget <= 0 {
		budget = DefaultXPingBudget
	}
	start := time.Now()
	elapsed := func() int64 { return time.Since(start).Milliseconds() }

	dialCtx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	x, err := xdial(dialCtx, socket)
	if err != nil {
		return Responsiveness{
			Status: StatusDisplayNotResponding, Method: MethodX11Ping,
			Detail:    fmt.Sprintf("no answer from the X server at %s: %v", socket, err),
			CheckedMS: elapsed(),
		}
	}
	defer x.Close()
	deadline := start.Add(budget)
	x.setDeadline(deadline)

	protocols, err := x.internAtom("WM_PROTOCOLS")
	if err != nil {
		return displayStalled(socket, "WM_PROTOCOLS atom", err, elapsed())
	}
	ping, err := x.internAtom("_NET_WM_PING")
	if err != nil {
		return displayStalled(socket, "_NET_WM_PING atom", err, elapsed())
	}

	// Subscribe to the root window BEFORE pinging. The echo is sent to root, so
	// selecting afterwards would race a fast application and lose — an app that
	// answered in under a millisecond would be reported as not responding,
	// which is the exact false badge this feature must never produce.
	if err := x.selectRootSubstructure(); err != nil {
		return displayStalled(socket, "root event mask", err, elapsed())
	}
	if err := x.roundTrip(); err != nil {
		return displayStalled(socket, "root event mask", err, elapsed())
	}

	targets, seen, err := pingableWindows(x, protocols, ping)
	if err != nil {
		return displayStalled(socket, "window tree", err, elapsed())
	}
	if len(targets) == 0 {
		// NOT a freeze. A raw-Xlib game, an SDL1 client, a splash window or an
		// app that has not mapped its first window yet all land here, and every
		// one of them may be perfectly healthy. Saying so is the whole reason
		// Status has an "unknown" that is not folded into "responding".
		return Responsiveness{
			Status: StatusUnknown, Method: MethodX11Ping,
			Detail: fmt.Sprintf("no window on this display implements _NET_WM_PING "+
				"(%d windows examined); the app cannot be asked, which is not the same as frozen", seen),
			CheckedMS: elapsed(),
		}
	}

	// A nonce in the timestamp field. A conforming client echoes the message
	// verbatim, so this comes back and identifies OUR ping. Correlation does
	// not depend on it — the echo is matched on the window id, and an echo from
	// a window is evidence that window's client drained its queue no matter who
	// asked — but a stray match would still be a wrong reason for a right
	// answer, and this makes one visible.
	nonce := uint32(time.Now().UnixNano())
	if nonce == 0 {
		nonce = 1
	}
	for _, w := range targets {
		ev := clientMessage32(w, protocols, [5]uint32{ping, nonce, w, 0, 0})
		if err := x.sendClientMessage(w, 0, ev); err != nil {
			return displayStalled(socket, "ping", err, elapsed())
		}
	}
	// Flush the pings and prove the server accepted them before starting to
	// wait. Without this, a server that swallowed the SendEvent (a BadWindow on
	// every target, say) would look like an application that stayed silent.
	if err := x.roundTrip(); err != nil {
		return displayStalled(socket, "ping", err, elapsed())
	}

	want := map[uint32]bool{}
	for _, w := range targets {
		want[w] = true
	}
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}
		x.setDeadline(time.Now().Add(remaining))
		p, err := x.nextEvent()
		if err != nil {
			break // deadline, or the connection died; both resolved below
		}
		msgType, data, ok := clientMessageData(p)
		if !ok || msgType != protocols || data[0] != ping {
			continue
		}
		if !want[data[2]] {
			continue
		}
		detail := fmt.Sprintf("_NET_WM_PING echoed by window 0x%x in %dms", data[2], elapsed())
		if data[1] != nonce {
			detail += " (echo carried a different timestamp than the ping)"
		}
		return Responsiveness{
			Status: StatusResponding, Method: MethodX11Ping,
			Detail: detail, CheckedMS: elapsed(),
		}
	}

	// Nothing echoed. Before this may be called an application freeze, the X
	// server has to prove it is still answering — otherwise a wedged server is
	// reported as a wedged app, and the user force-quits the wrong thing.
	//
	// On a FRESH connection, not this one. The wait above ended in a read
	// timeout, and a timeout can land in the middle of a 32-byte packet: the
	// bytes already consumed are gone and this connection's framing is
	// unreliable from here on. A confused parse on a desynced connection looks
	// exactly like a server that stopped answering, and would put the wrong
	// name on the failure — the same class of mistake as blaming the app.
	if err := xServerAlive(ctx, socket); err != nil {
		return Responsiveness{
			Status: StatusDisplayNotResponding, Method: MethodX11Ping,
			Detail: fmt.Sprintf("no ping echo, and the X server at %s then failed to answer "+
				"a round trip (%v) — the display is stalled, which says nothing about the app", socket, err),
			CheckedMS: elapsed(),
		}
	}
	return Responsiveness{
		Status: StatusNotResponding, Method: MethodX11Ping,
		Detail: fmt.Sprintf("no _NET_WM_PING echo from %d window(s) in %s, "+
			"while the X server kept answering", len(targets), budget),
		CheckedMS: elapsed(),
	}
}

// xServerAlive completes a handshake and one round trip on a new connection.
//
// Both halves matter. A stalled X server still has a listening socket — the
// kernel completes the TCP/unix accept without the server process doing
// anything — so "the connect succeeded" is not evidence of life. The handshake
// and the GetInputFocus reply are, because both require the server's own loop
// to run.
func xServerAlive(ctx context.Context, socket string) error {
	ctx, cancel := context.WithTimeout(ctx, xServerCheckBudget)
	defer cancel()
	x, err := xdial(ctx, socket)
	if err != nil {
		return err
	}
	defer x.Close()
	return x.roundTrip()
}

// displayStalled reports a failure of the X connection itself.
func displayStalled(socket, during string, err error, ms int64) Responsiveness {
	return Responsiveness{
		Status: StatusDisplayNotResponding, Method: MethodX11Ping,
		Detail:    fmt.Sprintf("the X server at %s stopped answering during %s: %v", socket, during, err),
		CheckedMS: ms,
	}
}

// pingableWindows walks the window tree and returns the windows whose
// WM_PROTOCOLS lists _NET_WM_PING, plus how many windows were examined.
//
// The walk is by tree rather than by _NET_CLIENT_LIST because the property is
// the window manager's to publish and matchbox — the WM on the Xvfb path — is
// not a full EWMH implementation. Reading the tree asks the server for what is
// actually there and works with no window manager at all, which is the state a
// session is in for the first few hundred milliseconds of its life.
//
// The descent is necessary, not defensive: a reparenting WM puts the client
// window inside a frame it created, so the children of root are frames and
// WM_PROTOCOLS lives one or more levels below.
func pingableWindows(x *xconn, protocols, ping uint32) ([]uint32, int, error) {
	var out []uint32
	seen := 0

	type node struct {
		win   uint32
		depth int
	}
	queue := []node{{x.root, 0}}
	for len(queue) > 0 && seen < xWalkMaxWindows {
		n := queue[0]
		queue = queue[1:]

		if n.win != x.root {
			seen++
			value, ptype, err := x.getProperty(n.win, protocols, atomAtom)
			if err != nil {
				return nil, seen, err
			}
			if ptype == atomAtom && hasAtom(value, ping) {
				out = append(out, n.win)
				// Do not descend into a client's own subwindows: the client
				// window is the toplevel that owns the protocol, and its
				// children are its widgets. One target per client is what is
				// wanted; a hundred is a hundred pings to one event loop.
				continue
			}
		}
		if n.depth >= xWalkMaxDepth {
			continue
		}
		kids, err := x.queryTree(n.win)
		if err != nil {
			// A PROTOCOL error means the window vanished between being listed
			// and being asked about, which is ordinary on a live display and
			// must not abort the walk. Any other error is the connection
			// itself failing, and swallowing that turns a stalled X server
			// into "no window implements _NET_WM_PING" — a wrong, calm answer
			// to a display that is actually broken. Written the wrong way
			// round first; TestProbeX11Ping_StalledServerIsNotBlamedOnTheApp
			// caught it.
			var xe *xerror
			if errors.As(err, &xe) {
				continue
			}
			return nil, seen, err
		}
		for _, k := range kids {
			queue = append(queue, node{k, n.depth + 1})
		}
	}
	return out, seen, nil
}

// hasAtom reports whether a 32-bit ATOM property value contains atom.
func hasAtom(value []byte, atom uint32) bool {
	for i := 0; i+4 <= len(value); i += 4 {
		if binary.LittleEndian.Uint32(value[i:]) == atom {
			return true
		}
	}
	return false
}
