// telephonyApi.ts — the Phone app's read/write seam over the box's GSM service.
//
// WHICH GSM: the box's own, via /api/telephony/*, which speaks to whatever
// ModemManager sees — a USB LTE stick, an M.2 card, a PCIe modem. There is NO
// Android assumption anywhere here. The Android phone's own SIM is a *second,
// optional* line reached through nativeBridge.telephony (see lines.ts); it is
// not the default and not required.
//
// TWO FAILURE MODES, BOTH HANDLED:
//
//  1. Transport/HTTP failure. Every read goes through lib/api's request(),
//     which throws on non-2xx. It is NOT raw fetch: a raw fetch + `.json()` +
//     "is this the shape I wanted?" narrowing maps a 500's JSON error body onto
//     an empty list, and the app then renders its designed empty state. For a
//     phone that is the worst possible failure — "no calls yet" and "the
//     telephony service is down" must never look the same.
//
//  2. A 200 that carries an error. POST /api/telephony/{call,sms/send} answer
//     `{"error":"telephony: no modem"}` with status 200 (see handlers.go), so
//     checking res.ok is NOT sufficient on the write path. okOrThrow reads the
//     body's error field and throws it. Without this, dialling with no modem
//     resolves successfully and the UI reports a call that never happened.

import { request } from '../../lib/api'

function isRecord(x: unknown): x is Record<string, unknown> {
  return typeof x === 'object' && x !== null
}

function str(x: unknown): string {
  return typeof x === 'string' ? x : ''
}

function num(x: unknown): number {
  return typeof x === 'number' && Number.isFinite(x) ? x : 0
}

/** Thrown for both failure modes above, so callers have one thing to catch. */
export class TelephonyError extends Error {
  constructor(message: string) {
    super(message)
    this.name = 'TelephonyError'
  }
}

// The box prefixes its own errors with "telephony: "; that is service-internal
// vocabulary, not something to show a person holding a phone.
function humanise(raw: string): string {
  const m = raw.replace(/^telephony:\s*/, '').trim()
  if (!m) return 'The call could not be placed.'
  if (/no modem/i.test(m)) return 'No modem is connected to this box.'
  if (/invalid number/i.test(m)) return 'That number is not dialable.'
  if (/voice unavailable/i.test(m)) return 'This modem does not support calls — it is data/SMS only.'
  if (/no provider/i.test(m)) return 'No second-number provider is configured.'
  return m.charAt(0).toUpperCase() + m.slice(1)
}

// okOrThrow — the guard for failure mode 2.
function okOrThrow(body: unknown): void {
  if (isRecord(body) && typeof body.error === 'string' && body.error) {
    throw new TelephonyError(humanise(body.error))
  }
}

// ─── status ────────────────────────────────────────────────────────────────

export interface LineStatus {
  available: boolean
  state: string
  signal: number
  operator: string
  number: string
  /** False on a data/SMS-only modem — the dial pad must say so, not just fail. */
  voice: boolean
}

const NO_LINE: LineStatus = { available: false, state: '', signal: 0, operator: '', number: '', voice: false }

export function toLineStatus(x: unknown): LineStatus {
  if (!isRecord(x)) return NO_LINE
  return {
    available: x.available === true,
    state: str(x.state),
    signal: num(x.signal_quality),
    operator: str(x.operator),
    number: str(x.number),
    voice: x.voice === true,
  }
}

export async function getStatus(): Promise<LineStatus> {
  return toLineStatus(await request('/telephony/status'))
}

export interface VirtualLineStatus {
  configured: boolean
  provider: string
  number: string
  canCall: boolean
}

export async function getVirtualStatus(): Promise<VirtualLineStatus> {
  const x = await request('/telephony/virtual/status')
  if (!isRecord(x)) return { configured: false, provider: '', number: '', canCall: false }
  return {
    configured: x.configured === true,
    provider: str(x.provider),
    number: str(x.number),
    canCall: x.can_call === true,
  }
}

// ─── calls ─────────────────────────────────────────────────────────────────

export type CallDirection = 'incoming' | 'outgoing' | 'missed'

/** One Recents row, from any source. `origin` is what the row is badged with. */
export interface CallEntry {
  id: string
  number: string
  /** Display name, resolved against the address book (not on the wire). */
  name?: string
  direction: CallDirection
  ts: number
  duration: number
  origin: 'gsm' | 'peer'
  /** Peer rows carry an id rather than a dialable number. */
  peerId?: string
}

function toDirection(x: unknown): CallDirection {
  const v = str(x)
  return v === 'incoming' || v === 'outgoing' || v === 'missed' ? v : 'incoming'
}

function toGsmCall(x: unknown, i: number): CallEntry | null {
  if (!isRecord(x)) return null
  return {
    id: str(x.id) || `gsm:${i}`,
    number: str(x.number),
    direction: toDirection(x.direction),
    ts: num(x.ts),
    duration: num(x.duration),
    origin: 'gsm',
  }
}

