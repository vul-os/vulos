// nativeBridge — a complete promise-based SDK over the Vulos Android app's
// origin-gated native bridges. Each bridge is a `window.vulosX` object the app
// registers with WebViewCompat.addWebMessageListener (see mobile/app/.../
// MainActivity.kt + BridgeBase.kt). Wire protocol:
//   JS→native : vulosX.postMessage(JSON.stringify({ id, action, ...args }))
//   native→JS : vulosX.onmessage → { id, ok, ... } | { id, error }
//   push      : vulosX.onmessage → { event, ... }   (no id)
//
// In a plain browser / PWA these globals don't exist, so every method degrades
// honestly: `available` is false and calls reject with 'native-unavailable'.
// Callers feature-detect (`nativeBridge.camera.available`) and fall back to the
// web path. This module NEVER throws at import time.

const NAMES = ['Contacts', 'Camera', 'Notify', 'Files', 'Biometric', 'Launcher', 'Telephony'] as const
type BridgeName = typeof NAMES[number]

// The object each window.vulosX global exposes: a postMessage sink plus a
// settable onmessage sink for replies/pushes. Not a real MessagePort (no
// start()/close()) — just what BridgeBase.kt's addWebMessageListener wires up.
interface NativePort {
  postMessage(data: string): void
  onmessage: ((ev: MessageEvent<string>) => void) | null
}

declare global {
  interface Window {
    vulosContacts?: NativePort
    vulosCamera?: NativePort
    vulosNotify?: NativePort
    vulosFiles?: NativePort
    vulosBiometric?: NativePort
    vulosLauncher?: NativePort
    vulosTelephony?: NativePort
  }
}

function getPort(name: BridgeName): NativePort | null {
  if (typeof window === 'undefined') return null
  switch (name) {
    case 'Contacts': return window.vulosContacts ?? null
    case 'Camera': return window.vulosCamera ?? null
    case 'Notify': return window.vulosNotify ?? null
    case 'Files': return window.vulosFiles ?? null
    case 'Biometric': return window.vulosBiometric ?? null
    case 'Launcher': return window.vulosLauncher ?? null
    case 'Telephony': return window.vulosTelephony ?? null
  }
}

function isRecord(x: unknown): x is Record<string, unknown> {
  return typeof x === 'object' && x !== null
}

// A wire reply/push event is arbitrary native-side JSON — every field is read
// through a narrow accessor rather than trusted wholesale.
type WireMessage = Record<string, unknown>

function str(m: WireMessage, key: string): string | undefined {
  const v = m[key]
  return typeof v === 'string' ? v : undefined
}
function bool(m: WireMessage, key: string): boolean | undefined {
  const v = m[key]
  return typeof v === 'boolean' ? v : undefined
}
function num(m: WireMessage, key: string): number | undefined {
  const v = m[key]
  return typeof v === 'number' ? v : undefined
}
function strArray(m: WireMessage, key: string): string[] {
  const v = m[key]
  return Array.isArray(v) ? v.filter((x): x is string => typeof x === 'string') : []
}

interface Pending {
  resolve: (m: WireMessage) => void
  reject: (e: Error) => void
}

interface Channel {
  port: NativePort
  pending: Map<string, Pending>
  subs: Set<(m: WireMessage) => void>
}

const state: Partial<Record<BridgeName, Channel | null>> = {}
let seq = 0

function chan(name: BridgeName): Channel | null {
  if (name in state) return state[name] ?? null
  const port = getPort(name)
  if (!port) { state[name] = null; return null }
  const c: Channel = { port, pending: new Map(), subs: new Set() }
  port.onmessage = (ev) => {
    let m: unknown
    try { m = JSON.parse(ev.data) } catch { return }
    if (!isRecord(m)) return
    if (m.event !== undefined && m.id === undefined) { for (const h of c.subs) { try { h(m) } catch { /* ignore */ } } return }
    const id = typeof m.id === 'string' ? m.id : undefined
    const w = id === undefined ? undefined : c.pending.get(id)
    if (!w || id === undefined) return
    c.pending.delete(id)
    if (m.error) w.reject(new Error(String(m.error)))
    else w.resolve(m)
  }
  state[name] = c
  return c
}

