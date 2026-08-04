import { useEffect, useRef } from 'react'
import { useShell } from '../providers/ShellProvider'
import { nextTile, nextWindowId } from './windowTiling'

// useWindowShortcuts — keyboard-first window management for the desktop shell.
// Bindings (chosen to not collide with the existing shell shortcuts:
// Ctrl+1–9 desktops, Ctrl+N new desktop, Ctrl/Cmd+K palette, Ctrl+L lock,
// F3 / Ctrl+↑ Mission Control, ? legend):
//
//   Super/⌘ + ← → ↑ ↓            tile the active window (half → quarter → …)
//   Ctrl + Alt + ← → ↑ ↓         same, for keyboards without a usable Super key
//   ↓ (from maximize/tiled)      restore to floating geometry
//   Alt + `                      cycle windows forward  (Alt+Shift+` backward)
//   Ctrl/⌘ + W                   close the active window
//
// It reads live state through a ref so the single window-level listener never
// needs re-binding as windows change.
export function useWindowShortcuts() {
  const shell = useShell()
  const ref = useRef(shell)
  // Keep the latest shell handlers/state available to the single window-level
  // listener without re-binding it on every render (same pattern as DesktopCanvas).
  // eslint-disable-next-line react-hooks/refs
  ref.current = shell

  useEffect(() => {
    const arrowDir = { ArrowLeft: 'left', ArrowRight: 'right', ArrowUp: 'up', ArrowDown: 'down' }

    const onKey = (e) => {
      // Never hijack keys while the user is typing in a field / rich editor.
      const t = e.target
      const tag = (t?.tagName || '').toLowerCase()
      if (tag === 'input' || tag === 'textarea' || tag === 'select' || t?.isContentEditable) return

      const s = ref.current
      const { windows, activeWindow, tileWindow, closeWindow, focusWindow } = s
      const active = windows.find(w => w.id === activeWindow) || null

      // ── Tiling: Super/⌘ + arrow, or Ctrl+Alt + arrow ───────────────────────
      const tileMod = (e.metaKey && !e.ctrlKey && !e.altKey) || (e.ctrlKey && e.altKey && !e.metaKey)
      if (tileMod && arrowDir[e.key]) {
        if (!active) return
        e.preventDefault()
        const zone = nextTile(active._tile || (active._maximized ? 'maximize' : null), arrowDir[e.key])
        tileWindow(active.id, zone)
        return
      }

      // ── Cycle windows: Alt + ` ─────────────────────────────────────────────
      if (e.altKey && e.code === 'Backquote') {
        e.preventDefault()
        const id = nextWindowId(windows, activeWindow, e.shiftKey ? -1 : 1)
        if (id != null) focusWindow(id)
        return
      }

      // ── Close active window: Ctrl/⌘ + W ────────────────────────────────────
      if ((e.ctrlKey || e.metaKey) && !e.altKey && (e.key === 'w' || e.key === 'W')) {
        if (!active) return
        e.preventDefault()
        closeWindow(active.id)
        return
      }
    }

    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [])
}
