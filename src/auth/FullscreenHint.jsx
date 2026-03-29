import { useState, useEffect, useCallback } from 'react'

export default function FullscreenHint() {
  const [isFullscreen, setIsFullscreen] = useState(!!document.fullscreenElement)
  const [showTip, setShowTip] = useState(false)

  useEffect(() => {
    const onChange = () => setIsFullscreen(!!document.fullscreenElement)
    document.addEventListener('fullscreenchange', onChange)
    return () => document.removeEventListener('fullscreenchange', onChange)
  }, [])

  const goFullscreen = useCallback(() => {
    document.documentElement.requestFullscreen?.().catch(() => {})
  }, [])

  if (isFullscreen) return null

  const isFirefox = navigator.userAgent.includes('Firefox')

  return (
    <div className="flex flex-col items-center gap-3">
      <button
        onClick={goFullscreen}
        className="flex items-center gap-2.5 px-5 py-2.5 rounded-xl bg-neutral-800/60 border border-neutral-700/40 text-neutral-400 hover:text-neutral-200 hover:bg-neutral-700/60 hover:border-neutral-600/50 transition-all group"
      >
        <svg width="16" height="16" viewBox="0 0 16 16" fill="none" className="text-neutral-500 group-hover:text-blue-400 transition-colors">
          <path d="M2 5V3a1 1 0 011-1h2M11 2h2a1 1 0 011 1v2M14 11v2a1 1 0 01-1 1h-2M5 14H3a1 1 0 01-1-1v-2" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round"/>
        </svg>
        <span className="text-xs font-medium">Enter Fullscreen</span>
      </button>

      {isFirefox && (
        <button
          onClick={() => setShowTip(!showTip)}
          className="text-[11px] text-neutral-600 hover:text-neutral-400 transition-colors"
        >
          Using Firefox? Prevent Esc from exiting fullscreen
        </button>
      )}

      {showTip && (
        <div className="max-w-xs bg-neutral-900/90 border border-neutral-700/50 rounded-xl p-4 text-left animate-[fadeIn_0.15s_ease-out]">
          <p className="text-xs text-neutral-300 font-medium mb-2">Stop Esc from exiting fullscreen:</p>
          <ol className="text-[11px] text-neutral-400 space-y-1.5 list-decimal list-inside">
            <li>Type <code className="text-amber-400/80 bg-neutral-800 px-1 py-0.5 rounded">about:config</code> in the address bar</li>
            <li>Click "Accept the Risk and Continue"</li>
            <li>Search for: <code className="text-amber-400/80 bg-neutral-800 px-1 py-0.5 rounded">browser.fullscreen.exit_on_escape</code></li>
            <li>Double-click to set it to <strong className="text-neutral-200">false</strong></li>
          </ol>
          <p className="text-[10px] text-neutral-600 mt-2">Use F11 or the menu to exit fullscreen instead.</p>
        </div>
      )}
    </div>
  )
}
