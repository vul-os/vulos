// Fast baremetal vs native detection — cached at module level, zero re-computation
// Baremetal: running as sole Cog/WPE fullscreen instance (no compositor multi-window)
// Native: running under a Wayland compositor (Sway, labwc, etc.) that supports multi-window
//
// D93 window model: v1 always streams apps over cage — no native-launch path.
// The native-launch / labwc path (BMINIT-04/06) is v2 only, gated behind an
// explicit opt-in: VULOS_NATIVE_MODE_V2=1 env on the server OR the backend
// returning v2_enabled:true from /api/shell/native-mode.
// Default (no opt-in) = 'baremetal' regardless of compositor env.

import { useState, useEffect } from 'react'

const ua = typeof navigator !== 'undefined' ? navigator.userAgent : ''

// Embedded WebKit engines used on device
const isEmbeddedWebKit = ua.includes('WPE') || ua.includes('Cog')

// Detect baremetal: embedded WebKit + no multi-window compositor hints
// On baremetal, Cog runs fullscreen as the only window — no WAYLAND_DISPLAY trickery needed.
// We check via a one-shot API call cached at module level.
let _mode = null // 'baremetal' | 'native' | 'browser'

if (!isEmbeddedWebKit) {
  _mode = 'browser' // Standard browser (dev, remote access)
} else {
  // v1 default: always baremetal/cage. The backend fetch below can only
  // upgrade to 'native' when the server has v2 explicitly enabled.
  _mode = 'baremetal'
}

// Async detection: ask backend once. In v1 the response will have
// v2_enabled:false (or be absent), so mode stays 'baremetal'.
// Only when VULOS_NATIVE_MODE_V2=1 is set server-side will v2_enabled be
// true and the mode promoted to 'native'.
let _modePromise = null
function detectMode() {
  if (_modePromise) return _modePromise
  if (_mode === 'browser') {
    _modePromise = Promise.resolve('browser')
    return _modePromise
  }
  _modePromise = fetch('/api/shell/native-mode')
    .then(r => r.json())
    .then(data => {
      // v1: only accept 'native' when the server explicitly opts in to v2.
      // Without v2_enabled the reported mode is always treated as 'baremetal'.
      if (data.v2_enabled && data.mode === 'native') {
        _mode = 'native'
      } else {
        _mode = 'baremetal'
      }
      return _mode
    })
    .catch(() => {
      _mode = 'baremetal' // Safe fallback
      return _mode
    })
  return _modePromise
}

// Eagerly detect on import (non-blocking)
detectMode()

// Sync getter — returns cached value (defaults correctly before async resolves)
export function getNativeMode() {
  return _mode
}

// Returns true if native windows are supported (not baremetal, not standard browser)
export function canSpawnNativeWindow() {
  return _mode === 'native'
}

// Returns true if running on embedded WebKit (device, not dev browser)
export function isOnDevice() {
  return _mode !== 'browser'
}

// Hook for React components that need to react to mode
export function useNativeMode() {
  const [mode, setMode] = useState(_mode)

  useEffect(() => {
    detectMode().then(m => setMode(m))
  }, [])

  return {
    mode,
    isNative: mode === 'native',
    isBaremetal: mode === 'baremetal',
    isBrowser: mode === 'browser',
    canSpawnNativeWindow: mode === 'native',
  }
}

// useThinWM — returns true when the React WM should operate in "thin" mode
// (D93 v2 labwc unification, BMINIT-18).
//
// In thin mode:
//   - Window.jsx hides its traffic-light buttons (labwc SSD decorates all windows)
//   - Window.jsx hides its drag title bar (labwc SSD moves windows)
//   - ShellProvider mirrors labwc state via /api/shell/windows instead of
//     owning positioning / stacking itself
//
// This is only true when the server has explicitly opted into v2 native mode
// (VULOS_NATIVE_MODE_V2=1); false in v1 / baremetal / browser.
export function useThinWM() {
  const [thin, setThin] = useState(false)

  useEffect(() => {
    detectMode().then(m => {
      setThin(m === 'native')
    })
  }, [])

  return thin
}