const available = (name: BridgeName): boolean => !!chan(name)

// call — send one action and await its reply (rejects on error/timeout).
function call(name: BridgeName, action: string, args: Record<string, unknown> = {}, timeoutMs = 30_000): Promise<WireMessage> {
  const c = chan(name)
  if (!c) return Promise.reject(new Error('native-unavailable'))
  const id = 'nb' + (++seq)
  // Drop undefined args so we never send `"x":null` for optional fields.
  const clean: Record<string, unknown> = {}
  for (const k in args) if (args[k] !== undefined) clean[k] = args[k]
  return new Promise<WireMessage>((resolve, reject) => {
    c.pending.set(id, { resolve, reject })
    try { c.port.postMessage(JSON.stringify({ id, action, ...clean })) }
    catch (e) { c.pending.delete(id); reject(e instanceof Error ? e : new Error(String(e))); return }
    setTimeout(() => { if (c.pending.delete(id)) reject(new Error('native-timeout')) }, timeoutMs)
  })
}

// subscribe — receive push events ({ event, ... }) from a bridge. Returns an
// unsubscribe fn. No-op when the bridge is unavailable.
function subscribe(name: BridgeName, handler: (m: WireMessage) => void): () => void {
  const c = chan(name)
  if (!c) return () => {}
  c.subs.add(handler)
  return () => { c.subs.delete(handler) }
}

// subscribeActivate — register a push handler AND send the bridge's activation
// action (Files/Telephony only push after a `subscribe` message). Returns an
// unsubscribe fn.
function subscribeActivate(name: BridgeName, action: string, handler: (m: WireMessage) => void): () => void {
  const off = subscribe(name, handler)
  if (available(name)) call(name, action).catch(() => {})
  return off
}

// ── Reply shapes, narrowed field-by-field off the wire (see the matching
// mobile/app/.../*Bridge.kt for each's authoritative JSON shape) ────────────

export interface NativeContact {
  name: string
  phones: string[]
  emails: string[]
  org?: string
}
function toContacts(m: WireMessage): NativeContact[] {
  const arr = m.contacts
  if (!Array.isArray(arr)) return []
  return arr.filter(isRecord).map((r) => ({
    name: str(r, 'name') || '',
    phones: strArray(r, 'phones'),
    emails: strArray(r, 'emails'),
    org: str(r, 'org'),
  }))
}

export interface NativeOpenedFile {
  name?: string
  mime?: string
  sizeBytes?: number
  dataUrl?: string
}
function toOpenedFile(m: WireMessage): NativeOpenedFile {
  return { name: str(m, 'name'), mime: str(m, 'mime'), sizeBytes: num(m, 'sizeBytes'), dataUrl: str(m, 'dataUrl') }
}

export interface NativeLauncherStatus {
  isDefault?: boolean
  canRequest?: boolean
}
function toLauncherStatus(m: WireMessage): NativeLauncherStatus {
  return { isDefault: bool(m, 'isDefault'), canRequest: bool(m, 'canRequest') }
}

