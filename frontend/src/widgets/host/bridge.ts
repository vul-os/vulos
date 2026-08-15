// bridge.ts — the shell↔sandboxed-widget protocol.
//
// A sandboxed widget runs in `<iframe sandbox="allow-scripts">`. No
// `allow-same-origin`, ever — that single omission is the entire containment
// story, and it is the same invariant core/AppOrigins.ts exists to protect for
// app frames. Without it the frame is an OPAQUE origin: it cannot read the
// shell's DOM, cookies, localStorage, IndexedDB, service worker or session, it
// cannot navigate the top window, and `fetch` from inside it is same-origin-less
// and CSP-bound. Everything it can do, it does by asking over this channel.
//
// TRUST MODEL — two checks, both required, on the handshake:
//
//   1. FRAME IDENTITY. `event.source` must be the exact `contentWindow` of the
//      iframe we created for this instance. An object-identity comparison
//      against a handle we hold: unforgeable, and the ONLY check that
//      distinguishes our frame from any other frame on the page, because every
//      opaque frame reports the same origin string ('null').
//
//   2. ORIGIN. `event.origin` must be 'null' — the literal origin of an opaque
//      frame. A message arriving from a real origin is not our sandboxed widget.
//
// After the handshake the shell only ever writes to the MessagePort the frame
// handed over. `postMessage(…, '*')` appears nowhere in the host half. The one
// message the FRAME sends is addressed to the shell's exact origin (injected
// into the source as __VULOS_SHELL_ORIGIN__), so if the frame is ever hosted
// somewhere other than the shell the browser drops it and the bridge never
// forms.
//
// IDENTITY IS NEVER CLAIMED. The widget does not send its instance id; the host
// derives it from the frame the message arrived on and scopes every storage key,
// every host allowlist check and every setting write to it. A widget that lies
// about everything in the payload still cannot address another widget's data.

import { storageFor } from '../storage'
import { hostAllowed } from '../net'
import type { WidgetNet, WidgetPermission, WidgetSettingValue } from '../types'

export const BRIDGE_VERSION = 1

function isRecord(x: unknown): x is Record<string, unknown> {
  return typeof x === 'object' && x !== null
}

/** Everything the host needs to answer one sandboxed widget's requests. */
export interface BridgeHost {
  instanceId: string
  granted: WidgetPermission[]
  hosts: string[]
  net: WidgetNet | null
  notify: ((title: string, body?: string) => void) | null
  openApp: ((appId: string) => void) | null
  setSetting: (key: string, value: WidgetSettingValue) => void
}

export type BridgeReply = (obj: Record<string, unknown>) => void

// A sandboxed widget is untrusted code that can call in a loop. Every verb that
// costs anything is rate-limited per instance; over budget is a refusal, not a
// queue, because a queue is just a slower way to let it win.
const NOTIFY_MIN_INTERVAL_MS = 10_000
const NET_MIN_INTERVAL_MS = 2_000
const lastNotify = new Map<string, number>()
const lastNet = new Map<string, number>()

/** Test seam: drop the rate-limit state. */
export function resetBridgeLimits(): void {
  lastNotify.clear()
  lastNet.clear()
}

function allowed(host: BridgeHost, perm: WidgetPermission): boolean {
  return host.granted.includes(perm)
}

/**
 * Apply one message from a sandboxed widget.
 *
 * Exported and pure-ish so the protocol can be driven directly in tests without
 * a real iframe — the security properties above are assertions about THIS
 * function, and a test that had to spin up a frame to check them would not be
 * run often enough to matter.
 *
 * Returns true when the verb was recognised AND permitted. An unknown verb
 * returns false: the surface is closed, not extensible by the widget.
 */
