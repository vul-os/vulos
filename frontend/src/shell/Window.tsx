import {
  Component,
  useCallback,
  useEffect,
  useRef,
  useState,
  type ErrorInfo,
  type PointerEvent as ReactPointerEvent,
  type ReactNode,
} from 'react'
import { useShell, type ShellWindow } from '../providers/ShellProvider'
import AppIcon from '../core/AppIcons'
import { canSpawnNativeWindow, useThinWM } from '../core/useNativeMode'
import { iframeSandboxForURL } from '../core/AppOrigins'
import { attachAppBridge, appFrameSrc } from '../core/AppBridge'
import { tileGeometry, snapZoneForPoint, MENU_BAR_H } from './windowTiling'
import { WINDOW_Z_ACTIVE, WINDOW_Z_INACTIVE, WINDOW_Z_CLOSING_LIFT } from './zLayers'
import './shell-chrome.css'

// ORIGIN-01: the iframe sandbox is derived from the frame URL's ORIGIN, not from
// an app-registry flag. `allow-same-origin` is granted only when that origin is
// distinct from the shell's — i.e. when the app is served from its own origin
// ({app}--{profile}.{base}), where "same origin" means the app itself. An app on
// the shell's own origin (the /app/{id}/ path-prefix fallback) NEVER gets it and
// runs opaque, so it cannot reach window.top, the shell's localStorage/cookies,
// or the gateway's auth headers. Apps that need persistent storage in that mode
// get it over the postMessage bridge (core/AppBridge.js).
//
// The old needsSameOrigin() allowlist is gone: it granted the shell's own origin
// to five first-party apps, which meant any one of them — or anything that could
// impersonate one — owned the shell.

// WINDOW MOTION — read the user's reduced-motion preference at call time so the
// open/close/minimize choreography can collapse to instant state changes.
function prefersReducedMotion(): boolean {
  return typeof window !== 'undefined' && !!window.matchMedia?.('(prefers-reduced-motion: reduce)').matches
}

// Aim the genie/minimize shrink at the dock area (bottom-center of the
// viewport) relative to the window's own box, so the window appears to fly
// toward the dock rather than collapsing in place.
function dockTransformOrigin(win: ShellWindow): string {
  try {
    const vw = window.innerWidth
    const vh = window.innerHeight
    const wx = win.position?.x ?? 0
    const wy = win.position?.y ?? 0
    const ww = win.size?.width ?? 720
    const wh = win.size?.height ?? 500
    // Dock target ≈ bottom-center of the screen.
    const ox = ((vw / 2 - wx) / ww) * 100
    const oy = ((vh - wy) / wh) * 100
    return `${Math.max(-50, Math.min(150, ox))}% ${Math.max(0, Math.min(200, oy))}%`
  } catch {
    return '50% 100%'
  }
}

interface WindowErrorBoundaryProps {
  children: ReactNode
}

interface WindowErrorBoundaryState {
  error: Error | null
}

// WindowErrorBoundary — catches errors thrown by a window's app component so
// a single broken app cannot crash the entire desktop shell.
class WindowErrorBoundary extends Component<WindowErrorBoundaryProps, WindowErrorBoundaryState> {
  constructor(props: WindowErrorBoundaryProps) {
    super(props)
    this.state = { error: null }
  }
  static getDerivedStateFromError(error: Error): WindowErrorBoundaryState { return { error } }
  componentDidCatch(error: Error, info: ErrorInfo) {
    console.error('[window] app error caught by boundary:', error, info?.componentStack)
  }
  render() {
    if (this.state.error) {
      const msg = this.state.error.message || String(this.state.error)
      return (
        <div className="vwin-fallback absolute inset-0 flex flex-col items-center justify-center p-6 text-center">
          <div className="text-[color:var(--text-tertiary)] text-sm mb-2">App error</div>
          <div className="text-danger text-xs font-mono rounded p-3 max-w-md overflow-auto bg-[color:var(--bg-elevated)]">{msg}</div>
          <button
            className="mt-4 text-xs text-[color:var(--text-tertiary)] hover:text-[color:var(--text-primary)]"
            onClick={() => this.setState({ error: null })}
          >
            Retry
          </button>
        </div>
      )
    }
    return this.props.children
  }
}

