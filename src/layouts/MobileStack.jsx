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
        <div className="px-3 h-10 flex items-center justify-between">
          {showApp ? (
            <button
              onClick={() => setView('home')}
              aria-label="Back to home"
              className="focus-primary -ml-1 h-8 px-1.5 flex items-center gap-2 rounded-lg text-[color:var(--text-secondary)] hover:bg-[color:var(--bg-hover)] transition-colors min-w-0"
            >
              <svg viewBox="0 0 16 16" className="w-4 h-4 shrink-0 text-[color:var(--text-tertiary)]" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round"><path d="M10 3L5 8l5 5" /></svg>
              <AppIcon id={activeWin.appId} size={16} />
              <span className="text-sm font-medium truncate">{activeWin.title}</span>
            </button>
          ) : (
            <div className="flex items-center gap-2">
              <img src="/vulos.png" alt="" className="w-4 h-4 opacity-70" />
              <span className="text-sm font-semibold text-[color:var(--text-secondary)]">vula</span>
            </div>
          )}
          <div className="flex items-center gap-2">
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
            <div className="shrink-0 flex flex-col items-center justify-center pt-8 pb-4">
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
        className="vmob-dock safe-pb safe-px shrink-0 flex items-stretch"
      >
        <DockButton
          label="Home"
          active={view === 'home'}
          onClick={() => setView('home')}
        >
          <svg viewBox="0 0 16 16" className="w-5 h-5"><path d="M8 1.5L1.5 7H3v6.5a.5.5 0 00.5.5H6v-4h4v4h2.5a.5.5 0 00.5-.5V7h1.5L8 1.5z" fill="currentColor" /></svg>
        </DockButton>
        <DockButton
          label="Apps"
          active={view === 'switcher'}
          badge={windows.length || null}
          disabled={windows.length === 0}
          onClick={() => setView(v => (v === 'switcher' ? (activeWin ? 'app' : 'home') : 'switcher'))}
        >
          <svg viewBox="0 0 16 16" className="w-5 h-5">
            <rect x="1.5" y="1.5" width="5.5" height="5.5" rx="1.4" fill="currentColor" />
            <rect x="9" y="1.5" width="5.5" height="5.5" rx="1.4" fill="currentColor" opacity="0.6" />
            <rect x="1.5" y="9" width="5.5" height="5.5" rx="1.4" fill="currentColor" opacity="0.6" />
            <rect x="9" y="9" width="5.5" height="5.5" rx="1.4" fill="currentColor" opacity="0.35" />
          </svg>
        </DockButton>
        <DockButton label="Library" onClick={toggleLaunchpad}>
          <svg viewBox="0 0 16 16" className="w-5 h-5"><circle cx="8" cy="8" r="6.2" fill="none" stroke="currentColor" strokeWidth="1.5" /><path d="M8 5v6M5 8h6" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" /></svg>
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
    return <div className="vmob-frame absolute inset-0 overflow-y-auto overscroll-contain">{win.component}</div>
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
    <div className="vmob-switcher absolute inset-0 z-10 overflow-y-auto anim-sheet-up">
      <div className="px-4 pt-4 pb-2 flex items-center justify-between">
        <h2 className="text-sm font-semibold text-[color:var(--text-secondary)]">Running apps</h2>
        <span className="text-xs text-[color:var(--text-faint)]">{windows.length} open</span>
      </div>
      {windows.length === 0 ? (
        <div className="flex flex-col items-center justify-center h-64 text-center px-6">
          <p className="text-sm text-[color:var(--text-tertiary)]">No apps are running</p>
          <button onClick={onHome} className="mt-3 text-xs accent-text hover:underline">Back to home</button>
        </div>
      ) : (
        <div className="p-4 grid grid-cols-1 sm:grid-cols-2 gap-3">
          {windows.map(win => (
            <div key={win.id} className="vmob-card rounded-2xl overflow-hidden">
              <div className="flex items-center gap-2 px-3 h-11">
                <AppIcon id={win.appId} size={18} />
                <span className="text-sm text-[color:var(--text-secondary)] truncate flex-1">{win.title}</span>
                <button
                  onClick={() => closeWindow(win.id)}
                  aria-label={`Close ${win.title}`}
                  className="focus-primary touch-target -mr-2 flex items-center justify-center rounded-lg text-[color:var(--text-tertiary)] hover:text-danger transition-colors"
                >
                  <svg viewBox="0 0 16 16" className="w-4 h-4" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round"><path d="M4 4l8 8M12 4l-8 8" /></svg>
                </button>
              </div>
              <button
                onClick={() => onOpen(win.id)}
                aria-label={`Switch to ${win.title}`}
                className="block w-full text-left"
              >
                <div className="vmob-card-body h-40 relative pointer-events-none">
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
      className={`touch-target flex-1 flex flex-col items-center justify-center gap-0.5 py-2 transition-colors
        ${disabled ? 'opacity-30' : active ? 'text-[color:var(--accent)]' : 'text-[color:var(--text-tertiary)] hover:text-[color:var(--text-primary)]'}`}
    >
      <span className="relative flex items-center justify-center">
        {children}
        {badge ? (
          <span className="absolute -top-1.5 -right-2 min-w-4 h-4 px-1 accent-bg rounded-full text-[9px] text-[color:var(--accent-contrast)] font-semibold flex items-center justify-center">{badge}</span>
        ) : null}
      </span>
      <span className="text-[10px] leading-none">{label}</span>
    </button>
  )
}
