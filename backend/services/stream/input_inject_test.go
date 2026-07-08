package stream

import (
	"sync"
	"testing"

	"vulos/backend/services/input"
)

// recInjector records every injected event so tests can assert the handlers
// clamp/validate remote input (GAME-INPUT-01) BEFORE injection.
type recInjector struct {
	mu       sync.Mutex
	absMoves [][2]int
	relMoves [][2]int
	buttons  []recBtn
	scrolls  []int
	keys     []recKey
	mods     []int
	gpBtn    []recGpBtn
	gpAxis   []recGpAxis
	gpTrig   []recGpAxis
}

type recBtn struct {
	button  int
	pressed bool
}
type recKey struct {
	key, code string
	pressed   bool
}
type recGpBtn struct {
	index   int
	pressed bool
}
type recGpAxis struct {
	index int
	value float64
}

func (r *recInjector) MouseMove(x, y int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.absMoves = append(r.absMoves, [2]int{x, y})
}
func (r *recInjector) MouseMoveRel(dx, dy int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.relMoves = append(r.relMoves, [2]int{dx, dy})
}
func (r *recInjector) MouseButton(button int, pressed bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buttons = append(r.buttons, recBtn{button, pressed})
}
func (r *recInjector) Scroll(clicks int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.scrolls = append(r.scrolls, clicks)
}
func (r *recInjector) SyncModifiers(clientMod int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.mods = append(r.mods, clientMod)
}
func (r *recInjector) KeyPress(jsKey, jsCode string, pressed bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.keys = append(r.keys, recKey{jsKey, jsCode, pressed})
}
func (r *recInjector) GamepadButton(index int, pressed bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.gpBtn = append(r.gpBtn, recGpBtn{index, pressed})
}
func (r *recInjector) GamepadAxis(index int, value float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.gpAxis = append(r.gpAxis, recGpAxis{index, value})
}
func (r *recInjector) GamepadTrigger(index int, value float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.gpTrig = append(r.gpTrig, recGpAxis{index, value})
}
func (r *recInjector) RumbleChan() <-chan input.RumbleEvent { return nil }
func (r *recInjector) Close()                               {}

// newRecSession builds a Session wired to a recording injector, at a known
// framebuffer size, NOT gated. No processes are launched.
func newRecSession(w, h int) (*Session, *recInjector) {
	rec := &recInjector{}
	s := &Session{
		ID:       "rec",
		Width:    w,
		Height:   h,
		Running:  true,
		injector: rec,
	}
	return s, rec
}

// TestHandleMouse_ClampsAbsoluteMove is the headline cage-safety test: an
// off-surface absolute move is clamped to the framebuffer before injection.
func TestHandleMouse_ClampsAbsoluteMove(t *testing.T) {
	s, rec := newRecSession(1920, 1080)
	s.handleMouse([]byte(`{"t":"mm","x":50000,"y":-999}`))
	if len(rec.absMoves) != 1 {
		t.Fatalf("want 1 abs move, got %d", len(rec.absMoves))
	}
	if rec.absMoves[0] != [2]int{1919, 0} {
		t.Fatalf("off-surface move not clamped: got %v want [1919 0]", rec.absMoves[0])
	}
}

// TestHandleMouse_RejectsBadButton ensures an out-of-range button index never
// reaches the injector (no arbitrary button code injection).
func TestHandleMouse_RejectsBadButton(t *testing.T) {
	s, rec := newRecSession(800, 600)
	s.handleMouse([]byte(`{"t":"md","x":10,"y":10,"b":99}`))
	if len(rec.buttons) != 0 {
		t.Fatalf("bad button index was injected: %+v", rec.buttons)
	}
	// A valid button DOES pass, with position clamped.
	s.handleMouse([]byte(`{"t":"md","x":10,"y":10,"b":2}`))
	if len(rec.buttons) != 1 || rec.buttons[0] != (recBtn{2, true}) {
		t.Fatalf("valid button not injected: %+v", rec.buttons)
	}
}

