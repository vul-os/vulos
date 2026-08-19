import { useState, useEffect, useCallback } from 'react'
import { nativeBridge } from '../nativeBridge'
import { Section, Card, SettingRow, Toggle, Pill } from './ui'

// ---------------------------------------------------------------------------
// DevicePanel — Settings -> Devices -> This device (Vulos Android app only).
//
// Surfaces three opt-in native-bridge controls: keeping the box reachable via
// a foreground service (Notify bridge), taking over the phone's home screen
// (Launcher bridge), and a biometric-unlock preference the auth flow reads.
// Settings.jsx only mounts this route when nativeBridge.inApp — invisible in
// a plain browser/PWA.
// ---------------------------------------------------------------------------

interface LauncherStatus {
  isDefault?: boolean
  canRequest?: boolean
}

const BIOMETRIC_KEY = 'vulos.biometric.unlock'
const readBiometricPref = (): boolean => { try { return localStorage.getItem(BIOMETRIC_KEY) === 'on' } catch { return false } }

export default function DevicePanel() {
  const [serviceOn, setServiceOn] = useState<boolean | null>(null) // null = loading
  const [serviceBusy, setServiceBusy] = useState(false)
  const [launcherStatus, setLauncherStatus] = useState<LauncherStatus | null>(null)
  const [biometricOn, setBiometricOn] = useState(readBiometricPref)

  const refreshService = useCallback(() => {
    if (!nativeBridge.notify.available) return Promise.resolve()
    return nativeBridge.notify.serviceStatus().then(setServiceOn).catch(() => setServiceOn(false))
  }, [])
  const refreshLauncher = useCallback(() => {
    if (!nativeBridge.launcher.available) return
    nativeBridge.launcher.status().then(setLauncherStatus).catch(() => {})
  }, [])

  useEffect(() => { refreshService(); refreshLauncher() }, [refreshService, refreshLauncher])

  const toggleService = async (next: boolean) => {
    setServiceBusy(true)
    try {
      await (next ? nativeBridge.notify.enableService() : nativeBridge.notify.disableService())
      await refreshService()
    } finally {
      setServiceBusy(false)
    }
  }

  const toggleBiometric = (next: boolean) => {
    setBiometricOn(next)
    try { localStorage.setItem(BIOMETRIC_KEY, next ? 'on' : 'off') } catch { /* storage unavailable */ }
  }

  return (
    <Section title="This device" desc="Settings specific to the Vulos app on this Android device.">
      <Card
        title="Stay connected"
        desc="Runs a small foreground service so this device keeps your box reachable in the background — otherwise Android may pause the connection and delay notifications while the app isn't in view."
      >
        <SettingRow
          label="Background connection"
          desc={serviceOn == null ? 'Checking…' : serviceOn ? 'Running' : 'Off — notifications may be delayed in the background'}
          control={<Toggle checked={!!serviceOn} disabled={serviceOn == null || serviceBusy} onChange={toggleService} />}
        />
      </Card>

      {nativeBridge.launcher.available && (
        <Card
          title="Use Vulos as home screen"
          desc="Opt in to make Vulos the screen you see when you press Home. You can change or undo this at any time."
        >
          <SettingRow
            label="Home screen"
            // `launcherStatus` is null until the native bridge answers, and
            // stays null for ever if that call rejects — refreshLauncher
            // swallows the error. `null?.isDefault` is undefined, which is
            // falsy, so the Pill printed the definite "Not set" for a question
            // nobody had answered yet. The Background connection row directly
            // above gets this right with an explicit `== null` check, so the
            // pattern was already in the file.
            control={<Pill tone={launcherStatus?.isDefault ? 'success' : 'neutral'}>{launcherStatus == null ? 'Checking…' : launcherStatus.isDefault ? 'Active' : 'Not set'}</Pill>}
          />
          <div className="mt-3 flex gap-2">
            {!launcherStatus?.isDefault ? (
              <button
                onClick={() => nativeBridge.launcher.setDefault().then(refreshLauncher).catch(() => {})}
                // Unknown is not permission. This read `!= null &&`, so while
                // the status was still unknown the guard evaluated false and
                // the button was live — offering an action we did not yet know
                // this device would accept, next to a Pill that was already
                // claiming to know the answer.
                disabled={launcherStatus == null || !launcherStatus.canRequest}
                className="btn-primary text-sm disabled:opacity-40"
              >
                Set as home screen
              </button>
            ) : (
              <button onClick={() => nativeBridge.launcher.openHomeSettings()} className="btn-secondary text-sm">
                Change / undo
              </button>
            )}
          </div>
        </Card>
      )}

      {nativeBridge.biometric?.available && (
        <Card
          title="Unlock with biometrics"
          desc="Use this device's fingerprint or face unlock instead of typing your PIN. Off by default."
        >
          <SettingRow label="Biometric unlock" control={<Toggle checked={biometricOn} onChange={toggleBiometric} />} />
        </Card>
      )}
    </Section>
  )
}