export async function getCalls(): Promise<CallEntry[]> {
  const data = await request('/telephony/calls')
  if (!Array.isArray(data)) {
    // The endpoint is documented to answer a JSON list. Anything else is a
    // service that is not behaving, and saying "no calls" would hide that.
    throw new TelephonyError('The telephony service returned something unreadable.')
  }
  return data.map(toGsmCall).filter((c): c is CallEntry => c !== null)
}

// Peer (Vulos-to-Vulos) call history. Separate endpoint, separate service,
// separate identity space — see peerCallsNote in Recents for what this is and,
// importantly, what it is not.
function toPeerCall(x: unknown, i: number): CallEntry | null {
  if (!isRecord(x)) return null
  const status = str(x.status)
  const direction: CallDirection =
    status === 'missed' || status === 'rejected' ? 'missed'
      : str(x.direction) === 'outbound' ? 'outgoing' : 'incoming'
  const started = str(x.started_at)
  const ms = started ? Date.parse(started) : NaN
  return {
    id: str(x.id) || `peer:${i}`,
    number: '',
    name: str(x.peer_display) || str(x.peer_id),
    direction,
    ts: Number.isFinite(ms) ? Math.floor(ms / 1000) : 0,
    duration: num(x.duration_sec),
    origin: 'peer',
    peerId: str(x.peer_id),
  }
}

export async function getPeerCalls(): Promise<CallEntry[]> {
  const data = await request('/peering/call/history?limit=50')
  if (!Array.isArray(data)) return []
  return data.map(toPeerCall).filter((c): c is CallEntry => c !== null)
}

export async function placeCall(number: string): Promise<void> {
  okOrThrow(await request('/telephony/call', { method: 'POST', body: JSON.stringify({ to: number }) }))
}

export async function hangUp(): Promise<void> {
  okOrThrow(await request('/telephony/call/hangup', { method: 'POST' }))
}

// ─── SMS ───────────────────────────────────────────────────────────────────

export interface SmsThread {
  number: string
  contactName: string
  lastBody: string
  lastTs: number
  unread: number
}

export interface SmsMessage {
  direction: string
  body: string
  ts: number
  status: string
}

export async function getThreads(): Promise<SmsThread[]> {
  const data = await request('/telephony/sms/threads')
  if (!Array.isArray(data)) throw new TelephonyError('The telephony service returned something unreadable.')
  return data.filter(isRecord).map((t) => ({
    number: str(t.number),
    contactName: str(t.contact_name),
    lastBody: str(t.last_body),
    lastTs: num(t.last_ts),
    unread: num(t.unread_count),
  }))
}

export async function getThread(number: string): Promise<SmsMessage[]> {
  const data = await request('/telephony/sms/thread/' + encodeURIComponent(number))
  if (!Array.isArray(data)) throw new TelephonyError('The telephony service returned something unreadable.')
  return data.filter(isRecord).map((m) => ({
    direction: str(m.direction),
    body: str(m.body),
    ts: num(m.ts),
    status: str(m.status),
  }))
}

export async function sendSms(to: string, body: string): Promise<void> {
  okOrThrow(await request('/telephony/sms/send', { method: 'POST', body: JSON.stringify({ to, body }) }))
}

// ─── unified address book ──────────────────────────────────────────────────

export interface PhoneContact {
  id: string
  name: string
  phones: string[]
  emails: string[]
  org: string
  sources: string[]
}

/**
 * digitKey — the match key for "is this the same number?", mirroring the box's
 * own normalize.go: the last 9 digits. That is what makes "+27 83 111 2222",
 * "083 111 2222" and "0831112222" resolve to one contact, which is the whole
 * point of a call log that shows names.
 */
export function digitKey(raw: string): string {
  const d = (raw || '').replace(/\D/g, '')
  return d.length > 9 ? d.slice(-9) : d
}

export async function getContacts(): Promise<PhoneContact[]> {
  const data = await request('/contacts/unified')
  const list = isRecord(data) && Array.isArray(data.contacts) ? data.contacts : null
  if (list === null) throw new TelephonyError('The contacts service returned something unreadable.')
  return list.filter(isRecord).map((c, i) => ({
    id: str(c.id) || `c:${i}`,
    name: str(c.name),
    phones: Array.isArray(c.phones) ? c.phones.filter((p): p is string => typeof p === 'string') : [],
    emails: Array.isArray(c.emails) ? c.emails.filter((e): e is string => typeof e === 'string') : [],
    org: str(c.org),
    sources: Array.isArray(c.sources) ? c.sources.filter((s): s is string => typeof s === 'string') : [],
  }))
}

/** Builds number-key → contact-name index for resolving call/SMS rows. */
export function nameIndex(contacts: PhoneContact[]): Map<string, string> {
  const idx = new Map<string, string>()
  for (const c of contacts) {
    if (!c.name) continue
    for (const p of c.phones) {
      const k = digitKey(p)
      if (k && !idx.has(k)) idx.set(k, c.name)
    }
  }
  return idx
}