export function handleWidgetMessage(host: BridgeHost, msg: unknown, reply: BridgeReply, now = Date.now()): boolean {
  if (!isRecord(msg)) return false
  if (msg.v !== BRIDGE_VERSION) return false
  const id = msg.id
  const store = storageFor(host.instanceId)

  switch (msg.type) {
    // ── storage ──────────────────────────────────────────────────────────────
    case 'widget.storage.get': {
      if (!allowed(host, 'storage')) { reply({ v: BRIDGE_VERSION, type: 'widget.reply', id, error: 'denied' }); return false }
      reply({ v: BRIDGE_VERSION, type: 'widget.reply', id, value: typeof msg.key === 'string' ? store.get(msg.key) : null })
      return true
    }
    case 'widget.storage.set': {
      if (!allowed(host, 'storage')) { reply({ v: BRIDGE_VERSION, type: 'widget.reply', id, error: 'denied' }); return false }
      const ok = typeof msg.key === 'string' && typeof msg.value === 'string' && store.set(msg.key, msg.value)
      reply({ v: BRIDGE_VERSION, type: 'widget.reply', id, ok })
      return true
    }
    case 'widget.storage.remove': {
      if (!allowed(host, 'storage')) { reply({ v: BRIDGE_VERSION, type: 'widget.reply', id, error: 'denied' }); return false }
      if (typeof msg.key === 'string') store.remove(msg.key)
      reply({ v: BRIDGE_VERSION, type: 'widget.reply', id, ok: true })
      return true
    }
    case 'widget.storage.keys': {
      if (!allowed(host, 'storage')) { reply({ v: BRIDGE_VERSION, type: 'widget.reply', id, error: 'denied' }); return false }
      reply({ v: BRIDGE_VERSION, type: 'widget.reply', id, keys: store.keys() })
      return true
    }

    // ── settings ─────────────────────────────────────────────────────────────
    // A widget may write its OWN declared settings. layout.setInstanceSetting
    // re-normalises against the manifest, so an undeclared key or a wrongly
    // typed value is dropped there rather than trusted here.
    case 'widget.setting.set': {
      if (typeof msg.key !== 'string') return false
      const v = msg.value
      if (typeof v !== 'string' && typeof v !== 'number' && typeof v !== 'boolean' && !Array.isArray(v)) return false
      host.setSetting(msg.key, v as WidgetSettingValue)
      reply({ v: BRIDGE_VERSION, type: 'widget.reply', id, ok: true })
      return true
    }

    // ── network ──────────────────────────────────────────────────────────────
    case 'widget.net.get': {
      if (!allowed(host, 'network') || !host.net) {
        reply({ v: BRIDGE_VERSION, type: 'widget.reply', id, error: 'denied' })
        return false
      }
      const url = msg.url
      if (typeof url !== 'string') { reply({ v: BRIDGE_VERSION, type: 'widget.reply', id, error: 'bad-url' }); return false }
      // Checked here as well as inside widgetNet and again on the box. The frame
      // is the least trustworthy of the three, so it gets the earliest refusal.
      if (!hostAllowed(url, host.hosts)) {
        reply({ v: BRIDGE_VERSION, type: 'widget.reply', id, error: 'blocked-host' })
        return false
      }
      // `undefined` means NEVER CALLED, and that is not the same as "called at
      // timestamp 0". Defaulting to 0 made the FIRST request refusable whenever
      // `now` happened to be small — invisible in production (Date.now() is
      // ~1.7e12) and immediately visible to a test that passes an explicit
      // clock, which is how it was found.
      const prev = lastNet.get(host.instanceId)
      if (prev !== undefined && now - prev < NET_MIN_INTERVAL_MS) {
        reply({ v: BRIDGE_VERSION, type: 'widget.reply', id, error: 'rate-limited' })
        return false
      }
      lastNet.set(host.instanceId, now)
      host.net.getJSON(url).then(
        (r) => reply({ v: BRIDGE_VERSION, type: 'widget.reply', id, result: r }),
        () => reply({ v: BRIDGE_VERSION, type: 'widget.reply', id, error: 'offline' }),
      )
      return true
    }

    // ── notify ───────────────────────────────────────────────────────────────
    case 'widget.notify': {
      if (!allowed(host, 'notify') || !host.notify) {
        reply({ v: BRIDGE_VERSION, type: 'widget.reply', id, error: 'denied' })
        return false
      }
      const prev = lastNotify.get(host.instanceId)
      if (prev !== undefined && now - prev < NOTIFY_MIN_INTERVAL_MS) {
        reply({ v: BRIDGE_VERSION, type: 'widget.reply', id, error: 'rate-limited' })
        return false
      }
      lastNotify.set(host.instanceId, now)
      const title = typeof msg.title === 'string' ? msg.title.slice(0, 120) : ''
      const body = typeof msg.body === 'string' ? msg.body.slice(0, 400) : undefined
      if (!title) { reply({ v: BRIDGE_VERSION, type: 'widget.reply', id, error: 'bad-title' }); return false }
      host.notify(title, body)
      reply({ v: BRIDGE_VERSION, type: 'widget.reply', id, ok: true })
      return true
    }

    // ── launch ───────────────────────────────────────────────────────────────
    // An app ID, not a URL. A widget cannot navigate the desktop anywhere; it
    // can only name something the user already has installed, and the shell
    // decides whether that name resolves.
    case 'widget.openApp': {
      if (!allowed(host, 'launch') || !host.openApp) {
        reply({ v: BRIDGE_VERSION, type: 'widget.reply', id, error: 'denied' })
        return false
      }
      if (typeof msg.appId !== 'string' || !/^[a-z0-9][a-z0-9-]*$/.test(msg.appId)) {
        reply({ v: BRIDGE_VERSION, type: 'widget.reply', id, error: 'bad-app' })
        return false
      }
      host.openApp(msg.appId)
      reply({ v: BRIDGE_VERSION, type: 'widget.reply', id, ok: true })
      return true
    }

    default:
      return false
  }
}

