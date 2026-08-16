package proctl

import (
	"context"
	"strings"
	"testing"
	"time"
)

// oneAppDisplay is the shape a stream session actually has: a root window, a
// window-manager frame, and the application's client window inside it. The
// nesting is not decoration — matchbox reparents, so a probe that only looked
// at the children of root would find frames and conclude that nothing on the
// display implements _NET_WM_PING.
func oneAppDisplay(echo bool) *fakeX {
	return &fakeX{
		tree: map[uint32][]uint32{
			fakeRootWindow: {10}, // 10 is the WM frame
			10:             {20}, // 20 is the client window
			20:             {21}, // 21 is a widget inside the client
		},
		pingable: map[uint32]bool{20: true},
		echo:     echo,
	}
}

func TestProbeX11Ping_RespondingWhenTheAppEchoes(t *testing.T) {
	sock := startFakeX(t, oneAppDisplay(true))

	got := ProbeX11Ping(context.Background(), sock, time.Second)
	if got.Status != StatusResponding {
		t.Fatalf("Status = %q, want %q (detail: %s)", got.Status, StatusResponding, got.Detail)
	}
	if got.Method != MethodX11Ping {
		t.Errorf("Method = %q, want %q", got.Method, MethodX11Ping)
	}
	if !strings.Contains(got.Detail, "0x14") {
		t.Errorf("detail %q does not name the window that answered", got.Detail)
	}
}

// The idle case, which is the one a bad implementation gets wrong. Nothing is
// drawing, no input has arrived, the app has been sitting there doing nothing —
// and it must NOT be reported as frozen, because its loop still runs. This is
// the same fake as the responding test with the same silence on every other
// signal; the ONLY difference from the hung case below is whether the client
// answered the ping.
func TestProbeX11Ping_IdleAppIsNotReportedFrozen(t *testing.T) {
	f := oneAppDisplay(true)
	f.echoDelay = 40 * time.Millisecond // answers, but not instantly
	sock := startFakeX(t, f)

	got := ProbeX11Ping(context.Background(), sock, 2*time.Second)
	if got.Status != StatusResponding {
		t.Fatalf("an idle app that answers its ping reported %q, want %q (detail: %s)",
			got.Status, StatusResponding, got.Detail)
	}
}

// The hung case. Identical display, identical windows, identical server — the
// client simply never dequeues the ping.
func TestProbeX11Ping_NotRespondingWhenTheAppNeverEchoes(t *testing.T) {
	sock := startFakeX(t, oneAppDisplay(false))

	start := time.Now()
	got := ProbeX11Ping(context.Background(), sock, 300*time.Millisecond)
	if got.Status != StatusNotResponding {
		t.Fatalf("Status = %q, want %q (detail: %s)", got.Status, StatusNotResponding, got.Detail)
	}
	if got.Method != MethodX11Ping {
		t.Errorf("Method = %q, want %q — an unanswered ping is still a measurement", got.Method, MethodX11Ping)
	}
	if !strings.Contains(got.Detail, "X server kept answering") {
		t.Errorf("detail %q does not record that the server was proved alive, which is the "+
			"only thing that makes this the APP's fault", got.Detail)
	}
	if time.Since(start) < 300*time.Millisecond {
		t.Errorf("returned after %v, before the budget elapsed — it cannot have waited for an echo",
			time.Since(start))
	}
}

// The two directions, side by side, as one assertion: the same probe against
// the same display must separate a live event loop from a wedged one.
func TestProbeX11Ping_SeparatesHungFromIdle(t *testing.T) {
	live := startFakeX(t, oneAppDisplay(true))
	hung := startFakeX(t, oneAppDisplay(false))

	a := ProbeX11Ping(context.Background(), live, 300*time.Millisecond)
	b := ProbeX11Ping(context.Background(), hung, 300*time.Millisecond)
	if a.Status == b.Status {
		t.Fatalf("a responsive app and a wedged one both reported %q — the probe distinguishes nothing", a.Status)
	}
	if a.Status != StatusResponding || b.Status != StatusNotResponding {
		t.Fatalf("got (%q, %q), want (%q, %q)", a.Status, b.Status, StatusResponding, StatusNotResponding)
	}
}

