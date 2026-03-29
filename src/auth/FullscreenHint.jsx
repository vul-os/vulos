import { useState, useEffect, useCallback } from 'react'

export default function FullscreenHint() {
  const [visible, setVisible] = useState(!document.fullscreenElement)

  useEffect(() => {
    const onChange = () => setVisible(!document.fullscreenElement)
    document.addEventListener('fullscreenchange', onChange)
    return () => document.removeEventListener('fullscreenchange', onChange)
  }, [])

  const goFullscreen = useCallback(() => {
    document.documentElement.requestFullscreen?.().catch(() => {})
  }, [])

  if (!visible) return null

  return (
    <button
      onClick={goFullscreen}
      className="text-[11px] text-amber-500/70 hover:text-amber-400 transition-colors cursor-pointer"
    >
      For the best experience, press F11 or click here to go fullscreen
    </button>
  )
}