// TestHandleMouse_ClampsRelativeAndScroll bounds relative deltas + scroll.
func TestHandleMouse_ClampsRelativeAndScroll(t *testing.T) {
	s, rec := newRecSession(1920, 1080)
	s.handleMouse([]byte(`{"t":"mr","dx":100000,"dy":-100000}`))
	if len(rec.relMoves) != 1 || rec.relMoves[0] != [2]int{MaxRelDelta, -MaxRelDelta} {
		t.Fatalf("relative move not clamped: %+v", rec.relMoves)
	}
	s.handleMouse([]byte(`{"t":"sc","y":9999}`))
	if len(rec.scrolls) != 1 || rec.scrolls[0] != MaxScrollClicks {
		t.Fatalf("scroll not clamped: %+v", rec.scrolls)
	}
}

// TestHandleKeyboard_StripsModsAndDropsOversizedKey verifies the modifier
// bitmask is masked and an oversized key is dropped.
func TestHandleKeyboard_StripsModsAndDropsOversizedKey(t *testing.T) {
	s, rec := newRecSession(800, 600)
	// Modifier with an undefined high bit set → masked to validModBits.
	s.handleKeyboard([]byte(`{"t":"kd","key":"a","code":"KeyA","mod":65535}`))
	if len(rec.mods) != 1 || rec.mods[0] != validModBits {
		t.Fatalf("modifier not masked: %+v", rec.mods)
	}
	if len(rec.keys) != 1 {
		t.Fatalf("valid key not injected: %+v", rec.keys)
	}

	// Oversized key string → dropped before injection.
	big := make([]byte, MaxKeyBytes+5)
	for i := range big {
		big[i] = 'x'
	}
	s.handleKeyboard([]byte(`{"t":"kd","key":"` + string(big) + `","code":"KeyA","mod":0}`))
	if len(rec.keys) != 1 { // unchanged
		t.Fatalf("oversized key was injected: %+v", rec.keys)
	}
}

// TestHandleGamepad_BoundsButtonsAxesTriggers ensures out-of-range indices are
// dropped and NaN/over-range analog values are sanitized before injection.
func TestHandleGamepad_BoundsButtonsAxesTriggers(t *testing.T) {
	s, rec := newRecSession(800, 600)
	// 40 buttons (> GamepadMaxButtons=32): only indices [0,32) inject.
	buttons := "["
	for i := 0; i < 40; i++ {
		if i > 0 {
			buttons += ","
		}
		buttons += "true"
	}
	buttons += "]"
	// Axes: over-range 5.0 → 1, NaN handled; triggers -1 → 0.
	payload := `{"buttons":` + buttons + `,"axes":[5.0,-9.0],"triggers":[2.0,-1.0]}`
	s.handleGamepad([]byte(payload))

	if len(rec.gpBtn) != GamepadMaxButtons {
		t.Fatalf("gamepad buttons not bounded: injected %d, want %d", len(rec.gpBtn), GamepadMaxButtons)
	}
	if len(rec.gpAxis) != 2 || rec.gpAxis[0].value != 1 || rec.gpAxis[1].value != -1 {
		t.Fatalf("gamepad axes not clamped: %+v", rec.gpAxis)
	}
	if len(rec.gpTrig) != 2 || rec.gpTrig[0].value != 1 || rec.gpTrig[1].value != 0 {
		t.Fatalf("gamepad triggers not clamped: %+v", rec.gpTrig)
	}
}

// TestHandleInput_GatedDropsEverything verifies the AUTH-13 gate blocks all
// injected input while armed (defence-in-depth alongside the transport binding).
func TestHandleInput_GatedDropsEverything(t *testing.T) {
	s, rec := newRecSession(800, 600)
	s.inputGated = true
	s.handleMouse([]byte(`{"t":"mm","x":10,"y":10}`))
	s.handleKeyboard([]byte(`{"t":"kd","key":"a","code":"KeyA"}`))
	s.handleGamepad([]byte(`{"buttons":[true]}`))
	s.handleInput([]byte(`{"type":"mousemove","x":10,"y":10}`))
	if len(rec.absMoves)+len(rec.keys)+len(rec.gpBtn) != 0 {
		t.Fatalf("gated session injected input: abs=%d keys=%d gp=%d",
			len(rec.absMoves), len(rec.keys), len(rec.gpBtn))
	}
}
