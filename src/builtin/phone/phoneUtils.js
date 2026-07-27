// Shared helpers for the Phone builtin (Messages + Calls tabs).

// SMS/call-log timestamps come back as epoch-ms longs (Telephony.Sms.DATE /
// CallLog.Calls.DATE); render newest-first lists with a compact relative age.
export function formatRelative(ms) {
  if (!ms) return ''
  const diff = Date.now() - Number(ms)
  if (diff < 60_000) return 'just now'
  if (diff < 3_600_000) return `${Math.floor(diff / 60_000)}m ago`
  if (diff < 86_400_000) return `${Math.floor(diff / 3_600_000)}h ago`
  if (diff < 604_800_000) return `${Math.floor(diff / 86_400_000)}d ago`
  return new Date(Number(ms)).toLocaleDateString([], { month: 'short', day: 'numeric' })
}

export function formatDuration(sec) {
  const s = Number(sec) || 0
  const m = Math.floor(s / 60)
  const r = s % 60
  return `${m}:${String(r).padStart(2, '0')}`
}

// Bridge rejections carry one of a small set of stable messages
// (native-unavailable / native-timeout / permission-denied) or a bare Error
// from the OS call itself — translate to something honest and actionable.
export function friendlyError(err) {
  const msg = err?.message || String(err || '')
  if (msg === 'native-unavailable') return 'Phone is only available in the Vulos Android app.'
  if (msg === 'native-timeout') return 'The phone app took too long to respond. Try again.'
  if (msg === 'permission-denied') return 'Permission was denied. Grant it in Android Settings → Apps → Vulos → Permissions.'
  return 'Something went wrong talking to the phone.'
}
