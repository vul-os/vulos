import { createContext, useContext, useState, useEffect, type ReactNode } from 'react'

// Known profiles: 'pc' | 'tablet' | 'mobile' | 'tv' | 'car' | 'watch' — but the
// value comes straight from the network response (see isRecord narrowing
// below), so it is kept as a plain string rather than a literal union that
// could silently drop a future/unknown profile the backend starts sending.
interface DeviceProfileContextValue {
  profile: string
}

const DeviceProfileContext = createContext<DeviceProfileContextValue | null>(null)

function isRecord(x: unknown): x is Record<string, unknown> {
  return typeof x === 'object' && x !== null
}

interface DeviceProfileProviderProps {
  children?: ReactNode
}

/**
 * Fetches the active device profile once from /api/device-profile and exposes
 * it app-wide. The root element gets data-device-profile="<profile>" so CSS
 * can target it without any component tree changes.
 *
 * Response shape (DEVPROF-01): { profile: string, suggested: string }
 * Known profiles: 'pc' | 'tablet' | 'mobile' | 'tv' | 'car' | 'watch'
 * Falls back to 'pc' on any error so the existing experience is unchanged.
 */
export function DeviceProfileProvider({ children }: DeviceProfileProviderProps) {
  const [profile, setProfile] = useState('pc')

  useEffect(() => {
    let cancelled = false
    fetch('/api/device-profile')
      .then(r => r.ok ? r.json() : null)
      .then((data: unknown) => {
        if (!cancelled && isRecord(data) && typeof data.profile === 'string' && data.profile) {
          setProfile(data.profile)
        }
      })
      .catch(() => {})
    return () => { cancelled = true }
  }, [])

  // Keep root element attribute in sync with the resolved profile
  useEffect(() => {
    document.documentElement.setAttribute('data-device-profile', profile)
  }, [profile])

  return (
    <DeviceProfileContext.Provider value={{ profile }}>
      {children}
    </DeviceProfileContext.Provider>
  )
}

// eslint-disable-next-line react-refresh/only-export-components
export function useDeviceProfile(): DeviceProfileContextValue {
  const ctx = useContext(DeviceProfileContext)
  if (!ctx) throw new Error('useDeviceProfile must be used within DeviceProfileProvider')
  return ctx
}
