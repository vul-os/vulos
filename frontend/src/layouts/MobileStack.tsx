import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, useSyncExternalStore, type ReactNode } from 'react'
import { useShell, type ShellWindow } from '../providers/ShellProvider'
import LifePulse from '../core/SystemPulse'
import AppIcon, { AppIconTile } from '../core/AppIcons'
import { getApps, getAppById, getAppsVersion, subscribeApps } from '../core/AppRegistry'
import { launchApp } from '../shell/launchApp'
import { useDockProfile } from '../desktop'
import { mobileDockAutohide, mobileDockGlyph, mobileDockPlate, mobileDockSlots, type MobileSlot } from '../mobile/mobileDock'
import Launchpad from '../shell/Launchpad'
import Toasts from '../shell/Toasts'
import TrustBadge from '../shell/TrustBadge'
import TransparencyPanel from '../shell/TransparencyPanel'
import CommandPalette from '../shell/CommandPalette'
import { iframeSandboxForURL } from '../core/AppOrigins'
import { attachAppBridge, appFrameSrc } from '../core/AppBridge'
import MobileHome from '../mobile/MobileHome'
import AppSwitcher from '../mobile/AppSwitcher'
import { useMobileEdgeGuards } from '../mobile/useMobileEdgeGuards'
import { useLaunchIntent } from '../mobile/useLaunchIntent'
import '../shell/shell-chrome.css'
import '../mobile/mobile.css'

// ORIGIN-01: identical rule to the desktop shell (src/shell/Window.jsx) —
// allow-same-origin is derived from the frame URL's origin and is granted only
// when that origin is not the shell's. See core/AppOrigins.js.
//
// MOBILE-ADAPTIVE (WAVE-30): the desktop metaphor ADAPTS on phones/tablets — it
// does not shrink. There are no tiny draggable windows: a launched app takes the
// FULL screen, and the running apps are reached through a phone-style app
// switcher (long-list of live cards) rather than a floating window stack. A
// persistent bottom dock (Home · Switcher · All apps) is the single, thumb-
// reachable navigation surface, padded for the device's home-indicator inset.
//
// Every open window stays MOUNTED (hidden, not unmounted) so switching apps
// preserves their scroll/iframe/component state — exactly like a native OS.
//
// STYLING (WAVE-32): the whole surface is token-driven so it is correct in light
// AND dark. Thumb-reach ergonomics: the status bar and dock own the top/bottom
// safe-area, every control clears the 44px touch floor, scroll surfaces carry
// momentum + overscroll containment, and press feedback replaces hover (there is
// no hover on touch). Iconography follows the OS system stroke (1.7px, rounded).

type MobileView = 'home' | 'app' | 'switcher'

