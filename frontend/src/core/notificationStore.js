// notificationStore.js — the shell-level notification seam (Wave 13).
//
// A tiny, framework-agnostic pub/sub store that any app or subsystem can post to
// via notify() — mirroring the commandRegistry.js "Actions" seam behind ⌘K. The
// shell chrome (the bell + Notification Center) and the transient Toaster both
// subscribe to it, so there is ONE unified notification feed made of:
//   • client-posted notifications  (any surface calling notify({...}))
//   • bridged backend/system ones  (see notificationBridge.js → ingest())
//
// The store is deliberately NOT React: pure functions + a couple of listener
// sets, so it can be imported from event handlers, notifiers, tests, etc. All
// browser access (localStorage) is guarded so it also runs under jsdom / SSR.
//
// A notification is a plain object:
//   {
//     id:      string    unique; re-posting the same id updates in place
//     title:   string    primary label
//     body?:   string    secondary detail
//     source:  string    'system' | 'mail' | 'assistant' | app id — groups + filters
//     level:   'info' | 'warning' | 'urgent' | 'critical'
//     actions?: Array<{ id, label, run?, route?: { app, query }, url? }>
//     ttl?:    number     toast auto-dismiss seconds (0/absent → level default)
//     read:    boolean
//     ts:      number     epoch ms
//     origin:  'local' | 'remote'   local = client notify(); remote = bridged
//   }

import { nativeBridge } from './nativeBridge'

// ── constants ────────────────────────────────────────────────────────────────
export const LEVELS = ['info', 'warning', 'urgent', 'critical']
const LEVEL_SET = new Set(LEVELS)
const MAX_ITEMS = 100
const MAX_TOASTS = 4
const RATE_MS = 4000 // per-source min gap between toasts — collapses bursts (quiet)
const LS_ITEMS = 'vulos.notifications.log.v1'
const LS_PREFS = 'vulos.notifications.prefs.v1'

// Toast auto-dismiss defaults (seconds). urgent/critical are sticky (0).
const TTL_DEFAULT = { info: 6, warning: 8, urgent: 0, critical: 0 }

const DEFAULT_PREFS = Object.freeze({
  muted: false, // global Do Not Disturb — suppresses toasts (items still logged)
  sound: true, // play a chime on new toasts
  sources: {}, // { [source]: false } → that source is fully off (no log, no toast)
})

// ── module state ─────────────────────────────────────────────────────────────
let items = loadItems() // newest first
let toasts = [] // transient toast queue: { key, notif }
let prefs = loadPrefs()
const lastToastAt = new Map() // source → epoch ms of last toast (rate limit)

const itemListeners = new Set()
const toastListeners = new Set()
const prefsListeners = new Set()

// The bridge registers a sink so that mutating a remote (backend) item proxies
// the mutation to the server. Local items are handled entirely client-side.
let remoteSink = null

// ── pure helpers (exported for tests) ────────────────────────────────────────

/** Normalise arbitrary notify() input into a well-formed notification. */
export function normalize(input, now = Date.now()) {
  const src = String(input.source || input.app || 'system')
  let body = input.body
  if (body && typeof body === 'object') body = body.detail || body.title || ''
  const level = LEVEL_SET.has(input.level) ? input.level : 'info'
  return {
    id: input.id || `n_${now}_${Math.random().toString(36).slice(2, 8)}`,
    title: String(input.title || '').slice(0, 200),
    body: body != null ? String(body).slice(0, 500) : '',
    source: src,
    level,
    actions: Array.isArray(input.actions) ? input.actions.slice(0, 3) : [],
    ttl: typeof input.ttl === 'number' ? input.ttl : (TTL_DEFAULT[level] ?? 6),
    read: !!input.read,
    ts: input.ts || now,
    origin: input.origin === 'remote' ? 'remote' : 'local',
  }
}

/**
 * Should this notification be LOGGED at all? A source the user switched off is
 * dropped entirely (no badge, no history) — the strongest "I don't want this".
 */
export function shouldLog(p, source) {
  return p.sources?.[source] !== false
}

/**
 * Should this notification raise a transient TOAST? Honours the source switch,
 * global mute (DND) — but 'critical' always breaks through mute, matching OS
 * conventions for genuinely can't-miss alerts.
 */
