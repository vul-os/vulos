import { describe, it, expect, beforeEach, vi } from 'vitest'
import {
  BRIDGE_VERSION,
  attachAppBridge,
  handleBridgeMessage,
  readAppSnapshot,
  writeAppItem,
  clearAppData,
  clearAllAppData,
  encodeSeed,
  appFrameSrc,
} from '../core/AppBridge'
import { holdMasterKey, clearMasterKey } from '../lib/masterKey.js'

// ORIGIN-01 — the shell side of the shell↔app bridge.
//
// The bridge is the ONLY thing an app frame can say to the shell. Everything here
// is about making sure it is the only thing, and that only the right frame can
// say it.

const APP_ORIGIN = 'http://clock--default.box.example.com'
const APP_URL = `${APP_ORIGIN}/`

// Dispatches a window 'message' event with the fields the bridge inspects.
// jsdom's MessageEvent honours `source` and `origin` from the init dict.
//
// `source` and `ports` are attached via defineProperty (rather than passed to
// the MessageEvent constructor) because the DOM lib types those fields as the
// REAL `MessageEventSource`/`MessagePort[]` — this suite deliberately uses
// lightweight fakes (fakeFrame(), a plain postMessage/close/onmessage object)
// that only jsdom's runtime laxity accepts, not TS's structural check.
// defineProperty's PropertyDescriptor.value is `any` in the DOM lib itself
// (the same escape hatch used for the window.addEventListener stub in
// endpoints.test.ts), so this stays honest without a project-introduced cast.
function postToShell({ source, origin, data, ports = [] }: {
  source: unknown
  origin: string
  data: unknown
  ports?: unknown[]
}) {
  const event = new MessageEvent('message', { origin, data })
  Object.defineProperty(event, 'source', { value: source })
  Object.defineProperty(event, 'ports', { value: ports })
  window.dispatchEvent(event)
}

function helloFrom(port?: unknown) {
  return { type: 'vulos.bridge.hello', v: BRIDGE_VERSION, ...(port ? {} : {}) }
}

// A stand-in for an iframe: the bridge only ever touches `.contentWindow`.
function fakeFrame() {
  return { contentWindow: { name: 'app-frame' } }
}

interface FakePort {
  postMessage: ReturnType<typeof vi.fn>
  close: ReturnType<typeof vi.fn>
  onmessage: ((ev: { data: unknown }) => void) | null
}

function callPortMessage(port: FakePort, data: unknown): void {
  if (!port.onmessage) throw new Error('expected port.onmessage to have been set by attachAppBridge')
  port.onmessage({ data })
}

