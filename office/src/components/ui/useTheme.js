/**
 * useTheme — tiny hook for explicit light/dark/system theme toggling.
 *
 * Storage:
 *   localStorage 'vulos.theme' = 'light' | 'dark' | 'system' (default)
 *
 * Side-effects:
 *   Sets [data-theme] on <html>. Absence of the attribute means "follow OS",
 *   which the tokens handle via @media (prefers-color-scheme: dark).
 */

import { useEffect, useState, useCallback } from 'react'

const STORE_KEY = 'vulos.theme'

function applyTheme(theme) {
  const root = document.documentElement
  if (theme === 'light' || theme === 'dark') {
    root.setAttribute('data-theme', theme)
  } else {
    root.removeAttribute('data-theme')
  }
}

export function useTheme() {
  const [theme, setTheme] = useState(() => {
    try { return localStorage.getItem(STORE_KEY) || 'system' } catch { return 'system' }
  })

  useEffect(() => {
    applyTheme(theme)
    try { localStorage.setItem(STORE_KEY, theme) } catch {}
  }, [theme])

  const cycle = useCallback(() => {
    setTheme((t) => (t === 'light' ? 'dark' : t === 'dark' ? 'system' : 'light'))
  }, [])

  return { theme, setTheme, cycle }
}
