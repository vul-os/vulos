import { createContext, useContext, useCallback, useSyncExternalStore, type ReactNode } from 'react'
import { WALLPAPER_LS_KEY, WALLPAPER_PREF_KEY } from './prefKeys'
import { exceedsSyncLimit, prefRead, setPref, subscribePrefs } from './syncedPrefs'

interface WallpaperContextValue {
  wallpaper: string | null
  setWallpaper: (value: string | null) => void
  /**
   * True when the current wallpaper is too large for the box to hold, so it
   * lives in this browser and nowhere else. Surfaced so Settings can say so
   * instead of letting the user believe it followed them.
   */
  localOnly: boolean
}

const WallpaperContext = createContext<WallpaperContextValue | null>(null)

const STORAGE_KEY = WALLPAPER_LS_KEY
export const DEFAULT_WALLPAPER = '/vulos.png'

/**
 * # What syncs, and what cannot
 *
 * The value here is whatever setWallpaper was handed, and today the only
 * producer is Settings' file picker, which calls FileReader.readAsDataURL — so
 * in practice it is a `data:` URI of the whole image, megabytes long.
 *
 * That cannot ride the preference bag, and the 512-byte cap is not the real
 * reason. The bag is ONE CRDT register (`profiles.data`). Every wallpaper
 * change would rewrite the register carrying the user's entire profile and ship
 * megabytes to every instance on every change. Raising the cap would not fix
 * that; it would only hide it.
 *
 * So a wallpaper that is a REFERENCE — a path, a URL, a gradient — syncs, and
 * an uploaded image stays in this browser while the box's copy is DELETED
 * rather than left stale. That deletion is what keeps the state honest: without
 * it, the next load applies the older server value over the photo the user just
 * chose. See roadmap/USER-STATE-INVENTORY.md §5 for the remedy this is waiting
 * on (a replicated byte store the shell can reach; there is not one today).
 */
interface WallpaperProviderProps {
  children?: ReactNode
}

export function WallpaperProvider({ children }: WallpaperProviderProps) {
  // Read through the cache rather than holding a copy: a wallpaper arriving
  // from the box is written from outside React, and a component that seeded
  // itself at mount would keep rendering the old one until the next reload.
  const wallpaper = useSyncExternalStore(
    subscribePrefs,
    () => prefRead(STORAGE_KEY) || null,
    () => null,
  )

  const setWallpaper = useCallback((value: string | null) => {
    setPref(WALLPAPER_PREF_KEY, STORAGE_KEY, value)
  }, [])

  return (
    <WallpaperContext value={{ wallpaper, setWallpaper, localOnly: exceedsSyncLimit(wallpaper) }}>
      {children}
    </WallpaperContext>
  )
}

// eslint-disable-next-line react-refresh/only-export-components
export function useWallpaper(): WallpaperContextValue | null {
  return useContext(WallpaperContext)
}