export function shouldToast(p, source, level) {
  if (!shouldLog(p, source)) return false
  if (level === 'critical') return true
  if (p.muted) return false
  return true
}

/** Merge an incoming notif into a list by id (update in place), newest first, bounded. */
export function mergeById(list, notif, max = MAX_ITEMS) {
  const idx = list.findIndex(n => n.id === notif.id)
  let next
  if (idx >= 0) {
    next = list.slice()
    next[idx] = { ...next[idx], ...notif }
  } else {
    next = [notif, ...list]
  }
  next.sort((a, b) => b.ts - a.ts)
  return next.slice(0, max)
}

/** Sanitise a persisted/loaded prefs object into the known shape. */
export function coercePrefs(raw) {
  if (!raw || typeof raw !== 'object') return { ...DEFAULT_PREFS, sources: {} }
  return {
    muted: !!raw.muted,
    sound: raw.sound !== false,
    sources: raw.sources && typeof raw.sources === 'object' ? { ...raw.sources } : {},
  }
}

// ── persistence (guarded) ────────────────────────────────────────────────────
function loadItems() {
  try {
    const raw = localStorage.getItem(LS_ITEMS)
    const arr = raw ? JSON.parse(raw) : []
    return Array.isArray(arr) ? arr.slice(0, MAX_ITEMS) : []
  } catch { return [] }
}

function persistItems() {
  try {
    // Persist only LOCAL items; remote ones are re-hydrated from the backend.
    const local = items.filter(n => n.origin === 'local')
    localStorage.setItem(LS_ITEMS, JSON.stringify(local.slice(0, MAX_ITEMS)))
  } catch { /* storage full / unavailable — ephemeral is fine */ }
}

function loadPrefs() {
  try { return coercePrefs(JSON.parse(localStorage.getItem(LS_PREFS))) }
  catch { return coercePrefs(null) }
}

function persistPrefs() {
  try { localStorage.setItem(LS_PREFS, JSON.stringify(prefs)) } catch { /* noop */ }
}

// ── emit ─────────────────────────────────────────────────────────────────────
function emitItems() { for (const fn of itemListeners) safe(fn) }
function emitToasts() { for (const fn of toastListeners) safe(fn) }
function emitPrefs() { for (const fn of prefsListeners) safe(fn) }
function safe(fn) { try { fn() } catch { /* a bad listener must not break dispatch */ } }

// ── core: post a notification ────────────────────────────────────────────────
/**
 * Post a notification. The single public producer API. Returns { id, dismiss }.
 *
 *   notify({ title: 'New mail', body: 'from Ada', source: 'mail', level: 'info',
 *            actions: [{ id: 'open', label: 'Open', route: { app: 'lilmail' } }] })
 */
export function notify(input) {
  if (!input || !input.title) return { id: null, dismiss: () => {} }
  const n = normalize(input)

  // Fully-off source → drop silently (honour the user's explicit choice).
  if (!shouldLog(prefs, n.source)) return { id: n.id, dismiss: () => {} }

  items = mergeById(items, n)
  if (n.origin === 'local') persistItems()
  emitItems()

  if (shouldToast(prefs, n.source, n.level) && !rateLimited(n.source, n.ts)) {
    lastToastAt.set(n.source, n.ts)
    toasts = [...toasts.filter(t => t.notif.id !== n.id), { key: `${n.id}:${n.ts}`, notif: n }].slice(-MAX_TOASTS)
    emitToasts()
    nativeAlert(n)
  }

  return { id: n.id, dismiss: () => dismiss(n.id) }
}

function rateLimited(source, now) {
  const last = lastToastAt.get(source)
  return last != null && (now - last) < RATE_MS
}

// In the Vulos Android app, ALSO surface anything that earned an in-shell
// toast as a native tray alert (the foreground service keeps the box reachable
// even when the app isn't in the foreground). Trivial 'info' toasts stay
// in-shell only — tasteful, not a mirror of every ping. tag = id so re-posting
// the same notification replaces its native alert instead of doubling it. A
// plain browser/PWA has no Notify bridge, so this is a total no-op there.
function nativeAlert(n) {
  if (!nativeBridge.notify.available || n.level === 'info') return
  nativeBridge.notify.alert({ title: n.title, text: n.body || n.title, tag: n.id }).catch(() => {})
}

