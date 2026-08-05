import { createContext, useContext, useState, useCallback, type ReactNode } from 'react'

interface WallpaperContextValue {
  wallpaper: string | null
  setWallpaper: (value: string | null) => void
}

const WallpaperContext = createContext<WallpaperContextValue | null>(null)

const STORAGE_KEY = 'vulos-wallpaper'
export const DEFAULT_WALLPAPER = '/vulos.png'

interface WallpaperProviderProps {
  children?: ReactNode
}

export function WallpaperProvider({ children }: WallpaperProviderProps) {
  const [wallpaper, setWallpaperState] = useState<string | null>(() => {
    try { return localStorage.getItem(STORAGE_KEY) || null } catch { return null }
  })

  const setWallpaper = useCallback((value: string | null) => {
    setWallpaperState(value)
    try {
      if (value) localStorage.setItem(STORAGE_KEY, value)
      else localStorage.removeItem(STORAGE_KEY)
    } catch { /* noop */ }
  }, [])

  return (
    <WallpaperContext value={{ wallpaper, setWallpaper }}>
      {children}
    </WallpaperContext>
  )
}

// eslint-disable-next-line react-refresh/only-export-components
export function useWallpaper(): WallpaperContextValue | null {
  return useContext(WallpaperContext)
}
