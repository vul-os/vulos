// Package input provides zero-overhead virtual input injection via Linux uinput.
// Instead of spawning xdotool processes per event, we write directly to /dev/uinput
// file descriptors — the same approach used by cloud gaming platforms.
//
// Falls back to xdotool on systems without uinput (containers without --device /dev/uinput).
package input

import (
	"encoding/binary"
	"fmt"
	"log"
	"os"
	"sync"
	"syscall"
	"unsafe"
)

// Linux uinput constants
const (
	uinputPath = "/dev/uinput"

	// ioctl codes
	uiSetEvBit  = 0x40045564 // UI_SET_EVBIT
	uiSetKeyBit = 0x40045565 // UI_SET_KEYBIT
	uiSetRelBit = 0x40045566 // UI_SET_RELBIT
	uiSetAbsBit = 0x40045567 // UI_SET_ABSBIT
	uiDevCreate = 0x5501     // UI_DEV_CREATE
	uiDevDestroy = 0x5502    // UI_DEV_DESTROY

	// Event types
	evSyn = 0x00
	evKey = 0x01
	evRel = 0x02
	evAbs = 0x03

	// Sync
	synReport = 0x00

	// Relative axes
	relX     = 0x00
	relY     = 0x01
	relWheel = 0x08

	// Absolute axes
	absX        = 0x00
	absY        = 0x01
	absRx       = 0x03 // right stick X
	absRy       = 0x04 // right stick Y
	absHat0X    = 0x10 // d-pad X
	absHat0Y    = 0x11 // d-pad Y
	absZ        = 0x02 // left trigger
	absRz       = 0x05 // right trigger

	// Mouse buttons
	btnLeft   = 0x110
	btnRight  = 0x111
	btnMiddle = 0x112

	// Gamepad buttons (Xbox layout)
	btnSouth  = 0x130 // A
	btnEast   = 0x131 // B
	btnNorth  = 0x133 // Y
	btnWest   = 0x134 // X
	btnTL     = 0x136 // LB
	btnTR     = 0x137 // RB
	btnSelect = 0x13a // Back/Select
	btnStart  = 0x13b // Start
	btnMode   = 0x13c // Guide
	btnThumbL = 0x13d // Left stick press
	btnThumbR = 0x13e // Right stick press
)

// inputEvent matches the Linux input_event struct.
type inputEvent struct {
	Time  syscall.Timeval
	Type  uint16
	Code  uint16
	Value int32
}

// uinputUserDev matches the uinput_user_dev struct.
type uinputUserDev struct {
	Name       [80]byte
	ID         inputID
	EffectsMax uint32
	AbsMax     [64]int32
	AbsMin     [64]int32
	AbsFuzz    [64]int32
	AbsFlat    [64]int32
}

type inputID struct {
	BusType uint16
	Vendor  uint16
	Product uint16
	Version uint16
}

// Device is a virtual input device backed by /dev/uinput.
type Device struct {
	fd   *os.File
	name string
}

func ioctl(fd uintptr, request uintptr, val uintptr) error {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, request, val)
	if errno != 0 {
		return errno
	}
	return nil
}

