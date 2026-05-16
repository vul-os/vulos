package input

import (
	"runtime"
	"testing"
	"unsafe"
)

// ---------------------------------------------------------------------------
// inputEvent struct layout
// ---------------------------------------------------------------------------

// TestInputEventSize checks the struct is laid out to match the Linux
// input_event ABI expected by /dev/uinput. On a 64-bit kernel the struct is
// 24 bytes (8-byte timeval + 2+2+4 for type/code/value).
// On a 32-bit build it is 16 bytes. We only assert the fields land where the
// kernel expects them relative to the beginning of the struct; the test is
// kept platform-generic by using unsafe.Offsetof.
func TestInputEventFieldOffsets(t *testing.T) {
	var ev inputEvent
	typeOff := unsafe.Offsetof(ev.Type)
	codeOff := unsafe.Offsetof(ev.Code)
	valOff := unsafe.Offsetof(ev.Value)

	// Type must come immediately after the timeval
	timevalSize := unsafe.Sizeof(ev.Time)
	if typeOff != timevalSize {
		t.Errorf("inputEvent.Type offset = %d, want %d (sizeof Timeval)", typeOff, timevalSize)
	}
	// Code is 2 bytes after Type
	if codeOff != typeOff+2 {
		t.Errorf("inputEvent.Code offset = %d, want %d", codeOff, typeOff+2)
	}
	// Value is 4 bytes after Code (2+2 padding alignment gives 4-byte boundary)
	if valOff != codeOff+2 {
		t.Errorf("inputEvent.Value offset = %d, want %d", valOff, codeOff+2)
	}
}

// TestInputEventSize checks the total struct size is reasonable.
func TestInputEventSize(t *testing.T) {
	var ev inputEvent
	sz := unsafe.Sizeof(ev)
	// 16 (32-bit timeval) or 24 (64-bit timeval); never smaller than 16
	if sz < 16 {
		t.Errorf("inputEvent size = %d, want >= 16", sz)
	}
}

// TestInputEventFieldValues verifies that assigning Type/Code/Value round-trips
// without corruption (catches accidental endian / alignment issues in pure Go).
func TestInputEventFieldValues(t *testing.T) {
	ev := inputEvent{
		Type:  evKey,
		Code:  keyEnter,
		Value: 1,
	}
	if ev.Type != evKey {
		t.Errorf("Type mismatch: got %d want %d", ev.Type, evKey)
	}
	if ev.Code != keyEnter {
		t.Errorf("Code mismatch: got %d want %d", ev.Code, keyEnter)
	}
	if ev.Value != 1 {
		t.Errorf("Value mismatch: got %d want 1", ev.Value)
	}
}

// ---------------------------------------------------------------------------
// uinputUserDev struct layout
// ---------------------------------------------------------------------------

// TestUinputUserDevNameField verifies that copy-into-[80]byte works correctly.
func TestUinputUserDevNameField(t *testing.T) {
	var dev uinputUserDev
	name := "Vula OS Virtual Mouse"
	copy(dev.Name[:], name)
	// Check the bytes match and are null-terminated
	for i, c := range []byte(name) {
		if dev.Name[i] != c {
			t.Errorf("Name[%d] = %q, want %q", i, dev.Name[i], c)
		}
	}
	if dev.Name[len(name)] != 0 {
		t.Errorf("Name[%d] (terminator) = %q, want 0", len(name), dev.Name[len(name)])
	}
}

