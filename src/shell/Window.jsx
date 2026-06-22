import { Component, useCallback, useEffect, useRef, useState } from 'react'
import { useShell } from '../providers/ShellProvider'
import AppIcon from '../core/AppIcons'
import { canSpawnNativeWindow, useThinWM } from '../core/useNativeMode'
import { needsSameOrigin } from '../core/AppRegistry'

// SANDBOX-01: build the iframe sandbox for a URL-loaded app. App pages are
// served same-origin, so `allow-same-origin` here defeats the sandbox (the app
// can reach window.top, shell localStorage/cookies, gateway auth headers). It
// is therefore opt-in per app via AppRegistry's needsSameOrigin(); apps that do
// not opt in run in an opaque origin and are isolated from the shell.
// The real fix is per-app origins — see the note in AppRegistry.js.
function iframeSandbox(appId) {
  const base = 'allow-scripts allow-forms allow-popups'
  return needsSameOrigin(appId) ? `${base} allow-same-origin` : base
}

// WINDOW MOTION — read the user's reduced-motion preference at call time so the
// open/close/minimize choreography can collapse to instant state changes.
function prefersReducedMotion() {
  return typeof window !== 'undefined' &&
    window.matchMedia &&
    window.matchMedia('(prefers-reduced-motion: reduce)').matches
}

