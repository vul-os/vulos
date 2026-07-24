/**
 * endpoints.js — vulos-native cloud↔LAN endpoint selection with same-origin
 * fallback (OS OFFLINE-02 frozen contract).
 *
 * The OS owns its endpoint layer natively. (was `
 * endpoints`, a sibling package (`../vulos-relay/client`) that is not part of
 * the vulos repo. To let the OS build and run STANDALONE, the endpoint layer is
 * now owned natively here, preserving the exact contract the OS depended on:
 *
 *   • state persisted under a CONFIGURABLE localStorage key (the OS passes
 *     'vulos.os.endpoints.v1' — must NOT change, or every user's last-known-good
 *     cloud↔LAN pair is wiped on load);
 *   • selectEndpoint() probes LAN → cloud → same-origin (''), preferring
 *     LAN-direct for latency;
 *   • a probe that gets ANY HTTP response (incl. 401/403) counts the box as
 *     reachable — only a network throw means "down";
 *   • re-selects on the window 'online' event;
 *   • same-origin '' is always reachable when the OS is served from the box —
 *     which, with no relay, is the common case.
 *
 * import sites and tests are unchanged.
 */

let lsKey = 'vulos.os.endpoints.v1'
let healthPath = '/api/auth/status'
let tierHintFn = () => 'free'

// cachedSelection: null = not yet selected; '' or a base URL once selected.
let cachedSelection = null
let onlineBound = false
const listeners = new Set()

function readPair() {
  try {
    const raw = localStorage.getItem(lsKey)
    if (!raw) return { cloud: '', lan: '' }
    const p = JSON.parse(raw)
    return { cloud: p.cloud || '', lan: p.lan || '' }
  } catch {
    return { cloud: '', lan: '' }
  }
}

function writePair(pair) {
  try {
    localStorage.setItem(lsKey, JSON.stringify({ cloud: pair.cloud || '', lan: pair.lan || '' }))
  } catch {
    /* localStorage unavailable — selection still works, just not persisted */
  }
}

function notify(sel) {
  for (const cb of listeners) {
    try {
      cb(sel)
    } catch {
      /* a listener must never break selection */
    }
  }
}

function bindOnline() {
  if (onlineBound) return
  if (typeof window !== 'undefined' && typeof window.addEventListener === 'function') {
    window.addEventListener('online', () => {
      invalidateEndpoint()
      // Re-probe now that connectivity is back.
      selectEndpoint({ force: true })
    })
    onlineBound = true
  }
}

/** configure sets the persistence key, health-probe path, and tier hint. */
export function configure(opts = {}) {
  if (opts.lsKeyPrefix) lsKey = opts.lsKeyPrefix
  if (opts.healthPath) healthPath = opts.healthPath
  if (typeof opts.tierHint === 'function') tierHintFn = opts.tierHint
  bindOnline()
}

/** currentTierHint returns the synchronous OS tier hint (default 'free'). */
export function currentTierHint() {
  try {
    return String(tierHintFn() || 'free').toLowerCase()
  } catch {
    return 'free'
  }
}

/**
 * resolveEndpoints returns the { cloud, lan } pair, preferring a freshly
 * injected window.__VULOS_ENDPOINTS__ (which it persists) and otherwise the
 * last-known-good pair from localStorage (so a reload keeps failover targets).
 */
export function resolveEndpoints() {
  if (typeof window !== 'undefined' && window.__VULOS_ENDPOINTS__) {
    const inj = window.__VULOS_ENDPOINTS__
    const pair = { cloud: inj.cloud || '', lan: inj.lan || '' }
    writePair(pair)
    return pair
  }
  return readPair()
}

/**
 * seedFromResolveBackend persists a { cloud, lan } endpoint pair from a resolve
 * / bootstrap payload and returns it.
 *
 * Accepts either a lowercase { cloud, lan } pair directly (or nested under
 * `.endpoints` / `.backend`), or the BackendTarget shape returned by the
 * control plane's /api/resolve/backend endpoint:
 *   { Endpoint: '<remote base url>', LANCandidate: { BoxID, Endpoint } | null }
 */
export function seedFromResolveBackend(payload) {
  const src = payload || {}
  if (typeof src.Endpoint === 'string' || 'LANCandidate' in src) {
    const pair = {
      cloud: src.Endpoint || '',
      lan: (src.LANCandidate && src.LANCandidate.Endpoint) || '',
    }
    writePair(pair)
    cachedSelection = null
    return pair
  }
  const e = src.endpoints || src.backend || src
  const pair = { cloud: e.cloud || '', lan: e.lan || '' }
  writePair(pair)
  // A freshly-seeded pair should be re-evaluated on the next selection.
  cachedSelection = null
  return pair
}

async function probe(base) {
  // same-origin ('') needs no round-trip — it's the box serving this document.
  if (!base) return true
  try {
    // ANY resolved response (200, 401, 403, …) means the box answered → up.
    // Only a network throw (unreachable / DNS / abort) counts as down.
    await fetch(base + healthPath, { credentials: 'include' })
    return true
  } catch {
    return false
  }
}

/**
 * selectEndpoint returns the best base URL: LAN → cloud → same-origin ('').
 * Caches the result; pass { force: true } to re-probe.
 */
export async function selectEndpoint(opts = {}) {
  if (!opts.force && cachedSelection !== null) return cachedSelection
  const { cloud, lan } = resolveEndpoints()
  let sel = ''
  if (lan && (await probe(lan))) sel = lan
  else if (cloud && (await probe(cloud))) sel = cloud
  else sel = '' // same-origin fallback — always works from the box
  const changed = sel !== cachedSelection
  cachedSelection = sel
  if (changed) notify(sel)
  return sel
}

/** currentEndpoint returns the cached selection synchronously ('' if none). */
export function currentEndpoint() {
  return cachedSelection || ''
}

/** invalidateEndpoint clears the cache so the next selectEndpoint re-probes. */
export function invalidateEndpoint() {
  cachedSelection = null
}

/** onEndpointChange subscribes to selection changes; returns an unsubscribe. */
export function onEndpointChange(cb) {
  listeners.add(cb)
  return () => listeners.delete(cb)
}

/* Test-only: clear cached endpoint selection and listener binding so each test
   starts from a clean slate. */
export function _resetForTests() {
  cachedSelection = null
  onlineBound = false
}