// IframeApp — wraps a URL-loaded app iframe with load/crash detection so a
// failed app (server down, crashed mid-session, blocked) gets a friendly
// in-shell fallback with retry instead of a blank/broken frame. The React
// error boundary above cannot see iframe load failures (they don't throw into
// React), so this fills that gap (REVIEW item 5).
const IFRAME_LOAD_TIMEOUT_MS = 12000

type IframeStatus = 'loading' | 'ok' | 'error'

interface IframeAppProps {
  url: string | undefined
  title: string | undefined
  appId: string
  sandbox: string
  dragging: boolean
}

function IframeApp({ url, title, appId, sandbox, dragging }: IframeAppProps) {
  const [status, setStatus] = useState<IframeStatus>('loading')
  const [attempt, setAttempt] = useState(0)
  const timerRef = useRef<ReturnType<typeof setTimeout> | undefined>(undefined)
  const frameRef = useRef<HTMLIFrameElement>(null)

  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect
    setStatus('loading')
    clearTimeout(timerRef.current)
    // If the frame hasn't loaded within the timeout, treat it as unreachable.
    timerRef.current = setTimeout(() => {
      setStatus(s => (s === 'loading' ? 'error' : s))
    }, IFRAME_LOAD_TIMEOUT_MS)
    return () => clearTimeout(timerRef.current)
  }, [url, attempt])

  // ORIGIN-01: bind the shell↔app bridge to THIS frame. The bridge accepts
  // messages only from this frame's contentWindow and only from the origin this
  // URL implies, so a message from any other frame or origin is dropped. It is
  // attached for every app: it is the only storage an opaque-origin app has, and
  // it is harmless for an app that also has its own origin.
  useEffect(() => {
    if (!frameRef.current || !appId) return undefined
    return attachAppBridge(frameRef.current, { appId, frameUrl: url })
  }, [appId, url, attempt])

  const retry = useCallback(() => {
    setStatus('loading')
    setAttempt(a => a + 1)
  }, [])

  // Cache-bust on retry so a transiently-down app re-fetches rather than
  // serving a stale failed response. The bridge seed goes in the fragment, so it
  // survives the query-string cache-bust and never reaches the server.
  //
  // ESCAPE (justified, single call site): IframeApp is only ever mounted from
  // Window's content switch below in the branch where win.component and
  // win.html are both unset — i.e. a url-backed window. `url` is typed
  // optional here only because ShellWindow's other window kinds (component /
  // html) don't set it; for THIS component it is always defined. The non-null
  // assertion below preserves the original untyped JS's unguarded
  // `url.includes(...)` call exactly, rather than silently changing behaviour
  // for a case (`url` undefined) that never occurs in practice.
  const busted = attempt === 0 ? url : `${url}${url!.includes('?') ? '&' : '?'}_r=${attempt}`
  const src = appId ? appFrameSrc(busted, appId) : busted

  return (
    <>
      <iframe
        key={attempt}
        ref={frameRef}
        src={src}
        title={title}
        className="absolute inset-0 w-full h-full border-0"
        style={{ pointerEvents: dragging ? 'none' : 'auto' }}
        sandbox={sandbox}
        referrerPolicy="no-referrer"
        onLoad={() => { clearTimeout(timerRef.current); setStatus('ok') }}
        onError={() => { clearTimeout(timerRef.current); setStatus('error') }}
      />
      {status === 'error' && (
        <div className="vwin-fallback absolute inset-0 flex flex-col items-center justify-center p-6 text-center">
          <svg viewBox="0 0 24 24" className="w-9 h-9 text-[color:var(--text-faint)] mb-3" fill="none" stroke="currentColor" strokeWidth="1.5">
            <circle cx="12" cy="12" r="9" />
            <path d="M12 8v4M12 16h.01" strokeLinecap="round" />
          </svg>
          <div className="text-[color:var(--text-secondary)] text-sm font-medium mb-1">{title} didn’t load</div>
          <div className="text-[color:var(--text-tertiary)] text-xs mb-4 max-w-xs">
            The app may still be starting up or is temporarily unreachable.
          </div>
          <button
            onClick={retry}
            onMouseEnter={e => { e.currentTarget.style.background = 'var(--accent)'; e.currentTarget.style.color = 'var(--accent-contrast)' }}
            onMouseLeave={e => { e.currentTarget.style.background = ''; e.currentTarget.style.color = '' }}
            className="px-3.5 py-1.5 rounded-lg text-xs font-medium bg-[color:var(--bg-elevated)] text-[color:var(--text-secondary)] transition-colors focus-primary"
          >
            Retry{attempt > 0 ? ` (${attempt})` : ''}
          </button>
          <div className="text-[12px] text-[color:var(--text-faint)] mt-3 font-mono truncate max-w-xs">{appId}</div>
        </div>
      )}
    </>
  )
}

