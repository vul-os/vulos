import { useRef, useCallback, useState } from 'react'
import { useShell } from '../providers/ShellProvider'
import AppIcon from '../core/AppIcons'
import { canSpawnNativeWindow } from '../core/useNativeMode'

export default function Window({ win, pointerBlock }) {
  const { closeWindow, focusWindow, moveWindow, resizeWindow, minimizeWindow, maximizeWindow, openNativeWindow, activeWindow } = useShell()
  const [dragging, setDragging] = useState(false)
  const isActive = win._active !== undefined ? win._active : activeWindow === win.id
  const zBase = isActive ? 20 : 10
  const isBrowser = win.appId === 'browser'

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

  return (
    <div
      data-window-id={win.id}
      className={`absolute flex flex-col rounded-lg overflow-hidden transition-shadow
        ${isActive ? 'ring-1 ring-neutral-600 shadow-2xl shadow-black/60' : 'ring-1 ring-neutral-800 shadow-lg shadow-black/30'}`}
      style={{
        left: win.position.x, top: win.position.y, width: win.size.width, height: win.size.height,
        zIndex: zBase,
        display: win.minimized ? 'none' : undefined,
      }}
      onPointerDown={() => focusWindow(win.id)}
    >
      {/* Title bar — hidden for browser, which has its own embedded controls */}
      {!isBrowser && (
        <div className="flex items-center gap-2 px-3 py-2 bg-neutral-900 select-none shrink-0 cursor-grab active:cursor-grabbing" onPointerDown={onDragStart}>
          {/* Traffic lights */}
          <div className="flex items-center gap-1.5" data-no-drag>
            <button onClick={() => closeWindow(win.id)} className="w-3 h-3 rounded-full bg-neutral-700 hover:bg-red-500 transition-colors" />
            <button onClick={() => minimizeWindow(win.id)} className="w-3 h-3 rounded-full bg-neutral-700 hover:bg-yellow-500 transition-colors" />
            <button onClick={() => maximizeWindow(win.id)} className="w-3 h-3 rounded-full bg-neutral-700 hover:bg-green-500 transition-colors" />
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
          <div className="absolute inset-0 overflow-y-auto">{win.component}</div>
        ) : win.html ? (
          <iframe
            srcDoc={win.html}
            title={win.title}
            className="absolute inset-0 w-full h-full border-0"
            style={{ pointerEvents: dragging ? 'none' : 'auto' }}
            sandbox="allow-scripts"
          />
        ) : (
          <iframe
            src={win.url}
            title={win.title}
            className="absolute inset-0 w-full h-full border-0"
            style={{ pointerEvents: dragging ? 'none' : 'auto' }}
            sandbox="allow-scripts allow-same-origin allow-forms allow-popups"
          />
        )}

      </div>

      {/* Browser overlay controls — top right, matching Chrome's title bar */}
      {isBrowser && (
        <div className="absolute top-3 right-3 z-10 flex items-center gap-1.5" data-no-drag>
          <button onClick={() => minimizeWindow(win.id)} className="w-3 h-3 rounded-full bg-neutral-700 hover:bg-yellow-500 transition-colors" title="Minimize" />
          <button onClick={() => maximizeWindow(win.id)} className="w-3 h-3 rounded-full bg-neutral-700 hover:bg-green-500 transition-colors" title="Maximize" />
          <button onClick={() => closeWindow(win.id)} className="w-3 h-3 rounded-full bg-neutral-700 hover:bg-red-500 transition-colors" title="Close" />
        </div>
      )}

      {/* Resize handle */}
      <div className="absolute bottom-0 right-0 w-4 h-4 cursor-se-resize" onPointerDown={onResizeStart}>
        <svg className="w-3 h-3 text-neutral-700 absolute bottom-0.5 right-0.5" viewBox="0 0 10 10">
          <path d="M9 1L1 9M9 5L5 9" stroke="currentColor" strokeWidth="1.5" fill="none" />
        </svg>
      </div>

      {/* Snap preview overlay */}
      {snapZone && <SnapPreview zone={snapZone} />}
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