describe('storage namespacing — an app cannot name anything but its own data', () => {
  beforeEach(() => localStorage.clear())

  it('scopes every key under the app id, derived from the FRAME not the message', () => {
    writeAppItem('clock', 'clock.worldClocks', '["utc"]')
    // The raw shell key is namespaced...
    expect(localStorage.getItem('vulos.appdata.clock::clock.worldClocks')).toBe('["utc"]')
    // ...and the app sees only its own unprefixed view.
    expect(readAppSnapshot('clock')).toEqual({ 'clock.worldClocks': '["utc"]' })
  })

  it('cannot read or clobber another app\'s data', () => {
    writeAppItem('clock', 'secret', 'clock-value')
    writeAppItem('weather', 'secret', 'weather-value')

    expect(readAppSnapshot('clock')).toEqual({ secret: 'clock-value' })
    expect(readAppSnapshot('weather')).toEqual({ secret: 'weather-value' })

    clearAppData('clock')
    expect(readAppSnapshot('clock')).toEqual({})
    expect(readAppSnapshot('weather')).toEqual({ secret: 'weather-value' }) // untouched
  })

  it('cannot reach the shell\'s own localStorage keys', () => {
    localStorage.setItem('vulos_session_hint', 'SHELL-SECRET')
    localStorage.setItem('theme', 'dark')

    // The app's snapshot contains ONLY its namespace — the shell's keys are invisible.
    writeAppItem('clock', 'x', '1')
    const snap = readAppSnapshot('clock')
    expect(snap).toEqual({ x: '1' })
    expect(Object.values(snap)).not.toContain('SHELL-SECRET')

    // And an app cannot escape its namespace by crafting a key: the shell always
    // prepends its prefix, so the key just becomes a (harmless) namespaced key.
    handleBridgeMessage('clock', {
      v: BRIDGE_VERSION, type: 'vulos.storage.set', key: '../../theme', value: 'PWNED',
    })
    expect(localStorage.getItem('theme')).toBe('dark') // shell key untouched
    expect(localStorage.getItem('vulos.appdata.clock::../../theme')).toBe('PWNED')
  })

  it('handles hostile key names without prototype confusion', () => {
    // "__proto__" on a plain {} would hit the prototype setter instead of being
    // stored; "constructor"/"toString" would collide with inherited members. The
    // snapshot is a null-prototype map, so these are ordinary keys.
    for (const k of ['__proto__', 'constructor', 'toString']) {
      expect(writeAppItem('clock', k, `v-${k}`)).toBe(true)
    }
    const snap = readAppSnapshot('clock')
    expect(snap.__proto__).toBe('v-__proto__')
    expect(snap.constructor).toBe('v-constructor')
    expect(snap.toString).toBe('v-toString')
    // Nothing was polluted: a fresh object is unaffected.
    const freshObj: Record<string, unknown> = {}
    expect(freshObj.polluted).toBeUndefined()
    expect(Object.prototype.toString).toBeTypeOf('function')
  })

  it('clear() only clears the calling app', () => {
    localStorage.setItem('theme', 'dark')
    writeAppItem('clock', 'a', '1')
    handleBridgeMessage('clock', { v: BRIDGE_VERSION, type: 'vulos.storage.clear' })
    expect(readAppSnapshot('clock')).toEqual({})
    expect(localStorage.getItem('theme')).toBe('dark')
  })

  // OFFLINE-AUTH-01: the wipe path clears EVERY app's data but never the shell's.
  it('clearAllAppData drops all app namespaces and leaves shell keys untouched', () => {
    localStorage.setItem('theme', 'dark')             // shell-owned key
    localStorage.setItem('vulos_session_hint', 'S')   // shell-owned key
    writeAppItem('clock', 'a', '1')
    writeAppItem('weather', 'b', '2')

    expect(clearAllAppData()).toBe(true)

    expect(readAppSnapshot('clock')).toEqual({})
    expect(readAppSnapshot('weather')).toEqual({})
    // Shell keys survive — a wipe must not brick the desktop.
    expect(localStorage.getItem('theme')).toBe('dark')
    expect(localStorage.getItem('vulos_session_hint')).toBe('S')
  })
})

