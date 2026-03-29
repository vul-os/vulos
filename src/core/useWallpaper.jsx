import { createContext, useContext, useState, useCallback } from 'react'

const WallpaperContext = createContext(null)

const STORAGE_KEY = 'vulos-wallpaper'
export const DEFAULT_WALLPAPER = '/vulos.png'

export function WallpaperProvider({ children }) {
  const [wallpaper, setWallpaperState] = useState(() => {
    try { return localStorage.getItem(STORAGE_KEY) || null } catch { return null }
  })

  const setWallpaper = useCallback((value) => {
    setWallpaperState(value)
    try {
      if (value) localStorage.setItem(STORAGE_KEY, value)
      else localStorage.removeItem(STORAGE_KEY)
    } catch {}
  }, [])

  return (
    <WallpaperContext value={{ wallpaper, setWallpaper }}>
      {children}
    </WallpaperContext>
  )
}

export function useWallpaper() {
  return useContext(WallpaperContext)
}
