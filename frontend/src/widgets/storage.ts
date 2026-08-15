// storage.ts — per-widget-instance key/value storage.
//
// Modelled directly on core/AppBridge.ts's app namespacing, for the same reason
// and with the same shape: the shell's own settings live in this localStorage,
// so an unbounded widget store is a way to brick the desktop for everything
// else, and an un-namespaced one is a way to read another widget's data.
//
// Scoped to the INSTANCE, not the widget id. Two world clocks in the rail are
// two separate stores; removing one cannot disturb the other, and the layout's
// "remove widget" can drop exactly the removed instance's keys.

const PREFIX = 'vulos.widget.'
const SEP = '::'

const MAX_KEY_LEN = 128
const MAX_VALUE_LEN = 16 * 1024
const MAX_INSTANCE_BYTES = 64 * 1024

// Instance ids are minted by layout.ts, but this module refuses to trust that:
// it is also reached from the sandbox bridge, where the identity comes off a
// message. A malformed id must not be able to escape its prefix.
const ID_RE = /^[a-z0-9][a-z0-9-]{0,63}$/

function validId(id: unknown): id is string {
  return typeof id === 'string' && ID_RE.test(id)
}

/**
 * The backing store, or null when there isn't a usable one.
 *
 * The truthiness check this replaces was not enough. `globalThis.localStorage`
 * can EXIST and not be a Storage: Node's experimental localStorage shim, a
 * hardened browser profile, and this repo's own jsdom test environment all
 * present an object that has none of the methods. Returning it because it was
 * truthy meant the first `ls.getItem(...)` threw a TypeError — from a render
 * path, on the desktop, which is a white screen.
 *
 * So the probe is for the METHODS this module actually calls, not for the
 * property's presence. A store that cannot answer is the same as no store, and
 * every caller already handles no store.
 */
function store(): Storage | null {
  try {
    const ls = globalThis.localStorage
    if (!ls) return null
    if (typeof ls.getItem !== 'function' || typeof ls.setItem !== 'function') return null
    if (typeof ls.removeItem !== 'function' || typeof ls.key !== 'function') return null
    if (typeof ls.length !== 'number') return null
    return ls
  } catch {
    return null
  }
}

function nsPrefix(instanceId: string): string {
  return `${PREFIX}${instanceId}${SEP}`
}

export function widgetKeys(instanceId: unknown): string[] {
  const ls = store()
  if (!ls || !validId(instanceId)) return []
  const p = nsPrefix(instanceId)
  const out: string[] = []
  try {
    for (let i = 0; i < ls.length; i++) {
      const k = ls.key(i)
      if (typeof k === 'string' && k.startsWith(p)) out.push(k.slice(p.length))
    }
  } catch { /* storage unavailable — an empty list is a truthful answer */ }
  return out
}

function usedBytes(instanceId: string): number {
  const ls = store()
  if (!ls) return 0
  let n = 0
  for (const k of widgetKeys(instanceId)) {
    let v: string | null = null
    try { v = ls.getItem(nsPrefix(instanceId) + k) } catch { /* ignore */ }
    n += k.length + (v ? v.length : 0)
  }
  return n
}

export function widgetGet(instanceId: unknown, key: unknown): string | null {
  const ls = store()
  if (!ls || !validId(instanceId) || typeof key !== 'string' || key === '') return null
  try {
    return ls.getItem(nsPrefix(instanceId) + key)
  } catch {
    return null
  }
}

/**
 * Returns false when refused. A refusal is a plain false rather than a throw:
 * this is called from a render path, and a widget that hits its quota must show
 * a degraded state, not take the desktop down with an exception.
 */
export function widgetSet(instanceId: unknown, key: unknown, value: unknown): boolean {
  const ls = store()
  if (!ls || !validId(instanceId)) return false
  if (typeof key !== 'string' || typeof value !== 'string') return false
  if (key.length === 0 || key.length > MAX_KEY_LEN) return false
  if (value.length > MAX_VALUE_LEN) return false

  const full = nsPrefix(instanceId) + key
  let existing = 0
  try {
    const prev = ls.getItem(full)
    if (typeof prev === 'string') existing = key.length + prev.length
  } catch { /* ignore */ }
  if (usedBytes(instanceId) - existing + key.length + value.length > MAX_INSTANCE_BYTES) return false

  try {
    ls.setItem(full, value)
    return true
  } catch {
    return false
  }
}

export function widgetRemove(instanceId: unknown, key: unknown): void {
  const ls = store()
  if (!ls || !validId(instanceId) || typeof key !== 'string') return
  try { ls.removeItem(nsPrefix(instanceId) + key) } catch { /* ignore */ }
}

/** Drop every key an instance owns. Called when the user removes it from the rail. */
export function widgetClear(instanceId: unknown): void {
  const ls = store()
  if (!ls || !validId(instanceId)) return
  const p = nsPrefix(instanceId)
  try {
    const doomed: string[] = []
    for (let i = 0; i < ls.length; i++) {
      const k = ls.key(i)
      if (typeof k === 'string' && k.startsWith(p)) doomed.push(k)
    }
    for (const k of doomed) ls.removeItem(k)
  } catch { /* ignore */ }
}

/** The WidgetStorage handed to a widget that was granted `storage`. */
export function storageFor(instanceId: string) {
  return {
    get: (key: string) => widgetGet(instanceId, key),
    set: (key: string, value: string) => widgetSet(instanceId, key, value),
    remove: (key: string) => widgetRemove(instanceId, key),
    keys: () => widgetKeys(instanceId),
  }
}

export const STORAGE_LIMITS = { MAX_KEY_LEN, MAX_VALUE_LEN, MAX_INSTANCE_BYTES }
