import { useEffect, useRef, useState } from 'react'
import { useShell } from '../providers/ShellProvider'
import LifePulse from '../core/SystemPulse'
import Portal from '../core/Portal'
import AppIcon from '../core/AppIcons'
import Launchpad from '../shell/Launchpad'
import Toasts from '../shell/Toasts'
import TrustBadge from '../shell/TrustBadge'
import TransparencyPanel from '../shell/TransparencyPanel'
import CommandPalette from '../shell/CommandPalette'
import { iframeSandboxForURL } from '../core/AppOrigins'
import { attachAppBridge, appFrameSrc } from '../core/AppBridge'
import '../shell/shell-chrome.css'

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

export default function MobileStack() {
  const { windows, activeWindow, focusWindow, toggleLaunchpad } = useShell()
  // view: 'home' (assistant + glance) | 'app' (fullscreen active app) | 'switcher'
  const [view, setView] = useState('home')
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

  const openApp = (id) => { focusWindow(id); setView('app') }

  return (
    <div data-shell="mobile" className="vmob-root fixed inset-0 flex flex-col overflow-hidden">
      {/* Status bar — safe-area padded so it clears a notch. Shows the active
          app's identity while an app is fullscreen, else the shell brand. */}
      <div className="vmob-bar safe-pt safe-px shrink-0">
        <div className="px-3 h-11 flex items-center justify-between gap-2">
          {showApp ? (
            <button
              onClick={() => setView('home')}
              aria-label="Back to home"
              className="focus-primary -ml-1.5 h-9 pl-1.5 pr-3 flex items-center gap-2 rounded-[var(--radius-md)] text-[color:var(--text-secondary)] active:bg-[color:var(--bg-hover)] transition-colors min-w-0"
            >
              <svg viewBox="0 0 16 16" className="w-4 h-4 shrink-0 text-[color:var(--text-tertiary)]" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" strokeLinejoin="round"><path d="M10 3L5 8l5 5" /></svg>
              <AppIcon id={activeWin.appId} size={18} />
              <span className="text-[13px] font-semibold tracking-[-0.01em] truncate">{activeWin.title}</span>
            </button>
          ) : (
            <div className="flex items-center gap-2 pl-1">
              <span className="w-[7px] h-[7px] rounded-full shrink-0" style={{ background: 'var(--accent)' }} aria-hidden="true" />
              <span className="text-[13px] font-semibold tracking-[-0.01em] text-[color:var(--text-secondary)]">vula</span>
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
        {/* HOME — assistant + glance. Kept mounted (hidden behind apps) so the
            assistant conversation survives app switches. */}
        <div className={`absolute inset-0 flex flex-col ${view === 'home' ? '' : 'hidden'}`}>
          {windows.length === 0 && (
            <div className="shrink-0 flex flex-col items-center justify-center pt-8 pb-5">
              <LifePulse />
            </div>
          )}
          <div className="flex-1 min-h-0 flex flex-col">
            <Portal mode="fullscreen" />
          </div>
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

        {/* APP SWITCHER — phone-style overview of running apps. */}
        {view === 'switcher' && (
          <MobileSwitcher onOpen={openApp} onHome={() => setView('home')} />
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
function MobileAppFrame({ win }) {
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

// MobileSwitcher — full-height overlay listing every running app as a large,
// tappable card with a close affordance. This is the mobile replacement for the
// desktop window stack / dock.
function MobileSwitcher({ onOpen, onHome }) {
  const { windows, closeWindow } = useShell()

  return (
    <div className="vmob-switcher absolute inset-0 z-10 overflow-y-auto overscroll-contain anim-sheet-up [-webkit-overflow-scrolling:touch]">
      {/* Grab handle + header — signals the overview is a dismissible sheet. */}
      <div className="safe-px px-4 pt-2.5 pb-1">
        <div className="mx-auto mb-3.5 h-1 w-9 rounded-full" style={{ background: 'var(--border-emphasis)' }} aria-hidden="true" />
        <div className="flex items-baseline justify-between">
          <h2 className="text-[15px] font-semibold tracking-[-0.01em] text-[color:var(--text-primary)]">Running apps</h2>
          <span className="text-xs font-medium text-[color:var(--text-faint)] tabular-nums">{windows.length} open</span>
        </div>
      </div>
      {windows.length === 0 ? (
        <div className="flex flex-col items-center justify-center h-64 text-center px-6">
          <p className="text-sm text-[color:var(--text-tertiary)]">No apps are running</p>
          <button onClick={onHome} className="focus-primary mt-3 text-xs font-medium accent-text active:opacity-70 transition-opacity">Back to home</button>
        </div>
      ) : (
        <div className="safe-px p-4 grid grid-cols-1 sm:grid-cols-2 gap-3.5">
          {windows.map(win => (
            <div key={win.id} className="vmob-card rounded-[var(--radius-xl)] overflow-hidden transition-transform duration-200 active:scale-[0.985]">
              <div className="flex items-center gap-2.5 px-3 h-12">
                <AppIcon id={win.appId} size={20} />
                <span className="text-[13px] font-medium text-[color:var(--text-secondary)] truncate flex-1">{win.title}</span>
                <button
                  onClick={() => closeWindow(win.id)}
                  aria-label={`Close ${win.title}`}
                  className="focus-primary touch-target -mr-1.5 flex items-center justify-center rounded-full text-[color:var(--text-tertiary)] active:text-[color:var(--status-danger)] active:bg-[color:var(--status-danger-soft)] transition-colors"
                >
                  <svg viewBox="0 0 16 16" className="w-4 h-4" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round"><path d="M4 4l8 8M12 4l-8 8" /></svg>
                </button>
              </div>
              <button
                onClick={() => onOpen(win.id)}
                aria-label={`Switch to ${win.title}`}
                className="block w-full text-left"
              >
                <div className="vmob-card-body h-44 relative pointer-events-none overflow-hidden">
                  <MobileAppFrame win={win} />
                </div>
              </button>
            </div>
          ))}
        </div>
      )}
    </div>
  )
}

function DockButton({ children, label, active, badge, disabled, onClick }) {
  return (
    <button
      onClick={onClick}
      disabled={disabled}
      aria-label={label}
      aria-current={active ? 'page' : undefined}
      className={`touch-target flex-1 flex flex-col items-center justify-center gap-1 py-1.5 select-none transition-colors duration-150
        ${disabled ? 'opacity-30' : active ? 'text-[color:var(--accent)]' : 'text-[color:var(--text-tertiary)] active:text-[color:var(--text-primary)]'}`}
    >
      <span
        className="relative flex items-center justify-center h-8 min-w-[3.25rem] rounded-full transition-colors duration-150"
        style={active ? { background: 'var(--accent-soft)' } : undefined}
      >
        {children}
        {badge ? (
          <span className="absolute -top-1 right-1.5 min-w-[1rem] h-4 px-1 accent-bg rounded-full text-[9px] leading-none text-[color:var(--accent-contrast)] font-semibold flex items-center justify-center tabular-nums">{badge}</span>
        ) : null}
      </span>
      <span className="text-[10px] leading-none font-medium tracking-[0.01em]">{label}</span>
    </button>
  )
}
