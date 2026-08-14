import { useEffect, useState, useCallback } from 'react'
import { useDrivingMode } from './core/useDrivingMode' // DEVPROF-06
import { AuthProvider, useAuth } from './auth/AuthProvider'
import { ThemeProvider } from './core/ThemeProvider'
import { I18nProvider } from './core/i18n'
import { WallpaperProvider } from './core/useWallpaper'
import { DeviceProfileProvider, useDeviceProfile } from './core/useDeviceProfile'
import { ShellProvider, useShell } from './providers/ShellProvider'
import { SovereigntyProvider } from './core/useSovereignty'
import { useSpatialNav } from './core/useSpatialNav'
import LoginScreen from './auth/LoginScreen'
import OfflineLockScreen from './auth/OfflineLockScreen'
import LockScreen from './auth/LockScreen'
import Setup from './auth/Setup'
import DesktopCanvas from './layouts/DesktopCanvas'
import SharedDesktopNotice from './shell/SharedDesktopNotice'
import MobileStack from './layouts/MobileStack'
import TVHome from './layouts/TVHome'
import Popout from './shell/Popout'
import Screensaver from './shell/Screensaver'
import ShortcutsLegend from './shell/ShortcutsLegend'
import OfflineIndicator from './components/OfflineIndicator'
import SessionTakeoverModal from './auth/SessionTakeoverModal'
import { loadOriginConfig } from './core/AppOrigins'
import { startNotificationBridge } from './core/notificationBridge'
import { startAttentionNotifier } from './core/notifiers/attentionNotifier'
import { refreshInstalled, refreshAIApps } from './core/AppRegistry'
import { startLocationReporting, stopLocationReporting } from './core/location/reporter'
import { applyScreenWindowTitle } from './providers/screenIdentity'

// SCREENS-01: announce which physical output this browser instance belongs to,
// at MODULE SCOPE, before React is involved at all.
//
// A multi-output kiosk starts one browser per connected output and labwc places
// each window by matching a windowRule against its TITLE (scripts/vulos-kiosk.sh
// → scripts/vulos-kiosk-genconfig.sh). Every instance of this shell is otherwise
// byte-identical, so the title is the only thing that tells the compositor which
// window is which. That makes it a contract with the compositor, not a piece of
// chrome, and it has to hold for the WHOLE life of the document.
//
// Here, rather than in a component, for three reasons:
//
//   1. No mount condition. This runs whether the tree renders the setup wizard,
//      the login screen, the lock screen, a popout or the full desktop. The
//      title previously came from the top bar's screen chip, which lives inside
//      DesktopCanvas — so it existed only after setup AND after login, and the
//      first real two-display boot landed on the setup wizard and left the
//      second monitor black (roadmap/SCREENS-QEMU.md).
//   2. Earliest reachable point. ES module imports are evaluated before the
//      importing module's body, so this runs before main.tsx's own pre-paint
//      work, and long before the first React commit.
//   3. It survives a failed boot. If the React tree throws and main.tsx paints
//      its VULOS BOOT ERROR screen instead, the window still carries its
//      connector name and still lands on the right monitor — a boot error you
//      can read on the correct screen, rather than one stacked on top of
//      another window.
//
// A browser with no screen identity (ordinary tab, phone on the LAN, dev
// server, one-monitor kiosk) is left completely untouched; see
// providers/screenIdentity.ts.
applyScreenWindowTitle()

// isRecord narrows an `unknown` value (parsed JSON from the network) to a
// plain object before any property access — same guard as lib/offlineAuth.ts.
function isRecord(x: unknown): x is Record<string, unknown> {
  return typeof x === 'object' && x !== null
}

function DesktopShortcuts() {
  const { desktops, switchDesktop, addDesktop } = useShell()

  useEffect(() => {
    const handler = (e: KeyboardEvent) => {
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
          const data: unknown = await res.json()
          const screenOn = isRecord(data) ? Boolean(data.screen_on) : false
          const screenDimmed = isRecord(data) && Boolean(data.screen_dimmed)
          if (!screenOn && !locked) setLocked(true)
          else if (screenDimmed && !screensaver && !locked) setScreensaver(true)
        }
      } catch { /* noop */ }
    }, 5000)
    return () => clearInterval(id)
  }, [locked, screensaver])

  useEffect(() => {
    const handler = (e: KeyboardEvent) => {
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
  const { profile, offlineMode, unlockOffline } = useAuth()
  // AuthProfile's fields are `unknown` (untrusted network JSON) — narrow before use.
  const displayName = typeof profile?.display_name === 'string' ? profile.display_name : undefined
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
    // Load the installed-app list (and AI-generated apps) once at boot so the
    // launcher and Home surface show them immediately — previously they only
    // populated after the user opened App Hub.
    refreshInstalled()
    refreshAIApps()
    // LOCATION-01: resume opt-in location reporting if the user enabled it
    // (Settings → Devices → Location). Off by default; never starts unarmed.
    let locationOn = false
    try { locationOn = localStorage.getItem('vulos.location.share') === 'on' } catch { /* no localStorage */ }
    if (locationOn) startLocationReporting()
    return () => { stopBridge(); stopNotifier(); stopLocationReporting() }
  }, [])
  if (locked) {
    // In an offline session the online PIN lock (server-verified) can never
    // succeed — it POSTs to the box we can't reach. Re-lock with the offline
    // password unlock instead, then dismiss the energy lock on success.
    if (offlineMode) {
      return (
        <OfflineLockScreen
          onUnlock={async (pw) => { const ok = await unlockOffline(pw); unlock(); return ok }}
          identity={{ name: displayName }}
        />
      )
    }
    return <LockScreen onUnlock={unlock} userName={displayName} />
  }
  if (screensaver) return <Screensaver onDismiss={dismissScreensaver} />
  if (popout) return <Popout />
  if (deviceProfile === 'tv') return <TVHome />

  return (
    <>
      <DesktopShortcuts />
      {useDesktop ? <DesktopCanvas /> : <MobileStack />}
      {/* Renders only when a second window is genuinely on this desktop; see
          SharedDesktopNotice for why it informs rather than blocks. */}
      <SharedDesktopNotice />
      {/* Press "?" anywhere to surface the keyboard-shortcut legend. */}
      <ShortcutsLegend />
    </>
  )
}