// createMouseDevice creates a virtual mouse with absolute positioning.
func createMouseDevice(screenW, screenH int) (*Device, error) {
	f, err := os.OpenFile(uinputPath, os.O_WRONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("open uinput: %w", err)
	}
	fd := f.Fd()

	// Enable event types: key (buttons), abs (position), rel (wheel)
	ioctl(fd, uiSetEvBit, evKey)
	ioctl(fd, uiSetEvBit, evAbs)
	ioctl(fd, uiSetEvBit, evRel)

	// Mouse buttons
	ioctl(fd, uiSetKeyBit, btnLeft)
	ioctl(fd, uiSetKeyBit, btnRight)
	ioctl(fd, uiSetKeyBit, btnMiddle)

	// Absolute axes for precise positioning
	ioctl(fd, uiSetAbsBit, absX)
	ioctl(fd, uiSetAbsBit, absY)

	// Scroll wheel
	ioctl(fd, uiSetRelBit, relWheel)

	// Setup device info
	dev := uinputUserDev{}
	copy(dev.Name[:], "Vula OS Virtual Mouse")
	dev.ID = inputID{BusType: 0x03, Vendor: 0x1234, Product: 0x0001, Version: 1} // BUS_USB
	dev.AbsMax[absX] = int32(screenW - 1)
	dev.AbsMax[absY] = int32(screenH - 1)

	if _, err := f.Write((*[unsafe.Sizeof(dev)]byte)(unsafe.Pointer(&dev))[:]); err != nil {
		f.Close()
		return nil, fmt.Errorf("write device: %w", err)
	}

	if err := ioctl(fd, uiDevCreate, 0); err != nil {
		f.Close()
		return nil, fmt.Errorf("create device: %w", err)
	}

	return &Device{fd: f, name: "mouse"}, nil
}

// createKeyboardDevice creates a virtual keyboard.
func createKeyboardDevice() (*Device, error) {
	f, err := os.OpenFile(uinputPath, os.O_WRONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("open uinput: %w", err)
	}
	fd := f.Fd()

	ioctl(fd, uiSetEvBit, evKey)

	// Enable all standard keys (0-255)
	for i := uintptr(0); i < 256; i++ {
		ioctl(fd, uiSetKeyBit, i)
	}

	dev := uinputUserDev{}
	copy(dev.Name[:], "Vula OS Virtual Keyboard")
	dev.ID = inputID{BusType: 0x03, Vendor: 0x1234, Product: 0x0002, Version: 1}

	if _, err := f.Write((*[unsafe.Sizeof(dev)]byte)(unsafe.Pointer(&dev))[:]); err != nil {
		f.Close()
		return nil, fmt.Errorf("write device: %w", err)
	}

	if err := ioctl(fd, uiDevCreate, 0); err != nil {
		f.Close()
		return nil, fmt.Errorf("create device: %w", err)
	}

	return &Device{fd: f, name: "keyboard"}, nil
}

// createGamepadDevice creates a virtual Xbox-style gamepad.
func createGamepadDevice() (*Device, error) {
	f, err := os.OpenFile(uinputPath, os.O_WRONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("open uinput: %w", err)
	}
	fd := f.Fd()

	ioctl(fd, uiSetEvBit, evKey)
	ioctl(fd, uiSetEvBit, evAbs)

	// Gamepad buttons
	for _, btn := range []uintptr{btnSouth, btnEast, btnNorth, btnWest,
		btnTL, btnTR, btnSelect, btnStart, btnMode, btnThumbL, btnThumbR} {
		ioctl(fd, uiSetKeyBit, btn)
	}

	// Analog sticks + triggers + d-pad
	for _, axis := range []uintptr{absX, absY, absRx, absRy, absZ, absRz, absHat0X, absHat0Y} {
		ioctl(fd, uiSetAbsBit, axis)
	}

	dev := uinputUserDev{}
	copy(dev.Name[:], "Vula OS Virtual Gamepad")
	dev.ID = inputID{BusType: 0x03, Vendor: 0x045e, Product: 0x028e, Version: 1} // Xbox 360 IDs
	// Analog sticks: -32768 to 32767
	for _, axis := range []int{int(absX), int(absY), int(absRx), int(absRy)} {
		dev.AbsMin[axis] = -32768
		dev.AbsMax[axis] = 32767
		dev.AbsFuzz[axis] = 16
		dev.AbsFlat[axis] = 128
	}
	// Triggers: 0 to 255
	dev.AbsMax[absZ] = 255
	dev.AbsMax[absRz] = 255
	// D-pad: -1 to 1
	dev.AbsMin[absHat0X] = -1
	dev.AbsMax[absHat0X] = 1
	dev.AbsMin[absHat0Y] = -1
	dev.AbsMax[absHat0Y] = 1

	if _, err := f.Write((*[unsafe.Sizeof(dev)]byte)(unsafe.Pointer(&dev))[:]); err != nil {
		f.Close()
		return nil, fmt.Errorf("write device: %w", err)
	}

	if err := ioctl(fd, uiDevCreate, 0); err != nil {
		f.Close()
		return nil, fmt.Errorf("create device: %w", err)
	}

	return &Device{fd: f, name: "gamepad"}, nil
}

