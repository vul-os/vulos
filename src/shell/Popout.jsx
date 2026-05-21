import { useEffect } from 'react'
import { useShell } from '../providers/ShellProvider'

export default function Popout() {
  const { popout, closePopout } = useShell()

  // ESC to exit fullscreen
  useEffect(() => {
    if (!popout) return
    const onKey = (e) => {
      if (e.key === 'Escape') {
        e.preventDefault()
        e.stopPropagation()
        closePopout()
      }
    }
    window.addEventListener('keydown', onKey, true)
    return () => window.removeEventListener('keydown', onKey, true)
  }, [popout, closePopout])

  if (!popout) return null

  return (
    <div className="fixed inset-0 z-[100] bg-neutral-950 flex flex-col">
      {/* Top bar — always visible */}
      <div className="flex items-center justify-between px-3 py-1.5 bg-neutral-900 border-b border-neutral-800/50 shrink-0">
        <div className="flex items-center gap-2">
          <button
            onClick={closePopout}
            className="flex items-center gap-1.5 px-3 py-1.5 rounded-md text-xs text-neutral-300 hover:text-white bg-neutral-800 hover:bg-neutral-700 transition-colors"
          >
            <svg viewBox="0 0 16 16" className="w-3.5 h-3.5"><path d="M10 3L5 8l5 5" stroke="currentColor" strokeWidth="2" fill="none" /></svg>
            Exit Fullscreen
          </button>
          <span className="text-xs text-neutral-600">|</span>
          <span className="text-sm text-neutral-400">{popout.icon} {popout.title}</span>
        </div>
        <span className="text-[10px] text-neutral-600">Press ESC to exit</span>
      </div>

      {/* Full-viewport iframe */}
      <iframe
        src={popout.url}
        title={popout.title}
        className="flex-1 w-full border-0"
        sandbox="allow-scripts allow-same-origin allow-forms allow-popups"
        referrerPolicy="no-referrer"
      />
    </div>
  )
}