export const nativeBridge = {
  /** True only inside the Vulos Android app (at least one bridge present). */
  get inApp() { return NAMES.some(available) },
  call,
  subscribe,

  // ── Contacts — device + SIM address book ────────────────────────────────
  contacts: {
    get available() { return available('Contacts') },
    perms() { return call('Contacts', 'perms') },                            // { ok, readContacts }
    list(limit?: number) { return call('Contacts', 'list', { limit }).then(toContacts) },
    sim() { return call('Contacts', 'sim').then(toContacts) },
  },

  // ── Camera — capture + QR/barcode scan (delegates to system camera/ZXing) ─
  camera: {
    get available() { return available('Camera') },
    perms() { return call('Camera', 'perms') },                              // { ok, camera }
    /** Capture a photo → JPEG data: URL (base64). */
    capturePhoto(maxBytes?: number) { return call('Camera', 'photo.capture', { maxBytes }).then((r) => str(r, 'dataUrl')) },
    /** Capture a video → { uri, path, sizeBytes }. */
    captureVideo() { return call('Camera', 'video.capture') },
    /** Scan a QR/barcode → { text, format } (rejects if cancelled). */
    scanQR(prompt?: string) { return call('Camera', 'qr.scan', { prompt }).then((r) => ({ text: str(r, 'text') || '', format: str(r, 'format') || '' })) },
  },

  // ── Notify — user alerts + the box-connection foreground service ─────────
  notify: {
    get available() { return available('Notify') },
    perms() { return call('Notify', 'perms') },                              // { ok, postNotifications }
    enableService() { return call('Notify', 'service.enable') },
    disableService() { return call('Notify', 'service.disable') },
    serviceStatus() { return call('Notify', 'service.status').then((r) => !!r.running) },
    /** Post a native notification. */
    alert(opts: { title: string; text: string; tag?: string; ongoing?: boolean }) { return call('Notify', 'alert', opts) },
  },

  // ── Files — Storage Access Framework open/save + share in/out ───────────
  files: {
    get available() { return available('Files') },
    /** Pick a document (SAF) → { name, mime, sizeBytes, dataUrl? }. */
    open(opts: { mimeTypes?: string[]; maxBytes?: number } = {}) {
      return call('Files', 'open', { mimeTypes: opts.mimeTypes, maxBytes: opts.maxBytes }).then(toOpenedFile)
    },
    /** Save bytes to the device (SAF → Downloads) → { uri }. */
    save(opts: { name: string; mime?: string; dataBase64: string }) {
      return call('Files', 'save', { name: opts.name, mime: opts.mime, dataBase64: opts.dataBase64 }).then((r) => str(r, 'uri'))
    },
    /** Share out via the system sheet. */
    share(opts: { text?: string; url?: string; dataBase64?: string; name?: string; mime?: string }) { return call('Files', 'share', opts) },
    /** Receive inbound shares. handler({ event:'shareIn', text?, items:[…] }). Returns unsubscribe. */
    onShareIn(handler: (m: WireMessage) => void) { return subscribeActivate('Files', 'subscribe', handler) },
  },

  // ── Biometric — fingerprint / face (or device credential) presence check ─
  biometric: {
    get available() { return available('Biometric') },
    /** { ok, status, canAuthenticate } — is biometric enrolled/usable? */
    check() { return call('Biometric', 'available') },
    /** Prompt the system biometric sheet → true on success. */
    authenticate(opts: { title?: string; subtitle?: string; allowDeviceCredential?: boolean } = {}) {
      return call('Biometric', 'authenticate', opts).then((r) => !!r.ok)
    },
  },

  // ── Launcher — opt in/out of Vulos as the phone's home screen ───────────
  launcher: {
    get available() { return available('Launcher') },
    status() { return call('Launcher', 'status').then(toLauncherStatus) },   // { ok, isDefault, canRequest }
    setDefault() { return call('Launcher', 'setDefault') },
    openHomeSettings() { return call('Launcher', 'openHomeSettings') },
  },

  // ── Telephony — the box/phone GSM SIM: SMS + calls ──────────────────────
  telephony: {
    get available() { return available('Telephony') },
    perms() { return call('Telephony', 'perms') },
    sendSms(to: string, text: string) { return call('Telephony', 'sms.send', { to, text }) },
    listSms(limit?: number) { return call('Telephony', 'sms.list', { limit }).then((r) => r.messages ?? r.sms ?? []) },
    dial(number: string) { return call('Telephony', 'call.dial', { number }) },
    listCallLog(limit?: number) { return call('Telephony', 'calllog.list', { limit }).then((r) => r.calls ?? r.log ?? []) },
    /** Receive inbound SMS. handler({ event:'sms', from, body, timestamp }). Returns unsubscribe. */
    onSms(handler: (m: WireMessage) => void) { return subscribeActivate('Telephony', 'subscribe', handler) },
  },
}

export default nativeBridge