func (d *Device) emit(evType, code uint16, value int32) error {
	ev := inputEvent{
		Type:  evType,
		Code:  code,
		Value: value,
	}
	buf := make([]byte, unsafe.Sizeof(ev))
	*(*inputEvent)(unsafe.Pointer(&buf[0])) = ev
	_, err := d.fd.Write(buf)
	return err
}

func (d *Device) sync() error {
	return d.emit(evSyn, synReport, 0)
}

func (d *Device) Close() {
	if d.fd != nil {
		ioctl(d.fd.Fd(), uiDevDestroy, 0)
		d.fd.Close()
	}
}

// Modifier bitmask constants — must match frontend/backend protocol.
const (
	ModShift    = 1
	ModCtrl     = 2
	ModAlt      = 4
	ModMeta     = 8
	ModCapsLock = 16
)

// Injector manages virtual input devices and injects events.
// Uses uinput when available, falls back to xdotool via persistent pipe.
type Injector struct {
	mu       sync.Mutex
	mouse    *Device
	keyboard *Device
	gamepad  *Device
	pipe     *xdotoolPipe // persistent xdotool process (fallback path)
	useUinput bool
	screenW   int
	screenH   int
	display   string
	// Tracked modifier state for reconciliation
	modState int // current bitmask of held modifiers
}

// NewInjector creates an input injector for the given display.
// Tries uinput first (zero overhead), falls back to xdotool.
func NewInjector(display string, screenW, screenH int) *Injector {
	inj := &Injector{
		screenW: screenW,
		screenH: screenH,
		display: display,
	}

	// Try uinput
	mouse, err := createMouseDevice(screenW, screenH)
	if err != nil {
		log.Printf("[input] uinput not available (%v), using xdotool pipe fallback", err)
		inj.pipe = newXdotoolPipe(display)
		return inj
	}

	kbd, err := createKeyboardDevice()
	if err != nil {
		mouse.Close()
		log.Printf("[input] keyboard uinput failed: %v, using xdotool pipe", err)
		inj.pipe = newXdotoolPipe(display)
		return inj
	}

	gamepad, err := createGamepadDevice()
	if err != nil {
		log.Printf("[input] gamepad uinput failed: %v (gamepad disabled)", err)
		// Continue without gamepad — mouse+keyboard still work
	}

	inj.mouse = mouse
	inj.keyboard = kbd
	inj.gamepad = gamepad
	inj.useUinput = true
	log.Printf("[input] uinput devices created (mouse + keyboard + gamepad)")
	return inj
}

// Close destroys all virtual devices and the xdotool pipe.
func (inj *Injector) Close() {
	if inj.mouse != nil {
		inj.mouse.Close()
	}
	if inj.keyboard != nil {
		inj.keyboard.Close()
	}
	if inj.gamepad != nil {
		inj.gamepad.Close()
	}
	if inj.pipe != nil {
		inj.pipe.close()
	}
}

// MouseMove moves the virtual mouse to absolute coordinates.
func (inj *Injector) MouseMove(x, y int) {
	if !inj.useUinput {
		inj.xdotool("mousemove", "--screen", "0", itoa(x), itoa(y))
		return
	}
	inj.mouse.emit(evAbs, absX, int32(x))
	inj.mouse.emit(evAbs, absY, int32(y))
	inj.mouse.sync()
}