describe('the capability surface is closed', () => {
  beforeEach(() => localStorage.clear())

  it('accepts exactly the known verbs — and nothing else', () => {
    expect(handleBridgeMessage('clock', { v: BRIDGE_VERSION, type: 'vulos.storage.set', key: 'k', value: 'v' })).toBe(true)
    expect(handleBridgeMessage('clock', { v: BRIDGE_VERSION, type: 'vulos.storage.remove', key: 'k' })).toBe(true)
    expect(handleBridgeMessage('clock', { v: BRIDGE_VERSION, type: 'vulos.storage.clear' })).toBe(true)
    // OFFLINE-AUTH-01 request/response verbs (reply is a no-op callback here).
    expect(handleBridgeMessage('clock', { v: BRIDGE_VERSION, type: 'vulos.offline.state', id: 1 }, () => {})).toBe(true)
    expect(handleBridgeMessage('clock', { v: BRIDGE_VERSION, type: 'vulos.offline.appKey', id: 1 }, () => {})).toBe(true)

    // Anything an attacker might wish for.
    for (const type of [
      'vulos.storage.readShell', 'vulos.exec', 'vulos.navigate', 'vulos.fetch',
      'vulos.storage.snapshot', 'vulos.offline.wipe', 'eval', '__proto__',
    ]) {
      expect(handleBridgeMessage('clock', { v: BRIDGE_VERSION, type, key: 'k', value: 'v' }), type).toBe(false)
    }
  })

  it('offline verbs never let an app name another app — the key is bound to the trusted appId', async () => {
    // (full assertions live in the dedicated describe below; here we just confirm
    //  they are part of the "closed but known" surface, not app-nameable.)
    const reply = vi.fn()
    // An app cannot pass an appId in the message; only the frame-derived id is used.
    handleBridgeMessage('clock', { v: BRIDGE_VERSION, type: 'vulos.offline.state', id: 1, appId: 'mail' }, reply)
    expect(reply).toHaveBeenCalledWith(expect.objectContaining({ type: 'vulos.offline.state.reply' }))
  })

  it('rejects a mismatched protocol version', () => {
    expect(handleBridgeMessage('clock', { v: 999, type: 'vulos.storage.set', key: 'k', value: 'v' })).toBe(false)
    expect(handleBridgeMessage('clock', { type: 'vulos.storage.set', key: 'k', value: 'v' })).toBe(false)
    expect(readAppSnapshot('clock')).toEqual({})
  })

  it('rejects non-string keys/values and enforces a per-app quota', () => {
    expect(writeAppItem('clock', 'k', { evil: true })).toBe(false)
    expect(writeAppItem('clock', 'k', null)).toBe(false)
    expect(writeAppItem('clock', 'x'.repeat(600), 'v')).toBe(false)      // key too long
    expect(writeAppItem('clock', 'k', 'v'.repeat(70 * 1024))).toBe(false) // value too long

    // Quota: an app must not be able to fill the shell's localStorage and brick
    // the desktop for everything else.
    const chunk = 'v'.repeat(60 * 1024)
    let accepted = 0
    for (let i = 0; i < 10; i++) if (writeAppItem('clock', `k${i}`, chunk)) accepted++
    expect(accepted).toBeGreaterThan(0)
    expect(accepted).toBeLessThan(10) // refused before it could grow without bound
  })
})