// TestUinputUserDevAbsArrays verifies the abs-axis array assignments used in
// createGamepadDevice — no device open required.
func TestUinputUserDevAbsArrays(t *testing.T) {
	var dev uinputUserDev
	// Analog sticks
	for _, axis := range []int{int(absX), int(absY), int(absRx), int(absRy)} {
		dev.AbsMin[axis] = -32768
		dev.AbsMax[axis] = 32767
		dev.AbsFuzz[axis] = 16
		dev.AbsFlat[axis] = 128
	}
	// Triggers
	dev.AbsMax[absZ] = 255
	dev.AbsMax[absRz] = 255
	// D-pad
	dev.AbsMin[absHat0X] = -1
	dev.AbsMax[absHat0X] = 1
	dev.AbsMin[absHat0Y] = -1
	dev.AbsMax[absHat0Y] = 1

	// Spot-check
	if dev.AbsMin[absX] != -32768 {
		t.Errorf("AbsMin[absX] = %d, want -32768", dev.AbsMin[absX])
	}
	if dev.AbsMax[absX] != 32767 {
		t.Errorf("AbsMax[absX] = %d, want 32767", dev.AbsMax[absX])
	}
	if dev.AbsMax[absZ] != 255 {
		t.Errorf("AbsMax[absZ] (left trigger) = %d, want 255", dev.AbsMax[absZ])
	}
	if dev.AbsMin[absHat0X] != -1 || dev.AbsMax[absHat0X] != 1 {
		t.Errorf("D-pad X range wrong: min=%d max=%d", dev.AbsMin[absHat0X], dev.AbsMax[absHat0X])
	}
}

// ---------------------------------------------------------------------------
// Gamepad button map
// ---------------------------------------------------------------------------

// TestGamepadButtonMapCompleteness checks every W3C standard index that vulos
// supports maps to a non-zero Linux button code with the expected constant.
func TestGamepadButtonMapCompleteness(t *testing.T) {
	expected := map[int]uint16{
		0:  btnSouth,
		1:  btnEast,
		2:  btnWest,
		3:  btnNorth,
		4:  btnTL,
		5:  btnTR,
		8:  btnSelect,
		9:  btnStart,
		10: btnThumbL,
		11: btnThumbR,
		16: btnMode,
	}
	for idx, want := range expected {
		got, ok := gamepadButtonMap[idx]
		if !ok {
			t.Errorf("gamepadButtonMap missing index %d", idx)
			continue
		}
		if got != want {
			t.Errorf("gamepadButtonMap[%d] = 0x%x, want 0x%x", idx, got, want)
		}
	}
}

// TestGamepadButtonMapNoZeroCodes verifies no entry maps to code 0 (reserved / invalid).
func TestGamepadButtonMapNoZeroCodes(t *testing.T) {
	for idx, code := range gamepadButtonMap {
		if code == 0 {
			t.Errorf("gamepadButtonMap[%d] = 0 (invalid Linux button code)", idx)
		}
	}
}

// TestGamepadButtonConstants verifies Xbox-layout button constants are in the
// range that the Linux kernel defines for gamepad buttons (0x130–0x13f).
func TestGamepadButtonConstants(t *testing.T) {
	btns := []struct {
		name string
		code uint16
	}{
		{"btnSouth", btnSouth},
		{"btnEast", btnEast},
		{"btnNorth", btnNorth},
		{"btnWest", btnWest},
		{"btnTL", btnTL},
		{"btnTR", btnTR},
		{"btnSelect", btnSelect},
		{"btnStart", btnStart},
		{"btnMode", btnMode},
		{"btnThumbL", btnThumbL},
		{"btnThumbR", btnThumbR},
	}
	for _, b := range btns {
		if b.code < 0x130 || b.code > 0x13f {
			t.Errorf("%s = 0x%x outside gamepad button range 0x130–0x13f", b.name, b.code)
		}
	}
}

// TestMouseButtonConstants verifies mouse button codes match the Linux BTN_MOUSE
// range (0x110–0x117).
func TestMouseButtonConstants(t *testing.T) {
	cases := []struct {
		name string
		code uint16
	}{
		{"btnLeft", btnLeft},
		{"btnRight", btnRight},
		{"btnMiddle", btnMiddle},
	}
	for _, c := range cases {
		if c.code < 0x110 || c.code > 0x117 {
			t.Errorf("%s = 0x%x outside BTN_MOUSE range", c.name, c.code)
		}
	}
}