// OfflineIdentity mirrors auth/OfflineLockScreen.tsx's own (unexported) shape —
// only the field that screen actually reads.
interface OfflineIdentity {
  name?: string
}

function toOfflineIdentity(x: unknown): OfflineIdentity | null {
  if (!isRecord(x)) return null
  return { name: typeof x.name === 'string' ? x.name : undefined }
}

function AuthGate() {
  const { user, loading, offline, unlockOffline } = useAuth()
  const [setupDone, setSetupDone] = useState<boolean | null>(null)
  // OFFLINE-AUTH-01: can this device attempt an offline unlock, and who for?
  // null = still checking; false = not enrolled (fall back to online login).
  const [offlineEnrolled, setOfflineEnrolled] = useState<boolean | null>(null)
  const [offlineIdentity, setOfflineIdentity] = useState<OfflineIdentity | null>(null)

  // Check if first-boot setup has been completed (public endpoint, no auth needed)
  useEffect(() => {
    fetch('/api/setup/status')
      .then(r => r.ok ? r.json() : { setup_complete: true })
      .then((d: unknown) => setSetupDone((isRecord(d) ? d.setup_complete : true) !== false))
      .catch(() => setSetupDone(true))
  }, [])

  useEffect(() => {
    let alive = true
    const done = (enrolled: boolean, id: unknown) => { if (alive) { setOfflineEnrolled(enrolled); setOfflineIdentity(toOfflineIdentity(id)) } }
    // Never let the offline-enrollment probe block boot. A blocked/slow IndexedDB
    // would otherwise hang the "Loading…" gate for EVERY user (online included),
    // so race the probe against a timeout and fall through to online login.
    const timeout: Promise<'timeout'> = new Promise((resolve) => setTimeout(() => resolve('timeout'), 3000))
    const probe: Promise<{ enrolled: boolean; id: unknown }> = import('./lib/offlineAuth').then(async (oa) => {
      const enrolled = await oa.isEnrolled().catch(() => false)
      const id = enrolled ? await oa.getCachedIdentity().catch(() => null) : null
      return { enrolled, id }
    })
    Promise.race([probe, timeout])
      .then((r) => { if (r === 'timeout') done(false, null); else done(r.enrolled, r.id) })
      .catch(() => done(false, null))

    // After a wipe (attempt cap crossed / explicit "forget offline data"), the
    // credential is gone — clear the shell-owned app data too, and drop back to
    // online login rather than trapping the user on a disabled offline lock screen.
    const onWipe = () => {
      import('./core/AppBridge').then((b) => b.clearAllAppData?.()).catch(() => {})
      if (alive) { setOfflineEnrolled(false); setOfflineIdentity(null) }
    }
    window.addEventListener('vulos:offline-wipe', onWipe)
    return () => { alive = false; window.removeEventListener('vulos:offline-wipe', onWipe) }
  }, [])

  if (loading || setupDone === null || offlineEnrolled === null) {
    return (
      <div className="fixed inset-0 bg-neutral-950 flex items-center justify-center">
        <span className="text-neutral-600 text-sm">Loading...</span>
      </div>
    )
  }

  // First boot — show setup wizard
  if (!setupDone) return <Setup onComplete={() => setSetupDone(true)} />

  if (!user) {
    // Box unreachable AND this device has a cached offline credential → offer the
    // fail-closed offline unlock instead of an online login that cannot succeed.
    if (offline && offlineEnrolled) {
      return <OfflineLockScreen onUnlock={unlockOffline} identity={offlineIdentity} />
    }
    return <LoginScreen />
  }

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
            {/* SESSION-TAKEOVER: global; renders only when a fresh sign-in
                finds the account already active on another device. */}
            <SessionTakeoverModal />
          </AuthProvider>
        </WallpaperProvider>
      </ThemeProvider>
    </DeviceProfileProvider>
    </I18nProvider>
  )
}