// A stalled X server must NOT be reported as a stalled application. Every app
// on that display is silent and all of them may be healthy; force-quitting one
// destroys work and does not unfreeze the picture.
func TestProbeX11Ping_StalledServerIsNotBlamedOnTheApp(t *testing.T) {
	f := oneAppDisplay(false)
	// Answer the two InternAtoms and the first round trip, then wedge. The
	// probe gets far enough to have a window to ping and then loses the server,
	// which is the ordering that most looks like an unresponsive app.
	f.stallAfter = 4
	sock := startFakeX(t, f)

	got := ProbeX11Ping(context.Background(), sock, 300*time.Millisecond)
	if got.Status == StatusNotResponding {
		t.Fatalf("a wedged X SERVER was reported as %q — that badge tells the user to kill a "+
			"healthy app (detail: %s)", got.Status, got.Detail)
	}
	if got.Status != StatusDisplayNotResponding {
		t.Fatalf("Status = %q, want %q (detail: %s)", got.Status, StatusDisplayNotResponding, got.Detail)
	}
	if !strings.Contains(got.Detail, "display") && !strings.Contains(got.Detail, "X server") {
		t.Errorf("detail %q does not name the display as the subject", got.Detail)
	}
}

// A server that stops answering before the probe has even found a window is the
// same fault at a different moment, and must get the same name.
func TestProbeX11Ping_StalledServerDuringSetupIsDisplayNotResponding(t *testing.T) {
	f := oneAppDisplay(true)
	f.stallAfter = 1
	sock := startFakeX(t, f)

	got := ProbeX11Ping(context.Background(), sock, 300*time.Millisecond)
	if got.Status != StatusDisplayNotResponding {
		t.Fatalf("Status = %q, want %q (detail: %s)", got.Status, StatusDisplayNotResponding, got.Detail)
	}
}

func TestProbeX11Ping_NoServerAtAll(t *testing.T) {
	got := ProbeX11Ping(context.Background(), "/tmp/definitely-not-an-x-socket-9871", 300*time.Millisecond)
	if got.Status != StatusDisplayNotResponding {
		t.Fatalf("Status = %q, want %q (detail: %s)", got.Status, StatusDisplayNotResponding, got.Detail)
	}
	if got.Status == StatusNotResponding {
		t.Error("an absent display must not be reported as an unresponsive app")
	}
}

// An app that does not implement _NET_WM_PING — a raw-Xlib game, an SDL1
// client, or any app in the moment before it maps a window — is UNKNOWN, never
// frozen. This is the honest branch, and the one a lazier implementation would
// quietly fold into not_responding to make the feature look complete.
func TestProbeX11Ping_UnknownWhenNothingImplementsThePing(t *testing.T) {
	f := &fakeX{
		tree: map[uint32][]uint32{
			fakeRootWindow: {10},
			10:             {20},
		},
		pingable: map[uint32]bool{}, // windows exist; none speak the protocol
		echo:     true,
	}
	sock := startFakeX(t, f)

	got := ProbeX11Ping(context.Background(), sock, 300*time.Millisecond)
	if got.Status != StatusUnknown {
		t.Fatalf("Status = %q, want %q (detail: %s)", got.Status, StatusUnknown, got.Detail)
	}
	if !strings.Contains(got.Detail, "_NET_WM_PING") {
		t.Errorf("detail %q does not say what was missing", got.Detail)
	}
	if !strings.Contains(got.Detail, "not the same as frozen") {
		t.Errorf("detail %q does not distinguish 'cannot ask' from 'asked and got nothing'", got.Detail)
	}
}

// An empty display — the app has not created any window yet.
func TestProbeX11Ping_UnknownWhenTheDisplayIsEmpty(t *testing.T) {
	sock := startFakeX(t, &fakeX{tree: map[uint32][]uint32{fakeRootWindow: {}}})

	got := ProbeX11Ping(context.Background(), sock, 300*time.Millisecond)
	if got.Status != StatusUnknown {
		t.Fatalf("Status = %q, want %q (detail: %s)", got.Status, StatusUnknown, got.Detail)
	}
}

