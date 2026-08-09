import { useCallback, useMemo, useState, useSyncExternalStore } from 'react'
import { useShell } from '../providers/ShellProvider'
import { AppIconTile } from '../core/AppIcons'
import { getApps, getAppById, subscribeApps, getAppsVersion } from '../core/AppRegistry'
import { launchApp } from './launchApp'
import type { ShellWindow } from '../providers/ShellProvider'
import './shell-chrome.css'

// Dock — the desktop's permanent launcher + window switcher.
//
// It used to render ONLY the windows already open, and to vanish entirely on an
// empty desktop, which is the opposite of what a dock is for: on a fresh boot
// there was nothing to click. It is now always present and has three bands,
// separated by hairlines, in the order a Mac user expects:
//
//   1. Spotlight            — open the launcher (⌘Space).
//   2. Pinned apps          — the user's kept-in-dock apps, running or not.
//   3. Running-not-pinned   — every other open window, so nothing is lost.
//   4. Assistant + Glance   — the two always-there shell panels.
//
// Behaviour on an app tile mirrors macOS:
//   · not running            → launch it
//   · running, not focused   → focus/raise it
//   · running and focused    → minimize it (toggle)
//   · minimized              → restore + focus
//
// Pins persist in localStorage; the tile's context menu adds/removes them. On
// narrow screens the strip scrolls horizontally instead of overflowing, and it
// lifts clear of the home indicator via the safe-area inset.

const PIN_KEY = 'vulos-dock-pins'

// The default keep-in-dock set: the apps a fresh box should offer before the
// user has expressed any preference. Ids that no longer exist in the registry
// are skipped at render, so this list can never produce a dead tile.
const DEFAULT_PINS = ['drive', 'lilmail', 'vulos-calendar', 'messages', 'terminal', 'persona']

function loadPins(): string[] {
  try {
    const raw = localStorage.getItem(PIN_KEY)
    if (!raw) return DEFAULT_PINS
    const ids: unknown = JSON.parse(raw)
    return Array.isArray(ids) ? ids.filter((x): x is string => typeof x === 'string') : DEFAULT_PINS
  } catch { return DEFAULT_PINS }
}

function savePins(ids: string[]): void {
  try { localStorage.setItem(PIN_KEY, JSON.stringify(ids)) } catch { /* noop */ }
}

/** One dock slot: an app id, plus whichever of its windows exist. */
interface Slot {
  appId: string
  title: string
  windows: ShellWindow[]
  pinned: boolean
}

