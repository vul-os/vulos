import { useEffect, useRef, createElement, lazy, Suspense } from 'react'
import { useShell } from '../providers/ShellProvider'
import Window from '../shell/Window'
import Spotlight from '../shell/Spotlight'
import MissionControl, { useMissionControlLayout } from '../shell/MissionControl'
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
import DesktopWidgets from '../shell/DesktopWidgets'
import AssistantPanel from '../shell/AssistantPanel'
import Dock from '../shell/Dock'
import TopBar from '../shell/TopBar'
import { useWindowShortcuts } from '../shell/useWindowShortcuts'
import { launchStreamedBrowser } from './streamedBrowser'

const StreamViewer = lazy(() => import('../builtin/stream/StreamViewer'))

/** windowId -> its Mission Control grid cell. Matches the shape
 *  useMissionControlLayout (shell/MissionControl.jsx, untyped JS) actually
 *  builds — `layout[win.id] = {x, y, scale}` — which that file's own
 *  inference can't express (an un-annotated `const layout = {}` types as
 *  `{}`, not an index signature), so it's asserted here rather than left
 *  broken. Not editable in this pass (out of scope). */
interface MissionControlCell { x: number; y: number; scale: number }

export default function DesktopCanvas() {
  const { windows, allWindows, missionControlOpen, setMissionControl, focusWindow, minimizeWindow, openWindow } = useShell()
  const mcLayout = useMissionControlLayout(windows.filter(w => !w.minimized), missionControlOpen) as Record<number, MissionControlCell>
  // useWallpaper()'s return type is `null` per TS's inference of that file's
  // untyped `createContext(null)` (core/useWallpaper.jsx, out of scope) — the
  // real Provider value is always `{ wallpaper, setWallpaper }` once mounted
  // (App.jsx wraps the whole app in WallpaperProvider). Asserted rather than
  // left broken.
  const { wallpaper } = useWallpaper() as unknown as { wallpaper: string | null; setWallpaper: (value: string | null) => void }
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
            // Create the per-user streaming Chromium session, then connect the
            // viewer to the returned session ID.
            //
            // A FAILED launch must say so, not spin forever.
            //
            // This used to be `r.ok ? r.json() : null`, then
            // `(data && data.id) || 'browser'`: a 500 became null, null fell
            // back to the literal session id 'browser' that nothing had
            // created, and the window opened anyway. The user got a window
            // titled Chromium with a "Starting…" spinner that never resolved
            // and no error anywhere — the exact experience reported on a
            // bare-metal box, where /api/browser/launch answers
            // {"error":"chromium not found"} because the binary was never in
            // the rootfs (see scripts/build-sh-packages.txt, now fixed).
            //
            // Shipping the binary does not retire this: a launch can still
            // fail on a box with no free display, no GPU tier, or a killed
            // stream pool. A non-ok response is an ERROR, never data.
            void launchStreamedBrowser({
              openWindow,
              viewer: (sessionId: string) => createElement(
                Suspense,
                {
                  fallback: createElement('div', { className: 'vwin-content flex items-center justify-center h-full text-[color:var(--text-tertiary)] text-sm' },
                    createElement('span', { className: 'flex items-center gap-2' },
                      createElement('span', { className: 'w-4 h-4 spinner' }),
                      'Starting Chromium\u2026'
                    )
                  ),
                },
                createElement(StreamViewer, { sessionId })
              ),
              errorView: (message: string) => createElement(
                'div',
                { className: 'vwin-content flex items-center justify-center h-full p-6 text-center text-[color:var(--text-secondary)] text-sm' },
                createElement('div', { className: 'flex flex-col items-center gap-2 max-w-md' },
                  createElement('span', { className: 'text-[color:var(--text-primary)] font-medium' }, 'Chromium could not start'),
                  createElement('span', null, message)
                )
              ),
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

  // First-boot background. A layered gradient MESH (accent-tinted, cool, with a
  // soft vignette) replaces the old flat purple so first paint reads as a premium
  // OS rather than a placeholder. CRITICAL: this sits ON TOP of a SOLID base
  // colour — Chromium under SwiftShader (the bare-metal software-GPU path)
  // doesn't reliably composite radial-gradients to virtio-gpu's pixman
  // framebuffer; they can render transparent. Because the outer wrapper is a
  // deliberate solid tone (not near-black), a gradient failure degrades to a
  // clean, intentional colour instead of the "crashed kiosk" black we used to hit.
  const solidBase = isDark ? '#0a0b10' : '#f4f6fb'
  const wallpaperMesh = isDark
    ? 'radial-gradient(120% 90% at 12% -8%, color-mix(in srgb, var(--accent) 26%, transparent), transparent 55%),' +
      'radial-gradient(100% 80% at 108% 116%, rgba(18,58,94,0.32), transparent 55%),' +
      'radial-gradient(90% 90% at 92% -12%, rgba(42,29,94,0.26), transparent 60%),' +
      'linear-gradient(160deg, #0a0b10 0%, #08090c 55%, #060709 100%)'
    : 'radial-gradient(120% 90% at 10% -10%, color-mix(in srgb, var(--accent) 18%, transparent), transparent 55%),' +
      'radial-gradient(100% 80% at 110% 115%, rgba(207,224,255,0.55), transparent 55%),' +
      'radial-gradient(90% 90% at 95% -10%, rgba(239,234,255,0.9), transparent 60%),' +
      'linear-gradient(160deg, #fbfcfe 0%, #f4f6fb 55%, #eef1f7 100%)'
  const vignette = isDark
    ? 'radial-gradient(130% 100% at 50% 45%, transparent 55%, rgba(0,0,0,0.30))'
    : 'radial-gradient(130% 100% at 50% 45%, transparent 60%, rgba(15,23,42,0.06))'

  return (
    <div
      className="fixed inset-0 overflow-hidden"
      style={{ background: solidBase }}
    >
      {/* Desktop wallpaper — always visible behind windows. A user-set image wins;
          otherwise the gradient mesh + vignette + brand mark. */}
      <div
        data-desktop-bg
        className="absolute inset-0 overflow-hidden flex items-center justify-center transition-[background] duration-500"
        style={{ background: wallpaper ? solidBase : wallpaperMesh }}
      >
        {wallpaper ? (
          <img src={wallpaper} alt="" className="block w-full h-full object-cover" />
        ) : (
          <>
            {/* Vignette for depth — a whisper, never a frame. */}
            <div aria-hidden="true" className="pointer-events-none absolute inset-0" style={{ background: vignette }} />
            <div className="relative flex flex-col items-center gap-3.5 select-none">
              <img src={DEFAULT_WALLPAPER} alt="" className="w-20 h-20 sm:w-24 sm:h-24" style={{ opacity: isDark ? 0.9 : 0.6, filter: isDark ? 'brightness(1.55)' : 'none' }} />
              <div className="text-center">
                <div className="text-3xl sm:text-4xl font-semibold tracking-[0.28em] pl-[0.28em]" style={{ color: 'var(--text-primary)', opacity: isDark ? 0.95 : 0.9 }}>Vulos</div>
                {/* 12px, not 10. The 19th and last of the sub-floor text nodes
                    the responsive sweep found on the desktop — the other 18 were
                    the widget rail. It reads as small because of the 0.36em
                    tracking and the uppercase, not because of the size, so the
                    floor costs it nothing. */}
                <div className="text-[12px] font-medium uppercase tracking-[0.36em] pl-[0.36em] mt-2" style={{ color: 'var(--accent-text, var(--accent))' }}>alpha</div>
              </div>
            </div>
          </>
        )}
      </div>

      {/* Desktop top bar / menu bar — the shell's premium chrome (own component). */}
      <TopBar />

      {/* PUBWEB-06: amber banner when focused app is publicly visible */}
      <PublicAppBanner />

      {/* Windows area — render ALL windows persistently, hide inactive desktops via CSS.
          The top inset is `--menubar-h`, NOT a hard-coded `pt-8`. This is the
          origin every window is positioned against, and it has to agree with the
          bar's actual height or windows open underneath the bar. Three files
          carried the number 32 independently — `h-8` in shell/TopBar.tsx, this
          padding, and `MENU_BAR_H` in shell/windowTiling.ts — with a
          `--menubar-h` token already in index.css that only two unrelated rules
          in shell-chrome.css read. All of them read it now, which is what makes
          it safe for the bar to become 44px on a coarse pointer (see the
          `@media (pointer: coarse)` block in shell/shell-chrome.css); growing it
          in some of the three and not the rest would have put every window 12px
          under the menu bar on exactly the tablets the change is for. */}
      <div className="absolute inset-0 pt-[var(--menubar-h)]">
        {/* The desktop is the WALLPAPER. Home used to render here as a
            full-bleed backdrop whenever no window was open, which is what made
            this read as a web page rather than an OS: the wallpaper was never
            actually visible, and closing every window did not reveal a desktop,
            it revealed a different page. Home is a window now (app id 'home',
            in the dock and in Spotlight), and what you see behind your windows
            is the wallpaper plus the ambient widget column below. */}
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

      {/* Ambient widget column — clock, "what's next", and what the box is
          trying to tell you. It is desktop furniture: always present, sitting
          on the wallpaper BENEATH every window (see zLayers), so a window that
          reaches it simply covers it. Hidden only under Mission Control, whose
          backdrop owns the whole screen. */}
      {!missionControlOpen && <DesktopWidgets />}

      {/* Assistant — a first-class slide-over rather than a window. This used
          to render core/Portal, an older intent-router prompt box that is not
          the Assistant the OS actually ships; AssistantPanel renders the real
          one and owns only the chrome around it (pop out, close, Esc). */}
      <AssistantPanel />

      {/* Dock / taskbar — restores minimized windows & fast-switches. Hidden
          while Mission Control is up (its backdrop already covers this z-layer). */}
      {!missionControlOpen && <Dock />}

      {/* WAVE-12: unified ⌘K command palette (apps · mail · actions · ask) */}
      <CommandPalette />

      {/* Spotlight — the ⌘Space app launcher (search + full library). Rides the
          shell store's `launchpadOpen` bit, so every existing opener (the menu
          bar, Home's "All apps", the ⌘K palette) drives it unchanged. */}
      <Spotlight />

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