type WindowPhase = 'opening' | 'open' | 'minimizing' | 'minimized' | 'restoring' | 'closing'

interface WindowProps {
  win: ShellWindow
  pointerBlock?: boolean
}

export default function Window({ win, pointerBlock }: WindowProps) {
  const { closeWindow, focusWindow, moveWindow, resizeWindow, minimizeWindow, maximizeWindow, tileWindow, openNativeWindow, activeWindow } = useShell()
  const [dragging, setDragging] = useState(false)
  // BMINIT-18: in v2 labwc native mode the compositor (labwc SSD) owns
  // decoration, positioning, and stacking — React becomes a thin mirror.
  const thinWM = useThinWM()
  const isActive = win._active !== undefined ? win._active : activeWindow === win.id
  const zBase = isActive ? WINDOW_Z_ACTIVE : WINDOW_Z_INACTIVE
  const isBrowser = win.appId === 'browser'

  // ── Window lifecycle motion ──────────────────────────────────────────────
  // phase: 'opening' → 'open' on mount; 'minimizing'/'restoring' track the
  // minimized flag; 'closing' is a deferred-removal exit. Compositor-only
  // (transform/opacity) and disabled under prefers-reduced-motion.
  const reduceMotion = prefersReducedMotion()
  const [phase, setPhase] = useState<WindowPhase>(reduceMotion ? 'open' : 'opening')
  const [closing, setClosing] = useState(false)
  const prevMinimized = useRef(win.minimized)
  const animTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined)

  // Mount: settle from 'opening' to 'open' on the next frame so the transition runs.
  useEffect(() => {
    if (reduceMotion) return
    const raf = requestAnimationFrame(() => setPhase('open'))
    return () => cancelAnimationFrame(raf)
    // Run once on mount.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  // Minimize / restore: drive the genie animation off the minimized flag.
  // The parent folds "on another desktop" into `minimized`; only a *real*
  // minimize (window still on the visible desktop) should play the genie —
  // desktop switches must be instant.
  useEffect(() => {
    const was = prevMinimized.current
    prevMinimized.current = win.minimized
    if (was === win.minimized) return
    const onVisibleDesktop = win._visible !== false
    if (reduceMotion || !onVisibleDesktop) { setPhase('open'); return }
    clearTimeout(animTimer.current)
    if (win.minimized) {
      // Going down to the dock: play 'minimizing', then fully hide once the
      // genie resolves (so it stops painting / consuming compositor work).
      setPhase('minimizing')
      animTimer.current = setTimeout(() => setPhase('minimized'), 280)
    } else {
      // Coming back: start collapsed, then expand to 'open' next frame.
      setPhase('restoring')
      const raf = requestAnimationFrame(() => setPhase('open'))
      return () => cancelAnimationFrame(raf)
    }
  }, [win.minimized, win._visible, reduceMotion])

  useEffect(() => () => clearTimeout(animTimer.current), [])

  // Intercept close so the window plays an exit animation before the shell
  // removes it from state. Under reduced motion, close immediately.
  const animatedClose = useCallback(() => {
    if (reduceMotion) { closeWindow(win.id); return }
    setClosing(true)
    setPhase('closing')
    clearTimeout(animTimer.current)
    animTimer.current = setTimeout(() => closeWindow(win.id), 170)
  }, [reduceMotion, closeWindow, win.id])

  const SNAP_PREVIEW = 48 // zone (px) to show snap preview while dragging

  const [snapZone, setSnapZone] = useState<string | null>(null) // 'left' | 'right' | 'top' | 'top-left' | 'top-right' | 'bottom-left' | 'bottom-right'

  // Geometry + edge-detection now live in the pure, tested windowTiling module.
  // tileWindow() applies the geometry AND records the tile zone (+ pre-tile
  // floating geometry) so the drag-snap path and the keyboard-tiling path stay
  // in lock-step and share one restore path.
  const applySnap = useCallback((zone: string) => tileWindow(win.id, zone), [win.id, tileWindow])

  const onDragStart = useCallback((e: ReactPointerEvent<HTMLDivElement>) => {
    if (e.target instanceof Element && e.target.closest('[data-no-drag]')) return
    e.preventDefault()
    focusWindow(win.id)
    setDragging(true)
    const ox = e.clientX - win.position.x
    const oy = e.clientY - win.position.y
    const vw = window.innerWidth
    const vh = window.innerHeight

    // Track the live snap zone in a local (the `snapZone` state is stale inside
    // these closures — it's captured at drag-start), so pointerup applies the
    // zone the pointer was actually over.
    let liveZone: string | null = null
    const onMove = (ev: PointerEvent) => {
      moveWindow(win.id, { x: Math.max(0, ev.clientX - ox), y: Math.max(0, ev.clientY - oy) })
      liveZone = snapZoneForPoint(ev.clientX, ev.clientY, vw, vh, SNAP_PREVIEW)
      setSnapZone(liveZone)
    }
    const onUp = (ev: PointerEvent) => {
      setDragging(false)
      const zone = liveZone || snapZoneForPoint(ev.clientX, ev.clientY, vw, vh, SNAP_PREVIEW)
      setSnapZone(null)
      window.removeEventListener('pointermove', onMove)
      window.removeEventListener('pointerup', onUp)

      if (zone) applySnap(zone)
    }
    window.addEventListener('pointermove', onMove)
    window.addEventListener('pointerup', onUp)
  }, [win, focusWindow, moveWindow, applySnap])

  const onResizeStart = useCallback((e: ReactPointerEvent<HTMLDivElement>) => {
    e.preventDefault()
    e.stopPropagation()
    const sx = e.clientX, sy = e.clientY, sw = win.size.width, sh = win.size.height
    const onMove = (ev: PointerEvent) => resizeWindow(win.id, { width: Math.max(360, sw + ev.clientX - sx), height: Math.max(240, sh + ev.clientY - sy) })
    const onUp = () => { window.removeEventListener('pointermove', onMove); window.removeEventListener('pointerup', onUp) }
    window.addEventListener('pointermove', onMove)
    window.addEventListener('pointerup', onUp)
  }, [win, resizeWindow])

  // Keep the window mounted+visible while the minimize genie plays; only fully
  // hide it once minimized AND the exit transition has resolved.
  const hidden = win.minimized && phase !== 'minimizing'
  // Disable pointer events mid-animation so a flying window can't be clicked.
  const animating = !reduceMotion && (phase === 'minimizing' || phase === 'restoring' || closing)
  const genieOrigin = (phase === 'minimizing' || phase === 'restoring')
    ? dockTransformOrigin(win)
    : 'center center'

  return (
    <div
      data-window-id={win.id}
      data-win-anim={reduceMotion ? undefined : phase}
      data-active={isActive ? '' : undefined}
      // .vwin drives the token-based elevation + accent focus ring (theme-aware,
      // active/inactive keyed off [data-active]); .win-anim drives lifecycle motion.
      className="vwin win-anim absolute flex flex-col rounded-lg overflow-hidden"
      style={{
        left: win.position.x, top: win.position.y, width: win.size.width, height: win.size.height,
        zIndex: closing ? zBase + WINDOW_Z_CLOSING_LIFT : zBase,
        transformOrigin: genieOrigin,
        pointerEvents: animating ? 'none' : undefined,
        display: hidden ? 'none' : undefined,
      }}
      onPointerDown={() => focusWindow(win.id)}
    >
      {/* Title bar — hidden for browser, and in thin WM mode (labwc SSD owns decoration).
          In v2 labwc mode: traffic lights + drag handle are suppressed because the
          compositor provides SSD (server-side decoration) for all windows. */}
      {!isBrowser && !thinWM && (
        <div className="vwin-titlebar flex items-center gap-2 px-3 py-2 select-none shrink-0 cursor-grab active:cursor-grabbing" onPointerDown={onDragStart} onDoubleClick={() => maximizeWindow(win.id)}>
          {/* Traffic lights — always-lit colour when the window is focused (or when
              the cluster is hovered), calm neutral otherwise; the glyph is revealed
              on cluster hover. focus-primary gives keyboard users a visible ring so
              a window can be focused + closed entirely from the keyboard. */}
          <div className="vwin-lights flex items-center gap-1.5" data-no-drag>
            <button onClick={animatedClose} aria-label="Close window" className="vwin-light vwin-light-close focus-primary">
              <svg viewBox="0 0 8 8" fill="none" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round"><path d="M2 2l4 4M6 2l-4 4" /></svg>
            </button>
            <button onClick={() => minimizeWindow(win.id)} aria-label="Minimize window" className="vwin-light vwin-light-min focus-primary">
              <svg viewBox="0 0 8 8" fill="none" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round"><path d="M2 4h4" /></svg>
            </button>
            <button onClick={() => maximizeWindow(win.id)} aria-label="Maximize window" className="vwin-light vwin-light-max focus-primary">
              <svg viewBox="0 0 8 8" fill="none" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round"><path d="M4 2v4M2 4h4" /></svg>
            </button>
          </div>
          <div className="vwin-title flex-1 flex items-center justify-center gap-1.5 text-xs truncate">
            <AppIcon id={win.appId} size={12} color="currentColor" />
            <span>{win.title}</span>
          </div>
          {/* Pop to native window button — only on native mode */}
          {canSpawnNativeWindow() && win.url && (
            <button
              data-no-drag
              onClick={() => openNativeWindow(win)}
              title="Open in native window"
              className="vwin-chrome-btn focus-primary w-5 h-5 flex items-center justify-center rounded-full text-[9px] mr-0.5"
            >
              <svg viewBox="0 0 12 12" className="w-3 h-3" fill="none" stroke="currentColor" strokeWidth="1.5">
                <path d="M5 1H2a1 1 0 00-1 1v8a1 1 0 001 1h8a1 1 0 001-1V7" />
                <path d="M7 1h4v4M11 1L5 7" />
              </svg>
            </button>
          )}
          {/* Save AI viewport button */}
          {win._saveable && (
            <button
              data-no-drag
              onClick={async () => {
                await fetch('/api/ai-apps/save', {
                  method: 'POST',
                  headers: { 'Content-Type': 'application/json' },
                  body: JSON.stringify({ title: win._saveable?.title, html: win._saveable?.html, python: win._saveable?.python || '' }),
                })
                const el = document.activeElement
                if (el) { el.textContent = '✓'; setTimeout(() => { el.textContent = '💾' }, 1000) }
              }}
              title="Save this AI app"
              className="vwin-chrome-btn focus-primary w-5 h-5 flex items-center justify-center rounded-full text-[9px] mr-0.5"
            >
              {'💾'}
            </button>
          )}
        </div>
      )}

      {/* Browser: thin draggable strip at top */}
      {isBrowser && (
        <div
          className="vwin-browserbar h-2 select-none shrink-0 cursor-grab active:cursor-grabbing"
          onPointerDown={onDragStart}
        />
      )}

      {/* Content */}
      <div className="vwin-content flex-1 relative overflow-hidden" style={pointerBlock ? { pointerEvents: 'none' } : undefined}>
        {win.component ? (
          <WindowErrorBoundary key={win.id}>
            <div className="absolute inset-0 overflow-y-auto">{win.component}</div>
          </WindowErrorBoundary>
        ) : win.html ? (
          <iframe
            srcDoc={win.html}
            title={win.title}
            className="absolute inset-0 w-full h-full border-0"
            style={{ pointerEvents: dragging ? 'none' : 'auto' }}
            sandbox="allow-scripts"
          />
        ) : (
          <IframeApp
            url={win.url}
            title={win.title}
            appId={win.appId}
            sandbox={iframeSandboxForURL(win.url)}
            dragging={dragging}
          />
        )}

      </div>

      {/* Browser overlay controls — top right, matching Chrome's title bar */}
      {isBrowser && (
        <div className="vwin-lights absolute top-3 right-3 z-10 flex items-center gap-1.5" data-no-drag>
          <button onClick={() => minimizeWindow(win.id)} className="vwin-light vwin-light-min focus-primary" title="Minimize" aria-label="Minimize window">
            <svg viewBox="0 0 8 8" fill="none" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round"><path d="M2 4h4" /></svg>
          </button>
          <button onClick={() => maximizeWindow(win.id)} className="vwin-light vwin-light-max focus-primary" title="Maximize" aria-label="Maximize window">
            <svg viewBox="0 0 8 8" fill="none" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round"><path d="M4 2v4M2 4h4" /></svg>
          </button>
          <button onClick={animatedClose} className="vwin-light vwin-light-close focus-primary" title="Close" aria-label="Close window">
            <svg viewBox="0 0 8 8" fill="none" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round"><path d="M2 2l4 4M6 2l-4 4" /></svg>
          </button>
        </div>
      )}

      {/* Resize handle — suppressed in thin WM mode (labwc SSD provides resize grips).
          Brightens on hover so the grip reads as interactive rather than dead chrome. */}
      {!thinWM && (
        <div className="vwin-resize absolute bottom-0 right-0 w-4 h-4 cursor-se-resize" onPointerDown={onResizeStart} aria-hidden="true">
          <svg className="vwin-grip w-3 h-3 absolute bottom-0.5 right-0.5" viewBox="0 0 10 10">
            <path d="M9 1L1 9M9 5L5 9" stroke="currentColor" strokeWidth="1.5" fill="none" />
          </svg>
        </div>
      )}

      {/* Snap preview overlay — not applicable in thin WM mode */}
      {!thinWM && snapZone && <SnapPreview zone={snapZone} />}
    </div>
  )
}

interface SnapPreviewProps {
  zone: string
}

function SnapPreview({ zone }: SnapPreviewProps) {
  const g = tileGeometry(zone, window.innerWidth, window.innerHeight, MENU_BAR_H)
  if (!g) return null
  return (
    <div
      className="fixed z-[100] rounded-xl border-2 pointer-events-none transition-all duration-150"
      style={{
        left: g.position.x, top: g.position.y, width: g.size.width, height: g.size.height,
        borderColor: 'color-mix(in srgb, var(--accent) 55%, transparent)',
        background: 'var(--accent-soft)',
        backdropFilter: 'blur(2px)',
        WebkitBackdropFilter: 'blur(2px)',
        boxShadow: 'inset 0 1px 0 color-mix(in srgb, var(--text-primary) 12%, transparent), 0 12px 40px -10px color-mix(in srgb, var(--accent) 45%, transparent)',
      }}
    />
  )
}
