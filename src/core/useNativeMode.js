// Fast baremetal vs native detection — cached at module level, zero re-computation
// Baremetal: running as sole Cog/WPE fullscreen instance (no compositor multi-window)
// Native: running under a Wayland compositor (Sway, labwc, etc.) that supports multi-window

const ua = navigator.userAgent

// Embedded WebKit engines used on device
const isEmbeddedWebKit = ua.includes('WPE') || ua.includes('Cog')

// Detect baremetal: embedded WebKit + no multi-window compositor hints
// On baremetal, Cog runs fullscreen as the only window — no WAYLAND_DISPLAY trickery needed.
// We check via a one-shot API call cached at module level.
let _mode = null // 'baremetal' | 'native' | 'browser'

if (!isEmbeddedWebKit) {
  _mode = 'browser' // Standard browser (dev, remote access)
} else {
  // Default to baremetal for embedded — backend confirms if compositor supports multi-window
  _mode = 'baremetal'
}

// Async detection: ask backend once if compositor supports native windows
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
      _mode = data.mode // 'baremetal' or 'native'
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
import { useState, useEffect } from 'react'

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
