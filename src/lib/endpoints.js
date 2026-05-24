/**
 * endpoints.js — Vulos OS web client multi-endpoint failover (OFFLINE-02).
 *
 * Frozen contract — mirrored byte-for-byte by:
 *   • vulos-office: src/lib/endpoints.js (OFFICE-OFFLINE-01)
 *   • vulos-mail:   webmail-vulos/src/lib/endpoints.js (MAIL-OFFLINE-01)
 *
 * All three surfaces behave identically from the user's perspective:
 *
 *   • Cache BOTH a cloud endpoint and a LAN endpoint.
 *   • Health-check each candidate.
 *   • Prefer the reachable one — LAN-direct is preferred for latency
 *     (it works with the internet down and avoids a round-trip through
 *     the cloud control plane).
 *   • Cloud-routing failure → transparently fall back to the cached LAN
 *     endpoint (and vice-versa). No user action required.
 *
 * The selected endpoint is a *base URL* (origin + optional path prefix) that
 * API clients prepend to `/api/...` paths. When the OS is served from the
 * box itself the default same-origin endpoint is used and this layer is a
 * transparent no-op — meaningful failover only kicks in when the OS shell is
 * loaded from the cloud (cloud-routed install) and the box also has a
 * reachable LAN endpoint.
 *
 * Endpoint discovery (in priority order):
 *   1. window.__VULOS_ENDPOINTS__ injected by the OS shell at serve time:
 *        { cloud: "https://<box>.vulos.org",
 *          lan:   "https://box.<id>.lan.vulos.org" }
 *      (these are exactly the cloud + LAN endpoints returned by the cloud
 *      control plane's ResolveBackend — Endpoint + LANCandidate.Endpoint).
 *   2. Vite env: VITE_CLOUD_ENDPOINT / VITE_LAN_ENDPOINT (build-time).
 *   3. localStorage cache (last known-good endpoints), persisted across loads
 *      so failover keeps working with the internet — and the discovery cloud —
 *      down.
 *   4. Same-origin fallback ('') so a standalone OSS/self-host build still
 *      works without any of the above being configured.
 *
 * Also exposes seedFromResolveBackend(target) for callers that have just
 * received a fresh BackendTarget from the cloud's /api/resolve/backend
 * endpoint — the response is fed through the same persistence path so the
 * next page load already has both endpoints cached.
 *
 * Pure JS — no framework, no native deps.
 */

const LS_KEY = 'vulos.os.endpoints.v1'

// How long a health-probe may take before the endpoint is considered down.
const HEALTH_TIMEOUT_MS = 2_500

// Re-validate the selected endpoint at most this often (ms). A failed request
// always forces an immediate re-selection regardless of this interval.
const REVALIDATE_AFTER_MS = 30_000

// Relative health path appended to a base URL. /api/auth/status is a cheap,
// always-mounted endpoint on the OS/cloud backend.
const HEALTH_PATH = '/api/auth/status'

/** @typedef {{ cloud: string, lan: string }} EndpointPair */

let _state = {
  /** @type {EndpointPair} */
  pair: { cloud: '', lan: '' },
  /** Currently selected base URL ('' = same-origin). */
  selected: '',
  /** Timestamp (ms) of the last successful selection. */
  selectedAt: 0,
  /** In-flight selection promise (deduped). */
  selecting: null,
}

const listeners = new Set()

function emit() {
  for (const fn of listeners) {
    try { fn(_state.selected) } catch { /* listener errors are non-fatal */ }
  }
}

/** Subscribe to selected-endpoint changes. Returns an unsubscribe fn. */
export function onEndpointChange(fn) {
  listeners.add(fn)
  return () => listeners.delete(fn)
}

function readEnv(name) {
  try {
    return (import.meta && import.meta.env && import.meta.env[name]) || ''
  } catch {
    return ''
  }
}

function readInjected() {
  try {
    const g = typeof window !== 'undefined' ? window.__VULOS_ENDPOINTS__ : null
    if (g && typeof g === 'object') return { cloud: g.cloud || '', lan: g.lan || '' }
  } catch { /* ignore */ }
  return null
}

function readCache() {
  try {
    const raw = typeof localStorage !== 'undefined' && localStorage.getItem(LS_KEY)
    if (!raw) return null
    const v = JSON.parse(raw)
    if (v && typeof v === 'object') return { cloud: v.cloud || '', lan: v.lan || '' }
  } catch { /* ignore */ }
  return null
}

function writeCache(pair) {
  try {
    if (typeof localStorage !== 'undefined') {
      localStorage.setItem(LS_KEY, JSON.stringify({ cloud: pair.cloud, lan: pair.lan }))
    }
  } catch { /* storage may be unavailable (private mode) */ }
}

/**
 * Resolve the cloud + LAN endpoint pair from all sources, caching the result.
 * Injected/env values take priority; otherwise the last cached pair is reused
 * so failover survives a cloud-discovery outage.
 */
