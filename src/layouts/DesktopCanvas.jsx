import { useEffect, useRef, createElement, lazy, Suspense } from 'react'
import { useShell } from '../providers/ShellProvider'
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
import AdoptPortManager from '../core/AdoptPortManager'
import IncomingCall from '../builtin/peering/call/IncomingCall'
import PublicAppBanner from '../shell/PublicAppBanner'
import TransparencyPanel from '../shell/TransparencyPanel'
import CommandPalette from '../shell/CommandPalette'
import CalendarWidget from '../shell/CalendarWidget'
import Dock from '../shell/Dock'
import TopBar from '../shell/TopBar'
import { useWindowShortcuts } from '../shell/useWindowShortcuts'

const StreamViewer = lazy(() => import('../builtin/stream/StreamViewer'))

export default function DesktopCanvas() {
  const { windows, allWindows, chatOpen, missionControlOpen, setMissionControl, focusWindow, minimizeWindow, openWindow } = useShell()
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
            const fallback = createElement('div', { className: 'vwin-content flex items-center justify-center h-full text-[color:var(--text-tertiary)] text-sm' },
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

      {/* Desktop top bar / menu bar — the shell's premium chrome (own component). */}
      <TopBar />

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
                  className="vshell-pip focus-primary absolute -top-2 -right-2 w-5 h-5 rounded-full text-xs flex items-center justify-center z-[53]"
                  style={{ transform: `scale(${1/mc.scale})`, transformOrigin: 'center' }}
                >
                  {'×'}
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
      {/* Adopt-a-port manager popover — listens for vulos:open-adopt-port */}
      <AdoptPortManager />
      {/* Incoming call modal + call history — shell-wide, z-[300] (PEER-24) */}
      <IncomingCall />
      {/* Legible-trust transparency panel — opened from the TrustBadge */}
      <TransparencyPanel />
    </div>
  )
}