export default function MobileStack() {
  const { windows, activeWindow, focusWindow, toggleLaunchpad, openWindow } = useShell()
  // MOBILE-DOCK: the phone dock is DATA now.
  //
  // 'mobile' is passed EXPLICITLY rather than letting the store pick the active
  // form factor. desktop/store.ts's activeFormFactor() is a pure width test at
  // 768px, but shell/useViewport.ts also mounts this shell on a coarse-pointer
  // tablet up to 1024px — so at 768–1024 the touch shell is up while
  // activeFormFactor() still says 'desktop'. A dock that read the "active"
  // profile would render the DESKTOP dock's twelve-item, small-tile geometry on
  // an iPad. Naming the form factor removes the disagreement entirely.
  //
  // For the same reason the geometry is stamped on THIS element's data-* rather
  // than read off <html>: store.ts stamps the root from activeFormFactor(), so
  // between 768 and 1024 the root carries the desktop profile's attributes.
  const profile = useDockProfile('mobile')
  const appsVersion = useSyncExternalStore(subscribeApps, getAppsVersion, getAppsVersion)
  // MOBILE-11: honour `?open=<appId>` so the manifest's launcher shortcuts
  // (long-press the Vulos icon → "Mail") actually open something.
  useLaunchIntent({ openWindow })
  // MOBILE-08: disarm Chrome's pull-to-refresh for as long as the mobile shell
  // is mounted. Measured armed ('auto') on the shipping build — a downward drag
  // near the top of the OS reloaded the whole shell. See useMobileEdgeGuards.
  useMobileEdgeGuards()
  // view: 'home' (assistant + glance) | 'app' (fullscreen active app) | 'switcher'
  const [view, setView] = useState<MobileView>('home')
  const prevCount = useRef(windows.length)

  // Launching an app (window count grew) jumps to the fullscreen app view.
  // Closing the last app falls back Home. Neither fights an explicit user nav.
  useEffect(() => {
    const grew = windows.length > prevCount.current
    prevCount.current = windows.length
    // Navigating in response to a launch/close is intentional external-driven
    // state (the window count is owned by ShellProvider), not a render-derived
    // value — hence the guarded setState in this effect.
    // eslint-disable-next-line react-hooks/set-state-in-effect
    if (grew) setView('app')
    else if (windows.length === 0) setView('home')
  }, [windows.length])

  const activeWin = windows.find(w => w.id === activeWindow) || windows.at(-1) || null
  const showApp = view === 'app' && activeWin

  const openApp = (id: number) => { focusWindow(id); setView('app') }

  // The dock's contents. getApps() is an external-store read, so appsVersion is
  // the dependency that matters: the registry repopulates after boot and a
  // profile item naming an app that was not installed yet must appear when it is.
  // `void appsVersion` rather than an eslint-disable: getApps() is an
  // external-store read, so the version counter is the only honest dependency,
  // and referencing it makes the dep array truthful. It matters more than style
  // — a `react-hooks/exhaustive-deps` suppression in this component makes the
  // compiler-backed hook rules BAIL OUT of the whole file, which silently
  // disarmed the `set-state-in-effect` check on the view effect above. Measured:
  // with the suppression, eslint reported that effect's disable directive as
  // unused; without it, the rule fires again.
  const slots = useMemo(() => {
    void appsVersion
    return mobileDockSlots(profile, getApps().map(a => a.id))
  }, [profile, appsVersion])

  // appId → its open windows, so a dock tile knows whether it launches, focuses
  // or dismisses.
  const byApp = useMemo(() => {
    const m = new Map<string, ShellWindow[]>()
    for (const w of windows) {
      const list = m.get(w.appId)
      if (list) list.push(w)
      else m.set(w.appId, [w])
    }
    return m
  }, [windows])

  // A docked app tile, with the phone's version of the desktop dock's contract:
  //   · not running          → launch it
  //   · running, not shown   → focus it and show it fullscreen
  //   · running and shown    → back to Home
  // That last branch is the phone analogue of the desktop's minimize-on-second-
  // click. There are no windows to minimize here, so "put it away" means Home —
  // and the app stays mounted, so nothing is lost by tapping it.
  const activateApp = useCallback((appId: string) => {
    const open = byApp.get(appId) ?? []
    if (open.length === 0) {
      const app = getAppById(appId)
      if (app) void launchApp(app, { openWindow })
      return
    }
    const target = open.find(w => w.id === activeWindow) ?? open[0]
    if (view === 'app' && activeWin && activeWin.id === target.id) { setView('home'); return }
    focusWindow(target.id)
    setView('app')
  }, [byApp, openWindow, activeWindow, activeWin, view, focusWindow])

  // The plate is MEASURED, not assumed. Screenshotted at 390×844 with eight
  // slots and the `large` tile, the fixed 56px marks rendered edge-to-edge as
  // one continuous strip of colour — five apps reading as one object. Nothing
  // caught it: the touch target is the BUTTON, and that measured 48.7px.
  // AppIconTile writes its size as an inline width/height, so CSS cannot shrink
  // a mark after the fact; the number has to be right before it renders.
  const stripRef = useRef<HTMLDivElement>(null)
  const [slotWidth, setSlotWidth] = useState(0)
  useLayoutEffect(() => {
    const el = stripRef.current
    if (!el) return undefined
    const measure = () => setSlotWidth(el.clientWidth / Math.max(1, slots.length))
    measure()
    // ResizeObserver catches the dock's own content changing; the window
    // listener catches the viewport and is the fallback where RO is absent
    // (jsdom) — without it the measurement would simply never run there.
    const ro = typeof ResizeObserver === 'undefined' ? null : new ResizeObserver(measure)
    ro?.observe(el)
    window.addEventListener('resize', measure)
    return () => {
      ro?.disconnect()
      window.removeEventListener('resize', measure)
    }
  }, [slots.length, profile.size, profile.style])

  const plate = mobileDockPlate(profile.size, slotWidth)
  const glyph = mobileDockGlyph(plate)

  const dock = (
    <nav
      aria-label="System navigation"
      className="vmob-dock shrink-0"
      data-edge={profile.edge}
      data-dock-style={profile.style}
      data-align={profile.align}
      data-size={profile.size}
      // Always 'off'. mobileDockAutohide() refuses every profile — there is no
      // hover on touch to bring a hidden dock back, and this is the only
      // navigation surface the phone shell has. See mobile/mobileDock.ts.
      data-autohide={mobileDockAutohide(profile) ? 'on' : 'off'}
      data-slots={slots.length}
    >
      <div className="vmob-dock-strip" ref={stripRef}>
        {slots.map(slot => (
          <DockSlot
            key={slotKey(slot)}
            slot={slot}
            glyph={glyph}
            plate={plate}
            view={view}
            windowCount={windows.length}
            running={slot.kind === 'app' ? (byApp.get(slot.appId)?.length ?? 0) > 0 : false}
            onHome={() => setView('home')}
            onSwitcher={() => setView(v => (v === 'switcher' ? (activeWin ? 'app' : 'home') : 'switcher'))}
            onLibrary={toggleLaunchpad}
            onApp={activateApp}
          />
        ))}
      </div>
    </nav>
  )

  return (
    <div data-shell="mobile" className="vmob-root fixed inset-0 flex flex-col overflow-hidden">
      {/* Status bar — safe-area padded so it clears a notch. Shows the active
          app's identity while an app is fullscreen, else the shell brand. */}
      <div className="vmob-bar safe-pt safe-px shrink-0">
        <div className="px-3 h-11 flex items-center justify-between gap-2">
          {/* flex-1 on the title button: the trailing status cluster is shrink-0, so
              without it the button sizes to content inside justify-between and is the
              first thing squeezed. At 390px that rendered "Assistant" as "Ass…" —
              useless as identity, and an unfortunate thing to print. Claiming the
              leftover space puts the app's name ahead of the badges. */}
          {showApp ? (
            <button
              onClick={() => setView('home')}
              aria-label="Back to home"
              className="focus-primary -ml-1.5 h-9 pl-1.5 pr-3 flex flex-1 items-center gap-2 rounded-[var(--radius-md)] text-[color:var(--text-secondary)] active:bg-[color:var(--bg-hover)] transition-colors min-w-0"
            >
              <svg viewBox="0 0 16 16" className="w-4 h-4 shrink-0 text-[color:var(--text-tertiary)]" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" strokeLinejoin="round"><path d="M10 3L5 8l5 5" /></svg>
              <AppIcon id={activeWin.appId} size={18} color={undefined} style={undefined} />
              <span className="text-[13px] font-semibold tracking-[-0.01em] truncate">{activeWin.title}</span>
            </button>
          ) : (
            <div className="flex items-center gap-2 pl-1">
              <span className="w-[7px] h-[7px] rounded-full shrink-0" style={{ background: 'var(--accent)' }} aria-hidden="true" />
              <span className="text-[13px] font-semibold tracking-[-0.01em] text-[color:var(--text-secondary)]">vulos</span>
            </div>
          )}
          <div className="flex items-center gap-1.5 shrink-0">
            <TrustBadge compact />
            <LifePulse compact />
          </div>
        </div>
      </div>

      {/* A TOP-edge dock sits directly under the status bar, never above it: the
          status bar owns the notch inset, and the OS's own notification shade
          owns the very top edge in both the PWA and the APK (no web API sees
          that touch — roadmap/MOBILE-SHELL.md §2). A tap target here is still
          reachable, which is why the profile may choose it; a top dock is simply
          not the default, and nothing the shell NEEDS is placed above it. */}
      {profile.edge === 'top' && dock}

      {/* Legible-trust transparency panel — opened from the TrustBadge */}
      <TransparencyPanel />

      {/* WAVE-12: unified ⌘K command palette */}
      <CommandPalette />

      {/* Main content */}
      <div className="flex-1 relative overflow-hidden">
        {/* HOME — a real phone home screen: app grid + a resting ask bar that
            hands the surface to the assistant on tap (MOBILE-10). Kept mounted
            (hidden behind apps) so both the grid's scroll position and any
            assistant conversation survive app switches. */}
        <div className={`absolute inset-0 flex flex-col ${view === 'home' ? '' : 'hidden'}`}>
          {/* profile.assistant lands HERE and not in the dock. On desktop the
              flag shows a dock button because the assistant is a side panel;
              on a phone it is a full-screen surface reached from Home, so the
              honest expression of "show the assistant" is the resting ask bar. */}
          <MobileHome assistant={profile.assistant} />
        </div>

        {/* APP STACK — every open window mounted; only the active one is shown.
            Hidden (not unmounted) windows keep their state. */}
        {windows.map(win => (
          <div
            key={win.id}
            className={`absolute inset-0 ${showApp && win.id === activeWin.id ? 'anim-sheet-up' : 'hidden'}`}
            aria-hidden={!(showApp && win.id === activeWin.id)}
          >
            <MobileAppFrame win={win} />
          </div>
        ))}

        {/* APP SWITCHER — phone-style overview of running apps (MOBILE-09).
            Cards are identity cards, NOT live second mounts of each app; the
            previous version doubled every running app's memory at exactly the
            moment the user opened it because memory was tight. See
            mobile/AppSwitcher.tsx. */}
        {view === 'switcher' && (
          <AppSwitcher onOpen={openApp} onHome={() => setView('home')} />
        )}
      </div>

      {/* The dock — the single navigation surface. Its edge, tile size, style,
          alignment and app items all come from the MOBILE dock profile; the
          safe-area padding follows the edge it lands on, so the home indicator
          never overlaps a target. Every slot is ≥44px at 390px even with the
          maximum eight (see mobileDock.ts MOBILE_MAX_SLOTS). */}
      {profile.edge !== 'top' && dock}

      {/* Launchpad overlay */}
      <Launchpad />
      <Toasts />
    </div>
  )
}

