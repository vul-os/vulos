import { useEffect, useRef, createElement, lazy, Suspense } from 'react'
import { useShell } from '../providers/ShellProvider'
import LifePulse from '../core/SystemPulse'
import Portal from '../core/Portal'
import Window from '../shell/Window'
import Launchpad from '../shell/Launchpad'
import MissionControl, { useMissionControlLayout } from '../shell/MissionControl'
import Home from '../shell/home/Home'
import Toasts from '../shell/Toasts'
import DesktopContextMenu from '../shell/DesktopContextMenu'
import { useWallpaper, DEFAULT_WALLPAPER } from '../core/useWallpaper.jsx'
import { useTheme } from '../core/ThemeProvider'
import AIFirstRun from '../core/AIFirstRun'
import PublicAppsManager from '../core/PublicAppsManager'
import IncomingCall from '../builtin/peering/call/IncomingCall'
import PublicAppBanner from '../shell/PublicAppBanner'
import TrustBadge from '../shell/TrustBadge'
import TransparencyPanel from '../shell/TransparencyPanel'
import CommandPalette from '../shell/CommandPalette'
import CalendarWidget from '../shell/CalendarWidget'
import Dock from '../shell/Dock'
import { useWindowShortcuts } from '../shell/useWindowShortcuts'

const StreamViewer = lazy(() => import('../builtin/stream/StreamViewer'))

function DesktopIndicator() {
  const { desktops, activeDesktop, removeDesktop } = useShell()
  const list = Object.values(desktops)
  if (list.length <= 1) return null
  const idx = list.findIndex(d => d.id === activeDesktop)

  return (
    <div className="flex items-center gap-1 ml-2">
      <span className="text-[11px] text-neutral-500 mr-0.5">Desktop {idx + 1}</span>
      <button
        onClick={() => removeDesktop(activeDesktop)}
        title="Close desktop (windows move to next)"
        aria-label="Close desktop"
        className="focus-primary w-4 h-4 flex items-center justify-center rounded text-neutral-600 hover:text-red-400 hover:bg-neutral-800/60 transition-colors text-[10px]"
      >
        {'\u00D7'}
      </button>
    </div>
  )
}

