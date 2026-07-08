package stream

// input_bound.go — GAME-INPUT-01: the input-passthrough sanitization boundary.
//
// The gaming/streaming session accepts controller/gamepad + keyboard/mouse
// events from a REMOTE client over WebRTC data channels and injects them into
// the sandboxed cage (Xvfb/Wayland) via the uinput injector. That injector
// drives the *host* kernel's input subsystem, so an unvalidated event stream is
// a direct escape surface: a malicious client could try to
//
//   - warp the mouse far outside the framebuffer (into other overlays / off
//     screen where a real cursor could land on host chrome),
//   - press key/button/axis codes the virtual device never advertised (the
//     injector maps these anyway — bounding here is defence-in-depth),
//   - drive NaN/Inf/huge float axis values that overflow the int32 the kernel
//     ABS event carries,
//   - flood events to pin a CPU or wedge the uinput fd.
//
// Nothing downstream re-validates: handleMouse/handleKeyboard/handleGamepad in
// stream.go call the injector directly. So THIS is the single choke point where
// every remote input event is clamped/validated BEFORE it reaches the injector.
//
// Design contract (why this is cage-safe):
//   - Coordinates are clamped to the session framebuffer [0,W-1]x[0,H-1]; a
//     remote peer can never move the virtual pointer outside the streamed
//     surface.  (Relative deltas are clamped to a per-event magnitude so a
//     single event can't teleport across the screen either.)
//   - Buttons/keys/axes are range-checked against the fixed set the virtual
//     device advertised (mouse: 0..2; gamepad axes -1..1, triggers 0..1,
//     button index 0..GamepadMaxButtons).  Out-of-range → dropped, not injected.
//   - Float axis values are rejected if non-finite (NaN/Inf) and otherwise
//     clamped, so the int32 conversion in the injector can never overflow.
//   - The virtual devices only expose mouse+keyboard+gamepad event codes; there
//     is no path from an input event to arbitrary host command execution — the
//     bounding here keeps every event inside that already-narrow envelope.
//
// This file is pure (no syscalls, no I/O) so it is fully unit-testable without
// GPU hardware or a uinput device.

import "math"

// Input bounding limits. These are deliberately conservative and match the
// virtual-device capabilities declared in services/input/uinput.go.
const (
	// GamepadMaxButtons bounds the Gamepad API button index we will forward.
	// The standard mapping tops out at index 16 (Guide); we accept up to 31 so
	// non-standard pads still work, but reject anything absurd that could be a
	// probe/overflow attempt.
	GamepadMaxButtons = 32
	// GamepadMaxAxes bounds analog stick axis indices (0..3 = LX,LY,RX,RY).
	GamepadMaxAxes = 8
	// GamepadMaxTriggers bounds trigger indices (0..1 = LT,RT).
	GamepadMaxTriggers = 4
	// MaxRelDelta caps the magnitude of a single relative mouse move (pixels).
	// A pointer-lock movementX/Y is normally small; a huge value would let one
	// event fling the pointer across many virtual screens. Clamp per-event.
	MaxRelDelta = 400
	// MaxScrollClicks caps a single scroll event magnitude.
	MaxScrollClicks = 16
)

// clampInt returns v clamped to [lo, hi].
func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// boundAbs clamps an absolute pointer coordinate to the session framebuffer.
// w/h are the current session Width/Height. A degenerate (0) dimension falls
// back to a 1px axis so we never produce a negative upper bound.
func boundAbs(x, y, w, h int) (int, int) {
	if w < 1 {
		w = 1
	}
	if h < 1 {
		h = 1
	}
	return clampInt(x, 0, w-1), clampInt(y, 0, h-1)
}

// boundRel clamps a relative pointer delta to +/- MaxRelDelta per event, and
// also rejects non-finite inputs by treating them as zero (the caller passes
// already-int deltas, but the float-source variant below guards NaN/Inf first).
func boundRel(dx, dy int) (int, int) {
	return clampInt(dx, -MaxRelDelta, MaxRelDelta), clampInt(dy, -MaxRelDelta, MaxRelDelta)
}

// finiteOrZero returns v if it is a finite number, else 0. Guards NaN/Inf from
// a hostile client before any float→int conversion in the injector (which would
// otherwise be an undefined/overflow conversion).
func finiteOrZero(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	return v
}

// boundMouseButton validates a mouse button index. The virtual mouse advertises
// exactly three buttons (left=0, middle=1, right=2). Returns (index, ok); ok is
// false for anything outside that set so the caller drops the event.
func boundMouseButton(b int) (int, bool) {
	if b < 0 || b > 2 {
		return 0, false
	}
	return b, true
}

// boundScroll clamps a scroll magnitude to +/- MaxScrollClicks.
func boundScroll(clicks int) int {
	return clampInt(clicks, -MaxScrollClicks, MaxScrollClicks)
}

// boundGamepadButton validates a gamepad button index against GamepadMaxButtons.
// Returns ok=false when out of range so the caller drops the event.
func boundGamepadButton(index int) (int, bool) {
	if index < 0 || index >= GamepadMaxButtons {
		return 0, false
	}
	return index, true
}

// boundGamepadAxis validates an analog-axis index and clamps its value to the
// gamepad stick range [-1, 1] after rejecting non-finite values. Returns
// (index, value, ok); ok=false → drop.
func boundGamepadAxis(index int, value float64) (int, float64, bool) {
	if index < 0 || index >= GamepadMaxAxes {
		return 0, 0, false
	}
	v := finiteOrZero(value)
	if v < -1 {
		v = -1
	} else if v > 1 {
		v = 1
	}
	return index, v, true
}

// boundGamepadTrigger validates a trigger index and clamps its value to [0, 1]
// after rejecting non-finite values. Returns (index, value, ok); ok=false → drop.
func boundGamepadTrigger(index int, value float64) (int, float64, bool) {
	if index < 0 || index >= GamepadMaxTriggers {
		return 0, 0, false
	}
	v := finiteOrZero(value)
	if v < 0 {
		v = 0
	} else if v > 1 {
		v = 1
	}
	return index, v, true
}

// MaxKeyBytes bounds the byte-length of a JS key/code string we will forward to
// the injector's keymap. A well-formed key/code is short ("Enter", "KeyA",
// "ArrowLeft", "F12"). An over-long string is a probe/DoS attempt: the keymap
// lookup drops unknowns anyway, but we cap the length so a client cannot ship a
// megabyte "key" to churn map lookups/allocations.
const MaxKeyBytes = 32

// boundKeyString reports whether a key/code string is within the accepted size
// bound. The injector's jsToLinuxKey already drops unknown names, so this is a
// cheap length guard against oversized/garbage input rather than a whitelist.
func boundKeyString(s string) bool {
	return len(s) <= MaxKeyBytes
}

// validModBits is the mask of modifier bits the protocol defines (shift, ctrl,
// alt, meta, capslock = 1|2|4|8|16 = 31). Any bit outside this mask is stripped
// so a client cannot smuggle unexpected state through SyncModifiers.
const validModBits = modShift | modCtrl | modAlt | modMeta | modCapsLock

// boundMod strips a modifier bitmask down to the bits the protocol defines.
func boundMod(mod int) int {
	return mod & validModBits
}