// MobileAppFrame — renders a single window FULLSCREEN. Mirrors the desktop
// Window content branches (component · srcDoc html · url iframe) but without any
// title bar / resize chrome (the shell status bar owns the app identity).
function MobileAppFrame({ win }: { win: ShellWindow }) {
  const frameRef = useRef(null)

  useEffect(() => {
    if (win.html || !frameRef.current || !win.appId) return undefined
    return attachAppBridge(frameRef.current, { appId: win.appId, frameUrl: win.url })
  }, [win.appId, win.url, win.html])

  const src = win.html ? undefined : (win.appId ? appFrameSrc(win.url, win.appId) : win.url)

  if (win.component) {
    return <div className="vmob-frame absolute inset-0 overflow-y-auto overscroll-contain [-webkit-overflow-scrolling:touch]">{win.component}</div>
  }
  return (
    <iframe
      ref={frameRef}
      src={src}
      srcDoc={win.html || undefined}
      title={win.title}
      className="vmob-frame absolute inset-0 w-full h-full border-0"
      sandbox={win.html ? 'allow-scripts' : iframeSandboxForURL(win.url)}
      referrerPolicy="no-referrer"
    />
  )
}

function slotKey(slot: MobileSlot): string {
  return slot.kind === 'app' ? `app:${slot.appId}` : slot.kind
}