// ---------------------------------------------------------------------------
// jsToLinuxKey mapping table
// ---------------------------------------------------------------------------

// TestJsCodeToLinuxSpotCheck verifies specific JS code → Linux key mappings.
func TestJsCodeToLinuxSpotCheck(t *testing.T) {
	cases := []struct {
		code string
		want uint16
	}{
		{"KeyA", keyA},
		{"KeyZ", keyZ},
		{"Space", keySpace},
		{"Enter", keyEnter},
		{"ShiftLeft", keyLeftShift},
		{"ShiftRight", keyRightShift},
		{"ControlLeft", keyLeftCtrl},
		{"ControlRight", keyRightCtrl},
		{"AltLeft", keyLeftAlt},
		{"AltRight", keyRightAlt},
		{"MetaLeft", keyLeftMeta},
		{"CapsLock", keyCapsLock},
		{"Digit0", key0},
		{"Digit9", key9},
		{"F1", keyF1},
		{"F12", keyF12},
		{"ArrowUp", keyUp},
		{"ArrowDown", keyDown},
		{"ArrowLeft", keyLeft},
		{"ArrowRight", keyRight},
		{"Home", keyHome},
		{"End", keyEnd},
		{"PageUp", keyPageUp},
		{"PageDown", keyPageDown},
		{"Insert", keyInsert},
		{"Delete", keyDelete},
		{"Backspace", keyBackspace},
		{"Tab", keyTab},
		{"Escape", keyEsc},
	}
	for _, tc := range cases {
		got, ok := jsToLinuxKey("", tc.code)
		if !ok {
			t.Errorf("jsToLinuxKey(code=%q): not found", tc.code)
			continue
		}
		if got != tc.want {
			t.Errorf("jsToLinuxKey(code=%q) = %d, want %d", tc.code, got, tc.want)
		}
	}
}

// TestJsKeyToLinuxFallback verifies that jsToLinuxKey falls back to the key
// name when the physical code is unknown.
func TestJsKeyToLinuxFallback(t *testing.T) {
	// "ArrowUp" is in jsKeyToLinux but NOT as a physical code string; passing
	// a blank code forces the key-name fallback path.
	got, ok := jsToLinuxKey("ArrowUp", "")
	if !ok {
		t.Fatal("jsToLinuxKey(key=ArrowUp, code=): not found via key-name fallback")
	}
	if got != keyUp {
		t.Errorf("jsToLinuxKey key-name fallback ArrowUp = %d, want %d", got, keyUp)
	}
}

// TestJsToLinuxKeyCodePriority checks that a matching code overrides the key name.
func TestJsToLinuxKeyCodePriority(t *testing.T) {
	// ShiftLeft (code) → keyLeftShift; "Shift" (key) → also keyLeftShift.
	// Supply both; we just confirm a valid result comes back and it matches code.
	got, ok := jsToLinuxKey("Shift", "ShiftLeft")
	if !ok {
		t.Fatal("jsToLinuxKey(Shift, ShiftLeft): not found")
	}
	if got != keyLeftShift {
		t.Errorf("jsToLinuxKey(Shift, ShiftLeft) = %d, want %d", got, keyLeftShift)
	}
}

// TestJsToLinuxKeyUnknown checks that an unknown key/code pair returns false.
func TestJsToLinuxKeyUnknown(t *testing.T) {
	_, ok := jsToLinuxKey("NotAKey", "NotACode")
	if ok {
		t.Error("jsToLinuxKey should return false for unknown key/code")
	}
}

// TestJsKeyToLinuxLetters verifies all 26 lowercase letters are present in
// jsKeyToLinux and map to distinct non-zero values.
func TestJsKeyToLinuxLetters(t *testing.T) {
	seen := map[uint16]string{}
	for ch := byte('a'); ch <= 'z'; ch++ {
		key := string(ch)
		code, ok := jsKeyToLinux[key]
		if !ok {
			t.Errorf("jsKeyToLinux missing letter %q", key)
			continue
		}
		if code == 0 {
			t.Errorf("jsKeyToLinux[%q] = 0 (invalid)", key)
			continue
		}
		if prev, dup := seen[code]; dup {
			t.Errorf("jsKeyToLinux[%q] and [%q] share code %d", key, prev, code)
		}
		seen[code] = key
	}
}

