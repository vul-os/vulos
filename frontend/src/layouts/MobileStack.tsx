import { useEffect, useRef, useState, type ReactNode } from 'react'
import { useShell, type ShellWindow } from '../providers/ShellProvider'
import LifePulse from '../core/SystemPulse'
import AppIcon from '../core/AppIcons'
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
  const { windows, activeWindow, focusWindow, toggleLaunchpad } = useShell()
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
          <MobileHome />
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

      {/* Bottom dock — the single navigation surface. Safe-area padded so the
          home indicator never overlaps the targets; every target ≥44px. */}
      <nav
        aria-label="System navigation"
        className="vmob-dock safe-pb safe-px shrink-0 flex items-stretch pt-1"
      >
        <DockButton
          label="Home"
          active={view === 'home'}
          onClick={() => setView('home')}
        >
          <svg viewBox="0 0 20 20" className="w-[22px] h-[22px]" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" strokeLinejoin="round"><path d="M3 9.5L10 3.5l7 6M5 8.5V16a.5.5 0 00.5.5H8v-4.5h4V16.5h2.5a.5.5 0 00.5-.5V8.5" /></svg>
        </DockButton>
        <DockButton
          label="Apps"
          active={view === 'switcher'}
          badge={windows.length || null}
          disabled={windows.length === 0}
          onClick={() => setView(v => (v === 'switcher' ? (activeWin ? 'app' : 'home') : 'switcher'))}
        >
          <svg viewBox="0 0 20 20" className="w-[22px] h-[22px]" fill="none" stroke="currentColor" strokeWidth="1.7">
            <rect x="2.5" y="2.5" width="6.2" height="6.2" rx="1.8" />
            <rect x="11.3" y="2.5" width="6.2" height="6.2" rx="1.8" />
            <rect x="2.5" y="11.3" width="6.2" height="6.2" rx="1.8" />
            <rect x="11.3" y="11.3" width="6.2" height="6.2" rx="1.8" />
          </svg>
        </DockButton>
        <DockButton label="Library" onClick={toggleLaunchpad}>
          <svg viewBox="0 0 20 20" className="w-[22px] h-[22px]" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round"><circle cx="10" cy="10" r="7.2" /><path d="M10 6.5v7M6.5 10h7" /></svg>
        </DockButton>
      </nav>

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

interface DockButtonProps {
  children: ReactNode
  label: string
  active?: boolean
  badge?: number | null
  disabled?: boolean
  onClick: () => void
}

function DockButton({ children, label, active, badge, disabled, onClick }: DockButtonProps) {
  return (
    <button
      onClick={onClick}
      disabled={disabled}
      aria-label={label}
      aria-current={active ? 'page' : undefined}
      className={`touch-target flex-1 flex flex-col items-center justify-center gap-1 py-1.5 select-none transition-colors duration-150
        ${disabled ? 'opacity-30' : active ? 'text-[color:var(--accent-text,var(--accent))]' : 'text-[color:var(--text-tertiary)] active:text-[color:var(--text-primary)]'}`}
    >
      <span
        className="relative flex items-center justify-center h-8 min-w-[3.25rem] rounded-full transition-colors duration-150"
        style={active ? { background: 'var(--accent-soft)' } : undefined}
      >
        {children}
        {badge ? (
          <span className="absolute -top-1 right-1.5 min-w-[1rem] h-4 px-1 accent-bg rounded-full text-[12px] leading-none text-[color:var(--accent-contrast)] font-semibold flex items-center justify-center tabular-nums">{badge}</span>
        ) : null}
      </span>
      <span className="text-[12px] leading-none font-medium tracking-[0.01em]">{label}</span>
    </button>
  )
}