// MouseButton presses or releases a mouse button.
// button: 0=left, 1=middle, 2=right. pressed: true=down, false=up.
func (inj *Injector) MouseButton(button int, pressed bool) {
	code := uint16(btnLeft)
	switch button {
	case 1:
		code = btnMiddle
	case 2:
		code = btnRight
	}
	val := int32(0)
	if pressed {
		val = 1
	}

	if !inj.useUinput {
		action := "mouseup"
		if pressed {
			action = "mousedown"
		}
		inj.xdotool(action, itoa(int(xBtn(button))))
		return
	}
	inj.mouse.emit(evKey, code, val)
	inj.mouse.sync()
}

// Scroll sends a scroll wheel event.
func (inj *Injector) Scroll(clicks int) {
	if !inj.useUinput {
		btn := 5 // scroll down
		if clicks < 0 {
			btn = 4 // scroll up
			clicks = -clicks
		}
		if clicks < 1 {
			clicks = 1
		}
		if clicks > 5 {
			clicks = 5
		}
		inj.xdotool("click", "--repeat", itoa(clicks), "--delay", "10", itoa(btn))
		return
	}
	inj.mouse.emit(evRel, relWheel, int32(-clicks))
	inj.mouse.sync()
}

// SyncModifiers reconciles the remote modifier state with the client's bitmask.
// If the client says shift is held but we haven't injected a shift-down, inject it.
// If the client says shift is released but we think it's held, release it.
// This recovers from dropped packets on unreliable channels.
func (inj *Injector) SyncModifiers(clientMod int) {
	modKeys := []struct {
		bit  int
		code uint16
		xkey string
	}{
		{ModShift, keyLeftShift, "Shift_L"},
		{ModCtrl, keyLeftCtrl, "Control_L"},
		{ModAlt, keyLeftAlt, "Alt_L"},
		{ModMeta, keyLeftMeta, "Super_L"},
	}

	for _, mk := range modKeys {
		clientHeld := clientMod&mk.bit != 0
		localHeld := inj.modState&mk.bit != 0
		if clientHeld == localHeld {
			continue
		}
		// State mismatch — reconcile
		if clientHeld {
			inj.modState |= mk.bit
			if inj.useUinput {
				inj.keyboard.emit(evKey, mk.code, 1)
				inj.keyboard.sync()
			} else {
				inj.xdotool("keydown", mk.xkey)
			}
		} else {
			inj.modState &^= mk.bit
			if inj.useUinput {
				inj.keyboard.emit(evKey, mk.code, 0)
				inj.keyboard.sync()
			} else {
				inj.xdotool("keyup", mk.xkey)
			}
		}
	}

	// CapsLock is a toggle, not a held modifier — don't sync it here.
	// The reliable keyboard channel guarantees keydown/keyup arrive in order,
	// so KeyPress handles CapsLock toggle naturally. Syncing here would
	// double-toggle (SyncModifiers toggles ON, then KeyPress toggles OFF).
}

// KeyPress presses or releases a key.
func (inj *Injector) KeyPress(jsKey, jsCode string, pressed bool) {
	val := int32(0)
	if pressed {
		val = 1
	}

	// Track modifier state locally
	if linuxCode, ok := jsToLinuxKey(jsKey, jsCode); ok {
		switch linuxCode {
		case keyLeftShift, keyRightShift:
			if pressed { inj.modState |= ModShift } else { inj.modState &^= ModShift }
		case keyLeftCtrl, keyRightCtrl:
			if pressed { inj.modState |= ModCtrl } else { inj.modState &^= ModCtrl }
		case keyLeftAlt, keyRightAlt:
			if pressed { inj.modState |= ModAlt } else { inj.modState &^= ModAlt }
		case keyLeftMeta, keyRightMeta:
			if pressed { inj.modState |= ModMeta } else { inj.modState &^= ModMeta }
		case keyCapsLock:
			if pressed { inj.modState ^= ModCapsLock } // toggle on press
		}
	}

	if !inj.useUinput {
		action := "keyup"
		if pressed {
			action = "keydown"
		}
		if xk := xKey(jsKey); xk != "" {
			inj.xdotool(action, xk)
		}
		return
	}

	if linuxCode, ok := jsToLinuxKey(jsKey, jsCode); ok {
		inj.keyboard.emit(evKey, linuxCode, val)
		inj.keyboard.sync()
	}
}

