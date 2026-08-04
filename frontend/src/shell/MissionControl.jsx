import { useEffect, useMemo } from 'react'
import { useShell } from '../providers/ShellProvider'
import AppIcon from '../core/AppIcons'
import './shell-chrome.css'

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
// eslint-disable-next-line react-refresh/only-export-components
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
    windows,
    missionControlOpen, toggleMissionControl, setMissionControl,
  } = useShell()

  const os = detectOS()

  // Keyboard shortcut: F3 (Windows/Linux), Ctrl+Up (mac fallback)
  useEffect(() => {
    const onKey = (e) => {
      // Ctrl+Alt+↑ is reserved for keyboard tiling (maximize), so require that
      // Alt is NOT held for the Ctrl+↑ Mission Control fallback.
      if (e.key === 'F3' || (e.ctrlKey && !e.altKey && e.key === 'ArrowUp')) {
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
      {/* Frosted backdrop — click to dismiss */}
      <div
        className="vshell-scrim fixed inset-0 z-45"
        onClick={() => setMissionControl(false)}
      />

      {/* Desktop strip at top */}
      <div className="fixed top-0 left-0 right-0 z-[60] flex items-center justify-center flex-wrap gap-3 px-4 pt-10 pb-4" style={{ paddingTop: 'max(2.5rem, var(--safe-top))' }}>
        {desktopList.map((desk, i) => {
          const isActive = desk.id === activeDesktop
          return (
            <div
              key={desk.id}
              role="button"
              tabIndex={0}
              aria-label={`Switch to Desktop ${i + 1}`}
              onClick={() => { switchDesktop(desk.id); setMissionControl(false) }}
              onKeyDown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); switchDesktop(desk.id); setMissionControl(false) } }}
              className="relative group flex flex-col items-center gap-1.5 cursor-pointer focus-primary rounded-xl"
            >
              <div
                data-active={isActive || undefined}
                className="vshell-space w-32 h-20 rounded-xl overflow-hidden"
              >
                <div className="relative w-full h-full">
                  {desk.windows.map(win => {
                    const s = 0.1
                    return (
                      <div
                        key={win.id}
                        className="vshell-space-win absolute rounded-sm"
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
              <span className="text-[12px]" style={{ color: isActive ? 'var(--text-primary)' : 'var(--text-muted)' }}>
                Desktop {i + 1}
              </span>
              {desktopList.length > 1 && (
                <button
                  onClick={(e) => { e.stopPropagation(); removeDesktop(desk.id) }}
                  aria-label={`Remove Desktop ${i + 1}`}
                  className="vshell-pip absolute -top-1 -right-1 w-4 h-4 rounded-full text-[10px] flex items-center justify-center opacity-0 group-hover:opacity-100 focus:opacity-100 transition-opacity"
                >
                  <span aria-hidden="true">{'\u00D7'}</span>
                </button>
              )}
            </div>
          )
        })}
        <button
          onClick={() => addDesktop()}
          aria-label="Add desktop"
          className="vshell-space-add focus-primary w-32 h-20 rounded-xl flex items-center justify-center text-2xl"
        >
          <span aria-hidden="true">+</span>
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
              <div className="flex items-center justify-center gap-1.5 text-xs" style={{ color: 'var(--text-tertiary)' }}>
                <AppIcon id={win.appId} size={12} color="currentColor" />
                <span className="truncate max-w-[12rem]">{win.title}</span>
              </div>
            </div>
          )
        })}
      </div>

      {/* Shortcut hint */}
      <div className="fixed bottom-0 left-0 right-0 z-[60] text-center pb-3 text-[12px]" style={{ color: 'var(--text-muted)', paddingBottom: 'max(0.75rem, var(--safe-bottom))' }}>
        {os === 'mac' ? 'F3 or Ctrl+\u2191 to toggle' : 'F3 to toggle'} · ESC to close · pinch to zoom
      </div>
    </>
  )
}
