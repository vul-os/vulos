import { useEffect, useCallback, useRef, useMemo } from 'react'
import { useShell } from '../providers/ShellProvider'
import AppIcon from '../core/AppIcons'

function detectOS() {
  const ua = navigator.userAgent || ''
  if (/Macintosh|Mac OS X/i.test(ua)) return 'mac'
  if (/Windows/i.test(ua)) return 'windows'
  if (/Linux/i.test(ua)) return 'linux'
  return 'other'
}

/**
 * Calculate grid positions for windows in Mission Control view.
 * Returns a map of windowId -> { x, y, scale }
 */
export function useMissionControlLayout(windows, isOpen) {
  return useMemo(() => {
    if (!isOpen || windows.length === 0) return {}

    const vw = window.innerWidth
    const vh = window.innerHeight
    const topOffset = 130 // space for desktop strip
    const bottomOffset = 36 // space for hint text
    const padding = 20
    const gap = 14

    const areaW = vw - padding * 2
    const areaH = vh - topOffset - bottomOffset - padding

    const count = windows.length

    // Find best grid that fills screen nicely (try different column counts)
    let bestCols = 1, bestScore = 0
    for (let c = 1; c <= Math.min(count, 6); c++) {
      const r = Math.ceil(count / c)
      const cw = (areaW - gap * (c - 1)) / c
      const ch = (areaH - gap * (r - 1)) / r
      // Score = total area used (prefer layouts that fill the space)
      const avgW = windows.reduce((s, w) => s + w.size.width, 0) / count
      const avgH = windows.reduce((s, w) => s + w.size.height, 0) / count
      const scale = Math.min(cw / avgW, ch / avgH, 0.75)
      const score = scale * avgW * scale * avgH * count
      if (score > bestScore) { bestScore = score; bestCols = c }
    }

    const cols = bestCols
    const rows = Math.ceil(count / cols)
    const cellW = (areaW - gap * (cols - 1)) / cols
    const cellH = (areaH - gap * (rows - 1)) / rows

    const layout = {}
    windows.forEach((win, i) => {
      const col = i % cols
      const row = Math.floor(i / cols)

      // Scale to fit cell, cap at 75% to leave breathing room
      const scaleX = cellW / win.size.width
      const scaleY = cellH / win.size.height
      const scale = Math.min(scaleX, scaleY, 0.75)

      const scaledW = win.size.width * scale
      const scaledH = win.size.height * scale

      // Center within cell
      const cellX = padding + col * (cellW + gap)
      const cellY = topOffset + row * (cellH + gap)
      const x = cellX + (cellW - scaledW) / 2
      const y = cellY + (cellH - scaledH) / 2

      layout[win.id] = { x, y, scale }
    })

    return layout
  }, [windows, isOpen])
}

