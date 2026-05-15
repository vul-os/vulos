import { useState, useCallback, useEffect } from 'react'
import { useShell } from '../providers/ShellProvider'
import { canSpawnNativeWindow } from '../core/useNativeMode'

function dispatchAskAI(prompt) {
  window.dispatchEvent(new CustomEvent('vulos:chat', { detail: prompt }))
}

export default function DesktopContextMenu() {
  const [menu, setMenu] = useState(null) // { x, y, selectedText }
  const { openNativeWindow } = useShell()

  useEffect(() => {
    if (!canSpawnNativeWindow()) return

    const onCtx = (e) => {
      // Only on desktop background — skip windows, dock, menubar
      const el = e.target
      if (el.closest('[data-no-ctx]') || el.closest('[data-window-id]')) return
      // Must be on the wallpaper / desktop area
      const canvas = el.closest('[data-desktop-bg]')
      if (!canvas) return

      e.preventDefault()
      const selectedText = window.getSelection()?.toString().trim() || ''
      setMenu({ x: e.clientX, y: e.clientY, selectedText })
    }

    window.addEventListener('contextmenu', onCtx)
    return () => window.removeEventListener('contextmenu', onCtx)
  }, [])

  // Close on click or escape
  useEffect(() => {
    if (!menu) return
    const close = () => setMenu(null)
    const onKey = (e) => { if (e.key === 'Escape') setMenu(null) }
    // Delay to avoid immediate close from the contextmenu event
    const id = setTimeout(() => {
      window.addEventListener('pointerdown', close)
      window.addEventListener('keydown', onKey)
    }, 0)
    return () => {
      clearTimeout(id)
      window.removeEventListener('pointerdown', close)
      window.removeEventListener('keydown', onKey)
    }
  }, [menu])

  const handleNewNative = useCallback(() => {
    openNativeWindow({ title: 'Vula', url: window.location.origin, appId: 'native' })
    setMenu(null)
  }, [openNativeWindow])

  const handleAskAI = useCallback(() => {
    const prompt = menu?.selectedText
      ? `Tell me about the following text: "${menu.selectedText}"`
      : 'What can you help me with on the desktop?'
    dispatchAskAI(prompt)
    setMenu(null)
  }, [menu])

  if (!menu) return null

  return (
    <div
      className="fixed z-[9999]"
      style={{ left: menu.x, top: menu.y }}
      onPointerDown={(e) => e.stopPropagation()}
    >
      <div className="bg-neutral-900/95 backdrop-blur-xl border border-neutral-700/60 rounded-lg py-1 min-w-[180px] shadow-2xl shadow-black/60">
        <button
          onClick={handleNewNative}
          className="w-full text-left px-3 py-1.5 text-[13px] text-neutral-300 hover:bg-neutral-700/60 hover:text-white transition-colors"
        >
          Open Native Window
        </button>
        <div className="my-1 mx-2 border-t border-neutral-700/40" />
        <button
          onClick={handleAskAI}
          className="w-full text-left px-3 py-1.5 text-[13px] text-violet-300 hover:bg-neutral-700/60 hover:text-violet-200 transition-colors"
        >
          {menu.selectedText ? 'Ask AI about selection' : 'Ask AI about this'}
        </button>
      </div>
    </div>
  )
}