/**
 * Fold a backend/system notification into the SAME store (origin 'remote').
 * Used by notificationBridge. Deduped by id; safe to call on re-hydration.
 */
export function ingest(remote) {
  return notify({ ...remote, origin: 'remote' })
}

// ── mutations ────────────────────────────────────────────────────────────────
export function markRead(id) {
  const item = items.find(n => n.id === id)
  if (!item || item.read) return
  items = items.map(n => (n.id === id ? { ...n, read: true } : n))
  persistItems(); emitItems()
  if (item.origin === 'remote') remoteSink?.markRead?.(id)
}

export function markAllRead() {
  const hadRemote = items.some(n => n.origin === 'remote' && !n.read)
  items = items.map(n => (n.read ? n : { ...n, read: true }))
  persistItems(); emitItems()
  if (hadRemote) remoteSink?.markAllRead?.()
}

/** Remove a single notification from the log (also drops any live toast for it). */
export function dismiss(id) {
  const item = items.find(n => n.id === id)
  items = items.filter(n => n.id !== id)
  toasts = toasts.filter(t => t.notif.id !== id)
  persistItems(); emitItems(); emitToasts()
  if (item?.origin === 'remote') remoteSink?.dismiss?.(id)
}

export function clear() {
  const hadRemote = items.some(n => n.origin === 'remote')
  items = []; toasts = []
  persistItems(); emitItems(); emitToasts()
  if (hadRemote) remoteSink?.clear?.()
}

export function dismissToast(key) {
  const before = toasts.length
  toasts = toasts.filter(t => t.key !== key)
  if (toasts.length !== before) emitToasts()
}

// ── prefs ────────────────────────────────────────────────────────────────────
export function getPrefs() { return prefs }

export function setPrefs(patch) {
  // `sources`, when provided, REPLACES the map wholesale (callers build the full
  // map) — merging would make it impossible to re-enable a source by deleting
  // its key. Other scalar fields patch normally.
  const sources = patch.sources !== undefined ? patch.sources : prefs.sources
  prefs = coercePrefs({ ...prefs, ...patch, sources })
  persistPrefs(); emitPrefs()
}

export function setMuted(v) { setPrefs({ muted: !!v }) }
export function setSound(v) { setPrefs({ sound: !!v }) }
export function setSourceEnabled(source, enabled) {
  const sources = { ...prefs.sources }
  if (enabled) delete sources[source] // absent = enabled (default)
  else sources[source] = false
  setPrefs({ sources })
}
export function isSourceEnabled(source) { return prefs.sources?.[source] !== false }

// ── selectors ────────────────────────────────────────────────────────────────
export function getItems() { return items }
export function getToasts() { return toasts }
export function getUnreadCount() { return items.reduce((n, x) => n + (x.read ? 0 : 1), 0) }

/** List of sources currently present in the feed (for per-source settings). */
export function getSources() {
  return [...new Set(items.map(n => n.source))]
}

// ── subscriptions ────────────────────────────────────────────────────────────
export function subscribe(fn) { itemListeners.add(fn); return () => itemListeners.delete(fn) }
export function subscribeToasts(fn) { toastListeners.add(fn); return () => toastListeners.delete(fn) }
export function subscribePrefs(fn) { prefsListeners.add(fn); return () => prefsListeners.delete(fn) }

// ── bridge wiring ────────────────────────────────────────────────────────────
/** Register the backend proxy for remote-item mutations. See notificationBridge. */
export function setRemoteSink(sink) { remoteSink = sink }

// Test-only reset so specs start from a clean, deterministic store.
export function __resetForTests() {
  items = []; toasts = []; prefs = coercePrefs(null)
  lastToastAt.clear()
  itemListeners.clear(); toastListeners.clear(); prefsListeners.clear()
  remoteSink = null
  try { localStorage.removeItem(LS_ITEMS); localStorage.removeItem(LS_PREFS) } catch { /* noop */ }
}