// The echo is correlated to the window that was pinged. An echo naming some
// other window is not evidence about the app that was asked, and accepting it
// would make the probe report "responding" on the strength of an unrelated
// message.
func TestProbeX11Ping_IgnoresAnEchoForAWindowItNeverPinged(t *testing.T) {
	f := oneAppDisplay(true)
	f.wrongWindow = 0x999
	sock := startFakeX(t, f)

	got := ProbeX11Ping(context.Background(), sock, 300*time.Millisecond)
	if got.Status == StatusResponding {
		t.Fatalf("an echo for an unpinged window was accepted as proof of life (detail: %s)", got.Detail)
	}
	if got.Status != StatusNotResponding {
		t.Fatalf("Status = %q, want %q", got.Status, StatusNotResponding)
	}
}

// The window-tree descent, asserted on its own: WM_PROTOCOLS lives on the
// client window, which a reparenting window manager buries under a frame.
func TestProbeX11Ping_FindsTheClientWindowUnderAWindowManagerFrame(t *testing.T) {
	f := &fakeX{
		tree: map[uint32][]uint32{
			fakeRootWindow: {10},
			10:             {11},
			11:             {12},
			12:             {20},
		},
		pingable: map[uint32]bool{20: true},
		echo:     true,
	}
	sock := startFakeX(t, f)

	got := ProbeX11Ping(context.Background(), sock, time.Second)
	if got.Status != StatusResponding {
		t.Fatalf("Status = %q, want %q — the client window four levels down was not found (detail: %s)",
			got.Status, StatusResponding, got.Detail)
	}
}

// The walk must not run away on a display whose tree is deeper than the cap.
func TestProbeX11Ping_WalkIsDepthBounded(t *testing.T) {
	tree := map[uint32][]uint32{}
	var w uint32 = fakeRootWindow
	for i := 0; i < 40; i++ {
		tree[w] = []uint32{w + 1}
		w++
	}
	f := &fakeX{tree: tree, pingable: map[uint32]bool{w: true}, echo: true}
	sock := startFakeX(t, f)

	got := ProbeX11Ping(context.Background(), sock, time.Second)
	// The pingable window is deliberately below the depth cap, so the honest
	// answer is "nothing found", not a hang and not a verdict.
	if got.Status != StatusUnknown {
		t.Fatalf("Status = %q, want %q (detail: %s)", got.Status, StatusUnknown, got.Detail)
	}
}

// StatusDisplayNotResponding is a measurement of the DISPLAY. It must never be
// produced by any of the constructors that speak about an app, and the app-level
// statuses must never be produced by a display failure.
func TestDisplayNotRespondingIsItsOwnStatus(t *testing.T) {
	if StatusDisplayNotResponding == StatusNotResponding {
		t.Fatal("the display status collapsed onto the app status; a stalled display would " +
			"render as a frozen app")
	}
	for name, r := range map[string]Responsiveness{
		"StreamUnknown": StreamUnknown(),
		"StreamWayland": StreamWayland(),
		"Builtin":       BuiltinNotApplicable(),
		"StateNote(D)":  StateNote("D"),
	} {
		if r.Status == StatusDisplayNotResponding {
			t.Errorf("%s claims a display measurement it never made", name)
		}
	}
}

// The Wayland answer must stay an honest unknown and must say why, because
// "unknown with no reason" is indistinguishable from a bug.
func TestStreamWaylandExplainsTheGapItCannotClose(t *testing.T) {
	got := StreamWayland()
	if got.Status != StatusUnknown {
		t.Fatalf("Status = %q, want %q", got.Status, StatusUnknown)
	}
	for _, want := range []string{"compositor", "cage"} {
		if !strings.Contains(got.Detail, want) {
			t.Errorf("detail %q does not mention %q", got.Detail, want)
		}
	}
}