// GamepadButton presses or releases a gamepad button.
// index matches the Gamepad API button index.
func (inj *Injector) GamepadButton(index int, pressed bool) {
	if inj.gamepad == nil {
		return
	}
	code, ok := gamepadButtonMap[index]
	if !ok {
		return
	}
	val := int32(0)
	if pressed {
		val = 1
	}
	inj.gamepad.emit(evKey, code, val)
	inj.gamepad.sync()
}

// GamepadAxis sets a gamepad analog axis value.
// index matches the Gamepad API axis index.
// value is -1.0 to 1.0 for sticks, 0.0 to 1.0 for triggers.
func (inj *Injector) GamepadAxis(index int, value float64) {
	if inj.gamepad == nil {
		return
	}
	switch index {
	case 0: // Left stick X
		inj.gamepad.emit(evAbs, absX, int32(value*32767))
	case 1: // Left stick Y
		inj.gamepad.emit(evAbs, absY, int32(value*32767))
	case 2: // Right stick X
		inj.gamepad.emit(evAbs, absRx, int32(value*32767))
	case 3: // Right stick Y
		inj.gamepad.emit(evAbs, absRy, int32(value*32767))
	}
	inj.gamepad.sync()
}

// GamepadTrigger sets a trigger value (0.0 to 1.0).
func (inj *Injector) GamepadTrigger(index int, value float64) {
	if inj.gamepad == nil {
		return
	}
	switch index {
	case 0: // Left trigger
		inj.gamepad.emit(evAbs, absZ, int32(value*255))
	case 1: // Right trigger
		inj.gamepad.emit(evAbs, absRz, int32(value*255))
	}
	inj.gamepad.sync()
}

// gamepadButtonMap maps Gamepad API button index → Linux button code.
// Standard mapping: https://w3c.github.io/gamepad/#remapping
var gamepadButtonMap = map[int]uint16{
	0:  btnSouth,  // A
	1:  btnEast,   // B
	2:  btnWest,   // X
	3:  btnNorth,  // Y
	4:  btnTL,     // LB
	5:  btnTR,     // RB
	8:  btnSelect, // Back
	9:  btnStart,  // Start
	10: btnThumbL, // Left stick
	11: btnThumbR, // Right stick
	16: btnMode,   // Guide
}

func xBtn(js int) int {
	switch js {
	case 1:
		return 2
	case 2:
		return 3
	default:
		return 1
	}
}

func itoa(i int) string {
	return fmt.Sprintf("%d", i)
}

// xdotool sends a command via the persistent pipe (fast) or falls back to fork-exec.
func (inj *Injector) xdotool(args ...string) {
	if inj.pipe != nil {
		inj.pipe.send(args...)
		return
	}
	xdotoolExec(inj.display, args)
}