export default function Dock() {
  const { windows, activeWindow, focusWindow, minimizeWindow, openWindow, setLaunchpad, launchpadOpen, chatOpen, toggleChat } = useShell()
  const [pins, setPins] = useState<string[]>(loadPins)
  const [menuFor, setMenuFor] = useState<string | null>(null)
  const appsVersion = useSyncExternalStore(subscribeApps, getAppsVersion, getAppsVersion)

  // appId → its open windows on the active desktop.
  const byApp = useMemo(() => {
    const m = new Map<string, ShellWindow[]>()
    for (const w of windows) {
      const list = m.get(w.appId)
      if (list) list.push(w)
      else m.set(w.appId, [w])
    }
    return m
  }, [windows])

  const slots: Slot[] = useMemo(() => {
    const known = new Set(getApps().map(a => a.id))
    const out: Slot[] = []
    const seen = new Set<string>()
    for (const id of pins) {
      if (!known.has(id) || seen.has(id)) continue
      seen.add(id)
      out.push({ appId: id, title: getAppById(id)?.name || id, windows: byApp.get(id) || [], pinned: true })
    }
    // Everything running that isn't pinned, in the order it was opened.
    for (const w of windows) {
      if (seen.has(w.appId)) continue
      seen.add(w.appId)
      out.push({ appId: w.appId, title: w.title || getAppById(w.appId)?.name || w.appId, windows: byApp.get(w.appId) || [], pinned: false })
    }
    return out
    // getApps()/getAppById() are external-store reads; appsVersion is what
    // changes when the installed/AI app lists repopulate after boot.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [pins, windows, byApp, appsVersion])

  const activate = useCallback((slot: Slot) => {
    setMenuFor(null)
    const open = slot.windows
    if (open.length === 0) {
      const app = getAppById(slot.appId)
      if (app) void launchApp(app, { openWindow })
      return
    }
    const focused = open.find(w => w.id === activeWindow && !w.minimized)
    if (focused) { minimizeWindow(focused.id); return }
    const target = open.find(w => !w.minimized) || open[0]
    if (target.minimized) minimizeWindow(target.id) // toggles back to visible
    focusWindow(target.id)
  }, [activeWindow, focusWindow, minimizeWindow, openWindow])

  const togglePin = useCallback((appId: string) => {
    setPins(prev => {
      const next = prev.includes(appId) ? prev.filter(p => p !== appId) : [...prev, appId]
      savePins(next)
      return next
    })
    setMenuFor(null)
  }, [])

  return (
    <div
      className="fixed left-1/2 -translate-x-1/2 z-30 pointer-events-none max-w-[calc(100vw-1.25rem)]"
      style={{ bottom: 'max(0.625rem, calc(env(safe-area-inset-bottom, 0px) + 0.375rem))' }}
      onMouseLeave={() => setMenuFor(null)}
    >
      <div
        role="toolbar"
        aria-label="Dock"
        className="vshell-dock flex items-end gap-1 px-2 py-1.5 rounded-[20px] backdrop-blur-xl pointer-events-auto overflow-x-auto no-scrollbar"
      >
        {/* 1 — Spotlight */}
        <DockButton
          label="Search apps"
          hint="Search apps  ⌘Space"
          active={launchpadOpen}
          onClick={() => setLaunchpad(!launchpadOpen)}
        >
          <svg viewBox="0 0 24 24" className="w-[22px] h-[22px]" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round">
            <circle cx="10.5" cy="10.5" r="6.5" /><path d="M15.5 15.5L21 21" />
          </svg>
        </DockButton>

        <span className="vshell-dock-sep" aria-hidden="true" />

        {/* 2 + 3 — pinned apps, then anything else that is running */}
        {slots.map(slot => {
          const running = slot.windows.length > 0
          const focused = slot.windows.some(w => w.id === activeWindow && !w.minimized)
          const allMinimized = running && slot.windows.every(w => w.minimized)
          // The tile's accessible name carries its state, so a screen-reader
          // user can tell a pinned-but-idle app from a running one from a
          // minimized one without reading the indicator dot.
          const state = !running ? '' : focused ? ' (focused)' : allMinimized ? ' (minimized)' : ' (running)'
          return (
            <div key={slot.appId} className="relative shrink-0">
              <button
                onClick={() => activate(slot)}
                onContextMenu={(e) => { e.preventDefault(); setMenuFor(slot.appId) }}
                title={slot.title}
                aria-label={`${slot.title}${state}`}
                aria-pressed={focused}
                className="vshell-dock-item focus-primary rounded-2xl group relative flex flex-col items-center"
              >
                <span
                  data-active={focused ? 'true' : undefined}
                  className={`vshell-dock-tile flex items-center justify-center w-11 h-11 rounded-[13px] ${allMinimized ? 'opacity-55' : ''}`}
                >
                  <AppIconTile id={slot.appId} size={40} unicode={getAppById(slot.appId)?.icon} />
                </span>
                <span
                  data-active={focused ? 'true' : undefined}
                  className="vshell-dot absolute -bottom-0.5 w-1 h-1 rounded-full"
                  style={{ opacity: running ? undefined : 0 }}
                />
                <DockTip>{slot.title}</DockTip>
              </button>
              {menuFor === slot.appId && (
                <div className="vshell-pop vshell-surface absolute bottom-full mb-3 left-1/2 -translate-x-1/2 rounded-xl py-1 min-w-[168px] z-10">
                  <button className="vshell-menu-item w-full text-left" onClick={() => togglePin(slot.appId)}>
                    {slot.pinned ? 'Remove from Dock' : 'Keep in Dock'}
                  </button>
                  {slot.windows.length > 0 && (
                    <button className="vshell-menu-item w-full text-left" onClick={() => { slot.windows.forEach(w => { if (!w.minimized) minimizeWindow(w.id) }); setMenuFor(null) }}>
                      Hide
                    </button>
                  )}
                </div>
              )}
            </div>
          )
        })}

        <span className="vshell-dock-sep" aria-hidden="true" />

        {/* 4 — the always-there shell panels */}
        <DockButton label="Assistant" hint="Assistant" active={chatOpen} onClick={toggleChat}>
          <svg viewBox="0 0 24 24" className="w-[22px] h-[22px]" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" strokeLinejoin="round">
            <path d="M4 6.5A2.5 2.5 0 016.5 4h11A2.5 2.5 0 0120 6.5v6A2.5 2.5 0 0117.5 15H9l-4 3.2V15h-.2A.8.8 0 014 14.2z" />
          </svg>
        </DockButton>
      </div>
    </div>
  )
}

// A non-app dock affordance (Spotlight, Assistant). Same footprint and hover
// behaviour as an app tile so the strip reads as one row.
function DockButton({ label, hint, active, onClick, children }: {
  label: string
  hint: string
  active?: boolean
  onClick: () => void
  children: React.ReactNode
}) {
  return (
    <button
      onClick={onClick}
      title={hint}
      aria-label={label}
      aria-pressed={active}
      className="vshell-dock-item focus-primary rounded-2xl group relative flex flex-col items-center shrink-0"
    >
      <span
        data-active={active ? 'true' : undefined}
        className="vshell-dock-tile vshell-dock-glyph flex items-center justify-center w-11 h-11 rounded-[13px]"
      >
        {children}
      </span>
      <DockTip>{hint}</DockTip>
    </button>
  )
}

function DockTip({ children }: { children: React.ReactNode }) {
  return (
    <span
      className="pointer-events-none absolute bottom-full mb-2.5 whitespace-nowrap px-2.5 py-1 rounded-lg text-[12px] font-medium opacity-0 translate-y-0.5 group-hover:opacity-100 group-hover:translate-y-0 transition-all duration-150"
      style={{
        background: 'color-mix(in srgb, var(--bg-elevated) 94%, transparent)',
        border: '1px solid color-mix(in srgb, var(--border-strong) 65%, transparent)',
        color: 'var(--text-secondary)',
        boxShadow: 'var(--shadow-md)',
      }}
    >
      {children}
    </span>
  )
}
