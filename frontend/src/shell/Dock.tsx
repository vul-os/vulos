import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, useSyncExternalStore } from 'react'
import { useShell } from '../providers/ShellProvider'
import { AppIconTile } from '../core/AppIcons'
import { getApps, getAppById, subscribeApps, getAppsVersion } from '../core/AppRegistry'
import { launchApp } from './launchApp'
import { useDockProfile } from '../desktop'
import type { DockProfile } from '../desktop'
import type { ShellWindow } from '../providers/ShellProvider'
import './shell-chrome.css'

// Dock — the desktop's permanent launcher + window switcher.
//
// It used to render ONLY the windows already open, and to vanish entirely on an
// empty desktop, which is the opposite of what a dock is for: on a fresh boot
// there was nothing to click. It is now always present and has four bands,
// separated by hairlines:
//
//   1. Launcher             — open Spotlight (⌘Space).            [profile.launcher]
//   2. Pinned apps          — the user's kept-in-dock apps, running or not.
//   3. Running-not-pinned   — every other open window, so nothing is lost.
//   4. Drawer + Assistant   — the app grid and the assistant panel.
//                                            [profile.drawer / profile.assistant]
//
// Behaviour on an app tile mirrors every desktop:
//   · not running            → launch it
//   · running, not focused   → focus/raise it
//   · running and focused    → minimize it (toggle)
//   · minimized              → restore + focus
//
// # Where it sits is DATA now
//
// Edge, size, style, alignment, autohide and which affordances are present all
// come from src/desktop's dock profile for the ACTIVE form factor — see
// roadmap/CUSTOMIZATION.md. This component reads the profile and stamps it onto
// the layer element as data-* attributes; every pixel of the geometry lives in
// shell-chrome.css, keyed off those attributes. That split is deliberate: a new
// edge or style becomes a CSS rule plus an enum entry rather than a new branch
// of JSX, and a Playwright test can read the applied geometry off the DOM
// instead of inferring it from class strings.
//
// Nothing here can be set to an arbitrary value: the profile has already been
// through src/desktop/validate.ts, which is why this file does no clamping of
// its own, and why it has no path that turns data into styling.
//
// Pins persist in localStorage; the tile's context menu adds/removes them. The
// preset's `items` list is the DEFAULT — it seeds an untouched box and is
// ignored the moment the user has pinned anything themselves.

const PIN_KEY = 'vulos-dock-pins'

function loadPins(): string[] | null {
  try {
    const raw = localStorage.getItem(PIN_KEY)
    if (!raw) return null
    const ids: unknown = JSON.parse(raw)
    return Array.isArray(ids) ? ids.filter((x): x is string => typeof x === 'string') : null
  } catch { return null }
}

function savePins(ids: string[]): void {
  try { localStorage.setItem(PIN_KEY, JSON.stringify(ids)) } catch { /* noop */ }
}

/**
 * Plate and glyph geometry per size. ONE source of truth, shared by the CSS
 * (which reads the plate off the inline width/height) and by AppIconTile's
 * numeric `size` prop — which is exactly why tile size is an enum and not a
 * customizable length token (see desktop/types.ts).
 */
const TILE: Record<DockProfile['size'], { plate: number; icon: number }> = {
  small: { plate: 36, icon: 30 },
  medium: { plate: 44, icon: 40 },
  large: { plate: 56, icon: 50 },
}

/** One dock slot: an app id, plus whichever of its windows exist. */
interface Slot {
  appId: string
  title: string
  windows: ShellWindow[]
  pinned: boolean
}

/** An open tile context menu, anchored to the rect the tile occupied. */
interface OpenMenu {
  appId: string
  rect: { top: number; bottom: number; left: number; right: number; width: number }
}