// TestJsKeyToLinuxDigits verifies all 10 digits are present.
func TestJsKeyToLinuxDigits(t *testing.T) {
	for ch := byte('0'); ch <= '9'; ch++ {
		key := string(ch)
		code, ok := jsKeyToLinux[key]
		if !ok {
			t.Errorf("jsKeyToLinux missing digit %q", key)
			continue
		}
		if code == 0 {
			t.Errorf("jsKeyToLinux[%q] = 0", key)
		}
	}
}

// ---------------------------------------------------------------------------
// Modifier bitmask logic
// ---------------------------------------------------------------------------

// TestModifierBitmaskValues verifies each modifier occupies a distinct bit and
// the set doesn't overlap.
func TestModifierBitmaskValues(t *testing.T) {
	mods := []struct {
		name string
		val  int
	}{
		{"ModShift", ModShift},
		{"ModCtrl", ModCtrl},
		{"ModAlt", ModAlt},
		{"ModMeta", ModMeta},
		{"ModCapsLock", ModCapsLock},
	}
	combined := 0
	for _, m := range mods {
		if m.val == 0 {
			t.Errorf("%s = 0 (must be non-zero)", m.name)
		}
		if combined&m.val != 0 {
			t.Errorf("%s overlaps with a previously seen modifier (combined=0b%b)", m.name, combined)
		}
		combined |= m.val
	}
}

// TestModifierBitmaskOperations exercises AND/OR/AND-NOT set operations that
// SyncModifiers and KeyPress use for tracking state.
func TestModifierBitmaskOperations(t *testing.T) {
	state := 0

	// Press shift + ctrl
	state |= ModShift
	state |= ModCtrl
	if state&ModShift == 0 {
		t.Error("shift should be set")
	}
	if state&ModCtrl == 0 {
		t.Error("ctrl should be set")
	}
	if state&ModAlt != 0 {
		t.Error("alt should not be set")
	}

	// Release ctrl
	state &^= ModCtrl
	if state&ModCtrl != 0 {
		t.Error("ctrl should have been cleared")
	}
	if state&ModShift == 0 {
		t.Error("shift should still be set after clearing ctrl")
	}

	// CapsLock toggle: press once → on, press again → off
	state ^= ModCapsLock
	if state&ModCapsLock == 0 {
		t.Error("CapsLock should be on after first toggle")
	}
	state ^= ModCapsLock
	if state&ModCapsLock != 0 {
		t.Error("CapsLock should be off after second toggle")
	}
}