describe('attachAppBridge — origin and frame identity are BOTH required', () => {
  let frame: ReturnType<typeof fakeFrame>
  let detach: (() => void) | null
  let port: FakePort

  beforeEach(() => {
    localStorage.clear()
    frame = fakeFrame()
    port = { postMessage: vi.fn(), close: vi.fn(), onmessage: null }
    detach = attachAppBridge(frame, { appId: 'clock', frameUrl: APP_URL })
  })

  const complete = () => { if (detach) detach() }

  it('accepts a hello from the right frame at the right origin', () => {
    postToShell({ source: frame.contentWindow, origin: APP_ORIGIN, data: helloFrom(), ports: [port] })
    expect(port.postMessage).toHaveBeenCalledOnce()
    const sent = port.postMessage.mock.calls[0][0]
    expect(sent.type).toBe('vulos.storage.snapshot')
    expect(sent.v).toBe(BRIDGE_VERSION)
    complete()
  })

  it('REJECTS a hello from an unexpected ORIGIN, even from the right frame', () => {
    // A hostile page that somehow reached this frame handle, or the app frame after
    // being navigated to an attacker origin.
    for (const origin of ['http://evil.com', 'null', 'http://localhost:3000', `${APP_ORIGIN}.evil.com`]) {
      postToShell({ source: frame.contentWindow, origin, data: helloFrom(), ports: [port] })
    }
    expect(port.postMessage).not.toHaveBeenCalled()
    complete()
  })

  it('REJECTS a hello from an unexpected FRAME, even at the right origin', () => {
    // Another iframe on the page (say, a second app) impersonating this one. The
    // origin check alone would not catch this when both frames are opaque.
    const otherFrame = fakeFrame()
    postToShell({ source: otherFrame.contentWindow, origin: APP_ORIGIN, data: helloFrom(), ports: [port] })
    expect(port.postMessage).not.toHaveBeenCalled()
    complete()
  })

  it('ignores a hello with no MessagePort, and non-hello chatter', () => {
    postToShell({ source: frame.contentWindow, origin: APP_ORIGIN, data: helloFrom(), ports: [] })
    postToShell({ source: frame.contentWindow, origin: APP_ORIGIN, data: { type: 'vulos.storage.set', key: 'k', value: 'v', v: BRIDGE_VERSION }, ports: [port] })
    postToShell({ source: frame.contentWindow, origin: APP_ORIGIN, data: 'hello', ports: [port] })
    expect(port.postMessage).not.toHaveBeenCalled()
    // The window-level channel carries NO storage verbs — only the port does.
    expect(readAppSnapshot('clock')).toEqual({})
    complete()
  })

  it('applies storage writes that arrive on the PORT', () => {
    postToShell({ source: frame.contentWindow, origin: APP_ORIGIN, data: helloFrom(), ports: [port] })
    expect(port.onmessage).toBeTypeOf('function')

    callPortMessage(port, { v: BRIDGE_VERSION, type: 'vulos.storage.set', key: 'te-theme', value: 'dark' })
    expect(readAppSnapshot('clock')).toEqual({ 'te-theme': 'dark' })

    callPortMessage(port, { v: BRIDGE_VERSION, type: 'vulos.storage.remove', key: 'te-theme' })
    expect(readAppSnapshot('clock')).toEqual({})
    complete()
  })

  it('seeds the snapshot of THIS app only', () => {
    writeAppItem('clock', 'clock.worldClocks', '["utc"]')
    writeAppItem('weather', 'weather.unit', 'C')

    postToShell({ source: frame.contentWindow, origin: APP_ORIGIN, data: helloFrom(), ports: [port] })
    const sent = port.postMessage.mock.calls[0][0]
    expect(sent.data).toEqual({ 'clock.worldClocks': '["utc"]' })
    expect(sent.data).not.toHaveProperty('weather.unit')
    complete()
  })

  it('stops listening after detach', () => {
    if (!detach) throw new Error('expected beforeEach to have set detach')
    detach()
    detach = null
    postToShell({ source: frame.contentWindow, origin: APP_ORIGIN, data: helloFrom(), ports: [port] })
    expect(port.postMessage).not.toHaveBeenCalled()
  })
})

describe('attachAppBridge on an OPAQUE frame (the path-prefix fallback)', () => {
  it('expects origin "null" and still requires the right frame', () => {
    localStorage.clear()
    const frame = fakeFrame()
    const port = { postMessage: vi.fn(), close: vi.fn(), onmessage: null }
    // A shell-origin URL → the frame is sandboxed opaque → event.origin is 'null'.
    const detach = attachAppBridge(frame, { appId: 'clock', frameUrl: '/app/clock/' })

    // Right frame, opaque origin → accepted.
    postToShell({ source: frame.contentWindow, origin: 'null', data: helloFrom(), ports: [port] })
    expect(port.postMessage).toHaveBeenCalledOnce()

    // A DIFFERENT opaque frame (e.g. another app, also origin 'null') → rejected.
    // Origin alone cannot distinguish these; frame identity is what saves us.
    const other = fakeFrame()
    const port2 = { postMessage: vi.fn(), close: vi.fn(), onmessage: null }
    postToShell({ source: other.contentWindow, origin: 'null', data: helloFrom(), ports: [port2] })
    expect(port2.postMessage).not.toHaveBeenCalled()

    detach()
  })
})

