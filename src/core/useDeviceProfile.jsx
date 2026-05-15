import { createContext, useContext, useState, useEffect } from 'react'

const DeviceProfileContext = createContext(null)

/**
 * Fetches the active device profile once from /api/device-profile and exposes
 * it app-wide. The root element gets data-device-profile="<profile>" so CSS
 * can target it without any component tree changes.
 *
 * Response shape (DEVPROF-01): { profile: string, suggested: string }
 * Known profiles: 'pc' | 'tablet' | 'mobile' | 'tv' | 'car' | 'watch'
 * Falls back to 'pc' on any error so the existing experience is unchanged.
 */
export function DeviceProfileProvider({ children }) {
  const [profile, setProfile] = useState('pc')

  useEffect(() => {
    let cancelled = false
    fetch('/api/device-profile')
      .then(r => r.ok ? r.json() : null)
      .then(data => {
        if (!cancelled && data?.profile) {
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

export function useDeviceProfile() {
  const ctx = useContext(DeviceProfileContext)
  if (!ctx) throw new Error('useDeviceProfile must be used within DeviceProfileProvider')
  return ctx
}
