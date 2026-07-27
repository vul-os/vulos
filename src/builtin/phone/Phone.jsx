/**
 * Phone — SMS + calls over the Android Telephony native bridge.
 *
 * Only meaningful inside the Vulos Android app on a device with a GSM SIM:
 * `nativeBridge.telephony` talks to TelephonyBridge.kt, which reads/writes the
 * device's real SMS + call-log content providers and places calls through the
 * system dialer. In a plain browser (or the app on hardware with no SIM) the
 * bridge is simply absent — this app renders an honest explainer, never fake
 * messages or calls.
 *
 * Two tabs: Messages (list + live inbound + composer) and Calls (log + dial).
 */
import { useState, useEffect, useCallback } from 'react'
import { nativeBridge } from '../../core/nativeBridge'
import MessagesTab from './MessagesTab'
import CallsTab from './CallsTab'

// TEL-01 permission keys, per tab — used to decide whether to show the
// "grant access" banner (perms() reports booleans; see TelephonyBridge.kt).
const NEEDED = {
  messages: ['readSms', 'sendSms'],
  calls: ['callLog', 'call'],
}

function PermissionBanner({ tab, perms, onRetry }) {
  const missing = NEEDED[tab].filter((k) => perms && perms[k] === false)
  if (missing.length === 0) return null
  const label = tab === 'messages' ? 'SMS' : 'call'
  return (
    <div className="shrink-0 flex items-center gap-2.5 px-3 py-2.5 border-b text-[12px]"
      style={{ borderColor: 'var(--border-subtle, rgba(255,255,255,0.08))', background: 'color-mix(in srgb, var(--accent) 8%, transparent)' }}>
      <span style={{ color: 'var(--accent)' }}>●</span>
      <span className="flex-1" style={{ color: 'var(--text-primary)' }}>
        Vulos needs {label} permission on this phone.
      </span>
      <button type="button" onClick={onRetry}
        className="shrink-0 px-2.5 py-1.5 rounded-md text-[11.5px] font-medium text-white hover:brightness-110 active:scale-95 focus-primary transition-all"
        style={{ background: 'var(--accent)' }}>
        Grant access
      </button>
    </div>
  )
}

function UnavailableState() {
  return (
    <div className="h-full grid place-items-center text-center px-6 animate-[fadeIn_0.2s_ease-out]"
      style={{ background: 'var(--bg-surface)' }} data-phone-app>
      <div className="max-w-xs">
        <div className="w-14 h-14 mx-auto mb-4 grid place-items-center rounded-2xl text-2xl"
          style={{ background: 'var(--accent-soft)', borderRadius: 'var(--radius-lg, 1rem)' }}>
          📶
        </div>
        <div className="text-sm font-medium" style={{ color: 'var(--text-primary)' }}>
          No SIM on this device.
        </div>
        <div className="text-[12px] mt-1.5 leading-relaxed" style={{ color: 'var(--text-secondary, #888)' }}>
          Phone (SMS + calls) only works inside the Vulos Android app, on the
          phone that holds the GSM SIM. Open Vulos there to see it.
        </div>
      </div>
    </div>
  )
}

export default function Phone() {
  const available = nativeBridge.telephony.available
  const [tab, setTab] = useState('messages')
  const [perms, setPerms] = useState(null)

  const refreshPerms = useCallback(() => {
    if (!available) return
    nativeBridge.telephony.perms().then(setPerms).catch(() => setPerms(null))
  }, [available])

  useEffect(() => { refreshPerms() }, [refreshPerms])

  // "Grant access" re-issues the read action for the active tab — Android's
  // runtime permission dialog surfaces on that native call (see withPerm in
  // TelephonyBridge.kt), then we re-read perms() to update the banner.
  const requestAccess = () => {
    const ask = tab === 'messages' ? nativeBridge.telephony.listSms(1) : nativeBridge.telephony.listCallLog(1)
    ask.catch(() => {}).then(refreshPerms)
  }

  if (!available) return <UnavailableState />

  return (
    <div className="h-full flex flex-col" style={{ background: 'var(--bg-surface)', color: 'var(--text-primary)' }} data-phone-app>
      <div className="shrink-0 flex items-center gap-1 px-2 pt-2">
        {[['messages', 'Messages'], ['calls', 'Calls']].map(([id, label]) => (
          <button key={id} type="button" onClick={() => setTab(id)}
            className="px-3.5 py-2 text-[13px] font-medium rounded-t-md transition-colors focus-primary"
            style={{
              color: tab === id ? 'var(--text-primary)' : 'var(--text-secondary, #888)',
              borderBottom: tab === id ? '2px solid var(--accent)' : '2px solid transparent',
            }}>
            {label}
          </button>
        ))}
      </div>
      <PermissionBanner tab={tab} perms={perms} onRetry={requestAccess} />
      <div className="flex-1 min-h-0">
        {tab === 'messages'
          ? <MessagesTab perms={perms} onPermsChange={setPerms} />
          : <CallsTab perms={perms} onPermsChange={setPerms} />}
      </div>
    </div>
  )
}