describe('seed encoding', () => {
  beforeEach(() => localStorage.clear())

  it('round-trips through the URL fragment', () => {
    const snap = { 'weather.unit': 'C', 'weather.location': '{"lat":1,"lon":2}' }
    const encoded = encodeSeed(snap)
    expect(encoded).toBeTruthy()
    // Decode the way the injected client does.
    const bin = atob(decodeURIComponent(encoded))
    const bytes = Uint8Array.from(bin, (c) => c.charCodeAt(0))
    expect(JSON.parse(new TextDecoder().decode(bytes))).toEqual(snap)
  })

  it('survives unicode values', () => {
    const snap = { k: '☀ 25°C — Zürich' }
    const bin = atob(decodeURIComponent(encodeSeed(snap)))
    const bytes = Uint8Array.from(bin, (c) => c.charCodeAt(0))
    expect(JSON.parse(new TextDecoder().decode(bytes))).toEqual(snap)
  })

  it('declines to seed an oversized snapshot (it arrives over the port instead)', () => {
    expect(encodeSeed({ big: 'x'.repeat(64 * 1024) })).toBe('')
    expect(encodeSeed({})).toBe('')
  })

  it('appends the seed in the FRAGMENT, so it never reaches the server', () => {
    writeAppItem('clock', 'a', '1')
    const src = appFrameSrc('/app/clock/', 'clock')
    expect(src).toContain('#__vulos_s=')
    expect(src.split('#')[0]).toBe('/app/clock/') // nothing added to path or query
  })
})

describe('offline-auth bridge verbs (OFFLINE-AUTH-01)', () => {
  beforeEach(() => clearMasterKey())
  afterEach(() => clearMasterKey())

  it('offline.state reports locked when the OS holds no offline key', () => {
    const reply = vi.fn()
    handleBridgeMessage('notes', { v: BRIDGE_VERSION, type: 'vulos.offline.state', id: 7 }, reply)
    expect(reply).toHaveBeenCalledWith(
      expect.objectContaining({ type: 'vulos.offline.state.reply', id: 7, unlocked: false }),
    )
  })

  it('offline.state reports unlocked once a key is held', () => {
    holdMasterKey(new Uint8Array(32))
    const reply = vi.fn()
    handleBridgeMessage('notes', { v: BRIDGE_VERSION, type: 'vulos.offline.state', id: 3 }, reply)
    expect(reply).toHaveBeenCalledWith(expect.objectContaining({ unlocked: true }))
  })

  it('offline.appKey returns null when locked (fail-closed)', async () => {
    const reply = vi.fn()
    handleBridgeMessage('notes', { v: BRIDGE_VERSION, type: 'vulos.offline.appKey', id: 2 }, reply)
    await new Promise((r) => setTimeout(r, 0))
    expect(reply).toHaveBeenCalledWith(
      expect.objectContaining({ type: 'vulos.offline.appKey.reply', id: 2, key: null }),
    )
  })

  it('offline.appKey returns THIS app’s non-extractable key, bound to the trusted appId', async () => {
    holdMasterKey(crypto.getRandomValues(new Uint8Array(32)))
    const replyNotes = vi.fn()
    const replyMail = vi.fn()
    handleBridgeMessage('notes', { v: BRIDGE_VERSION, type: 'vulos.offline.appKey', id: 1 }, replyNotes)
    handleBridgeMessage('mail', { v: BRIDGE_VERSION, type: 'vulos.offline.appKey', id: 1 }, replyMail)
    for (let i = 0; i < 100 && !(replyNotes.mock.calls.length && replyMail.mock.calls.length); i++) {
      await new Promise((r) => setTimeout(r, 1))
    }

    const notesKey = replyNotes.mock.calls[0][0].key
    const mailKey = replyMail.mock.calls[0][0].key
    expect(notesKey.extractable).toBe(false)

    // The two apps get DIFFERENT keys — an app can only ever obtain its own.
    const iv = new Uint8Array(12)
    const pt = new TextEncoder().encode('secret')
    const cN = new Uint8Array(await crypto.subtle.encrypt({ name: 'AES-GCM', iv }, notesKey, pt))
    const cM = new Uint8Array(await crypto.subtle.encrypt({ name: 'AES-GCM', iv }, mailKey, pt))
    expect(Array.from(cN)).not.toEqual(Array.from(cM))
  })
})
