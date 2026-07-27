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

const NAMES = ['Contacts', 'Camera', 'Notify', 'Files', 'Biometric', 'Launcher', 'Telephony']

const state = {} // name -> { port, pending:Map, subs:Set } | null
let seq = 0

function chan(name) {
  if (name in state) return state[name]
  const port = (typeof window !== 'undefined' && window['vulos' + name]) || null
  if (!port) { state[name] = null; return null }
  const c = { port, pending: new Map(), subs: new Set() }
  port.onmessage = (ev) => {
    let m
    try { m = JSON.parse(ev.data) } catch { return }
    if (m && m.event !== undefined && m.id === undefined) { for (const h of c.subs) { try { h(m) } catch { /* ignore */ } } return }
    const w = c.pending.get(m.id)
    if (!w) return
    c.pending.delete(m.id)
    if (m.error) w.reject(new Error(m.error))
    else w.resolve(m)
  }
  state[name] = c
  return c
}

const available = (name) => !!chan(name)

// call — send one action and await its reply (rejects on error/timeout).
function call(name, action, args = {}, timeoutMs = 30_000) {
  const c = chan(name)
  if (!c) return Promise.reject(new Error('native-unavailable'))
  const id = 'nb' + (++seq)
  // Drop undefined args so we never send `"x":null` for optional fields.
  const clean = {}
  for (const k in args) if (args[k] !== undefined) clean[k] = args[k]
  return new Promise((resolve, reject) => {
    c.pending.set(id, { resolve, reject })
    try { c.port.postMessage(JSON.stringify({ id, action, ...clean })) }
    catch (e) { c.pending.delete(id); reject(e); return }
    setTimeout(() => { if (c.pending.delete(id)) reject(new Error('native-timeout')) }, timeoutMs)
  })
}

// subscribe — receive push events ({ event, ... }) from a bridge. Returns an
// unsubscribe fn. No-op when the bridge is unavailable.
function subscribe(name, handler) {
  const c = chan(name)
  if (!c) return () => {}
  c.subs.add(handler)
  return () => c.subs.delete(handler)
}

// subscribeActivate — register a push handler AND send the bridge's activation
// action (Files/Telephony only push after a `subscribe` message). Returns an
// unsubscribe fn.
function subscribeActivate(name, action, handler) {
  const off = subscribe(name, handler)
  if (available(name)) call(name, action).catch(() => {})
  return off
}

export const nativeBridge = {
  /** True only inside the Vulos Android app (at least one bridge present). */
  get inApp() { return NAMES.some(available) },
  call,
  subscribe,

  // ── Contacts — device + SIM address book ────────────────────────────────
  contacts: {
    get available() { return available('Contacts') },
    perms: () => call('Contacts', 'perms'),                                   // { ok, readContacts }
    list: (limit) => call('Contacts', 'list', { limit }).then((r) => r.contacts || []),
    sim: () => call('Contacts', 'sim').then((r) => r.contacts || []),
  },

  // ── Camera — capture + QR/barcode scan (delegates to system camera/ZXing) ─
  camera: {
    get available() { return available('Camera') },
    perms: () => call('Camera', 'perms'),                                     // { ok, camera }
    /** Capture a photo → JPEG data: URL (base64). */
    capturePhoto: (maxBytes) => call('Camera', 'photo.capture', { maxBytes }).then((r) => r.dataUrl),
    /** Capture a video → { uri, path, sizeBytes }. */
    captureVideo: () => call('Camera', 'video.capture'),
    /** Scan a QR/barcode → { text, format } (rejects if cancelled). */
    scanQR: (prompt) => call('Camera', 'qr.scan', { prompt }).then((r) => ({ text: r.text, format: r.format })),
  },

  // ── Notify — user alerts + the box-connection foreground service ─────────
  notify: {
    get available() { return available('Notify') },
    perms: () => call('Notify', 'perms'),                                     // { ok, postNotifications }
    enableService: () => call('Notify', 'service.enable'),
    disableService: () => call('Notify', 'service.disable'),
    serviceStatus: () => call('Notify', 'service.status').then((r) => !!r.running),
    /** Post a native notification. { title, text, tag?, ongoing? } */
    alert: (opts) => call('Notify', 'alert', opts),
  },

  // ── Files — Storage Access Framework open/save + share in/out ───────────
  files: {
    get available() { return available('Files') },
    /** Pick a document (SAF) → { name, mime, sizeBytes, dataUrl? }. */
    open: (opts = {}) => call('Files', 'open', { mimeTypes: opts.mimeTypes, maxBytes: opts.maxBytes }),
    /** Save bytes to the device (SAF → Downloads) → { uri }. */
    save: (opts) => call('Files', 'save', { name: opts.name, mime: opts.mime, dataBase64: opts.dataBase64 }).then((r) => r.uri),
    /** Share out via the system sheet. { text?, url?, dataBase64?, name?, mime? } */
    share: (opts) => call('Files', 'share', opts),
    /** Receive inbound shares. handler({ event:'shareIn', text?, items:[…] }). Returns unsubscribe. */
    onShareIn: (handler) => subscribeActivate('Files', 'subscribe', handler),
  },

  // ── Biometric — fingerprint / face (or device credential) presence check ─
  biometric: {
    get available() { return available('Biometric') },
    /** { ok, status, canAuthenticate } — is biometric enrolled/usable? */
    check: () => call('Biometric', 'available'),
    /** Prompt the system biometric sheet → true on success. { title?, subtitle?, allowDeviceCredential? } */
    authenticate: (opts = {}) => call('Biometric', 'authenticate', opts).then((r) => !!r.ok),
  },

  // ── Launcher — opt in/out of Vulos as the phone's home screen ───────────
  launcher: {
    get available() { return available('Launcher') },
    status: () => call('Launcher', 'status'),                                 // { ok, isDefault, canRequest }
    setDefault: () => call('Launcher', 'setDefault'),
    openHomeSettings: () => call('Launcher', 'openHomeSettings'),
  },

  // ── Telephony — the box/phone GSM SIM: SMS + calls ──────────────────────
  telephony: {
    get available() { return available('Telephony') },
    perms: () => call('Telephony', 'perms'),
    sendSms: (to, text) => call('Telephony', 'sms.send', { to, text }),
    listSms: (limit) => call('Telephony', 'sms.list', { limit }).then((r) => r.messages || r.sms || []),
    dial: (number) => call('Telephony', 'call.dial', { number }),
    listCallLog: (limit) => call('Telephony', 'calllog.list', { limit }).then((r) => r.calls || r.log || []),
    /** Receive inbound SMS. handler({ event:'sms', from, body, timestamp }). Returns unsubscribe. */
    onSms: (handler) => subscribeActivate('Telephony', 'subscribe', handler),
  },
}

export default nativeBridge
