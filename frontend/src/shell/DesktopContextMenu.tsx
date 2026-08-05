import { useState, useCallback, useEffect } from 'react'
import { useShell } from '../providers/ShellProvider'
import { canSpawnNativeWindow } from '../core/useNativeMode'
import { useFocusTrap } from './useFocusTrap'
import './shell-chrome.css'

interface MenuState {
  x: number
  y: number
  selectedText: string
}

function dispatchAskAI(prompt: string) {
  window.dispatchEvent(new CustomEvent('vulos:chat', { detail: prompt }))
}

export default function DesktopContextMenu() {
  const [menu, setMenu] = useState<MenuState | null>(null)
  const { openNativeWindow } = useShell()
  // A11Y: keyboard users land inside the menu and return to where they were.
  const trapRef = useFocusTrap(!!menu)

  useEffect(() => {
    if (!canSpawnNativeWindow()) return

    const onCtx = (e: MouseEvent) => {
      // Only on desktop background — skip windows, dock, menubar
      const el = e.target as HTMLElement
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
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') setMenu(null) }
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
      ref={trapRef}
      role="menu"
      aria-label="Desktop actions"
      className="fixed z-[9999]"
      style={{ left: menu.x, top: menu.y }}
      onPointerDown={(e) => e.stopPropagation()}
    >
      <div className="vshell-surface vshell-pop rounded-xl py-1 min-w-[190px]">
        <button
          role="menuitem"
          onClick={handleNewNative}
          className="vshell-menu-item w-full text-left px-3 py-1.5 text-[13px] focus:outline-none"
        >
          Open Native Window
        </button>
        <div className="vshell-hairline my-1 mx-2 h-px" />
        <button
          role="menuitem"
          onClick={handleAskAI}
          data-accent="true"
          className="vshell-menu-item w-full text-left px-3 py-1.5 text-[13px] focus:outline-none flex items-center gap-2"
        >
          <span aria-hidden="true">✦</span>
          {menu.selectedText ? 'Ask AI about selection' : 'Ask AI about this'}
        </button>
      </div>
    </div>
  )
}
