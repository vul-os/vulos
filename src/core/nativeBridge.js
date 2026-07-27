// nativeBridge — a promise-based SDK over the Vulos Android app's origin-gated
// native bridges (window.vulosContacts / vulosCamera / vulosNotify / vulosFiles
// / vulosBiometric / vulosLauncher / vulosTelephony).
//
// The Android shell registers each bridge with WebViewCompat.addWebMessageListener
// under a `window.vulosX` object (see mobile/app/.../MainActivity.kt +
// BridgeBase.kt). The wire protocol is:
//   JS→native : vulosX.postMessage(JSON.stringify({ id, action, ...args }))
//   native→JS : vulosX.onmessage → { id, ok, ... } | { id, error }
//   push      : vulosX.onmessage → { event, ... }   (no id)
//
// In a plain browser / PWA these globals don't exist, so every method degrades
// honestly: `available` is false and calls reject with 'native-unavailable'.
// Callers should feature-detect (`nativeBridge.contacts.available`) and fall
// back to the web path. This module NEVER throws at import time.

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

function available(name) { return !!chan(name) }

// call — send one action and await its reply (rejects on error/timeout).
function call(name, action, args = {}, timeoutMs = 20_000) {
  const c = chan(name)
  if (!c) return Promise.reject(new Error('native-unavailable'))
  const id = 'nb' + (++seq)
  return new Promise((resolve, reject) => {
    c.pending.set(id, { resolve, reject })
    try { c.port.postMessage(JSON.stringify({ id, action, ...args })) }
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

export const nativeBridge = {
  /** True only inside the Vulos Android app (at least one bridge present). */
  get inApp() { return NAMES.some(available) },
  call,
  subscribe,

  // Contacts — device + SIM address book (verified against ContactsBridge.kt).
  contacts: {
    get available() { return available('Contacts') },
    /** { ok, readContacts } */
    perms: () => call('Contacts', 'perms'),
    /** device contacts [{ name, phones[], emails[], org? }] (asks READ_CONTACTS) */
    list: (limit) => call('Contacts', 'list', limit ? { limit } : {}).then((r) => r.contacts || []),
    /** SIM contacts [{ name, phones[], emails[], org? }] */
    sim: () => call('Contacts', 'sim').then((r) => r.contacts || []),
  },

  // The remaining bridges expose the generic `call(name, action, args)` until
  // each one's action set is wired to a specific consumer. `available` lets the
  // UI decide whether to offer the native path.
  camera: { get available() { return available('Camera') } },
  notify: { get available() { return available('Notify') } },
  files: { get available() { return available('Files') } },
  biometric: { get available() { return available('Biometric') } },
  launcher: { get available() { return available('Launcher') } },
  telephony: { get available() { return available('Telephony') } },
}

export default nativeBridge