// xKey maps browser key names to X11 keysym names (xdotool fallback).
func xKey(key string) string {
	m := map[string]string{
		"Enter": "Return", "Backspace": "BackSpace", "Tab": "Tab",
		"Escape": "Escape", "Delete": "Delete", "Insert": "Insert",
		"Home": "Home", "End": "End", "PageUp": "Prior", "PageDown": "Next",
		"ArrowUp": "Up", "ArrowDown": "Down", "ArrowLeft": "Left", "ArrowRight": "Right",
		"Shift": "Shift_L", "Control": "Control_L", "Alt": "Alt_L", "Meta": "Super_L",
		"CapsLock": "Caps_Lock", " ": "space",
		"F1": "F1", "F2": "F2", "F3": "F3", "F4": "F4", "F5": "F5", "F6": "F6",
		"F7": "F7", "F8": "F8", "F9": "F9", "F10": "F10", "F11": "F11", "F12": "F12",
	}
	if x, ok := m[key]; ok {
		return x
	}
	// Map single characters to X11 keysym names
	charMap := map[byte]string{
		'=': "equal", '+': "plus", '-': "minus", '_': "underscore",
		'[': "bracketleft", ']': "bracketright", '{': "braceleft", '}': "braceright",
		';': "semicolon", ':': "colon", '\'': "apostrophe", '"': "quotedbl",
		',': "comma", '.': "period", '<': "less", '>': "greater",
		'/': "slash", '?': "question", '\\': "backslash", '|': "bar",
		'`': "grave", '~': "asciitilde", '!': "exclam", '@': "at",
		'#': "numbersign", '$': "dollar", '%': "percent", '^': "asciicircum",
		'&': "ampersand", '*': "asterisk", '(': "parenleft", ')': "parenright",
	}
	if len(key) == 1 {
		if sym, ok := charMap[key[0]]; ok {
			return sym
		}
		return key // letters and digits pass through as-is
	}
	return ""
}

// jsToLinuxKey maps JavaScript key/code to Linux input keycodes.
func jsToLinuxKey(key, code string) (uint16, bool) {
	// Try code first (more reliable for physical key position)
	if v, ok := jsCodeToLinux[code]; ok {
		return v, true
	}
	// Fall back to key name
	if v, ok := jsKeyToLinux[key]; ok {
		return v, true
	}
	return 0, false
}

// Linux KEY_* constants
const (
	keyEsc       = 1
	key1         = 2
	key2         = 3
	key3         = 4
	key4         = 5
	key5         = 6
	key6         = 7
	key7         = 8
	key8         = 9
	key9         = 10
	key0         = 11
	keyMinus     = 12
	keyEqual     = 13
	keyBackspace = 14
	keyTab       = 15
	keyQ         = 16
	keyW         = 17
	keyE         = 18
	keyR         = 19
	keyT         = 20
	keyY         = 21
	keyU         = 22
	keyI         = 23
	keyO         = 24
	keyP         = 25
	keyLeftBrace = 26
	keyRightBrace = 27
	keyEnter     = 28
	keyLeftCtrl  = 29
	keyA         = 30
	keyS         = 31
	keyD         = 32
	keyF         = 33
	keyG         = 34
	keyH         = 35
	keyJ         = 36
	keyK         = 37
	keyL         = 38
	keySemicolon = 39
	keyApostrophe = 40
	keyGrave     = 41
	keyLeftShift = 42
	keyBackslash = 43
	keyZ         = 44
	keyX         = 45
	keyC         = 46
	keyV         = 47
	keyB         = 48
	keyN         = 49
	keyM         = 50
	keyComma     = 51
	keyDot       = 52
	keySlash     = 53
	keyRightShift = 54
	keyLeftAlt   = 56
	keySpace     = 57
	keyCapsLock  = 58
	keyF1        = 59
	keyF2        = 60
	keyF3        = 61
	keyF4        = 62
	keyF5        = 63
	keyF6        = 64
	keyF7        = 65
	keyF8        = 66
	keyF9        = 67
	keyF10       = 68
	keyF11       = 87
	keyF12       = 88
	keyHome      = 102
	keyUp        = 103
	keyPageUp    = 104
	keyLeft      = 105
	keyRight     = 106
	keyEnd       = 107
	keyDown      = 108
	keyPageDown  = 109
	keyInsert    = 110
	keyDelete    = 111
	keyLeftMeta  = 125
	keyRightMeta = 126
	keyRightCtrl = 97
	keyRightAlt  = 100
)

