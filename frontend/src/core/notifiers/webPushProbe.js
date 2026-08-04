// webPushProbe.js — pure device/box probe behind the Web Push settings toggle.
//
// Extracted from the component so it can be unit-tested and so the component
// file only exports a component (react-refresh). Returns one of:
//   'unsupported'  — this browser can't do Web Push at all
//   'unconfigured' — the box has no VAPID keys (push send-path is off)
//   'denied'       — the user blocked notifications at the browser level
//   'on'           — this device already has a live subscription
//   'off'          — supported + configured, but this device isn't subscribed
import { pushSupported } from './webPush'

export async function probeState(deps = {}) {
  const doFetch = deps.fetch || (typeof fetch !== 'undefined' ? fetch : null)
  if (!doFetch || !pushSupported()) return 'unsupported'

  // Is the send-path configured on the box?
  let vapid
  try {
    const res = await doFetch('/api/notifications/push/vapid-public', {
      headers: { Accept: 'application/json' },
    })
    if (!res || !res.ok) return 'unconfigured'
    vapid = await res.json()
  } catch {
    return 'unconfigured'
  }
  if (!vapid || !vapid.enabled || !vapid.publicKey) return 'unconfigured'

  if (typeof Notification !== 'undefined' && Notification.permission === 'denied') return 'denied'

  // Does THIS device already hold a subscription?
  try {
    const reg = deps.registrationPromise || navigator.serviceWorker.ready
    const registration = await reg
    const sub = await registration.pushManager.getSubscription()
    return sub ? 'on' : 'off'
  } catch {
    return 'off'
  }
}