// Aim the genie/minimize shrink at the dock area (bottom-center of the
// viewport) relative to the window's own box, so the window appears to fly
// toward the dock rather than collapsing in place.
function dockTransformOrigin(win) {
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

// WindowErrorBoundary — catches errors thrown by a window's app component so
// a single broken app cannot crash the entire desktop shell.
class WindowErrorBoundary extends Component {
  constructor(props) {
    super(props)
    this.state = { error: null }
  }
  static getDerivedStateFromError(error) { return { error } }
  componentDidCatch(error, info) {
    console.error('[window] app error caught by boundary:', error, info?.componentStack)
  }
  render() {
    if (this.state.error) {
      const msg = this.state.error?.message || String(this.state.error)
      return (
        <div className="absolute inset-0 flex flex-col items-center justify-center p-6 bg-neutral-950 text-center">
          <div className="text-neutral-500 text-sm mb-2">App error</div>
          <div className="text-red-400 text-xs font-mono bg-neutral-900 rounded p-3 max-w-md overflow-auto">{msg}</div>
          <button
            className="mt-4 text-xs text-neutral-500 hover:text-neutral-300"
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

function IframeApp({ url, title, appId, sandbox, dragging }) {
  const [status, setStatus] = useState('loading') // 'loading' | 'ok' | 'error'
  const [attempt, setAttempt] = useState(0)
  const timerRef = useRef(null)

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

  const retry = useCallback(() => {
    setStatus('loading')
    setAttempt(a => a + 1)
  }, [])

  // Cache-bust on retry so a transiently-down app re-fetches rather than
  // serving a stale failed response.
  const src = attempt === 0 ? url : `${url}${url.includes('?') ? '&' : '?'}_r=${attempt}`

  return (
    <>
      <iframe
        key={attempt}
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
        <div className="absolute inset-0 flex flex-col items-center justify-center p-6 text-center bg-neutral-950/95">
          <svg viewBox="0 0 24 24" className="w-9 h-9 text-neutral-700 mb-3" fill="none" stroke="currentColor" strokeWidth="1.5">
            <circle cx="12" cy="12" r="9" />
            <path d="M12 8v4M12 16h.01" strokeLinecap="round" />
          </svg>
          <div className="text-neutral-300 text-sm font-medium mb-1">{title} didn’t load</div>
          <div className="text-neutral-500 text-xs mb-4 max-w-xs">
            The app may still be starting up or is temporarily unreachable.
          </div>
          <button
            onClick={retry}
            className="px-3.5 py-1.5 rounded-lg text-xs font-medium bg-neutral-800 hover:bg-blue-600 text-neutral-200 hover:text-white transition-colors focus-primary"
          >
            Retry{attempt > 0 ? ` (${attempt})` : ''}
          </button>
          <div className="text-[10px] text-neutral-700 mt-3 font-mono truncate max-w-xs">{appId}</div>
        </div>
      )}
    </>
  )
}

export default function Window({ win, pointerBlock }) {
  const { closeWindow, focusWindow, moveWindow, resizeWindow, minimizeWindow, maximizeWindow, openNativeWindow, activeWindow } = useShell()
  const [dragging, setDragging] = useState(false)
  // BMINIT-18: in v2 labwc native mode the compositor (labwc SSD) owns
  // decoration, positioning, and stacking — React becomes a thin mirror.
  const thinWM = useThinWM()
  const isActive = win._active !== undefined ? win._active : activeWindow === win.id
  const zBase = isActive ? 20 : 10
  const isBrowser = win.appId === 'browser'

  // ── Window lifecycle motion ──────────────────────────────────────────────
  // phase: 'opening' → 'open' on mount; 'minimizing'/'restoring' track the
  // minimized flag; 'closing' is a deferred-removal exit. Compositor-only
  // (transform/opacity) and disabled under prefers-reduced-motion.
  const reduceMotion = prefersReducedMotion()
  const [phase, setPhase] = useState(reduceMotion ? 'open' : 'opening')
  const [closing, setClosing] = useState(false)
  const prevMinimized = useRef(win.minimized)
  const animTimer = useRef(null)

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

  const SNAP_EDGE = 3 // pixels from edge to trigger snap on release
  const SNAP_PREVIEW = 48 // larger zone to show snap preview while dragging

  const [snapZone, setSnapZone] = useState(null) // 'left' | 'right' | 'top' | 'top-left' | 'top-right' | 'bottom-left' | 'bottom-right'

  const getSnapZone = (x, y, vw, vh, edge) => {
    const isLeft = x <= edge
    const isRight = x >= vw - edge
    const isTop = y <= edge
    const isBottom = y >= vh - edge
    if (isLeft && isTop) return 'top-left'
    if (isRight && isTop) return 'top-right'
    if (isLeft && isBottom) return 'bottom-left'
    if (isRight && isBottom) return 'bottom-right'
    if (isLeft) return 'left'
    if (isRight) return 'right'
    if (isTop) return 'top'
    return null
  }

  const applySnap = (zone, vw, vh) => {
    const top = 32    // menu bar
    const usableH = vh - top
    const halfW = Math.floor(vw / 2)
    const halfH = Math.floor(usableH / 2)
    switch (zone) {
      case 'left':
        moveWindow(win.id, { x: 0, y: top }); resizeWindow(win.id, { width: halfW, height: usableH }); break
      case 'right':
        moveWindow(win.id, { x: halfW, y: top }); resizeWindow(win.id, { width: halfW, height: usableH }); break
      case 'top':
        moveWindow(win.id, { x: 0, y: top }); resizeWindow(win.id, { width: vw, height: usableH }); break
      case 'top-left':
        moveWindow(win.id, { x: 0, y: top }); resizeWindow(win.id, { width: halfW, height: halfH }); break
      case 'top-right':
        moveWindow(win.id, { x: halfW, y: top }); resizeWindow(win.id, { width: halfW, height: halfH }); break
      case 'bottom-left':
        moveWindow(win.id, { x: 0, y: top + halfH }); resizeWindow(win.id, { width: halfW, height: halfH }); break
      case 'bottom-right':
        moveWindow(win.id, { x: halfW, y: top + halfH }); resizeWindow(win.id, { width: halfW, height: halfH }); break
    }
  }

  const onDragStart = useCallback((e) => {
    if (e.target.closest('[data-no-drag]')) return
    e.preventDefault()
    focusWindow(win.id)
    setDragging(true)
    const ox = e.clientX - win.position.x
    const oy = e.clientY - win.position.y
    const vw = window.innerWidth
    const vh = window.innerHeight

    const onMove = (ev) => {
      moveWindow(win.id, { x: Math.max(0, ev.clientX - ox), y: Math.max(0, ev.clientY - oy) })
      setSnapZone(getSnapZone(ev.clientX, ev.clientY, vw, vh, SNAP_PREVIEW))
    }
    const onUp = (ev) => {
      setDragging(false)
      const zone = snapZone || getSnapZone(ev.clientX, ev.clientY, vw, vh, SNAP_PREVIEW)
      setSnapZone(null)
      window.removeEventListener('pointermove', onMove)
      window.removeEventListener('pointerup', onUp)

      if (zone) applySnap(zone, vw, vh)
    }
    window.addEventListener('pointermove', onMove)
    window.addEventListener('pointerup', onUp)
  }, [win, focusWindow, moveWindow, resizeWindow])

  const onResizeStart = useCallback((e) => {
    e.preventDefault()
    e.stopPropagation()
    const sx = e.clientX, sy = e.clientY, sw = win.size.width, sh = win.size.height
    const onMove = (ev) => resizeWindow(win.id, { width: Math.max(360, sw + ev.clientX - sx), height: Math.max(240, sh + ev.clientY - sy) })
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
      className={`win-anim absolute flex flex-col rounded-lg overflow-hidden
        ${isActive ? 'ring-1 ring-neutral-600 shadow-2xl shadow-black/60' : 'ring-1 ring-neutral-800 shadow-lg shadow-black/30'}`}
      style={{
        left: win.position.x, top: win.position.y, width: win.size.width, height: win.size.height,
        zIndex: closing ? zBase + 5 : zBase,
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
        <div className="flex items-center gap-2 px-3 py-2 bg-neutral-900 select-none shrink-0 cursor-grab active:cursor-grabbing" onPointerDown={onDragStart}>
          {/* Traffic lights */}
          <div className="flex items-center gap-1.5" data-no-drag>
            <button onClick={animatedClose} aria-label="Close window" className="w-3 h-3 rounded-full bg-neutral-700 hover:bg-red-500 transition-colors" />
            <button onClick={() => minimizeWindow(win.id)} aria-label="Minimize window" className="w-3 h-3 rounded-full bg-neutral-700 hover:bg-yellow-500 transition-colors" />
            <button onClick={() => maximizeWindow(win.id)} aria-label="Maximize window" className="w-3 h-3 rounded-full bg-neutral-700 hover:bg-green-500 transition-colors" />
          </div>
          <div className="flex-1 flex items-center justify-center gap-1.5 text-xs text-neutral-500 truncate">
            <AppIcon id={win.appId} size={12} color="#737373" />
            <span>{win.title}</span>
          </div>
          {/* Pop to native window button — only on native mode */}
          {canSpawnNativeWindow() && win.url && (
            <button
              data-no-drag
              onClick={() => openNativeWindow(win)}
              title="Open in native window"
              className="w-5 h-5 flex items-center justify-center rounded-full bg-neutral-800 hover:bg-blue-600 text-neutral-500 hover:text-white text-[9px] transition-colors mr-0.5"
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
                  body: JSON.stringify({ title: win._saveable.title, html: win._saveable.html, python: win._saveable.python || '' }),
                })
                const el = document.activeElement
                if (el) { el.textContent = '\u2713'; setTimeout(() => { el.textContent = '\uD83D\uDCBE' }, 1000) }
              }}
              title="Save this AI app"
              className="w-5 h-5 flex items-center justify-center rounded-full bg-neutral-800 hover:bg-green-600 text-neutral-500 hover:text-white text-[9px] transition-colors mr-0.5"
            >
              {'\uD83D\uDCBE'}
            </button>
          )}
        </div>
      )}

      {/* Browser: thin draggable strip at top */}
      {isBrowser && (
        <div
          className="h-2 bg-neutral-900 select-none shrink-0 cursor-grab active:cursor-grabbing"
          onPointerDown={onDragStart}
        />
      )}

      {/* Content */}
      <div className="flex-1 relative bg-neutral-950 overflow-hidden" style={pointerBlock ? { pointerEvents: 'none' } : undefined}>
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
            sandbox={iframeSandbox(win.appId)}
            dragging={dragging}
          />
        )}

      </div>

      {/* Browser overlay controls — top right, matching Chrome's title bar */}
      {isBrowser && (
        <div className="absolute top-3 right-3 z-10 flex items-center gap-1.5" data-no-drag>
          <button onClick={() => minimizeWindow(win.id)} className="w-3 h-3 rounded-full bg-neutral-700 hover:bg-yellow-500 transition-colors" title="Minimize" aria-label="Minimize window" />
          <button onClick={() => maximizeWindow(win.id)} className="w-3 h-3 rounded-full bg-neutral-700 hover:bg-green-500 transition-colors" title="Maximize" aria-label="Maximize window" />
          <button onClick={animatedClose} className="w-3 h-3 rounded-full bg-neutral-700 hover:bg-red-500 transition-colors" title="Close" aria-label="Close window" />
        </div>
      )}

      {/* Resize handle — suppressed in thin WM mode (labwc SSD provides resize grips) */}
      {!thinWM && (
        <div className="absolute bottom-0 right-0 w-4 h-4 cursor-se-resize" onPointerDown={onResizeStart}>
          <svg className="w-3 h-3 text-neutral-700 absolute bottom-0.5 right-0.5" viewBox="0 0 10 10">
            <path d="M9 1L1 9M9 5L5 9" stroke="currentColor" strokeWidth="1.5" fill="none" />
          </svg>
        </div>
      )}

      {/* Snap preview overlay — not applicable in thin WM mode */}
      {!thinWM && snapZone && <SnapPreview zone={snapZone} />}
    </div>
  )
}

function SnapPreview({ zone }) {
  const vw = window.innerWidth
  const vh = window.innerHeight
  const top = 32
  const usableH = vh - top
  const halfW = Math.floor(vw / 2)
  const halfH = Math.floor(usableH / 2)

  const styles = {
    'left':         { left: 0, top, width: halfW, height: usableH },
    'right':        { left: halfW, top, width: halfW, height: usableH },
    'top':          { left: 0, top, width: vw, height: usableH },
    'top-left':     { left: 0, top, width: halfW, height: halfH },
    'top-right':    { left: halfW, top, width: halfW, height: halfH },
    'bottom-left':  { left: 0, top: top + halfH, width: halfW, height: halfH },
    'bottom-right': { left: halfW, top: top + halfH, width: halfW, height: halfH },
  }

  const s = styles[zone]
  if (!s) return null

  return (
    <div
      className="fixed z-[100] rounded-xl border-2 border-blue-500/40 bg-blue-500/10 pointer-events-none transition-all duration-150"
      style={s}
    />
  )
}
