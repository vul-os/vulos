import { useEffect, useState, type ReactNode } from 'react'
import { useFocusTrap } from './useFocusTrap'

// A11Y / discoverability — a visible keyboard-shortcut legend. Opened with "?"
// (Shift+/) from anywhere on the desktop, and dismissed with Escape or a click
// on the backdrop. Mirrors the actual bindings registered across the shell:
//   App.jsx          Ctrl+1–9, Ctrl+N, Ctrl+L
//   MissionControl   F3 / Ctrl+↑
//   Portal           Cmd/Ctrl+K
// Built on the same semantic tokens (--bg-*/--text-*/--border-*) as the rest
// of the OS so it tracks the active theme automatically.
interface ShortcutItem {
  keys: string[]
  label: string
}
interface ShortcutGroup {
  title: string
  items: ShortcutItem[]
}

const GROUPS: ShortcutGroup[] = [
  {
    title: 'Windows',
    items: [
      { keys: ['Super', '←/→'], label: 'Snap window to left / right half' },
      { keys: ['Super', '↑'], label: 'Maximize the window' },
      { keys: ['Super', '↓'], label: 'Restore / un-tile the window' },
      { keys: ['Ctrl+Alt', '←→↑↓'], label: 'Tile (no Super key)' },
      { keys: ['Alt', '`'], label: 'Cycle windows (Shift to reverse)' },
      { keys: ['Ctrl / Cmd', 'W'], label: 'Close the active window' },
      { keys: ['Double-click'], label: 'Title bar → maximize / restore' },
      { keys: ['Drag'], label: 'To a screen edge → snap' },
    ],
  },
  {
    title: 'Desktops & overview',
    items: [
      { keys: ['Ctrl', '1–9'], label: 'Switch to desktop' },
      { keys: ['Ctrl', 'N'], label: 'New desktop' },
      { keys: ['F3'], label: 'Mission Control' },
      { keys: ['Ctrl', '↑'], label: 'Mission Control' },
    ],
  },
  {
    title: 'Apps & search',
    items: [
      { keys: ['Cmd / Ctrl', 'K'], label: 'Ask AI / command palette' },
      { keys: ['Esc'], label: 'Close the current overlay' },
    ],
  },
  {
    title: 'System',
    items: [
      { keys: ['Ctrl', 'L'], label: 'Lock the screen' },
      { keys: ['?'], label: 'Show this shortcut legend' },
    ],
  },
]

function Key({ children }: { children: ReactNode }) {
  return (
    <kbd
      className="inline-flex items-center justify-center min-w-[1.5rem] px-1.5 py-0.5 rounded-md text-[12px] font-medium"
      style={{
        background: 'var(--bg-elevated)',
        border: '1px solid var(--border-emphasis)',
        color: 'var(--text-secondary)',
      }}
    >
      {children}
    </kbd>
  )
}

export default function ShortcutsLegend() {
  const [open, setOpen] = useState(false)
  const trapRef = useFocusTrap(open)

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      // Open on "?" — but never while typing in a field.
      const target = e.target as HTMLElement | null
      const tag = (target?.tagName || '').toLowerCase()
      const typing = tag === 'input' || tag === 'textarea' || target?.isContentEditable
      if (!typing && e.key === '?') {
        e.preventDefault()
        setOpen(v => !v)
      } else if (open && e.key === 'Escape') {
        e.preventDefault()
        setOpen(false)
      }
    }
    // Also openable from the ⌘K palette ("Keyboard shortcuts" command).
    const onOpen = () => setOpen(true)
    window.addEventListener('keydown', onKey)
    window.addEventListener('vulos:open-shortcuts', onOpen)
    return () => {
      window.removeEventListener('keydown', onKey)
      window.removeEventListener('vulos:open-shortcuts', onOpen)
    }
  }, [open])

  if (!open) return null

  return (
    <div
      className="fixed inset-0 z-[300] flex items-center justify-center p-6"
      style={{ background: 'rgba(0,0,0,0.55)', backdropFilter: 'blur(6px)' }}
      onClick={(e) => { if (e.target === e.currentTarget) setOpen(false) }}
    >
      <div
        ref={trapRef}
        role="dialog"
        aria-modal="true"
        aria-label="Keyboard shortcuts"
        className="w-full max-w-md rounded-2xl overflow-hidden shadow-2xl"
        style={{
          background: 'var(--bg-surface)',
          border: '1px solid var(--border-strong)',
          color: 'var(--text-primary)',
        }}
      >
        <div
          className="flex items-center justify-between px-5 py-3.5"
          style={{ borderBottom: '1px solid var(--border-default)' }}
        >
          <h2 className="text-sm font-semibold">Keyboard shortcuts</h2>
          <button
            onClick={() => setOpen(false)}
            aria-label="Close shortcuts"
            className="w-6 h-6 flex items-center justify-center rounded-md transition-colors focus:outline-none focus-primary"
            style={{ color: 'var(--text-faint)' }}
          >
            {'×'}
          </button>
        </div>

        <div className="px-5 py-4 space-y-5 max-h-[60vh] overflow-y-auto">
          {GROUPS.map(group => (
            <div key={group.title}>
              <h3
                className="text-[12px] uppercase tracking-wider font-medium mb-2"
                style={{ color: 'var(--text-faint)' }}
              >
                {group.title}
              </h3>
              <div className="space-y-1.5">
                {group.items.map((item, i) => (
                  <div key={i} className="flex items-center justify-between gap-4">
                    <span className="text-[13px]" style={{ color: 'var(--text-secondary)' }}>
                      {item.label}
                    </span>
                    <span className="flex items-center gap-1 shrink-0">
                      {item.keys.map((k, j) => (
                        <span key={j} className="flex items-center gap-1">
                          {j > 0 && (
                            <span className="text-[12px]" style={{ color: 'var(--text-ghost)' }}>+</span>
                          )}
                          <Key>{k}</Key>
                        </span>
                      ))}
                    </span>
                  </div>
                ))}
              </div>
            </div>
          ))}
        </div>

        <div
          className="px-5 py-2.5 text-[12px] flex items-center justify-between"
          style={{ borderTop: '1px solid var(--border-default)', color: 'var(--text-faint)' }}
        >
          <span>Part of Vulos</span>
          <span>Press <Key>Esc</Key> to close</span>
        </div>
      </div>
    </div>
  )
}