export default function MissionControl() {
  const {
    desktops, activeDesktop, switchDesktop, addDesktop, removeDesktop,
    windows, activeWindow,
    focusWindow, minimizeWindow, closeWindow,
    missionControlOpen, toggleMissionControl, setMissionControl,
  } = useShell()

  const os = useRef(detectOS()).current

  // Keyboard shortcut: F3 (Windows/Linux), Ctrl+Up (mac fallback)
  useEffect(() => {
    const onKey = (e) => {
      if (e.key === 'F3' || (e.ctrlKey && e.key === 'ArrowUp')) {
        e.preventDefault()
        toggleMissionControl()
      }
      if (e.key === 'Escape' && missionControlOpen) {
        setMissionControl(false)
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [toggleMissionControl, setMissionControl, missionControlOpen])

  // Pinch-in gesture (trackpad) — on macOS, pinch fires as wheel events with ctrlKey=true
  useEffect(() => {
    let pinchAccum = 0
    let resetTimer = null

    const onWheel = (e) => {
      if (!e.ctrlKey) return
      if (e.target.closest('[data-window-id]') && !missionControlOpen) return
      if (e.target.closest('[data-no-pinch]')) return

      if (e.deltaY > 0) {
        pinchAccum += e.deltaY
        e.preventDefault()
      } else {
        if (missionControlOpen) {
          pinchAccum -= e.deltaY
          if (pinchAccum > 50) {
            setMissionControl(false)
            pinchAccum = 0
          }
          e.preventDefault()
          return
        }
        pinchAccum = 0
      }

      clearTimeout(resetTimer)
      resetTimer = setTimeout(() => { pinchAccum = 0 }, 300)

      if (pinchAccum > 50 && !missionControlOpen) {
        toggleMissionControl()
        pinchAccum = 0
      }
    }

    window.addEventListener('wheel', onWheel, { passive: false })
    return () => {
      window.removeEventListener('wheel', onWheel)
      clearTimeout(resetTimer)
    }
  }, [toggleMissionControl, setMissionControl, missionControlOpen])

  const desktopList = Object.values(desktops)

  if (!missionControlOpen) return null

  return (
    <>
      {/* Dark backdrop — click to dismiss */}
      <div
        className="fixed inset-0 z-45 bg-neutral-950/80 backdrop-blur-xl transition-opacity"
        onClick={() => setMissionControl(false)}
      />

      {/* Desktop strip at top */}
      <div className="fixed top-0 left-0 right-0 z-[60] flex items-center justify-center gap-3 pt-10 pb-4">
        {desktopList.map((desk, i) => {
          const isActive = desk.id === activeDesktop
          return (
            <button
              key={desk.id}
              onClick={() => { switchDesktop(desk.id); setMissionControl(false) }}
              className="relative group flex flex-col items-center gap-1.5"
            >
              <div className={`w-32 h-20 rounded-lg border-2 transition-all overflow-hidden
                ${isActive
                  ? 'border-blue-500 bg-neutral-800/80'
                  : 'border-neutral-700/50 bg-neutral-800/40 hover:border-neutral-600'}`}
              >
                <div className="relative w-full h-full">
                  {desk.windows.map(win => {
                    const s = 0.1
                    return (
                      <div
                        key={win.id}
                        className="absolute rounded-sm bg-neutral-600/50 border border-neutral-500/30"
                        style={{
                          left: `${win.position.x * s + 4}px`,
                          top: `${win.position.y * s + 2}px`,
                          width: `${Math.max(16, win.size.width * s)}px`,
                          height: `${Math.max(10, win.size.height * s)}px`,
                        }}
                      />
                    )
                  })}
                </div>
              </div>
              <span className={`text-[11px] ${isActive ? 'text-white' : 'text-neutral-500'}`}>
                Desktop {i + 1}
              </span>
              {desktopList.length > 1 && (
                <button
                  onClick={(e) => { e.stopPropagation(); removeDesktop(desk.id) }}
                  className="absolute -top-1 -right-1 w-4 h-4 rounded-full bg-neutral-700 text-neutral-400 hover:bg-red-500 hover:text-white text-[10px] flex items-center justify-center opacity-0 group-hover:opacity-100 transition-opacity"
                >
                  {'\u00D7'}
                </button>
              )}
            </button>
          )
        })}
        <button
          onClick={() => addDesktop()}
          className="w-32 h-20 rounded-lg border-2 border-dashed border-neutral-700/40 hover:border-neutral-600 text-neutral-600 hover:text-neutral-400 flex items-center justify-center text-2xl transition-colors"
        >
          +
        </button>
      </div>

      {/* Window labels + close buttons — rendered on top of real (transformed) windows */}
      <div className="fixed inset-0 z-[52] pointer-events-none">
        {windows.filter(w => !w.minimized).map(win => {
          const el = document.querySelector(`[data-window-id="${win.id}"]`)
          if (!el) return null
          const rect = el.getBoundingClientRect()
          return (
            <div
              key={win.id}
              className="absolute pointer-events-auto"
              style={{ left: rect.left, top: rect.bottom + 6, width: rect.width }}
            >
              <div className="flex items-center justify-center gap-1.5 text-xs text-neutral-400">
                <AppIcon id={win.appId} size={12} color="#737373" />
                <span className="truncate max-w-[12rem]">{win.title}</span>
              </div>
            </div>
          )
        })}
      </div>

      {/* Shortcut hint */}
      <div className="fixed bottom-0 left-0 right-0 z-[60] text-center pb-3 text-[11px] text-neutral-600">
        {os === 'mac' ? 'F3 or Ctrl+\u2191 to toggle' : 'F3 to toggle'} · ESC to close · pinch to zoom
      </div>
    </>
  )
}
