import { useRef, useCallback, useState } from 'react'
import { useShell } from '../providers/ShellProvider'
import AppIcon from '../core/AppIcons'

export default function Window({ win }) {
  const { closeWindow, focusWindow, moveWindow, resizeWindow, minimizeWindow, maximizeWindow, activeWindow } = useShell()
  const [dragging, setDragging] = useState(false)
  const isActive = win._active !== undefined ? win._active : activeWindow === win.id
  const zBase = isActive ? 20 : 10
  const isBrowser = win.appId === 'browser'

  const SNAP_EDGE = 12 // pixels from edge to trigger snap

  const onDragStart = useCallback((e) => {
    if (e.target.closest('[data-no-drag]')) return
    e.preventDefault()
    focusWindow(win.id)
    setDragging(true)
    const ox = e.clientX - win.position.x
    const oy = e.clientY - win.position.y
    const vw = window.innerWidth
    const vh = window.innerHeight

    const onMove = (ev) => moveWindow(win.id, { x: Math.max(0, ev.clientX - ox), y: Math.max(0, ev.clientY - oy) })
    const onUp = (ev) => {
      setDragging(false)
      window.removeEventListener('pointermove', onMove)
      window.removeEventListener('pointerup', onUp)

      // Snap to edges
      const x = ev.clientX, y = ev.clientY
      if (x <= SNAP_EDGE) {
        // Snap left half
        moveWindow(win.id, { x: 0, y: 32 })
        resizeWindow(win.id, { width: Math.floor(vw / 2), height: vh - 80 })
      } else if (x >= vw - SNAP_EDGE) {
        // Snap right half
        moveWindow(win.id, { x: Math.floor(vw / 2), y: 32 })
        resizeWindow(win.id, { width: Math.floor(vw / 2), height: vh - 80 })
      } else if (y <= SNAP_EDGE) {
        // Snap maximize
        moveWindow(win.id, { x: 0, y: 32 })
        resizeWindow(win.id, { width: vw, height: vh - 80 })
      }
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
      <div className="flex-1 relative bg-neutral-950 overflow-hidden">
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
    </div>
  )
}