export default function Dock() {
  const { windows, activeWindow, focusWindow, minimizeWindow, openWindow, setLaunchpad, launchpadOpen, chatOpen, toggleChat } = useShell()
  const profile = useDockProfile()
  const [storedPins, setStoredPins] = useState<string[] | null>(loadPins)
  const [menu, setMenu] = useState<OpenMenu | null>(null)
  const appsVersion = useSyncExternalStore(subscribeApps, getAppsVersion, getAppsVersion)
  const stripRef = useRef<HTMLDivElement>(null)
  const [overflowing, setOverflowing] = useState(false)

  // The user's own pins win whenever they exist; otherwise the preset seeds the
  // dock. Switching preset on a box where someone has already curated their dock
  // must not silently rearrange it.
  const pins = storedPins ?? profile.items

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

  const vertical = profile.edge === 'left' || profile.edge === 'right'

  // ── Overflow ───────────────────────────────────────────────────────────────
  // The strip is capped at the viewport (max-width/max-height: 100% in CSS) and
  // draws with `overflow: visible` so hover labels are not clipped. That is the
  // right default and the wrong one the moment the content no longer fits: it
  // would push the document sideways, which on a 768px shell is a horizontal
  // scrollbar across the whole OS.
  //
  // So overflow is MEASURED and the strip switches to a scroll container only
  // when it genuinely does not fit. Measured rather than guessed from an item
  // count, because the count is not the input — tile size, the launcher, the
  // drawer, the running-not-pinned apps and the viewport all move it.
  useLayoutEffect(() => {
    const el = stripRef.current
    if (!el) return
    const measure = () => {
      // Compare against the cap, which is why this works with overflow:visible:
      // clientWidth is the capped box, scrollWidth is the content.
      const over = vertical
        ? el.scrollHeight > el.clientHeight + 1
        : el.scrollWidth > el.clientWidth + 1
      setOverflowing(over)
    }
    measure()
    // ResizeObserver catches the dock's own content changing; the window
    // listener catches the viewport changing and is the fallback where RO is
    // absent (jsdom, and any engine old enough to lack it) — without it the
    // measurement would simply never run there and the dock would silently keep
    // whatever overflow state it started with.
    const ro = typeof ResizeObserver === 'undefined' ? null : new ResizeObserver(measure)
    ro?.observe(el)
    if (el.parentElement) ro?.observe(el.parentElement)
    window.addEventListener('resize', measure)
    return () => {
      ro?.disconnect()
      window.removeEventListener('resize', measure)
    }
  }, [vertical, slots.length, profile.size, profile.style, profile.launcher, profile.drawer, profile.assistant])

  // The anchored menu is positioned in viewport coordinates, so it has to close
  // when the page moves under it rather than float away from its tile.
  useEffect(() => {
    if (!menu) return
    const close = () => setMenu(null)
    window.addEventListener('resize', close)
    window.addEventListener('blur', close)
    return () => {
      window.removeEventListener('resize', close)
      window.removeEventListener('blur', close)
    }
  }, [menu])

  const activate = useCallback((slot: Slot) => {
    setMenu(null)
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
    setStoredPins(prev => {
      const base = prev ?? profile.items
      const next = base.includes(appId) ? base.filter(p => p !== appId) : [...base, appId]
      savePins(next)
      return next
    })
    setMenu(null)
  }, [profile.items])

  const tile = TILE[profile.size]
  const menuSlot = menu ? slots.find(s => s.appId === menu.appId) : null

  return (
    <div
      className="vdock-layer"
      data-edge={profile.edge}
      data-dock-style={profile.style}
      data-align={profile.align}
      data-size={profile.size}
      data-autohide={profile.autohide ? 'on' : 'off'}
      onMouseLeave={() => setMenu(null)}
    >
      <div
        ref={stripRef}
        role="toolbar"
        aria-label="Dock"
        aria-orientation={vertical ? 'vertical' : 'horizontal'}
        data-overflow={overflowing ? 'true' : undefined}
        className="vshell-dock no-scrollbar"
      >
        {/* 1 — Launcher */}
        {profile.launcher && (
          <>
            <DockButton
              label="Search apps"
              hint="Search apps  ⌘Space"
              plate={tile.plate}
              active={launchpadOpen}
              onClick={() => setLaunchpad(!launchpadOpen)}
            >
              <svg viewBox="0 0 24 24" className="vdock-glyph-svg" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round">
                <circle cx="10.5" cy="10.5" r="6.5" /><path d="M15.5 15.5L21 21" />
              </svg>
            </DockButton>
            <span className="vshell-dock-sep" aria-hidden="true" />
          </>
        )}

        {/* 2 + 3 — pinned apps, then anything else that is running */}
        {slots.map(slot => {
          const running = slot.windows.length > 0
          const focused = slot.windows.some(w => w.id === activeWindow && !w.minimized)
          const allMinimized = running && slot.windows.every(w => w.minimized)
          // A screen-reader user should be able to tell a pinned-but-idle app
          // from a running one from a minimized one without reading the
          // indicator dot — but that belongs in the tile's DESCRIPTION, not in
          // its NAME. The name has to be stable: it is what a user (and a test)
          // refers to the tile by, and folding state into it meant the Files
          // tile was called "Files" or "Files (focused)" or "Files (minimized)"
          // depending on what the window happened to be doing. Focus is already
          // exposed properly via aria-pressed; this only has to carry the two
          // states that attribute cannot express.
          const state = !running ? '' : focused ? 'focused' : allMinimized ? 'minimized' : 'running'
          const stateId = state ? `dock-state-${slot.appId}` : undefined
          return (
            <button
              key={slot.appId}
              onClick={() => activate(slot)}
              onContextMenu={(e) => {
                e.preventDefault()
                const r = e.currentTarget.getBoundingClientRect()
                setMenu({ appId: slot.appId, rect: { top: r.top, bottom: r.bottom, left: r.left, right: r.right, width: r.width } })
              }}
              title={slot.title}
              aria-label={slot.title}
              aria-describedby={stateId}
              aria-pressed={focused}
              className="vshell-dock-item focus-primary group"
            >
              {stateId && <span id={stateId} className="sr-only">{state}</span>}
              <span
                data-active={focused ? 'true' : undefined}
                className={`vshell-dock-tile ${allMinimized ? 'opacity-55' : ''}`}
                style={{ width: tile.plate, height: tile.plate }}
              >
                <AppIconTile id={slot.appId} size={tile.icon} unicode={getAppById(slot.appId)?.icon} />
              </span>
              <span data-active={focused ? 'true' : undefined} className="vshell-dot" style={{ opacity: running ? undefined : 0 }} />
              <DockTip>{slot.title}</DockTip>
            </button>
          )
        })}

        {(profile.drawer || profile.assistant) && <span className="vshell-dock-sep" aria-hidden="true" />}

        {/* 4 — the app drawer and the assistant panel */}
        {profile.drawer && (
          <DockButton
            label="All apps"
            hint="All apps"
            plate={tile.plate}
            active={launchpadOpen}
            onClick={() => setLaunchpad(!launchpadOpen)}
          >
            <svg viewBox="0 0 24 24" className="vdock-glyph-svg" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinejoin="round">
              <rect x="4" y="4" width="6.2" height="6.2" rx="1.9" />
              <rect x="13.8" y="4" width="6.2" height="6.2" rx="1.9" />
              <rect x="4" y="13.8" width="6.2" height="6.2" rx="1.9" />
              <rect x="13.8" y="13.8" width="6.2" height="6.2" rx="1.9" />
            </svg>
          </DockButton>
        )}
        {profile.assistant && (
          <DockButton label="Assistant" hint="Assistant" plate={tile.plate} active={chatOpen} onClick={toggleChat}>
            <svg viewBox="0 0 24 24" className="vdock-glyph-svg" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" strokeLinejoin="round">
              <path d="M4 6.5A2.5 2.5 0 016.5 4h11A2.5 2.5 0 0120 6.5v6A2.5 2.5 0 0117.5 15H9l-4 3.2V15h-.2A.8.8 0 014 14.2z" />
            </svg>
          </DockButton>
        )}
      </div>

      {/* The tile context menu is a SIBLING of the strip, positioned in viewport
          coordinates. It used to be a child, which meant that the moment the
          strip became a scroll container (a narrow shell, a full dock) the only
          route to "Remove from Dock" was clipped out of existence. */}
      {menu && menuSlot && (
        <div
          className="vshell-pop vshell-surface vdock-menu rounded-xl py-1 min-w-[168px]"
          style={menuStyle(profile.edge, menu.rect)}
        >
          <button className="vshell-menu-item w-full text-left" onClick={() => togglePin(menu.appId)}>
            {menuSlot.pinned ? 'Remove from Dock' : 'Keep in Dock'}
          </button>
          {menuSlot.windows.length > 0 && (
            <button
              className="vshell-menu-item w-full text-left"
              onClick={() => { menuSlot.windows.forEach(w => { if (!w.minimized) minimizeWindow(w.id) }); setMenu(null) }}
            >
              Hide
            </button>
          )}
        </div>
      )}
    </div>
  )
}

