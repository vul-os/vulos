// WebPushToggle.jsx — the opt-in surface for the cell-side Web Push send-path
// (PUSH-CELL-01). The client helper (webPush.js) has always existed but was
// never surfaced; this is its settings home.
//
// Web Push is DEVICE-scoped (a PushManager subscription belongs to this
// browser/profile), so this toggle reflects THIS device's state — not an
// account-wide preference. When on, the box sends notifications to this device
// even while the tab is closed; the vendor (FCM/Apple/Mozilla) routes but can't
// read the RFC-8291-encrypted payload.
//
// The toggle is deliberately fail-safe: if the browser lacks push support, or
// the box has no VAPID keys configured, or permission is denied, it renders an
// honest disabled/explanatory state rather than a dead switch. Do Not Disturb
// (the store's `muted` pref) is honoured by the BOX before it ever sends a push,
// so we surface that relationship inline rather than duplicating the control.

import { useState, useEffect, useCallback, useRef, useSyncExternalStore } from 'react'
import { enableWebPush, disableWebPush } from './webPush'
import { probeState } from './webPushProbe'
import { getPrefs, subscribePrefs } from '../notificationStore'

export default function WebPushToggle({ deps }) {
  const [state, setState] = useState('loading') // loading|unsupported|unconfigured|denied|on|off
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState(null)
  const mounted = useRef(true)
  const prefs = useSyncExternalStore(subscribePrefs, getPrefs)

  useEffect(() => {
    mounted.current = true
    probeState(deps).then((s) => { if (mounted.current) setState(s) })
    return () => { mounted.current = false }
  }, [deps])

  const toggle = useCallback(async (next) => {
    setBusy(true)
    setError(null)
    try {
      if (next) {
        const sub = await enableWebPush(deps)
        if (!mounted.current) return
        if (sub) {
          setState('on')
        } else {
          // enable failed — re-probe to show the honest reason (denied / off).
          const s = await probeState(deps)
          if (!mounted.current) return
          setState(s)
          if (s === 'off') setError('Could not enable push on this device.')
        }
      } else {
        await disableWebPush(deps)
        if (!mounted.current) return
        setState('off')
      }
    } catch {
      if (mounted.current) setError('Something went wrong. Try again.')
    } finally {
      if (mounted.current) setBusy(false)
    }
  }, [deps])

  // Non-actionable states render an explanatory row instead of a live switch.
  if (state === 'loading') {
    return <ExplainRow label="Push notifications" hint="Checking this device…" muted />
  }
  if (state === 'unsupported') {
    return <ExplainRow label="Push notifications" hint="This browser doesn’t support Web Push." muted />
  }
  if (state === 'unconfigured') {
    return (
      <ExplainRow
        label="Push notifications"
        hint="Not available — your box hasn’t enabled the Web Push send-path."
        muted
      />
    )
  }
  if (state === 'denied') {
    return (
      <ExplainRow
        label="Push notifications"
        hint="Blocked in your browser settings. Re-allow notifications for this site to turn on."
        muted
      />
    )
  }

  const on = state === 'on'
  return (
    <div>
      <div className="flex items-center justify-between py-2">
        <span className="text-sm">
          Push notifications
          <span className="block text-[12px] text-neutral-500">
            {on
              ? 'This device receives notifications even when Vulos is closed.'
              : 'Get notifications on this device even when Vulos is closed.'}
          </span>
        </span>
        <button
          type="button"
          role="switch"
          aria-checked={on}
          aria-busy={busy || undefined}
          aria-label="Push notifications on this device"
          disabled={busy}
          onClick={() => toggle(!on)}
          className={`shrink-0 w-10 h-5 rounded-full transition-colors relative disabled:opacity-50 disabled:cursor-progress ${on ? 'bg-blue-600' : 'bg-neutral-700'}`}>
          <span aria-hidden="true" className={`absolute top-0.5 w-4 h-4 rounded-full bg-white transition-transform ${on ? 'left-5' : 'left-0.5'}`} />
        </button>
      </div>
      {error && (
        <p role="alert" className="text-[12px] text-red-400 pb-1">{error}</p>
      )}
      {on && prefs.muted && (
        <p className="text-[12px] text-amber-500/90 pb-1">
          Do Not Disturb is on — the box holds pushes back until you turn it off.
        </p>
      )}
    </div>
  )
}

// ExplainRow — a non-interactive row that mirrors the Toggle layout so a
// disabled/unavailable push state reads consistently in the list.
function ExplainRow({ label, hint, muted }) {
  return (
    <div className="flex items-center justify-between py-2">
      <span className="text-sm">
        {label}
        <span className="block text-[12px] text-neutral-500">{hint}</span>
      </span>
      <span
        aria-hidden="true"
        className={`shrink-0 w-10 h-5 rounded-full relative ${muted ? 'bg-neutral-800' : 'bg-neutral-700'}`}>
        <span className="absolute top-0.5 left-0.5 w-4 h-4 rounded-full bg-neutral-600" />
      </span>
    </div>
  )
}
