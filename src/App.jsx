import { useEffect, useState, useCallback } from 'react'
import { useDrivingMode } from './core/useDrivingMode' // DEVPROF-06
import { AuthProvider, useAuth } from './auth/AuthProvider'
import { ThemeProvider } from './core/ThemeProvider'
import { I18nProvider } from './core/i18n'
import { WallpaperProvider } from './core/useWallpaper.jsx'
import { DeviceProfileProvider, useDeviceProfile } from './core/useDeviceProfile.jsx'
import { ShellProvider, useShell } from './providers/ShellProvider'
import { useSpatialNav } from './core/useSpatialNav'
import LoginScreen from './auth/LoginScreen'
import LockScreen from './auth/LockScreen'
import Setup from './auth/Setup'
import DesktopCanvas from './layouts/DesktopCanvas'
import MobileStack from './layouts/MobileStack'
import TVHome from './layouts/TVHome'
import Popout from './shell/Popout'
import Screensaver from './shell/Screensaver'

function DesktopShortcuts() {
  const { desktops, switchDesktop, addDesktop } = useShell()

  useEffect(() => {
    const handler = (e) => {
      if (e.ctrlKey && e.key >= '1' && e.key <= '9') {
        e.preventDefault()
        const list = Object.keys(desktops)
        const idx = parseInt(e.key) - 1
        if (idx < list.length) switchDesktop(list[idx])
      }
      if (e.ctrlKey && e.key === 'n') {
        e.preventDefault()
        addDesktop()
      }
    }
    window.addEventListener('keydown', handler)
    return () => window.removeEventListener('keydown', handler)
  }, [desktops, switchDesktop, addDesktop])

  return null
}

function useEnergyState() {
  const [locked, setLocked] = useState(false)
  const [screensaver, setScreensaver] = useState(false)

  useEffect(() => {
    const id = setInterval(async () => {
      try {
        const res = await fetch('/api/energy/status')
        if (res.ok) {
          const data = await res.json()
          if (!data.screen_on && !locked) setLocked(true)
          else if (data.screen_dimmed && !screensaver && !locked) setScreensaver(true)
        }
      } catch { /* noop */ }
    }, 5000)
    return () => clearInterval(id)
  }, [locked, screensaver])

  useEffect(() => {
    const handler = (e) => {
      if (e.ctrlKey && e.key === 'l') {
        e.preventDefault()
        setLocked(true)
        setScreensaver(false)
      }
    }
    window.addEventListener('keydown', handler)
    return () => window.removeEventListener('keydown', handler)
  }, [])

  const unlock = useCallback(() => {
    setLocked(false)
    setScreensaver(false)
    fetch('/api/energy/wake', { method: 'POST' }).catch(() => {})
  }, [])

  const dismissScreensaver = useCallback(() => {
    setScreensaver(false)
    fetch('/api/energy/wake', { method: 'POST' }).catch(() => {})
  }, [])

  return { locked, screensaver, unlock, dismissScreensaver }
}

// SEC: Defensive postMessage guard — reject any cross-origin message that does
// not come from the same origin (app iframes run same-origin under the gateway).
// If apps ever need to message the shell, they MUST use this same-origin channel.
function usePostMessageGuard() {
  useEffect(() => {
    const handler = (e) => {
      // Reject messages from any origin other than our own.
      if (e.origin !== window.location.origin) {
        // Silently discard — do not expose internal state to unknown origins.
        return
      }
      // Shell currently handles no postMessage commands; drop all same-origin
      // messages too unless a specific handler is registered here.
    }
    window.addEventListener('message', handler)
    return () => window.removeEventListener('message', handler)
  }, [])
}

function Shell() {
  const { layout, popout } = useShell()
  const { profile } = useAuth()
  const { profile: deviceProfile } = useDeviceProfile()
  const { locked, screensaver, unlock, dismissScreensaver } = useEnergyState()
  usePostMessageGuard()
  // MOBILE-06: device profile overrides the viewport-only `layout` value;
  // 'mobile' and 'tablet' both collapse to single-column MobileStack.
  const useDesktop = deviceProfile === 'desktop' && layout === 'desktop'

  const { isDriving } = useDrivingMode() // DEVPROF-06
  useEffect(() => { document.body.classList.toggle('driving-mode', isDriving) }, [isDriving])
  if (locked) return <LockScreen onUnlock={unlock} userName={profile?.display_name} />
  if (screensaver) return <Screensaver onDismiss={dismissScreensaver} />
  if (popout) return <Popout />
  if (deviceProfile === 'tv') return <TVHome />

  return (
    <>
      <DesktopShortcuts />
      {useDesktop ? <DesktopCanvas /> : <MobileStack />}
    </>
  )
}

function AuthGate() {
  const { user, loading } = useAuth()
  const [setupDone, setSetupDone] = useState(null)

  // Check if first-boot setup has been completed (public endpoint, no auth needed)
  useEffect(() => {
    fetch('/api/setup/status')
      .then(r => r.ok ? r.json() : { setup_complete: true })
      .then(d => setSetupDone(d.setup_complete !== false))
      .catch(() => setSetupDone(true))
  }, [])

  if (loading || setupDone === null) {
    return (
      <div className="fixed inset-0 bg-neutral-950 flex items-center justify-center">
        <span className="text-neutral-600 text-sm">Loading...</span>
      </div>
    )
  }

  // First boot — show setup wizard
  if (!setupDone) return <Setup onComplete={() => setSetupDone(true)} />

  if (!user) return <LoginScreen />

  return (
    <ShellProvider>
      <Shell />
    </ShellProvider>
  )
}

export default function App() {
  // TV D-pad navigation — no-op when data-device-profile !== "tv"
  useSpatialNav()

  return (
    <I18nProvider>
    <DeviceProfileProvider>
      <ThemeProvider>
        <WallpaperProvider>
          <AuthProvider>
            <AuthGate />
          </AuthProvider>
        </WallpaperProvider>
      </ThemeProvider>
    </DeviceProfileProvider>
    </I18nProvider>
  )
}