/** Place the anchored menu on the side of the tile that is on screen. */
function menuStyle(edge: DockProfile['edge'], rect: OpenMenu['rect']): React.CSSProperties {
  const gap = 10
  if (edge === 'bottom') return { position: 'fixed', bottom: window.innerHeight - rect.top + gap, left: rect.left + rect.width / 2, transform: 'translateX(-50%)' }
  if (edge === 'top') return { position: 'fixed', top: rect.bottom + gap, left: rect.left + rect.width / 2, transform: 'translateX(-50%)' }
  if (edge === 'left') return { position: 'fixed', left: rect.right + gap, top: rect.top }
  return { position: 'fixed', right: window.innerWidth - rect.left + gap, top: rect.top }
}

// A non-app dock affordance (Search, All apps, Assistant). Same footprint and
// hover behaviour as an app tile so the strip reads as one row.
function DockButton({ label, hint, plate, active, onClick, children }: {
  label: string
  hint: string
  plate: number
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
      className="vshell-dock-item focus-primary group"
    >
      <span
        data-active={active ? 'true' : undefined}
        className="vshell-dock-tile vshell-dock-glyph"
        style={{ width: plate, height: plate }}
      >
        {children}
      </span>
      <DockTip>{hint}</DockTip>
    </button>
  )
}

// The hover label. Which side it appears on is driven entirely by the layer's
// data-edge in CSS — a label drawn above a TOP-edge dock, or to the left of a
// LEFT-edge dock, is drawn off-screen.
function DockTip({ children }: { children: React.ReactNode }) {
  return <span className="vdock-tip" aria-hidden="true">{children}</span>
}