export default function DesktopCanvas() {
  const { windows, allWindows, chatOpen, toggleMissionControl, toggleLaunchpad, toggleChat, missionControlOpen, setMissionControl, focusWindow, minimizeWindow, openWindow } = useShell()
  const mcLayout = useMissionControlLayout(windows.filter(w => !w.minimized), missionControlOpen)
  const { wallpaper } = useWallpaper()
  const { isDark } = useTheme()
  // Keyboard-first window management (tile / cycle / close).
  useWindowShortcuts()

  // xdg-open: listen for browser open events and focus/open browser window
  const windowsRef = useRef(windows)
  // eslint-disable-next-line react-hooks/refs
  windowsRef.current = windows
  useEffect(() => {
    const wsUrl = `${location.protocol === 'https:' ? 'wss' : 'ws'}://${location.host}/api/notifications/stream`
    let alive = true
    function connect() {
      if (!alive) return
      const ws = new WebSocket(wsUrl)
      ws.onmessage = (e) => {
        try {
          const msg = JSON.parse(e.data)
          if (msg.source !== 'xdg-open') return
          const browserWin = windowsRef.current.find(w => w.appId === 'browser-stream' || w.appId?.startsWith('browser-stream'))
          if (browserWin) {
            focusWindow(browserWin.id)
          } else {
            // Create the per-user streaming Chrome session, then connect the
            // viewer to the returned session ID. Previously this opened a
            // StreamViewer for a hardcoded 'browser' session that nothing
            // created (orphaned); now /api/browser/launch actually spawns it.
            const fallback = createElement('div', { className: 'flex items-center justify-center h-full bg-neutral-950 text-neutral-500 text-sm' },
              createElement('span', { className: 'flex items-center gap-2' },
                createElement('span', { className: 'w-4 h-4 spinner' }),
                'Starting Chrome...'
              )
            )
            fetch('/api/browser/launch', { method: 'POST' })
              .then(r => r.ok ? r.json() : null)
              .catch(() => null)
              .then(data => {
                const sessionId = (data && data.id) || 'browser'
                openWindow({
                  appId: 'browser-stream',
                  title: 'Streaming Chrome',
                  icon: 'chrome',
                  component: createElement(Suspense, { fallback },
                    createElement(StreamViewer, { sessionId })
                  ),
                })
              })
          }
        } catch { /* noop */ }
      }
      ws.onclose = () => { if (alive) setTimeout(connect, 3000) }
      ws.onerror = () => ws.close()
    }
    connect()
    return () => { alive = false }
  }, [focusWindow, openWindow])

  // First-boot background. Use a SOLID color (not a gradient) for the
  // placeholder — Chromium under SwiftShader (the bare-metal software-GPU
  // path) doesn't reliably composite radial-gradients to virtio-gpu's
  // pixman framebuffer; they render transparent and the outer wrapper
  // (bg-neutral-950 ≈ #0a0a0a) shows through, which is visually identical
  // to a crashed kiosk. Solid colors always work.
  const placeholderBg = isDark ? '#1f0e3d' : '#e9deff'

  return (
    <div
      className="fixed inset-0 overflow-hidden"
      style={{ background: isDark ? '#1f0e3d' : '#e9deff' }}
    >
      {/* Desktop wallpaper — always visible behind windows */}
      <div
        data-desktop-bg
        className="absolute inset-0 overflow-hidden flex items-center justify-center transition-colors duration-500"
        style={{ background: wallpaper ? (isDark ? '#0c0c0c' : '#f0f0f0') : placeholderBg }}
      >
        {wallpaper ? (
          <img src={wallpaper} alt="" className="block w-full h-full object-cover" />
        ) : (
          <div className="flex flex-col items-center gap-3 select-none">
            <img src={DEFAULT_WALLPAPER} alt="" className="w-24 h-24" style={{ opacity: isDark ? 0.85 : 0.55, filter: isDark ? 'brightness(1.6)' : 'none' }} />
            <div style={{ opacity: isDark ? 0.95 : 0.85 }}>
              <div className="text-center text-3xl font-light tracking-[0.3em]" style={{ color: isDark ? '#fff' : '#1a1a1a' }}>Vulos</div>
              <div className="text-center text-[10px] tracking-[0.2em] mt-1" style={{ color: isDark ? '#c9b6ff' : '#5a3aa3' }}>alpha</div>
            </div>
          </div>
        )}
      </div>

      {/* Menu bar */}
      <div className={`absolute top-0 left-0 right-0 z-40 h-8 flex items-center justify-between px-1 backdrop-blur-xl ${isDark ? 'bg-neutral-800/70 border-b border-neutral-700/40' : 'bg-neutral-900/60 border-b border-neutral-800/30'}`}>
        <div className="flex items-center">
          <LifePulse />
          <DesktopIndicator />
          {/* Launchpad button — rocket icon */}
          <button
            onClick={toggleLaunchpad}
            title="Applications"
            aria-label="Applications"
            className="focus-primary ml-1 w-6 h-6 flex items-center justify-center rounded hover:bg-neutral-700/50 transition-colors"
          >
            <svg viewBox="0 0 16 16" className="w-3.5 h-3.5 text-neutral-400" fill="none" stroke="currentColor" strokeWidth="1.3">
              <path d="M8 1.5c0 0-3 3-3 7.5h6c0-4.5-3-7.5-3-7.5z" fill="currentColor" opacity="0.5" stroke="none" />
              <path d="M8 1.5c0 0-3 3-3 7.5h6c0-4.5-3-7.5-3-7.5z" />
              <path d="M5 9l-1.5 3L5 11" />
              <path d="M11 9l1.5 3L11 11" />
              <path d="M6.5 12.5h3" strokeLinecap="round" />
              <circle cx="8" cy="6.5" r="1" fill="currentColor" stroke="none" opacity="0.7" />
            </svg>
          </button>
          {/* Mission Control button — two staggered windows */}
          <button
            onClick={toggleMissionControl}
            title="Mission Control (F3)"
            aria-label="Mission Control"
            className="focus-primary ml-0.5 w-6 h-6 flex items-center justify-center rounded hover:bg-neutral-700/50 transition-colors"
          >
            <svg viewBox="0 0 16 16" className="w-3.5 h-3.5 text-neutral-400" fill="none" stroke="currentColor" strokeWidth="1.2">
              <rect x="1" y="4" width="8" height="6" rx="1" fill="currentColor" opacity="0.25" />
              <rect x="1" y="4" width="8" height="6" rx="1" />
              <line x1="1" y1="6" x2="9" y2="6" />
              <rect x="7" y="1.5" width="8" height="6" rx="1" fill="currentColor" opacity="0.15" />
              <rect x="7" y="1.5" width="8" height="6" rx="1" />
              <line x1="7" y1="3.5" x2="15" y2="3.5" />
            </svg>
          </button>
        </div>
        <div className="flex items-center">
          {/* Always-on sovereignty indicator — AI tier + "what leaves this box" +
              at-rest lock; click opens the transparency panel. */}
          <TrustBadge />
          <div className="w-px h-3.5 bg-neutral-700/40 mx-1" />
          {/* Chat toggle */}
          <button
            onClick={toggleChat}
            title="Chat (Ctrl+K)"
            aria-label="Chat"
            aria-pressed={chatOpen}
            style={chatOpen ? { background: 'var(--accent-soft)', color: 'var(--accent)' } : undefined}
            className={`focus-primary mr-1 w-6 h-6 flex items-center justify-center rounded transition-colors
              ${chatOpen ? '' : 'hover:bg-neutral-700/50 text-neutral-400'}`}
          >
            <svg viewBox="0 0 16 16" className="w-3.5 h-3.5">
              <path d="M2 3a2 2 0 012-2h8a2 2 0 012 2v6a2 2 0 01-2 2H6l-3 3V11H4a2 2 0 01-2-2V3z" fill="currentColor" opacity="0.7" />
            </svg>
          </button>
          {/* Fullscreen toggle */}
          <button
            onClick={() => {
              if (document.fullscreenElement) document.exitFullscreen()
              else document.documentElement.requestFullscreen()
            }}
            title="Toggle fullscreen"
            aria-label="Toggle fullscreen"
            className="focus-primary mr-1 w-6 h-6 flex items-center justify-center rounded hover:bg-neutral-700/50 text-neutral-400 transition-colors"
          >
            <svg viewBox="0 0 16 16" className="w-3.5 h-3.5" fill="none" stroke="currentColor" strokeWidth="1.3" strokeLinecap="round">
              <path d="M2 5V2h3M11 2h3v3M14 11v3h-3M5 14H2v-3" />
            </svg>
          </button>
          <LifePulse compact />
        </div>
      </div>

      {/* PUBWEB-06: amber banner when focused app is publicly visible */}
      <PublicAppBanner />

      {/* Windows area — render ALL windows persistently, hide inactive desktops via CSS */}
      <div className="absolute inset-0 pt-8">
        {/* WAVE-11: Home — the proactive default surface. It sits as the desktop
            backdrop BELOW all windows, so it's what you see first (and return to
            when you close everything), while apps stack over it. Only shown when
            the active desktop has no open windows, so it never steals clicks from
            a floating app. */}
        {windows.filter(w => !w.minimized).length === 0 && (
          <div className="absolute inset-0">
            <Home />
          </div>
        )}
        {allWindows.map(win => {
          const mc = missionControlOpen && win._visible && !win.minimized ? mcLayout[win.id] : null
          return (
            <div
              key={win.id}
              style={mc ? {
                position: 'absolute',
                left: 0, top: 0,
                transform: `translate(${mc.x}px, ${mc.y}px) scale(${mc.scale})`,
                transformOrigin: 'top left',
                zIndex: 51,
                transition: 'transform 0.35s cubic-bezier(0.4, 0, 0.2, 1)',
                cursor: 'pointer',
              } : undefined}
              onClick={mc ? (e) => {
                e.stopPropagation()
                focusWindow(win.id)
                setMissionControl(false)
              } : undefined}
            >
              {mc && (
                <button
                  onClick={(e) => { e.stopPropagation(); minimizeWindow(win.id) }}
                  aria-label={`Minimize ${win.title || 'window'}`}
                  className="focus-primary absolute -top-2 -right-2 w-5 h-5 rounded-full bg-neutral-700/90 text-neutral-300 hover:bg-red-500 hover:text-white text-xs flex items-center justify-center z-[53] transition-colors"
                  style={{ transform: `scale(${1/mc.scale})`, transformOrigin: 'center' }}
                >
                  {'\u00D7'}
                </button>
              )}
              <Window
                win={{ ...win, minimized: win.minimized || !win._visible }}
                pointerBlock={!!mc}
              />
            </div>
          )
        })}
      </div>

      {/* Ambient Calendar widget — an always-on "what's next" glance on the
          RHS (macOS style). It fills the gap when open windows cover the Home
          backdrop: Home already surfaces the full agenda when it's the visible
          backdrop (no windows), so the widget only mounts once a window covers
          Home — keeping the agenda visible either way without double-rendering
          the same events. Also hidden while the chat panel or Mission Control
          occupies the same right-side z-30 layer, so they never overlap. */}
      {windows.filter(w => !w.minimized).length > 0 && !chatOpen && !missionControlOpen && <CalendarWidget />}

      {/* Chat panel — right side */}
      {chatOpen && (
        <div className="absolute top-8 right-0 bottom-0 w-[380px] z-30">
          <Portal />
        </div>
      )}

      {/* Dock / taskbar — restores minimized windows & fast-switches. Hidden
          while Mission Control is up (its backdrop already covers this z-layer). */}
      {!missionControlOpen && <Dock />}

      {/* WAVE-12: unified ⌘K command palette (apps · mail · actions · ask) */}
      <CommandPalette />

      {/* Launchpad overlay */}
      <Launchpad />

      {/* Mission Control overlay */}
      <MissionControl />

      {/* Toast notifications */}
      <Toasts />

      {/* Native window context menu (only renders on native mode) */}
      <DesktopContextMenu />

      {/* First-run AI chat introduction (one-time, persisted via localStorage) */}
      <AIFirstRun />
      {/* Public apps manager popover — listens for vulos:open-public-apps */}
      <PublicAppsManager />
      {/* Incoming call modal + call history — shell-wide, z-[300] (PEER-24) */}
      <IncomingCall />
      {/* Legible-trust transparency panel — opened from the TrustBadge */}
      <TransparencyPanel />
    </div>
  )
}