export function resolveEndpoints() {
  const injected = readInjected()
  const cached = readCache()

  const cloud =
    (injected && injected.cloud) ||
    readEnv('VITE_CLOUD_ENDPOINT') ||
    (cached && cached.cloud) ||
    ''
  const lan =
    (injected && injected.lan) ||
    readEnv('VITE_LAN_ENDPOINT') ||
    (cached && cached.lan) ||
    ''

  _state.pair = { cloud, lan }

  // Persist whatever we discovered so a later offline load still has both
  // endpoints to fail over between.
  if (cloud || lan) writeCache(_state.pair)
  return _state.pair
}

/**
 * Seed the endpoint pair from a freshly-fetched BackendTarget returned by the
 * cloud control plane's /api/resolve/backend endpoint.
 *
 *   target = {
 *     Endpoint:     "https://<box>.vulos.org",        // cloud-routed
 *     LANCandidate: { BoxID, Endpoint: "https://box.<id>.lan.vulos.org" } | null,
 *     // …other fields…
 *   }
 *
 * The LANCandidate field is a pointer + omitempty on the wire so legacy
 * clients see byte-identical JSON when no LAN candidate is advertised. Either
 * field may be absent; this function preserves whatever it already had cached
 * for the missing side and triggers an immediate re-selection so the live
 * pair is exercised at once.
 */
export function seedFromResolveBackend(target) {
  if (!target || typeof target !== 'object') return _state.pair
  const cached = readCache() || { cloud: '', lan: '' }
  const cloud = target.Endpoint || target.endpoint || cached.cloud || ''
  const lanCand = target.LANCandidate || target.lan_candidate || null
  const lan =
    (lanCand && (lanCand.Endpoint || lanCand.endpoint)) || cached.lan || ''
  _state.pair = { cloud, lan }
  if (cloud || lan) writeCache(_state.pair)
  // Force a re-probe — the freshly-seeded pair should be exercised now, not
  // after the cached selection's TTL.
  invalidateEndpoint()
  return _state.pair
}

/**
 * Health-check a single base URL. Resolves to true when the endpoint answers
 * within HEALTH_TIMEOUT_MS (any HTTP status counts as reachable — a 401/403 on
 * /auth/status still proves the box is up). Same-origin ('') is always usable.
 */
export async function probe(base) {
  // An empty base means same-origin: assume reachable if the document is
  // online, and trivially reachable when offline reads come from the SW cache.
  if (base === '') {
    return typeof navigator === 'undefined' || navigator.onLine !== false
  }
  const ctrl = typeof AbortController !== 'undefined' ? new AbortController() : null
  const timer = ctrl ? setTimeout(() => ctrl.abort(), HEALTH_TIMEOUT_MS) : null
  try {
    const res = await fetch(base + HEALTH_PATH, {
      method: 'GET',
      credentials: 'include',
      cache: 'no-store',
      signal: ctrl ? ctrl.signal : undefined,
    })
    // Any response — including 401/403 — means the endpoint is reachable.
    return !!res
  } catch {
    return false
  } finally {
    if (timer) clearTimeout(timer)
  }
}

/**
 * Select the best reachable endpoint.
 *
 * Preference order, per the OFFLINE-02 contract:
 *   1. LAN-direct (lowest latency, works with the internet down).
 *   2. Cloud.
 *   3. Same-origin fallback ('').
 *
 * The first candidate that passes a health-probe wins. Probing the preferred
 * candidates is done concurrently so a dead cloud route doesn't add latency to
 * picking the live LAN one.
 *
 * @param {{ force?: boolean }} [opts]
 * @returns {Promise<string>} the selected base URL
 */
export async function selectEndpoint(opts = {}) {
  const { force = false } = opts

  // Reuse a recent successful selection unless forced (e.g. after a failure).
  if (!force && _state.selected !== undefined &&
      _state.selectedAt && Date.now() - _state.selectedAt < REVALIDATE_AFTER_MS) {
    return _state.selected
  }
  // Dedupe concurrent callers.
  if (_state.selecting) return _state.selecting

  _state.selecting = (async () => {
    const { cloud, lan } = resolveEndpoints()

    // Candidate list, LAN preferred for latency, then cloud, then same-origin.
    const candidates = []
    if (lan) candidates.push(lan)
    if (cloud) candidates.push(cloud)
    candidates.push('') // same-origin fallback is always last and always present

    // Probe LAN and cloud concurrently; same-origin is resolved without a
    // network round-trip.
    const probed = await Promise.all(
      candidates.map(async (base) => ({ base, ok: await probe(base) }))
    )

    const winner = probed.find((c) => c.ok)
    const selected = winner ? winner.base : ''

    const changed = selected !== _state.selected
    _state.selected = selected
    _state.selectedAt = Date.now()
    _state.selecting = null
    if (changed) emit()
    return selected
  })()

  return _state.selecting
}

/** The currently selected base URL (synchronous; '' = same-origin). */
export function currentEndpoint() {
  return _state.selected
}

/**
 * Invalidate the current selection. Called by the API client when a request to
 * the selected endpoint fails so the next call re-probes and fails over.
 */
export function invalidateEndpoint() {
  _state.selectedAt = 0
}

// Re-select on connectivity changes so we fail over the moment the network
// state flips (cloud-down → LAN, LAN-down → cloud, offline → online).
if (typeof window !== 'undefined' && window.addEventListener) {
  const reselect = () => { selectEndpoint({ force: true }) }
  window.addEventListener('online', reselect)
  window.addEventListener('offline', reselect)
}