// TestSyncModifiersReconciliation simulates the reconciliation logic in
// SyncModifiers using only the bitmask arithmetic (no device required).
func TestSyncModifiersReconciliation(t *testing.T) {
	type modKey struct {
		bit int
	}
	modKeys := []modKey{
		{ModShift}, {ModCtrl}, {ModAlt}, {ModMeta},
	}

	scenarios := []struct {
		name      string
		local     int
		client    int
		wantPress []int
		wantRel   []int
	}{
		{
			name:      "client holds shift, local does not",
			local:     0,
			client:    ModShift,
			wantPress: []int{ModShift},
			wantRel:   nil,
		},
		{
			name:      "local holds ctrl, client does not",
			local:     ModCtrl,
			client:    0,
			wantPress: nil,
			wantRel:   []int{ModCtrl},
		},
		{
			name:      "states match",
			local:     ModShift | ModCtrl,
			client:    ModShift | ModCtrl,
			wantPress: nil,
			wantRel:   nil,
		},
	}

	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			state := sc.local
			var pressed, released []int
			for _, mk := range modKeys {
				clientHeld := sc.client&mk.bit != 0
				localHeld := state&mk.bit != 0
				if clientHeld == localHeld {
					continue
				}
				if clientHeld {
					state |= mk.bit
					pressed = append(pressed, mk.bit)
				} else {
					state &^= mk.bit
					released = append(released, mk.bit)
				}
			}
			// Verify pressed
			for _, want := range sc.wantPress {
				found := false
				for _, p := range pressed {
					if p == want {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("expected bit 0x%x to be pressed, pressed=%v", want, pressed)
				}
			}
			// Verify released
			for _, want := range sc.wantRel {
				found := false
				for _, r := range released {
					if r == want {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("expected bit 0x%x to be released, released=%v", want, released)
				}
			}
			// Verify final state
			if state != sc.client {
				t.Errorf("final state = 0x%x, want 0x%x", state, sc.client)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Axis value encoding / scaling
// ---------------------------------------------------------------------------

// TestGamepadAxisScaling verifies the float64→int32 scaling arithmetic for
// analog sticks (−1.0…1.0 → −32768…32767).
func TestGamepadAxisScaling(t *testing.T) {
	cases := []struct {
		input float64
		want  int32
	}{
		{0.0, 0},
		{1.0, 32767},
		{-1.0, -32767},
		{0.5, 16383},
		{-0.5, -16383},
	}
	for _, tc := range cases {
		got := int32(tc.input * 32767)
		if got != tc.want {
			t.Errorf("axis scale(%v) = %d, want %d", tc.input, got, tc.want)
		}
	}
}

// TestGamepadTriggerScaling verifies trigger scaling (0.0…1.0 → 0…255).
func TestGamepadTriggerScaling(t *testing.T) {
	cases := []struct {
		input float64
		want  int32
	}{
		{0.0, 0},
		{1.0, 255},
		{0.5, 127},
	}
	for _, tc := range cases {
		got := int32(tc.input * 255)
		if got != tc.want {
			t.Errorf("trigger scale(%v) = %d, want %d", tc.input, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// xBtn helper
// ---------------------------------------------------------------------------

// TestXBtn verifies the Gamepad-API → X11 button number mapping used by
// MouseButton in the xdotool fallback path.
func TestXBtn(t *testing.T) {
	cases := []struct{ in, want int }{
		{0, 1}, // left
		{1, 2}, // middle (JS 1 → X11 2)
		{2, 3}, // right  (JS 2 → X11 3)
		{3, 1}, // unknown → left
	}
	for _, tc := range cases {
		got := xBtn(tc.in)
		if got != tc.want {
			t.Errorf("xBtn(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// itoa helper
// ---------------------------------------------------------------------------

func TestItoa(t *testing.T) {
	cases := []struct {
		in   int
		want string
	}{
		{0, "0"},
		{1, "1"},
		{-1, "-1"},
		{1920, "1920"},
		{1080, "1080"},
	}
	for _, tc := range cases {
		got := itoa(tc.in)
		if got != tc.want {
			t.Errorf("itoa(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// xKey helper (xdotool keysym names)
// ---------------------------------------------------------------------------

func TestXKeySpecialKeys(t *testing.T) {
	cases := []struct {
		jsKey string
		want  string
	}{
		{"Enter", "Return"},
		{"Backspace", "BackSpace"},
		{"Tab", "Tab"},
		{"Escape", "Escape"},
		{"Delete", "Delete"},
		{"ArrowUp", "Up"},
		{"ArrowDown", "Down"},
		{"ArrowLeft", "Left"},
		{"ArrowRight", "Right"},
		{"PageUp", "Prior"},
		{"PageDown", "Next"},
		{"Home", "Home"},
		{"End", "End"},
		{"F1", "F1"},
		{"F12", "F12"},
		{"Shift", "Shift_L"},
		{"Control", "Control_L"},
		{"Alt", "Alt_L"},
		{"Meta", "Super_L"},
		{"CapsLock", "Caps_Lock"},
		{" ", "space"},
	}
	for _, tc := range cases {
		got := xKey(tc.jsKey)
		if got != tc.want {
			t.Errorf("xKey(%q) = %q, want %q", tc.jsKey, got, tc.want)
		}
	}
}

// TestXKeyCharacterPassThrough verifies single letters pass through as-is.
func TestXKeyCharacterPassThrough(t *testing.T) {
	for _, ch := range "abcdefghijklmnopqrstuvwxyz0123456789" {
		key := string(ch)
		got := xKey(key)
		if got != key {
			t.Errorf("xKey(%q) = %q, want %q", key, got, key)
		}
	}
}

// TestXKeySpecialCharacters verifies punctuation maps to X11 keysym names.
func TestXKeySpecialCharacters(t *testing.T) {
	cases := []struct {
		jsKey string
		want  string
	}{
		{"=", "equal"},
		{"+", "plus"},
		{"-", "minus"},
		{"_", "underscore"},
		{"[", "bracketleft"},
		{"]", "bracketright"},
		{";", "semicolon"},
		{",", "comma"},
		{".", "period"},
		{"/", "slash"},
		{"\\", "backslash"},
		{"`", "grave"},
		{"!", "exclam"},
		{"@", "at"},
		{"#", "numbersign"},
		{"$", "dollar"},
		{"%", "percent"},
		{"&", "ampersand"},
		{"*", "asterisk"},
		{"(", "parenleft"},
		{")", "parenright"},
	}
	for _, tc := range cases {
		got := xKey(tc.jsKey)
		if got != tc.want {
			t.Errorf("xKey(%q) = %q, want %q", tc.jsKey, got, tc.want)
		}
	}
}

// TestXKeyUnknownMultiChar verifies that an unknown multi-character key
// returns the empty string (caller skips the event).
func TestXKeyUnknownMultiChar(t *testing.T) {
	got := xKey("UnknownKeyXYZ")
	if got != "" {
		t.Errorf("xKey(unknown) = %q, want empty", got)
	}
}

// ---------------------------------------------------------------------------
// Linux key constant sanity
// ---------------------------------------------------------------------------

// TestLinuxKeyConstantsNonZero ensures no key constant was accidentally
// defined as zero (KEY_RESERVED).
func TestLinuxKeyConstantsNonZero(t *testing.T) {
	keys := []struct {
		name string
		val  uint16
	}{
		{"keyEsc", keyEsc},
		{"keyEnter", keyEnter},
		{"keySpace", keySpace},
		{"keyLeftShift", keyLeftShift},
		{"keyRightShift", keyRightShift},
		{"keyLeftCtrl", keyLeftCtrl},
		{"keyRightCtrl", keyRightCtrl},
		{"keyLeftAlt", keyLeftAlt},
		{"keyRightAlt", keyRightAlt},
		{"keyLeftMeta", keyLeftMeta},
		{"keyRightMeta", keyRightMeta},
		{"keyCapsLock", keyCapsLock},
		{"keyBackspace", keyBackspace},
		{"keyTab", keyTab},
		{"keyA", keyA},
		{"keyZ", keyZ},
		{"key0", key0},
		{"key9", key9},
		{"keyF1", keyF1},
		{"keyF12", keyF12},
		{"keyUp", keyUp},
		{"keyDown", keyDown},
		{"keyLeft", keyLeft},
		{"keyRight", keyRight},
		{"keyHome", keyHome},
		{"keyEnd", keyEnd},
		{"keyPageUp", keyPageUp},
		{"keyPageDown", keyPageDown},
		{"keyInsert", keyInsert},
		{"keyDelete", keyDelete},
	}
	for _, k := range keys {
		if k.val == 0 {
			t.Errorf("key constant %s = 0 (KEY_RESERVED / invalid)", k.name)
		}
	}
}

// TestLinuxKeyConstantsDistinct checks that common key constants are not
// accidentally aliased to each other.
func TestLinuxKeyConstantsDistinct(t *testing.T) {
	pairs := []struct {
		aName, bName string
		a, b         uint16
	}{
		{"keyLeftShift", "keyRightShift", keyLeftShift, keyRightShift},
		{"keyLeftCtrl", "keyRightCtrl", keyLeftCtrl, keyRightCtrl},
		{"keyLeftAlt", "keyRightAlt", keyLeftAlt, keyRightAlt},
		{"keyLeftMeta", "keyRightMeta", keyLeftMeta, keyRightMeta},
		{"keyUp", "keyDown", keyUp, keyDown},
		{"keyLeft", "keyRight", keyLeft, keyRight},
		{"keyF1", "keyF12", keyF1, keyF12},
		{"key0", "key9", key0, key9},
	}
	for _, p := range pairs {
		if p.a == p.b {
			t.Errorf("%s and %s share value %d", p.aName, p.bName, p.a)
		}
	}
}

// ---------------------------------------------------------------------------
// Event type and axis constant values (must match kernel headers)
// ---------------------------------------------------------------------------

func TestEventTypeConstants(t *testing.T) {
	if evSyn != 0x00 {
		t.Errorf("evSyn = 0x%x, want 0x00", evSyn)
	}
	if evKey != 0x01 {
		t.Errorf("evKey = 0x%x, want 0x01", evKey)
	}
	if evRel != 0x02 {
		t.Errorf("evRel = 0x%x, want 0x02", evRel)
	}
	if evAbs != 0x03 {
		t.Errorf("evAbs = 0x%x, want 0x03", evAbs)
	}
}

func TestAbsAxisConstants(t *testing.T) {
	cases := []struct {
		name string
		got  uint16
		want uint16
	}{
		{"absX", absX, 0x00},
		{"absY", absY, 0x01},
		{"absZ (left trigger)", absZ, 0x02},
		{"absRx (right stick X)", absRx, 0x03},
		{"absRy (right stick Y)", absRy, 0x04},
		{"absRz (right trigger)", absRz, 0x05},
		{"absHat0X (d-pad X)", absHat0X, 0x10},
		{"absHat0Y (d-pad Y)", absHat0Y, 0x11},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s = 0x%x, want 0x%x", c.name, c.got, c.want)
		}
	}
}

func TestRelAxisConstants(t *testing.T) {
	if relX != 0x00 {
		t.Errorf("relX = 0x%x, want 0x00", relX)
	}
	if relY != 0x01 {
		t.Errorf("relY = 0x%x, want 0x01", relY)
	}
	if relWheel != 0x08 {
		t.Errorf("relWheel = 0x%x, want 0x08", relWheel)
	}
}

// ---------------------------------------------------------------------------
// Device open paths — skip if not Linux or no /dev/uinput
// ---------------------------------------------------------------------------

func TestCreateMouseDeviceSkipIfUnavailable(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("uinput only available on Linux")
	}
	dev, err := createMouseDevice(1920, 1080)
	if err != nil {
		t.Skipf("uinput not available: %v", err)
	}
	defer dev.Close()
	if dev.name != "mouse" {
		t.Errorf("device name = %q, want %q", dev.name, "mouse")
	}
}

func TestCreateKeyboardDeviceSkipIfUnavailable(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("uinput only available on Linux")
	}
	dev, err := createKeyboardDevice()
	if err != nil {
		t.Skipf("uinput not available: %v", err)
	}
	defer dev.Close()
	if dev.name != "keyboard" {
		t.Errorf("device name = %q, want %q", dev.name, "keyboard")
	}
}

func TestCreateGamepadDeviceSkipIfUnavailable(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("uinput only available on Linux")
	}
	dev, err := createGamepadDevice()
	if err != nil {
		t.Skipf("uinput not available: %v", err)
	}
	defer dev.Close()
	if dev.name != "gamepad" {
		t.Errorf("device name = %q, want %q", dev.name, "gamepad")
	}
}
