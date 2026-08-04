/**
 * endpoints.ts — vulos-native cloud↔LAN endpoint selection with same-origin
 * fallback (OS OFFLINE-02 frozen contract).
 *
 * The OS owns its endpoint layer natively, with no sibling-package dependency,
 * so it builds and runs standalone. The contract:
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

// __VULOS_ENDPOINTS__ is injected ambiently by the shell/box HTML that serves
// this app (outside this repo) — this augmentation documents that existing
// runtime contract, it does not create it.
declare global {
  interface Window {
    __VULOS_ENDPOINTS__?: { cloud?: unknown; lan?: unknown }
  }
}

export interface EndpointPair {
  cloud: string
  lan: string
}

interface ConfigureOptions {
  lsKeyPrefix?: string
  healthPath?: string
  tierHint?: () => string
}

let lsKey = 'vulos.os.endpoints.v1'
let healthPath = '/api/auth/status'
let tierHintFn: () => string = () => 'free'

// cachedSelection: null = not yet selected; '' or a base URL once selected.
let cachedSelection: string | null = null
let onlineBound = false
const listeners = new Set<(sel: string) => void>()

function isRecord(x: unknown): x is Record<string, unknown> {
  return typeof x === 'object' && x !== null
}

function readPair(): EndpointPair {
  try {
    const raw = localStorage.getItem(lsKey)
    if (!raw) return { cloud: '', lan: '' }
    // Untrusted (previously-written, but still parsed JSON) storage read.
    const p: unknown = JSON.parse(raw)
    const r = isRecord(p) ? p : {}
    return { cloud: typeof r.cloud === 'string' ? r.cloud : '', lan: typeof r.lan === 'string' ? r.lan : '' }
  } catch {
    return { cloud: '', lan: '' }
  }
}

function writePair(pair: EndpointPair): void {
  try {
    localStorage.setItem(lsKey, JSON.stringify({ cloud: pair.cloud || '', lan: pair.lan || '' }))
  } catch {
    /* localStorage unavailable — selection still works, just not persisted */
  }
}

function notify(sel: string): void {
  for (const cb of listeners) {
    try {
      cb(sel)
    } catch {
      /* a listener must never break selection */
    }
  }
}

function bindOnline(): void {
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
export function configure(opts: ConfigureOptions = {}): void {
  if (opts.lsKeyPrefix) lsKey = opts.lsKeyPrefix
  if (opts.healthPath) healthPath = opts.healthPath
  if (typeof opts.tierHint === 'function') tierHintFn = opts.tierHint
  bindOnline()
}

/** currentTierHint returns the synchronous OS tier hint (default 'free'). */
export function currentTierHint(): string {
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
export function resolveEndpoints(): EndpointPair {
  if (typeof window !== 'undefined' && window.__VULOS_ENDPOINTS__) {
    const inj = window.__VULOS_ENDPOINTS__
    const pair: EndpointPair = {
      cloud: typeof inj.cloud === 'string' ? inj.cloud : '',
      lan: typeof inj.lan === 'string' ? inj.lan : '',
    }
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
 *
 * TRUST BOUNDARY: `payload` is network data (the control plane's response, or
 * a caller-supplied pair). Typed `unknown` and narrowed field-by-field rather
 * than cast to a BackendTarget interface. One deliberate hardening vs. the
 * original: a non-string `Endpoint`/`.cloud`/`.lan` now yields '' instead of
 * being stored verbatim (the original `src.Endpoint || ''` would have kept a
 * truthy non-string value, e.g. a stray number, breaking the "always string"
 * invariant every other function in this module relies on). No real caller
 * (Go's JSON encoder always emits a string here) exercises this path.
 */
export function seedFromResolveBackend(payload: unknown): EndpointPair {
  const src = isRecord(payload) ? payload : {}
  if (typeof src.Endpoint === 'string' || 'LANCandidate' in src) {
    const lanCandidate = isRecord(src.LANCandidate) ? src.LANCandidate : null
    const pair: EndpointPair = {
      cloud: typeof src.Endpoint === 'string' ? src.Endpoint : '',
      lan: lanCandidate && typeof lanCandidate.Endpoint === 'string' ? lanCandidate.Endpoint : '',
    }
    writePair(pair)
    cachedSelection = null
    return pair
  }
  const eRaw: unknown = src.endpoints || src.backend || src
  const e = isRecord(eRaw) ? eRaw : {}
  const pair: EndpointPair = {
    cloud: typeof e.cloud === 'string' ? e.cloud : '',
    lan: typeof e.lan === 'string' ? e.lan : '',
  }
  writePair(pair)
  // A freshly-seeded pair should be re-evaluated on the next selection.
  cachedSelection = null
  return pair
}

async function probe(base: string): Promise<boolean> {
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
export async function selectEndpoint(opts: { force?: boolean } = {}): Promise<string> {
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
export function currentEndpoint(): string {
  return cachedSelection || ''
}

/** invalidateEndpoint clears the cache so the next selectEndpoint re-probes. */
export function invalidateEndpoint(): void {
  cachedSelection = null
}

/** onEndpointChange subscribes to selection changes; returns an unsubscribe. */
export function onEndpointChange(cb: (sel: string) => void): () => void {
  listeners.add(cb)
  return () => listeners.delete(cb)
}

/* Test-only: clear cached endpoint selection and listener binding so each test
   starts from a clean slate. */
export function _resetForTests(): void {
  cachedSelection = null
  onlineBound = false
}
