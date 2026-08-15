// Shared helpers for the Phone app (Recents + Contacts + Keypad + Messages).

export function isRecord(x: unknown): x is Record<string, unknown> {
  return typeof x === 'object' && x !== null
}

// Booleans reported by the Android TelephonyBridge's `perms` action. Every field
// is optional because a missing/non-boolean key must NOT read as "denied" (the
// permission banner only shows for an explicit `false`). Only relevant for the
// optional device-SIM line; the box's own modem has no runtime permissions.
export interface TelephonyPerms {
  readSms?: boolean
  sendSms?: boolean
  callLog?: boolean
  call?: boolean
}

function boolField(r: Record<string, unknown>, key: string): boolean | undefined {
  const v = r[key]
  return typeof v === 'boolean' ? v : undefined
}

export function toPerms(x: unknown): TelephonyPerms | null {
  if (!isRecord(x)) return null
  return {
    readSms: boolField(x, 'readSms'),
    sendSms: boolField(x, 'sendSms'),
    callLog: boolField(x, 'callLog'),
    call: boolField(x, 'call'),
  }
}

// Timestamps: the box's GSM service emits epoch SECONDS (Call.TS / Message.TS in
// Go), the Android bridge emits epoch MILLISECONDS (CallLog.Calls.DATE). Mixing
// them silently puts every box row in 1970, so normalise at the boundary rather
// than hoping each call site remembers which it has.
export function secondsToMs(sec: unknown): number {
  const n = Number(sec)
  return Number.isFinite(n) ? n * 1000 : 0
}

export function formatRelative(ms: unknown): string {
  if (!ms) return ''
  const t = Number(ms)
  if (!Number.isFinite(t) || t <= 0) return ''
  const diff = Date.now() - t
  if (diff < 60_000) return 'just now'
  if (diff < 3_600_000) return `${Math.floor(diff / 60_000)}m ago`
  if (diff < 86_400_000) return `${Math.floor(diff / 3_600_000)}h ago`
  if (diff < 604_800_000) return `${Math.floor(diff / 86_400_000)}d ago`
  return new Date(t).toLocaleDateString([], { month: 'short', day: 'numeric' })
}

export function formatClock(ms: unknown): string {
  const t = Number(ms)
  if (!Number.isFinite(t) || t <= 0) return ''
  return new Date(t).toLocaleTimeString([], { hour: 'numeric', minute: '2-digit' })
}

export function formatDuration(sec: unknown): string {
  const s = Math.max(0, Math.floor(Number(sec) || 0))
  const m = Math.floor(s / 60)
  const r = s % 60
  return `${m}:${String(r).padStart(2, '0')}`
}

// Bridge rejections carry one of a small set of stable messages
// (native-unavailable / native-timeout / permission-denied) or a bare Error.
export function friendlyError(err: unknown): string {
  const msg = (isRecord(err) && typeof err.message === 'string' && err.message) || String(err || '')
  if (msg === 'native-unavailable') return 'This phone’s own SIM is only reachable from the Vulos Android app.'
  if (msg === 'native-timeout') return 'The phone took too long to respond. Try again.'
  if (msg === 'permission-denied') return 'Permission was denied. Grant it in Android Settings → Apps → Vulos → Permissions.'
  return msg || 'Something went wrong.'
}

/** initials for an avatar bubble; falls back to a dialled number's last digits. */
export function initials(name: string, number = ''): string {
  const parts = (name || '').trim().split(/\s+/).filter(Boolean)
  if (parts.length >= 2) return (parts[0][0] + parts[parts.length - 1][0]).toUpperCase()
  if (parts.length === 1) return parts[0].slice(0, 2).toUpperCase()
  const d = (number || '').replace(/\D/g, '')
  return d ? d.slice(-2) : '?'
}

// A stable, non-random avatar tint per contact. Deterministic from the key so a
// person keeps the same colour across reloads and across the Recents/Contacts
// tabs — an avatar that changes colour on every render reads as a different
// person.
const AVATAR_HUES = [210, 265, 150, 35, 340, 190, 95, 310]
export function avatarHue(key: string): number {
  let h = 0
  for (let i = 0; i < key.length; i++) h = (h * 31 + key.charCodeAt(i)) >>> 0
  return AVATAR_HUES[h % AVATAR_HUES.length]
}

/**
 * Group numbers for readability without pretending to know the country's
 * formatting rules. We deliberately do NOT reformat into a national pattern:
 * this box may be on any network in any country, and a wrong "+1 (555)"-shaped
 * guess is worse than the digits the user actually dialled. E.164 numbers get
 * their country code split off; everything else is returned untouched.
 */
export function displayNumber(raw: string): string {
  const s = (raw || '').trim()
  if (!s.startsWith('+')) return s
  const d = s.slice(1).replace(/\D/g, '')
  if (d.length < 8 || d.length > 15) return s
  const cc = d.length > 11 ? d.slice(0, 3) : d.length > 10 ? d.slice(0, 2) : d.slice(0, 1)
  const rest = d.slice(cc.length)
  const groups = rest.replace(/(\d{1,3})(?=(\d{3})+$)/g, '$1 ')
  return `+${cc} ${groups}`.replace(/\s+/g, ' ').trim()
}
