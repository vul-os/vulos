package stream

import (
	"math"
	"testing"
)

// TestBoundAbs_ClampsToFramebuffer verifies absolute pointer coords are clamped
// inside [0,W-1]x[0,H-1] — a remote peer cannot warp the pointer off-surface.
func TestBoundAbs_ClampsToFramebuffer(t *testing.T) {
	const w, h = 1920, 1080
	cases := []struct {
		x, y, wantX, wantY int
	}{
		{100, 200, 100, 200},         // in-bounds unchanged
		{-50, -1, 0, 0},              // negative → 0
		{99999, 99999, w - 1, h - 1}, // huge → clamped to max
		{1920, 1080, w - 1, h - 1},   // exactly at size → last pixel
		{0, 0, 0, 0},                 // origin
	}
	for _, c := range cases {
		gx, gy := boundAbs(c.x, c.y, w, h)
		if gx != c.wantX || gy != c.wantY {
			t.Errorf("boundAbs(%d,%d,%d,%d)=(%d,%d) want (%d,%d)",
				c.x, c.y, w, h, gx, gy, c.wantX, c.wantY)
		}
	}
}

// TestBoundAbs_DegenerateFramebuffer ensures a 0-sized framebuffer never yields
// a negative upper bound (no panic / negative clamp).
func TestBoundAbs_DegenerateFramebuffer(t *testing.T) {
	gx, gy := boundAbs(500, 500, 0, 0)
	if gx != 0 || gy != 0 {
		t.Fatalf("degenerate framebuffer: got (%d,%d), want (0,0)", gx, gy)
	}
}

// TestBoundRel_ClampsMagnitude ensures a single relative move cannot fling the
// pointer across many virtual screens.
func TestBoundRel_ClampsMagnitude(t *testing.T) {
	dx, dy := boundRel(100000, -100000)
	if dx != MaxRelDelta || dy != -MaxRelDelta {
		t.Fatalf("boundRel huge: got (%d,%d), want (%d,%d)", dx, dy, MaxRelDelta, -MaxRelDelta)
	}
	dx, dy = boundRel(5, -5)
	if dx != 5 || dy != -5 {
		t.Fatalf("boundRel small: got (%d,%d), want (5,-5)", dx, dy)
	}
}

// TestFiniteOrZero rejects NaN/Inf before any float→int conversion.
func TestFiniteOrZero(t *testing.T) {
	if finiteOrZero(math.NaN()) != 0 {
		t.Error("NaN should map to 0")
	}
	if finiteOrZero(math.Inf(1)) != 0 || finiteOrZero(math.Inf(-1)) != 0 {
		t.Error("Inf should map to 0")
	}
	if finiteOrZero(3.5) != 3.5 {
		t.Error("finite value should pass through")
	}
}

// TestBoundMouseButton rejects out-of-range button indices (the virtual mouse
// only advertises left/middle/right).
func TestBoundMouseButton(t *testing.T) {
	for _, b := range []int{0, 1, 2} {
		if _, ok := boundMouseButton(b); !ok {
			t.Errorf("button %d should be valid", b)
		}
	}
	for _, b := range []int{-1, 3, 999, math.MaxInt32} {
		if _, ok := boundMouseButton(b); ok {
			t.Errorf("button %d should be rejected", b)
		}
	}
}

// TestBoundGamepadButton bounds the button index to the advertised range.
func TestBoundGamepadButton(t *testing.T) {
	if _, ok := boundGamepadButton(0); !ok {
		t.Error("index 0 should be valid")
	}
	if _, ok := boundGamepadButton(GamepadMaxButtons - 1); !ok {
		t.Error("max-1 should be valid")
	}
	for _, i := range []int{-1, GamepadMaxButtons, 100000} {
		if _, ok := boundGamepadButton(i); ok {
			t.Errorf("index %d should be rejected", i)
		}
	}
}

// TestBoundGamepadAxis clamps analog values to [-1,1] and rejects NaN/Inf and
// out-of-range indices.
func TestBoundGamepadAxis(t *testing.T) {
	if _, v, ok := boundGamepadAxis(0, 2.5); !ok || v != 1 {
		t.Errorf("axis over-range: ok=%v v=%v, want ok=true v=1", ok, v)
	}
	if _, v, ok := boundGamepadAxis(1, -9.0); !ok || v != -1 {
		t.Errorf("axis under-range: ok=%v v=%v, want ok=true v=-1", ok, v)
	}
	if _, v, ok := boundGamepadAxis(2, math.NaN()); !ok || v != 0 {
		t.Errorf("axis NaN: ok=%v v=%v, want ok=true v=0", ok, v)
	}
	if _, _, ok := boundGamepadAxis(GamepadMaxAxes, 0.1); ok {
		t.Error("out-of-range axis index should be rejected")
	}
}

// TestBoundGamepadTrigger clamps triggers to [0,1].
func TestBoundGamepadTrigger(t *testing.T) {
	if _, v, ok := boundGamepadTrigger(0, 5.0); !ok || v != 1 {
		t.Errorf("trigger over-range: ok=%v v=%v", ok, v)
	}
	if _, v, ok := boundGamepadTrigger(1, -1.0); !ok || v != 0 {
		t.Errorf("trigger negative should clamp to 0: ok=%v v=%v", ok, v)
	}
	if _, _, ok := boundGamepadTrigger(GamepadMaxTriggers, 0.1); ok {
		t.Error("out-of-range trigger index should be rejected")
	}
}

// TestBoundKeyString rejects oversized key/code strings (probe/DoS guard).
func TestBoundKeyString(t *testing.T) {
	if !boundKeyString("Enter") || !boundKeyString("KeyA") {
		t.Error("normal keys should be accepted")
	}
	big := make([]byte, MaxKeyBytes+1)
	for i := range big {
		big[i] = 'x'
	}
	if boundKeyString(string(big)) {
		t.Error("oversized key string should be rejected")
	}
}

// TestBoundMod strips modifier bits outside the defined mask.
func TestBoundMod(t *testing.T) {
	// All defined bits pass.
	if got := boundMod(validModBits); got != validModBits {
		t.Errorf("all defined bits: got %d, want %d", got, validModBits)
	}
	// A bit outside the mask is stripped.
	extra := validModBits | 0x8000
	if got := boundMod(extra); got != validModBits {
		t.Errorf("extra bit not stripped: got %d, want %d", got, validModBits)
	}
	// -1 (all bits) collapses to just the defined mask.
	if got := boundMod(-1); got != validModBits {
		t.Errorf("boundMod(-1)=%d, want %d", got, validModBits)
	}
}