var jsCodeToLinux = map[string]uint16{
	"Escape": keyEsc, "Digit1": key1, "Digit2": key2, "Digit3": key3,
	"Digit4": key4, "Digit5": key5, "Digit6": key6, "Digit7": key7,
	"Digit8": key8, "Digit9": key9, "Digit0": key0,
	"Minus": keyMinus, "Equal": keyEqual, "Backspace": keyBackspace,
	"Tab": keyTab,
	"KeyQ": keyQ, "KeyW": keyW, "KeyE": keyE, "KeyR": keyR, "KeyT": keyT,
	"KeyY": keyY, "KeyU": keyU, "KeyI": keyI, "KeyO": keyO, "KeyP": keyP,
	"BracketLeft": keyLeftBrace, "BracketRight": keyRightBrace,
	"Enter": keyEnter, "ControlLeft": keyLeftCtrl,
	"KeyA": keyA, "KeyS": keyS, "KeyD": keyD, "KeyF": keyF, "KeyG": keyG,
	"KeyH": keyH, "KeyJ": keyJ, "KeyK": keyK, "KeyL": keyL,
	"Semicolon": keySemicolon, "Quote": keyApostrophe, "Backquote": keyGrave,
	"ShiftLeft": keyLeftShift, "Backslash": keyBackslash,
	"KeyZ": keyZ, "KeyX": keyX, "KeyC": keyC, "KeyV": keyV, "KeyB": keyB,
	"KeyN": keyN, "KeyM": keyM,
	"Comma": keyComma, "Period": keyDot, "Slash": keySlash,
	"ShiftRight": keyRightShift, "AltLeft": keyLeftAlt, "Space": keySpace,
	"CapsLock": keyCapsLock,
	"F1": keyF1, "F2": keyF2, "F3": keyF3, "F4": keyF4, "F5": keyF5, "F6": keyF6,
	"F7": keyF7, "F8": keyF8, "F9": keyF9, "F10": keyF10, "F11": keyF11, "F12": keyF12,
	"Home": keyHome, "ArrowUp": keyUp, "PageUp": keyPageUp,
	"ArrowLeft": keyLeft, "ArrowRight": keyRight,
	"End": keyEnd, "ArrowDown": keyDown, "PageDown": keyPageDown,
	"Insert": keyInsert, "Delete": keyDelete,
	"MetaLeft": keyLeftMeta, "ControlRight": keyRightCtrl, "AltRight": keyRightAlt,
}

var jsKeyToLinux = map[string]uint16{
	"Escape": keyEsc, "Backspace": keyBackspace, "Tab": keyTab, "Enter": keyEnter,
	"Shift": keyLeftShift, "Control": keyLeftCtrl, "Alt": keyLeftAlt, "Meta": keyLeftMeta,
	"CapsLock": keyCapsLock, " ": keySpace, "Delete": keyDelete, "Insert": keyInsert,
	"Home": keyHome, "End": keyEnd, "PageUp": keyPageUp, "PageDown": keyPageDown,
	"ArrowUp": keyUp, "ArrowDown": keyDown, "ArrowLeft": keyLeft, "ArrowRight": keyRight,
	"F1": keyF1, "F2": keyF2, "F3": keyF3, "F4": keyF4, "F5": keyF5, "F6": keyF6,
	"F7": keyF7, "F8": keyF8, "F9": keyF9, "F10": keyF10, "F11": keyF11, "F12": keyF12,
	// Single character keys mapped by rune
	"a": keyA, "b": keyB, "c": keyC, "d": keyD, "e": keyE, "f": keyF, "g": keyG,
	"h": keyH, "i": keyI, "j": keyJ, "k": keyK, "l": keyL, "m": keyM, "n": keyN,
	"o": keyO, "p": keyP, "q": keyQ, "r": keyR, "s": keyS, "t": keyT, "u": keyU,
	"v": keyV, "w": keyW, "x": keyX, "y": keyY, "z": keyZ,
	"0": key0, "1": key1, "2": key2, "3": key3, "4": key4,
	"5": key5, "6": key6, "7": key7, "8": key8, "9": key9,
}

// Ensure binary package is used (for future serialization needs)
var _ = binary.LittleEndian
