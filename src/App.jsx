import { useEffect, useState, useCallback } from 'react'
import { useDrivingMode } from './core/useDrivingMode' // DEVPROF-06
import { AuthProvider, useAuth } from './auth/AuthProvider'
import { ThemeProvider } from './core/ThemeProvider'
import { I18nProvider } from './core/i18n'
import { WallpaperProvider } from './core/useWallpaper.jsx'
import { DeviceProfileProvider, useDeviceProfile } from './core/useDeviceProfile.jsx'
import { ShellProvider, useShell } from './providers/ShellProvider'
import { SovereigntyProvider } from './core/useSovereignty'
import { useSpatialNav } from './core/useSpatialNav'
import LoginScreen from './auth/LoginScreen'
import LockScreen from './auth/LockScreen'
import Setup from './auth/Setup'
import DesktopCanvas from './layouts/DesktopCanvas'
import MobileStack from './layouts/MobileStack'
import TVHome from './layouts/TVHome'
import Popout from './shell/Popout'
import Screensaver from './shell/Screensaver'
import ShortcutsLegend from './shell/ShortcutsLegend'
import OfflineIndicator from './components/OfflineIndicator'
import { loadOriginConfig } from './core/AppOrigins'
import { startNotificationBridge } from './core/notificationBridge'
import { startAttentionNotifier } from './core/notifiers/attentionNotifier'

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

// ORIGIN-01: load the per-app origin config once at shell boot. It tells us
// whether this deployment can serve each app from its own origin. It is only ever
// used to BUILD app frame URLs — never to decide an iframe's sandbox, which is
// derived from the resulting URL's actual origin (see core/AppOrigins.js). A
// missing or wrong answer here therefore costs an app its own origin; it can
// never hand an app the shell's.
function useOriginConfig() {
  useEffect(() => { loadOriginConfig() }, [])
}

// SEC: the shell registers NO global postMessage command handler. Every
// shell↔app message is handled by the per-frame bridge in core/AppBridge.js,
// which validates the sending frame's identity (event.source must be that app's
// contentWindow) AND its origin before it will accept anything — see the trust
// model documented there. A global handler here would be a second, weaker door
// into the shell, so there deliberely isn't one.
//
// (This used to be a no-op guard that dropped every message. Dropping messages in
// a listener grants nothing, but neither did it protect anything — the real
// control is that no handler acts on untrusted input. Keeping a listener that
// does nothing only invites someone to "just add one case" to it later.)

function Shell() {
  const { layout, popout } = useShell()
  const { profile } = useAuth()
  const { profile: deviceProfile } = useDeviceProfile()
  const { locked, screensaver, unlock, dismissScreensaver } = useEnergyState()
  useOriginConfig()
  // MOBILE-06: device profile overrides the viewport-only `layout` value;
  // 'mobile' and 'tablet' both collapse to single-column MobileStack.
  // Known profiles: 'pc' | 'tablet' | 'mobile' | 'tv' | 'car' | 'watch'
  // 'pc' is the desktop/workstation profile — use DesktopCanvas for it.
  const useDesktop = (deviceProfile === 'pc' || deviceProfile === 'desktop') && layout === 'desktop'

  const { isDriving } = useDrivingMode() // DEVPROF-06
  useEffect(() => { document.body.classList.toggle('driving-mode', isDriving) }, [isDriving])

  // WAVE-13: start the notification runtime once the shell is up — the backend
  // bridge (folds /api/notifications into the client store) and the sovereign
  // attention notifier (polls the on-instance assistant for "needs you" items).
  useEffect(() => {
    const stopBridge = startNotificationBridge()
    const stopNotifier = startAttentionNotifier()
    return () => { stopBridge(); stopNotifier() }
  }, [])
  if (locked) return <LockScreen onUnlock={unlock} userName={profile?.display_name} />
  if (screensaver) return <Screensaver onDismiss={dismissScreensaver} />
  if (popout) return <Popout />
  if (deviceProfile === 'tv') return <TVHome />

  return (
    <>
      <DesktopShortcuts />
      {useDesktop ? <DesktopCanvas /> : <MobileStack />}
      {/* Press "?" anywhere to surface the keyboard-shortcut legend. */}
      <ShortcutsLegend />
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
      <SovereigntyProvider>
        <Shell />
      </SovereigntyProvider>
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
            <OfflineIndicator />
          </AuthProvider>
        </WallpaperProvider>
      </ThemeProvider>
    </DeviceProfileProvider>
    </I18nProvider>
  )
}