/**
 * The client half, injected into every sandboxed widget's document.
 *
 * Shipped by the host rather than written by the widget author so that the
 * handshake is done correctly every time — in particular so the `hello` is
 * addressed to the shell's exact origin instead of '*', which no widget author
 * would get right or would bother to keep right.
 */
export function bridgeClientScript(shellOrigin: string): string {
  const origin = JSON.stringify(shellOrigin)
  return `
(function () {
  var V = ${BRIDGE_VERSION};
  var ch = new MessageChannel();
  var seq = 0;
  var pending = {};
  var listeners = [];
  var ctx = { size: 'small', now: new Date(), settings: {}, reducedMotion: false, telemetry: null, calendar: null, notifications: null };

  ch.port1.onmessage = function (e) {
    var m = e.data;
    if (!m || m.v !== V) return;
    if (m.type === 'widget.reply') {
      var fn = pending[m.id];
      if (fn) { delete pending[m.id]; fn(m); }
      return;
    }
    if (m.type === 'widget.context') {
      ctx = {
        size: m.size, now: new Date(m.now), settings: m.settings || {},
        reducedMotion: !!m.reducedMotion, telemetry: m.telemetry || null, calendar: m.calendar || null,
        notifications: m.notifications || null
      };
      for (var i = 0; i < listeners.length; i++) { try { listeners[i](ctx); } catch (err) { /* widget bug */ } }
    }
  };

  function call(type, payload) {
    return new Promise(function (resolve) {
      var id = ++seq;
      pending[id] = resolve;
      var msg = { v: V, type: type, id: id };
      for (var k in payload) msg[k] = payload[k];
      ch.port1.postMessage(msg);
    });
  }

  window.vulosWidget = {
    context: function () { return ctx; },
    onContext: function (fn) { listeners.push(fn); if (ctx) { try { fn(ctx); } catch (e) {} } },
    storage: {
      get: function (k) { return call('widget.storage.get', { key: k }).then(function (r) { return r.value; }); },
      set: function (k, v) { return call('widget.storage.set', { key: k, value: v }).then(function (r) { return !!r.ok; }); },
      remove: function (k) { return call('widget.storage.remove', { key: k }); },
      keys: function () { return call('widget.storage.keys', {}).then(function (r) { return r.keys || []; }); }
    },
    setSetting: function (k, v) { return call('widget.setting.set', { key: k, value: v }); },
    getJSON: function (url) { return call('widget.net.get', { url: url }).then(function (r) { return r.result || { ok: false, status: 0, data: null, error: r.error }; }); },
    notify: function (t, b) { return call('widget.notify', { title: t, body: b }); },
    openApp: function (a) { return call('widget.openApp', { appId: a }); }
  };

  parent.postMessage({ v: V, type: 'vulos.widget.hello' }, ${origin}, [ch.port2]);
})();
`.trim()
}