interface DockSlotProps {
  slot: MobileSlot
  glyph: number
  plate: number
  view: MobileView
  windowCount: number
  running: boolean
  onHome: () => void
  onSwitcher: () => void
  onLibrary: () => void
  onApp: (appId: string) => void
}

// One dock slot. System destinations carry a glyph and a WORD; app tiles carry
// the app's own mark and a running dot, with the name on the accessible name.
//
// That asymmetry is the point, not an oversight. A destination is recognised by
// its label; an app is recognised by its mark — which is exactly how the desktop
// dock draws app tiles too (shell/Dock.tsx has no visible captions on them). And
// at the maximum eight slots a 390px phone gives each column ~48px, which is a
// fine touch target and far too narrow for a legible app name; a truncated dock
// label is worse than none (roadmap/MOBILE-SHELL.md §4.1). Both kinds reserve
// the same caption row so the row of marks stays on one line.
function DockSlot({ slot, glyph, plate, view, windowCount, running, onHome, onSwitcher, onLibrary, onApp }: DockSlotProps) {
  if (slot.kind === 'app') {
    const app = getAppById(slot.appId)
    const name = app?.name || slot.appId
    return (
      <button
        onClick={() => onApp(slot.appId)}
        aria-label={name}
        aria-pressed={running}
        data-dock-slot={`app:${slot.appId}`}
        className="vmob-dock-slot touch-target"
      >
        <span className="vmob-dock-plate" style={{ width: plate, height: plate }}>
          <AppIconTile id={slot.appId} size={plate} unicode={app?.icon} />
        </span>
        <span className="vmob-dock-caption">
          <span className="vmob-dock-dot" data-running={running ? 'true' : 'false'} aria-hidden="true" />
        </span>
      </button>
    )
  }

  const spec = SYSTEM_SLOTS[slot.kind]
  const active = slot.kind === 'home' ? view === 'home' : slot.kind === 'switcher' ? view === 'switcher' : false
  const disabled = slot.kind === 'switcher' && windowCount === 0
  const badge = slot.kind === 'switcher' ? windowCount || null : null
  const onClick = slot.kind === 'home' ? onHome : slot.kind === 'switcher' ? onSwitcher : onLibrary

  return (
    <button
      onClick={onClick}
      disabled={disabled}
      aria-label={spec.label}
      aria-current={active ? 'page' : undefined}
      data-dock-slot={slot.kind}
      data-active={active ? 'true' : undefined}
      className="vmob-dock-slot touch-target"
    >
      <span className="vmob-dock-plate vmob-dock-glyph" data-active={active ? 'true' : undefined} style={{ width: plate, height: plate }}>
        <svg viewBox="0 0 20 20" width={glyph} height={glyph} fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" strokeLinejoin="round">{spec.path}</svg>
        {badge ? (
          <span className="vmob-dock-badge accent-bg tabular-nums">{badge}</span>
        ) : null}
      </span>
      <span className="vmob-dock-caption">{spec.label}</span>
    </button>
  )
}

const SYSTEM_SLOTS: Record<'home' | 'switcher' | 'library', { label: string; path: ReactNode }> = {
  home: {
    label: 'Home',
    path: <path d="M3 9.5L10 3.5l7 6M5 8.5V16a.5.5 0 00.5.5H8v-4.5h4V16.5h2.5a.5.5 0 00.5-.5V8.5" />,
  },
  switcher: {
    label: 'Apps',
    path: (
      <>
        <rect x="2.5" y="2.5" width="6.2" height="6.2" rx="1.8" />
        <rect x="11.3" y="2.5" width="6.2" height="6.2" rx="1.8" />
        <rect x="2.5" y="11.3" width="6.2" height="6.2" rx="1.8" />
        <rect x="11.3" y="11.3" width="6.2" height="6.2" rx="1.8" />
      </>
    ),
  },
  library: {
    label: 'Library',
    path: <><circle cx="10" cy="10" r="7.2" /><path d="M10 6.5v7M6.5 10h7" /></>,
  },
}
